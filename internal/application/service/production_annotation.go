package service

import (
	"context"
	"encoding/json"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

// CreateProductionAnnotationInput contains the caller-controlled annotation fields.
// Tenant, project, status, and actor identity are derived by the service.
type CreateProductionAnnotationInput struct {
	DocumentID       string
	VersionID        string
	BlockID          string
	AnnotationType   types.ProductionAnnotationType
	QualityTag       *types.ProductionAnnotationCategory
	Severity         types.ProductionAnnotationSeverity
	Anchor           types.JSON
	Body             string
	SuggestedContent *string
}

type ListProductionAnnotationsInput struct {
	DocumentID     string
	VersionID      string
	AnnotationType types.ProductionAnnotationType
	Severity       types.ProductionAnnotationSeverity
	Status         types.ProductionAnnotationStatus
	Page           int
	PageSize       int
}

type ProductionAnnotationPage struct {
	Data     []*types.ProductionAnnotation
	Total    int64
	Page     int
	PageSize int
}

type productionAnnotationService struct {
	reviews   interfaces.ProductionReviewRepository
	documents interfaces.ProductionDocumentRepository
	projects  interfaces.ProductionProjectAuthorizer
	audit     interfaces.AuditLogService
	uow       interfaces.ProductionUnitOfWork
}

func NewProductionAnnotationService(
	reviews interfaces.ProductionReviewRepository,
	documents interfaces.ProductionDocumentRepository,
	projects interfaces.ProductionProjectAuthorizer,
	audit interfaces.AuditLogService,
	uow interfaces.ProductionUnitOfWork,
) *productionAnnotationService {
	return &productionAnnotationService{
		reviews: reviews, documents: documents, projects: projects, audit: audit, uow: uow,
	}
}

func (s *productionAnnotationService) List(
	ctx context.Context,
	input ListProductionAnnotationsInput,
) (*ProductionAnnotationPage, error) {
	tenantID, _, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireProductionSourceID(input.DocumentID, "document id"); err != nil {
		return nil, err
	}
	if input.Page < 1 || input.Page > types.ProductionAnnotationMaxPage ||
		input.PageSize < 1 || input.PageSize > 100 ||
		input.Page-1 > int(^uint(0)>>1)/input.PageSize ||
		(input.AnnotationType != "" && !input.AnnotationType.IsValid()) ||
		(input.Severity != "" && !input.Severity.IsValid()) ||
		(input.Status != "" && !input.Status.IsValid()) {
		return nil, types.ErrProductionReviewScopeInvalid
	}
	offset := (input.Page - 1) * input.PageSize
	if offset > types.ProductionAnnotationMaxOffset {
		return nil, types.ErrProductionReviewScopeInvalid
	}
	document, err := s.documents.GetDocument(ctx, tenantID, input.DocumentID)
	if err != nil {
		return nil, err
	}
	if document == nil || document.TenantID != tenantID || document.ID != input.DocumentID || document.ProjectID == "" {
		return nil, types.ErrProductionReviewScopeInvalid
	}
	if s.projects == nil {
		return nil, types.ErrProductionForbidden
	}
	if err := s.projects.RequireProjectRole(ctx, document.ProjectID,
		types.ProductionRoleProjectOwner,
		types.ProductionRoleAuthor,
		types.ProductionRoleBusinessReviewer,
		types.ProductionRoleEngineeringReviewer,
		types.ProductionRoleComplianceReviewer,
		types.ProductionRoleObserver,
	); err != nil {
		return nil, err
	}
	if input.VersionID != "" {
		if err := requireProductionSourceID(input.VersionID, "version id"); err != nil {
			return nil, err
		}
		version, loadErr := s.documents.GetVersion(ctx, tenantID, input.VersionID)
		if loadErr != nil {
			return nil, loadErr
		}
		if version == nil || version.TenantID != tenantID || version.ID != input.VersionID ||
			version.DocumentID != document.ID || version.ProjectID != document.ProjectID {
			return nil, types.ErrProductionReviewScopeInvalid
		}
	}
	items, total, err := s.reviews.ListAnnotations(
		ctx, tenantID, document.ID,
		interfaces.ListProductionAnnotationsFilter{
			VersionID: input.VersionID, AnnotationType: input.AnnotationType,
			Severity: input.Severity, Status: input.Status,
		},
		offset, input.PageSize,
	)
	if err != nil {
		return nil, err
	}
	return &ProductionAnnotationPage{
		Data: items, Total: total, Page: input.Page, PageSize: input.PageSize,
	}, nil
}

func (s *productionAnnotationService) Create(
	ctx context.Context,
	input CreateProductionAnnotationInput,
) (*types.ProductionAnnotation, error) {
	tenantID, actorID, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	document, err := s.documents.GetDocument(ctx, tenantID, input.DocumentID)
	if err != nil {
		return nil, err
	}
	version, err := s.documents.GetVersion(ctx, tenantID, input.VersionID)
	if err != nil {
		return nil, err
	}
	if document == nil || document.TenantID != tenantID || document.ID != input.DocumentID ||
		version == nil || version.TenantID != tenantID || version.ProjectID != document.ProjectID ||
		version.DocumentID != document.ID || version.ID != input.VersionID ||
		!productionAnnotationBlockBelongsToVersion(version, input.BlockID) {
		return nil, types.ErrProductionAnnotationAnchorInvalid
	}
	if s.projects == nil {
		return nil, types.ErrProductionForbidden
	}
	if err := s.projects.RequireProjectRole(ctx, document.ProjectID,
		types.ProductionRoleAuthor,
		types.ProductionRoleBusinessReviewer,
		types.ProductionRoleEngineeringReviewer,
		types.ProductionRoleComplianceReviewer,
	); err != nil {
		return nil, err
	}
	annotation := &types.ProductionAnnotation{
		ID: uuid.NewString(), TenantID: tenantID, ProjectID: document.ProjectID,
		DocumentID: document.ID, VersionID: version.ID, BlockID: input.BlockID,
		AnnotationType: input.AnnotationType, QualityTag: input.QualityTag,
		Severity: input.Severity, Anchor: input.Anchor, Body: input.Body,
		SuggestedContent: input.SuggestedContent, Status: types.ProductionAnnotationOpen,
		CreatedBy: actorID,
	}
	if s.uow == nil {
		return nil, types.ErrProductionForbidden
	}
	err = s.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		return s.reviews.CreateAnnotation(txCtx, annotation)
	})
	if err != nil {
		return nil, err
	}
	return annotation, nil
}

