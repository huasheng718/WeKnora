package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
		ID: uuid.NewString(), TenantID: 7, ProjectID: "project-1", DocumentID: "document-1",
		SourceSetID: "source-1", RunType: types.ProductionRunWrite,
		Status: types.ProductionRunQueued, Attempt: 1, CurrentStep: currentStep,
		StatePayload: types.JSON(`{"seed":true}`), ModelID: "model-1",
		DocumentTypeSnapshot: types.JSON(`{"version":1}`), IdempotencyKey: uuid.NewString(),
	}
	require.NoError(t, runRepo.Create(context.Background(), run))
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
VALUES ('project-1', 7, 'Project', 'owner-1', 'active')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO production_document_types
    (id, tenant_id, code, name, schema_version, status, created_by)
VALUES ('type-1', 7, 'type-1', 'Type', 1, 'active', 'owner-1')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO production_source_sets
    (id, tenant_id, project_id, document_type_id, status, created_by)
VALUES ('source-1', 7, 'project-1', 'type-1', 'frozen', 'owner-1')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO production_documents
    (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, status, created_by)
VALUES ('document-1', 7, 'project-1', 'type-1', 1, 'Document', 'draft', 'owner-1')`).Error)
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
		ID: "not-a-canonical-uuid", TenantID: 7, ProjectID: "project-1", DocumentID: "document-1",
		SourceSetID: "source-1", RunType: types.ProductionRunWrite,
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
		context.Background(), 7, calls[0].ID, types.ProductionToolCallApproved, "reviewer-1",
	)
	require.NoError(t, err)
	require.True(t, resumed)

	freshExecutor := &productionStepExecutorFake{}
	freshExecutor.fn = func(run *types.ProductionRun, approved []*types.ProductionToolCall) (ProductionStepResult, error) {
		require.Equal(t, 2, run.CurrentStep)
		require.Len(t, approved, 1)
		return ProductionStepResult{StatePayload: types.JSON(`{"step":3}`)}, nil
	}
	fresh := NewProductionOrchestrator(
		f.repo, repository.NewProductionUnitOfWork(f.db), freshExecutor, f.enqueuer,
	)
	fresh.now = f.orchestrator.now
	fresh.leaseTTL = time.Minute
	require.NoError(t, fresh.HandleRun(context.Background(), f.payload()))
	require.Equal(t, 3, f.load(t).CurrentStep)
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
				context.Background(), 7, calls[0].ID, types.ProductionToolCallApproved, "reviewer-1",
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
		cancelled, err := f.orchestrator.Cancel(context.Background(), 7, f.run.ID, "user-1")
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
) (bool, error) {
	if to == types.ProductionRunWaitingApproval {
		return false, nil
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
