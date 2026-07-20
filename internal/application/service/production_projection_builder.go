package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type ProductionProjectionBuilder struct {
	releases  interfaces.ProductionReleaseRepository
	documents interfaces.ProductionDocumentRepository
	reviews   interfaces.ProductionReviewRepository
	sources   interfaces.ProductionSourceRepository
	resources interfaces.ResourceCatalog
	kbs       interfaces.KnowledgeBaseService
	knowledge interfaces.KnowledgeService
	uow       interfaces.ProductionUnitOfWork
	audit     interfaces.AuditLogService
}

func NewProductionProjectionBuilder(
	releases interfaces.ProductionReleaseRepository,
	documents interfaces.ProductionDocumentRepository,
	reviews interfaces.ProductionReviewRepository,
	sources interfaces.ProductionSourceRepository,
	resources interfaces.ResourceCatalog,
	kbs interfaces.KnowledgeBaseService,
	knowledge interfaces.KnowledgeService,
	uow interfaces.ProductionUnitOfWork,
	audit interfaces.AuditLogService,
) *ProductionProjectionBuilder {
	return &ProductionProjectionBuilder{
		releases: releases, documents: documents, reviews: reviews, sources: sources,
		resources: resources, kbs: kbs, knowledge: knowledge, uow: uow, audit: audit,
	}
}

func (b *ProductionProjectionBuilder) Build(ctx context.Context, targetID string) (*types.Knowledge, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 || strings.TrimSpace(targetID) == "" {
		return nil, types.ErrProductionForbidden
	}
	if b == nil || b.releases == nil || b.documents == nil || b.reviews == nil ||
		b.sources == nil || b.kbs == nil || b.knowledge == nil || b.uow == nil || b.audit == nil {
		return nil, errors.New("production projection builder dependencies are unavailable")
	}

	target, err := b.releases.GetTarget(ctx, tenantID, targetID)
	if err != nil {
		return nil, err
	}
	if target == nil || target.ID != targetID || target.TenantID != tenantID {
		return nil, types.ErrProductionReleaseInvalid
	}
	if target.Status == types.ReleaseTargetFailed || target.Status == types.ReleaseTargetRolledBack {
		changed, transitionErr := b.releases.TransitionTarget(
			ctx, target.ID, target.Status, types.ReleaseTargetBuilding, nil,
		)
		if transitionErr != nil {
			return nil, transitionErr
		}
		if !changed {
			return nil, types.ErrProductionProjectionConflict
		}
		target.Status = types.ReleaseTargetBuilding
	}
	if target.Status != types.ReleaseTargetBuilding && target.Status != types.ReleaseTargetReady &&
		target.Status != types.ReleaseTargetActive {
		return nil, types.ErrProductionReleaseLifecycle
	}

	release, err := b.releases.GetRelease(ctx, tenantID, target.ReleaseID)
	if err != nil {
		return nil, err
	}
	document, err := b.documents.GetDocument(ctx, tenantID, target.DocumentID)
	if err != nil {
		return nil, err
	}
	version, err := b.documents.GetVersion(ctx, tenantID, target.VersionID)
	if err != nil {
		return nil, err
	}
	review, err := b.reviews.GetReview(ctx, tenantID, release.ReviewRequestID)
	if err != nil {
		return nil, err
	}
	if err := validateProductionProjectionScope(target, release, document, version, review); err != nil {
		return nil, err
	}
	blocking, err := b.reviews.CountOpenBlocking(ctx, tenantID, version.ID)
	if err != nil {
		return nil, err
	}
	if blocking != 0 {
		return nil, types.ErrProductionBlockingAnnotations
	}

	_, evidenceByID, err := loadProductionAcceptedEvidence(
		ctx, b.sources, b.resources, tenantID, target.ProjectID, version.SourceSetID,
	)
	if err != nil {
		return nil, err
	}
	if err := verifyProductionProjectionDigests(target, release, version, review, evidenceByID); err != nil {
		return nil, b.failDigestMismatch(ctx, target, err)
	}

	markdown, err := RenderProductionMarkdown(version)
	if err != nil {
		return nil, err
	}
	contentSum := sha256.Sum256([]byte(markdown))
	processOverrides, embeddingModelID, graphModelID, err := productionProjectionProcessSnapshot(target.ConfigSnapshot)
	if err != nil {
		return nil, err
	}
	kb, err := b.kbs.GetKnowledgeBaseByID(ctx, target.TargetKnowledgeBaseID)
	if err != nil {
		return nil, err
	}
	if kb == nil || kb.ID != target.TargetKnowledgeBaseID || kb.TenantID != tenantID ||
		kb.Type != types.KnowledgeBaseTypeDocument {
		return nil, types.ErrProductionForbidden
	}

	payload := &types.ProductionProjectionKnowledgePayload{
		KnowledgeID: target.KnowledgeID, KnowledgeBaseID: target.TargetKnowledgeBaseID,
		Title: document.Title, Content: markdown, EmbeddingModelID: embeddingModelID,
		GraphModelID:     graphModelID,
		ProcessOverrides: processOverrides,
		ProductionProjection: &types.ProductionProjectionMetadata{
			DocumentID: target.DocumentID, VersionID: target.VersionID,
			ReleaseTargetID: target.ID, ContentDigest: hex.EncodeToString(contentSum[:]),
			GraphModelID: graphModelID,
		},
	}
	if existing, loadErr := b.knowledge.GetKnowledgeByID(ctx, target.KnowledgeID); loadErr == nil {
		if err := validateProductionProjectionKnowledge(existing, payload); err != nil {
			return nil, err
		}
		if existing.ParseStatus == types.ParseStatusFailed && target.Status == types.ReleaseTargetBuilding {
			return b.knowledge.CreateKnowledgeFromProductionProjection(ctx, payload)
		}
		return existing, nil
	} else if !errors.Is(loadErr, apprepository.ErrKnowledgeNotFound) {
		return nil, loadErr
	}
	return b.knowledge.CreateKnowledgeFromProductionProjection(ctx, payload)
}

