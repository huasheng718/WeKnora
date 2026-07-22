package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/database"
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
	if project == nil {
		return errors.New("production project is required")
	}
	if owner == nil {
		return errors.New("production project owner is required")
	}
	persistedOwner := &types.ProductionProjectMember{
		ProjectID:  project.ID,
		UserID:     project.OwnerUserID,
		Role:       types.ProductionRoleProjectOwner,
		AssignedBy: owner.AssignedBy,
	}
	return database.DBFromContext(ctx, r.db).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(project).Error; err != nil {
			return err
		}
		return tx.Create(persistedOwner).Error
	})
}

func (r *productionProjectRepository) GetByID(
	ctx context.Context,
	tenantID uint64,
	projectID string,
) (*types.ProductionProject, error) {
	var project types.ProductionProject
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
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
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
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

type productionProjectRoleDecorationRow struct {
	ProjectID string
	Role      types.ProductionRole
	UpdatedAt time.Time
}

type productionProjectAggregateRow struct {
	ProjectID      string
	RowCount       int64
	LatestActivity *string
}

const productionAuthorizedProjectScope = `EXISTS (
	SELECT 1 FROM production_project_members AS authorized_member
	WHERE authorized_member.project_id = production_projects.id
		AND authorized_member.user_id = ?
		AND authorized_member.deleted_at IS NULL
)`

func laterProductionProjectActivity(current time.Time, candidate *time.Time) time.Time {
	if candidate != nil && candidate.After(current) {
		return candidate.UTC()
	}
	return current
}

func parseProductionProjectActivity(value *string) *time.Time {
	if value == nil || *value == "" {
		return nil
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999-07",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, *value); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}

func (r *productionProjectRepository) ListDecorationsByUser(
	ctx context.Context,
	tenantID uint64,
	userID string,
) (types.ProductionProjectDecorations, error) {
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	decorations := make(types.ProductionProjectDecorations)

	var roleRows []productionProjectRoleDecorationRow
	if err := db.Table("production_project_members AS member").
		Select("member.project_id, member.role, project.updated_at").
		Joins("JOIN production_projects AS project ON project.id = member.project_id").
		Where("project.tenant_id = ? AND project.deleted_at IS NULL", tenantID).
		Where("member.user_id = ? AND member.deleted_at IS NULL", userID).
		Order("member.project_id ASC, member.created_at ASC, member.role ASC").
		Scan(&roleRows).Error; err != nil {
		return nil, err
	}
	for _, row := range roleRows {
		decoration := decorations[row.ProjectID]
		decoration.CurrentUserRoles = append(decoration.CurrentUserRoles, row.Role)
		if decoration.Summary.LatestActivity.IsZero() {
			decoration.Summary.LatestActivity = row.UpdatedAt.UTC()
		}
		decorations[row.ProjectID] = decoration
	}

	type aggregateSpec struct {
		table      string
		alias      string
		activity   string
		count      string
		applyCount func(*types.ProductionProjectSummary, int64)
	}
	specs := []aggregateSpec{
		{"production_documents", "related", "updated_at", "COUNT(related.id)", func(s *types.ProductionProjectSummary, count int64) { s.DocumentCount = count }},
		{"production_source_sets", "related", "created_at", "COUNT(related.id)", func(s *types.ProductionProjectSummary, count int64) { s.SourceSetCount = count }},
		{"production_review_requests", "related", "updated_at", "SUM(CASE WHEN related.status IN ('pending', 'changes_requested') THEN 1 ELSE 0 END)", func(s *types.ProductionProjectSummary, count int64) { s.PendingReviews = count }},
		{"production_release_targets", "related", "updated_at", "SUM(CASE WHEN related.status = 'failed' THEN 1 ELSE 0 END)", func(s *types.ProductionProjectSummary, count int64) { s.FailedTargets = count }},
		{"production_runs", "related", "updated_at", "SUM(CASE WHEN related.status IN ('queued', 'running', 'waiting_approval') THEN 1 ELSE 0 END)", func(s *types.ProductionProjectSummary, count int64) { s.InFlightRuns = count }},
	}
	for _, spec := range specs {
		var rows []productionProjectAggregateRow
		selectClause := "production_projects.id AS project_id, " + spec.count +
			" AS row_count, CAST(MAX(" + spec.alias + "." + spec.activity + ") AS TEXT) AS latest_activity"
		joinClause := "LEFT JOIN " + spec.table + " AS " + spec.alias +
			" ON " + spec.alias + ".tenant_id = production_projects.tenant_id" +
			" AND " + spec.alias + ".project_id = production_projects.id"
		if err := db.Table("production_projects").
			Select(selectClause).
			Joins(joinClause).
			Where("production_projects.tenant_id = ? AND production_projects.deleted_at IS NULL", tenantID).
			Where(productionAuthorizedProjectScope, userID).
			Group("production_projects.id").
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			decoration, ok := decorations[row.ProjectID]
			if !ok {
				continue
			}
			spec.applyCount(&decoration.Summary, row.RowCount)
			decoration.Summary.LatestActivity = laterProductionProjectActivity(
				decoration.Summary.LatestActivity,
				parseProductionProjectActivity(row.LatestActivity),
			)
			decorations[row.ProjectID] = decoration
		}
	}
	return decorations, nil
}

func (r *productionProjectRepository) AssignRole(
	ctx context.Context,
	tenantID uint64,
	member *types.ProductionProjectMember,
) error {
	if member == nil {
		return errors.New("production project member is required")
	}
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var project types.ProductionProject
		if err := tx.Select("id").
			Where("tenant_id = ? AND id = ?", tenantID, member.ProjectID).
			First(&project).Error; err != nil {
			return err
		}
		return tx.Create(member).Error
	})
	return translateProductionWriteError(err)
}

func (r *productionProjectRepository) RemoveRole(
	ctx context.Context,
	tenantID uint64,
	projectID, userID string,
	role types.ProductionRole,
) error {
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	tenantProjects := db.Model(&types.ProductionProject{}).
		Select("id").
		Where("tenant_id = ? AND id = ?", tenantID, projectID)
	result := db.
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
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
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

func (r *productionProjectRepository) HasLiveRoleAssignee(
	ctx context.Context,
	tenantID uint64,
	projectID string,
	role types.ProductionRole,
) (bool, error) {
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	args := []any{tenantID, projectID, types.ProductionProjectActive, role, types.TenantMemberStatusActive}
	candidates := `SELECT member.user_id
FROM production_project_members AS member
JOIN production_projects AS project ON project.id = member.project_id
JOIN tenant_members AS tenant_member
  ON tenant_member.tenant_id = project.tenant_id AND tenant_member.user_id = member.user_id
WHERE project.tenant_id = ? AND project.id = ? AND project.status = ?
  AND project.deleted_at IS NULL AND member.deleted_at IS NULL AND tenant_member.deleted_at IS NULL
  AND member.role = ? AND tenant_member.status = ?
ORDER BY member.user_id`
	if db.Dialector.Name() == "postgres" {
		var userIDs []string
		err := db.Raw(candidates+"\nFOR UPDATE OF member, tenant_member", args...).Scan(&userIDs).Error
		return len(userIDs) > 0, err
	}
	result := db.Exec(`UPDATE production_project_members
SET created_at = created_at
WHERE rowid IN (`+strings.Replace(candidates, "member.user_id", "member.rowid", 1)+`)`, args...)
	return result.RowsAffected > 0, result.Error
}

var _ interfaces.ProductionProjectRepository = (*productionProjectRepository)(nil)
