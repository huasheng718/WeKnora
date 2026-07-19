package repository

import (
	"context"
	"errors"
	"fmt"
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

const (
	repoProjectID    = "00000000-0000-4000-8000-000000000001"
	repoTypeID       = "00000000-0000-4000-8000-000000000002"
	repoSourceSetID  = "00000000-0000-4000-8000-000000000003"
	repoDocumentID   = "00000000-0000-4000-8000-000000000004"
	repoSourceItemID = "00000000-0000-4000-8000-000000000005"
	repoEvidenceID   = "00000000-0000-4000-8000-000000000006"
	repoActorID      = "00000000-0000-4000-8000-000000000007"
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
	seedProductionRunContext(t, db, 7, repoProjectID, repoTypeID, repoSourceSetID, repoDocumentID)
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
	VALUES (?, ?, ?, ?, 'collecting', 'owner-1')`, sourceSetID, tenantID, projectID, documentTypeID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO production_source_items
    (id, source_set_id, source_kind, title, mime_type, content_digest, captured_at, metadata, status)
VALUES (?, ?, 'manual', 'Evidence', 'application/json', ?, CURRENT_TIMESTAMP, '{}', 'accepted')`,
		repoSourceItemID, sourceSetID, strings.Repeat("a", 64)).Error)
	require.NoError(t, db.Exec(`
INSERT INTO production_evidence_snapshots
    (id, source_item_id, snapshot_type, inline_content, content_digest, redaction_metadata)
VALUES (?, ?, 'tool_result', '{"ok":true}', ?, '{}')`,
		repoEvidenceID, repoSourceItemID, strings.Repeat("b", 64)).Error)
	require.NoError(t, db.Exec(`
UPDATE production_source_sets SET status = 'frozen', frozen_at = CURRENT_TIMESTAMP WHERE id = ?`,
		sourceSetID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO production_documents
    (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, status, created_by)
VALUES (?, ?, ?, ?, 1, ?, 'draft', 'owner-1')`, documentID, tenantID, projectID, documentTypeID, documentID).Error)
}

func newTestProductionRun(tenantID uint64) *types.ProductionRun {
	return &types.ProductionRun{
		ID:                   uuid.NewString(),
		TenantID:             tenantID,
		ProjectID:            repoProjectID,
		DocumentID:           repoDocumentID,
		SourceSetID:          repoSourceSetID,
		RunType:              types.ProductionRunWrite,
		Status:               types.ProductionRunQueued,
		Attempt:              1,
		CurrentStep:          0,
		WakeupVersion:        1,
		StatePayload:         types.JSON(`{"z":2,"a":1}`),
		ModelID:              "model-1",
		DocumentTypeSnapshot: types.JSON(`{"version":1,"name":"report"}`),
		IdempotencyKey:       uuid.NewString(),
	}
}

func runCAS(run *types.ProductionRun) interfaces.ProductionRunCAS {
	return interfaces.ProductionRunCAS{
		Status: run.Status, Attempt: run.Attempt, CurrentStep: run.CurrentStep,
		WakeupVersion: run.WakeupVersion,
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
	for _, key := range []string{
		"access_token", "Access-Token", "refreshToken", "client_secret", "API-KEY", "apikey",
		"Authorization", "password", "token", "secret", "credentials", "private-key",
	} {
		t.Run(key, func(t *testing.T) {
			repo, db := newProductionRunRepoTestDB(t)
			run := newTestProductionRun(7)
			run.StatePayload = types.JSON(fmt.Sprintf(`{"nested":[{"%s":"must-not-persist"}]}`, key))

			err := repo.Create(context.Background(), run)

			require.ErrorContains(t, err, "credential")
			var count int64
			require.NoError(t, db.Model(&types.ProductionRun{}).Where("id = ?", run.ID).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestProductionRunRepositoryRejectsNonCanonicalUUIDsBeforeWrite(t *testing.T) {
	for _, invalid := range []string{
		"not-a-uuid", strings.ReplaceAll(uuid.NewString(), "-", ""),
		"{" + uuid.NewString() + "}", "urn:uuid:" + uuid.NewString(), strings.ToUpper(uuid.NewString()),
	} {
		t.Run(invalid, func(t *testing.T) {
			repo, db := newProductionRunRepoTestDB(t)
			run := newTestProductionRun(7)
			run.ID = invalid
			err := repo.Create(context.Background(), run)
			require.ErrorContains(t, err, "canonical UUID")
			var count int64
			require.NoError(t, db.Model(&types.ProductionRun{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}

	repo, db := newProductionRunRepoTestDB(t)
	run := newTestProductionRun(7)
	run.ProjectID = "project-1"
	require.ErrorContains(t, repo.Create(context.Background(), run), "project_id")
	var count int64
	require.NoError(t, db.Model(&types.ProductionRun{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProductionRunRepositoryTransitionUsesFullCASAndRejectsInvalidTransition(t *testing.T) {
	repo, _ := newProductionRunRepoTestDB(t)
	run := newTestProductionRun(7)
	require.NoError(t, repo.Create(context.Background(), run))

	_, changed, err := repo.Transition(context.Background(), 7, run.ID,
		interfaces.ProductionRunCAS{Status: types.ProductionRunQueued, Attempt: 2, CurrentStep: 0, WakeupVersion: 1},
		types.ProductionRunRunning, interfaces.ProductionRunPatch{})
	require.NoError(t, err)
	require.False(t, changed)

	_, changed, err = repo.Transition(context.Background(), 7, run.ID, runCAS(run),
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
			_, claimed, err := repo.Claim(context.Background(), 7, run.ID, runCAS(run), time.Minute)
			results <- claimed
			errs <- err
		}()
	}
	ready.Wait()
	close(start)

	winners := 0
	for i := 0; i < 2; i++ {
		err := <-errs
		if err != nil {
			require.ErrorIs(t, err, types.ErrProductionRunLeaseActive)
		}
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
	require.NoError(t, repo.Create(context.Background(), run))
	require.NoError(t, db.Exec(`UPDATE production_runs
        SET updated_at = datetime(CURRENT_TIMESTAMP, '-10 minutes')
        WHERE tenant_id = ? AND id = ?`, 7, run.ID).Error)

	claimed, ok, err := repo.Claim(context.Background(), 7, run.ID, runCAS(run), time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 2, claimed.Attempt)

	nextStep := 1
	_, changed, err := repo.Transition(context.Background(), 7, run.ID, runCAS(run),
		types.ProductionRunQueued, interfaces.ProductionRunPatch{CurrentStep: &nextStep, IncrementWakeup: true})
	require.NoError(t, err)
	require.False(t, changed, "attempt 1 worker must not advance attempt 2")
}

func TestProductionRunRepositoryActiveLeaseReturnsTypedRetryableError(t *testing.T) {
	repo, db := newProductionRunRepoTestDB(t)
	run := newTestProductionRun(7)
	run.Status = types.ProductionRunRunning
	require.NoError(t, repo.Create(context.Background(), run))
	require.NoError(t, db.Exec(`UPDATE production_runs SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, run.ID).Error)

	claimed, ok, err := repo.Claim(context.Background(), 7, run.ID, runCAS(run), 5*time.Minute)

	require.False(t, ok)
	require.Nil(t, claimed)
	var leaseErr *types.ProductionRunLeaseActiveError
	require.ErrorAs(t, err, &leaseErr)
	require.Positive(t, leaseErr.RetryAfter)
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
		types.ProductionToolCallApproved, repoActorID)
	require.NoError(t, err)
	require.True(t, resolved)
	resolved, err = repo.ResolveToolCall(context.Background(), 7, call.ID, expected,
		types.ProductionToolCallApproved, uuid.NewString())
	require.NoError(t, err)
	require.False(t, resolved)
}

