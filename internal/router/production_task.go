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
}

func NewProductionRunTaskHandler(orchestrator interfaces.ProductionRunOrchestrator) *ProductionRunTaskHandler {
	return &ProductionRunTaskHandler{orchestrator: orchestrator}
}

func (h *ProductionRunTaskHandler) Handle(ctx context.Context, task *asynq.Task) error {
	if h == nil || h.orchestrator == nil || task == nil {
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
	return h.orchestrator.HandleRun(ctx, payload)
}

var _ interfaces.TaskHandler = (*ProductionRunTaskHandler)(nil)
