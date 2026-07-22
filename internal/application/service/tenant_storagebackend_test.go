package service_test

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
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
}

func (failingBuiltinSeeder) SeedBuiltins(context.Context, uint64, string, []types.ProductionDocumentType) error {
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
	tenantSvc := service.NewTenantService(
		tenantRepo, storageRepo, failingBuiltinSeeder{ProductionDocumentTypeRepository: documentTypes}, uow, catalog,
	)

	created, err := tenantSvc.CreateTenant(context.Background(), &types.Tenant{Name: "rollback-workspace"})
	require.EqualError(t, err, "seed failure")
	require.Nil(t, created)

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
