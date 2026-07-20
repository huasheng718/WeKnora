package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	orchProjectID    = "10000000-0000-4000-8000-000000000001"
	orchTypeID       = "10000000-0000-4000-8000-000000000002"
	orchSourceSetID  = "10000000-0000-4000-8000-000000000003"
	orchDocumentID   = "10000000-0000-4000-8000-000000000004"
	orchSourceItemID = "10000000-0000-4000-8000-000000000005"
	orchEvidenceID   = "10000000-0000-4000-8000-000000000006"
)

type productionStepExecutorFake struct {
	mu    sync.Mutex
	calls int
	fn    func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error)
}

func (f *productionStepExecutorFake) ExecuteStep(
	_ context.Context,
	run *types.ProductionRun,
	calls []*types.ProductionToolCall,
) (ProductionStepResult, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.fn(run, calls)
}

func (f *productionStepExecutorFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type productionTaskEnqueuerFake struct {
	mu       sync.Mutex
	accepted map[string]types.ProductionRunPayload
	failNext error
	calls    int
}

func newProductionTaskEnqueuerFake() *productionTaskEnqueuerFake {
	return &productionTaskEnqueuerFake{accepted: make(map[string]types.ProductionRunPayload)}
}

func (f *productionTaskEnqueuerFake) Enqueue(
	task *asynq.Task,
	opts ...asynq.Option,
) (*asynq.TaskInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return nil, err
	}
	var taskID string
	for _, opt := range opts {
		if opt.Type() == asynq.TaskIDOpt {
			taskID, _ = opt.Value().(string)
		}
	}
	if taskID == "" {
		return nil, errors.New("production task must have deterministic task id")
	}
	if _, exists := f.accepted[taskID]; exists {
		return nil, asynq.ErrTaskIDConflict
	}
	var payload types.ProductionRunPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return nil, err
	}
	f.accepted[taskID] = payload
	return &asynq.TaskInfo{ID: taskID, Queue: types.QueueProduction, Type: task.Type()}, nil
}

func (f *productionTaskEnqueuerFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.accepted)
}

func (f *productionTaskEnqueuerFake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type productionOrchestratorFixture struct {
	repo         interfaces.ProductionRunRepository
	db           *gorm.DB
	run          *types.ProductionRun
	executor     *productionStepExecutorFake
	enqueuer     *productionTaskEnqueuerFake
	orchestrator *ProductionOrchestrator
}

func newProductionOrchestratorFixture(t *testing.T, currentStep int) *productionOrchestratorFixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "orchestrator.db")
	db, err := gorm.Open(sqlite.Open(
		"file:"+dbPath+"?_foreign_keys=1&_busy_timeout=10000&_journal_mode=WAL",
	), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })

	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migrationRoot := filepath.Join(filepath.Dir(filename), "../../../migrations/sqlite")
	for _, name := range []string{
		"000001_knowledge_production_foundation.up.sql",
		"000002_knowledge_production_documents.up.sql",
		"000003_knowledge_production_runs.up.sql",
	} {
		migration, readErr := os.ReadFile(filepath.Join(migrationRoot, name))
		require.NoError(t, readErr)
		require.NoError(t, db.Exec(string(migration)).Error)
	}
	seedProductionOrchestratorContext(t, db)

	runRepo := repository.NewProductionRunRepository(db)
	run := &types.ProductionRun{
		ID: uuid.NewString(), TenantID: 7, ProjectID: orchProjectID, DocumentID: orchDocumentID,
		SourceSetID: orchSourceSetID, RunType: types.ProductionRunWrite,
		Status: types.ProductionRunQueued, Attempt: 1, CurrentStep: currentStep,
		WakeupVersion: 1, WakeupEnqueuedVersion: 1,
		StatePayload: types.JSON(`{"seed":true}`), ModelID: "model-1",
		DocumentTypeSnapshot: types.JSON(`{"version":1}`), IdempotencyKey: uuid.NewString(),
	}
	require.NoError(t, runRepo.Create(context.Background(), run))
	persistedRun, err := runRepo.Get(context.Background(), 7, run.ID)
	require.NoError(t, err)
	require.Equal(t, 1, persistedRun.WakeupVersion)
	require.Equal(t, 1, persistedRun.WakeupEnqueuedVersion)
	executor := &productionStepExecutorFake{}
	enqueuer := newProductionTaskEnqueuerFake()
	orchestrator := NewProductionOrchestrator(
		runRepo, repository.NewProductionUnitOfWork(db), executor, enqueuer,
	)
	orchestrator.now = func() time.Time { return time.Date(2026, 7, 19, 1, 0, 0, 0, time.UTC) }
	orchestrator.leaseTTL = time.Minute
	return &productionOrchestratorFixture{
		repo: runRepo, db: db, run: run, executor: executor,
		enqueuer: enqueuer, orchestrator: orchestrator,
	}
}

