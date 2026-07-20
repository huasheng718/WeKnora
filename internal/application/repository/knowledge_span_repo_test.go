package repository

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const spanKnowledgeTestDDL = `
CREATE TABLE IF NOT EXISTS knowledges (
    id         VARCHAR(64) PRIMARY KEY,
    tenant_id  INTEGER NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);
`

// spansTestDDL mirrors migration 000053 for SQLite — same column order
// minus the JSONB type (SQLite stores JSON as TEXT, the JSONMap Scanner
// handles the round trip transparently). Inlined for the same reason
// knowledgebase_sqlite_test.go inlines its DDL: GORM AutoMigrate doesn't
// reproduce our PostgreSQL-flavoured schema cleanly.
const spansTestDDL = `
CREATE TABLE IF NOT EXISTS knowledge_processing_spans (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    knowledge_id    VARCHAR(64) NOT NULL,
    attempt         INTEGER     NOT NULL DEFAULT 1,
    span_id         VARCHAR(64) NOT NULL,
    parent_span_id  VARCHAR(64),
    name            VARCHAR(255) NOT NULL,
    kind            VARCHAR(16) NOT NULL,
    status          VARCHAR(16) NOT NULL,
    input           TEXT,
    output          TEXT,
    metadata        TEXT,
    error_code      VARCHAR(64),
    error_message   TEXT,
    error_detail    TEXT,
    started_at      DATETIME,
    finished_at     DATETIME,
    duration_ms     BIGINT,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (knowledge_id, attempt, span_id)
);
`

func setupSpanTestRepo(t *testing.T) (KnowledgeSpanRepository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(spansTestDDL).Error)
	return NewKnowledgeSpanRepository(db), db
}

// TestKnowledgeSpanRepo_UpsertAndList covers the round-trip: a Begin
// followed by an End for the same (kid, attempt, span_id) updates the
// existing row in place, leaving exactly one row queryable by
// ListByAttempt with the latest state.
func TestKnowledgeSpanRepo_UpsertAndList(t *testing.T) {
	repo, _ := setupSpanTestRepo(t)
	ctx := context.Background()
	kid := "kid-1"
	now := time.Now()
	row := &types.KnowledgeProcessingSpan{
		KnowledgeID: kid,
		Attempt:     1,
		SpanID:      "span-A",
		Name:        types.StageDocReader,
		Kind:        types.SpanKindStage,
		Status:      types.SpanStatusRunning,
		StartedAt:   &now,
	}
	require.NoError(t, repo.Upsert(ctx, row))

	// Second Upsert with same (kid, attempt, span_id) flips status and
	// sets finished_at — must overwrite, not insert a duplicate.
	finished := now.Add(2 * time.Second)
	row.Status = types.SpanStatusDone
	row.FinishedAt = &finished
	row.DurationMs = 2000
	require.NoError(t, repo.Upsert(ctx, row))

	rows, err := repo.ListByAttempt(ctx, kid, 1)
	require.NoError(t, err)
	require.Len(t, rows, 1, "Upsert must replace, not append")
	assert.Equal(t, types.SpanStatusDone, rows[0].Status)
	assert.Equal(t, int64(2000), rows[0].DurationMs)
}

// TestKnowledgeSpanRepo_NextAttempt confirms that NextAttempt allocates
// a fresh number per knowledge, isolating reparse history. Critical
// because the API layer renders attempt history by this number.
func TestKnowledgeSpanRepo_NextAttempt(t *testing.T) {
	repo, _ := setupSpanTestRepo(t)
	ctx := context.Background()
	kid := "kid-2"

	a, err := repo.NextAttempt(ctx, kid)
	require.NoError(t, err)
	assert.Equal(t, 1, a, "first NextAttempt for fresh knowledge must be 1")

	now := time.Now()
	require.NoError(t, repo.Upsert(ctx, &types.KnowledgeProcessingSpan{
		KnowledgeID: kid, Attempt: 1, SpanID: "root-1",
		Name: "knowledge_processing", Kind: types.SpanKindRoot,
		Status: types.SpanStatusRunning, StartedAt: &now,
	}))
	a, err = repo.NextAttempt(ctx, kid)
	require.NoError(t, err)
	assert.Equal(t, 2, a, "after one attempt exists, NextAttempt must be 2")

	// Cross-knowledge isolation: a different kid stays at 1.
	other, err := repo.NextAttempt(ctx, "kid-other")
	require.NoError(t, err)
	assert.Equal(t, 1, other, "NextAttempt must scope to the knowledge_id")
}

