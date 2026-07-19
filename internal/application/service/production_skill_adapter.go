package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/Tencent/WeKnora/internal/agent/skills"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

const productionSkillToolName = "load_instructions"

var (
	errProductionToolCallInvalid       = errors.New("production tool call is invalid")
	errProductionToolScope             = errors.New("production tool call is outside its run scope")
	errProductionSkillNotPreloaded     = errors.New("production skill is not preloaded")
	errProductionSkillUnbound          = errors.New("production skill is not bound to the document type snapshot")
	errProductionSkillDigestMismatch   = errors.New("production skill digest does not match the document type snapshot")
	errProductionProviderExecution     = errors.New("production provider execution failed")
	errProductionProviderOutputUnsafe  = errors.New("production provider output is unsafe")
	errProductionProviderConfiguration = errors.New("production provider configuration is invalid")
)

// ProductionToolAdapter is the deterministic boundary between durable tool
// calls and provider-specific execution.
type ProductionToolAdapter interface {
	Plan(ctx context.Context, call *types.ProductionToolCall) (*ProductionToolPlan, error)
	Execute(ctx context.Context, call *types.ProductionToolCall) (*ProductionToolResult, error)
}

// ProductionToolPlan is a canonical, side-effect-free description of one
// durable invocation. Digest is SHA-256 over Canonical.
type ProductionToolPlan struct {
	ToolCallID     string
	ProviderType   types.ProductionToolProviderType
	ProviderID     string
	ToolName       string
	Canonical      types.JSON
	Digest         string
	ProviderDigest string
}

// ProductionToolResult contains only normalized provider output. Evidence has
// a deterministic ID, so persisting the same call attempt twice cannot create
// two evidence identities.
type ProductionToolResult struct {
	ToolCallID        string
	PlanDigest        string
	ResponseSnapshot  types.JSON
	ResponseDigest    string
	ProviderDigest    string
	RedactionMetadata types.JSON
	Evidence          *types.ProductionEvidenceSnapshot
}

type productionToolRunResolver interface {
	Get(ctx context.Context, tenantID uint64, runID string) (*types.ProductionRun, error)
}

type productionToolSourceResolver interface {
	GetItem(ctx context.Context, tenantID uint64, itemID string) (*types.ProductionSourceItem, *types.ProductionSourceSet, error)
}

type productionSkillRuntime interface {
	LoadSkill(ctx context.Context, skillName string) (*skills.Skill, error)
}

type productionSkillCatalog interface {
	ListPreloadedSkills(ctx context.Context) ([]*skills.SkillMetadata, error)
	GetSkillByName(ctx context.Context, name string) (*skills.Skill, error)
}

type productionToolScope struct {
	runs    productionToolRunResolver
	sources productionToolSourceResolver
}

// ProductionSkillAdapter exposes only preloaded, snapshot-bound Skill
// instructions. It never executes Skill scripts.
type ProductionSkillAdapter struct {
	runtime productionSkillRuntime
	catalog productionSkillCatalog
	scope   productionToolScope
}

func NewProductionSkillAdapter(
	runtime productionSkillRuntime,
	catalog productionSkillCatalog,
	runs productionToolRunResolver,
	sources productionToolSourceResolver,
) *ProductionSkillAdapter {
	return &ProductionSkillAdapter{
		runtime: runtime,
		catalog: catalog,
		scope:   productionToolScope{runs: runs, sources: sources},
	}
}

type productionSkillRequest struct {
	SourceItemID string `json:"source_item_id"`
}

type productionSkillPrepared struct {
	plan    *ProductionToolPlan
	content types.JSON
	digest  string
	item    *types.ProductionSourceItem
}

func (a *ProductionSkillAdapter) Plan(ctx context.Context, call *types.ProductionToolCall) (*ProductionToolPlan, error) {
	prepared, err := a.prepare(ctx, call)
	if err != nil {
		return nil, err
	}
	return prepared.plan, nil
}

