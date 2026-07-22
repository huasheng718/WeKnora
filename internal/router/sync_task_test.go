package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type syncTaskTestClock struct {
	mu  sync.Mutex
	now time.Time
}

type syncTaskTestTimer struct {
	durations chan time.Duration
	fire      chan time.Time
}

func newSyncTaskTestTimer() *syncTaskTestTimer {
	return &syncTaskTestTimer{durations: make(chan time.Duration, 1), fire: make(chan time.Time, 1)}
}

func (t *syncTaskTestTimer) After(delay time.Duration) <-chan time.Time {
	t.durations <- delay
	return t.fire
}

func (c *syncTaskTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *syncTaskTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func waitForRetainedTaskIDs(t *testing.T, executor *SyncTaskExecutor, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		executor.mu.RLock()
		defer executor.mu.RUnlock()
		if len(executor.taskIDs) != want {
			return false
		}
		for _, expiresAt := range executor.taskIDs {
			if expiresAt.IsZero() {
				return false
			}
		}
		return true
	}, time.Second, time.Millisecond)
}

func TestSyncTaskExecutorDeduplicatesConcurrentRetainedTaskID(t *testing.T) {
	const concurrentEnqueues = 32
	executor := NewSyncTaskExecutor()
	var executions atomic.Int32
	release := make(chan struct{})
	done := make(chan struct{}, concurrentEnqueues)
	executor.RegisterHandler("knowledge:post_process", func(context.Context, *asynq.Task) error {
		executions.Add(1)
		<-release
		done <- struct{}{}
		return nil
	})

	start := make(chan struct{})
	type enqueueResult struct {
		info *asynq.TaskInfo
		err  error
	}
	results := make(chan enqueueResult, concurrentEnqueues)
	var enqueues sync.WaitGroup
	for range concurrentEnqueues {
		enqueues.Add(1)
		go func() {
			defer enqueues.Done()
			<-start
			info, err := executor.Enqueue(
				asynq.NewTask("knowledge:post_process", []byte(`{"knowledge_id":"projection-1"}`)),
				asynq.TaskID("production-projection-post-process-target-1-attempt-1"),
				asynq.Retention(time.Minute),
				asynq.MaxRetry(0),
			)
			results <- enqueueResult{info: info, err: err}
		}()
	}
	close(start)
	enqueues.Wait()
	close(release)

	successes := 0
	conflicts := 0
	for range concurrentEnqueues {
		result := <-results
		if result.err == nil {
			successes++
			require.NotNil(t, result.info)
			require.Equal(t, "production-projection-post-process-target-1-attempt-1", result.info.ID)
			continue
		}
		require.Nil(t, result.info)
		require.True(t, errors.Is(result.err, asynq.ErrTaskIDConflict), "unexpected enqueue error: %v", result.err)
		conflicts++
	}
	for range successes {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("accepted Lite task did not execute")
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, concurrentEnqueues-1, conflicts)
	require.EqualValues(t, 1, executions.Load())

	info, err := executor.Enqueue(
		asynq.NewTask("knowledge:post_process", nil),
		asynq.TaskID("production-projection-post-process-target-1-attempt-1"),
		asynq.Retention(time.Minute),
	)
	require.Nil(t, info)
	require.ErrorIs(t, err, asynq.ErrTaskIDConflict)
}

