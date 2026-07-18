package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newProductionDocumentTypeRepoTestDB(
	t *testing.T,
) (interfaces.ProductionDocumentTypeRepository, *gorm.DB) {
	t.Helper()
	_, db := newProductionRepoTestDB(t)
	return NewProductionDocumentTypeRepository(db), db
}

func productionDocumentType(id string, tenantID uint64, code string, version int) *types.ProductionDocumentType {
	return &types.ProductionDocumentType{
		ID:                 id,
		TenantID:           tenantID,
		Code:               code,
		Name:               "Baseline",
		SchemaVersion:      version,
		BlockSchema:        types.JSON(`{}`),
		SourceRequirements: types.JSON(`{}`),
		SkillBindings:      types.JSON(`{}`),
		QualityRules:       types.JSON(`{}`),
		ReviewPolicy:       types.JSON(`{}`),
		PublicationPolicy:  types.JSON(`{}`),
		Status:             types.ProductionDocumentTypeDraft,
		CreatedBy:          "author-1",
	}
}

func TestProductionDocumentTypeRepositoryCreatesAndScopesLookups(t *testing.T) {
	repo, _ := newProductionDocumentTypeRepoTestDB(t)
	documentType := productionDocumentType("type-1", 7, "baseline", 1)

	require.NoError(t, repo.Create(context.Background(), documentType))

	got, err := repo.GetByID(context.Background(), 7, documentType.ID)
	require.NoError(t, err)
	require.Equal(t, documentType.ID, got.ID)

	got, err = repo.GetByID(context.Background(), 8, documentType.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Nil(t, got)
}

func TestProductionDocumentTypeRepositoryListsOnlyTenantRows(t *testing.T) {
	repo, db := newProductionDocumentTypeRepoTestDB(t)
	require.NoError(t, db.Create(productionDocumentType("type-1", 7, "baseline", 1)).Error)
	require.NoError(t, db.Create(productionDocumentType("type-2", 8, "baseline", 1)).Error)

	rows, err := repo.List(context.Background(), 7)

	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint64(7), rows[0].TenantID)
}

func TestDocumentTypeRepositoryActivatesOneVersion(t *testing.T) {
	repo, db := newProductionDocumentTypeRepoTestDB(t)
	v1 := productionDocumentType("type-1", 7, "baseline", 1)
	v1.Status = types.ProductionDocumentTypeActive
	v2 := productionDocumentType("type-2", 7, "baseline", 2)
	otherTenant := productionDocumentType("type-3", 8, "baseline", 1)
	otherTenant.Status = types.ProductionDocumentTypeActive
	require.NoError(t, db.Create(v1).Error)
	require.NoError(t, db.Create(v2).Error)
	require.NoError(t, db.Create(otherTenant).Error)

	require.NoError(t, repo.Activate(context.Background(), 7, "baseline", 2))

	active, err := repo.GetActiveByCode(context.Background(), 7, "baseline")
	require.NoError(t, err)
	require.Equal(t, 2, active.SchemaVersion)

	var retired types.ProductionDocumentType
	require.NoError(t, db.First(&retired, "id = ?", v1.ID).Error)
	require.Equal(t, types.ProductionDocumentTypeRetired, retired.Status)

	otherActive, err := repo.GetActiveByCode(context.Background(), 8, "baseline")
	require.NoError(t, err)
	require.Equal(t, otherTenant.ID, otherActive.ID)
}

func TestDocumentTypeRepositoryActivationRollsBackWhenDraftDoesNotExist(t *testing.T) {
	repo, db := newProductionDocumentTypeRepoTestDB(t)
	v1 := productionDocumentType("type-1", 7, "baseline", 1)
	v1.Status = types.ProductionDocumentTypeActive
	require.NoError(t, db.Create(v1).Error)

	require.ErrorIs(t, repo.Activate(context.Background(), 7, "baseline", 2), gorm.ErrRecordNotFound)

	var persisted types.ProductionDocumentType
	require.NoError(t, db.First(&persisted, "id = ?", v1.ID).Error)
	require.Equal(t, types.ProductionDocumentTypeActive, persisted.Status)
}

func TestDocumentTypeRepositoryActiveLookupRejectsCrossTenantRead(t *testing.T) {
	repo, db := newProductionDocumentTypeRepoTestDB(t)
	active := productionDocumentType("type-1", 7, "baseline", 1)
	active.Status = types.ProductionDocumentTypeActive
	require.NoError(t, db.Create(active).Error)

	got, err := repo.GetActiveByCode(context.Background(), 8, "baseline")

	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Nil(t, got)
}
