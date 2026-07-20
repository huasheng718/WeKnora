package repository

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
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
		"000003_knowledge_production_runs.up.sql",
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

func TestProductionSourceRepositoryGetsEvidenceWithAuthoritativeTenantContext(t *testing.T) {
	repo, _ := newProductionSourceRepoTestDB(t)
	createProductionSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createProductionSourceItem(t, repo, types.ProductionSourceItemAccepted)
	require.NoError(t, repo.CreateEvidence(context.Background(), 7, sourceItemID, &types.ProductionEvidenceSnapshot{
		ID: evidenceID, SnapshotType: types.ProductionEvidenceSnapshotText,
		InlineContent: types.JSON(`"snapshot"`), ContentDigest: testDigest, RedactionMetadata: types.JSON(`{}`),
	}))

	evidence, item, set, err := repo.GetEvidence(context.Background(), 7, evidenceID)
	require.NoError(t, err)
	require.Equal(t, evidenceID, evidence.ID)
	require.Equal(t, sourceItemID, item.ID)
	require.Equal(t, sourceSetID, set.ID)

	evidence, item, set, err = repo.GetEvidence(context.Background(), 8, evidenceID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Nil(t, evidence)
	require.Nil(t, item)
	require.Nil(t, set)
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

func TestProductionSourceRepositoryAllowsOnlyRunCapturedEvidenceOnFrozenAcceptedItem(t *testing.T) {
	repo, db := newProductionSourceRepoTestDB(t)
	createProductionSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createProductionSourceItem(t, repo, types.ProductionSourceItemAccepted)
	require.NoError(t, repo.CreateEvidence(context.Background(), 7, sourceItemID, &types.ProductionEvidenceSnapshot{
		ID: evidenceID, SnapshotType: types.ProductionEvidenceSnapshotText,
		InlineContent: types.JSON(`"seed"`), ContentDigest: testDigest, RedactionMetadata: types.JSON(`{}`),
	}))
	require.NoError(t, db.Create(&types.ProductionDocument{
		ID: repoDocumentID, TenantID: 7, ProjectID: sourceProjectID, DocumentTypeID: sourceTypeID,
		DocumentTypeSchemaVersion: 1, Title: "Document", CreatedBy: "author",
	}).Error)
	run := newTestProductionRun(7)
	run.ProjectID, run.SourceSetID, run.DocumentID = sourceProjectID, sourceSetID, repoDocumentID
	require.NoError(t, db.Create(run).Error)
	require.NoError(t, repo.Freeze(context.Background(), 7, sourceSetID))

	late := &types.ProductionEvidenceSnapshot{
		ID: "77777777-7777-4777-8777-777777777777", SnapshotType: types.ProductionEvidenceSnapshotToolResult,
		InlineContent: types.JSON(`{"ok":true}`), ContentDigest: strings.Repeat("b", 64),
		RedactionMetadata: types.JSON(`{}`), CapturedByRunID: run.ID,
	}
	require.NoError(t, repo.CreateEvidence(context.Background(), 7, sourceItemID, late))

	ordinary := *late
	ordinary.ID = "88888888-8888-4888-8888-888888888888"
	ordinary.CapturedByRunID = ""
	require.ErrorIs(t, repo.CreateEvidence(context.Background(), 7, sourceItemID, &ordinary), types.ErrProductionSourceSetFrozen)
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

type productionSourceLockBarrierContextKey struct{}

type productionSourceLockBarrierState struct {
	entered chan struct{}
	release chan struct{}
	claimed atomic.Bool
}

func productionSourceLockBarrier(t *testing.T, db *gorm.DB) (context.Context, <-chan struct{}, func()) {
	t.Helper()
	state := &productionSourceLockBarrierState{entered: make(chan struct{}), release: make(chan struct{})}
	callbackName := "production-source-lock-barrier-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		barrier, _ := tx.Statement.Context.Value(productionSourceLockBarrierContextKey{}).(*productionSourceLockBarrierState)
		if barrier == nil || tx.Statement.Table != "production_source_sets" || !barrier.claimed.CompareAndSwap(false, true) {
			return
		}
		close(barrier.entered)
		<-barrier.release
	}))
	t.Cleanup(func() { db.Callback().Update().Remove(callbackName) })
	ctx := context.WithValue(context.Background(), productionSourceLockBarrierContextKey{}, state)
	return ctx, state.entered, func() { close(state.release) }
}