func TestSyncTaskExecutorPrunesExpiredRetainedTaskIDOnUnrelatedEnqueue(t *testing.T) {
	clock := &syncTaskTestClock{now: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
	executor := newSyncTaskExecutor(clock.Now)
	executor.RegisterHandler("done", func(context.Context, *asynq.Task) error { return nil })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	executor.RegisterHandler("blocked", func(context.Context, *asynq.Task) error {
		<-release
		return nil
	})

	_, err := executor.Enqueue(asynq.NewTask("done", nil),
		asynq.TaskID("expired-id"), asynq.Retention(time.Minute), asynq.MaxRetry(0))
	require.NoError(t, err)
	waitForRetainedTaskIDs(t, executor, 1)
	clock.Advance(2 * time.Minute)

	_, err = executor.Enqueue(asynq.NewTask("blocked", nil),
		asynq.TaskID("unrelated-active-id"), asynq.Retention(time.Hour), asynq.MaxRetry(0))
	require.NoError(t, err)
	executor.mu.RLock()
	_, expiredExists := executor.taskIDs["expired-id"]
	activeExpiry, activeExists := executor.taskIDs["unrelated-active-id"]
	executor.mu.RUnlock()
	require.False(t, expiredExists)
	require.True(t, activeExists)
	require.True(t, activeExpiry.IsZero(), "an active claim must never be pruned")
}

func TestSyncTaskExecutorPruningKeepsActiveAndUnexpiredTaskIDsConflicting(t *testing.T) {
	clock := &syncTaskTestClock{now: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
	executor := newSyncTaskExecutor(clock.Now)
	executor.RegisterHandler("done", func(context.Context, *asynq.Task) error { return nil })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	executor.RegisterHandler("blocked", func(context.Context, *asynq.Task) error {
		<-release
		return nil
	})

	_, err := executor.Enqueue(asynq.NewTask("done", nil),
		asynq.TaskID("unexpired-id"), asynq.Retention(time.Hour), asynq.MaxRetry(0))
	require.NoError(t, err)
	waitForRetainedTaskIDs(t, executor, 1)
	_, err = executor.Enqueue(asynq.NewTask("blocked", nil),
		asynq.TaskID("active-id"), asynq.Retention(time.Hour), asynq.MaxRetry(0))
	require.NoError(t, err)
	clock.Advance(30 * time.Minute)

	_, err = executor.Enqueue(asynq.NewTask("done", nil), asynq.MaxRetry(0))
	require.NoError(t, err)
	for _, taskID := range []string{"unexpired-id", "active-id"} {
		info, conflictErr := executor.Enqueue(asynq.NewTask("done", nil), asynq.TaskID(taskID))
		require.Nil(t, info)
		require.ErrorIs(t, conflictErr, asynq.ErrTaskIDConflict)
	}
}

func TestSyncTaskExecutorBoundsExpiredTaskIDPruningWorkAndMapGrowth(t *testing.T) {
	const retainedTasks = syncTaskIDPruneBudget * 4
	clock := &syncTaskTestClock{now: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
	executor := newSyncTaskExecutor(clock.Now)
	executor.RegisterHandler("done", func(context.Context, *asynq.Task) error { return nil })

	for i := range retainedTasks {
		_, err := executor.Enqueue(asynq.NewTask("done", nil),
			asynq.TaskID(fmt.Sprintf("retained-%d", i)), asynq.Retention(time.Minute), asynq.MaxRetry(0))
		require.NoError(t, err)
	}
	waitForRetainedTaskIDs(t, executor, retainedTasks)
	clock.Advance(2 * time.Minute)

	_, err := executor.Enqueue(asynq.NewTask("done", nil), asynq.MaxRetry(0))
	require.NoError(t, err)
	executor.mu.RLock()
	remainingAfterOnePrune := len(executor.taskIDs)
	executor.mu.RUnlock()
	require.Equal(t, retainedTasks-syncTaskIDPruneBudget, remainingAfterOnePrune,
		"one enqueue must perform only the bounded prune budget")

	for range retainedTasks / syncTaskIDPruneBudget {
		_, err = executor.Enqueue(asynq.NewTask("done", nil), asynq.MaxRetry(0))
		require.NoError(t, err)
	}
	executor.mu.RLock()
	remaining := len(executor.taskIDs)
	executor.mu.RUnlock()
	require.Zero(t, remaining, "unrelated enqueues must eventually reclaim every expired retained ID")
}

func TestSyncTaskExecutorConcurrentEnqueueAndPrune(t *testing.T) {
	const expiredTasks = syncTaskIDPruneBudget * 2
	clock := &syncTaskTestClock{now: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
	executor := newSyncTaskExecutor(clock.Now)
	executor.RegisterHandler("done", func(context.Context, *asynq.Task) error { return nil })

	for i := range expiredTasks {
		_, err := executor.Enqueue(asynq.NewTask("done", nil),
			asynq.TaskID(fmt.Sprintf("expired-%d", i)), asynq.Retention(time.Minute), asynq.MaxRetry(0))
		require.NoError(t, err)
	}
	waitForRetainedTaskIDs(t, executor, expiredTasks)
	clock.Advance(2 * time.Minute)

	var enqueues sync.WaitGroup
	for range 8 {
		enqueues.Add(1)
		go func() {
			defer enqueues.Done()
			_, err := executor.Enqueue(asynq.NewTask("done", nil), asynq.MaxRetry(0))
			require.NoError(t, err)
		}()
	}
	enqueues.Wait()
	executor.mu.RLock()
	remaining := len(executor.taskIDs)
	executor.mu.RUnlock()
	require.Zero(t, remaining)
}

func TestSyncTaskExecutorProcessAtUsesInjectedClockAndTimer(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	clock := &syncTaskTestClock{now: now}
	timer := newSyncTaskTestTimer()
	executor := newSyncTaskExecutor(clock.Now, timer.After)
	executed := make(chan struct{}, 1)
	executor.RegisterHandler("scheduled", func(context.Context, *asynq.Task) error {
		executed <- struct{}{}
		return nil
	})

	_, err := executor.Enqueue(asynq.NewTask("scheduled", nil), asynq.ProcessAt(now.Add(90*time.Minute)), asynq.MaxRetry(0))
	require.NoError(t, err)
	require.Equal(t, 90*time.Minute, <-timer.durations)
	select {
	case <-executed:
		t.Fatal("ProcessAt task executed before its timer fired")
	default:
	}
	timer.fire <- now.Add(90 * time.Minute)
	require.Eventually(t, func() bool { return len(executed) == 1 }, time.Second, time.Millisecond)
}

func TestSyncTaskExecutorLastSchedulingOptionWins(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		opts []asynq.Option
		want time.Duration
	}{
		{name: "ProcessAt wins", opts: []asynq.Option{asynq.ProcessIn(5 * time.Minute), asynq.ProcessAt(now.Add(2 * time.Hour))}, want: 2 * time.Hour},
		{name: "ProcessIn wins", opts: []asynq.Option{asynq.ProcessAt(now.Add(2 * time.Hour)), asynq.ProcessIn(5 * time.Minute)}, want: 5 * time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clock := &syncTaskTestClock{now: now}
			timer := newSyncTaskTestTimer()
			executor := newSyncTaskExecutor(clock.Now, timer.After)
			executed := make(chan struct{}, 1)
			executor.RegisterHandler("scheduled", func(context.Context, *asynq.Task) error {
				executed <- struct{}{}
				return nil
			})

			_, err := executor.Enqueue(asynq.NewTask("scheduled", nil), append(tc.opts, asynq.MaxRetry(0))...)
			require.NoError(t, err)
			require.Equal(t, tc.want, <-timer.durations)
			timer.fire <- now.Add(tc.want)
			require.Eventually(t, func() bool { return len(executed) == 1 }, time.Second, time.Millisecond)
		})
	}
}

func TestSyncTaskExecutorRetainsOneIDPerFailureGeneration(t *testing.T) {
	executor := NewSyncTaskExecutor()
	var executions atomic.Int32
	executed := make(chan struct{}, 2)
	executor.RegisterHandler("production:build", func(context.Context, *asynq.Task) error {
		executions.Add(1)
		executed <- struct{}{}
		return nil
	})

	enqueueGeneration := func(taskID string) {
		const callers = 20
		start := make(chan struct{})
		results := make(chan error, callers)
		for range callers {
			go func() {
				<-start
				_, err := executor.Enqueue(
					asynq.NewTask("production:build", nil),
					asynq.TaskID(taskID), asynq.Retention(24*time.Hour), asynq.MaxRetry(0),
				)
				results <- err
			}()
		}
		close(start)
		successes := 0
		for range callers {
			err := <-results
			if err == nil {
				successes++
				continue
			}
			require.ErrorIs(t, err, asynq.ErrTaskIDConflict)
		}
		require.Equal(t, 1, successes)
		select {
		case <-executed:
		case <-time.After(time.Second):
			t.Fatal("accepted generation did not execute")
		}
	}

	enqueueGeneration("production-build-failed-1721548800000000000")
	waitForRetainedTaskIDs(t, executor, 1)
	enqueueGeneration("production-build-failed-1721548860000000000")
	waitForRetainedTaskIDs(t, executor, 2)
	require.EqualValues(t, 2, executions.Load())
}

func TestSyncTaskExecutorCallsTerminalFailureOnceBeforeRetainingFailedTaskID(t *testing.T) {
	clock := &syncTaskTestClock{now: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
	executor := newSyncTaskExecutor(clock.Now, func(time.Duration) <-chan time.Time {
		ready := make(chan time.Time, 1)
		ready <- clock.Now()
		return ready
	})
	var attempts atomic.Int32
	expectedErr := errors.New("permanent parse failure")
	executor.RegisterHandler(types.TypeDocumentProcess, func(context.Context, *asynq.Task) error {
		attempts.Add(1)
		return expectedErr
	})

	type callbackResult struct {
		taskType string
		err      error
	}
	callbackStarted := make(chan callbackResult, 1)
	releaseCallback := make(chan struct{})
	var callbacks atomic.Int32
	executor.SetTerminalFailureHandler(func(_ context.Context, task *asynq.Task, taskErr error) {
		callbacks.Add(1)
		callbackStarted <- callbackResult{taskType: task.Type(), err: taskErr}
		<-releaseCallback
	})

	const taskID = "failed-document-generation-1"
	_, err := executor.Enqueue(
		asynq.NewTask(types.TypeDocumentProcess, []byte(`{"knowledge_id":"knowledge-1"}`)),
		asynq.TaskID(taskID), asynq.Retention(time.Minute), asynq.MaxRetry(2),
	)
	require.NoError(t, err)
	var result callbackResult
	select {
	case result = <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("terminal failure callback did not run")
	}
	require.Equal(t, types.TypeDocumentProcess, result.taskType)
	require.ErrorIs(t, result.err, expectedErr)

	executor.mu.RLock()
	activeExpiry, exists := executor.taskIDs[taskID]
	executor.mu.RUnlock()
	require.True(t, exists)
	require.True(t, activeExpiry.IsZero(), "task ID retention must begin only after terminal failure handling finishes")
	require.EqualValues(t, 3, attempts.Load())
	require.EqualValues(t, 1, callbacks.Load())

	close(releaseCallback)
	waitForRetainedTaskIDs(t, executor, 1)
	info, err := executor.Enqueue(
		asynq.NewTask(types.TypeDocumentProcess, nil),
		asynq.TaskID(taskID), asynq.Retention(time.Minute), asynq.MaxRetry(0),
	)
	require.Nil(t, info)
	require.ErrorIs(t, err, asynq.ErrTaskIDConflict)
	require.EqualValues(t, 1, callbacks.Load())
}

func TestSyncTaskExecutorHonorsAsynqSkipRetry(t *testing.T) {
	clock := &syncTaskTestClock{now: time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)}
	executor := newSyncTaskExecutor(clock.Now, func(time.Duration) <-chan time.Time {
		ready := make(chan time.Time, 1)
		ready <- clock.Now()
		return ready
	})
	var attempts atomic.Int32
	expectedErr := errors.New("permanent head mutation rejection")
	executor.RegisterHandler(types.TypeProductionActivate, func(context.Context, *asynq.Task) error {
		attempts.Add(1)
		return errors.Join(expectedErr, asynq.SkipRetry)
	})
	terminal := make(chan error, 1)
	executor.SetTerminalFailureHandler(func(_ context.Context, _ *asynq.Task, taskErr error) {
		terminal <- taskErr
	})

	_, err := executor.Enqueue(
		asynq.NewTask(types.TypeProductionActivate, nil),
		asynq.TaskID("permanent-head-mutation"), asynq.Retention(time.Minute), asynq.MaxRetry(8),
	)
	require.NoError(t, err)
	select {
	case taskErr := <-terminal:
		require.ErrorIs(t, taskErr, expectedErr)
		require.ErrorIs(t, taskErr, asynq.SkipRetry)
	case <-time.After(time.Second):
		t.Fatal("terminal failure callback did not run")
	}
	require.EqualValues(t, 1, attempts.Load())
}

func TestSyncTaskExecutorUsesProjectionFailureCallbackForKnowledgeTerminalFailures(t *testing.T) {
	for _, taskType := range []string{
		types.TypeDocumentProcess,
		types.TypeManualProcess,
		types.TypeKnowledgePostProcess,
	} {
		t.Run(taskType, func(t *testing.T) {
			executor := NewSyncTaskExecutor()
			repo := &projectionDeadLetterKnowledgeRepoStub{}
			recorder := &projectionDeadLetterFailureRecorderStub{projection: true}
			callback := newDeadLetterKnowledgeFailer(
				projectionDeadLetterKnowledgeServiceStub{repo: repo}, nil, recorder,
			)
			callbackDone := make(chan struct{})
			executor.SetTerminalFailureHandler(func(ctx context.Context, task *asynq.Task, taskErr error) {
				callback(ctx, task, taskErr)
				close(callbackDone)
			})
			executor.RegisterHandler(taskType, func(context.Context, *asynq.Task) error {
				return errors.New("provider token=super-secret host=10.0.0.7")
			})
			payload, err := json.Marshal(map[string]any{"knowledge_id": "knowledge-1", "tenant_id": 7})
			require.NoError(t, err)

			_, err = executor.Enqueue(asynq.NewTask(taskType, payload), asynq.MaxRetry(0))
			require.NoError(t, err)
			select {
			case <-callbackDone:
			case <-time.After(time.Second):
				t.Fatal("projection terminal failure callback did not finish")
			}

			require.Equal(t, "knowledge-1", recorder.knowledgeID)
			require.Equal(t, taskType, recorder.taskType)
			require.Equal(t, types.ParseStatusFailed, repo.values["parse_status"])
			require.Equal(t, types.ProductionProjectionFailureReasonBuildFailed, repo.values["error_message"])
			require.NotContains(t, repo.values["error_message"], "super-secret")
		})
	}
}
