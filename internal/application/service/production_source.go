package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type productionSourceService struct {
	repo      interfaces.ProductionSourceRepository
	projects  interfaces.ProductionProjectAuthorizer
	resources interfaces.ResourceCatalog
	audit     interfaces.AuditLogService
	uow       interfaces.ProductionUnitOfWork
}

func NewProductionSourceService(
	repo interfaces.ProductionSourceRepository,
	projects interfaces.ProductionProjectAuthorizer,
	resources interfaces.ResourceCatalog,
	audit interfaces.AuditLogService,
	uow interfaces.ProductionUnitOfWork,
) *productionSourceService {
	return &productionSourceService{repo: repo, projects: projects, resources: resources, audit: audit, uow: uow}
}

func canonicalProductionSourceID(value, name string, rejectNonCanonical bool) (string, error) {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return "", fmt.Errorf("%s must be a UUID: %w", name, err)
	}
	canonical := parsed.String()
	if rejectNonCanonical && value != canonical {
		return "", fmt.Errorf("%s must be a canonical UUID", name)
	}
	return canonical, nil
}

func requireProductionSourceID(value, name string) error {
	_, err := canonicalProductionSourceID(value, name, true)
	return err
}

func requireProductionSourceAuthor(ctx context.Context, projects interfaces.ProductionProjectAuthorizer, projectID string) error {
	if principal, ok := types.ProductionInternalPrincipalFromContext(ctx); ok {
		tenantID, tenantOK := types.TenantIDFromContext(ctx)
		if tenantOK && principal.Matches(tenantID, projectID, principal.RunID) {
			return nil
		}
		return types.ErrProductionForbidden
	}
	if projects == nil {
		return types.ErrProductionForbidden
	}
	return projects.RequireProjectRole(ctx, projectID, types.ProductionRoleProjectOwner, types.ProductionRoleAuthor)
}

func normalizedSHA256(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	decoded, err := hex.DecodeString(value)
	return value, err == nil && len(decoded) == sha256.Size
}

func canonicalProductionJSON(value types.JSON, defaultValue string) (types.JSON, error) {
	if len(value) == 0 {
		value = types.JSON(defaultValue)
	}
	return types.CanonicalProductionJSON(value)
}

func (s *productionSourceService) ListSets(
	ctx context.Context,
	projectID string,
) ([]*types.ProductionSourceSet, error) {
	tenantID, _, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireProductionSourceID(projectID, "project id"); err != nil {
		return nil, err
	}
	if s.projects == nil {
		return nil, types.ErrProductionForbidden
	}
	if err := s.projects.RequireProjectRole(ctx, projectID, allProductionProjectRoles...); err != nil {
		return nil, err
	}
	return s.repo.ListSets(ctx, tenantID, projectID)
}

func (s *productionSourceService) ListEvidence(
	ctx context.Context,
	sourceSetID string,
) ([]*types.ProductionEvidenceSnapshot, error) {
	tenantID, _, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireProductionSourceID(sourceSetID, "source set id"); err != nil {
		return nil, err
	}
	sourceSet, err := s.repo.GetSet(ctx, tenantID, sourceSetID)
	if err != nil {
		return nil, err
	}
	if sourceSet == nil || sourceSet.ID != sourceSetID || sourceSet.TenantID != tenantID {
		return nil, types.ErrProductionForbidden
	}
	if err := requireProductionDocumentReader(ctx, s.projects, sourceSet.ProjectID); err != nil {
		return nil, err
	}
	return s.repo.ListAcceptedEvidence(ctx, tenantID, sourceSet.ProjectID, sourceSetID)
}

