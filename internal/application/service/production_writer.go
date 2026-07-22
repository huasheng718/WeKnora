package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/google/uuid"
)

const (
	productionWriterMaxEvidenceCount        = 128
	productionWriterMaxInlineEvidenceBytes  = 64 * 1024
	productionWriterMaxEvidenceBytes        = 512 * 1024
	productionWriterMaxCurrentVersionBlocks = 512
	productionWriterMaxCurrentVersionBytes  = 512 * 1024
	productionWriterMaxContextBytes         = 1024 * 1024
	productionWriterMaxRawResponseBytes     = 1024 * 1024
	productionWriterMaxOutputBlocks         = 256
	productionWriterMaxOutputBlockBytes     = 64 * 1024
	productionWriterMaxOutputBytes          = 512 * 1024
	productionWriterMaxCompletionTokens     = 8192
)

var (
	errProductionWriterConfiguration         = errors.New("production writer dependencies are required")
	errProductionWriterScope                 = errors.New("production writer input is outside the governed run scope")
	errProductionWriterOutput                = errors.New("production writer model output is invalid")
	errProductionWriterAuditConflict         = errors.New("production writer raw response audit lost its run fence")
	errProductionWriterEvidenceNormalization = errors.New("production writer requires inline normalized evidence")
	errProductionWriterInputLimit            = errors.New("production writer input exceeds size limit")
	errProductionWriterOutputLimit           = errors.New("production writer output exceeds size limit")
)

type ProductionWriterBlock struct {
	LogicalBlockID    string          `json:"logical_block_id" jsonschema:"required"`
	BlockType         string          `json:"block_type" jsonschema:"required"`
	Content           json.RawMessage `json:"content" jsonschema:"required"`
	EvidenceRefs      []string        `json:"evidence_refs" jsonschema:"required"`
	NeedsConfirmation bool            `json:"needs_confirmation" jsonschema:"required"`
}

type ProductionWriterOutput struct {
	Blocks []ProductionWriterBlock `json:"blocks" jsonschema:"required"`
}

type productionWriterBlockWire struct {
	LogicalBlockID    *string         `json:"logical_block_id"`
	BlockType         *string         `json:"block_type"`
	Content           json.RawMessage `json:"content"`
	EvidenceRefs      *[]string       `json:"evidence_refs"`
	NeedsConfirmation *bool           `json:"needs_confirmation"`
}

type productionWriterOutputWire struct {
	Blocks *[]productionWriterBlockWire `json:"blocks"`
}

type productionWriterDocumentTypeSnapshot struct {
	ID                 string
	Code               string
	SchemaVersion      int
	BlockSchema        types.ProductionBlockSchemaV1
	SourceRequirements types.ProductionSourceRequirementsV1
	SkillBindings      types.ProductionSkillBindingsV1
	WorkflowPlan       types.ProductionWorkflowPlanV1
	QualityRules       types.ProductionQualityRulesV1
	ReviewPolicy       types.ProductionReviewPolicyV1
	PublicationPolicy  types.ProductionPublicationPolicyV1
}

type productionWriterDocumentTypeSnapshotWire struct {
	ID                 string     `json:"id"`
	Code               string     `json:"code"`
	SchemaVersion      int        `json:"schema_version"`
	BlockSchema        types.JSON `json:"block_schema"`
	SourceRequirements types.JSON `json:"source_requirements"`
	SkillBindings      types.JSON `json:"skill_bindings"`
	WorkflowPlan       types.JSON `json:"workflow_plan"`
	QualityRules       types.JSON `json:"quality_rules"`
	ReviewPolicy       types.JSON `json:"review_policy"`
	PublicationPolicy  types.JSON `json:"publication_policy"`
}

type productionWriterPromptEvidence struct {
	ID            string                               `json:"id"`
	SnapshotType  types.ProductionEvidenceSnapshotType `json:"snapshot_type"`
	ContentDigest string                               `json:"content_digest"`
	Content       any                                  `json:"content,omitempty"`
}

type ProductionWriter struct {
	modelService interfaces.ModelService
	runs         interfaces.ProductionRunRepository
	sources      interfaces.ProductionSourceRepository
	documents    interfaces.ProductionDocumentRepository
	resources    interfaces.ResourceCatalog
	service      interfaces.ProductionDocumentService
}

