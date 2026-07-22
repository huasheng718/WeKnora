package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
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
	require.NoError(t, db.AutoMigrate(&types.TenantMember{}))
	require.NoError(t, db.Create(&types.TenantMember{
		UserID: "author-1", TenantID: 7, Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusActive,
	}).Error)
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

func validDerivedProductionDocumentType(id string, tenantID uint64) *types.ProductionDocumentType {
	return &types.ProductionDocumentType{
		ID: id, TenantID: tenantID, Name: "Tenant SOP", Description: "Derived definition",
		BlockSchema:        types.JSON(`{"allowed_block_types":["paragraph"],"required_sections":["Scope"],"version":1}`),
		SourceRequirements: types.JSON(`{"allow_unsupported_facts":false,"allowed_source_kinds":["upload"],"min_accepted_evidence":1,"require_evidence_section":true,"version":1}`),
		SkillBindings:      types.JSON(`{"skills":[],"version":1}`),
		WorkflowPlan:       types.JSON(`{"steps":[],"version":1}`),
		QualityRules:       types.JSON(`{"block_needs_confirmation":true,"gates":["section_completeness"],"require_evidence_for_facts":true,"version":1}`),
		ReviewPolicy:       types.JSON(`{"steps":["business_reviewer"]}`),
		PublicationPolicy:  types.JSON(`{"chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true,"target_type":"knowledge_base","version":1}`),
		CreatedBy:          "author-1",
	}
}

func TestDocumentTypeRepositoryDerivesDraftWithImmutableLineageAndNextVersion(t *testing.T) {
	repo, db := newProductionDocumentTypeRepoTestDB(t)
	templateKey := "sop"
	base := productionDocumentType("base-active", 7, "sop", 1)
	base.Status = types.ProductionDocumentTypeActive
	base.Origin = types.ProductionDocumentTypeOriginBuiltin
	base.TemplateKey = &templateKey
	retired := productionDocumentType("base-retired", 7, "sop", 2)
	retired.Status = types.ProductionDocumentTypeRetired
	retired.Origin = types.ProductionDocumentTypeOriginCustom
	retired.TemplateKey = &templateKey
	require.NoError(t, db.Create(base).Error)
	require.NoError(t, db.Create(retired).Error)

	draft := validDerivedProductionDocumentType("derived-1", 7)
	draft.Code = "client-code"
	draft.SchemaVersion = 99
	draft.Status = types.ProductionDocumentTypeActive
	draft.Origin = types.ProductionDocumentTypeOriginBuiltin
	clientTemplate := "client-template"
	draft.TemplateKey = &clientTemplate
	derived, err := repo.DeriveDraft(context.Background(), 7, retired.ID, "author-1", draft)

	require.NoError(t, err)
	require.Equal(t, "sop", derived.Code)
	require.Equal(t, 3, derived.SchemaVersion)
	require.Equal(t, types.ProductionDocumentTypeDraft, derived.Status)
	require.Equal(t, types.ProductionDocumentTypeOriginCustom, derived.Origin)
	require.NotNil(t, derived.TemplateKey)
	require.Equal(t, templateKey, *derived.TemplateKey)
	require.Equal(t, "Tenant SOP", derived.Name)
}

