package repository

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	sourceProjectID = "11111111-1111-4111-8111-111111111111"
	sourceTypeID    = "22222222-2222-4222-8222-222222222222"
	sourceSetID     = "33333333-3333-4333-8333-333333333333"
	sourceItemID    = "44444444-4444-4444-8444-444444444444"
	evidenceID      = "55555555-5555-4555-8555-555555555555"
	testDigest      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func newProductionSourceRepoTestDB(t *testing.T) (interfaces.ProductionSourceRepository, *gorm.DB) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "source.db") + "?_foreign_keys=1&_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })

	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	for _, name := range []string{
		"000001_knowledge_production_foundation.up.sql",
		"000002_knowledge_production_documents.up.sql",
	} {
		migration, readErr := os.ReadFile(filepath.Join(filepath.Dir(filename), "../../../migrations/sqlite", name))
		require.NoError(t, readErr)
		require.NoError(t, db.Exec(string(migration)).Error)
	}

	seedProductionSourceParents(t, db, 7, sourceProjectID, sourceTypeID)
	seedProductionSourceParents(t, db, 8,
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
	)
	return NewProductionSourceRepository(db), db
}

func seedProductionSourceParents(t *testing.T, db *gorm.DB, tenantID uint64, projectID, documentTypeID string) {
	t.Helper()
	require.NoError(t, db.Create(&types.ProductionProject{
		ID: projectID, TenantID: tenantID, Name: "Project", OwnerUserID: "owner", Status: types.ProductionProjectActive,
	}).Error)
	require.NoError(t, db.Create(&types.ProductionDocumentType{
		ID: documentTypeID, TenantID: tenantID, Code: "type", Name: "Type", SchemaVersion: 1,
		BlockSchema: types.JSON(`{}`), SourceRequirements: types.JSON(`{}`), SkillBindings: types.JSON(`{}`),
		QualityRules: types.JSON(`{}`), ReviewPolicy: types.JSON(`{}`), PublicationPolicy: types.JSON(`{}`),
		Status: types.ProductionDocumentTypeActive, CreatedBy: "owner",
	}).Error)
}

func createProductionSourceSet(t *testing.T, repo interfaces.ProductionSourceRepository, status types.ProductionSourceSetStatus) {
	t.Helper()
	require.NoError(t, repo.CreateSet(context.Background(), &types.ProductionSourceSet{
		ID: sourceSetID, TenantID: 7, ProjectID: sourceProjectID, DocumentTypeID: sourceTypeID,
		Status: status, CreatedBy: "author",
	}))
}

func createProductionSourceItem(t *testing.T, repo interfaces.ProductionSourceRepository, status types.ProductionSourceItemStatus) {
	t.Helper()
	require.NoError(t, repo.CreateItem(context.Background(), 7, sourceSetID, &types.ProductionSourceItem{
		ID: sourceItemID, SourceSetID: sourceSetID, SourceKind: types.ProductionSourceKindManual,
		Title: "Source", MimeType: "text/plain", ContentDigest: testDigest,
		CapturedAt: time.Now().UTC(), Metadata: types.JSON(`{}`), Status: status,
	}))
}

