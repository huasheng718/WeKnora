package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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

func TestDeleteTenantRejectsProvisioningBeforeChildMutation(t *testing.T) {
	fixture := seedRegistrationPurgeFixture(
		t, newRegistrationPurgeDB(t), "pending-delete", types.TenantStatusProvisioning,
	)
	require.NoError(t, fixture.db.Exec(`
		CREATE TRIGGER reject_pending_membership_soft_delete
		BEFORE UPDATE OF deleted_at ON tenant_members
		WHEN OLD.tenant_id = `+fmt.Sprint(fixture.tenant.ID)+`
		BEGIN
			SELECT RAISE(ABORT, 'pending membership delete attempted');
		END
	`).Error)

	err := fixture.tenantRepo.DeleteTenant(context.Background(), fixture.tenant.ID)
	require.ErrorIs(t, err, ErrTenantNotActive)
	require.NotContains(t, err.Error(), "pending membership delete attempted")
	fixture.assertResourceCounts(t, 1)
}

func TestDeleteTenantActivePreservesRegistrationResources(t *testing.T) {
	fixture := seedRegistrationPurgeFixture(
		t, newRegistrationPurgeDB(t), "active-delete", types.TenantStatusActive,
	)

	require.NoError(t, fixture.tenantRepo.DeleteTenant(context.Background(), fixture.tenant.ID))

	for name, query := range map[string]*gorm.DB{
		"tenant":     fixture.db.Model(&types.Tenant{}).Where("id = ?", fixture.tenant.ID),
		"membership": fixture.db.Model(&types.TenantMember{}).Where("tenant_id = ?", fixture.tenant.ID),
	} {
		t.Run("hidden "+name, func(t *testing.T) {
			var count int64
			require.NoError(t, query.Count(&count).Error)
			assert.Zero(t, count)
		})
	}
	fixture.assertResourceCounts(t, 1)
}

func TestSearchTenantsReturnsOnlyActiveRowsWithAccuratePagination(t *testing.T) {
	db := setupTestDB(t)
	repo := NewTenantRepository(db)
	base := time.Now().Add(-time.Hour)
	for index, status := range []string{
		types.TenantStatusActive,
		types.TenantStatusProvisioning,
		types.TenantStatusActive,
		types.TenantStatusProvisioning,
		types.TenantStatusActive,
	} {
		require.NoError(t, db.Create(&types.Tenant{
			Name:      fmt.Sprintf("tenant-%d", index),
			Status:    status,
			CreatedAt: base.Add(time.Duration(index) * time.Minute),
		}).Error)
	}

	first, total, err := repo.SearchTenants(context.Background(), "tenant", 0, 1, 2)
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
	require.Len(t, first, 2)
	for _, tenant := range first {
		require.Equal(t, types.TenantStatusActive, tenant.Status)
	}

	second, total, err := repo.SearchTenants(context.Background(), "tenant", 0, 2, 2)
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
	require.Len(t, second, 1)
	require.Equal(t, types.TenantStatusActive, second[0].Status)

	var pending types.Tenant
	require.NoError(t, db.Where("status = ?", types.TenantStatusProvisioning).First(&pending).Error)
	items, total, err := repo.SearchTenants(context.Background(), "", pending.ID, 1, 20)
	require.NoError(t, err)
	require.Empty(t, items)
	require.Zero(t, total)
}

func TestBulkSetStorageQuotaUpdatesOnlyActiveTenants(t *testing.T) {
	db := setupTestDB(t)
	repo := NewTenantRepository(db)
	active := &types.Tenant{Name: "active-quota", Status: types.TenantStatusActive, StorageQuota: 10}
	pending := &types.Tenant{Name: "pending-quota", Status: types.TenantStatusProvisioning, StorageQuota: 20}
	require.NoError(t, db.Create(active).Error)
	require.NoError(t, db.Create(pending).Error)

	affected, err := repo.BulkSetStorageQuota(context.Background(), 99)
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)

	require.NoError(t, db.First(active, active.ID).Error)
	require.Equal(t, int64(99), active.StorageQuota)
	require.NoError(t, db.First(pending, pending.ID).Error)
	require.Equal(t, int64(20), pending.StorageQuota)
}