func seedProductionOrchestratorContext(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO production_projects (id, tenant_id, name, owner_user_id, status)
VALUES (?, 7, 'Project', 'owner-1', 'active')`, orchProjectID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO production_document_types
    (id, tenant_id, code, name, schema_version, status, created_by)
VALUES (?, 7, 'type-1', 'Type', 1, 'active', 'owner-1')`, orchTypeID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO production_source_sets
    (id, tenant_id, project_id, document_type_id, status, created_by)
VALUES (?, 7, ?, ?, 'collecting', 'owner-1')`, orchSourceSetID, orchProjectID, orchTypeID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO production_source_items
    (id, source_set_id, source_kind, title, mime_type, content_digest, captured_at, metadata, status)
VALUES (?, ?, 'manual', 'Evidence', 'application/json', ?, CURRENT_TIMESTAMP, '{}', 'accepted')`,
		orchSourceItemID, orchSourceSetID, strings.Repeat("a", 64)).Error)
	require.NoError(t, db.Exec(`
INSERT INTO production_evidence_snapshots
    (id, source_item_id, snapshot_type, inline_content, content_digest, redaction_metadata)
VALUES (?, ?, 'tool_result', '{"ok":true}', ?, '{}')`,
		orchEvidenceID, orchSourceItemID, strings.Repeat("b", 64)).Error)
	require.NoError(t, db.Exec(`UPDATE production_source_sets
        SET status = 'frozen', frozen_at = CURRENT_TIMESTAMP WHERE id = ?`, orchSourceSetID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO production_documents
    (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, status, created_by)
VALUES (?, 7, ?, ?, 1, 'Document', 'draft', 'owner-1')`, orchDocumentID, orchProjectID, orchTypeID).Error)
}

func (f *productionOrchestratorFixture) payload() types.ProductionRunPayload {
	return types.ProductionRunPayload{TenantID: 7, RunID: f.run.ID, Attempt: 1}
}

func (f *productionOrchestratorFixture) load(t *testing.T) *types.ProductionRun {
	t.Helper()
	run, err := f.repo.Get(context.Background(), 7, f.run.ID)
	require.NoError(t, err)
	return run
}

func approvalRequiredCall() *types.ProductionToolCall {
	now := time.Date(2026, 7, 19, 1, 0, 0, 0, time.UTC)
	return &types.ProductionToolCall{
		ID: uuid.NewString(), IdempotencyKey: uuid.NewString(),
		ProviderType: types.ProductionToolProviderSkill, ProviderID: "research",
		ToolName: "collect", RequestSnapshot: types.JSON(`{"query":"evidence"}`),
		Status:         types.ProductionToolCallPendingApproval,
		ApprovalStatus: types.ProductionToolApprovalPending, ApprovalRequestedAt: &now,
	}
}

func completedToolResult(callID string) *ProductionToolCallResult {
	return &ProductionToolCallResult{
		ToolCallID: callID, Status: types.ProductionToolCallCompleted,
		ResponseSnapshot:   types.JSON(`{"result":true}`),
		ResponseEvidenceID: orchEvidenceID, ResponseEvidenceSourceItemID: orchSourceItemID,
	}
}

func TestProductionOrchestratorHandleRunExecutesOneStepAndReturns(t *testing.T) {
	f := newProductionOrchestratorFixture(t, 0)
	f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{StatePayload: types.JSON(`{"step":1}`)}, nil
	}

	require.NoError(t, f.orchestrator.HandleRun(context.Background(), f.payload()))

	run := f.load(t)
	require.Equal(t, types.ProductionRunQueued, run.Status)
	require.Equal(t, 1, run.CurrentStep)
	require.Equal(t, 1, f.executor.count())
	require.Equal(t, 1, f.enqueuer.count())
}

func TestProductionOrchestratorCreateRunValidatesWakeupBeforePersisting(t *testing.T) {
	f := newProductionOrchestratorFixture(t, 0)
	run := &types.ProductionRun{
		ID: "not-a-canonical-uuid", TenantID: 7, ProjectID: orchProjectID, DocumentID: orchDocumentID,
		SourceSetID: orchSourceSetID, RunType: types.ProductionRunWrite,
		StatePayload: types.JSON(`{"seed":true}`), ModelID: "model-1",
		DocumentTypeSnapshot: types.JSON(`{"version":1}`), IdempotencyKey: uuid.NewString(),
	}

	err := f.orchestrator.CreateRun(context.Background(), run)

	require.ErrorContains(t, err, "run_id")
	got, getErr := f.repo.Get(context.Background(), 7, run.ID)
	require.ErrorIs(t, getErr, gorm.ErrRecordNotFound)
	require.Nil(t, got)
}

func TestProductionOrchestratorWaitingApprovalDoesNotBlockOrEnqueue(t *testing.T) {
	f := newProductionOrchestratorFixture(t, 0)
	f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{ToolCall: approvalRequiredCall()}, nil
	}

	require.NoError(t, f.orchestrator.HandleRun(context.Background(), f.payload()))

	run := f.load(t)
	require.Equal(t, types.ProductionRunWaitingApproval, run.Status)
	require.Zero(t, f.enqueuer.count())
	calls, err := f.repo.ListToolCalls(context.Background(), 7, f.run.ID)
	require.NoError(t, err)
	require.Len(t, calls, 1)
}

func TestProductionOrchestratorApprovedResumeUsesPersistedStepOnFreshInstance(t *testing.T) {
	f := newProductionOrchestratorFixture(t, 2)
	f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{ToolCall: approvalRequiredCall()}, nil
	}
	require.NoError(t, f.orchestrator.HandleRun(context.Background(), f.payload()))
	calls, err := f.repo.ListToolCalls(context.Background(), 7, f.run.ID)
	require.NoError(t, err)

	resumed, err := f.orchestrator.ResolveDecision(
		context.Background(), 7, calls[0].ID, types.ProductionToolCallApproved, uuid.NewString(),
	)
	require.NoError(t, err)
	require.True(t, resumed)

	freshExecutor := &productionStepExecutorFake{}
	freshExecutor.fn = func(run *types.ProductionRun, approved []*types.ProductionToolCall) (ProductionStepResult, error) {
		require.Equal(t, 2, run.CurrentStep)
		require.Len(t, approved, 1)
		return ProductionStepResult{
			StatePayload: types.JSON(`{"step":3}`), ToolCallResult: completedToolResult(approved[0].ID),
		}, nil
	}
	fresh := NewProductionOrchestrator(
		f.repo, repository.NewProductionUnitOfWork(f.db), freshExecutor, f.enqueuer,
	)
	fresh.now = f.orchestrator.now
	fresh.leaseTTL = time.Minute
	require.NoError(t, fresh.HandleRun(context.Background(), f.payload()))
	require.Equal(t, 3, f.load(t).CurrentStep)
	completedCall, err := f.repo.GetToolCall(context.Background(), 7, calls[0].ID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionToolCallCompleted, completedCall.Status)
	require.Len(t, *completedCall.ResponseDigest, 64)
}

func TestProductionOrchestratorDuplicateApprovalHasOneWinnerAndOneEnqueue(t *testing.T) {
	f := newProductionOrchestratorFixture(t, 0)
	f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{ToolCall: approvalRequiredCall()}, nil
	}
	require.NoError(t, f.orchestrator.HandleRun(context.Background(), f.payload()))
	calls, err := f.repo.ListToolCalls(context.Background(), 7, f.run.ID)
	require.NoError(t, err)

	start := make(chan struct{})
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			ready.Done()
			<-start
			won, resolveErr := f.orchestrator.ResolveDecision(
				context.Background(), 7, calls[0].ID, types.ProductionToolCallApproved, uuid.NewString(),
			)
			results <- won
			errs <- resolveErr
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
	require.Equal(t, 1, f.enqueuer.count())
}

func TestProductionOrchestratorCrashAfterPersistBeforeEnqueueIsRecoverable(t *testing.T) {
	f := newProductionOrchestratorFixture(t, 0)
	f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{StatePayload: types.JSON(`{"step":1}`)}, nil
	}
	f.enqueuer.failNext = errors.New("queue unavailable")

	err := f.orchestrator.HandleRun(context.Background(), f.payload())
	require.ErrorContains(t, err, "queue unavailable")
	require.Equal(t, 1, f.load(t).CurrentStep)
	require.Equal(t, types.ProductionRunQueued, f.load(t).Status)

	freshExecutor := &productionStepExecutorFake{}
	freshExecutor.fn = func(run *types.ProductionRun, _ []*types.ProductionToolCall) (ProductionStepResult, error) {
		require.Equal(t, 1, run.CurrentStep)
		return ProductionStepResult{StatePayload: types.JSON(`{"done":true}`), Complete: true}, nil
	}
	fresh := NewProductionOrchestrator(
		f.repo, repository.NewProductionUnitOfWork(f.db), freshExecutor, f.enqueuer,
	)
	fresh.now = f.orchestrator.now
	fresh.leaseTTL = time.Minute
	require.NoError(t, fresh.HandleRun(context.Background(), f.payload()))
	require.Equal(t, types.ProductionRunCompleted, f.load(t).Status)
}

func TestProductionOrchestratorFailureCancellationAndTerminalResumeGuards(t *testing.T) {
	t.Run("executor failure is durable", func(t *testing.T) {
		f := newProductionOrchestratorFixture(t, 0)
		f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
			return ProductionStepResult{}, errors.New("model failed")
		}
		require.NoError(t, f.orchestrator.HandleRun(context.Background(), f.payload()))
		run := f.load(t)
		require.Equal(t, types.ProductionRunFailed, run.Status)
		require.NotNil(t, run.ErrorMessage)
		require.Contains(t, *run.ErrorMessage, "model failed")
	})

	t.Run("cancelled cannot resume or execute", func(t *testing.T) {
		f := newProductionOrchestratorFixture(t, 0)
		cancelled, err := f.orchestrator.Cancel(context.Background(), 7, f.run.ID, uuid.NewString())
		require.NoError(t, err)
		require.True(t, cancelled)
		resumed, err := f.orchestrator.Resume(context.Background(), 7, f.run.ID, 1)
		require.ErrorContains(t, err, "terminal")
		require.False(t, resumed)
		require.NoError(t, f.orchestrator.HandleRun(context.Background(), f.payload()))
		require.Zero(t, f.executor.count())
	})
}

func TestProductionOrchestratorRejectsInvalidAndCrossTenantPayload(t *testing.T) {
	f := newProductionOrchestratorFixture(t, 0)
	f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{}, nil
	}

	require.Error(t, f.orchestrator.HandleRun(context.Background(), types.ProductionRunPayload{}))
	err := f.orchestrator.HandleRun(context.Background(), types.ProductionRunPayload{
		TenantID: 8, RunID: f.run.ID, Attempt: 1,
	})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Zero(t, f.executor.count())
}

func TestProductionOrchestratorEarlyRedeliveryReturnsLeaseActiveThenDatabaseExpiryReclaims(t *testing.T) {
	f := newProductionOrchestratorFixture(t, 0)
	f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{StatePayload: types.JSON(`{"reclaimed":true}`)}, nil
	}
	require.NoError(t, f.db.Exec(`UPDATE production_runs
        SET status = 'running', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, f.run.ID).Error)

	err := f.orchestrator.HandleRun(context.Background(), f.payload())
	var leaseErr *types.ProductionRunLeaseActiveError
	require.ErrorAs(t, err, &leaseErr)
	require.Zero(t, f.executor.count())

	require.NoError(t, f.db.Exec(`UPDATE production_runs
        SET updated_at = datetime(CURRENT_TIMESTAMP, '-10 minutes') WHERE id = ?`, f.run.ID).Error)
	require.NoError(t, f.orchestrator.HandleRun(context.Background(), f.payload()))
	require.Equal(t, 1, f.executor.count())
	require.Equal(t, 2, f.load(t).Attempt)
}

