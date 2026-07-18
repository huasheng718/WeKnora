package repository

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	documentID       = "66666666-6666-4666-8666-666666666666"
	firstVersionID   = "77777777-7777-4777-8777-777777777777"
	secondVersionID  = "88888888-8888-4888-8888-888888888888"
	thirdVersionID   = "99999999-9999-4999-8999-999999999999"
	otherProjectID   = "aaaaaaaa-1111-4111-8111-111111111111"
	otherSourceSetID = "bbbbbbbb-1111-4111-8111-111111111111"
)

func newProductionDocumentRepoFixture(t *testing.T) (interfaces.ProductionDocumentRepository, interfaces.ProductionSourceRepository, *gorm.DB) {
	t.Helper()
	sourceRepo, db := newProductionSourceRepoTestDB(t)
	require.NoError(t, sourceRepo.CreateSet(context.Background(), &types.ProductionSourceSet{
		ID: sourceSetID, TenantID: 7, ProjectID: sourceProjectID, DocumentTypeID: sourceTypeID,
		Status: types.ProductionSourceSetFrozen, CreatedBy: "author", FrozenAt: timePointer(time.Now().UTC()),
	}))
	return NewProductionDocumentRepository(db), sourceRepo, db
}

func timePointer(value time.Time) *time.Time { return &value }
func stringPointer(value string) *string     { return &value }

func productionDocumentFixture() *types.ProductionDocument {
	return &types.ProductionDocument{
		ID: documentID, TenantID: 7, ProjectID: sourceProjectID, DocumentTypeID: sourceTypeID,
		DocumentTypeSchemaVersion: 1, Title: "Baseline", Status: types.ProductionDocumentDraft, CreatedBy: "author",
	}
}

func productionBlock(rowID, logicalID, content string) *types.ProductionDocumentBlock {
	block := &types.ProductionDocumentBlock{
		ID: rowID, LogicalBlockID: logicalID, BlockType: "paragraph",
		Content: types.JSON(content), Attributes: types.JSON(`{}`), EvidenceRefs: types.JSON(`[]`), AIProvenance: types.JSON(`{}`),
	}
	block.ContentDigest = types.ComputeProductionBlockDigest(block)
	return block
}

func productionVersion(id string, parent *string, blocks ...*types.ProductionDocumentBlock) *types.ProductionDocumentVersion {
	version := &types.ProductionDocumentVersion{
		ID: id, DocumentID: documentID, ParentVersionID: parent, SourceSetID: sourceSetID,
		Origin: types.ProductionDocumentOriginHuman, ChangeSummary: "change", CreatedBy: "author", Blocks: blocks,
	}
	for position, block := range blocks {
		block.Position = position
		block.ContentDigest = types.ComputeProductionBlockDigest(block)
	}
	version.ContentDigest = types.ComputeProductionVersionDigest(version)
	return version
}

func createDocumentAndFirstVersion(t *testing.T, repo interfaces.ProductionDocumentRepository) *types.ProductionDocumentVersion {
	t.Helper()
	require.NoError(t, repo.CreateDocument(context.Background(), productionDocumentFixture()))
	version := productionVersion(firstVersionID, nil, productionBlock("block-row-1", "block-a", `"first"`))
	require.NoError(t, repo.AppendVersion(context.Background(), version, version.Blocks, nil))
	return version
}

func TestProductionDocumentRepositoryHasNoVersionUpdateMethod(t *testing.T) {
	var _ interfaces.ProductionDocumentRepository = NewProductionDocumentRepository(nil)
}