func validateProductionProjectionScope(
	target *types.ProductionReleaseTarget,
	release *types.ProductionRelease,
	document *types.ProductionDocument,
	version *types.ProductionDocumentVersion,
	review *types.ProductionReviewRequest,
) error {
	if target == nil || release == nil || document == nil || version == nil || review == nil {
		return types.ErrProductionReleaseInvalid
	}
	if release.ID != target.ReleaseID || release.TenantID != target.TenantID ||
		release.ProjectID != target.ProjectID || release.DocumentID != target.DocumentID ||
		release.VersionID != target.VersionID ||
		document.ID != target.DocumentID || document.TenantID != target.TenantID ||
		document.ProjectID != target.ProjectID || version.ID != target.VersionID ||
		version.DocumentID != target.DocumentID || version.TenantID != target.TenantID ||
		version.ProjectID != target.ProjectID || review.ID != release.ReviewRequestID ||
		review.TenantID != target.TenantID || review.ProjectID != target.ProjectID ||
		review.DocumentID != target.DocumentID || review.VersionID != target.VersionID ||
		review.Status != types.ProductionReviewApproved || version.FrozenAt == nil {
		return types.ErrProductionReviewScopeInvalid
	}
	for _, step := range review.Steps {
		if step == nil || step.ReviewRequestID != review.ID || step.TenantID != target.TenantID ||
			step.ProjectID != target.ProjectID || step.DocumentID != target.DocumentID ||
			step.VersionID != target.VersionID || step.Decision != types.ProductionReviewApproved {
			return types.ErrProductionReviewScopeInvalid
		}
	}
	return nil
}