func TestProductionOrchestratorApprovedExecutorFailureTerminalizesCallAndRun(t *testing.T) {
	f := newProductionOrchestratorFixture(t, 0)
	f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{ToolCall: approvalRequiredCall()}, nil
	}
	require.NoError(t, f.orchestrator.HandleRun(context.Background(), f.payload()))
	calls, err := f.repo.ListToolCalls(context.Background(), 7, f.run.ID)
	require.NoError(t, err)
	_, err = f.orchestrator.ResolveDecision(
		context.Background(), 7, calls[0].ID, types.ProductionToolCallApproved, uuid.NewString(),
	)
	require.NoError(t, err)
	f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{}, errors.New("approved tool execution failed")
	}

	require.NoError(t, f.orchestrator.HandleRun(context.Background(), f.payload()))
	run := f.load(t)
	require.Equal(t, types.ProductionRunFailed, run.Status)
	call, err := f.repo.GetToolCall(context.Background(), 7, calls[0].ID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionToolCallFailed, call.Status)
	require.NotNil(t, call.CompletedAt)
}

func TestProductionOrchestratorCancelTerminalizesPendingAndApprovedChildren(t *testing.T) {
	for _, approved := range []bool{false, true} {
		name := "pending"
		if approved {
			name = "approved"
		}
		t.Run(name, func(t *testing.T) {
			f := newProductionOrchestratorFixture(t, 0)
			f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
				return ProductionStepResult{ToolCall: approvalRequiredCall()}, nil
			}
			require.NoError(t, f.orchestrator.HandleRun(context.Background(), f.payload()))
			calls, err := f.repo.ListToolCalls(context.Background(), 7, f.run.ID)
			require.NoError(t, err)
			if approved {
				_, err = f.orchestrator.ResolveDecision(
					context.Background(), 7, calls[0].ID, types.ProductionToolCallApproved, uuid.NewString(),
				)
				require.NoError(t, err)
			}

			cancelled, err := f.orchestrator.Cancel(context.Background(), 7, f.run.ID, uuid.NewString())
			require.NoError(t, err)
			require.True(t, cancelled)
			require.Equal(t, types.ProductionRunCancelled, f.load(t).Status)
			call, err := f.repo.GetToolCall(context.Background(), 7, calls[0].ID)
			require.NoError(t, err)
			if approved {
				require.Equal(t, types.ProductionToolCallFailed, call.Status)
			} else {
				require.Equal(t, types.ProductionToolCallRejected, call.Status)
			}
		})
	}
}

