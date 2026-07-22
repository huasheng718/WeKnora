package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func productionBuiltinSeedDefinitions(tenantID uint64) []types.ProductionDocumentType {
	codes := []string{"sop", "policy_process", "product_service_guide", "faq", "incident_playbook"}
	definitions := make([]types.ProductionDocumentType, 0, len(codes))
	for index, code := range codes {
		definition := productionDocumentType("builtin-"+code, tenantID, code, 1)
		definition.Name = code
		definition.Status = types.ProductionDocumentTypeActive
		definition.BlockSchema = types.JSON(`{"allowed_block_types":["paragraph"],"required_sections":["section"],"version":1}`)
		definition.ReviewPolicy = types.JSON(`{"steps":["business_reviewer"]}`)
		definition.ID = string(rune('a'+index)) + "0000000-0000-4000-8000-000000000000"
		definitions = append(definitions, *definition)
	}
	return definitions
}

func TestProductionDocumentTypeRepositorySeedsBuiltinsAndSkipsOnlyCodeCollision(t *testing.T) {
	_, db := newProductionDocumentTypeRepoTestDB(t)

	customFAQ := productionDocumentType("custom-faq", 7, "faq", 2)
	customFAQ.Name = "Tenant FAQ"
	customFAQ.Status = types.ProductionDocumentTypeRetired
	require.NoError(t, db.Create(customFAQ).Error)
	repo := NewProductionDocumentTypeRepository(db).(*productionDocumentTypeRepository)

	require.NoError(t, repo.SeedBuiltins(context.Background(), 7, "system:builtin-document-types", productionBuiltinSeedDefinitions(7)))
	require.NoError(t, repo.SeedBuiltins(context.Background(), 7, "system:builtin-document-types", productionBuiltinSeedDefinitions(7)))

	var rows []types.ProductionDocumentType
	require.NoError(t, db.Where("tenant_id = ? AND deleted_at IS NULL", 7).Order("code").Find(&rows).Error)
	require.Len(t, rows, 5)
	for _, row := range rows {
		if row.Code == "faq" {
			require.Equal(t, "Tenant FAQ", row.Name)
			require.Equal(t, types.ProductionDocumentTypeOriginCustom, row.Origin)
			require.Nil(t, row.TemplateKey)
			continue
		}
		require.Equal(t, types.ProductionDocumentTypeOriginBuiltin, row.Origin)
		require.NotNil(t, row.TemplateKey)
		require.Equal(t, row.Code, *row.TemplateKey)
		require.Equal(t, "system:builtin-document-types", row.CreatedBy)
	}
}

func TestProductionDocumentTypeRepositorySeedsJoinTransactionContext(t *testing.T) {
	_, db := newProductionDocumentTypeRepoTestDB(t)
	repo := NewProductionDocumentTypeRepository(db).(*productionDocumentTypeRepository)

	err := database.WithTransactionContext(context.Background(), db, func(txCtx context.Context) error {
		require.NoError(t, repo.SeedBuiltins(txCtx, 9, "system:builtin-document-types", productionBuiltinSeedDefinitions(9)))
		return errors.New("rollback probe")
	})
	require.ErrorContains(t, err, "rollback probe")
	var count int64
	require.NoError(t, db.Model(&types.ProductionDocumentType{}).Where("tenant_id = ?", 9).Count(&count).Error)
	require.Zero(t, count)
}

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

func TestProductionDocumentTypeRepositoryCreateRejectsNilInput(t *testing.T) {
	repo, _ := newProductionDocumentTypeRepoTestDB(t)

	err := repo.Create(context.Background(), nil)

	require.ErrorContains(t, err, "production document type")
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

	active, err := repo.Activate(context.Background(), 7, "baseline", 2)
	require.NoError(t, err)
	require.Equal(t, v2.ID, active.ID)
	require.Equal(t, types.ProductionDocumentTypeActive, active.Status)
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

	activated, err := repo.Activate(context.Background(), 7, "baseline", 2)
	require.Nil(t, activated)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

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

func TestProductionDocumentTypeRepositoryDuplicateLiveVersionReturnsConflict(t *testing.T) {
	repo, _ := newProductionDocumentTypeRepoTestDB(t)
	require.NoError(t, repo.Create(context.Background(), productionDocumentType("type-1", 7, "baseline", 1)))

	err := repo.Create(context.Background(), productionDocumentType("type-2", 7, "baseline", 1))

	require.ErrorIs(t, err, types.ErrProductionConflict)
}

func TestProductionDocumentTypeRepositoryPostgresLocksExactActiveReviewPolicy(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	mock.ExpectBegin()
	query := `SELECT \* FROM "production_document_types" WHERE .*tenant_id = \$1 AND id = \$2 AND schema_version = \$3 AND status = \$4.*FOR SHARE`
	mock.ExpectQuery(query).
		WithArgs(uint64(7), "type-1", 3, types.ProductionDocumentTypeActive, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "schema_version", "status", "review_policy"}).
			AddRow("type-1", 7, 3, string(types.ProductionDocumentTypeActive), `{"steps":["business_reviewer"]}`))
	mock.ExpectCommit()

	got, err := NewProductionDocumentTypeRepository(db).GetActiveByIDForReview(
		context.Background(), 7, "type-1", 3,
	)

	require.NoError(t, err)
	require.Equal(t, "type-1", got.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProductionDocumentTypeRepositorySQLiteReservesWriterBeforeReviewPolicyRead(t *testing.T) {
	repo, db := newProductionDocumentTypeRepoTestDB(t)
	active := productionDocumentType("type-review", 7, "review", 3)
	active.Status = types.ProductionDocumentTypeActive
	active.ReviewPolicy = types.JSON(`{"steps":["business_reviewer"]}`)
	require.NoError(t, db.Create(active).Error)
	require.NoError(t, db.Exec(`CREATE TABLE review_policy_lock_probe (count INTEGER NOT NULL)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO review_policy_lock_probe (count) VALUES (0)`).Error)
	require.NoError(t, db.Exec(`
CREATE TRIGGER capture_review_policy_writer_reservation
BEFORE UPDATE OF status ON production_document_types
FOR EACH ROW
WHEN OLD.id = 'type-review' AND OLD.status = 'active' AND NEW.status = 'active'
BEGIN
    UPDATE review_policy_lock_probe SET count = count + 1;
END`).Error)

	got, err := repo.GetActiveByIDForReview(context.Background(), 7, active.ID, 3)

	require.NoError(t, err)
	require.Equal(t, active.ID, got.ID)
	var reservations int
	require.NoError(t, db.Raw(`SELECT count FROM review_policy_lock_probe`).Scan(&reservations).Error)
	require.Equal(t, 1, reservations)

	require.NoError(t, db.Model(&types.ProductionDocumentType{}).Where("id = ?", active.ID).
		UpdateColumn("status", types.ProductionDocumentTypeRetired).Error)
	got, err = repo.GetActiveByIDForReview(context.Background(), 7, active.ID, 3)
	require.Nil(t, got)
	require.ErrorIs(t, err, types.ErrProductionDocumentTypeInactive)
}
