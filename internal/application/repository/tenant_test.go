package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupTestDB creates an in-memory SQLite database with tenant table.
func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.TenantMember{}))
	return db
}

func TestDeleteTenant_SoftDeletesMemberships(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewTenantRepository(db)

	tenant := &types.Tenant{Name: "gone", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)

	member := &types.TenantMember{
		UserID:   "user-1",
		TenantID: tenant.ID,
		Role:     types.TenantRoleOwner,
		Status:   types.TenantMemberStatusActive,
	}
	require.NoError(t, db.Create(member).Error)

	require.NoError(t, repo.DeleteTenant(ctx, tenant.ID))

	var tenantCount int64
	require.NoError(t, db.Model(&types.Tenant{}).Count(&tenantCount).Error)
	assert.Equal(t, int64(0), tenantCount)

	var memberCount int64
	require.NoError(t, db.Model(&types.TenantMember{}).Count(&memberCount).Error)
	assert.Equal(t, int64(0), memberCount)

	// Unscoped: rows still exist but are soft-deleted.
	var rawTenantCount int64
	require.NoError(t, db.Unscoped().Model(&types.Tenant{}).Count(&rawTenantCount).Error)
	assert.Equal(t, int64(1), rawTenantCount)

	var rawMemberCount int64
	require.NoError(t, db.Unscoped().Model(&types.TenantMember{}).Count(&rawMemberCount).Error)
	assert.Equal(t, int64(1), rawMemberCount)
}