func TestProductionOrchestratorDecisionRetriesPendingWakeupAfterEnqueueFailure(t *testing.T) {
	f := newProductionOrchestratorFixture(t, 0)
	f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{ToolCall: approvalRequiredCall()}, nil
	}
	require.NoError(t, f.orchestrator.HandleRun(context.Background(), f.payload()))
	calls, err := f.repo.ListToolCalls(context.Background(), 7, f.run.ID)
	require.NoError(t, err)
	f.enqueuer.failNext = errors.New("queue unavailable")

	won, err := f.orchestrator.ResolveDecision(
		context.Background(), 7, calls[0].ID, types.ProductionToolCallApproved, uuid.NewString(),
	)
	require.ErrorContains(t, err, "queue unavailable")
	require.False(t, won)
	run := f.load(t)
	require.Greater(t, run.WakeupVersion, run.WakeupEnqueuedVersion)

	won, err = f.orchestrator.ResolveDecision(
		context.Background(), 7, calls[0].ID, types.ProductionToolCallApproved, uuid.NewString(),
	)
	require.NoError(t, err)
	require.True(t, won)
	run = f.load(t)
	require.Equal(t, run.WakeupVersion, run.WakeupEnqueuedVersion)
	callsBefore := f.enqueuer.callCount()
	won, err = f.orchestrator.ResolveDecision(
		context.Background(), 7, calls[0].ID, types.ProductionToolCallApproved, uuid.NewString(),
	)
	require.NoError(t, err)
	require.False(t, won)
	require.Equal(t, callsBefore, f.enqueuer.callCount())
}

