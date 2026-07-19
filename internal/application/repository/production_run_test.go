package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newProductionRunRepoTestDB(t *testing.T) (interfaces.ProductionRunRepository, *gorm.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "production-run.db")
	dsn := "file:" + path + "?_foreign_keys=1&_busy_timeout=10000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })

	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Join(filepath.Dir(filename), "../../../migrations/sqlite")
	for _, name := range []string{
		"000001_knowledge_production_foundation.up.sql",
		"000002_knowledge_production_documents.up.sql",
		"000003_knowledge_production_runs.up.sql",
	} {
		migration, readErr := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, readErr)
		require.NoError(t, db.Exec(string(migration)).Error)
	}
	seedProductionRunContext(t, db, 7, "project-1", "type-1", "source-1", "document-1")
	return NewProductionRunRepository(db), db
}

func seedProductionRunContext(
	t *testing.T,
	db *gorm.DB,
	tenantID uint64,
	projectID, documentTypeID, sourceSetID, documentID string,
) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO production_projects (id, tenant_id, name, owner_user_id, status)
VALUES (?, ?, ?, 'owner-1', 'active')`, projectID, tenantID, projectID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO production_document_types
    (id, tenant_id, code, name, schema_version, status, created_by)
VALUES (?, ?, ?, ?, 1, 'active', 'owner-1')`, documentTypeID, tenantID, documentTypeID, documentTypeID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO production_source_sets
    (id, tenant_id, project_id, document_type_id, status, created_by)
VALUES (?, ?, ?, ?, 'frozen', 'owner-1')`, sourceSetID, tenantID, projectID, documentTypeID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO production_documents
    (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, status, created_by)
VALUES (?, ?, ?, ?, 1, ?, 'draft', 'owner-1')`, documentID, tenantID, projectID, documentTypeID, documentID).Error)
}

func newTestProductionRun(tenantID uint64) *types.ProductionRun {
	return &types.ProductionRun{
		ID:                   uuid.NewString(),
		TenantID:             tenantID,
		ProjectID:            "project-1",
		DocumentID:           "document-1",
		SourceSetID:          "source-1",
		RunType:              types.ProductionRunWrite,
		Status:               types.ProductionRunQueued,
		Attempt:              1,
		CurrentStep:          0,
		StatePayload:         types.JSON(`{"z":2,"a":1}`),
		ModelID:              "model-1",
		DocumentTypeSnapshot: types.JSON(`{"version":1,"name":"report"}`),
		IdempotencyKey:       uuid.NewString(),
	}
}

func runCAS(run *types.ProductionRun) interfaces.ProductionRunCAS {
	return interfaces.ProductionRunCAS{
		Status: run.Status, Attempt: run.Attempt, CurrentStep: run.CurrentStep,
	}
}

func TestProductionRunRepositoryCreateCanonicalizesSnapshotsAndScopesReads(t *testing.T) {
	repo, _ := newProductionRunRepoTestDB(t)
	run := newTestProductionRun(7)
	run.RawModelResponse = types.JSON(`{"answer":1.0,"meta":{"b":2,"a":1}}`)

	require.NoError(t, repo.Create(context.Background(), run))
	require.Equal(t, `{"a":1,"z":2}`, string(run.StatePayload))
	require.Equal(t, `{"answer":1,"meta":{"a":1,"b":2}}`, string(run.RawModelResponse))
	require.NotNil(t, run.RawModelResponseDigest)
	require.Len(t, *run.RawModelResponseDigest, 64)

	got, err := repo.Get(context.Background(), 7, run.ID)
	require.NoError(t, err)
	require.Equal(t, run.ID, got.ID)
	got, err = repo.Get(context.Background(), 8, run.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Nil(t, got)
}

func TestProductionRunRepositoryRejectsCredentialSnapshots(t *testing.T) {
	repo, _ := newProductionRunRepoTestDB(t)
	run := newTestProductionRun(7)
	run.StatePayload = types.JSON(`{"api_key":"must-not-persist"}`)

	err := repo.Create(context.Background(), run)

	require.ErrorContains(t, err, "credential")
}

func TestProductionRunRepositoryTransitionUsesFullCASAndRejectsInvalidTransition(t *testing.T) {
	repo, _ := newProductionRunRepoTestDB(t)
	run := newTestProductionRun(7)
	require.NoError(t, repo.Create(context.Background(), run))

	changed, err := repo.Transition(context.Background(), 7, run.ID,
		interfaces.ProductionRunCAS{Status: types.ProductionRunQueued, Attempt: 2, CurrentStep: 0},
		types.ProductionRunRunning, interfaces.ProductionRunPatch{})
	require.NoError(t, err)
	require.False(t, changed)

	changed, err = repo.Transition(context.Background(), 7, run.ID, runCAS(run),
		types.ProductionRunCompleted, interfaces.ProductionRunPatch{})
	require.ErrorContains(t, err, "invalid production run transition")
	require.False(t, changed)
}

func TestProductionRunRepositoryTwoWorkerClaimHasSingleWinner(t *testing.T) {
	repo, _ := newProductionRunRepoTestDB(t)
	run := newTestProductionRun(7)
	require.NoError(t, repo.Create(context.Background(), run))

	start := make(chan struct{})
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			ready.Done()
			<-start
			_, claimed, err := repo.Claim(context.Background(), 7, run.ID, runCAS(run), time.Time{})
			results <- claimed
			errs <- err
		}()
	}
	ready.Wait()
	close(start)

	winners := 0
	for i := 0; i < 2; i++ {
		require.NoError(t, <-errs)
		if <-results {
			winners++
		}
	}
	require.Equal(t, 1, winners)
}