func NewProductionWriter(
	modelService interfaces.ModelService,
	runs interfaces.ProductionRunRepository,
	sources interfaces.ProductionSourceRepository,
	documents interfaces.ProductionDocumentRepository,
	resources interfaces.ResourceCatalog,
	service interfaces.ProductionDocumentService,
) *ProductionWriter {
	return &ProductionWriter{
		modelService: modelService,
		runs:         runs,
		sources:      sources,
		documents:    documents,
		resources:    resources,
		service:      service,
	}
}

func (w *ProductionWriter) Write(
	ctx context.Context,
	run *types.ProductionRun,
) (*types.ProductionDocumentVersion, error) {
	if w == nil || w.modelService == nil || w.runs == nil || w.sources == nil || w.documents == nil || w.service == nil {
		return nil, errProductionWriterConfiguration
	}
	if run == nil || run.TenantID == 0 || run.ID == "" || run.ProjectID == "" || run.DocumentID == "" ||
		run.SourceSetID == "" || run.RunType != types.ProductionRunWrite || run.Status != types.ProductionRunRunning ||
		run.Attempt < 1 || run.CurrentStep < 0 || run.WakeupVersion < 1 || strings.TrimSpace(run.ModelID) == "" ||
		run.InputVersionID == nil || *run.InputVersionID == "" {
		return nil, errProductionWriterScope
	}

	principal, ok := types.ProductionInternalPrincipalFromContext(ctx)
	if !ok || !principal.Matches(run.TenantID, run.ProjectID, run.ID) {
		return nil, types.ErrProductionForbidden
	}
	governedCtx := ctx

	documentType, err := decodeProductionWriterDocumentType(run.DocumentTypeSnapshot)
	if err != nil {
		return nil, err
	}
	document, sourceSet, inputVersion, evidence, accepted, evidenceByID, err := w.loadContext(governedCtx, run, documentType)
	if err != nil {
		return nil, err
	}
	responseContent, rawDigest, audited, err := productionWriterAuditedResponse(run)
	if err != nil {
		return nil, err
	}
	if !audited {
		messages, messageErr := productionWriterMessages(run, documentType, document, sourceSet, inputVersion, evidence)
		if messageErr != nil {
			return nil, messageErr
		}
		chatModel, modelErr := w.modelService.GetChatModel(governedCtx, run.ModelID)
		if modelErr != nil {
			return nil, modelErr
		}
		if chatModel == nil {
			return nil, errProductionWriterConfiguration
		}
		response, chatErr := chatModel.Chat(governedCtx, messages, &chat.ChatOptions{
			Temperature: 0,
			MaxTokens:   productionWriterMaxCompletionTokens,
			Format:      utils.GenerateSchema[ProductionWriterOutput](),
		})
		if chatErr != nil {
			return nil, chatErr
		}
		if response == nil {
			return nil, fmt.Errorf("%w: model returned no response", errProductionWriterOutput)
		}
		rawSnapshot, marshalErr := json.Marshal(response.Content)
		if marshalErr != nil {
			return nil, marshalErr
		}
		canonicalRaw, canonicalErr := types.CanonicalProductionJSON(rawSnapshot)
		if canonicalErr != nil {
			return nil, canonicalErr
		}
		if len(canonicalRaw) > productionWriterMaxRawResponseBytes {
			return nil, errProductionWriterOutputLimit
		}
		rawSum := sha256.Sum256(canonicalRaw)
		rawDigest = hex.EncodeToString(rawSum[:])
		auditedRun, changed, transitionErr := w.runs.Transition(
			governedCtx, run.TenantID, run.ID, productionRunCAS(run), types.ProductionRunRunning,
			interfaces.ProductionRunPatch{RawModelResponse: canonicalRaw, RawModelResponseDigest: &rawDigest},
		)
		if transitionErr != nil {
			return nil, transitionErr
		}
		if !changed || auditedRun == nil {
			auditedRun, transitionErr = w.runs.Get(governedCtx, run.TenantID, run.ID)
			if transitionErr != nil {
				return nil, transitionErr
			}
		}
		responseContent, rawDigest, audited, transitionErr = productionWriterAuditedResponse(auditedRun)
		if transitionErr != nil || !audited {
			return nil, errProductionWriterAuditConflict
		}
	}

	output, err := decodeProductionWriterOutput(responseContent)
	if err != nil {
		return nil, err
	}
	inputs, err := productionWriterBlockInputs(output, accepted, run, rawDigest)
	if err != nil {
		return nil, err
	}
	if err := prevalidateProductionWriterCandidate(documentType.BlockSchema, documentType.QualityRules, inputs, evidenceByID); err != nil {
		return nil, err
	}

	return w.service.AppendVersion(governedCtx, string(run.DocumentID), interfaces.AppendProductionVersionInput{
		VersionID:       productionRunVersionID(run.ID),
		ParentVersionID: *run.InputVersionID,
		SourceSetID:     run.SourceSetID,
		Origin:          types.ProductionDocumentOriginAI,
		ChangeSummary:   "AI evidence-grounded production draft",
		Blocks:          inputs,
	})
}

