package service_test

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCreateTenantCreatesConcreteDefaultStorageBackend(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "local")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.StorageBackend{}, &types.ProductionDocumentType{}))
	tenantRepo := repository.NewTenantRepository(db)
	storageRepo := repository.NewStorageBackendRepository(db)
	documentTypes := repository.NewProductionDocumentTypeRepository(db)
	uow := repository.NewProductionUnitOfWork(db)
	catalog, err := service.NewProductionBuiltinCatalog()
	require.NoError(t, err)
	tenantSvc := service.NewTenantService(tenantRepo, storageRepo, documentTypes, uow, catalog)

	tenant, err := tenantSvc.CreateTenant(context.Background(), &types.Tenant{Name: "workspace"})
	require.NoError(t, err)
	require.NotNil(t, tenant.DefaultStorageBackendID)

	backend, err := storageRepo.GetByID(context.Background(), tenant.ID, *tenant.DefaultStorageBackendID)
	require.NoError(t, err)
	require.NotNil(t, backend)
	assert.Equal(t, "local", backend.Provider)
	assert.Equal(t, types.StorageBackendSourceEnv, backend.Source)
	assert.True(t, backend.LegacyAlias)

	persistedTenant, err := tenantRepo.GetTenantByID(context.Background(), tenant.ID)
	require.NoError(t, err)
	require.NotNil(t, persistedTenant.DefaultStorageBackendID)
	backends, err := storageRepo.List(context.Background(), tenant.ID)
	require.NoError(t, err)
	require.Len(t, backends, 1)
	assert.Equal(t, backends[0].ID, *persistedTenant.DefaultStorageBackendID)
}

func TestCreateTenantCreatesExactlyFiveActiveBuiltins(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "local")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.StorageBackend{}, &types.ProductionDocumentType{}))
	tenantRepo := repository.NewTenantRepository(db)
	storageRepo := repository.NewStorageBackendRepository(db)
	documentTypes := repository.NewProductionDocumentTypeRepository(db)
	uow := repository.NewProductionUnitOfWork(db)
	catalog, err := service.NewProductionBuiltinCatalog()
	require.NoError(t, err)
	tenantSvc := service.NewTenantService(tenantRepo, storageRepo, documentTypes, uow, catalog)

	tenant, err := tenantSvc.CreateTenant(context.Background(), &types.Tenant{Name: "workspace-with-builtins"})
	require.NoError(t, err)

	var builtins []types.ProductionDocumentType
	require.NoError(t, db.Where("tenant_id = ?", tenant.ID).Find(&builtins).Error)
	require.Len(t, builtins, 5)
	codes := make([]string, 0, len(builtins))
	for _, builtin := range builtins {
		require.Equal(t, types.ProductionDocumentTypeActive, builtin.Status)
		require.Equal(t, types.ProductionDocumentTypeOriginBuiltin, builtin.Origin)
		require.NotNil(t, builtin.TemplateKey)
		require.Equal(t, builtin.Code, *builtin.TemplateKey)
		codes = append(codes, builtin.Code)
	}
	sort.Strings(codes)
	assert.Equal(t, []string{"faq", "incident_playbook", "policy_process", "product_service_guide", "sop"}, codes)
}

type failingBuiltinSeeder struct {
	interfaces.ProductionDocumentTypeRepository
	partialRows int64
}

func (f *failingBuiltinSeeder) SeedBuiltins(
	ctx context.Context,
	tenantID uint64,
	actor string,
	definitions []types.ProductionDocumentType,
) error {
	tx := database.DBFromContext(ctx, nil)
	if tx == nil {
		return errors.New("transaction context is required")
	}
	if len(definitions) == 0 {
		return errors.New("built-in definitions are required")
	}
	if err := f.ProductionDocumentTypeRepository.SeedBuiltins(ctx, tenantID, actor, definitions[:1]); err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Unscoped().Model(&types.ProductionDocumentType{}).
		Where("tenant_id = ?", tenantID).Count(&f.partialRows).Error; err != nil {
		return err
	}
	if f.partialRows != 1 {
		return errors.New("partial built-in seed did not join transaction")
	}
	return errors.New("seed failure")
}