func TestKnowledgeSpanRepo_ClaimPendingRootIsTenantScopedAndConcurrent(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "span-claim.db") + "?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(spanKnowledgeTestDDL).Error)
	require.NoError(t, db.Exec(spansTestDDL).Error)
	require.NoError(t, db.Exec(`INSERT INTO knowledges (id, tenant_id) VALUES ('kid-claim', 7)`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(16)
	repo := NewKnowledgeSpanRepository(db)

	const claimers = 16
	start := make(chan struct{})
	type result struct {
		root    *types.KnowledgeProcessingSpan
		created bool
		err     error
	}
	results := make(chan result, claimers)
	var claims sync.WaitGroup
	for i := range claimers {
		claims.Add(1)
		go func(index int) {
			defer claims.Done()
			<-start
			now := time.Now()
			root, created, claimErr := repo.ClaimPendingRoot(context.Background(), 7, "kid-claim", &types.KnowledgeProcessingSpan{
				KnowledgeID: "kid-claim", SpanID: fmt.Sprintf("candidate-%d", index),
				Name: "knowledge_processing", Kind: types.SpanKindRoot,
				Status: types.SpanStatusRunning, StartedAt: &now,
			})
			results <- result{root: root, created: created, err: claimErr}
		}(i)
	}
	close(start)
	claims.Wait()
	close(results)

	createdCount := 0
	claimedSpanID := ""
	for claim := range results {
		require.NoError(t, claim.err)
		require.NotNil(t, claim.root)
		require.Equal(t, 1, claim.root.Attempt)
		if claimedSpanID == "" {
			claimedSpanID = claim.root.SpanID
		}
		require.Equal(t, claimedSpanID, claim.root.SpanID)
		if claim.created {
			createdCount++
		}
	}
	require.Equal(t, 1, createdCount)
	rows, err := repo.ListByAttempt(context.Background(), "kid-claim", 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	wrongTenantRoot, _, err := repo.ClaimPendingRoot(context.Background(), 8, "kid-claim", &types.KnowledgeProcessingSpan{
		KnowledgeID: "kid-claim", SpanID: "wrong-tenant", Name: "knowledge_processing",
		Kind: types.SpanKindRoot, Status: types.SpanStatusRunning,
	})
	require.ErrorIs(t, err, ErrKnowledgeNotFound)
	require.Nil(t, wrongTenantRoot)
}

func TestKnowledgeSpanRepo_ClaimPendingRootLocksPostgreSQLKnowledgeTenantScope(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	require.NoError(t, err)
	repo := NewKnowledgeSpanRepository(db)
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT "id" FROM "knowledges" WHERE .*tenant_id = \$1 AND id = \$2.*FOR UPDATE`).
		WithArgs(uint64(7), "kid-pg", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("kid-pg"))
	mock.ExpectQuery(`SELECT \* FROM "knowledge_processing_spans" WHERE knowledge_id = \$1 AND kind = \$2 ORDER BY attempt DESC, id DESC LIMIT \$3`).
		WithArgs("kid-pg", types.SpanKindRoot, 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "knowledge_id", "attempt", "span_id", "name", "kind", "status", "started_at",
		}).AddRow(1, "kid-pg", 3, "root-existing", "knowledge_processing", types.SpanKindRoot, types.SpanStatusRunning, now))
	mock.ExpectCommit()

	root, created, err := repo.ClaimPendingRoot(context.Background(), 7, "kid-pg", &types.KnowledgeProcessingSpan{
		KnowledgeID: "kid-pg", SpanID: "candidate", Name: "knowledge_processing",
		Kind: types.SpanKindRoot, Status: types.SpanStatusRunning, StartedAt: &now,
	})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, 3, root.Attempt)
	require.Equal(t, "root-existing", root.SpanID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestKnowledgeSpanRepo_CancelDescendants verifies the cascade walk:
// failing a stage cancels every pending/running descendant in its
// subtree, while terminal states (done/skipped/failed) are left intact.
func TestKnowledgeSpanRepo_CancelDescendants(t *testing.T) {
	repo, _ := setupSpanTestRepo(t)
	ctx := context.Background()
	kid := "kid-3"
	now := time.Now()

	// Tree: chunking → embedding (running) → batch[0] (running)
	//                → multimodal (running) → image[0] (done)
	for _, r := range []*types.KnowledgeProcessingSpan{
		{KnowledgeID: kid, Attempt: 1, SpanID: "chunking", Name: types.StageChunking, Kind: types.SpanKindStage, Status: types.SpanStatusRunning, StartedAt: &now},
		{KnowledgeID: kid, Attempt: 1, SpanID: "embedding", ParentSpanID: "chunking", Name: types.StageEmbedding, Kind: types.SpanKindStage, Status: types.SpanStatusRunning, StartedAt: &now},
		{KnowledgeID: kid, Attempt: 1, SpanID: "batch0", ParentSpanID: "embedding", Name: "embedding.batch[0]", Kind: types.SpanKindGeneration, Status: types.SpanStatusRunning, StartedAt: &now},
		{KnowledgeID: kid, Attempt: 1, SpanID: "multimodal", ParentSpanID: "chunking", Name: types.StageMultimodal, Kind: types.SpanKindStage, Status: types.SpanStatusRunning, StartedAt: &now},
		{KnowledgeID: kid, Attempt: 1, SpanID: "image0", ParentSpanID: "multimodal", Name: "multimodal.image[0]", Kind: types.SpanKindGeneration, Status: types.SpanStatusDone, StartedAt: &now},
	} {
		require.NoError(t, repo.Upsert(ctx, r))
	}

	affected, err := repo.CancelDescendants(ctx, kid, 1, "chunking", "test reason")
	require.NoError(t, err)
	// Expected cancellations: embedding, batch0, multimodal (3 rows).
	// The done image0 is terminal and left alone.
	assert.Equal(t, int64(3), affected, "must cancel exactly the 3 pending/running descendants")

	rows, err := repo.ListByAttempt(ctx, kid, 1)
	require.NoError(t, err)
	statusBy := map[string]string{}
	for _, r := range rows {
		statusBy[r.SpanID] = r.Status
	}
	assert.Equal(t, types.SpanStatusRunning, statusBy["chunking"], "the failed span itself stays untouched (FailSpan layer flips it)")
	assert.Equal(t, types.SpanStatusCancelled, statusBy["embedding"])
	assert.Equal(t, types.SpanStatusCancelled, statusBy["batch0"])
	assert.Equal(t, types.SpanStatusCancelled, statusBy["multimodal"])
	assert.Equal(t, types.SpanStatusDone, statusBy["image0"], "terminal states must not be touched")
}

func TestKnowledgeSpanRepo_CancelOpenSpansByName(t *testing.T) {
	repo, _ := setupSpanTestRepo(t)
	ctx := context.Background()
	kid := "kid-supersede"
	now := time.Now()

	for _, r := range []*types.KnowledgeProcessingSpan{
		{KnowledgeID: kid, Attempt: 1, SpanID: "sum-old", Name: "postprocess.summary", Kind: types.SpanKindSubSpan, Status: types.SpanStatusRunning, StartedAt: &now},
		{KnowledgeID: kid, Attempt: 1, SpanID: "sum-done", Name: "postprocess.summary", Kind: types.SpanKindSubSpan, Status: types.SpanStatusDone, StartedAt: &now},
		{KnowledgeID: kid, Attempt: 1, SpanID: "q-old", Name: "postprocess.question", Kind: types.SpanKindSubSpan, Status: types.SpanStatusRunning, StartedAt: &now},
	} {
		require.NoError(t, repo.Upsert(ctx, r))
	}

	affected, err := repo.CancelOpenSpansByName(ctx, kid, 1, "postprocess.summary", "TASK_SUPERSEDED", "retry")
	require.NoError(t, err)
	assert.Equal(t, int64(1), affected)

	rows, err := repo.ListByAttempt(ctx, kid, 1)
	require.NoError(t, err)
	statusBy := map[string]string{}
	for _, r := range rows {
		statusBy[r.SpanID] = r.Status
	}
	assert.Equal(t, types.SpanStatusCancelled, statusBy["sum-old"])
	assert.Equal(t, types.SpanStatusDone, statusBy["sum-done"])
	assert.Equal(t, types.SpanStatusRunning, statusBy["q-old"])
}

// TestKnowledgeSpanRepo_ListAttemptIsolation guarantees that different
// attempts of the same knowledge stay queryable independently — the
// foundation for the "show history" UI navigation (?attempt=N).
func TestKnowledgeSpanRepo_ListAttemptIsolation(t *testing.T) {
	repo, _ := setupSpanTestRepo(t)
	ctx := context.Background()
	kid := "kid-history"
	now := time.Now()

	for _, attempt := range []int{1, 2} {
		require.NoError(t, repo.Upsert(ctx, &types.KnowledgeProcessingSpan{
			KnowledgeID: kid, Attempt: attempt, SpanID: "root",
			Name: "knowledge_processing", Kind: types.SpanKindRoot,
			Status: types.SpanStatusDone, StartedAt: &now,
		}))
	}
	a1, err := repo.ListByAttempt(ctx, kid, 1)
	require.NoError(t, err)
	require.Len(t, a1, 1)
	a2, err := repo.ListByAttempt(ctx, kid, 2)
	require.NoError(t, err)
	require.Len(t, a2, 1)

	all, err := repo.ListByAttempt(ctx, kid, 0)
	require.NoError(t, err)
	assert.Len(t, all, 2, "attempt=0 returns all attempts (used by housekeeping)")
}