func productionRunVersionID(runID string) string {
	return uuid.NewSHA1(uuid.MustParse("9bc0c43e-38f0-475a-aab3-a1d2c194c93f"), []byte(runID+":ai-version")).String()
}

func productionWriterAuditedResponse(run *types.ProductionRun) (string, string, bool, error) {
	if run == nil {
		return "", "", false, errProductionWriterAuditConflict
	}
	if len(run.RawModelResponse) == 0 && run.RawModelResponseDigest == nil {
		return "", "", false, nil
	}
	if len(run.RawModelResponse) == 0 || run.RawModelResponseDigest == nil {
		return "", "", false, errProductionWriterAuditConflict
	}
	if len(run.RawModelResponse) > productionWriterMaxRawResponseBytes {
		return "", "", false, errProductionWriterOutputLimit
	}
	canonical, err := types.CanonicalProductionJSON(run.RawModelResponse)
	if err != nil || !bytes.Equal(canonical, run.RawModelResponse) {
		return "", "", false, errProductionWriterAuditConflict
	}
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])
	if digest != *run.RawModelResponseDigest {
		return "", "", false, errProductionWriterAuditConflict
	}
	var content string
	if err := json.Unmarshal(canonical, &content); err != nil {
		return "", "", false, errProductionWriterAuditConflict
	}
	return content, digest, true, nil
}

func decodeProductionWriterDocumentType(raw types.JSON) (*productionWriterDocumentTypeSnapshot, error) {
	var wire productionWriterDocumentTypeSnapshotWire
	if err := decodeProductionJSON(raw, &wire, false); err != nil {
		return nil, fmt.Errorf("%w: invalid document type snapshot: %v", errProductionWriterScope, err)
	}
	if wire.ID == "" || wire.Code == "" || wire.SchemaVersion < 1 {
		return nil, fmt.Errorf("%w: incomplete document type snapshot", errProductionWriterScope)
	}
	input := types.ProductionDocumentTypeConfigInput{
		BlockSchema: wire.BlockSchema, SourceRequirements: wire.SourceRequirements,
		SkillBindings: wire.SkillBindings, WorkflowPlan: wire.WorkflowPlan,
		QualityRules: wire.QualityRules, ReviewPolicy: wire.ReviewPolicy,
		PublicationPolicy: wire.PublicationPolicy,
	}
	config, err := types.CanonicalProductionDocumentTypeConfig(input)
	if err != nil {
		if !isLegacyProductionDocumentTypeConfig(input) {
			return nil, fmt.Errorf("%w: invalid document type governance snapshot: %v", errProductionWriterScope, err)
		}
		var adapted bool
		config, adapted, err = canonicalLegacyProductionDocumentTypeConfig(wire.Code, input)
		if !adapted {
			return nil, fmt.Errorf("%w: invalid document type governance snapshot: %v", errProductionWriterScope, err)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: invalid legacy document type governance snapshot: %v", errProductionWriterScope, err)
		}
	}
	return &productionWriterDocumentTypeSnapshot{
		ID: wire.ID, Code: wire.Code, SchemaVersion: wire.SchemaVersion,
		BlockSchema: config.BlockSchema, SourceRequirements: config.SourceRequirements,
		SkillBindings: config.SkillBindings, WorkflowPlan: config.WorkflowPlan,
		QualityRules: config.QualityRules, ReviewPolicy: config.ReviewPolicy,
		PublicationPolicy: config.PublicationPolicy,
	}, nil
}