func TestProductionSourceRepositorySerializesFreezeAgainstAdd(t *testing.T) {
	t.Run("freeze wins", func(t *testing.T) {
		repo, db := newProductionSourceRepoTestDB(t)
		createProductionSourceSet(t, repo, types.ProductionSourceSetCollecting)
		blockedCtx, entered, release := productionSourceLockBarrier(t, db)
		addDone := make(chan error, 1)
		go func() {
			addDone <- repo.CreateItem(blockedCtx, 7, sourceSetID, &types.ProductionSourceItem{
				ID: sourceItemID, SourceKind: types.ProductionSourceKindManual, Title: "Source",
				MimeType: "text/plain", ContentDigest: testDigest, CapturedAt: time.Now().UTC(),
				Metadata: types.JSON(`{}`), Status: types.ProductionSourceItemCandidate,
			})
		}()
		<-entered
		require.NoError(t, repo.Freeze(context.Background(), 7, sourceSetID))
		release()
		require.ErrorIs(t, <-addDone, types.ErrProductionSourceSetFrozen)

		var count int64
		require.NoError(t, db.Model(&types.ProductionSourceItem{}).Count(&count).Error)
		require.Zero(t, count)
	})

	t.Run("add wins", func(t *testing.T) {
		repo, db := newProductionSourceRepoTestDB(t)
		createProductionSourceSet(t, repo, types.ProductionSourceSetCollecting)
		blockedCtx, entered, release := productionSourceLockBarrier(t, db)
		freezeDone := make(chan error, 1)
		go func() { freezeDone <- repo.Freeze(blockedCtx, 7, sourceSetID) }()
		<-entered
		require.NoError(t, repo.CreateItem(context.Background(), 7, sourceSetID, &types.ProductionSourceItem{
			ID: sourceItemID, SourceKind: types.ProductionSourceKindManual, Title: "Source",
			MimeType: "text/plain", ContentDigest: testDigest, CapturedAt: time.Now().UTC(),
			Metadata: types.JSON(`{}`), Status: types.ProductionSourceItemCandidate,
		}))
		release()
		require.NoError(t, <-freezeDone)

		var count int64
		require.NoError(t, db.Model(&types.ProductionSourceItem{}).Count(&count).Error)
		require.Equal(t, int64(1), count)
	})
}

func TestProductionSourceRepositorySerializesFreezeAgainstEvidence(t *testing.T) {
	t.Run("freeze wins before evidence lock", func(t *testing.T) {
		repo, db := newProductionSourceRepoTestDB(t)
		createProductionSourceSet(t, repo, types.ProductionSourceSetCollecting)
		createProductionSourceItem(t, repo, types.ProductionSourceItemCandidate)
		blockedCtx, entered, release := productionSourceLockBarrier(t, db)
		evidenceDone := make(chan error, 1)
		go func() {
			evidenceDone <- repo.CreateEvidence(blockedCtx, 7, sourceItemID, &types.ProductionEvidenceSnapshot{
				ID: evidenceID, SnapshotType: types.ProductionEvidenceSnapshotText,
				InlineContent: types.JSON(`"snapshot"`), ContentDigest: testDigest, RedactionMetadata: types.JSON(`{}`),
			})
		}()
		<-entered
		require.NoError(t, repo.Freeze(context.Background(), 7, sourceSetID))
		release()
		require.ErrorIs(t, <-evidenceDone, types.ErrProductionSourceSetFrozen)

		var count int64
		require.NoError(t, db.Model(&types.ProductionEvidenceSnapshot{}).Count(&count).Error)
		require.Zero(t, count)
	})

	t.Run("evidence wins", func(t *testing.T) {
		repo, db := newProductionSourceRepoTestDB(t)
		createProductionSourceSet(t, repo, types.ProductionSourceSetCollecting)
		createProductionSourceItem(t, repo, types.ProductionSourceItemAccepted)
		blockedCtx, entered, release := productionSourceLockBarrier(t, db)
		freezeDone := make(chan error, 1)
		go func() { freezeDone <- repo.Freeze(blockedCtx, 7, sourceSetID) }()
		<-entered
		require.NoError(t, repo.CreateEvidence(context.Background(), 7, sourceItemID, &types.ProductionEvidenceSnapshot{
			ID: evidenceID, SnapshotType: types.ProductionEvidenceSnapshotText,
			InlineContent: types.JSON(`"snapshot"`), ContentDigest: testDigest, RedactionMetadata: types.JSON(`{}`),
		}))
		release()
		require.NoError(t, <-freezeDone)

		var count int64
		require.NoError(t, db.Model(&types.ProductionEvidenceSnapshot{}).Count(&count).Error)
		require.Equal(t, int64(1), count)
	})
}

func TestProductionSourceRepositoryPostgresLocksSourceSetForUpdate(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	query := regexp.QuoteMeta(`SELECT * FROM "production_source_sets" WHERE tenant_id = $1 AND id = $2 ORDER BY "production_source_sets"."id" LIMIT $3 FOR UPDATE`)
	mock.ExpectQuery(query).
		WithArgs(uint64(7), sourceSetID, 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "project_id", "document_type_id", "status", "created_by", "created_at",
		}).AddRow(sourceSetID, 7, sourceProjectID, sourceTypeID, string(types.ProductionSourceSetCollecting), "author", time.Now()))

	locked, err := lockProductionSourceSet(db, 7, sourceSetID)
	require.NoError(t, err)
	require.Equal(t, sourceSetID, locked.ID)
	require.NoError(t, mock.ExpectationsWereMet())
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
