package service

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

type productionDocumentTypeService struct {
	repo    interfaces.ProductionDocumentTypeRepository
	members interfaces.TenantMemberService
	audit   interfaces.AuditLogService
}

// NewProductionDocumentTypeService constructs the document-type lifecycle service.
func NewProductionDocumentTypeService(
	repo interfaces.ProductionDocumentTypeRepository,
	members interfaces.TenantMemberService,
	audit interfaces.AuditLogService,
) *productionDocumentTypeService {
	return &productionDocumentTypeService{repo: repo, members: members, audit: audit}
}

func (s *productionDocumentTypeService) CreateDocumentType(
	ctx context.Context,
	tenantID uint64,
	input interfaces.CreateProductionDocumentTypeInput,
) (*types.ProductionDocumentType, error) {
	membership, userID, err := requireProductionTenantRole(ctx, s.members, tenantID, types.TenantRoleAdmin)
	if err != nil {
		return nil, err
	}
	workflowPlan, err := canonicalProductionWorkflowPlan(input.WorkflowPlan, input.SkillBindings)
	if err != nil {
		return nil, err
	}
	documentType := &types.ProductionDocumentType{
		ID:                 uuid.NewString(),
		TenantID:           tenantID,
		Code:               input.Code,
		Name:               input.Name,
		Description:        input.Description,
		SchemaVersion:      input.SchemaVersion,
		BlockSchema:        input.BlockSchema,
		SourceRequirements: input.SourceRequirements,
		SkillBindings:      input.SkillBindings,
		WorkflowPlan:       workflowPlan,
		QualityRules:       input.QualityRules,
		ReviewPolicy:       input.ReviewPolicy,
		PublicationPolicy:  input.PublicationPolicy,
		Status:             types.ProductionDocumentTypeDraft,
		CreatedBy:          userID,
	}
	if err := s.repo.Create(ctx, documentType); err != nil {
		return nil, err
	}
	emitProductionAudit(ctx, s.audit, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: userID,
		ActorRole:   string(membership.Role),
		Action:      types.AuditActionProductionDocumentTypeCreated,
		TargetType:  "production_document_type",
		TargetID:    documentType.ID,
		Outcome:     types.AuditOutcomeSuccess,
	})
	return documentType, nil
}

func (s *productionDocumentTypeService) ActivateDocumentType(
	ctx context.Context,
	tenantID uint64,
	code string,
	schemaVersion int,
) (*types.ProductionDocumentType, error) {
	membership, userID, err := requireProductionTenantRole(ctx, s.members, tenantID, types.TenantRoleAdmin)
	if err != nil {
		return nil, err
	}
	documentType, err := s.repo.Activate(ctx, tenantID, code, schemaVersion)
	if err != nil {
		return nil, err
	}
	emitProductionAudit(ctx, s.audit, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: userID,
		ActorRole:   string(membership.Role),
		Action:      types.AuditActionProductionDocumentTypeActivated,
		TargetType:  "production_document_type",
		TargetID:    documentType.ID,
		Outcome:     types.AuditOutcomeSuccess,
	})
	return documentType, nil
}

func (s *productionDocumentTypeService) GetDocumentType(
	ctx context.Context,
	tenantID uint64,
	documentTypeID string,
) (*types.ProductionDocumentType, error) {
	if _, _, err := productionMembership(ctx, s.members, tenantID); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, tenantID, documentTypeID)
}

func (s *productionDocumentTypeService) ListDocumentTypes(
	ctx context.Context,
	tenantID uint64,
) ([]*types.ProductionDocumentType, error) {
	if _, _, err := productionMembership(ctx, s.members, tenantID); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, tenantID)
}

var _ interfaces.ProductionDocumentTypeService = (*productionDocumentTypeService)(nil)