func TestCreateTenantBuiltinFailureRollsBackAllProvisioningRows(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "local")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.StorageBackend{}, &types.ProductionDocumentType{}))
	tenantRepo := repository.NewTenantRepository(db)
	storageRepo := repository.NewStorageBackendRepository(db)
	documentTypes := repository.NewProductionDocumentTypeRepository(db)
	uow := repository.NewProductionUnitOfWork(db)
	catalog, err := service.NewProductionBuiltinCatalog()
	require.NoError(t, err)
	seeder := &failingBuiltinSeeder{ProductionDocumentTypeRepository: documentTypes}
	tenantSvc := service.NewTenantService(
		tenantRepo, storageRepo, seeder, uow, catalog,
	)

	created, err := tenantSvc.CreateTenant(context.Background(), &types.Tenant{Name: "rollback-workspace"})
	require.EqualError(t, err, "seed failure")
	require.Nil(t, created)
	require.Equal(t, int64(1), seeder.partialRows)

	for name, model := range map[string]any{
		"tenant":        &types.Tenant{},
		"storage":       &types.StorageBackend{},
		"document type": &types.ProductionDocumentType{},
	} {
		t.Run(name, func(t *testing.T) {
			var count int64
			require.NoError(t, db.Unscoped().Model(model).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func newTenantLifecycleService(t *testing.T) (interfaces.TenantService, interfaces.TenantRepository) {
	t.Helper()
	t.Setenv("STORAGE_TYPE", "local")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.StorageBackend{}, &types.ProductionDocumentType{}))
	tenantRepo := repository.NewTenantRepository(db)
	storageRepo := repository.NewStorageBackendRepository(db)
	documentTypes := repository.NewProductionDocumentTypeRepository(db)
	uow := repository.NewProductionUnitOfWork(db)
	catalog, err := service.NewProductionBuiltinCatalog()
	require.NoError(t, err)
	return service.NewTenantService(tenantRepo, storageRepo, documentTypes, uow, catalog), tenantRepo
}

func TestCreateTenantRemainsHiddenUntilActivated(t *testing.T) {
	ctx := context.Background()
	tenantSvc, tenantRepo := newTenantLifecycleService(t)

	created, err := tenantSvc.CreateTenant(ctx, &types.Tenant{Name: "pending-workspace"})
	require.NoError(t, err)
	assert.Equal(t, types.TenantStatusProvisioning, created.Status)

	raw, err := tenantRepo.GetTenantByID(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, types.TenantStatusProvisioning, raw.Status)

	visible, err := tenantSvc.GetTenantByID(ctx, created.ID)
	require.Error(t, err)
	assert.Nil(t, visible)

	visibleForUser, err := tenantSvc.GetTenantByIDForUser(ctx, created.ID, "owner")
	require.Error(t, err)
	assert.Nil(t, visibleForUser)

	visibleByID, err := tenantSvc.GetTenantsByIDs(ctx, []uint64{created.ID})
	require.NoError(t, err)
	assert.NotContains(t, visibleByID, created.ID)
}

func TestActivateProvisionedTenantMakesTenantVisible(t *testing.T) {
	ctx := context.Background()
	tenantSvc, _ := newTenantLifecycleService(t)
	created, err := tenantSvc.CreateTenant(ctx, &types.Tenant{Name: "activation-workspace"})
	require.NoError(t, err)

	activated, err := tenantSvc.ActivateProvisionedTenant(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, activated)
	assert.Equal(t, types.TenantStatusActive, activated.Status)

	visible, err := tenantSvc.GetTenantByID(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, types.TenantStatusActive, visible.Status)

	visibleByID, err := tenantSvc.GetTenantsByIDs(ctx, []uint64{created.ID})
	require.NoError(t, err)
	require.Contains(t, visibleByID, created.ID)
	assert.Equal(t, types.TenantStatusActive, visibleByID[created.ID].Status)
}

func TestActivateProvisionedTenantFailsClosed(t *testing.T) {
	ctx := context.Background()
	tenantSvc, tenantRepo := newTenantLifecycleService(t)
	created, err := tenantSvc.CreateTenant(ctx, &types.Tenant{Name: "single-activation-workspace"})
	require.NoError(t, err)
	_, err = tenantSvc.ActivateProvisionedTenant(ctx, created.ID)
	require.NoError(t, err)

	for name, id := range map[string]uint64{
		"zero ID":           0,
		"missing tenant":    created.ID + 1000,
		"second activation": created.ID,
	} {
		t.Run(name, func(t *testing.T) {
			activated, err := tenantSvc.ActivateProvisionedTenant(ctx, id)
			require.Error(t, err)
			assert.Nil(t, activated)
		})
	}

	wrongStatus := &types.Tenant{Name: "disabled-workspace", Status: "disabled"}
	require.NoError(t, tenantRepo.CreateTenant(ctx, wrongStatus))
	activated, err := tenantSvc.ActivateProvisionedTenant(ctx, wrongStatus.ID)
	require.Error(t, err)
	assert.Nil(t, activated)
	raw, err := tenantRepo.GetTenantByID(ctx, wrongStatus.ID)
	require.NoError(t, err)
	assert.Equal(t, "disabled", raw.Status)
}
