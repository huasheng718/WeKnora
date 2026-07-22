package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
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

	tenant := &types.Tenant{Name: "failed-provisioning", Status: "active"}
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
	tenant := &types.Tenant{Name: "atomic-purge", Status: "active"}
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
