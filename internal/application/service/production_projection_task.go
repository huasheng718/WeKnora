package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

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
	if !productionProjectionOperationMatchesTaskType(payload.Operation, task.Type()) {
		return types.ErrProductionReleaseInvalid
	}
	ctx = context.WithValue(ctx, types.TenantIDContextKey, payload.TenantID)
	actorID := payload.ActorUserID
	isUserHeadMutation := payload.Operation == types.ProductionProjectionOperationActivate ||
		payload.Operation == types.ProductionProjectionOperationRollback
	if !isUserHeadMutation && actorID == "" {
		actorID = types.ProductionSystemActorID
	}
	ctx = context.WithValue(ctx, types.UserIDContextKey, actorID)
	target, err := h.releases.releases.GetTarget(ctx, payload.TenantID, payload.TargetID)
	if err != nil {
		return err
	}
	if target == nil || target.TenantID != payload.TenantID || target.ProjectID != payload.ProjectID || target.ID != payload.TargetID {
		return productionHeadMutationTaskError(types.ErrProductionForbidden)
	}
	if isUserHeadMutation {
		if target.Status == types.ReleaseTargetActive {
			ctx = context.WithValue(ctx, types.UserIDContextKey, types.ProductionSystemActorID)
		} else {
			if strings.TrimSpace(actorID) == "" || actorID == types.ProductionSystemActorID {
				return productionHeadMutationTaskError(types.ErrProductionForbidden)
			}
			target, err = h.releases.authorizeTarget(ctx, target.ID)
			if err != nil {
				return productionHeadMutationTaskError(err)
			}
		}
	}
	switch task.Type() {
	case types.TypeProductionBuild:
		if h.releases.builder == nil {
			return errors.New("production projection builder is unavailable")
		}
		_, err = h.releases.builder.Build(ctx, target.ID)
		if err == nil {
			return nil
		}
		if failureErr := h.releases.recordProjectionTargetFailure(ctx, target.ID, task.Type()); failureErr != nil {
			return errors.Join(err, failureErr)
		}
		return err
	case types.TypeProductionActivate:
		if payload.Operation == types.ProductionProjectionOperationRollback {
			return productionHeadMutationTaskError(h.releases.rollbackAuthorized(ctx, target, payload.ExpectedLock))
		}
		return productionHeadMutationTaskError(h.releases.activateAuthorized(ctx, target, payload.ExpectedLock))
	case types.TypeProductionCleanup:
		return h.handleCleanup(ctx, target, payload.CleanupGeneration)
	default:
		return errors.New("unsupported production projection task type")
	}
}

func productionHeadMutationTaskError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, types.ErrProductionReleaseLifecycle) ||
		errors.Is(err, types.ErrProductionProjectionConflict) ||
		errors.Is(err, types.ErrProductionForbidden) {
		return errors.Join(err, asynq.SkipRetry)
	}
	return err
}

func (h *ProductionProjectionTaskHandler) handleCleanup(
	ctx context.Context,
	target *types.ProductionReleaseTarget,
	queuedGeneration string,
) error {
	switch target.Status {
	case types.ReleaseTargetActive, types.ReleaseTargetReady, types.ReleaseTargetBuilding, types.ReleaseTargetCleaned:
		return nil
	}
	currentGeneration, err := productionProjectionCleanupGeneration(target)
	if err != nil {
		return err
	}
	if currentGeneration != queuedGeneration {
		if h.releases.cleanup == nil {
			return errors.New("production cleanup scheduler is unavailable")
		}
		return h.releases.cleanup.EnqueueCleanup(ctx, target)
	}
	return h.cleanup.Cleanup(ctx, target.ID)
}

func productionProjectionOperationMatchesTaskType(operation types.ProductionProjectionOperation, taskType string) bool {
	switch taskType {
	case types.TypeProductionBuild:
		return operation == types.ProductionProjectionOperationBuild
	case types.TypeProductionActivate:
		return operation == types.ProductionProjectionOperationActivate || operation == types.ProductionProjectionOperationRollback
	case types.TypeProductionCleanup:
		return operation == types.ProductionProjectionOperationCleanup
	default:
		return false
	}
}

var _ interfaces.TaskHandler = (*ProductionProjectionTaskHandler)(nil)
