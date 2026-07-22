package service

import (
	"context"
	"encoding/json"

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
	config, err := canonicalProductionDocumentTypeConfig(
		input.BlockSchema, input.SourceRequirements, input.SkillBindings, input.WorkflowPlan,
		input.QualityRules, input.ReviewPolicy, input.PublicationPolicy,
	)
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
		BlockSchema:        config.BlockSchema,
		SourceRequirements: config.SourceRequirements,
		SkillBindings:      config.SkillBindings,
		WorkflowPlan:       config.WorkflowPlan,
		QualityRules:       config.QualityRules,
		ReviewPolicy:       config.ReviewPolicy,
		PublicationPolicy:  config.PublicationPolicy,
		Status:             types.ProductionDocumentTypeDraft,
		Origin:             types.ProductionDocumentTypeOriginCustom,
		TemplateKey:        nil,
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

func canonicalProductionDocumentTypeConfig(
	blockSchema, sourceRequirements, skillBindings, workflowPlan types.JSON,
	qualityRules, reviewPolicy, publicationPolicy types.JSON,
) (types.ProductionDocumentTypeConfigInput, error) {
	config, err := types.CanonicalProductionDocumentTypeConfig(types.ProductionDocumentTypeConfigInput{
		BlockSchema: blockSchema, SourceRequirements: sourceRequirements,
		SkillBindings: skillBindings, WorkflowPlan: workflowPlan,
		QualityRules: qualityRules, ReviewPolicy: reviewPolicy,
		PublicationPolicy: publicationPolicy,
	})
	if err != nil {
		return types.ProductionDocumentTypeConfigInput{}, err
	}
	workflow, err := canonicalProductionWorkflowPlan(config.Canonical.WorkflowPlan, config.Canonical.SkillBindings)
	if err != nil {
		return types.ProductionDocumentTypeConfigInput{}, err
	}
	config.Canonical.WorkflowPlan = workflow
	return config.Canonical, nil
}

func (s *productionDocumentTypeService) DeriveDraft(
	ctx context.Context,
	tenantID uint64,
	baseID string,
	input interfaces.DeriveProductionDocumentTypeInput,
) (*types.ProductionDocumentType, error) {
	membership, userID, err := requireProductionTenantRole(ctx, s.members, tenantID, types.TenantRoleAdmin)
	if err != nil {
		return nil, err
	}
	base, err := s.repo.GetByID(ctx, tenantID, baseID)
	if err != nil {
		return nil, err
	}
	config, err := canonicalProductionDocumentTypeConfig(
		input.BlockSchema, input.SourceRequirements, input.SkillBindings, input.WorkflowPlan,
		input.QualityRules, input.ReviewPolicy, input.PublicationPolicy,
	)
	if err != nil {
		return nil, err
	}
	draft := &types.ProductionDocumentType{
		ID: uuid.NewString(), TenantID: tenantID, Name: input.Name, Description: input.Description,
		BlockSchema: config.BlockSchema, SourceRequirements: config.SourceRequirements,
		SkillBindings: config.SkillBindings, WorkflowPlan: config.WorkflowPlan,
		QualityRules: config.QualityRules, ReviewPolicy: config.ReviewPolicy,
		PublicationPolicy: config.PublicationPolicy, Status: types.ProductionDocumentTypeDraft,
		Origin: types.ProductionDocumentTypeOriginCustom, CreatedBy: userID,
	}
	derived, err := s.repo.DeriveDraft(ctx, tenantID, baseID, draft)
	if err != nil {
		return nil, err
	}
	details, _ := json.Marshal(map[string]any{
		"base_document_type_id":  base.ID,
		"base_schema_version":    base.SchemaVersion,
		"derived_schema_version": derived.SchemaVersion,
	})
	emitProductionAudit(ctx, s.audit, &types.AuditLog{
		TenantID: tenantID, ActorUserID: userID, ActorRole: string(membership.Role),
		Action:     types.AuditActionProductionDocumentTypeDerived,
		TargetType: "production_document_type", TargetID: derived.ID,
		Outcome: types.AuditOutcomeSuccess, Details: types.JSON(details),
	})
	return derived, nil
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