func TestProductionDocumentRepositoryScopesReadsByTenant(t *testing.T) {
	repo, _, _ := newProductionDocumentRepoFixture(t)
	first := createDocumentAndFirstVersion(t, repo)

	got, err := repo.GetDocument(context.Background(), 7, documentID)
	require.NoError(t, err)
	require.Equal(t, documentID, got.ID)

	got, err = repo.GetDocument(context.Background(), 8, documentID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Nil(t, got)
	version, versionErr := repo.GetVersion(context.Background(), 8, first.ID)
	require.ErrorIs(t, versionErr, gorm.ErrRecordNotFound)
	require.Nil(t, version)
}

func TestProductionDocumentRepositoryDerivesScopeAndAllocatesVersionNumber(t *testing.T) {
	repo, _, _ := newProductionDocumentRepoFixture(t)
	first := createDocumentAndFirstVersion(t, repo)

	require.Equal(t, uint64(7), first.TenantID)
	require.Equal(t, sourceProjectID, first.ProjectID)
	require.Equal(t, 1, first.VersionNumber)
	got, err := repo.GetVersion(context.Background(), 7, first.ID)
	require.NoError(t, err)
	require.Len(t, got.Blocks, 1)
	require.Equal(t, "block-a", got.Blocks[0].LogicalBlockID)

	document, err := repo.GetDocument(context.Background(), 7, documentID)
	require.NoError(t, err)
	require.NotNil(t, document.CurrentVersionID)
	require.Equal(t, first.ID, *document.CurrentVersionID)
}

func TestProductionDocumentRepositoryRejectsStaleParentWithoutInserts(t *testing.T) {
	repo, _, db := newProductionDocumentRepoFixture(t)
	createDocumentAndFirstVersion(t, repo)
	stale := productionVersion(secondVersionID, stringPointer("00000000-0000-4000-8000-000000000000"),
		productionBlock("block-row-2", "block-a", `"stale"`))

	err := repo.AppendVersion(context.Background(), stale, stale.Blocks, nil)

	require.ErrorIs(t, err, types.ErrProductionDocumentStaleParent)
	var count int64
	require.NoError(t, db.Model(&types.ProductionDocumentVersion{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestProductionDocumentRepositoryConcurrentAppendsHaveOneWinner(t *testing.T) {
	repo, _, db := newProductionDocumentRepoFixture(t)
	first := createDocumentAndFirstVersion(t, repo)
	versions := []*types.ProductionDocumentVersion{
		productionVersion(secondVersionID, stringPointer(first.ID), productionBlock("block-row-2", "block-a", `"second"`)),
		productionVersion(thirdVersionID, stringPointer(first.ID), productionBlock("block-row-3", "block-a", `"third"`)),
	}
	start := make(chan struct{})
	errs := make(chan error, len(versions))
	var wg sync.WaitGroup
	for _, version := range versions {
		version := version
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- repo.AppendVersion(context.Background(), version, version.Blocks, nil)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var success, stale int
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, types.ErrProductionDocumentStaleParent) {
			stale++
		} else {
			t.Fatalf("unexpected append error: %v", err)
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, stale)

	var persisted []types.ProductionDocumentVersion
	require.NoError(t, db.Order("version_number ASC").Find(&persisted).Error)
	require.Len(t, persisted, 2)
	require.Equal(t, []int{1, 2}, []int{persisted[0].VersionNumber, persisted[1].VersionNumber})
	document, err := repo.GetDocument(context.Background(), 7, documentID)
	require.NoError(t, err)
	require.Equal(t, persisted[1].ID, *document.CurrentVersionID)
}

func TestProductionDocumentRepositoryRejectsWrongProjectSourceSet(t *testing.T) {
	repo, sourceRepo, db := newProductionDocumentRepoFixture(t)
	require.NoError(t, db.Create(&types.ProductionProject{
		ID: otherProjectID, TenantID: 7, Name: "Other", OwnerUserID: "owner", Status: types.ProductionProjectActive,
	}).Error)
	require.NoError(t, sourceRepo.CreateSet(context.Background(), &types.ProductionSourceSet{
		ID: otherSourceSetID, TenantID: 7, ProjectID: otherProjectID, DocumentTypeID: sourceTypeID,
		Status: types.ProductionSourceSetFrozen, CreatedBy: "author", FrozenAt: timePointer(time.Now().UTC()),
	}))
	require.NoError(t, repo.CreateDocument(context.Background(), productionDocumentFixture()))
	version := productionVersion(firstVersionID, nil, productionBlock("block-row-1", "block-a", `"wrong source"`))
	version.SourceSetID = otherSourceSetID

	err := repo.AppendVersion(context.Background(), version, version.Blocks, nil)

	require.ErrorIs(t, err, types.ErrProductionDocumentSourceSetInvalid)
	var count int64
	require.NoError(t, db.Model(&types.ProductionDocumentVersion{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProductionDocumentRepositoryPersistsSplitAndMergedLineage(t *testing.T) {
	repo, _, _ := newProductionDocumentRepoFixture(t)
	require.NoError(t, repo.CreateDocument(context.Background(), productionDocumentFixture()))
	first := productionVersion(firstVersionID, nil,
		productionBlock("block-row-1", "block-a", `"a"`),
		productionBlock("block-row-2", "block-b", `"b"`),
	)
	require.NoError(t, repo.AppendVersion(context.Background(), first, first.Blocks, nil))
	second := productionVersion(secondVersionID, stringPointer(first.ID),
		productionBlock("block-row-3", "block-a-1", `"a1"`),
		productionBlock("block-row-4", "block-a-2", `"a2"`),
		productionBlock("block-row-5", "block-ab", `"ab"`),
	)
	lineage := []*types.ProductionBlockLineage{
		{ID: "lineage-1", FromVersionID: first.ID, FromLogicalBlockID: "block-a", ToVersionID: second.ID, ToLogicalBlockID: "block-a-1", Relation: types.ProductionBlockRelationSplit},
		{ID: "lineage-2", FromVersionID: first.ID, FromLogicalBlockID: "block-a", ToVersionID: second.ID, ToLogicalBlockID: "block-a-2", Relation: types.ProductionBlockRelationSplit},
		{ID: "lineage-3", FromVersionID: first.ID, FromLogicalBlockID: "block-a", ToVersionID: second.ID, ToLogicalBlockID: "block-ab", Relation: types.ProductionBlockRelationMerged},
		{ID: "lineage-4", FromVersionID: first.ID, FromLogicalBlockID: "block-b", ToVersionID: second.ID, ToLogicalBlockID: "block-ab", Relation: types.ProductionBlockRelationMerged},
	}

	require.NoError(t, repo.AppendVersion(context.Background(), second, second.Blocks, lineage))
	got, err := repo.GetVersion(context.Background(), 7, second.ID)
	require.NoError(t, err)
	require.Len(t, got.Lineage, 4)
}

func TestProductionDocumentRepositoryRejectsMissingLineageEndpointAndRollsBack(t *testing.T) {
	repo, _, db := newProductionDocumentRepoFixture(t)
	first := createDocumentAndFirstVersion(t, repo)
	second := productionVersion(secondVersionID, stringPointer(first.ID), productionBlock("block-row-2", "block-b", `"b"`))
	lineage := []*types.ProductionBlockLineage{{
		ID: "lineage-invalid", FromVersionID: first.ID, FromLogicalBlockID: "missing",
		ToVersionID: second.ID, ToLogicalBlockID: "block-b", Relation: types.ProductionBlockRelationSame,
	}}

	err := repo.AppendVersion(context.Background(), second, second.Blocks, lineage)

	require.ErrorIs(t, err, types.ErrProductionBlockLineageInvalid)
	var count int64
	require.NoError(t, db.Model(&types.ProductionDocumentVersion{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestProductionDocumentRepositoryRollsBackOnBlockOrLineageInsertFailure(t *testing.T) {
	for _, tc := range []struct {
		name, table, body string
		lineage           bool
	}{
		{name: "block", table: "production_document_blocks", body: "forced block failure"},
		{name: "lineage", table: "production_block_lineage", body: "forced lineage failure", lineage: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, _, db := newProductionDocumentRepoFixture(t)
			first := createDocumentAndFirstVersion(t, repo)
			trigger := "fail_document_" + tc.name + "_insert"
			require.NoError(t, db.Exec("CREATE TRIGGER "+trigger+" BEFORE INSERT ON "+tc.table+" BEGIN SELECT RAISE(ABORT, '"+tc.body+"'); END").Error)
			second := productionVersion(secondVersionID, stringPointer(first.ID), productionBlock("block-row-2", "block-b", `"b"`))
			var lineage []*types.ProductionBlockLineage
			if tc.lineage {
				lineage = []*types.ProductionBlockLineage{{
					ID: "lineage-1", FromVersionID: first.ID, FromLogicalBlockID: "block-a",
					ToVersionID: second.ID, ToLogicalBlockID: "block-b", Relation: types.ProductionBlockRelationSame,
				}}
			}

			require.Error(t, repo.AppendVersion(context.Background(), second, second.Blocks, lineage))
			var count int64
			require.NoError(t, db.Model(&types.ProductionDocumentVersion{}).Count(&count).Error)
			require.Equal(t, int64(1), count)
			document, err := repo.GetDocument(context.Background(), 7, documentID)
			require.NoError(t, err)
			require.Equal(t, first.ID, *document.CurrentVersionID)
		})
	}
}

func TestProductionDocumentRepositoryJoinsSharedTransactionContext(t *testing.T) {
	repo, _, db := newProductionDocumentRepoFixture(t)
	errRollback := context.Canceled
	err := database.WithTransactionContext(context.Background(), db, func(txCtx context.Context) error {
		require.NoError(t, repo.CreateDocument(txCtx, productionDocumentFixture()))
		version := productionVersion(firstVersionID, nil, productionBlock("block-row-1", "block-a", `"first"`))
		require.NoError(t, repo.AppendVersion(txCtx, version, version.Blocks, nil))
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)
	var count int64
	require.NoError(t, db.Model(&types.ProductionDocument{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProductionDocumentVersionAndBlockRowsAreDatabaseImmutable(t *testing.T) {
	repo, _, db := newProductionDocumentRepoFixture(t)
	first := createDocumentAndFirstVersion(t, repo)

	versionErr := db.Model(&types.ProductionDocumentVersion{}).Where("id = ?", first.ID).Update("change_summary", "changed").Error
	require.ErrorIs(t, translateProductionDocumentError(versionErr), types.ErrProductionDocumentVersionImmutable)
	blockErr := db.Model(&types.ProductionDocumentBlock{}).Where("version_id = ?", first.ID).Update("content", types.JSON(`"changed"`)).Error
	require.ErrorIs(t, translateProductionDocumentError(blockErr), types.ErrProductionDocumentBlockImmutable)
}

func TestProductionDocumentRepositoryPostgresLocksHeadForUpdate(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	query := regexp.QuoteMeta(`SELECT * FROM "production_documents" WHERE id = $1 ORDER BY "production_documents"."id" LIMIT $2 FOR UPDATE`)
	mock.ExpectQuery(query).WithArgs(documentID, 1).WillReturnRows(sqlmock.NewRows([]string{
		"id", "tenant_id", "project_id", "document_type_id", "document_type_schema_version", "title", "status", "created_by", "created_at", "updated_at",
	}).AddRow(documentID, 7, sourceProjectID, sourceTypeID, 1, "Baseline", string(types.ProductionDocumentDraft), "author", time.Now(), time.Now()))

	locked, err := lockProductionDocumentHead(db, documentID)
	require.NoError(t, err)
	require.Equal(t, documentID, locked.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProductionDocumentRepositoryListsVersionsInNumberOrder(t *testing.T) {
	repo, _, _ := newProductionDocumentRepoFixture(t)
	first := createDocumentAndFirstVersion(t, repo)
	second := productionVersion(secondVersionID, stringPointer(first.ID), productionBlock("block-row-2", "block-a", `"second"`))
	require.NoError(t, repo.AppendVersion(context.Background(), second, second.Blocks, nil))

	versions, err := repo.ListVersions(context.Background(), 7, documentID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	numbers := []int{versions[0].VersionNumber, versions[1].VersionNumber}
	require.True(t, sort.IntsAreSorted(numbers))
}
