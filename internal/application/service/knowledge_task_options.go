package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/tracing/langfuse"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
)

func documentProcessTaskOptions(cfg *config.Config, extra ...asynq.Option) []asynq.Option {
	opts := []asynq.Option{
		asynq.Queue(types.QueueDefault),
		asynq.Timeout(config.DocumentProcessTimeout(cfg)),
		asynq.MaxRetry(3),
	}
	opts = append(opts, extra...)
	return opts
}

func knowledgePostProcessTaskOptions() []asynq.Option {
	return []asynq.Option{
		asynq.Queue(types.QueuePostProcess),
		asynq.MaxRetry(3),
		asynq.Timeout(30 * time.Minute),
	}
}

func enqueueInitialKnowledgePostProcess(
	ctx context.Context,
	enqueuer interfaces.TaskEnqueuer,
	knowledge *types.Knowledge,
	payload types.KnowledgePostProcessPayload,
) error {
	projection, err := types.ValidateProductionProjectionIntegrity(knowledge)
	if err != nil {
		return err
	}
	if enqueuer == nil {
		if projection != nil {
			return errors.New("production projection post-process enqueuer is unavailable")
		}
		return nil
	}

	langfuse.InjectTracing(ctx, &payload)
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal knowledge post process payload: %w", err)
	}
	task := asynq.NewTask(types.TypeKnowledgePostProcess, payloadBytes,
		knowledgePostProcessTaskOptions()...)
	var enqueueOptions []asynq.Option
	if projection != nil {
		enqueueOptions = append(enqueueOptions,
			asynq.TaskID(productionProjectionAttemptTaskID(
				projection.ReleaseTargetID, payload.KnowledgeID, "post-process", payload.Attempt,
			)),
			asynq.Retention(24*time.Hour),
		)
	}
	if _, err := enqueuer.Enqueue(task, enqueueOptions...); err != nil {
		if projection != nil && errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("enqueue knowledge post process task: %w", err)
	}
	return nil
}