func productionDocumentTypeConfig(documentType *types.ProductionDocumentType) (types.ProductionDocumentTypeConfig, error) {
	if documentType == nil {
		return types.ProductionDocumentTypeConfig{}, types.ErrProductionDocumentTypeConfigInvalid
	}
	input := types.ProductionDocumentTypeConfigInput{
		BlockSchema: documentType.BlockSchema, SourceRequirements: documentType.SourceRequirements,
		SkillBindings: documentType.SkillBindings, WorkflowPlan: documentType.WorkflowPlan,
		QualityRules: documentType.QualityRules, ReviewPolicy: documentType.ReviewPolicy,
		PublicationPolicy: documentType.PublicationPolicy,
	}
	config, err := types.CanonicalProductionDocumentTypeConfig(input)
	if err == nil {
		return config, nil
	}
	if isLegacyProductionDocumentTypeConfig(input) {
		legacy, ok, legacyErr := canonicalLegacyProductionDocumentTypeConfig(documentType.Code, input)
		if ok {
			if legacyErr != nil {
				return types.ProductionDocumentTypeConfig{}, legacyErr
			}
			return legacy, nil
		}
	}
	return types.ProductionDocumentTypeConfig{}, err
}

func isLegacyProductionDocumentTypeConfig(input types.ProductionDocumentTypeConfigInput) bool {
	for _, raw := range []types.JSON{
		input.BlockSchema, input.SourceRequirements, input.QualityRules,
		input.ReviewPolicy, input.PublicationPolicy,
	} {
		if !isEmptyProductionJSONObject(raw) {
			return false
		}
	}
	return true
}

func isEmptyProductionJSONObject(raw types.JSON) bool {
	if len(raw) == 0 {
		return true
	}
	var object map[string]json.RawMessage
	if err := decodeProductionJSON(raw, &object, false); err != nil || object == nil {
		return false
	}
	return len(object) == 0
}

func canonicalLegacyProductionDocumentTypeConfig(
	code string,
	input types.ProductionDocumentTypeConfigInput,
) (types.ProductionDocumentTypeConfig, bool, error) {
	legacy, ok := legacyProductionDocumentTypeConfig(code)
	if !ok {
		return types.ProductionDocumentTypeConfig{}, false, nil
	}
	legacyInput, err := productionDocumentTypeConfigInput(legacy)
	if err != nil {
		return types.ProductionDocumentTypeConfig{}, true, err
	}
	if !isEmptyProductionJSONObject(input.SkillBindings) {
		legacyInput.SkillBindings = input.SkillBindings
	}
	if !isEmptyProductionJSONObject(input.WorkflowPlan) {
		legacyInput.WorkflowPlan = input.WorkflowPlan
	}
	config, err := types.CanonicalProductionDocumentTypeConfig(legacyInput)
	return config, true, err
}

func productionDocumentTypeConfigInput(
	config types.ProductionDocumentTypeConfig,
) (types.ProductionDocumentTypeConfigInput, error) {
	var input types.ProductionDocumentTypeConfigInput
	values := []struct {
		target *types.JSON
		value  any
	}{
		{target: &input.BlockSchema, value: config.BlockSchema},
		{target: &input.SourceRequirements, value: config.SourceRequirements},
		{target: &input.SkillBindings, value: config.SkillBindings},
		{target: &input.WorkflowPlan, value: config.WorkflowPlan},
		{target: &input.QualityRules, value: config.QualityRules},
		{target: &input.ReviewPolicy, value: config.ReviewPolicy},
		{target: &input.PublicationPolicy, value: config.PublicationPolicy},
	}
	for _, value := range values {
		encoded, err := canonicalProductionValue(value.value)
		if err != nil {
			return types.ProductionDocumentTypeConfigInput{}, err
		}
		*value.target = encoded
	}
	return input, nil
}