func TestUpdateActiveTenantRejectsSoftDeletedTenant(t *testing.T) {
	db := setupTestDB(t)
	repo := NewTenantRepository(db)
	tenant := &types.Tenant{Name: "before-delete", Status: types.TenantStatusActive}
	require.NoError(t, db.Create(tenant).Error)
	require.NoError(t, db.Delete(&types.Tenant{}, tenant.ID).Error)

	tenant.Name = "must-not-update"
	err := repo.UpdateActiveTenant(context.Background(), tenant)
	require.ErrorIs(t, err, ErrTenantNotActive)

	var stored types.Tenant
	require.NoError(t, db.Unscoped().First(&stored, tenant.ID).Error)
	require.Equal(t, "before-delete", stored.Name)
}

func TestUpdateActiveTenantAndDeleteTenantLinearizeInEitherOrder(t *testing.T) {
	newFixture := func(t *testing.T, suffix string) (*gorm.DB, interfaces.TenantRepository, *types.Tenant) {
		t.Helper()
		dsn := fmt.Sprintf("file:%s/%s.db?_busy_timeout=5000&_journal_mode=WAL", t.TempDir(), suffix)
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(4)
		require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.TenantMember{}))
		tenant := &types.Tenant{Name: "before-" + suffix, Status: types.TenantStatusActive}
		require.NoError(t, db.Create(tenant).Error)
		return db, NewTenantRepository(db), tenant
	}

	t.Run("delete first rejects update", func(t *testing.T) {
		db, repo, tenant := newFixture(t, "delete-first")
		deleteStart := make(chan struct{})
		deleteDone := make(chan error, 1)
		updateDone := make(chan error, 1)
		go func() {
			<-deleteStart
			deleteDone <- repo.DeleteTenant(context.Background(), tenant.ID)
		}()
		go func() {
			deleteErr := <-deleteDone
			if deleteErr != nil {
				updateDone <- deleteErr
				return
			}
			tenant.Name = "after-delete"
			updateDone <- repo.UpdateActiveTenant(context.Background(), tenant)
		}()
		close(deleteStart)

		require.ErrorIs(t, <-updateDone, ErrTenantNotActive)
		var stored types.Tenant
		require.NoError(t, db.Unscoped().First(&stored, tenant.ID).Error)
		require.Equal(t, "before-delete-first", stored.Name)
		require.True(t, stored.DeletedAt.Valid)
	})

	t.Run("update first succeeds before delete", func(t *testing.T) {
		db, repo, tenant := newFixture(t, "update-first")
		updateStart := make(chan struct{})
		updateDone := make(chan error, 1)
		deleteDone := make(chan error, 1)
		go func() {
			<-updateStart
			tenant.Name = "updated-first"
			updateDone <- repo.UpdateActiveTenant(context.Background(), tenant)
		}()
		go func() {
			updateErr := <-updateDone
			if updateErr != nil {
				deleteDone <- updateErr
				return
			}
			deleteDone <- repo.DeleteTenant(context.Background(), tenant.ID)
		}()
		close(updateStart)

		require.NoError(t, <-deleteDone)
		var stored types.Tenant
		require.NoError(t, db.Unscoped().First(&stored, tenant.ID).Error)
		require.Equal(t, "updated-first", stored.Name)
		require.True(t, stored.DeletedAt.Valid)
	})
}