func TestProductionOrchestratorConcurrentResumeHasOneEnqueueOwner(t *testing.T) {
	f := newProductionOrchestratorFixture(t, 0)
	f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{ToolCall: approvalRequiredCall()}, nil
	}
	require.NoError(t, f.orchestrator.HandleRun(context.Background(), f.payload()))
	calls, err := f.repo.ListToolCalls(context.Background(), 7, f.run.ID)
	require.NoError(t, err)
	f.enqueuer.failNext = errors.New("queue unavailable")
	_, err = f.orchestrator.ResolveDecision(
		context.Background(), 7, calls[0].ID, types.ProductionToolCallApproved, uuid.NewString(),
	)
	require.Error(t, err)

	start := make(chan struct{})
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			ready.Done()
			<-start
			won, resumeErr := f.orchestrator.Resume(context.Background(), 7, f.run.ID, 1)
			results <- won
			errs <- resumeErr
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
	require.Equal(t, 1, f.enqueuer.count())
}

type failWakeupMarkRepository struct {
	interfaces.ProductionRunRepository
	mu       sync.Mutex
	failOnce bool
}

func (r *failWakeupMarkRepository) MarkWakeupEnqueued(
	ctx context.Context, tenantID uint64, runID string, attempt, currentStep, wakeupVersion int,
) (bool, error) {
	r.mu.Lock()
	if r.failOnce {
		r.failOnce = false
		r.mu.Unlock()
		return false, errors.New("crash before wakeup mark")
	}
	r.mu.Unlock()
	return r.ProductionRunRepository.MarkWakeupEnqueued(ctx, tenantID, runID, attempt, currentStep, wakeupVersion)
}

