package repository

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

type productionProjectRepository struct {
	db *gorm.DB
}

// NewProductionProjectRepository creates a tenant-scoped project repository.
func NewProductionProjectRepository(db *gorm.DB) interfaces.ProductionProjectRepository {
	return &productionProjectRepository{db: db}
}

func (r *productionProjectRepository) Create(
	ctx context.Context,
	project *types.ProductionProject,
	owner *types.ProductionProjectMember,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(project).Error; err != nil {
			return err
		}
		return tx.Create(owner).Error
	})
}

func (r *productionProjectRepository) GetByID(
	ctx context.Context,
	tenantID uint64,
	projectID string,
) (*types.ProductionProject, error) {
	var project types.ProductionProject
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, projectID).
		First(&project).Error
	if err != nil {
		return nil, err
	}
	return &project, nil
}

func (r *productionProjectRepository) ListByUser(
	ctx context.Context,
	tenantID uint64,
	userID string,
) ([]*types.ProductionProject, error) {
	var projects []*types.ProductionProject
	err := r.db.WithContext(ctx).
		Model(&types.ProductionProject{}).
		Distinct("production_projects.*").
		Joins("JOIN production_project_members ON production_project_members.project_id = production_projects.id").
		Where("production_projects.tenant_id = ?", tenantID).
		Where("production_project_members.user_id = ? AND production_project_members.deleted_at IS NULL", userID).
		Order("production_projects.created_at DESC, production_projects.id ASC").
		Find(&projects).Error
	if err != nil {
		return nil, err
	}
	return projects, nil
}

func (r *productionProjectRepository) AssignRole(
	ctx context.Context,
	tenantID uint64,
	member *types.ProductionProjectMember,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var project types.ProductionProject
		if err := tx.Select("id").
			Where("tenant_id = ? AND id = ?", tenantID, member.ProjectID).
			First(&project).Error; err != nil {
			return err
		}
		return tx.Create(member).Error
	})
}

func (r *productionProjectRepository) RemoveRole(
	ctx context.Context,
	tenantID uint64,
	projectID, userID string,
	role types.ProductionRole,
) error {
	tenantProjects := r.db.Model(&types.ProductionProject{}).
		Select("id").
		Where("tenant_id = ? AND id = ?", tenantID, projectID)
	result := r.db.WithContext(ctx).
		Where("project_id IN (?) AND user_id = ? AND role = ?", tenantProjects, userID, role).
		Delete(&types.ProductionProjectMember{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *productionProjectRepository) ListRoles(
	ctx context.Context,
	tenantID uint64,
	projectID, userID string,
) ([]types.ProductionRole, error) {
	var roles []types.ProductionRole
	err := r.db.WithContext(ctx).
		Model(&types.ProductionProjectMember{}).
		Select("production_project_members.role").
		Joins("JOIN production_projects ON production_projects.id = production_project_members.project_id").
		Where("production_projects.tenant_id = ? AND production_projects.id = ?", tenantID, projectID).
		Where("production_projects.deleted_at IS NULL").
		Where("production_project_members.user_id = ?", userID).
		Order("production_project_members.created_at ASC, production_project_members.role ASC").
		Pluck("production_project_members.role", &roles).Error
	if err != nil {
		return nil, err
	}
	return roles, nil
}

var _ interfaces.ProductionProjectRepository = (*productionProjectRepository)(nil)
