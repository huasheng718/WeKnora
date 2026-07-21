package router

import (
	"container/heap"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/middleware/asynqdl"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"go.uber.org/dig"
)

const syncTaskIDPruneBudget = 64

type syncTaskIDExpiry struct {
	taskID    string
	expiresAt time.Time
}

type syncTaskIDExpiryHeap []syncTaskIDExpiry

func (h syncTaskIDExpiryHeap) Len() int           { return len(h) }
func (h syncTaskIDExpiryHeap) Less(i, j int) bool { return h[i].expiresAt.Before(h[j].expiresAt) }
func (h syncTaskIDExpiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *syncTaskIDExpiryHeap) Push(value interface{}) {
	*h = append(*h, value.(syncTaskIDExpiry))
}

func (h *syncTaskIDExpiryHeap) Pop() interface{} {
	old := *h
	last := len(old) - 1
	value := old[last]
	*h = old[:last]
	return value
}

// SyncTaskExecutor executes tasks synchronously (in a goroutine) without Redis.
// Used in Lite mode as a drop-in replacement for *asynq.Client.
type SyncTaskExecutor struct {
	mu              sync.RWMutex
	handlers        map[string]func(context.Context, *asynq.Task) error
	terminalFailure asynqdl.OnDeadLetter
	taskIDs         map[string]time.Time
	taskIDExpiries  syncTaskIDExpiryHeap
	now             func() time.Time
	after           func(time.Duration) <-chan time.Time
}

func NewSyncTaskExecutor() *SyncTaskExecutor {
	return newSyncTaskExecutor(time.Now, time.After)
}

func newSyncTaskExecutor(now func() time.Time, timers ...func(time.Duration) <-chan time.Time) *SyncTaskExecutor {
	if now == nil {
		now = time.Now
	}
	after := time.After
	if len(timers) != 0 && timers[0] != nil {
		after = timers[0]
	}
	return &SyncTaskExecutor{
		handlers: make(map[string]func(context.Context, *asynq.Task) error),
		taskIDs:  make(map[string]time.Time),
		now:      now,
		after:    after,
	}
}

// RegisterHandler registers a handler for a given task type pattern.
func (e *SyncTaskExecutor) RegisterHandler(pattern string, handler func(context.Context, *asynq.Task) error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers[pattern] = handler
}

// SetTerminalFailureHandler installs the shared terminal-failure callback used
// after a Lite task exhausts its retry budget.
func (e *SyncTaskExecutor) SetTerminalFailureHandler(handler asynqdl.OnDeadLetter) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.terminalFailure = handler
}