func TestProductionOrchestratorEnqueueBeforeMarkRecoversThroughDeterministicConflict(t *testing.T) {
	f := newProductionOrchestratorFixture(t, 0)
	f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{ToolCall: approvalRequiredCall()}, nil
	}
	require.NoError(t, f.orchestrator.HandleRun(context.Background(), f.payload()))
	calls, err := f.repo.ListToolCalls(context.Background(), 7, f.run.ID)
	require.NoError(t, err)
	failingRepo := &failWakeupMarkRepository{ProductionRunRepository: f.repo, failOnce: true}
	orchestrator := NewProductionOrchestrator(
		failingRepo, repository.NewProductionUnitOfWork(f.db), f.executor, f.enqueuer,
	)
	orchestrator.now = f.orchestrator.now
	orchestrator.leaseTTL = time.Minute

	won, err := orchestrator.ResolveDecision(
		context.Background(), 7, calls[0].ID, types.ProductionToolCallApproved, uuid.NewString(),
	)
	require.ErrorContains(t, err, "crash before wakeup mark")
	require.True(t, won, "the caller that accepted the enqueue owns it even when marking crashes")
	require.Equal(t, 1, f.enqueuer.count())
	run := f.load(t)
	require.Greater(t, run.WakeupVersion, run.WakeupEnqueuedVersion)

	won, err = f.orchestrator.ResolveDecision(
		context.Background(), 7, calls[0].ID, types.ProductionToolCallApproved, uuid.NewString(),
	)
	require.NoError(t, err)
	require.False(t, won, "deterministic conflict recovers the mark but does not own the enqueue")
	run = f.load(t)
	require.Equal(t, run.WakeupVersion, run.WakeupEnqueuedVersion)
}

type failAdvancingTransitionRepository struct {
	interfaces.ProductionRunRepository
}

func (r failAdvancingTransitionRepository) Transition(
	ctx context.Context,
	tenantID uint64,
	runID string,
	expected interfaces.ProductionRunCAS,
	to types.ProductionRunStatus,
	patch interfaces.ProductionRunPatch,
) (*types.ProductionRun, bool, error) {
	if expected.Status == types.ProductionRunRunning && to == types.ProductionRunQueued {
		return nil, false, nil
	}
	return r.ProductionRunRepository.Transition(ctx, tenantID, runID, expected, to, patch)
}

func TestProductionOrchestratorStaleParentRollsBackToolTerminalization(t *testing.T) {
	f := newProductionOrchestratorFixture(t, 0)
	f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{ToolCall: approvalRequiredCall()}, nil
	}
	require.NoError(t, f.orchestrator.HandleRun(context.Background(), f.payload()))
	calls, err := f.repo.ListToolCalls(context.Background(), 7, f.run.ID)
	require.NoError(t, err)
	_, err = f.orchestrator.ResolveDecision(
		context.Background(), 7, calls[0].ID, types.ProductionToolCallApproved, uuid.NewString(),
	)
	require.NoError(t, err)
	f.executor.fn = func(_ *types.ProductionRun, approved []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{
			StatePayload: types.JSON(`{"step":1}`), ToolCallResult: completedToolResult(approved[0].ID),
		}, nil
	}
	orchestrator := NewProductionOrchestrator(
		failAdvancingTransitionRepository{ProductionRunRepository: f.repo},
		repository.NewProductionUnitOfWork(f.db), f.executor, f.enqueuer,
	)
	orchestrator.now = f.orchestrator.now
	orchestrator.leaseTTL = time.Minute

	err = orchestrator.HandleRun(context.Background(), f.payload())
	require.ErrorContains(t, err, "stale")
	call, getErr := f.repo.GetToolCall(context.Background(), 7, calls[0].ID)
	require.NoError(t, getErr)
	require.Equal(t, types.ProductionToolCallApproved, call.Status)
	require.Equal(t, types.ProductionRunRunning, f.load(t).Status)
}

