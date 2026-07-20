package service

import (
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
)

var (
	errProductionWriterConfiguration = errors.New("production writer dependencies are required")
	errProductionWriterScope         = errors.New("production writer input is outside the governed run scope")
	errProductionWriterOutput        = errors.New("production writer model output is invalid")
	errProductionWriterAuditConflict = errors.New("production writer raw response audit lost its run fence")
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
	ID            string          `json:"id"`
	Code          string          `json:"code"`
	SchemaVersion int             `json:"schema_version"`
	BlockSchema   json.RawMessage `json:"block_schema"`
	SkillBindings json.RawMessage `json:"skill_bindings"`
}

type productionWriterPromptEvidence struct {
	ID            string                               `json:"id"`
	SnapshotType  types.ProductionEvidenceSnapshotType `json:"snapshot_type"`
	ContentDigest string                               `json:"content_digest"`
	Content       any                                  `json:"content,omitempty"`
	Resource      string                               `json:"resource,omitempty"`
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
	messages, err := productionWriterMessages(run, documentType, document, sourceSet, inputVersion, evidence)
	if err != nil {
		return nil, err
	}

	chatModel, err := w.modelService.GetChatModel(governedCtx, run.ModelID)
	if err != nil {
		return nil, err
	}
	if chatModel == nil {
		return nil, errProductionWriterConfiguration
	}
	response, err := chatModel.Chat(governedCtx, messages, &chat.ChatOptions{
		Temperature: 0,
		Format:      utils.GenerateSchema[ProductionWriterOutput](),
	})
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("%w: model returned no response", errProductionWriterOutput)
	}

	rawSnapshot, err := json.Marshal(response.Content)
	if err != nil {
		return nil, err
	}
	canonicalRaw, err := types.CanonicalProductionJSON(rawSnapshot)
	if err != nil {
		return nil, err
	}
	rawSum := sha256.Sum256(canonicalRaw)
	rawDigest := hex.EncodeToString(rawSum[:])
	auditedRun, changed, err := w.runs.Transition(
		governedCtx,
		run.TenantID,
		run.ID,
		productionRunCAS(run),
		types.ProductionRunRunning,
		interfaces.ProductionRunPatch{RawModelResponse: canonicalRaw, RawModelResponseDigest: &rawDigest},
	)
	if err != nil {
		return nil, err
	}
	if !changed || auditedRun == nil || auditedRun.RawModelResponseDigest == nil || *auditedRun.RawModelResponseDigest != rawDigest {
		return nil, errProductionWriterAuditConflict
	}

	output, err := decodeProductionWriterOutput(response.Content)
	if err != nil {
		return nil, err
	}
	inputs, err := productionWriterBlockInputs(output, accepted, run, rawDigest)
	if err != nil {
		return nil, err
	}
	if err := prevalidateProductionWriterCandidate(documentType.Code, inputs, evidenceByID); err != nil {
		return nil, err
	}

	return w.service.AppendVersion(governedCtx, string(run.DocumentID), interfaces.AppendProductionVersionInput{
		ParentVersionID: *run.InputVersionID,
		SourceSetID:     run.SourceSetID,
		Origin:          types.ProductionDocumentOriginAI,
		ChangeSummary:   "AI evidence-grounded production draft",
		Blocks:          inputs,
	})
}

func decodeProductionWriterDocumentType(raw types.JSON) (*productionWriterDocumentTypeSnapshot, error) {
	var snapshot productionWriterDocumentTypeSnapshot
	if err := decodeProductionJSON(raw, &snapshot, false); err != nil {
		return nil, fmt.Errorf("%w: invalid document type snapshot: %v", errProductionWriterScope, err)
	}
	if snapshot.ID == "" || snapshot.Code == "" || snapshot.SchemaVersion < 1 {
		return nil, fmt.Errorf("%w: incomplete document type snapshot", errProductionWriterScope)
	}
	if _, known := BuiltinProductionTemplate(snapshot.Code); !known {
		return nil, fmt.Errorf("%w: unknown document type code", errProductionWriterScope)
	}
	return &snapshot, nil
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
	evidence, err := w.sources.ListAcceptedEvidence(ctx, run.TenantID, run.ProjectID, run.SourceSetID)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	accepted := make(map[string]struct{}, len(evidence))
	evidenceByID := make(map[string]*types.ProductionEvidenceSnapshot, len(evidence))
	for _, snapshot := range evidence {
		if snapshot == nil || snapshot.ID == "" {
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("%w: accepted evidence snapshot is invalid", errProductionWriterScope)
		}
		copy := *snapshot
		if copy.StoragePath != "" {
			if w.resources == nil {
				return nil, nil, nil, nil, nil, nil, fmt.Errorf("%w: resource catalog is required", errProductionWriterConfiguration)
			}
			resource, resolveErr := w.resources.ResolveBound(ctx, copy.StoragePath, interfaces.ResourceBindingRequirement{
				TenantID: run.TenantID, OwnerType: types.ResourceOwnerTypeProductionProject, OwnerID: run.ProjectID,
			})
			if resolveErr != nil {
				return nil, nil, nil, nil, nil, nil, resolveErr
			}
			if resource == nil || resource.TenantID != run.TenantID || resource.State != types.ResourceStateActive ||
				resource.Lifecycle != types.ResourceLifecyclePersistent {
				return nil, nil, nil, nil, nil, nil, types.ErrProductionEvidenceResourceInvalid
			}
			copy.ResolvedContentDigest = resource.ContentHash
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
		} else {
			entry.Resource = snapshot.StoragePath
		}
		promptEvidence = append(promptEvidence, entry)
	}
	template, _ := BuiltinProductionTemplate(documentType.Code)
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
			"required_sections": template.RequiredSections, "quality_gates": template.QualityGates,
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
	var wire productionWriterOutputWire
	if err := decodeProductionJSON(types.JSON(raw), &wire, true); err != nil {
		return nil, fmt.Errorf("%w: %v", errProductionWriterOutput, err)
	}
	if wire.Blocks == nil || len(*wire.Blocks) == 0 {
		return nil, fmt.Errorf("%w: blocks are required", errProductionWriterOutput)
	}
	output := &ProductionWriterOutput{Blocks: make([]ProductionWriterBlock, 0, len(*wire.Blocks))}
	for index, block := range *wire.Blocks {
		if block.LogicalBlockID == nil || block.BlockType == nil || len(block.Content) == 0 ||
			block.EvidenceRefs == nil || block.NeedsConfirmation == nil {
			return nil, fmt.Errorf("%w: block %d is missing required fields", errProductionWriterOutput, index)
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
	documentTypeCode string,
	inputs []types.ProductionDocumentBlockInput,
	evidenceByID map[string]*types.ProductionEvidenceSnapshot,
) error {
	blocks, err := buildProductionDocumentBlocks(inputs)
	if err != nil {
		return err
	}
	version := &types.ProductionDocumentVersion{
		Origin: types.ProductionDocumentOriginAI, Blocks: blocks, DocumentTypeCode: documentTypeCode,
	}
	version.ContentDigest = types.ComputeProductionVersionDigest(version)
	accepted := make(map[string]struct{}, len(evidenceByID))
	for id := range evidenceByID {
		accepted[id] = struct{}{}
	}
	validation := ValidateProductionVersion(version, accepted)
	if len(validation.Errors) != 0 {
		return &ProductionDocumentValidationError{Issues: validation.Errors}
	}
	if err := VerifyProductionVersionDigests(version, evidenceByID); err != nil {
		return &ProductionDocumentValidationError{Cause: err}
	}
	return nil
}