func legacyProductionDocumentTypeConfig(code string) (types.ProductionDocumentTypeConfig, bool) {
	var template ProductionBuiltinTemplate
	switch code {
	case "software-development-baseline":
		template = BuiltinSoftwareDevelopmentBaseline()
	case "project-retrospective":
		template = BuiltinProjectRetrospective()
	default:
		return types.ProductionDocumentTypeConfig{}, false
	}
	return types.ProductionDocumentTypeConfig{
		BlockSchema: types.ProductionBlockSchemaV1{
			Version: 1, RequiredSections: append([]string(nil), template.RequiredSections...),
			AllowedBlockTypes: []string{"heading", "paragraph", "code", "callout", "list", "table", "image"},
		},
		SourceRequirements: types.ProductionSourceRequirementsV1{
			Version: 1, MinAcceptedEvidence: 1,
			AllowedSourceKinds: []types.ProductionSourceKind{
				types.ProductionSourceKindUpload, types.ProductionSourceKindDatasource, types.ProductionSourceKindMCP,
				types.ProductionSourceKindSkill, types.ProductionSourceKindManual,
			},
			RequireEvidenceSection: true,
		},
		SkillBindings: types.ProductionSkillBindingsV1{Version: 1, Skills: []types.ProductionSkillBindingV1{}},
		WorkflowPlan:  types.ProductionWorkflowPlanV1{Version: 1, Steps: []types.ProductionWorkflowStepV1{}},
		QualityRules: types.ProductionQualityRulesV1{
			Version: 1, RequireEvidenceForFacts: true,
			Gates: []string{"section_completeness", "fact_evidence"},
		},
		ReviewPolicy: types.ProductionReviewPolicyV1{Steps: []types.ProductionRole{types.ProductionRoleBusinessReviewer}},
		PublicationPolicy: types.ProductionPublicationPolicyV1{
			Version: 1, TargetType: "knowledge_base", Chunking: "inherit_target",
			KnowledgeGraph: "inherit_target", RequireApprovedReview: true,
		},
	}, true
}

func (w *ProductionWriter) loadContext(
	ctx context.Context,
	run *types.ProductionRun,
	documentType *productionWriterDocumentTypeSnapshot,
) (
	*types.ProductionDocument,
	*types.ProductionSourceSet,
	*types.ProductionDocumentVersion,
	[]*types.ProductionEvidenceSnapshot,
	map[string]struct{},
	map[string]*types.ProductionEvidenceSnapshot,
	error,
) {
	document, err := w.documents.GetDocument(ctx, run.TenantID, string(run.DocumentID))
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	if document == nil || document.ID != string(run.DocumentID) || document.TenantID != run.TenantID ||
		document.ProjectID != run.ProjectID || document.DocumentTypeID != documentType.ID ||
		document.DocumentTypeSchemaVersion != documentType.SchemaVersion {
		return nil, nil, nil, nil, nil, nil, errProductionWriterScope
	}
	sourceSet, err := w.sources.GetSet(ctx, run.TenantID, run.SourceSetID)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	if sourceSet == nil || sourceSet.ID != run.SourceSetID || sourceSet.TenantID != run.TenantID ||
		sourceSet.ProjectID != run.ProjectID || sourceSet.DocumentTypeID != documentType.ID ||
		sourceSet.Status != types.ProductionSourceSetFrozen {
		return nil, nil, nil, nil, nil, nil, types.ErrProductionDocumentSourceSetInvalid
	}
	inputVersion, err := w.documents.GetVersion(ctx, run.TenantID, *run.InputVersionID)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	if inputVersion == nil || inputVersion.ID != *run.InputVersionID || inputVersion.TenantID != run.TenantID ||
		inputVersion.ProjectID != run.ProjectID || inputVersion.DocumentID != string(run.DocumentID) {
		return nil, nil, nil, nil, nil, nil, errProductionWriterScope
	}
	if len(inputVersion.Blocks) > productionWriterMaxCurrentVersionBlocks {
		return nil, nil, nil, nil, nil, nil, errProductionWriterInputLimit
	}
	encodedVersion, err := json.Marshal(inputVersion)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, errProductionWriterScope
	}
	if len(encodedVersion) > productionWriterMaxCurrentVersionBytes {
		return nil, nil, nil, nil, nil, nil, errProductionWriterInputLimit
	}
	evidence, err := w.sources.ListAcceptedEvidence(ctx, run.TenantID, run.ProjectID, run.SourceSetID)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	if len(evidence) > productionWriterMaxEvidenceCount {
		return nil, nil, nil, nil, nil, nil, errProductionWriterInputLimit
	}
	accepted := make(map[string]struct{}, len(evidence))
	evidenceByID := make(map[string]*types.ProductionEvidenceSnapshot, len(evidence))
	totalEvidenceBytes := 0
	for _, snapshot := range evidence {
		if snapshot == nil || snapshot.ID == "" {
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("%w: accepted evidence snapshot is invalid", errProductionWriterScope)
		}
		copy := *snapshot
		if copy.StoragePath != "" || len(copy.InlineContent) == 0 {
			return nil, nil, nil, nil, nil, nil, errProductionWriterEvidenceNormalization
		}
		if len(copy.InlineContent) > productionWriterMaxInlineEvidenceBytes {
			return nil, nil, nil, nil, nil, nil, errProductionWriterInputLimit
		}
		totalEvidenceBytes += len(copy.InlineContent)
		if totalEvidenceBytes > productionWriterMaxEvidenceBytes {
			return nil, nil, nil, nil, nil, nil, errProductionWriterInputLimit
		}
		if _, duplicate := accepted[copy.ID]; duplicate {
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("%w: duplicate accepted evidence id", errProductionWriterScope)
		}
		accepted[copy.ID] = struct{}{}
		evidenceByID[copy.ID] = &copy
	}
	return document, sourceSet, inputVersion, evidence, accepted, evidenceByID, nil
}