func TestPurgeProvisionedTenantHardDeletesAllProvisioningRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.TenantMember{},
		&types.StorageBackend{},
		&types.ProductionDocumentType{},
	))
	repo := NewTenantRepository(db)
	ctx := context.Background()

	tenant := &types.Tenant{Name: "failed-provisioning", Status: types.TenantStatusProvisioning}
	require.NoError(t, db.Create(tenant).Error)
	member := &types.TenantMember{
		UserID: "failed-owner", TenantID: tenant.ID,
		Role: types.TenantRoleOwner, Status: types.TenantMemberStatusActive,
	}
	require.NoError(t, db.Create(member).Error)
	require.NoError(t, db.Delete(member).Error, "simulate an earlier best-effort membership rollback")
	require.NoError(t, db.Create(&types.StorageBackend{
		ID: "failed-storage", TenantID: tenant.ID, Name: "default", Provider: "local",
		Config: types.StorageBackendConfig{
			Mode: "local", AccessKeyID: "snapshotted-access", SecretAccessKey: "snapshotted-secret",
		},
		Source: types.StorageBackendSourceEnv, Status: types.StorageBackendStatusActive, LegacyAlias: true,
	}).Error)
	templateKey := "sop"
	require.NoError(t, db.Create(&types.ProductionDocumentType{
		ID: "failed-builtin", TenantID: tenant.ID, Code: "sop", Name: "SOP",
		SchemaVersion: 1, Status: types.ProductionDocumentTypeActive,
		Origin: types.ProductionDocumentTypeOriginBuiltin, TemplateKey: &templateKey,
		CreatedBy: "system:builtin-document-types",
	}).Error)

	for name, query := range map[string]*gorm.DB{
		"tenant":        db.Unscoped().Model(&types.Tenant{}).Where("id = ?", tenant.ID),
		"membership":    db.Unscoped().Model(&types.TenantMember{}).Where("tenant_id = ?", tenant.ID),
		"storage":       db.Unscoped().Model(&types.StorageBackend{}).Where("tenant_id = ?", tenant.ID),
		"document type": db.Unscoped().Model(&types.ProductionDocumentType{}).Where("tenant_id = ?", tenant.ID),
	} {
		t.Run("precondition "+name, func(t *testing.T) {
			var count int64
			require.NoError(t, query.Count(&count).Error)
			assert.Equal(t, int64(1), count)
		})
	}

	require.NoError(t, repo.PurgeProvisionedTenant(ctx, tenant.ID))

	for name, query := range map[string]*gorm.DB{
		"tenant":        db.Unscoped().Model(&types.Tenant{}).Where("id = ?", tenant.ID),
		"membership":    db.Unscoped().Model(&types.TenantMember{}).Where("tenant_id = ?", tenant.ID),
		"storage":       db.Unscoped().Model(&types.StorageBackend{}).Where("tenant_id = ?", tenant.ID),
		"document type": db.Unscoped().Model(&types.ProductionDocumentType{}).Where("tenant_id = ?", tenant.ID),
	} {
		t.Run("purged "+name, func(t *testing.T) {
			var count int64
			require.NoError(t, query.Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestPurgeProvisionedTenantRollsBackEveryDeleteWhenFinalDeleteFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.TenantMember{},
		&types.StorageBackend{},
		&types.ProductionDocumentType{},
	))
	repo := NewTenantRepository(db)
	tenant := &types.Tenant{Name: "atomic-purge", Status: types.TenantStatusProvisioning}
	require.NoError(t, db.Create(tenant).Error)
	require.NoError(t, db.Create(&types.TenantMember{
		UserID: "owner", TenantID: tenant.ID, Role: types.TenantRoleOwner,
		Status: types.TenantMemberStatusActive,
	}).Error)
	require.NoError(t, db.Create(&types.StorageBackend{
		ID: "atomic-storage", TenantID: tenant.ID, Name: "default", Provider: "local",
		Source: types.StorageBackendSourceEnv, Status: types.StorageBackendStatusActive,
	}).Error)
	templateKey := "faq"
	require.NoError(t, db.Create(&types.ProductionDocumentType{
		ID: "atomic-builtin", TenantID: tenant.ID, Code: "faq", Name: "FAQ",
		SchemaVersion: 1, Status: types.ProductionDocumentTypeActive,
		Origin: types.ProductionDocumentTypeOriginBuiltin, TemplateKey: &templateKey,
		CreatedBy: "system:builtin-document-types",
	}).Error)
	require.NoError(t, db.Exec(`
		CREATE TRIGGER block_failed_provisioning_tenant_delete
		BEFORE DELETE ON tenants
		WHEN OLD.id = `+fmt.Sprint(tenant.ID)+`
		BEGIN
			SELECT RAISE(ABORT, 'tenant delete blocked');
		END
	`).Error)

	err = repo.PurgeProvisionedTenant(context.Background(), tenant.ID)
	require.ErrorContains(t, err, "tenant delete blocked")

	for name, query := range map[string]*gorm.DB{
		"tenant":        db.Unscoped().Model(&types.Tenant{}).Where("id = ?", tenant.ID),
		"membership":    db.Unscoped().Model(&types.TenantMember{}).Where("tenant_id = ?", tenant.ID),
		"storage":       db.Unscoped().Model(&types.StorageBackend{}).Where("tenant_id = ?", tenant.ID),
		"document type": db.Unscoped().Model(&types.ProductionDocumentType{}).Where("tenant_id = ?", tenant.ID),
	} {
		t.Run(name, func(t *testing.T) {
			var count int64
			require.NoError(t, query.Count(&count).Error)
			assert.Equal(t, int64(1), count, "earlier deletes must roll back with the final failure")
		})
	}
}

func TestPurgeProvisionedTenantRejectsActiveTenant(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&types.StorageBackend{}, &types.ProductionDocumentType{}))
	repo := NewTenantRepository(db)
	tenant := &types.Tenant{Name: "active-workspace", Status: types.TenantStatusActive}
	require.NoError(t, db.Create(tenant).Error)
	require.NoError(t, db.Create(&types.TenantMember{
		UserID: "owner", TenantID: tenant.ID, Role: types.TenantRoleOwner,
		Status: types.TenantMemberStatusActive,
	}).Error)

	err := repo.PurgeProvisionedTenant(context.Background(), tenant.ID)
	require.ErrorIs(t, err, ErrTenantNotProvisioning)

	var tenantCount int64
	require.NoError(t, db.Unscoped().Model(&types.Tenant{}).Where("id = ?", tenant.ID).Count(&tenantCount).Error)
	assert.Equal(t, int64(1), tenantCount)
	var memberCount int64
	require.NoError(t, db.Unscoped().Model(&types.TenantMember{}).Where("tenant_id = ?", tenant.ID).Count(&memberCount).Error)
	assert.Equal(t, int64(1), memberCount)
}

type registrationPurgeFixture struct {
	db         *gorm.DB
	tenantRepo interfaces.TenantRepository
	userRepo   interfaces.UserRepository
	tenant     *types.Tenant
	user       *types.User
}

func newRegistrationPurgeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.User{},
		&types.TenantMember{},
		&types.StorageBackend{},
		&types.ProductionDocumentType{},
	))
	return db
}

func seedRegistrationPurgeFixture(
	t *testing.T,
	db *gorm.DB,
	suffix string,
	status string,
) *registrationPurgeFixture {
	t.Helper()
	tenantRepo := NewTenantRepository(db)
	userRepo := NewUserRepository(db)
	tenant := &types.Tenant{Name: "registration-" + suffix, Status: status}
	require.NoError(t, tenantRepo.CreateTenant(context.Background(), tenant))
	user := &types.User{
		ID: "user-" + suffix, Username: "user-" + suffix, Email: "user-" + suffix + "@example.com",
		PasswordHash: "hash", TenantID: tenant.ID, IsActive: true,
	}
	require.NoError(t, userRepo.CreateUser(context.Background(), user))
	require.NoError(t, db.Create(&types.TenantMember{
		UserID: user.ID, TenantID: tenant.ID, Role: types.TenantRoleOwner,
		Status: types.TenantMemberStatusActive,
	}).Error)
	require.NoError(t, db.Create(&types.StorageBackend{
		ID: "storage-" + suffix, TenantID: tenant.ID, Name: "default", Provider: "local",
		Source: types.StorageBackendSourceEnv, Status: types.StorageBackendStatusActive,
	}).Error)
	templateKey := "sop"
	require.NoError(t, db.Create(&types.ProductionDocumentType{
		ID: "builtin-" + suffix, TenantID: tenant.ID, Code: "sop", Name: "SOP",
		SchemaVersion: 1, Status: types.ProductionDocumentTypeActive,
		Origin: types.ProductionDocumentTypeOriginBuiltin, TemplateKey: &templateKey,
		CreatedBy: "system:builtin-document-types",
	}).Error)
	return &registrationPurgeFixture{
		db: db, tenantRepo: tenantRepo, userRepo: userRepo, tenant: tenant, user: user,
	}
}