func (s *productionSourceService) CreateSet(
	ctx context.Context,
	input interfaces.CreateProductionSourceSetInput,
) (*types.ProductionSourceSet, error) {
	tenantID, userID, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireProductionSourceID(input.ProjectID, "project id"); err != nil {
		return nil, err
	}
	if err := requireProductionSourceID(input.DocumentTypeID, "document type id"); err != nil {
		return nil, err
	}
	if input.TimeRangeStart != nil && input.TimeRangeEnd != nil && input.TimeRangeStart.After(*input.TimeRangeEnd) {
		return nil, errors.New("production source time range start must not be after end")
	}
	if err := requireProductionSourceAuthor(ctx, s.projects, input.ProjectID); err != nil {
		return nil, err
	}
	sourceSet := &types.ProductionSourceSet{
		ID: uuid.NewString(), TenantID: tenantID, ProjectID: input.ProjectID, DocumentTypeID: input.DocumentTypeID,
		TimeRangeStart: input.TimeRangeStart, TimeRangeEnd: input.TimeRangeEnd,
		Status: types.ProductionSourceSetCollecting, CreatedBy: userID,
	}
	if err := s.repo.CreateSet(ctx, sourceSet); err != nil {
		return nil, err
	}
	return sourceSet, nil
}

func (s *productionSourceService) AddItem(
	ctx context.Context,
	sourceSetID string,
	input interfaces.CreateProductionSourceItemInput,
) (*types.ProductionSourceItem, error) {
	tenantID, _, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireProductionSourceID(sourceSetID, "source set id"); err != nil {
		return nil, err
	}
	sourceSet, err := s.repo.GetSet(ctx, tenantID, sourceSetID)
	if err != nil {
		return nil, err
	}
	if err := requireProductionSourceAuthor(ctx, s.projects, sourceSet.ProjectID); err != nil {
		return nil, err
	}
	if !input.SourceKind.IsValid() || strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.MimeType) == "" {
		return nil, errors.New("production source item requires kind, title, and MIME type")
	}
	digest, ok := normalizedSHA256(input.ContentDigest)
	if !ok {
		return nil, errors.New("production source item content digest must be SHA-256")
	}
	metadata, err := canonicalProductionJSON(input.Metadata, `{}`)
	if err != nil {
		return nil, fmt.Errorf("invalid production source metadata: %w", err)
	}
	capturedAt := input.CapturedAt
	if capturedAt.IsZero() {
		capturedAt = time.Now().UTC()
	}
	item := &types.ProductionSourceItem{
		ID: uuid.NewString(), SourceSetID: sourceSetID, SourceKind: input.SourceKind,
		SourceSystem: input.SourceSystem, ExternalID: input.ExternalID, SourceURI: input.SourceURI,
		Title: input.Title, MimeType: input.MimeType, ContentDigest: digest,
		CapturedAt: capturedAt, Metadata: metadata, Status: types.ProductionSourceItemCandidate,
	}
	if err := s.repo.CreateItem(ctx, tenantID, sourceSetID, item); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *productionSourceService) DecideItem(
	ctx context.Context,
	itemID string,
	decision types.ProductionSourceItemStatus,
) error {
	tenantID, _, err := productionCaller(ctx)
	if err != nil {
		return err
	}
	if err := requireProductionSourceID(itemID, "source item id"); err != nil {
		return err
	}
	if !decision.IsDecision() {
		return errors.New("production source decision must be accepted, rejected, or unavailable")
	}
	_, sourceSet, err := s.repo.GetItem(ctx, tenantID, itemID)
	if err != nil {
		return err
	}
	if err := requireProductionSourceAuthor(ctx, s.projects, sourceSet.ProjectID); err != nil {
		return err
	}
	return s.repo.DecideItem(ctx, tenantID, itemID, decision)
}

