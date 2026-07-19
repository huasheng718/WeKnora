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

	"github.com/Tencent/WeKnora/internal/agent/skills"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
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
	GetEvidence(ctx context.Context, tenantID uint64, evidenceID string) (*types.ProductionEvidenceSnapshot, *types.ProductionSourceItem, *types.ProductionSourceSet, error)
}

type productionToolEvidenceService interface {
	AttachEvidence(ctx context.Context, itemID string, evidence interfaces.CreateEvidenceSnapshotInput) (*types.ProductionEvidenceSnapshot, error)
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
	runtime  productionSkillRuntime
	catalog  productionSkillCatalog
	scope    productionToolScope
	evidence productionToolEvidenceService
}

func NewProductionSkillAdapter(
	runtime productionSkillRuntime,
	catalog productionSkillCatalog,
	runs productionToolRunResolver,
	sources productionToolSourceResolver,
	evidence productionToolEvidenceService,
) *ProductionSkillAdapter {
	return &ProductionSkillAdapter{
		runtime:  runtime,
		catalog:  catalog,
		scope:    productionToolScope{runs: runs, sources: sources},
		evidence: evidence,
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
	plan, _, _, err := a.plan(ctx, call, types.ProductionToolCallPlanned)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func (a *ProductionSkillAdapter) Execute(ctx context.Context, call *types.ProductionToolCall) (*ProductionToolResult, error) {
	plan, item, pinnedDigest, err := a.plan(ctx, call, types.ProductionToolCallExecuting)
	if err != nil {
		return nil, err
	}
	if result, found, err := a.persistedResult(ctx, call, plan, item, pinnedDigest); err != nil || found {
		return result, err
	}
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
	evidenceID := productionToolEvidenceID(call)
	persisted, err := a.evidence.AttachEvidence(ctx, prepared.item.ID, interfaces.CreateEvidenceSnapshotInput{
		EvidenceID: evidenceID, SnapshotType: types.ProductionEvidenceSnapshotToolResult,
		InlineContent: prepared.content, ContentDigest: productionToolDigest(prepared.content),
		RedactionMetadata: metadata, CapturedByRunID: call.RunID,
	})
	if err != nil {
		return nil, err
	}
	return productionToolResultFromEvidence(call, plan, persisted, prepared.digest)
}

func (a *ProductionSkillAdapter) plan(
	ctx context.Context,
	call *types.ProductionToolCall,
	status types.ProductionToolCallStatus,
) (*ProductionToolPlan, *types.ProductionSourceItem, string, error) {
	if a == nil || a.scope.runs == nil || a.scope.sources == nil {
		return nil, nil, "", errProductionProviderConfiguration
	}
	if err := validateProductionToolCall(call, types.ProductionToolProviderSkill, status, productionSkillToolName); err != nil {
		return nil, nil, "", err
	}
	var request productionSkillRequest
	if err := decodeProductionToolRequest(call, &request); err != nil || !canonicalProductionUUID(request.SourceItemID) {
		return nil, nil, "", errProductionToolCallInvalid
	}
	run, item, _, err := a.scope.resolve(ctx, call, request.SourceItemID, types.ProductionSourceKindSkill)
	if err != nil || item.ExternalID != call.ProviderID {
		return nil, nil, "", errProductionToolScope
	}
	pinnedDigest, err := pinnedProductionSkillDigest(run.DocumentTypeSnapshot, call.ProviderID)
	if err != nil {
		return nil, nil, "", err
	}
	plan, err := newProductionToolPlan(call, pinnedDigest)
	return plan, item, pinnedDigest, err
}

func (a *ProductionSkillAdapter) persistedResult(
	ctx context.Context,
	call *types.ProductionToolCall,
	plan *ProductionToolPlan,
	item *types.ProductionSourceItem,
	providerDigest string,
) (*ProductionToolResult, bool, error) {
	evidence, evidenceItem, evidenceSet, err := a.scope.sources.GetEvidence(ctx, call.TenantID, productionToolEvidenceID(call))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if evidenceItem == nil || evidenceSet == nil || evidenceItem.ID != item.ID || evidenceItem.SourceSetID != call.SourceSetID ||
		evidenceSet.ID != call.SourceSetID || evidenceSet.TenantID != call.TenantID || evidenceSet.ProjectID != call.ProjectID {
		return nil, false, types.ErrProductionEvidenceConflict
	}
	result, err := productionToolResultFromEvidence(call, plan, evidence, providerDigest)
	return result, err == nil, err
}

func (a *ProductionSkillAdapter) prepare(ctx context.Context, call *types.ProductionToolCall) (*productionSkillPrepared, error) {
	if a == nil || a.runtime == nil || a.catalog == nil || a.evidence == nil {
		return nil, errProductionProviderConfiguration
	}
	plan, item, pinnedDigest, err := a.plan(ctx, call, types.ProductionToolCallExecuting)
	if err != nil {
		return nil, err
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
	var root struct {
		SkillBindings json.RawMessage `json:"skill_bindings"`
	}
	if err := decodeProductionJSON(snapshot, &root, false); err != nil || len(root.SkillBindings) == 0 {
		return "", errProductionSkillUnbound
	}
	var bindings struct {
		Version int `json:"version"`
		Skills  []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"skills"`
	}
	if err := decodeProductionJSON(types.JSON(root.SkillBindings), &bindings, true); err != nil || bindings.Version != 1 {
		return "", errProductionSkillUnbound
	}
	found := ""
	seen := make(map[string]struct{}, len(bindings.Skills))
	for _, binding := range bindings.Skills {
		if binding.Name == "" || strings.TrimSpace(binding.Name) != binding.Name || !canonicalProductionSHA256(binding.Digest) {
			return "", errProductionSkillDigestMismatch
		}
		if _, duplicate := seen[binding.Name]; duplicate {
			return "", errProductionSkillDigestMismatch
		}
		seen[binding.Name] = struct{}{}
		if binding.Name == name {
			found = binding.Digest
		}
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

func validateProductionToolCall(
	call *types.ProductionToolCall,
	provider types.ProductionToolProviderType,
	status types.ProductionToolCallStatus,
	tools ...string,
) error {
	if call == nil || call.TenantID == 0 || call.Attempt < 1 || call.CurrentStep < 0 ||
		!canonicalProductionUUID(call.ID) || !canonicalProductionUUID(call.RunID) ||
		!canonicalProductionUUID(call.ProjectID) || !canonicalProductionUUID(call.DocumentID) ||
		!canonicalProductionUUID(call.SourceSetID) || call.ProviderType != provider || call.Status != status || strings.TrimSpace(call.ProviderID) == "" {
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
	evidenceID := productionToolEvidenceID(call)
	return &types.ProductionEvidenceSnapshot{
		ID: evidenceID, SourceItemID: sourceItemID, SnapshotType: types.ProductionEvidenceSnapshotToolResult,
		InlineContent: content, ContentDigest: productionToolDigest(content),
		RedactionMetadata: metadata, CapturedByRunID: call.RunID,
	}
}

func productionToolEvidenceID(call *types.ProductionToolCall) string {
	name := call.ID + ":" + string(call.ProviderType) + ":" + call.ProviderID + ":" + call.ToolName + ":" + strconv.Itoa(call.Attempt)
	return uuid.NewSHA1(uuid.MustParse("a148243e-c5b7-45a1-92f1-c410f317e7f4"), []byte(name)).String()
}

func productionToolResultFromEvidence(
	call *types.ProductionToolCall,
	plan *ProductionToolPlan,
	evidence *types.ProductionEvidenceSnapshot,
	providerDigest string,
) (*ProductionToolResult, error) {
	if evidence == nil || evidence.ID != productionToolEvidenceID(call) || evidence.SnapshotType != types.ProductionEvidenceSnapshotToolResult ||
		evidence.CapturedByRunID != call.RunID || !canonicalProductionSHA256(evidence.ContentDigest) ||
		productionToolDigest(evidence.InlineContent) != evidence.ContentDigest {
		return nil, types.ErrProductionEvidenceConflict
	}
	canonicalContent, err := types.CanonicalProductionJSON(evidence.InlineContent)
	if err != nil || !bytes.Equal(canonicalContent, evidence.InlineContent) {
		return nil, types.ErrProductionEvidenceConflict
	}
	canonicalMetadata, err := types.CanonicalProductionJSON(evidence.RedactionMetadata)
	if err != nil || !bytes.Equal(canonicalMetadata, evidence.RedactionMetadata) {
		return nil, types.ErrProductionEvidenceConflict
	}
	var metadata struct {
		ProviderDigest string `json:"provider_digest"`
	}
	if err := decodeProductionJSON(canonicalMetadata, &metadata, false); err != nil || !canonicalProductionSHA256(metadata.ProviderDigest) {
		return nil, types.ErrProductionEvidenceConflict
	}
	if providerDigest == "" {
		providerDigest = metadata.ProviderDigest
	} else if metadata.ProviderDigest != providerDigest {
		return nil, types.ErrProductionEvidenceConflict
	}
	response, err := canonicalProductionResponse(canonicalContent, providerDigest, canonicalMetadata, evidence)
	if err != nil {
		return nil, types.ErrProductionEvidenceConflict
	}
	return &ProductionToolResult{
		ToolCallID: call.ID, PlanDigest: plan.Digest, ResponseSnapshot: response,
		ResponseDigest: productionToolDigest(response), ProviderDigest: providerDigest,
		RedactionMetadata: canonicalMetadata, Evidence: evidence,
	}, nil
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

func productionSecretKey(key string) bool {
	return types.IsProductionCredentialKey(key)
}

func rejectProductionSecretFields(raw types.JSON) error {
	return types.RejectProductionCredentialFields(raw)
}

var _ ProductionToolAdapter = (*ProductionSkillAdapter)(nil)