func TestProductionRunRepositoryApprovalActorsRequireCanonicalUUIDBeforeWrite(t *testing.T) {
	repo, db := newProductionRunRepoTestDB(t)
	run := newTestProductionRun(7)
	run.Status = types.ProductionRunWaitingApproval
	require.NoError(t, repo.Create(context.Background(), run))
	now := time.Now().UTC()
	call := &types.ProductionToolCall{
		ID: uuid.NewString(), RunID: run.ID, TenantID: 7, ProjectID: run.ProjectID,
		DocumentID: run.DocumentID, SourceSetID: run.SourceSetID, Attempt: 1, CurrentStep: 0,
		IdempotencyKey: "actor-call", ProviderType: types.ProductionToolProviderMCP,
		ProviderID: "search", ToolName: "lookup", RequestSnapshot: types.JSON(`{"query":"ok"}`),
		Status: types.ProductionToolCallPendingApproval, ApprovalStatus: types.ProductionToolApprovalPending,
		ApprovalRequestedAt: &now,
	}
	require.NoError(t, repo.CreateToolCall(context.Background(), call))
	expected := interfaces.ProductionToolCallCAS{
		Status: types.ProductionToolCallPendingApproval, Attempt: 1, CurrentStep: 0,
	}
	for _, invalid := range []string{
		strings.ReplaceAll(uuid.NewString(), "-", ""), "{" + uuid.NewString() + "}",
		"urn:uuid:" + uuid.NewString(), strings.ToUpper(uuid.NewString()),
	} {
		resolved, err := repo.ResolveToolCall(
			context.Background(), 7, call.ID, expected, types.ProductionToolCallApproved, invalid,
		)
		require.ErrorContains(t, err, "canonical UUID")
		require.False(t, resolved)
		persisted, getErr := repo.GetToolCall(context.Background(), 7, call.ID)
		require.NoError(t, getErr)
		require.Equal(t, types.ProductionToolCallPendingApproval, persisted.Status)
		require.Nil(t, persisted.ApprovedBy)
	}

	resolved, err := repo.ResolveToolCall(
		context.Background(), 7, call.ID, expected, types.ProductionToolCallApproved, repoActorID,
	)
	require.NoError(t, err)
	require.True(t, resolved)
	var count int64
	require.NoError(t, db.Model(&types.ProductionToolCall{}).Where("approved_by = ?", repoActorID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestProductionRunRepositoryCreateToolCallValidatesPrepopulatedDecisionActors(t *testing.T) {
	for _, field := range []string{"approved_by", "rejected_by"} {
		t.Run(field, func(t *testing.T) {
			repo, db := newProductionRunRepoTestDB(t)
			run := newTestProductionRun(7)
			run.Status = types.ProductionRunRunning
			require.NoError(t, repo.Create(context.Background(), run))
			now := time.Now().UTC()
			invalid := "urn:uuid:" + uuid.NewString()
			call := &types.ProductionToolCall{
				ID: uuid.NewString(), RunID: run.ID, TenantID: 7, ProjectID: run.ProjectID,
				DocumentID: run.DocumentID, SourceSetID: run.SourceSetID, Attempt: 1, CurrentStep: 0,
				IdempotencyKey: field, ProviderType: types.ProductionToolProviderMCP,
				ProviderID: "search", ToolName: "lookup", RequestSnapshot: types.JSON(`{"query":"ok"}`),
				ApprovalRequestedAt: &now,
			}
			if field == "approved_by" {
				call.Status = types.ProductionToolCallApproved
				call.ApprovalStatus = types.ProductionToolApprovalApproved
				call.ApprovedBy = &invalid
				call.ApprovedAt = &now
			} else {
				call.Status = types.ProductionToolCallRejected
				call.ApprovalStatus = types.ProductionToolApprovalRejected
				call.RejectedBy = &invalid
				call.RejectedAt = &now
				call.CompletedAt = &now
			}

			require.ErrorContains(t, repo.CreateToolCall(context.Background(), call), "canonical UUID")
			var count int64
			require.NoError(t, db.Model(&types.ProductionToolCall{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestProductionRunRepositoryRejectsNonCanonicalToolCallUUIDs(t *testing.T) {
	repo, db := newProductionRunRepoTestDB(t)
	run := newTestProductionRun(7)
	run.Status = types.ProductionRunWaitingApproval
	require.NoError(t, repo.Create(context.Background(), run))
	now := time.Now().UTC()
	call := &types.ProductionToolCall{
		ID: "{" + uuid.NewString() + "}", RunID: run.ID, TenantID: 7,
		ProjectID: run.ProjectID, DocumentID: run.DocumentID, SourceSetID: run.SourceSetID,
		Attempt: 1, CurrentStep: 0, IdempotencyKey: "invalid-call",
		ProviderType: types.ProductionToolProviderMCP, ProviderID: "search", ToolName: "lookup",
		RequestSnapshot: types.JSON(`{"query":"ok"}`), Status: types.ProductionToolCallPendingApproval,
		ApprovalStatus: types.ProductionToolApprovalPending, ApprovalRequestedAt: &now,
	}

	require.ErrorContains(t, repo.CreateToolCall(context.Background(), call), "canonical UUID")
	var count int64
	require.NoError(t, db.Model(&types.ProductionToolCall{}).Count(&count).Error)
	require.Zero(t, count)

	call.ID = uuid.NewString()
	call.RunID = "run-1"
	require.ErrorContains(t, repo.CreateToolCall(context.Background(), call), "run_id")
}

func TestProductionRunRepositoryToolCompletionRollsBackWithStaleParent(t *testing.T) {
	repo, db := newProductionRunRepoTestDB(t)
	uow := NewProductionUnitOfWork(db)
	run := newTestProductionRun(7)
	run.Status = types.ProductionRunRunning
	require.NoError(t, repo.Create(context.Background(), run))
	now := time.Now().UTC()
	actor := uuid.NewString()
	call := &types.ProductionToolCall{
		ID: uuid.NewString(), RunID: run.ID, TenantID: 7, ProjectID: run.ProjectID,
		DocumentID: run.DocumentID, SourceSetID: run.SourceSetID, Attempt: 1, CurrentStep: 0,
		IdempotencyKey: "approved-call", ProviderType: types.ProductionToolProviderMCP,
		ProviderID: "search", ToolName: "lookup", RequestSnapshot: types.JSON(`{"query":"ok"}`),
		Status: types.ProductionToolCallApproved, ApprovalStatus: types.ProductionToolApprovalApproved,
		ApprovalRequestedAt: &now, ApprovedBy: &actor, ApprovedAt: &now,
	}
	require.NoError(t, repo.CreateToolCall(context.Background(), call))

	err := uow.WithinTransaction(context.Background(), func(txCtx context.Context) error {
		completed, transitionErr := repo.TransitionToolCall(
			txCtx, 7, run.ID, call.ID,
			interfaces.ProductionToolCallCAS{Status: call.Status, Attempt: 1, CurrentStep: 0},
			types.ProductionToolCallCompleted,
			interfaces.ProductionToolCallPatch{
				ResponseSnapshot: types.JSON(`{"result":true}`), ResponseEvidenceID: ptr(repoEvidenceID),
				ResponseEvidenceSourceItemID: ptr(repoSourceItemID), CompletedAt: &now,
			},
		)
		require.NoError(t, transitionErr)
		require.True(t, completed)
		stale := runCAS(run)
		stale.Attempt++
		_, changed, parentErr := repo.Transition(
			txCtx, 7, run.ID, stale, types.ProductionRunCompleted,
			interfaces.ProductionRunPatch{CompletedAt: &now},
		)
		if parentErr != nil {
			return parentErr
		}
		if !changed {
			return errors.New("stale parent")
		}
		return nil
	})
	require.ErrorContains(t, err, "stale parent")

	got, getErr := repo.GetToolCall(context.Background(), 7, call.ID)
	require.NoError(t, getErr)
	require.Equal(t, types.ProductionToolCallApproved, got.Status)
	require.Empty(t, got.ResponseSnapshot)
}

func TestProductionRunRepositoryWakeupMarkHasSingleWinner(t *testing.T) {
	repo, db := newProductionRunRepoTestDB(t)
	run := newTestProductionRun(7)
	run.Status = types.ProductionRunRunning
	require.NoError(t, repo.Create(context.Background(), run))
	require.NoError(t, db.Exec(`UPDATE production_runs
        SET updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`, run.ID).Error)
	var before string
	require.NoError(t, db.Raw(`SELECT CAST(updated_at AS TEXT) FROM production_runs WHERE id = ?`, run.ID).Scan(&before).Error)

	for _, stale := range []struct{ attempt, step, version int }{
		{2, 0, 1}, {1, 1, 1}, {1, 0, 2},
	} {
		won, err := repo.MarkWakeupEnqueued(
			context.Background(), 7, run.ID, stale.attempt, stale.step, stale.version,
		)
		require.NoError(t, err)
		require.False(t, won)
	}

	won, err := repo.MarkWakeupEnqueued(context.Background(), 7, run.ID, 1, 0, 1)
	require.NoError(t, err)
	require.True(t, won)
	var after string
	require.NoError(t, db.Raw(`SELECT CAST(updated_at AS TEXT) FROM production_runs WHERE id = ?`, run.ID).Scan(&after).Error)
	require.Equal(t, before, after, "wakeup marking must not renew or alter the database lease timestamp")

	won, err = repo.MarkWakeupEnqueued(context.Background(), 7, run.ID, 1, 0, 1)
	require.NoError(t, err)
	require.False(t, won)
	got, err := repo.Get(context.Background(), 7, run.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got.WakeupEnqueuedVersion)

	terminal := newTestProductionRun(7)
	terminal.Status = types.ProductionRunCancelled
	completedAt := time.Now().UTC()
	terminal.CompletedAt = &completedAt
	require.NoError(t, repo.Create(context.Background(), terminal))
	won, err = repo.MarkWakeupEnqueued(context.Background(), 7, terminal.ID, 1, 0, 1)
	require.NoError(t, err)
	require.False(t, won, "terminal runs cannot mark wakeups")
}

func ptr[T any](value T) *T { return &value }

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
	_, changed, err := repo.Transition(context.Background(), 7, run.ID, runCAS(run),
		types.ProductionRunCancelled, interfaces.ProductionRunPatch{CompletedAt: &completedAt})
	require.NoError(t, err)
	require.True(t, changed)

	cancelled, err := repo.Get(context.Background(), 7, run.ID)
	require.NoError(t, err)
	_, changed, err = repo.Transition(context.Background(), 7, run.ID, runCAS(cancelled),
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
	for _, fragment := range []string{
		"tenant_id =", "id =", "status =", "attempt =", "current_step =", "wakeup_version =",
		"CURRENT_TIMESTAMP", "INTERVAL '1 second'", "RETURNING",
	} {
		require.Contains(t, strings.ToUpper(postgresProductionRunClaimSQL), strings.ToUpper(fragment))
	}
	for _, fragment := range []string{
		"tenant_id =", "id =", "status =", "attempt =", "current_step =", "wakeup_version =",
		"CURRENT_TIMESTAMP", "julianday(updated_at)", "RETURNING",
	} {
		require.Contains(t, strings.ToUpper(sqliteProductionRunClaimSQL), strings.ToUpper(fragment))
	}
}