func (s *productionSourceService) AttachEvidence(
	ctx context.Context,
	itemID string,
	input interfaces.CreateEvidenceSnapshotInput,
) (*types.ProductionEvidenceSnapshot, error) {
	tenantID, _, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireProductionSourceID(itemID, "source item id"); err != nil {
		return nil, err
	}
	_, sourceSet, err := s.repo.GetItem(ctx, tenantID, itemID)
	if err != nil {
		return nil, err
	}
	if err := requireProductionSourceAuthor(ctx, s.projects, sourceSet.ProjectID); err != nil {
		return nil, err
	}
	if !input.SnapshotType.IsValid() {
		return nil, errors.New("invalid production evidence snapshot type")
	}
	runID := ""
	if input.CapturedByRunID != "" {
		runID, err = canonicalProductionSourceID(input.CapturedByRunID, "run id", false)
		if err != nil {
			return nil, err
		}
	}
	toolCallID := ""
	if input.CapturedByToolCallID != "" {
		toolCallID, err = canonicalProductionSourceID(input.CapturedByToolCallID, "tool call id", false)
		if err != nil {
			return nil, err
		}
	}
	principal, internal := types.ProductionInternalPrincipalFromContext(ctx)
	if sourceSet.Status == types.ProductionSourceSetFrozen {
		if runID == "" || toolCallID == "" || input.EvidenceID == "" ||
			input.SnapshotType != types.ProductionEvidenceSnapshotToolResult {
			return nil, types.ErrProductionSourceSetFrozen
		}
		if !internal || !principal.Matches(tenantID, sourceSet.ProjectID, runID) {
			return nil, types.ErrProductionForbidden
		}
	} else if internal && !principal.Matches(tenantID, sourceSet.ProjectID, runID) {
		return nil, types.ErrProductionForbidden
	}
	if (input.ResourceReference == "") == (len(input.InlineContent) == 0) {
		return nil, errors.New("production evidence requires exactly one resource reference or inline content")
	}

	redaction, err := canonicalProductionJSON(input.RedactionMetadata, `{}`)
	if err != nil {
		return nil, fmt.Errorf("invalid production evidence redaction metadata: %w", err)
	}
	evidenceID := input.EvidenceID
	if evidenceID == "" {
		evidenceID = uuid.NewString()
	} else if err := requireProductionSourceID(evidenceID, "evidence id"); err != nil {
		return nil, err
	}
	snapshot := &types.ProductionEvidenceSnapshot{
		ID: evidenceID, SourceItemID: itemID, SnapshotType: input.SnapshotType,
		RedactionMetadata: redaction, CapturedByRunID: runID, CapturedByToolCallID: toolCallID,
	}
	if input.ResourceReference != "" {
		handle, ok := types.ParseResourcePath(input.ResourceReference)
		if !ok || s.resources == nil {
			return nil, types.ErrProductionEvidenceResourceInvalid
		}
		canonicalReference := types.BuildResourcePath(handle)
		resource, resolveErr := s.resources.ResolveBound(ctx, canonicalReference, interfaces.ResourceBindingRequirement{
			TenantID: tenantID, OwnerType: types.ResourceOwnerTypeProductionProject, OwnerID: sourceSet.ProjectID,
		})
		if resolveErr != nil {
			if errors.Is(resolveErr, types.ErrResourceBindingNotFound) {
				return nil, errors.Join(types.ErrProductionForbidden, resolveErr)
			}
			return nil, resolveErr
		}
		if resource == nil {
			return nil, types.ErrProductionEvidenceResourceInvalid
		}
		if resource.TenantID != tenantID {
			return nil, types.ErrProductionForbidden
		}
		if resource.Lifecycle != types.ResourceLifecyclePersistent || resource.State != types.ResourceStateActive {
			return nil, types.ErrProductionEvidenceResourceInvalid
		}
		digest, ok := normalizedSHA256(resource.ContentHash)
		if !ok {
			return nil, types.ErrProductionEvidenceResourceInvalid
		}
		if input.ContentDigest != "" && !strings.EqualFold(strings.TrimSpace(input.ContentDigest), digest) {
			return nil, types.ErrProductionEvidenceDigestMismatch
		}
		snapshot.StoragePath = canonicalReference
		snapshot.ContentDigest = digest
	} else {
		canonical, canonicalErr := canonicalProductionJSON(input.InlineContent, "")
		if canonicalErr != nil {
			return nil, fmt.Errorf("invalid production inline evidence: %w", canonicalErr)
		}
		sum := sha256.Sum256(canonical)
		digest := hex.EncodeToString(sum[:])
		if input.ContentDigest != "" && !strings.EqualFold(strings.TrimSpace(input.ContentDigest), digest) {
			return nil, types.ErrProductionEvidenceDigestMismatch
		}
		snapshot.InlineContent = canonical
		snapshot.ContentDigest = digest
	}
	if existing, existingItem, existingSet, getErr := s.repo.GetEvidence(ctx, tenantID, evidenceID); getErr == nil {
		if !sameProductionEvidence(existing, existingItem, existingSet, snapshot, sourceSet) {
			return nil, types.ErrProductionEvidenceConflict
		}
		return existing, nil
	} else if !errors.Is(getErr, gorm.ErrRecordNotFound) {
		return nil, getErr
	}
	if err := s.repo.CreateEvidence(ctx, tenantID, itemID, snapshot); err != nil {
		if !errors.Is(err, types.ErrProductionConflict) {
			return nil, err
		}
		existing, existingItem, existingSet, getErr := s.repo.GetEvidence(ctx, tenantID, evidenceID)
		if getErr != nil || !sameProductionEvidence(existing, existingItem, existingSet, snapshot, sourceSet) {
			return nil, types.ErrProductionEvidenceConflict
		}
		return existing, nil
	}
	persisted, _, _, err := s.repo.GetEvidence(ctx, tenantID, evidenceID)
	if err != nil {
		return nil, err
	}
	return persisted, nil
}