func TestDocumentTypeRepositoryDeriveRejectsCrossTenantBase(t *testing.T) {
	repo, db := newProductionDocumentTypeRepoTestDB(t)
	base := productionDocumentType("base-tenant-8", 8, "sop", 1)
	base.Status = types.ProductionDocumentTypeActive
	require.NoError(t, db.Create(base).Error)

	derived, err := repo.DeriveDraft(context.Background(), 7, base.ID, "author-1", validDerivedProductionDocumentType("derived-cross-tenant", 7))

	require.Nil(t, derived)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestDocumentTypeRepositoryDeriveRejectsDraftBase(t *testing.T) {
	repo, db := newProductionDocumentTypeRepoTestDB(t)
	base := productionDocumentType("base-draft", 7, "sop", 1)
	base.Status = types.ProductionDocumentTypeDraft
	require.NoError(t, db.Create(base).Error)

	derived, err := repo.DeriveDraft(
		context.Background(), 7, base.ID, "author-1",
		validDerivedProductionDocumentType("derived-from-draft", 7),
	)

	require.Nil(t, derived)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	var count int64
	require.NoError(t, db.Model(&types.ProductionDocumentType{}).
		Where("tenant_id = ? AND id = ?", 7, "derived-from-draft").Count(&count).Error)
	require.Zero(t, count)
}

func TestDocumentTypeRepositoryDeriveRevalidatesActiveAdminInsideWriteTransaction(t *testing.T) {
	for _, test := range []struct {
		name   string
		revoke func(*gorm.DB) error
	}{
		{
			name: "demoted",
			revoke: func(db *gorm.DB) error {
				return db.Model(&types.TenantMember{}).
					Where("tenant_id = ? AND user_id = ?", 7, "author-1").
					Update("role", types.TenantRoleContributor).Error
			},
		},
		{
			name: "removed",
			revoke: func(db *gorm.DB) error {
				return db.Where("tenant_id = ? AND user_id = ?", 7, "author-1").
					Delete(&types.TenantMember{}).Error
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, db := newProductionDocumentTypeRepoTestDB(t)
			base := productionDocumentType("base-reauth", 7, "sop", 1)
			base.Status = types.ProductionDocumentTypeActive
			require.NoError(t, db.Create(base).Error)
			require.NoError(t, test.revoke(db))

			derived, err := repo.DeriveDraft(
				context.Background(), 7, base.ID, "author-1", validDerivedProductionDocumentType("derived-reauth", 7),
			)

			require.Nil(t, derived)
			require.ErrorIs(t, err, types.ErrProductionForbidden)
			var count int64
			require.NoError(t, db.Model(&types.ProductionDocumentType{}).
				Where("tenant_id = ? AND id = ?", 7, "derived-reauth").Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestDocumentTypeRepositoryDeriveConcurrentRequestsAllocateConsecutiveVersions(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "document-type.db") +
		"?_foreign_keys=1&_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migration, err := os.ReadFile(filepath.Join(
		filepath.Dir(filename), "../../../migrations/sqlite/000001_knowledge_production_foundation.up.sql",
	))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)
	require.NoError(t, db.Exec(`ALTER TABLE production_document_types ADD COLUMN template_key VARCHAR(255)`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE production_document_types ADD COLUMN origin VARCHAR(16) NOT NULL DEFAULT 'custom'`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_production_document_types_live_template_version ON production_document_types (tenant_id, template_key, schema_version) WHERE template_key IS NOT NULL AND deleted_at IS NULL`).Error)
	require.NoError(t, db.AutoMigrate(&types.TenantMember{}))
	require.NoError(t, db.Create(&types.TenantMember{
		UserID: "author-1", TenantID: 7, Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusActive,
	}).Error)
	repo := NewProductionDocumentTypeRepository(db)
	base := productionDocumentType("base-concurrent", 7, "sop", 1)
	base.Status = types.ProductionDocumentTypeActive
	require.NoError(t, db.Create(base).Error)

	start := make(chan struct{})
	results := make(chan *types.ProductionDocumentType, 2)
	errorsCh := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for index := range 2 {
		go func(index int) {
			ready.Done()
			<-start
			derived, deriveErr := repo.DeriveDraft(
				context.Background(), 7, base.ID, "author-1",
				validDerivedProductionDocumentType(fmt.Sprintf("derived-concurrent-%d", index), 7),
			)
			results <- derived
			errorsCh <- deriveErr
		}(index)
	}
	ready.Wait()
	close(start)

	versions := make([]int, 0, 2)
	for range 2 {
		require.NoError(t, <-errorsCh)
		versions = append(versions, (<-results).SchemaVersion)
	}
	require.ElementsMatch(t, []int{2, 3}, versions)
}

func TestDocumentTypeRepositoryDerivePostgresLocksCodeBeforeVersionAllocation(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "tenant_members" WHERE .*tenant_id = \$1 AND user_id = \$2 AND status = \$3 AND role IN \(\$4,\$5\).*FOR UPDATE`).
		WithArgs(uint64(7), "author-1", types.TenantMemberStatusActive, types.TenantRoleAdmin, types.TenantRoleOwner, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "role", "status"}).
			AddRow(1, "author-1", 7, string(types.TenantRoleAdmin), string(types.TenantMemberStatusActive)))
	mock.ExpectQuery(`SELECT \* FROM "production_document_types" WHERE .*tenant_id = \$1 AND id = \$2 AND status IN \(\$3,\$4\).*FOR UPDATE`).
		WithArgs(uint64(7), "base-postgres", types.ProductionDocumentTypeActive, types.ProductionDocumentTypeRetired, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "code", "schema_version", "status", "origin", "template_key"}).
			AddRow("base-postgres", 7, "sop", 4, string(types.ProductionDocumentTypeActive), string(types.ProductionDocumentTypeOriginBuiltin), "sop"))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\(\$1, 0\)\)`).
		WithArgs("7:sop").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(schema_version\), 0\) \+ 1 FROM "production_document_types" WHERE .*tenant_id = \$1 AND code = \$2`).
		WithArgs(uint64(7), "sop").WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(5))
	mock.ExpectQuery(`INSERT INTO "production_document_types".*RETURNING`).
		WillReturnRows(sqlmock.NewRows([]string{
			"block_schema", "source_requirements", "skill_bindings", "workflow_plan",
			"quality_rules", "review_policy", "publication_policy",
		}).AddRow(
			`{"allowed_block_types":["paragraph"],"required_sections":["Scope"],"version":1}`,
			`{"allow_unsupported_facts":false,"allowed_source_kinds":["upload"],"min_accepted_evidence":1,"require_evidence_section":true,"version":1}`,
			`{"skills":[],"version":1}`, `{"steps":[],"version":1}`,
			`{"block_needs_confirmation":true,"gates":["section_completeness"],"require_evidence_for_facts":true,"version":1}`,
			`{"steps":["business_reviewer"]}`,
			`{"chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true,"target_type":"knowledge_base","version":1}`,
		))
	mock.ExpectCommit()

	derived, err := NewProductionDocumentTypeRepository(db).DeriveDraft(
		context.Background(), 7, "base-postgres", "author-1", validDerivedProductionDocumentType("derived-postgres", 7),
	)

	require.NoError(t, err)
	require.Equal(t, 5, derived.SchemaVersion)
	require.Equal(t, types.ProductionDocumentTypeOriginCustom, derived.Origin)
	require.NotNil(t, derived.TemplateKey)
	require.Equal(t, "sop", *derived.TemplateKey)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentTypeRepositoryDeriveRetriesUniqueConflictInsideBoundedOperation(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	expectAttempt := func(nextVersion int, insertErr error) {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT \* FROM "tenant_members" WHERE .*FOR UPDATE`).
			WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "role", "status"}).
				AddRow(1, "author-1", 7, string(types.TenantRoleAdmin), string(types.TenantMemberStatusActive)))
		mock.ExpectQuery(`SELECT \* FROM "production_document_types" WHERE .*FOR UPDATE`).
			WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "code", "schema_version", "status"}).
				AddRow("base-retry", 7, "sop", 4, string(types.ProductionDocumentTypeActive)))
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT COALESCE\(MAX\(schema_version\), 0\) \+ 1`).
			WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(nextVersion))
		insert := mock.ExpectQuery(`INSERT INTO "production_document_types".*RETURNING`)
		if insertErr != nil {
			insert.WillReturnError(insertErr)
			mock.ExpectRollback()
			return
		}
		insert.WillReturnRows(sqlmock.NewRows([]string{
			"block_schema", "source_requirements", "skill_bindings", "workflow_plan",
			"quality_rules", "review_policy", "publication_policy",
		}).AddRow(`{}`, `{}`, `{}`, `{}`, `{}`, `{}`, `{}`))
		mock.ExpectCommit()
	}
	expectAttempt(5, &pgconn.PgError{Code: "23505", Message: "duplicate key"})
	expectAttempt(6, nil)

	derived, err := NewProductionDocumentTypeRepository(db).DeriveDraft(
		context.Background(), 7, "base-retry", "author-1", validDerivedProductionDocumentType("derived-retry", 7),
	)

	require.NoError(t, err)
	require.Equal(t, 6, derived.SchemaVersion)
	require.NoError(t, mock.ExpectationsWereMet())
}