func TestProductionRunRepositoryExpiredClaimIncrementsAttemptAndFencesStaleWorker(t *testing.T) {
	repo, db := newProductionRunRepoTestDB(t)
	run := newTestProductionRun(7)
	run.Status = types.ProductionRunRunning
	run.UpdatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, repo.Create(context.Background(), run))
	require.NoError(t, db.Model(&types.ProductionRun{}).
		Where("tenant_id = ? AND id = ?", 7, run.ID).
		UpdateColumn("updated_at", run.UpdatedAt).Error)

	claimed, ok, err := repo.Claim(context.Background(), 7, run.ID, runCAS(run), run.UpdatedAt.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 2, claimed.Attempt)

	nextStep := 1
	changed, err := repo.Transition(context.Background(), 7, run.ID, runCAS(run),
		types.ProductionRunQueued, interfaces.ProductionRunPatch{CurrentStep: &nextStep})
	require.NoError(t, err)
	require.False(t, changed, "attempt 1 worker must not advance attempt 2")
}

func TestProductionRunRepositoryToolCallsAreTenantScopedAndDecisionIsCAS(t *testing.T) {
	repo, _ := newProductionRunRepoTestDB(t)
	run := newTestProductionRun(7)
	run.Status = types.ProductionRunWaitingApproval
	require.NoError(t, repo.Create(context.Background(), run))
	now := time.Now().UTC()
	call := &types.ProductionToolCall{
		ID: uuid.NewString(), RunID: run.ID, TenantID: 7, ProjectID: run.ProjectID,
		DocumentID: run.DocumentID, SourceSetID: run.SourceSetID, Attempt: 1, CurrentStep: 0,
		IdempotencyKey: "call-1", ProviderType: types.ProductionToolProviderMCP,
		ProviderID: "search", ToolName: "lookup", RequestSnapshot: types.JSON(`{"b":2,"a":1}`),
		Status: types.ProductionToolCallPendingApproval, ApprovalStatus: types.ProductionToolApprovalPending,
		ApprovalRequestedAt: &now,
	}
	require.NoError(t, repo.CreateToolCall(context.Background(), call))
	require.Equal(t, `{"a":1,"b":2}`, string(call.RequestSnapshot))
	require.Len(t, call.RequestDigest, 64)

	got, err := repo.GetToolCall(context.Background(), 8, call.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Nil(t, got)
	calls, err := repo.ListToolCalls(context.Background(), 7, run.ID)
	require.NoError(t, err)
	require.Len(t, calls, 1)

	expected := interfaces.ProductionToolCallCAS{
		Status: types.ProductionToolCallPendingApproval, Attempt: 1, CurrentStep: 0,
	}
	resolved, err := repo.ResolveToolCall(context.Background(), 7, call.ID, expected,
		types.ProductionToolCallApproved, "reviewer-1")
	require.NoError(t, err)
	require.True(t, resolved)
	resolved, err = repo.ResolveToolCall(context.Background(), 7, call.ID, expected,
		types.ProductionToolCallApproved, "reviewer-2")
	require.NoError(t, err)
	require.False(t, resolved)
}

func TestProductionRunRepositoryTransactionRollback(t *testing.T) {
	repo, db := newProductionRunRepoTestDB(t)
	uow := NewProductionUnitOfWork(db)
	run := newTestProductionRun(7)

	err := uow.WithinTransaction(context.Background(), func(txCtx context.Context) error {
		require.NoError(t, repo.Create(txCtx, run))
		return errors.New("force rollback")
	})
	require.ErrorContains(t, err, "force rollback")

	got, err := repo.Get(context.Background(), 7, run.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Nil(t, got)
}

func TestProductionRunRepositoryTerminalAndCancellationGuards(t *testing.T) {
	repo, _ := newProductionRunRepoTestDB(t)
	run := newTestProductionRun(7)
	require.NoError(t, repo.Create(context.Background(), run))
	completedAt := time.Now().UTC()
	changed, err := repo.Transition(context.Background(), 7, run.ID, runCAS(run),
		types.ProductionRunCancelled, interfaces.ProductionRunPatch{CompletedAt: &completedAt})
	require.NoError(t, err)
	require.True(t, changed)

	cancelled, err := repo.Get(context.Background(), 7, run.ID)
	require.NoError(t, err)
	changed, err = repo.Transition(context.Background(), 7, run.ID, runCAS(cancelled),
		types.ProductionRunQueued, interfaces.ProductionRunPatch{})
	require.ErrorContains(t, err, "terminal")
	require.False(t, changed)
}

func TestProductionRunPostgresSQLContractsScopeCASAndLocking(t *testing.T) {
	for _, fragment := range []string{
		"tenant_id =", "id =", "status =", "attempt =", "current_step =", "FOR UPDATE",
	} {
		require.Contains(t, strings.ToUpper(postgresProductionRunLockSQL), strings.ToUpper(fragment))
	}
	for _, fragment := range []string{"tenant_id =", "id =", "status =", "attempt =", "current_step =", "RETURNING"} {
		require.Contains(t, strings.ToUpper(postgresProductionRunClaimSQL), strings.ToUpper(fragment))
	}
}