func productionAnnotationBlockBelongsToVersion(version *types.ProductionDocumentVersion, blockID string) bool {
	for _, block := range version.Blocks {
		if block != nil && block.ID == blockID && block.VersionID == version.ID {
			return true
		}
	}
	return false
}

func (s *productionAnnotationService) Resolve(
	ctx context.Context,
	annotationID string,
	status types.ProductionAnnotationStatus,
) error {
	tenantID, actorID, err := productionCaller(ctx)
	if err != nil {
		return err
	}
	if status != types.ProductionAnnotationResolved && status != types.ProductionAnnotationDismissed {
		return types.ErrProductionAnnotationLifecycle
	}
	annotation, err := s.reviews.GetAnnotation(ctx, tenantID, annotationID)
	if err != nil {
		return err
	}
	if annotation == nil || annotation.TenantID != tenantID || annotation.ProjectID == "" {
		return types.ErrProductionReviewScopeInvalid
	}
	if annotation.CreatedBy != actorID {
		if s.projects == nil {
			return types.ErrProductionForbidden
		}
		if err := s.projects.RequireProjectRole(ctx, annotation.ProjectID,
			types.ProductionRoleProjectOwner,
			types.ProductionRoleAuthor,
		); err != nil {
			return err
		}
	}
	if productionAnnotationRequiresComplianceResolution(annotation) {
		if s.projects == nil {
			return types.ErrProductionForbidden
		}
		if err := s.projects.RequireProjectRole(ctx, annotation.ProjectID,
			types.ProductionRoleProjectOwner,
			types.ProductionRoleComplianceReviewer,
		); err != nil {
			return err
		}
	}
	if s.uow == nil {
		return types.ErrProductionForbidden
	}
	details, err := json.Marshal(map[string]string{
		"actor_user_id": actorID,
		"from_status":   string(types.ProductionAnnotationOpen),
		"status":        string(status),
	})
	if err != nil {
		return err
	}
	return s.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		updated, resolveErr := s.reviews.ResolveAnnotation(
			txCtx, tenantID, annotation.ID, actorID, status,
		)
		if resolveErr != nil {
			return resolveErr
		}
		if !updated {
			return types.ErrProductionAnnotationLifecycle
		}
		return emitRequiredProductionAudit(txCtx, s.audit, &types.AuditLog{
			TenantID: tenantID, ActorUserID: actorID, ActorRole: string(types.TenantRoleFromContext(ctx)),
			Action: types.AuditActionProductionAnnotationResolved, TargetType: "production_annotation",
			TargetID: annotation.ID, Outcome: types.AuditOutcomeSuccess, Details: types.JSON(details),
		})
	})
}

func productionAnnotationRequiresComplianceResolution(annotation *types.ProductionAnnotation) bool {
	return annotation.Severity == types.ProductionAnnotationBlocking &&
		annotation.QualityTag != nil && *annotation.QualityTag == types.ProductionQualityTagComplianceRisk
}
