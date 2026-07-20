package service

import (
	"context"

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

type productionAnnotationService struct {
	reviews   interfaces.ProductionReviewRepository
	documents interfaces.ProductionDocumentRepository
	projects  interfaces.ProductionProjectAuthorizer
}

func NewProductionAnnotationService(
	reviews interfaces.ProductionReviewRepository,
	documents interfaces.ProductionDocumentRepository,
	projects interfaces.ProductionProjectAuthorizer,
) *productionAnnotationService {
	return &productionAnnotationService{reviews: reviews, documents: documents, projects: projects}
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
	if err := s.reviews.CreateAnnotation(ctx, annotation); err != nil {
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
	updated, err := s.reviews.ResolveAnnotation(ctx, tenantID, annotation.ID, actorID, status)
	if err != nil {
		return err
	}
	if !updated {
		return types.ErrProductionAnnotationLifecycle
	}
	return nil
}

func productionAnnotationRequiresComplianceResolution(annotation *types.ProductionAnnotation) bool {
	return annotation.Severity == types.ProductionAnnotationBlocking &&
		annotation.QualityTag != nil && *annotation.QualityTag == types.ProductionQualityTagComplianceRisk
}
