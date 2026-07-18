package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

type productionProjectService struct {
	repo    interfaces.ProductionProjectRepository
	members interfaces.TenantMemberService
	audit   interfaces.AuditLogService
}

// NewProductionProjectService constructs the tenant-scoped project service.
func NewProductionProjectService(
	repo interfaces.ProductionProjectRepository,
	members interfaces.TenantMemberService,
	audit interfaces.AuditLogService,
) *productionProjectService {
	return &productionProjectService{repo: repo, members: members, audit: audit}
}

func productionCaller(ctx context.Context) (uint64, string, error) {
	tenantID, tenantOK := types.TenantIDFromContext(ctx)
	userID, userOK := types.UserIDFromContext(ctx)
	if !tenantOK || tenantID == 0 || !userOK {
		return 0, "", types.ErrProductionForbidden
	}
	return tenantID, userID, nil
}

func productionMembership(
	ctx context.Context,
	members interfaces.TenantMemberService,
	tenantID uint64,
) (*types.TenantMember, string, error) {
	contextTenantID, userID, err := productionCaller(ctx)
	if err != nil || contextTenantID != tenantID {
		return nil, "", types.ErrProductionForbidden
	}
	membership, err := members.GetMembership(ctx, userID, tenantID)
	if err != nil {
		return nil, "", err
	}
	if membership == nil || membership.Status != types.TenantMemberStatusActive {
		return nil, "", types.ErrProductionForbidden
	}
	return membership, userID, nil
}

func requireProductionTenantRole(
	ctx context.Context,
	members interfaces.TenantMemberService,
	tenantID uint64,
	required types.TenantRole,
) (*types.TenantMember, string, error) {
	membership, userID, err := productionMembership(ctx, members, tenantID)
	if err != nil {
		return nil, "", err
	}
	if !membership.Role.HasPermission(required) {
		return nil, "", types.ErrProductionForbidden
	}
	return membership, userID, nil
}

func emitProductionAudit(ctx context.Context, audit interfaces.AuditLogService, entry *types.AuditLog) {
	if audit != nil {
		_ = audit.Log(ctx, entry)
	}
}

func (s *productionProjectService) CreateProject(
	ctx context.Context,
	input interfaces.CreateProductionProjectInput,
) (*types.ProductionProject, error) {
	tenantID, _, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	membership, userID, err := requireProductionTenantRole(ctx, s.members, tenantID, types.TenantRoleContributor)
	if err != nil {
		return nil, err
	}
	project := &types.ProductionProject{
		ID:          uuid.NewString(),
		TenantID:    tenantID,
		Name:        input.Name,
		Description: input.Description,
		OwnerUserID: userID,
		Status:      types.ProductionProjectActive,
	}
	owner := &types.ProductionProjectMember{
		ProjectID:  project.ID,
		UserID:     userID,
		Role:       types.ProductionRoleProjectOwner,
		AssignedBy: userID,
	}
	if err := s.repo.Create(ctx, project, owner); err != nil {
		return nil, err
	}
	emitProductionAudit(ctx, s.audit, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: userID,
		ActorRole:   string(membership.Role),
		Action:      types.AuditActionProductionProjectCreated,
		TargetType:  "production_project",
		TargetID:    project.ID,
		Outcome:     types.AuditOutcomeSuccess,
	})
	return project, nil
}

func (s *productionProjectService) GetProject(
	ctx context.Context,
	tenantID uint64,
	projectID string,
) (*types.ProductionProject, error) {
	membership, _, err := productionMembership(ctx, s.members, tenantID)
	if err != nil {
		return nil, err
	}
	if !membership.Role.HasPermission(types.TenantRoleAdmin) {
		if err := s.RequireProjectRole(ctx, projectID, allProductionProjectRoles...); err != nil {
			return nil, err
		}
	}
	return s.repo.GetByID(ctx, tenantID, projectID)
}

func (s *productionProjectService) ListProjects(
	ctx context.Context,
	tenantID uint64,
	userID string,
) ([]*types.ProductionProject, error) {
	_, callerID, err := productionMembership(ctx, s.members, tenantID)
	if err != nil {
		return nil, err
	}
	if callerID != userID {
		return nil, types.ErrProductionForbidden
	}
	return s.repo.ListByUser(ctx, tenantID, userID)
}