func TestUpdateActiveTenantPostgresRequiresActiveRowAndAffectedRow(t *testing.T) {
	for _, test := range []struct {
		name         string
		rowsAffected int64
		wantErr      error
	}{
		{name: "active row updated", rowsAffected: 1},
		{name: "missing active row rejected", rowsAffected: 0, wantErr: ErrTenantNotActive},
	} {
		t.Run(test.name, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
				Logger: logger.Default.LogMode(logger.Silent),
			})
			require.NoError(t, err)
			repo := NewTenantRepository(db)
			tenant := &types.Tenant{ID: 31, Name: "cas", Status: types.TenantStatusActive}

			mock.ExpectBegin()
			mock.ExpectExec(`UPDATE "tenants" SET .* WHERE \(id = \$[0-9]+ AND status = \$[0-9]+\) AND "tenants"\."deleted_at" IS NULL`).
				WillReturnResult(sqlmock.NewResult(0, test.rowsAffected))
			mock.ExpectCommit()

			err = repo.UpdateActiveTenant(context.Background(), tenant)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestDeleteTenantAndRegistrationPurgeDoNotOrphanPendingRegistration(t *testing.T) {
	dsn := fmt.Sprintf("file:%s/tenant-lifecycle.db?_busy_timeout=5000&_journal_mode=WAL", t.TempDir())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.User{},
		&types.TenantMember{},
		&types.StorageBackend{},
		&types.ProductionDocumentType{},
	))
	fixture := seedRegistrationPurgeFixture(t, db, "concurrent", types.TenantStatusProvisioning)

	start := make(chan struct{})
	var wg sync.WaitGroup
	var deleteErr, purgeErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		deleteErr = fixture.tenantRepo.DeleteTenant(context.Background(), fixture.tenant.ID)
	}()
	go func() {
		defer wg.Done()
		<-start
		purgeErr = fixture.tenantRepo.PurgeProvisionedRegistration(
			context.Background(), fixture.tenant.ID, fixture.user.ID,
		)
	}()
	close(start)
	wg.Wait()

	require.ErrorIs(t, deleteErr, ErrTenantNotActive)
	require.NoError(t, purgeErr)
	fixture.assertResourceCounts(t, 0)
}

func TestDeleteTenantPostgresLocksTenantBeforeMemberships(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	repo := NewTenantRepository(db)
	const tenantID uint64 = 17

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT "status" FROM "tenants" WHERE id = \$1 AND "tenants"\."deleted_at" IS NULL LIMIT \$2 FOR UPDATE`).
		WithArgs(tenantID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(types.TenantStatusActive))
	mock.ExpectExec(`UPDATE "tenant_members" SET "deleted_at"=\$1 WHERE tenant_id = \$2 AND "tenant_members"\."deleted_at" IS NULL`).
		WithArgs(sqlmock.AnyArg(), tenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "tenants" SET "deleted_at"=\$1 WHERE \(id = \$2 AND status = \$3\) AND "tenants"\."deleted_at" IS NULL`).
		WithArgs(sqlmock.AnyArg(), tenantID, types.TenantStatusActive).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.DeleteTenant(context.Background(), tenantID))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPurgeProvisionedTenantPostgresLocksTenantBeforeChildren(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	repo := NewTenantRepository(db)
	const tenantID uint64 = 23

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT "status" FROM "tenants" WHERE id = \$1 LIMIT \$2 FOR UPDATE`).
		WithArgs(tenantID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(types.TenantStatusProvisioning))
	mock.ExpectExec(`DELETE FROM "production_document_types" WHERE tenant_id = \$1`).
		WithArgs(tenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM "storage_backends" WHERE tenant_id = \$1`).
		WithArgs(tenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM "tenant_members" WHERE tenant_id = \$1`).
		WithArgs(tenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM "tenants" WHERE id = \$1 AND status = \$2`).
		WithArgs(tenantID, types.TenantStatusProvisioning).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.PurgeProvisionedTenant(context.Background(), tenantID))
	require.NoError(t, mock.ExpectationsWereMet())
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
	require.NoError(t, db.Exec(`
		CREATE TRIGGER reject_active_tenant_purge_child_delete
		BEFORE DELETE ON tenant_members
		WHEN OLD.tenant_id = `+fmt.Sprint(tenant.ID)+`
		BEGIN
			SELECT RAISE(ABORT, 'active purge child delete attempted');
		END
	`).Error)

	err := repo.PurgeProvisionedTenant(context.Background(), tenant.ID)
	require.ErrorIs(t, err, ErrTenantNotProvisioning)
	require.NotContains(t, err.Error(), "active purge child delete attempted")

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
