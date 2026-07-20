package router

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type syncTaskTestClock struct {
	mu  sync.Mutex
	now time.Time
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
