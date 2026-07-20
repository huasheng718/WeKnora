package router

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type productionTaskOrchestratorStub struct {
	calls   int
	payload types.ProductionRunPayload
}

func (s *productionTaskOrchestratorStub) HandleRun(_ context.Context, payload types.ProductionRunPayload) error {
	s.calls++
	s.payload = payload
	return nil
}

func TestProductionRunTaskHandlerValidatesAndDispatchesEveryProductionTaskType(t *testing.T) {
	orchestrator := &productionTaskOrchestratorStub{}
	handler := NewProductionRunTaskHandler(orchestrator)
	payload := types.ProductionRunPayload{TenantID: 7, RunID: "74000000-0000-4000-8000-000000000001", Attempt: 1}
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	for _, taskType := range []string{types.TypeProductionCollect, types.TypeProductionWrite, types.TypeProductionValidate} {
		require.NoError(t, handler.Handle(context.Background(), asynq.NewTask(taskType, encoded)))
	}
	require.Equal(t, 3, orchestrator.calls)
	require.Equal(t, payload.RunID, orchestrator.payload.RunID)
	require.Error(t, handler.Handle(context.Background(), asynq.NewTask(types.TypeProductionWrite, []byte(`{"tenant_id":7}`))))
}

func TestProductionWorkerPoolUsesOnlyProductionQueueAndConfiguredConcurrency(t *testing.T) {
	config := productionWorkerPoolConfig(types.WorkerPoolConcurrency{Production: 3})
	require.Equal(t, 3, config.Concurrency)
	require.Equal(t, map[string]int{types.QueueProduction: 1}, config.Queues)
}
