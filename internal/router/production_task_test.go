package router

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type productionProjectionTaskStub struct {
	interfaces.TaskHandler
	calls atomic.Int32
}

func (s *productionProjectionTaskStub) Handle(context.Context, *asynq.Task) error {
	s.calls.Add(1)
	return nil
}

type productionLifecycleServerStub struct {
	started  chan struct{}
	stopped  chan struct{}
	stopOnce sync.Once
}

func (s *productionLifecycleServerStub) Run(asynq.Handler) error {
	close(s.started)
	<-s.stopped
	return nil
}

func (s *productionLifecycleServerStub) Shutdown() {
	s.stopOnce.Do(func() { close(s.stopped) })
}

type productionLifecycleCleanerStub struct {
	name    string
	cleanup types.CleanupFunc
}

func (s *productionLifecycleCleanerStub) Register(cleanup types.CleanupFunc) { s.cleanup = cleanup }
func (s *productionLifecycleCleanerStub) RegisterWithName(name string, cleanup types.CleanupFunc) {
	s.name, s.cleanup = name, cleanup
}
func (s *productionLifecycleCleanerStub) Cleanup(context.Context) []error {
	if s.cleanup == nil {
		return nil
	}
	if err := s.cleanup(); err != nil {
		return []error{err}
	}
	return nil
}

type productionTaskOrchestratorStub struct {
	calls     int
	payload   types.ProductionRunPayload
	principal types.ProductionInternalPrincipal
}

func (s *productionTaskOrchestratorStub) HandleRun(ctx context.Context, payload types.ProductionRunPayload) error {
	s.calls++
	s.payload = payload
	s.principal, _ = types.ProductionInternalPrincipalFromContext(ctx)
	return nil
}

type productionTaskRunRepoStub struct {
	interfaces.ProductionRunRepository
	run *types.ProductionRun
}

func (s *productionTaskRunRepoStub) Get(context.Context, uint64, string) (*types.ProductionRun, error) {
	return s.run, nil
}

func TestProductionRunTaskHandlerValidatesAndDispatchesEveryProductionTaskType(t *testing.T) {
	orchestrator := &productionTaskOrchestratorStub{}
	payload := types.ProductionRunPayload{TenantID: 7, RunID: "74000000-0000-4000-8000-000000000001", Attempt: 1}
	handler := NewProductionRunTaskHandler(orchestrator, &productionTaskRunRepoStub{run: &types.ProductionRun{
		ID: payload.RunID, TenantID: 7, ProjectID: "74000000-0000-4000-8000-000000000002", Attempt: 1,
	}})
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	for _, taskType := range []string{types.TypeProductionCollect, types.TypeProductionWrite, types.TypeProductionValidate} {
		require.NoError(t, handler.Handle(context.Background(), asynq.NewTask(taskType, encoded)))
	}
	require.Equal(t, 3, orchestrator.calls)
	require.Equal(t, payload.RunID, orchestrator.payload.RunID)
	require.Equal(t, types.ProductionInternalActorWorker, orchestrator.principal.ActorKind)
	require.Equal(t, types.ProductionSystemActorID, orchestrator.principal.ActorID)
	require.Equal(t, payload.RunID, orchestrator.principal.RunID)
	require.Error(t, handler.Handle(context.Background(), asynq.NewTask(types.TypeProductionWrite, []byte(`{"tenant_id":7}`))))
}

func TestProductionWorkerPoolUsesOnlyProductionQueueAndConfiguredConcurrency(t *testing.T) {
	config := productionWorkerPoolConfig(types.WorkerPoolConcurrency{Production: 3})
	require.Equal(t, 3, config.Concurrency)
	require.Equal(t, map[string]int{types.QueueProduction: 1}, config.Queues)
}

func TestProductionAsynqServerLifecycleIsOwnedAndGracefullyStopped(t *testing.T) {
	server := &productionLifecycleServerStub{started: make(chan struct{}), stopped: make(chan struct{})}
	cleaner := &productionLifecycleCleanerStub{}
	done := startManagedProductionAsynqServer(server, asynq.HandlerFunc(func(context.Context, *asynq.Task) error {
		return nil
	}), cleaner)

	<-server.started
	require.Equal(t, "ProductionAsynqServer", cleaner.name)
	require.NotNil(t, cleaner.cleanup)
	require.Empty(t, cleaner.Cleanup(context.Background()))
	<-done
}

func TestProductionProjectionTasksRegisterInRedisAndLiteRuntimes(t *testing.T) {
	taskTypes := []string{types.TypeProductionBuild, types.TypeProductionActivate, types.TypeProductionCleanup}

	redisHandler := &productionProjectionTaskStub{}
	mux := asynq.NewServeMux()
	registerProductionProjectionHandlers(mux, redisHandler)
	for _, taskType := range taskTypes {
		require.NoError(t, mux.ProcessTask(context.Background(), asynq.NewTask(taskType, nil)))
	}
	require.EqualValues(t, len(taskTypes), redisHandler.calls.Load())

	liteHandler := &productionProjectionTaskStub{}
	executor := NewSyncTaskExecutor()
	registerSyncProductionProjectionHandlers(executor, liteHandler)
	for _, taskType := range taskTypes {
		_, err := executor.Enqueue(asynq.NewTask(taskType, nil), asynq.MaxRetry(0))
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool { return liteHandler.calls.Load() == int32(len(taskTypes)) }, time.Second, time.Millisecond)
}