func productionWriterMessages(
	run *types.ProductionRun,
	documentType *productionWriterDocumentTypeSnapshot,
	document *types.ProductionDocument,
	sourceSet *types.ProductionSourceSet,
	inputVersion *types.ProductionDocumentVersion,
	evidence []*types.ProductionEvidenceSnapshot,
) ([]chat.Message, error) {
	promptEvidence := make([]productionWriterPromptEvidence, 0, len(evidence))
	for _, snapshot := range evidence {
		entry := productionWriterPromptEvidence{
			ID: snapshot.ID, SnapshotType: snapshot.SnapshotType, ContentDigest: snapshot.ContentDigest,
		}
		if len(snapshot.InlineContent) != 0 {
			var decoded any
			if err := decodeProductionJSON(snapshot.InlineContent, &decoded, false); err != nil {
				return nil, err
			}
			cleaned, _, err := redactProductionSecrets(decoded)
			if err != nil {
				return nil, err
			}
			entry.Content = cleaned
		}
		promptEvidence = append(promptEvidence, entry)
	}
	contextValue := map[string]any{
		"run": map[string]any{
			"id": run.ID, "project_id": run.ProjectID, "document_id": run.DocumentID,
			"source_set_id": run.SourceSetID, "input_version_id": *run.InputVersionID,
		},
		"document": map[string]any{
			"id": document.ID, "title": document.Title, "current_version_id": document.CurrentVersionID,
		},
		"document_type": map[string]any{
			"id": documentType.ID, "code": documentType.Code, "schema_version": documentType.SchemaVersion,
			"block_schema": documentType.BlockSchema, "skill_bindings": documentType.SkillBindings,
			"source_requirements": documentType.SourceRequirements, "workflow_plan": documentType.WorkflowPlan,
			"quality_rules": documentType.QualityRules, "review_policy": documentType.ReviewPolicy,
			"publication_policy": documentType.PublicationPolicy,
			"required_sections":  documentType.BlockSchema.RequiredSections, "quality_gates": documentType.QualityRules.Gates,
		},
		"source_set":                map[string]any{"id": sourceSet.ID, "status": sourceSet.Status},
		"accepted_evidence":         promptEvidence,
		"current_immutable_version": inputVersion,
	}
	cleaned, _, err := redactProductionSecrets(contextValue)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		return nil, err
	}
	if len(encoded) > productionWriterMaxContextBytes {
		return nil, errProductionWriterInputLimit
	}
	return []chat.Message{
		{
			Role: "system",
			Content: "Generate one JSON object matching the supplied schema. Use only the server-provided context. " +
				"Do not invent evidence identifiers or instructions. A factual claim must use block_type fact.",
		},
		{Role: "user", Content: string(encoded)},
	}, nil
}

