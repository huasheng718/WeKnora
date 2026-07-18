package repository

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newProductionRepoTestDB(t *testing.T) (interfaces.ProductionProjectRepository, *gorm.DB) {
	t.Helper()

	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared&_foreign_keys=1&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migration, err := os.ReadFile(filepath.Join(
		filepath.Dir(filename), "../../../migrations/sqlite/000001_knowledge_production_foundation.up.sql",
	))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)

	return NewProductionProjectRepository(db), db
}

func seedProductionProject(t *testing.T, db *gorm.DB, id string, tenantID uint64) {
	t.Helper()
	require.NoError(t, db.Create(&types.ProductionProject{
		ID:          id,
		TenantID:    tenantID,
		Name:        "Project " + id,
		OwnerUserID: "owner-1",
		Status:      types.ProductionProjectActive,
	}).Error)
}

func TestProductionProjectRepositoryCreatePersistsProjectAndOwner(t *testing.T) {
	repo, db := newProductionRepoTestDB(t)
	project := &types.ProductionProject{
		ID:          "project-1",
		TenantID:    7,
		Name:        "Foundation",
		OwnerUserID: "author-1",
		Status:      types.ProductionProjectActive,
	}
	owner := &types.ProductionProjectMember{
		ProjectID:  project.ID,
		UserID:     "author-1",
		Role:       types.ProductionRoleProjectOwner,
		AssignedBy: "author-1",
	}

	require.NoError(t, repo.Create(context.Background(), project, owner))

	var projectCount, ownerCount int64
	require.NoError(t, db.Model(&types.ProductionProject{}).Count(&projectCount).Error)
	require.NoError(t, db.Model(&types.ProductionProjectMember{}).Count(&ownerCount).Error)
	require.Equal(t, int64(1), projectCount)
	require.Equal(t, int64(1), ownerCount)
}

func TestProductionProjectRepositoryCreateDerivesOwnerMembershipFromProject(t *testing.T) {
	repo, db := newProductionRepoTestDB(t)
	seedProductionProject(t, db, "project-b", 8)
	project := &types.ProductionProject{
		ID:          "project-a",
		TenantID:    7,
		Name:        "Foundation",
		OwnerUserID: "owner-a",
		Status:      types.ProductionProjectActive,
	}
	owner := &types.ProductionProjectMember{
		ProjectID:  "project-b",
		UserID:     "attacker",
		Role:       types.ProductionRolePublisher,
		AssignedBy: "creator-a",
	}

	require.NoError(t, repo.Create(context.Background(), project, owner))

	var members []types.ProductionProjectMember
	require.NoError(t, db.Order("project_id ASC").Find(&members).Error)
	require.Len(t, members, 1)
	require.Equal(t, "project-a", members[0].ProjectID)
	require.Equal(t, "owner-a", members[0].UserID)
	require.Equal(t, types.ProductionRoleProjectOwner, members[0].Role)
	require.Equal(t, "creator-a", members[0].AssignedBy)
}

func TestProductionProjectRepositoryCreateRejectsNilInputs(t *testing.T) {
	repo, _ := newProductionRepoTestDB(t)
	project := &types.ProductionProject{
		ID: "project-1", TenantID: 7, Name: "Foundation", OwnerUserID: "owner-1",
		Status: types.ProductionProjectActive,
	}
	owner := &types.ProductionProjectMember{AssignedBy: "owner-1"}

	require.ErrorContains(t, repo.Create(context.Background(), nil, owner), "production project")
	require.ErrorContains(t, repo.Create(context.Background(), project, nil), "project owner")
}