func verifyProductionProjectionDigests(
	target *types.ProductionReleaseTarget,
	release *types.ProductionRelease,
	version *types.ProductionDocumentVersion,
	review *types.ProductionReviewRequest,
	evidenceByID map[string]*types.ProductionEvidenceSnapshot,
) error {
	canonicalConfig, configDigest, err := types.CanonicalProductionReleaseTargetConfig(target.ConfigSnapshot)
	if err != nil || configDigest != target.ConfigDigest || string(canonicalConfig) != string(target.ConfigSnapshot) {
		return errors.Join(types.ErrProductionContentDigestMismatch, errors.New("target configuration digest mismatch"))
	}
	_, policyDigest, err := types.CanonicalProductionReviewPolicy(review.PolicySnapshot)
	if err != nil || policyDigest != review.PolicyDigest {
		return errors.Join(types.ErrProductionContentDigestMismatch, errors.New("review policy digest mismatch"))
	}
	if err := VerifyProductionVersionDigests(version, evidenceByID); err != nil {
		return errors.Join(types.ErrProductionContentDigestMismatch, err)
	}
	if expected := types.ComputeProductionReleaseDigest(release, version, review); expected == "" || expected != release.ReleaseDigest || target.ReleaseDigest != expected {
		return errors.Join(types.ErrProductionContentDigestMismatch, errors.New("release digest mismatch"))
	}
	return nil
}

func (b *ProductionProjectionBuilder) failDigestMismatch(
	ctx context.Context,
	target *types.ProductionReleaseTarget,
	cause error,
) error {
	failure := errors.Join(types.ErrProductionContentDigestMismatch, cause)
	details, _ := json.Marshal(map[string]string{
		"failure_code": types.ProductionProjectionFailureContentDigestMismatch,
	})
	err := b.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		changed, transitionErr := b.releases.TransitionTarget(
			txCtx, target.ID, target.Status, types.ReleaseTargetFailed,
			types.JSONMap{
				"failure_code":   types.ProductionProjectionFailureContentDigestMismatch,
				"failure_reason": types.ProductionProjectionFailureReasonContentDigestMismatch,
			},
		)
		if transitionErr != nil {
			return transitionErr
		}
		if !changed {
			return types.ErrProductionProjectionConflict
		}
		actorID, _ := types.UserIDFromContext(txCtx)
		return emitRequiredProductionAudit(txCtx, b.audit, &types.AuditLog{
			TenantID: target.TenantID, ActorUserID: actorID, ActorRole: "system",
			Action:     types.AuditActionProductionContentDigestMismatch,
			TargetType: "production_release_target", TargetID: target.ID,
			Outcome: types.AuditOutcomeDenied, Details: types.JSON(details),
		})
	})
	if err != nil {
		return errors.Join(failure, err)
	}
	return failure
}

func validateProductionProjectionKnowledge(
	knowledge *types.Knowledge,
	payload *types.ProductionProjectionKnowledgePayload,
) error {
	if knowledge == nil || payload == nil || payload.ProductionProjection == nil ||
		knowledge.ID != payload.KnowledgeID || knowledge.KnowledgeBaseID != payload.KnowledgeBaseID ||
		knowledge.Type != types.KnowledgeTypeManual {
		return types.ErrProductionProjectionConflict
	}
	meta, err := knowledge.ManualMetadata()
	if err != nil || meta == nil || meta.ProductionProjection == nil ||
		*meta.ProductionProjection != *payload.ProductionProjection {
		return types.ErrProductionProjectionConflict
	}
	return nil
}