func (a *ProductionSkillAdapter) Execute(ctx context.Context, call *types.ProductionToolCall) (*ProductionToolResult, error) {
	prepared, err := a.prepare(ctx, call)
	if err != nil {
		return nil, err
	}
	metadata, err := canonicalProductionValue(map[string]any{
		"adapter":         "skill",
		"provider_digest": prepared.digest,
		"redacted_fields": 0,
		"skill_digest":    prepared.digest,
	})
	if err != nil {
		return nil, errProductionProviderOutputUnsafe
	}
	evidence := newProductionToolEvidence(call, prepared.item.ID, prepared.content, metadata)
	response, err := canonicalProductionResponse(prepared.content, prepared.digest, metadata, evidence)
	if err != nil {
		return nil, errProductionProviderOutputUnsafe
	}
	return &ProductionToolResult{
		ToolCallID: call.ID, PlanDigest: prepared.plan.Digest,
		ResponseSnapshot: response, ResponseDigest: productionToolDigest(response),
		ProviderDigest: prepared.digest, RedactionMetadata: metadata, Evidence: evidence,
	}, nil
}

func (a *ProductionSkillAdapter) prepare(ctx context.Context, call *types.ProductionToolCall) (*productionSkillPrepared, error) {
	if a == nil || a.runtime == nil || a.catalog == nil {
		return nil, errProductionProviderConfiguration
	}
	if err := validateProductionToolCall(call, types.ProductionToolProviderSkill, productionSkillToolName); err != nil {
		return nil, err
	}
	var request productionSkillRequest
	if err := decodeProductionToolRequest(call, &request); err != nil || !canonicalProductionUUID(request.SourceItemID) {
		return nil, errProductionToolCallInvalid
	}
	run, item, _, err := a.scope.resolve(ctx, call, request.SourceItemID, types.ProductionSourceKindSkill)
	if err != nil || item.ExternalID != call.ProviderID {
		return nil, errProductionToolScope
	}
	preloaded, err := a.catalog.ListPreloadedSkills(ctx)
	if err != nil {
		return nil, errProductionSkillNotPreloaded
	}
	found := false
	for _, metadata := range preloaded {
		if metadata != nil && metadata.Name == call.ProviderID {
			found = true
			break
		}
	}
	if !found {
		return nil, errProductionSkillNotPreloaded
	}
	pinnedDigest, err := pinnedProductionSkillDigest(run.DocumentTypeSnapshot, call.ProviderID)
	if err != nil {
		return nil, err
	}
	catalogSkill, err := a.catalog.GetSkillByName(ctx, call.ProviderID)
	if err != nil || catalogSkill == nil || catalogSkill.Name != call.ProviderID {
		return nil, errProductionSkillNotPreloaded
	}
	catalogContent, catalogDigest, err := canonicalProductionSkill(catalogSkill)
	if err != nil || catalogDigest != pinnedDigest {
		return nil, errProductionSkillDigestMismatch
	}
	liveSkill, err := a.runtime.LoadSkill(ctx, call.ProviderID)
	if err != nil || liveSkill == nil || liveSkill.Name != call.ProviderID {
		return nil, errProductionSkillDigestMismatch
	}
	liveContent, liveDigest, err := canonicalProductionSkill(liveSkill)
	if err != nil || liveDigest != pinnedDigest || !bytes.Equal(liveContent, catalogContent) {
		return nil, errProductionSkillDigestMismatch
	}
	plan, err := newProductionToolPlan(call, liveDigest)
	if err != nil {
		return nil, errProductionToolCallInvalid
	}
	return &productionSkillPrepared{plan: plan, content: liveContent, digest: liveDigest, item: item}, nil
}

func canonicalProductionSkill(skill *skills.Skill) (types.JSON, string, error) {
	if skill == nil || !skill.Loaded || strings.TrimSpace(skill.Name) == "" || strings.TrimSpace(skill.Description) == "" {
		return nil, "", errProductionSkillDigestMismatch
	}
	content, err := canonicalProductionValue(map[string]any{
		"description":  skill.Description,
		"instructions": skill.Instructions,
		"name":         skill.Name,
	})
	if err != nil {
		return nil, "", err
	}
	return content, productionToolDigest(content), nil
}