func TestProductionProjectRepositoryCreateRollsBackProjectWhenOwnerInsertFails(t *testing.T) {
	repo, db := newProductionRepoTestDB(t)
	require.NoError(t, db.Exec(`
CREATE TRIGGER fail_production_owner_insert
BEFORE INSERT ON production_project_members
BEGIN
    SELECT RAISE(ABORT, 'forced owner insert failure');
END`).Error)
	project := &types.ProductionProject{
		ID: "project-1", TenantID: 7, Name: "Foundation", OwnerUserID: "owner-1",
		Status: types.ProductionProjectActive,
	}
	owner := &types.ProductionProjectMember{AssignedBy: "owner-1"}

	require.Error(t, repo.Create(context.Background(), project, owner))

	var count int64
	require.NoError(t, db.Model(&types.ProductionProject{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProductionProjectRepositoryRejectsCrossTenantRead(t *testing.T) {
	repo, db := newProductionRepoTestDB(t)
	seedProductionProject(t, db, "project-1", 7)

	got, err := repo.GetByID(context.Background(), 8, "project-1")

	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Nil(t, got)
}

func TestProductionProjectRepositoryListsDistinctProjectsForTenantUser(t *testing.T) {
	repo, db := newProductionRepoTestDB(t)
	seedProductionProject(t, db, "project-1", 7)
	seedProductionProject(t, db, "project-2", 8)
	for _, member := range []*types.ProductionProjectMember{
		{ProjectID: "project-1", UserID: "user-1", Role: types.ProductionRoleAuthor, AssignedBy: "owner-1"},
		{ProjectID: "project-1", UserID: "user-1", Role: types.ProductionRoleObserver, AssignedBy: "owner-1"},
		{ProjectID: "project-2", UserID: "user-1", Role: types.ProductionRoleAuthor, AssignedBy: "owner-1"},
	} {
		require.NoError(t, db.Create(member).Error)
	}

	projects, err := repo.ListByUser(context.Background(), 7, "user-1")

	require.NoError(t, err)
	require.Len(t, projects, 1)
	require.Equal(t, "project-1", projects[0].ID)
}

func TestProductionProjectRepositoryScopesRoleOperationsByProjectTenant(t *testing.T) {
	repo, db := newProductionRepoTestDB(t)
	seedProductionProject(t, db, "project-1", 7)
	member := &types.ProductionProjectMember{
		ProjectID: "project-1", UserID: "user-1", Role: types.ProductionRoleAuthor, AssignedBy: "owner-1",
	}

	require.ErrorIs(t, repo.AssignRole(context.Background(), 8, member), gorm.ErrRecordNotFound)
	require.NoError(t, repo.AssignRole(context.Background(), 7, member))

	roles, err := repo.ListRoles(context.Background(), 8, "project-1", "user-1")
	require.NoError(t, err)
	require.Empty(t, roles)

	require.ErrorIs(t, repo.RemoveRole(
		context.Background(), 8, "project-1", "user-1", types.ProductionRoleAuthor,
	), gorm.ErrRecordNotFound)

	roles, err = repo.ListRoles(context.Background(), 7, "project-1", "user-1")
	require.NoError(t, err)
	require.Equal(t, []types.ProductionRole{types.ProductionRoleAuthor}, roles)

	require.NoError(t, repo.RemoveRole(
		context.Background(), 7, "project-1", "user-1", types.ProductionRoleAuthor,
	))
	roles, err = repo.ListRoles(context.Background(), 7, "project-1", "user-1")
	require.NoError(t, err)
	require.Empty(t, roles)
}

func TestProductionProjectRepositoryAssignRoleRejectsNilMember(t *testing.T) {
	repo, _ := newProductionRepoTestDB(t)
	var err error

	require.NotPanics(t, func() {
		err = repo.AssignRole(context.Background(), 7, nil)
	})
	require.ErrorContains(t, err, "project member")
}

func TestProductionProjectRepositoryDuplicateLiveRoleReturnsConflict(t *testing.T) {
	repo, db := newProductionRepoTestDB(t)
	seedProductionProject(t, db, "project-1", 7)
	member := &types.ProductionProjectMember{
		ProjectID: "project-1", UserID: "user-1", Role: types.ProductionRoleAuthor, AssignedBy: "owner-1",
	}
	require.NoError(t, repo.AssignRole(context.Background(), 7, member))

	err := repo.AssignRole(context.Background(), 7, &types.ProductionProjectMember{
		ProjectID: "project-1", UserID: "user-1", Role: types.ProductionRoleAuthor, AssignedBy: "owner-1",
	})

	require.ErrorIs(t, err, types.ErrProductionConflict)
}

func TestProductionProjectRepositoryNonUniqueFailureIsNotConflict(t *testing.T) {
	repo, db := newProductionRepoTestDB(t)
	seedProductionProject(t, db, "project-1", 7)
	require.NoError(t, db.Exec(`
CREATE TRIGGER fail_production_member_write
BEFORE INSERT ON production_project_members
BEGIN
    SELECT RAISE(ABORT, 'forced non-unique write failure');
END`).Error)

	err := repo.AssignRole(context.Background(), 7, &types.ProductionProjectMember{
		ProjectID: "project-1", UserID: "user-1", Role: types.ProductionRoleAuthor, AssignedBy: "owner-1",
	})

	require.Error(t, err)
	require.NotErrorIs(t, err, types.ErrProductionConflict)
}