func productionProjectionProcessSnapshot(raw types.JSON) (*types.KnowledgeProcessOverrides, string, string, error) {
	var snapshot struct {
		EmbeddingModelID         string                           `json:"embedding_model_id"`
		Chunking                 *types.ChunkingConfig            `json:"chunking"`
		ChunkingConfig           *types.ChunkingConfig            `json:"chunking_config"`
		ParserEngineRules        []types.ParserEngineRule         `json:"parser_engine_rules"`
		EnableMultimodel         *bool                            `json:"enable_multimodel"`
		VLMConfig                *types.VLMConfig                 `json:"vlm_config"`
		ASRConfig                *types.ASRConfig                 `json:"asr_config"`
		QuestionGenerationConfig *types.QuestionGenerationConfig  `json:"question_generation_config"`
		ExtractConfig            *types.ExtractConfig             `json:"extract_config"`
		Graph                    json.RawMessage                  `json:"graph"`
		GraphEnabled             *bool                            `json:"graph_enabled"`
		ProcessOverrides         *types.KnowledgeProcessOverrides `json:"process_overrides"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, "", "", fmt.Errorf("%w: decode processing snapshot: %v", types.ErrProductionReleaseConfigInvalid, err)
	}
	normalized, err := json.Marshal(normalizeProductionProjectionJSONNumbers(decoded))
	if err != nil {
		return nil, "", "", fmt.Errorf("%w: normalize processing snapshot: %v", types.ErrProductionReleaseConfigInvalid, err)
	}
	if err := json.Unmarshal(normalized, &snapshot); err != nil {
		return nil, "", "", fmt.Errorf("%w: decode processing snapshot: %v", types.ErrProductionReleaseConfigInvalid, err)
	}
	overrides := snapshot.ProcessOverrides
	if overrides == nil {
		overrides = &types.KnowledgeProcessOverrides{}
	}
	chunking := snapshot.ChunkingConfig
	if chunking == nil {
		chunking = snapshot.Chunking
	}
	if chunking != nil {
		copyChunking := *chunking
		overrides.ChunkingConfig = &copyChunking
	}
	if len(snapshot.ParserEngineRules) != 0 {
		overrides.ParserEngineRules = append([]types.ParserEngineRule(nil), snapshot.ParserEngineRules...)
	}
	overrides.EnableMultimodel = snapshot.EnableMultimodel
	overrides.VLMConfig = snapshot.VLMConfig
	overrides.ASRConfig = snapshot.ASRConfig
	overrides.QuestionGenerationConfig = snapshot.QuestionGenerationConfig
	overrides.ExtractConfig = snapshot.ExtractConfig
	if snapshot.GraphEnabled != nil {
		value := *snapshot.GraphEnabled
		overrides.GraphEnabled = &value
	} else if len(snapshot.Graph) != 0 {
		var enabled bool
		if err := json.Unmarshal(snapshot.Graph, &enabled); err == nil {
			overrides.GraphEnabled = &enabled
		} else {
			var graph struct {
				Enabled       bool                 `json:"enabled"`
				ModelID       string               `json:"model_id"`
				ExtractConfig *types.ExtractConfig `json:"extract_config"`
			}
			if err := json.Unmarshal(snapshot.Graph, &graph); err != nil {
				return nil, "", "", fmt.Errorf("%w: decode graph snapshot: %v", types.ErrProductionReleaseConfigInvalid, err)
			}
			overrides.GraphEnabled = &graph.Enabled
			if graph.ExtractConfig != nil {
				overrides.ExtractConfig = graph.ExtractConfig
			}
			return overrides, strings.TrimSpace(snapshot.EmbeddingModelID), strings.TrimSpace(graph.ModelID), nil
		}
	}
	return overrides, strings.TrimSpace(snapshot.EmbeddingModelID), "", nil
}

func normalizeProductionProjectionJSONNumbers(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			typed[key] = normalizeProductionProjectionJSONNumbers(child)
		}
		return typed
	case []any:
		for index, child := range typed {
			typed[index] = normalizeProductionProjectionJSONNumbers(child)
		}
		return typed
	case json.Number:
		if integer, err := strconv.ParseInt(string(typed), 10, 64); err == nil {
			return integer
		}
		if decimal, err := strconv.ParseFloat(string(typed), 64); err == nil {
			if decimal >= math.MinInt64 && decimal <= math.MaxInt64 && math.Trunc(decimal) == decimal {
				return int64(decimal)
			}
			return decimal
		}
		return string(typed)
	default:
		return value
	}
}