func pinnedProductionSkillDigest(snapshot types.JSON, name string) (string, error) {
	var root map[string]any
	if err := decodeProductionJSON(snapshot, &root, false); err != nil {
		return "", errProductionSkillUnbound
	}
	bindings, ok := root["skill_bindings"]
	if !ok {
		return "", errProductionSkillUnbound
	}
	found := ""
	add := func(bindingName, digest string) error {
		if bindingName != name {
			return nil
		}
		if !canonicalProductionSHA256(digest) || found != "" {
			return errProductionSkillDigestMismatch
		}
		found = digest
		return nil
	}
	switch typed := bindings.(type) {
	case []any:
		for _, candidate := range typed {
			binding, ok := candidate.(map[string]any)
			if !ok {
				return "", errProductionSkillUnbound
			}
			bindingName, _ := binding["name"].(string)
			digest, _ := binding["instruction_digest"].(string)
			if err := add(bindingName, digest); err != nil {
				return "", err
			}
		}
	case map[string]any:
		candidate, ok := typed[name]
		if !ok {
			break
		}
		switch binding := candidate.(type) {
		case string:
			if err := add(name, binding); err != nil {
				return "", err
			}
		case map[string]any:
			digest, _ := binding["instruction_digest"].(string)
			if err := add(name, digest); err != nil {
				return "", err
			}
		default:
			return "", errProductionSkillUnbound
		}
	default:
		return "", errProductionSkillUnbound
	}
	if found == "" {
		return "", errProductionSkillUnbound
	}
	return found, nil
}

func (s productionToolScope) resolve(
	ctx context.Context,
	call *types.ProductionToolCall,
	sourceItemID string,
	kind types.ProductionSourceKind,
) (*types.ProductionRun, *types.ProductionSourceItem, *types.ProductionSourceSet, error) {
	if s.runs == nil || s.sources == nil {
		return nil, nil, nil, errProductionProviderConfiguration
	}
	run, err := s.runs.Get(ctx, call.TenantID, call.RunID)
	if err != nil || run == nil || run.ID != call.RunID || run.TenantID != call.TenantID ||
		run.ProjectID != call.ProjectID || run.DocumentID != call.DocumentID || run.SourceSetID != call.SourceSetID ||
		run.Attempt != call.Attempt || run.CurrentStep != call.CurrentStep {
		return nil, nil, nil, errProductionToolScope
	}
	item, set, err := s.sources.GetItem(ctx, call.TenantID, sourceItemID)
	if err != nil || item == nil || set == nil || item.ID != sourceItemID || item.SourceSetID != call.SourceSetID ||
		item.SourceKind != kind || set.ID != call.SourceSetID || set.TenantID != call.TenantID || set.ProjectID != call.ProjectID {
		return nil, nil, nil, errProductionToolScope
	}
	return run, item, set, nil
}

func validateProductionToolCall(call *types.ProductionToolCall, provider types.ProductionToolProviderType, tools ...string) error {
	if call == nil || call.TenantID == 0 || call.Attempt < 1 || call.CurrentStep < 0 ||
		!canonicalProductionUUID(call.ID) || !canonicalProductionUUID(call.RunID) ||
		!canonicalProductionUUID(call.ProjectID) || !canonicalProductionUUID(call.DocumentID) ||
		!canonicalProductionUUID(call.SourceSetID) || call.ProviderType != provider || strings.TrimSpace(call.ProviderID) == "" {
		return errProductionToolCallInvalid
	}
	validTool := false
	for _, tool := range tools {
		if call.ToolName == tool {
			validTool = true
			break
		}
	}
	if !validTool || len(call.RequestSnapshot) == 0 || !canonicalProductionSHA256(call.RequestDigest) {
		return errProductionToolCallInvalid
	}
	canonical, err := types.CanonicalProductionJSON(call.RequestSnapshot)
	if err != nil || !bytes.Equal(canonical, call.RequestSnapshot) || productionToolDigest(canonical) != call.RequestDigest {
		return errProductionToolCallInvalid
	}
	if err := rejectProductionSecretFields(canonical); err != nil {
		return errProductionToolCallInvalid
	}
	return nil
}

func decodeProductionToolRequest(call *types.ProductionToolCall, target any) error {
	return decodeProductionJSON(call.RequestSnapshot, target, true)
}