// Enqueue satisfies interfaces.TaskEnqueuer.
// Instead of queuing to Redis, it dispatches the task to a goroutine.
// Supports ProcessAt/ProcessIn scheduling and MaxRetry options for parity with asynq.
func (e *SyncTaskExecutor) Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	enqueuedAt := e.now()
	var scheduledAt time.Time
	var retention time.Duration
	var requestedTaskID string
	hasTaskID := false
	maxRetry := 25 // asynq default
	maxRetrySet := false
	for _, opt := range opts {
		switch opt.Type() {
		case asynq.ProcessAtOpt:
			if at, ok := opt.Value().(time.Time); ok {
				scheduledAt = at
			}
		case asynq.ProcessInOpt:
			if d, ok := opt.Value().(time.Duration); ok {
				scheduledAt = enqueuedAt.Add(d)
			}
		case asynq.MaxRetryOpt:
			if n, ok := opt.Value().(int); ok {
				maxRetry = n
				maxRetrySet = true
			}
		case asynq.TaskIDOpt:
			if id, ok := opt.Value().(string); ok {
				requestedTaskID = id
				hasTaskID = true
			}
		case asynq.RetentionOpt:
			if d, ok := opt.Value().(time.Duration); ok {
				retention = d
			}
		}
	}
	// Callers that explicitly pass MaxRetry(0) want no retries.
	// Without the flag we can't distinguish "not set" from "set to 0".
	if maxRetrySet && maxRetry < 0 {
		maxRetry = 0
	}

	if hasTaskID && requestedTaskID == "" {
		return nil, fmt.Errorf("sync task executor: task ID cannot be empty")
	}

	e.mu.Lock()
	handler, ok := e.handlers[task.Type()]
	if !ok {
		e.mu.Unlock()
		return nil, fmt.Errorf("sync task executor: no handler registered for type %q", task.Type())
	}
	now := enqueuedAt
	e.pruneExpiredTaskIDsLocked(now)
	if hasTaskID {
		if expiresAt, exists := e.taskIDs[requestedTaskID]; exists &&
			(expiresAt.IsZero() || now.Before(expiresAt)) {
			e.mu.Unlock()
			return nil, asynq.ErrTaskIDConflict
		}
		e.taskIDs[requestedTaskID] = time.Time{}
	}
	terminalFailure := e.terminalFailure
	e.mu.Unlock()

	taskID := requestedTaskID
	if !hasTaskID {
		taskID = uuid.New().String()
	}
	info := &asynq.TaskInfo{
		ID:            taskID,
		Queue:         "sync",
		Type:          task.Type(),
		NextProcessAt: scheduledAt,
	}
	delay := scheduledAt.Sub(enqueuedAt)

	go func() {
		if hasTaskID {
			defer e.finishTaskID(taskID, retention)
		}
		if delay > 0 {
			<-e.after(delay)
		}

		// Tag as a background worker execution so the per-model concurrency
		// governor throttles Lite-mode ingestion/enrichment LLM calls, mirroring
		// the asynq backgroundTaskMiddleware in the Redis path.
		ctx := types.WithBackgroundTask(context.Background())
		start := time.Now()
		logger.Infof(ctx, "[SyncTask] Executing task type=%s id=%s", task.Type(), taskID)

		var lastErr error
		for attempt := 0; attempt <= maxRetry; attempt++ {
			if attempt > 0 {
				backoff := time.Duration(attempt) * 5 * time.Second
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
				logger.Infof(ctx, "[SyncTask] Retrying task type=%s id=%s attempt=%d/%d backoff=%s",
					task.Type(), taskID, attempt, maxRetry, backoff)
				<-e.after(backoff)
			}

			lastErr = handler(ctx, task)
			if lastErr == nil {
				logger.Infof(ctx, "[SyncTask] Task completed type=%s id=%s elapsed=%v",
					task.Type(), taskID, time.Since(start))
				return
			}
		}

		logger.Errorf(ctx, "[SyncTask] Task failed (exhausted retries) type=%s id=%s elapsed=%v err=%v",
			task.Type(), taskID, time.Since(start), lastErr)
		if terminalFailure != nil {
			terminalFailure(ctx, task, lastErr)
		}
	}()

	return info, nil
}

func (e *SyncTaskExecutor) finishTaskID(taskID string, retention time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if retention <= 0 {
		delete(e.taskIDs, taskID)
		return
	}
	expiresAt := e.now().Add(retention)
	e.taskIDs[taskID] = expiresAt
	heap.Push(&e.taskIDExpiries, syncTaskIDExpiry{taskID: taskID, expiresAt: expiresAt})
}

func (e *SyncTaskExecutor) pruneExpiredTaskIDsLocked(now time.Time) {
	for scanned := 0; scanned < syncTaskIDPruneBudget && e.taskIDExpiries.Len() > 0; scanned++ {
		next := e.taskIDExpiries[0]
		if now.Before(next.expiresAt) {
			return
		}
		heap.Pop(&e.taskIDExpiries)
		if current, exists := e.taskIDs[next.taskID]; exists && current.Equal(next.expiresAt) {
			delete(e.taskIDs, next.taskID)
		}
	}
}

