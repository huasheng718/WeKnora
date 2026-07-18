package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// CreateProductionProjectInput contains the caller-controlled fields for a new project.
// Tenant and owner identity are derived from the request context by the service.
type CreateProductionProjectInput struct {
	Name        string
	Description string
}

// ProductionProjectRepository persists tenant-scoped projects and their roles.
type ProductionProjectRepository interface {
	Create(ctx context.Context, project *types.ProductionProject, owner *types.ProductionProjectMember) error
	GetByID(ctx context.Context, tenantID uint64, projectID string) (*types.ProductionProject, error)
	ListByUser(ctx context.Context, tenantID uint64, userID string) ([]*types.ProductionProject, error)
	AssignRole(ctx context.Context, tenantID uint64, member *types.ProductionProjectMember) error
	RemoveRole(ctx context.Context, tenantID uint64, projectID, userID string, role types.ProductionRole) error
	ListRoles(ctx context.Context, tenantID uint64, projectID, userID string) ([]types.ProductionRole, error)
}

// ProductionProjectService is the production-project application contract.
type ProductionProjectService interface {
	CreateProject(ctx context.Context, input CreateProductionProjectInput) (*types.ProductionProject, error)
	GetProject(ctx context.Context, tenantID uint64, projectID string) (*types.ProductionProject, error)
	ListProjects(ctx context.Context, tenantID uint64, userID string) ([]*types.ProductionProject, error)
	AssignRole(ctx context.Context, projectID, userID string, role types.ProductionRole) error
	RemoveRole(ctx context.Context, projectID, userID string, role types.ProductionRole) error
	RequireProjectRole(ctx context.Context, projectID string, roles ...types.ProductionRole) error
}