func (s *productionProjectService) AssignRole(
	ctx context.Context,
	projectID, userID string,
	role types.ProductionRole,
) error {
	if !role.IsValid() {
		return fmt.Errorf("invalid production role %q", role)
	}
	tenantID, actorID, err := productionCaller(ctx)
	if err != nil {
		return err
	}
	actorMembership, err := s.requireRoleManager(ctx, tenantID, projectID)
	if err != nil {
		return err
	}
	targetMembership, err := s.members.GetMembership(ctx, userID, tenantID)
	if err != nil {
		return err
	}
	if targetMembership == nil || targetMembership.Status != types.TenantMemberStatusActive {
		return types.ErrProductionForbidden
	}
	if targetMembership.Role == types.TenantRoleViewer && role != types.ProductionRoleObserver {
		return types.ErrProductionForbidden
	}
	member := &types.ProductionProjectMember{
		ProjectID:  projectID,
		UserID:     userID,
		Role:       role,
		AssignedBy: actorID,
	}
	if err := s.repo.AssignRole(ctx, tenantID, member); err != nil {
		return err
	}
	s.emitRoleAudit(ctx, tenantID, actorID, actorMembership.Role, projectID, userID, role, "assigned")
	return nil
}

func (s *productionProjectService) RemoveRole(
	ctx context.Context,
	projectID, userID string,
	role types.ProductionRole,
) error {
	if !role.IsValid() {
		return fmt.Errorf("invalid production role %q", role)
	}
	tenantID, actorID, err := productionCaller(ctx)
	if err != nil {
		return err
	}
	actorMembership, err := s.requireRoleManager(ctx, tenantID, projectID)
	if err != nil {
		return err
	}
	if err := s.repo.RemoveRole(ctx, tenantID, projectID, userID, role); err != nil {
		return err
	}
	s.emitRoleAudit(ctx, tenantID, actorID, actorMembership.Role, projectID, userID, role, "removed")
	return nil
}

func (s *productionProjectService) requireRoleManager(
	ctx context.Context,
	tenantID uint64,
	projectID string,
) (*types.TenantMember, error) {
	membership, _, err := productionMembership(ctx, s.members, tenantID)
	if err != nil {
		return nil, err
	}
	if membership.Role.HasPermission(types.TenantRoleAdmin) {
		return membership, nil
	}
	if err := s.RequireProjectRole(ctx, projectID, types.ProductionRoleProjectOwner); err != nil {
		return nil, err
	}
	return membership, nil
}

// RequireProjectRole authorizes the context caller against roles in the
// caller's tenant. A same-named project in another tenant never contributes.
func (s *productionProjectService) RequireProjectRole(
	ctx context.Context,
	projectID string,
	required ...types.ProductionRole,
) error {
	tenantID, userID, err := productionCaller(ctx)
	if err != nil {
		return err
	}
	if _, _, err := productionMembership(ctx, s.members, tenantID); err != nil {
		return err
	}
	roles, err := s.repo.ListRoles(ctx, tenantID, projectID, userID)
	if err != nil {
		return err
	}
	for _, actual := range roles {
		for _, allowed := range required {
			if allowed.IsValid() && actual == allowed {
				return nil
			}
		}
	}
	return types.ErrProductionForbidden
}

func (s *productionProjectService) emitRoleAudit(
	ctx context.Context,
	tenantID uint64,
	actorID string,
	actorRole types.TenantRole,
	projectID, targetUserID string,
	role types.ProductionRole,
	operation string,
) {
	details, _ := json.Marshal(map[string]string{
		"operation":  operation,
		"project_id": projectID,
		"role":       string(role),
	})
	emitProductionAudit(ctx, s.audit, &types.AuditLog{
		TenantID:     tenantID,
		ActorUserID:  actorID,
		ActorRole:    string(actorRole),
		Action:       types.AuditActionProductionProjectRoleSet,
		TargetType:   "production_project_member",
		TargetID:     projectID,
		TargetUserID: targetUserID,
		Outcome:      types.AuditOutcomeSuccess,
		Details:      types.JSON(details),
	})
}

var allProductionProjectRoles = []types.ProductionRole{
	types.ProductionRoleProjectOwner,
	types.ProductionRoleAuthor,
	types.ProductionRoleBusinessReviewer,
	types.ProductionRoleEngineeringReviewer,
	types.ProductionRoleKnowledgeAdmin,
	types.ProductionRoleComplianceReviewer,
	types.ProductionRolePublisher,
	types.ProductionRoleObserver,
}

var _ interfaces.ProductionProjectService = (*productionProjectService)(nil)
