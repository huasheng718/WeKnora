package router

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
)

type ProductionRunTaskHandler struct {
	orchestrator interfaces.ProductionRunOrchestrator
	runs         interfaces.ProductionRunRepository
}

func NewProductionRunTaskHandler(
	orchestrator interfaces.ProductionRunOrchestrator,
	runs interfaces.ProductionRunRepository,
) *ProductionRunTaskHandler {
	return &ProductionRunTaskHandler{orchestrator: orchestrator, runs: runs}
}

func (h *ProductionRunTaskHandler) Handle(ctx context.Context, task *asynq.Task) error {
	if h == nil || h.orchestrator == nil || h.runs == nil || task == nil {
		return errors.New("production run task dependencies are required")
	}
	switch task.Type() {
	case types.TypeProductionCollect, types.TypeProductionWrite, types.TypeProductionValidate:
	default:
		return errors.New("unsupported production task type")
	}
	var payload types.ProductionRunPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return err
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	run, err := h.runs.Get(ctx, payload.TenantID, payload.RunID)
	if err != nil {
		return err
	}
	if run == nil || run.ID != payload.RunID || run.TenantID != payload.TenantID {
		return errors.New("production task run scope is invalid")
	}
	principalCtx, err := types.WithProductionInternalPrincipal(ctx, types.ProductionInternalPrincipal{
		ActorID: types.ProductionSystemActorID, ActorKind: types.ProductionInternalActorWorker,
		TenantID: run.TenantID, ProjectID: run.ProjectID, RunID: run.ID,
	})
	if err != nil {
		return err
	}
	return h.orchestrator.HandleRun(principalCtx, payload)
}

var _ interfaces.TaskHandler = (*ProductionRunTaskHandler)(nil)
