package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
)

type ProductionProjectionTaskHandler struct {
	releases *ProductionReleaseService
	cleanup  *ProductionProjectionCleanup
}

func NewProductionProjectionTaskHandler(
	releases *ProductionReleaseService,
	cleanup *ProductionProjectionCleanup,
) *ProductionProjectionTaskHandler {
	return &ProductionProjectionTaskHandler{releases: releases, cleanup: cleanup}
}

func (h *ProductionProjectionTaskHandler) Handle(ctx context.Context, task *asynq.Task) error {
	if h == nil || h.releases == nil || h.cleanup == nil || task == nil {
		return errors.New("production projection task dependencies are unavailable")
	}
	var payload types.ProductionProjectionTaskPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return err
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	ctx = context.WithValue(ctx, types.TenantIDContextKey, payload.TenantID)
	actorID := payload.ActorUserID
	if actorID == "" {
		actorID = types.ProductionSystemActorID
	}
	ctx = context.WithValue(ctx, types.UserIDContextKey, actorID)
	target, err := h.releases.releases.GetTarget(ctx, payload.TenantID, payload.TargetID)
	if err != nil {
		return err
	}
	if target == nil || target.TenantID != payload.TenantID || target.ProjectID != payload.ProjectID || target.ID != payload.TargetID {
		return types.ErrProductionForbidden
	}
	switch task.Type() {
	case types.TypeProductionBuild:
		if h.releases.builder == nil {
			return errors.New("production projection builder is unavailable")
		}
		_, err = h.releases.builder.Build(ctx, target.ID)
		return err
	case types.TypeProductionActivate:
		return h.releases.activateAuthorized(ctx, target, payload.ExpectedLock, false)
	case types.TypeProductionCleanup:
		return h.cleanup.Cleanup(ctx, target.ID)
	default:
		return errors.New("unsupported production projection task type")
	}
}

var _ interfaces.TaskHandler = (*ProductionProjectionTaskHandler)(nil)
