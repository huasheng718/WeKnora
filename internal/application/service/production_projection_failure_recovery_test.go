package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type productionFailureRecoverySweepCall struct {
	tenantID uint64
	actorID  string
	limit    int
}

type productionFailureRecoverySweeperStub struct {
	mu    sync.Mutex
	calls []productionFailureRecoverySweepCall
	err   error
}

func (s *productionFailureRecoverySweeperStub) ReconcileFailedProjectionKnowledge(ctx context.Context, limit int) error {
	tenantID, _ := types.TenantIDFromContext(ctx)
	actorID, _ := types.UserIDFromContext(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, productionFailureRecoverySweepCall{tenantID: tenantID, actorID: actorID, limit: limit})
	return s.err
}

func (s *productionFailureRecoverySweeperStub) snapshotCalls() []productionFailureRecoverySweepCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]productionFailureRecoverySweepCall(nil), s.calls...)
}

func TestProductionFailureRecoveryRunsStartupAndPeriodicBoundedSweeps(t *testing.T) {
	tenants := &productionCleanupRecoveryTenantStub{tenants: []*types.Tenant{{ID: 7}, {ID: 8}}}
	sweeper := &productionFailureRecoverySweeperStub{}
	ticker := newProductionCleanupRecoveryTickerStub()
	runner := newProductionFailureRecoveryRunner(
		sweeper, tenants, 100, time.Hour,
		func(time.Duration) productionFailureRecoveryTicker { return ticker },
	)
	runner.Start(context.Background())
	runner.Start(context.Background())
	t.Cleanup(runner.Stop)

	require.Eventually(t, func() bool { return len(sweeper.snapshotCalls()) == 2 }, time.Second, time.Millisecond)
	require.Equal(t, []productionFailureRecoverySweepCall{
		{tenantID: 7, actorID: types.ProductionSystemActorID, limit: 100},
		{tenantID: 8, actorID: types.ProductionSystemActorID, limit: 100},
	}, sweeper.snapshotCalls())
	ticker.ch <- time.Now()
	require.Eventually(t, func() bool { return len(sweeper.snapshotCalls()) == 4 }, time.Second, time.Millisecond)
	require.Equal(t, 2, tenants.calls)
}

func TestProductionFailureRecoveryNormalizesLimitsAndContinuesAcrossTenantErrors(t *testing.T) {
	for _, limit := range []int{0, 201} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			tenants := &productionCleanupRecoveryTenantStub{tenants: []*types.Tenant{{ID: 7}, {ID: 8}}}
			sweeper := &productionFailureRecoverySweeperStub{err: errors.New("audit unavailable")}
			ticker := newProductionCleanupRecoveryTickerStub()
			runner := newProductionFailureRecoveryRunner(
				sweeper, tenants, limit, time.Hour,
				func(time.Duration) productionFailureRecoveryTicker { return ticker },
			)
			runner.Start(context.Background())
			t.Cleanup(runner.Stop)
			require.Eventually(t, func() bool { return len(sweeper.snapshotCalls()) == 2 }, time.Second, time.Millisecond)
			for _, call := range sweeper.snapshotCalls() {
				require.Equal(t, 100, call.limit)
			}
		})
	}
}

func TestProductionFailureRecoveryCancellationStopsTickerAndGoroutine(t *testing.T) {
	tenants := &productionCleanupRecoveryTenantStub{tenants: []*types.Tenant{{ID: 7}}}
	sweeper := &productionFailureRecoverySweeperStub{}
	ticker := newProductionCleanupRecoveryTickerStub()
	runner := newProductionFailureRecoveryRunner(
		sweeper, tenants, 100, time.Hour,
		func(time.Duration) productionFailureRecoveryTicker { return ticker },
	)
	ctx, cancel := context.WithCancel(context.Background())
	runner.Start(ctx)
	require.Eventually(t, func() bool { return len(sweeper.snapshotCalls()) == 1 }, time.Second, time.Millisecond)
	cancel()
	done := make(chan struct{})
	go func() {
		runner.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("failure recovery runner did not stop after cancellation")
	}
	select {
	case <-ticker.stopped:
	case <-time.After(time.Second):
		t.Fatal("failure recovery ticker was not stopped")
	}
}

func TestProductionFailureRecoveryConcurrentRunnersKeepEverySweepBounded(t *testing.T) {
	tenants := &productionCleanupRecoveryTenantStub{tenants: []*types.Tenant{{ID: 7}, {ID: 8}}}
	sweeper := &productionFailureRecoverySweeperStub{}
	firstTicker := newProductionCleanupRecoveryTickerStub()
	secondTicker := newProductionCleanupRecoveryTickerStub()
	first := newProductionFailureRecoveryRunner(
		sweeper, tenants, 100, time.Hour,
		func(time.Duration) productionFailureRecoveryTicker { return firstTicker },
	)
	second := newProductionFailureRecoveryRunner(
		sweeper, tenants, 100, time.Hour,
		func(time.Duration) productionFailureRecoveryTicker { return secondTicker },
	)
	first.Start(context.Background())
	second.Start(context.Background())
	t.Cleanup(first.Stop)
	t.Cleanup(second.Stop)

	require.Eventually(t, func() bool { return len(sweeper.snapshotCalls()) == 4 }, time.Second, time.Millisecond)
	for _, call := range sweeper.snapshotCalls() {
		require.Equal(t, 100, call.limit)
		require.Contains(t, []uint64{7, 8}, call.tenantID)
	}
}