func TestProductionSourceRepositoryScopesEveryLookupByTenant(t *testing.T) {
	repo, _ := newProductionSourceRepoTestDB(t)
	createProductionSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createProductionSourceItem(t, repo, types.ProductionSourceItemCandidate)

	set, err := repo.GetSet(context.Background(), 8, sourceSetID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Nil(t, set)

	item, itemSet, err := repo.GetItem(context.Background(), 8, sourceItemID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Nil(t, item)
	require.Nil(t, itemSet)
	require.ErrorIs(t, repo.DecideItem(context.Background(), 8, sourceItemID, types.ProductionSourceItemAccepted), gorm.ErrRecordNotFound)
}

func TestProductionSourceRepositoryRejectsNewItemsAndEvidenceForFrozenSet(t *testing.T) {
	repo, db := newProductionSourceRepoTestDB(t)
	createProductionSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createProductionSourceItem(t, repo, types.ProductionSourceItemAccepted)
	require.NoError(t, repo.CreateEvidence(context.Background(), 7, sourceItemID, &types.ProductionEvidenceSnapshot{
		ID: evidenceID, SourceItemID: sourceItemID, SnapshotType: types.ProductionEvidenceSnapshotText,
		InlineContent: types.JSON(`"snapshot"`), ContentDigest: testDigest, RedactionMetadata: types.JSON(`{}`),
	}))
	require.NoError(t, repo.Freeze(context.Background(), 7, sourceSetID))

	err := repo.CreateItem(context.Background(), 7, sourceSetID, &types.ProductionSourceItem{
		ID: "66666666-6666-4666-8666-666666666666", SourceSetID: sourceSetID,
		SourceKind: types.ProductionSourceKindManual, Title: "Late", MimeType: "text/plain",
		ContentDigest: testDigest, CapturedAt: time.Now().UTC(), Metadata: types.JSON(`{}`), Status: types.ProductionSourceItemCandidate,
	})
	require.ErrorIs(t, err, types.ErrProductionSourceSetFrozen)
	require.ErrorIs(t, repo.DecideItem(context.Background(), 7, sourceItemID, types.ProductionSourceItemRejected), types.ErrProductionSourceSetFrozen)
	require.ErrorIs(t, repo.CreateEvidence(context.Background(), 7, sourceItemID, &types.ProductionEvidenceSnapshot{
		ID: "77777777-7777-4777-8777-777777777777", SourceItemID: sourceItemID,
		SnapshotType: types.ProductionEvidenceSnapshotText, InlineContent: types.JSON(`"late"`),
		ContentDigest: testDigest, RedactionMetadata: types.JSON(`{}`),
	}), types.ErrProductionSourceSetFrozen)

	var count int64
	require.NoError(t, db.Model(&types.ProductionSourceItem{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestProductionSourceRepositoryFreezeIsAtomicAndRequiresEvidence(t *testing.T) {
	repo, _ := newProductionSourceRepoTestDB(t)
	createProductionSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createProductionSourceItem(t, repo, types.ProductionSourceItemAccepted)

	require.ErrorIs(t, repo.Freeze(context.Background(), 7, sourceSetID), types.ErrProductionEvidenceMissing)
	set, err := repo.GetSet(context.Background(), 7, sourceSetID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionSourceSetCollecting, set.Status)
	require.Nil(t, set.FrozenAt)

	require.NoError(t, repo.CreateEvidence(context.Background(), 7, sourceItemID, &types.ProductionEvidenceSnapshot{
		ID: evidenceID, SourceItemID: sourceItemID, SnapshotType: types.ProductionEvidenceSnapshotText,
		InlineContent: types.JSON(`"snapshot"`), ContentDigest: testDigest, RedactionMetadata: types.JSON(`{}`),
	}))
	require.NoError(t, repo.Freeze(context.Background(), 7, sourceSetID))
	set, err = repo.GetSet(context.Background(), 7, sourceSetID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionSourceSetFrozen, set.Status)
	require.NotNil(t, set.FrozenAt)
}

func TestProductionSourceRepositoryJoinsFoundationTransactionContext(t *testing.T) {
	repo, db := newProductionSourceRepoTestDB(t)
	errRollback := context.Canceled
	err := database.WithTransactionContext(context.Background(), db, func(txCtx context.Context) error {
		require.NoError(t, repo.CreateSet(txCtx, &types.ProductionSourceSet{
			ID: sourceSetID, TenantID: 7, ProjectID: sourceProjectID, DocumentTypeID: sourceTypeID,
			Status: types.ProductionSourceSetCollecting, CreatedBy: "author",
		}))
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)

	set, err := repo.GetSet(context.Background(), 7, sourceSetID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Nil(t, set)
}

func TestProductionSourceRepositorySerializesFreezeAgainstAcceptDecision(t *testing.T) {
	repo, _ := newProductionSourceRepoTestDB(t)
	createProductionSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createProductionSourceItem(t, repo, types.ProductionSourceItemCandidate)

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var freezeErr, decideErr error
	go func() {
		defer wg.Done()
		<-start
		freezeErr = repo.Freeze(context.Background(), 7, sourceSetID)
	}()
	go func() {
		defer wg.Done()
		<-start
		decideErr = repo.DecideItem(context.Background(), 7, sourceItemID, types.ProductionSourceItemAccepted)
	}()
	close(start)
	wg.Wait()

	set, err := repo.GetSet(context.Background(), 7, sourceSetID)
	require.NoError(t, err)
	item, _, err := repo.GetItem(context.Background(), 7, sourceItemID)
	require.NoError(t, err)
	if freezeErr == nil {
		require.Equal(t, types.ProductionSourceSetFrozen, set.Status)
		require.Equal(t, types.ProductionSourceItemCandidate, item.Status)
		require.ErrorIs(t, decideErr, types.ErrProductionSourceSetFrozen)
	} else {
		require.ErrorIs(t, freezeErr, types.ErrProductionEvidenceMissing)
		require.NoError(t, decideErr)
		require.Equal(t, types.ProductionSourceItemAccepted, item.Status)
		require.NotEqual(t, types.ProductionSourceSetFrozen, set.Status)
	}
}

func TestProductionEvidenceSnapshotDatabaseRowsAreImmutable(t *testing.T) {
	repo, db := newProductionSourceRepoTestDB(t)
	createProductionSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createProductionSourceItem(t, repo, types.ProductionSourceItemAccepted)
	require.NoError(t, repo.CreateEvidence(context.Background(), 7, sourceItemID, &types.ProductionEvidenceSnapshot{
		ID: evidenceID, SourceItemID: sourceItemID, SnapshotType: types.ProductionEvidenceSnapshotText,
		InlineContent: types.JSON(`"snapshot"`), ContentDigest: testDigest, RedactionMetadata: types.JSON(`{}`),
	}))

	err := db.Model(&types.ProductionEvidenceSnapshot{}).Where("id = ?", evidenceID).Update("content_digest", "changed").Error
	require.ErrorIs(t, translateProductionSourceError(err), types.ErrProductionEvidenceImmutable)
}