func (s *productionSourceService) GetEvidence(
	ctx context.Context,
	evidenceID string,
) (*types.ProductionEvidenceSnapshot, *types.ProductionSourceItem, *types.ProductionSourceSet, error) {
	tenantID, _, err := productionCaller(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := requireProductionSourceID(evidenceID, "evidence id"); err != nil {
		return nil, nil, nil, err
	}
	evidence, item, sourceSet, err := s.repo.GetEvidence(ctx, tenantID, evidenceID)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := requireProductionSourceAuthor(ctx, s.projects, sourceSet.ProjectID); err != nil {
		return nil, nil, nil, err
	}
	return evidence, item, sourceSet, nil
}

func sameProductionEvidence(
	existing *types.ProductionEvidenceSnapshot,
	existingItem *types.ProductionSourceItem,
	existingSet *types.ProductionSourceSet,
	candidate *types.ProductionEvidenceSnapshot,
	candidateSet *types.ProductionSourceSet,
) bool {
	return existing != nil && existingItem != nil && existingSet != nil && candidate != nil && candidateSet != nil &&
		existing.ID == candidate.ID && existing.SourceItemID == candidate.SourceItemID &&
		existingItem.ID == candidate.SourceItemID && existingItem.SourceSetID == candidateSet.ID &&
		existingSet.ID == candidateSet.ID && existingSet.TenantID == candidateSet.TenantID && existingSet.ProjectID == candidateSet.ProjectID &&
		existing.SnapshotType == candidate.SnapshotType && existing.StoragePath == candidate.StoragePath &&
		bytes.Equal(existing.InlineContent, candidate.InlineContent) && existing.ContentDigest == candidate.ContentDigest &&
		bytes.Equal(existing.RedactionMetadata, candidate.RedactionMetadata) && existing.CapturedByRunID == candidate.CapturedByRunID &&
		existing.CapturedByToolCallID == candidate.CapturedByToolCallID
}

func (s *productionSourceService) Freeze(ctx context.Context, sourceSetID string) error {
	tenantID, userID, err := productionCaller(ctx)
	if err != nil {
		return err
	}
	if err := requireProductionSourceID(sourceSetID, "source set id"); err != nil {
		return err
	}
	sourceSet, err := s.repo.GetSet(ctx, tenantID, sourceSetID)
	if err != nil {
		return err
	}
	if err := requireProductionSourceAuthor(ctx, s.projects, sourceSet.ProjectID); err != nil {
		return err
	}
	if s.uow == nil {
		return errors.New("production unit of work is required")
	}
	return s.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.repo.Freeze(txCtx, tenantID, sourceSetID); err != nil {
			return err
		}
		return emitRequiredProductionAudit(txCtx, s.audit, &types.AuditLog{
			TenantID: tenantID, ActorUserID: userID, ActorRole: string(types.TenantRoleFromContext(ctx)),
			Action: types.AuditActionProductionSourceFrozen, TargetType: "production_source_set",
			TargetID: sourceSetID, Outcome: types.AuditOutcomeSuccess,
		})
	})
}

var _ interfaces.ProductionSourceService = (*productionSourceService)(nil)