func (f *registrationPurgeFixture) assertResourceCounts(t *testing.T, want int64) {
	t.Helper()
	queries := map[string]*gorm.DB{
		"tenant":        f.db.Unscoped().Model(&types.Tenant{}).Where("id = ?", f.tenant.ID),
		"user":          f.db.Unscoped().Model(&types.User{}).Where("id = ?", f.user.ID),
		"membership":    f.db.Unscoped().Model(&types.TenantMember{}).Where("tenant_id = ?", f.tenant.ID),
		"storage":       f.db.Unscoped().Model(&types.StorageBackend{}).Where("tenant_id = ?", f.tenant.ID),
		"document type": f.db.Unscoped().Model(&types.ProductionDocumentType{}).Where("tenant_id = ?", f.tenant.ID),
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			var count int64
			require.NoError(t, query.Count(&count).Error)
			assert.Equal(t, want, count)
		})
	}
}

func TestPurgeProvisionedRegistrationHardDeletesUserAndTenantResources(t *testing.T) {
	fixture := seedRegistrationPurgeFixture(
		t, newRegistrationPurgeDB(t), "retry", types.TenantStatusProvisioning,
	)
	fixture.assertResourceCounts(t, 1)

	require.NoError(t, fixture.tenantRepo.PurgeProvisionedRegistration(
		context.Background(), fixture.tenant.ID, fixture.user.ID,
	))
	fixture.assertResourceCounts(t, 0)

	retry := &types.User{
		ID: "retry-user", Username: fixture.user.Username, Email: fixture.user.Email,
		PasswordHash: "new-hash", IsActive: true,
	}
	require.NoError(t, fixture.userRepo.CreateUser(context.Background(), retry))
}

func TestPurgeProvisionedRegistrationRejectsActiveTenant(t *testing.T) {
	fixture := seedRegistrationPurgeFixture(
		t, newRegistrationPurgeDB(t), "active", types.TenantStatusActive,
	)
	require.NoError(t, fixture.db.Exec(`
		CREATE TRIGGER reject_active_registration_child_delete
		BEFORE DELETE ON production_document_types
		WHEN OLD.tenant_id = `+fmt.Sprint(fixture.tenant.ID)+`
		BEGIN
			SELECT RAISE(ABORT, 'active child delete attempted');
		END
	`).Error)

	err := fixture.tenantRepo.PurgeProvisionedRegistration(
		context.Background(), fixture.tenant.ID, fixture.user.ID,
	)
	require.ErrorIs(t, err, ErrTenantNotProvisioning)
	fixture.assertResourceCounts(t, 1)
}

func TestPurgeProvisionedRegistrationRejectsMismatchedUserAndTenant(t *testing.T) {
	db := newRegistrationPurgeDB(t)
	first := seedRegistrationPurgeFixture(t, db, "first", types.TenantStatusProvisioning)
	second := seedRegistrationPurgeFixture(t, db, "second", types.TenantStatusProvisioning)

	err := first.tenantRepo.PurgeProvisionedRegistration(
		context.Background(), first.tenant.ID, second.user.ID,
	)
	require.Error(t, err)
	first.assertResourceCounts(t, 1)
	second.assertResourceCounts(t, 1)
}

func TestPurgeProvisionedRegistrationRollsBackEveryDeleteWhenTenantDeleteFails(t *testing.T) {
	fixture := seedRegistrationPurgeFixture(
		t, newRegistrationPurgeDB(t), "atomic", types.TenantStatusProvisioning,
	)
	require.NoError(t, fixture.db.Exec(`
		CREATE TRIGGER block_registration_tenant_delete
		BEFORE DELETE ON tenants
		WHEN OLD.id = `+fmt.Sprint(fixture.tenant.ID)+`
		BEGIN
			SELECT RAISE(ABORT, 'registration tenant delete blocked');
		END
	`).Error)

	err := fixture.tenantRepo.PurgeProvisionedRegistration(
		context.Background(), fixture.tenant.ID, fixture.user.ID,
	)
	require.ErrorContains(t, err, "registration tenant delete blocked")
	fixture.assertResourceCounts(t, 1)
}