func TestProductionOrchestratorRejectsStepResultRawModelResponse(t *testing.T) {
	f := newProductionOrchestratorFixture(t, 0)
	f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{RawModelResponse: types.JSON(`"must be audited by the writer"`)}, nil
	}

	err := f.orchestrator.HandleRun(context.Background(), f.payload())

	require.ErrorContains(t, err, "raw model response must be audited before returning a step result")
	run := f.load(t)
	require.Nil(t, run.RawModelResponse)
	require.Nil(t, run.RawModelResponseDigest)
	require.Equal(t, types.ProductionRunRunning, run.Status)
}

func TestProductionOrchestratorRejectsRawStepResultBeforeOtherPersistenceBranches(t *testing.T) {
	for _, test := range []struct {
		name   string
		result ProductionStepResult
	}{
		{
			name: "tool call request",
			result: ProductionStepResult{
				RawModelResponse: types.JSON(`"must be audited by the writer"`), ToolCall: approvalRequiredCall(),
			},
		},
		{
			name: "invalid tool result",
			result: ProductionStepResult{
				RawModelResponse: types.JSON(`"must be audited by the writer"`), ToolCallResult: &ProductionToolCallResult{},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newProductionOrchestratorFixture(t, 0)
			enqueueCalls := f.enqueuer.callCount()
			f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
				return test.result, nil
			}

			err := f.orchestrator.HandleRun(context.Background(), f.payload())

			require.ErrorContains(t, err, "raw model response must be audited before returning a step result")
			run := f.load(t)
			require.Nil(t, run.RawModelResponse)
			require.Nil(t, run.RawModelResponseDigest)
			require.Equal(t, 0, run.CurrentStep)
			require.Equal(t, types.ProductionRunRunning, run.Status)
			calls, listErr := f.repo.ListToolCalls(context.Background(), 7, run.ID)
			require.NoError(t, listErr)
			require.Empty(t, calls)
			require.Equal(t, enqueueCalls, f.enqueuer.callCount())
		})
	}
}

type failWaitingTransitionRepository struct {
	interfaces.ProductionRunRepository
}

func (r failWaitingTransitionRepository) Transition(
	ctx context.Context,
	tenantID uint64,
	runID string,
	expected interfaces.ProductionRunCAS,
	to types.ProductionRunStatus,
	patch interfaces.ProductionRunPatch,
) (*types.ProductionRun, bool, error) {
	if to == types.ProductionRunWaitingApproval {
		return nil, false, nil
	}
	return r.ProductionRunRepository.Transition(ctx, tenantID, runID, expected, to, patch)
}

func TestProductionOrchestratorApprovalTransactionRollsBackToolCall(t *testing.T) {
	f := newProductionOrchestratorFixture(t, 0)
	f.executor.fn = func(*types.ProductionRun, []*types.ProductionToolCall) (ProductionStepResult, error) {
		return ProductionStepResult{ToolCall: approvalRequiredCall()}, nil
	}
	orchestrator := NewProductionOrchestrator(
		failWaitingTransitionRepository{ProductionRunRepository: f.repo},
		repository.NewProductionUnitOfWork(f.db), f.executor, f.enqueuer,
	)
	orchestrator.now = f.orchestrator.now
	orchestrator.leaseTTL = time.Minute

	err := orchestrator.HandleRun(context.Background(), f.payload())
	require.ErrorContains(t, err, "stale")
	calls, listErr := f.repo.ListToolCalls(context.Background(), 7, f.run.ID)
	require.NoError(t, listErr)
	require.Empty(t, calls)
}