type SyncTaskParams struct {
	dig.In

	Executor             *SyncTaskExecutor
	KnowledgeService     interfaces.KnowledgeService
	KnowledgeBaseService interfaces.KnowledgeBaseService
	TagService           interfaces.KnowledgeTagService
	DataSourceService    interfaces.DataSourceService
	ChunkExtractor       interfaces.TaskHandler `name:"chunkExtractor"`
	DataTableSummary     interfaces.TaskHandler `name:"dataTableSummary"`
	ImageMultimodal      interfaces.TaskHandler `name:"imageMultimodal"`
	KnowledgePostProcess interfaces.TaskHandler `name:"knowledgePostProcess"`
	WikiIngest           interfaces.TaskHandler `name:"wikiIngest"`
	ProductionRun        interfaces.TaskHandler `name:"productionRun"`
	ProductionProjection interfaces.TaskHandler `name:"productionProjection"`
	ProductionRelease    *service.ProductionReleaseService
	SpanTracker          service.SpanTracker
}

// RegisterSyncHandlers registers all task handlers on the SyncTaskExecutor.
// Used in Lite mode instead of RunAsynqServer.
func RegisterSyncHandlers(params SyncTaskParams) {
	params.Executor.SetTerminalFailureHandler(newDeadLetterKnowledgeFailer(
		params.KnowledgeService, params.SpanTracker, params.ProductionRelease,
	))
	params.Executor.RegisterHandler(types.TypeChunkExtract, params.ChunkExtractor.Handle)
	params.Executor.RegisterHandler(types.TypeDataTableSummary, params.DataTableSummary.Handle)
	params.Executor.RegisterHandler(types.TypeDocumentProcess, params.KnowledgeService.ProcessDocument)
	params.Executor.RegisterHandler(types.TypeManualProcess, params.KnowledgeService.ProcessManualUpdate)
	params.Executor.RegisterHandler(types.TypeFAQImport, params.KnowledgeService.ProcessFAQImport)
	params.Executor.RegisterHandler(types.TypeQuestionGeneration, params.KnowledgeService.ProcessQuestionGeneration)
	params.Executor.RegisterHandler(types.TypeSummaryGeneration, params.KnowledgeService.ProcessSummaryGeneration)
	params.Executor.RegisterHandler(types.TypeKBClone, params.KnowledgeService.ProcessKBClone)
	params.Executor.RegisterHandler(types.TypeKnowledgeMove, params.KnowledgeService.ProcessKnowledgeMove)
	params.Executor.RegisterHandler(types.TypeKnowledgeListDelete, params.KnowledgeService.ProcessKnowledgeListDelete)
	params.Executor.RegisterHandler(types.TypeKnowledgeListReparse, params.KnowledgeService.ProcessKnowledgeListReparse)
	params.Executor.RegisterHandler(types.TypeIndexDelete, params.TagService.ProcessIndexDelete)
	params.Executor.RegisterHandler(types.TypeKBDelete, params.KnowledgeBaseService.ProcessKBDelete)
	params.Executor.RegisterHandler(types.TypeImageMultimodal, params.ImageMultimodal.Handle)
	params.Executor.RegisterHandler(types.TypeKnowledgePostProcess, params.KnowledgePostProcess.Handle)
	params.Executor.RegisterHandler(types.TypeDataSourceSync, params.DataSourceService.ProcessSync)
	params.Executor.RegisterHandler(types.TypeWikiIngest, params.WikiIngest.Handle)
	params.Executor.RegisterHandler(types.TypeWikiFinalize, params.WikiIngest.Handle)
	params.Executor.RegisterHandler(types.TypeProductionCollect, params.ProductionRun.Handle)
	params.Executor.RegisterHandler(types.TypeProductionWrite, params.ProductionRun.Handle)
	params.Executor.RegisterHandler(types.TypeProductionValidate, params.ProductionRun.Handle)
	registerSyncProductionProjectionHandlers(params.Executor, params.ProductionProjection)
	logger.Infof(context.Background(), "[SyncTask] All task handlers registered (Lite mode, no Redis)")
}

func registerSyncProductionProjectionHandlers(executor *SyncTaskExecutor, handler interfaces.TaskHandler) {
	executor.RegisterHandler(types.TypeProductionBuild, handler.Handle)
	executor.RegisterHandler(types.TypeProductionActivate, handler.Handle)
	executor.RegisterHandler(types.TypeProductionCleanup, handler.Handle)
}
