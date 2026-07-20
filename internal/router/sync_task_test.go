package router

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

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