func decodeProductionWriterOutput(raw string) (*ProductionWriterOutput, error) {
	if len(raw) > productionWriterMaxOutputBytes {
		return nil, errProductionWriterOutputLimit
	}
	var wire productionWriterOutputWire
	if err := decodeProductionJSON(types.JSON(raw), &wire, true); err != nil {
		return nil, fmt.Errorf("%w: %v", errProductionWriterOutput, err)
	}
	if wire.Blocks == nil || len(*wire.Blocks) == 0 {
		return nil, fmt.Errorf("%w: blocks are required", errProductionWriterOutput)
	}
	if len(*wire.Blocks) > productionWriterMaxOutputBlocks {
		return nil, errProductionWriterOutputLimit
	}
	output := &ProductionWriterOutput{Blocks: make([]ProductionWriterBlock, 0, len(*wire.Blocks))}
	for index, block := range *wire.Blocks {
		if block.LogicalBlockID == nil || block.BlockType == nil || len(block.Content) == 0 ||
			block.EvidenceRefs == nil || block.NeedsConfirmation == nil {
			return nil, fmt.Errorf("%w: block %d is missing required fields", errProductionWriterOutput, index)
		}
		if len(block.Content) > productionWriterMaxOutputBlockBytes {
			return nil, errProductionWriterOutputLimit
		}
		output.Blocks = append(output.Blocks, ProductionWriterBlock{
			LogicalBlockID: *block.LogicalBlockID, BlockType: *block.BlockType,
			Content: block.Content, EvidenceRefs: append([]string{}, (*block.EvidenceRefs)...),
			NeedsConfirmation: *block.NeedsConfirmation,
		})
	}
	return output, nil
}

func productionWriterBlockInputs(
	output *ProductionWriterOutput,
	accepted map[string]struct{},
	run *types.ProductionRun,
	rawDigest string,
) ([]types.ProductionDocumentBlockInput, error) {
	inputs := make([]types.ProductionDocumentBlockInput, 0, len(output.Blocks))
	for _, block := range output.Blocks {
		for _, ref := range block.EvidenceRefs {
			if _, ok := accepted[ref]; !ok {
				return nil, fmt.Errorf("%w: %s", types.ErrProductionEvidenceReferenceInvalid, ref)
			}
		}
		blockType := strings.TrimSpace(block.BlockType)
		content := types.JSON(block.Content)
		factual := blockType == "fact"
		if factual {
			var fact struct {
				Text string `json:"text"`
			}
			if err := decodeProductionJSON(content, &fact, true); err != nil || strings.TrimSpace(fact.Text) == "" {
				return nil, fmt.Errorf("%w: fact content must contain only non-empty text", errProductionWriterOutput)
			}
			content, _ = canonicalProductionValue(fact.Text)
			blockType = "paragraph"
		}
		needsConfirmation := block.NeedsConfirmation || (factual && len(block.EvidenceRefs) == 0)
		attributes, err := canonicalProductionValue(map[string]any{
			"factual": factual, "needs_confirmation": needsConfirmation,
		})
		if err != nil {
			return nil, err
		}
		refs, err := canonicalProductionValue(block.EvidenceRefs)
		if err != nil {
			return nil, err
		}
		provenance, err := canonicalProductionValue(map[string]any{
			"model_id": run.ModelID, "raw_response_digest": rawDigest, "run_id": run.ID,
		})
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, types.ProductionDocumentBlockInput{
			LogicalBlockID: strings.TrimSpace(block.LogicalBlockID), BlockType: blockType,
			Content: content, Attributes: attributes, EvidenceRefs: refs, AIProvenance: provenance,
		})
	}
	return inputs, nil
}

func prevalidateProductionWriterCandidate(
	blockSchema types.ProductionBlockSchemaV1,
	qualityRules types.ProductionQualityRulesV1,
	inputs []types.ProductionDocumentBlockInput,
	evidenceByID map[string]*types.ProductionEvidenceSnapshot,
) error {
	blocks, err := buildProductionDocumentBlocks(inputs)
	if err != nil {
		return err
	}
	version := &types.ProductionDocumentVersion{Origin: types.ProductionDocumentOriginAI, Blocks: blocks}
	version.ContentDigest = types.ComputeProductionVersionDigest(version)
	accepted := make(map[string]struct{}, len(evidenceByID))
	for id := range evidenceByID {
		accepted[id] = struct{}{}
	}
	validation := ValidateProductionVersion(version, accepted, blockSchema, qualityRules)
	if len(validation.Errors) != 0 {
		return &ProductionDocumentValidationError{Issues: validation.Errors}
	}
	if err := VerifyProductionVersionDigests(version, evidenceByID); err != nil {
		return &ProductionDocumentValidationError{Cause: err}
	}
	return nil
}