func decodeProductionJSON(raw types.JSON, target any, strict bool) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errProductionToolCallInvalid
		}
		return err
	}
	return nil
}

func newProductionToolPlan(call *types.ProductionToolCall, providerDigest string) (*ProductionToolPlan, error) {
	canonical, err := canonicalProductionValue(map[string]any{
		"attempt":         call.Attempt,
		"current_step":    call.CurrentStep,
		"document_id":     call.DocumentID,
		"project_id":      call.ProjectID,
		"provider_digest": providerDigest,
		"provider_id":     call.ProviderID,
		"provider_type":   call.ProviderType,
		"request_digest":  call.RequestDigest,
		"run_id":          call.RunID,
		"source_set_id":   call.SourceSetID,
		"tenant_id":       call.TenantID,
		"tool_call_id":    call.ID,
		"tool_name":       call.ToolName,
	})
	if err != nil {
		return nil, err
	}
	return &ProductionToolPlan{
		ToolCallID: call.ID, ProviderType: call.ProviderType, ProviderID: call.ProviderID,
		ToolName: call.ToolName, Canonical: canonical, Digest: productionToolDigest(canonical),
		ProviderDigest: providerDigest,
	}, nil
}

func newProductionToolEvidence(call *types.ProductionToolCall, sourceItemID string, content, metadata types.JSON) *types.ProductionEvidenceSnapshot {
	name := call.ID + ":" + string(call.ProviderType) + ":" + call.ProviderID + ":" + call.ToolName + ":" + strconv.Itoa(call.Attempt)
	evidenceID := uuid.NewSHA1(uuid.MustParse("a148243e-c5b7-45a1-92f1-c410f317e7f4"), []byte(name)).String()
	return &types.ProductionEvidenceSnapshot{
		ID: evidenceID, SourceItemID: sourceItemID, SnapshotType: types.ProductionEvidenceSnapshotToolResult,
		InlineContent: content, ContentDigest: productionToolDigest(content),
		RedactionMetadata: metadata, CapturedByRunID: call.RunID,
	}
}

func canonicalProductionResponse(content types.JSON, providerDigest string, metadata types.JSON, evidence *types.ProductionEvidenceSnapshot) (types.JSON, error) {
	var contentValue, metadataValue any
	if err := decodeProductionJSON(content, &contentValue, false); err != nil {
		return nil, err
	}
	if err := decodeProductionJSON(metadata, &metadataValue, false); err != nil {
		return nil, err
	}
	return canonicalProductionValue(map[string]any{
		"content":                 contentValue,
		"content_digest":          evidence.ContentDigest,
		"evidence_id":             evidence.ID,
		"evidence_source_item_id": evidence.SourceItemID,
		"provider_digest":         providerDigest,
		"redaction_metadata":      metadataValue,
	})
}

func canonicalProductionValue(value any) (types.JSON, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return types.CanonicalProductionJSON(raw)
}

func productionToolDigest(raw types.JSON) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func canonicalProductionUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func canonicalProductionSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func normalizedProductionSecretKey(key string) string {
	var normalized strings.Builder
	for _, r := range strings.ToLower(key) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			normalized.WriteRune(r)
		}
	}
	return normalized.String()
}

func productionSecretKey(key string) bool {
	switch normalizedProductionSecretKey(key) {
	case "token", "accesstoken", "refreshtoken", "clientsecret", "apikey", "authorization",
		"password", "secret", "privatekey", "credential", "credentials":
		return true
	default:
		return false
	}
}

func rejectProductionSecretFields(raw types.JSON) error {
	var value any
	if err := decodeProductionJSON(raw, &value, false); err != nil {
		return err
	}
	return walkProductionValue(value, func(key string, _ any) error {
		if productionSecretKey(key) {
			return errProductionProviderOutputUnsafe
		}
		return nil
	})
}

func walkProductionValue(value any, visit func(string, any) error) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if err := visit(key, nested); err != nil {
				return err
			}
			if err := walkProductionValue(nested, visit); err != nil {
				return err
			}
		}
	case []any:
		for _, nested := range typed {
			if err := walkProductionValue(nested, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

var _ ProductionToolAdapter = (*ProductionSkillAdapter)(nil)
