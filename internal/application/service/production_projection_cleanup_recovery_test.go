package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type productionCleanupRecoveryTenantStub struct {
	interfaces.TenantRepository
	mu      sync.Mutex
	tenants []*types.Tenant
	calls   int
	err     error
}

func (s *productionCleanupRecoveryTenantStub) ListTenants(context.Context) ([]*types.Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.tenants, s.err
}

type productionCleanupRecoverySweepCall struct {
	tenantID uint64
	actorID  string
	limit    int
}

type productionCleanupRecoverySweeperStub struct {
	mu    sync.Mutex
	calls []productionCleanupRecoverySweepCall
	err   error
}

func (s *productionCleanupRecoverySweeperStub) CleanupExpired(ctx context.Context, limit int) error {
	tenantID, _ := types.TenantIDFromContext(ctx)
	actorID, _ := types.UserIDFromContext(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, productionCleanupRecoverySweepCall{tenantID: tenantID, actorID: actorID, limit: limit})
	return s.err
}

func (s *productionCleanupRecoverySweeperStub) snapshotCalls() []productionCleanupRecoverySweepCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]productionCleanupRecoverySweepCall(nil), s.calls...)
}

type productionCleanupRecoveryTickerStub struct {
	ch       chan time.Time
	stopOnce sync.Once
	stopped  chan struct{}
}

func newProductionCleanupRecoveryTickerStub() *productionCleanupRecoveryTickerStub {
	return &productionCleanupRecoveryTickerStub{ch: make(chan time.Time, 1), stopped: make(chan struct{})}
}

func (t *productionCleanupRecoveryTickerStub) C() <-chan time.Time { return t.ch }
func (t *productionCleanupRecoveryTickerStub) Stop() {
	t.stopOnce.Do(func() { close(t.stopped) })
}

func TestProductionCleanupRecoveryRunsStartupAndPeriodicBoundedSweeps(t *testing.T) {
	tenants := &productionCleanupRecoveryTenantStub{tenants: []*types.Tenant{{ID: 7}, {ID: 8}}}
	sweeper := &productionCleanupRecoverySweeperStub{}
	ticker := newProductionCleanupRecoveryTickerStub()
	runner := newProductionCleanupRecoveryRunner(
		sweeper, tenants, 100, time.Hour,
		func(time.Duration) productionCleanupRecoveryTicker { return ticker },
	)
	runner.Start(context.Background())
	runner.Start(context.Background())
	t.Cleanup(runner.Stop)

	require.Eventually(t, func() bool { return len(sweeper.snapshotCalls()) == 2 }, time.Second, time.Millisecond)
	require.Equal(t, []productionCleanupRecoverySweepCall{
		{tenantID: 7, actorID: types.ProductionSystemActorID, limit: 100},
		{tenantID: 8, actorID: types.ProductionSystemActorID, limit: 100},
	}, sweeper.snapshotCalls())

	ticker.ch <- time.Now()
	require.Eventually(t, func() bool { return len(sweeper.snapshotCalls()) == 4 }, time.Second, time.Millisecond)
	require.Equal(t, 2, tenants.calls)
}

func TestProductionCleanupRecoveryNormalizesLimitsAndContinuesAcrossTenantErrors(t *testing.T) {
	for _, limit := range []int{0, 201} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			tenants := &productionCleanupRecoveryTenantStub{tenants: []*types.Tenant{{ID: 7}, {ID: 8}}}
			sweeper := &productionCleanupRecoverySweeperStub{err: errors.New("cleanup unavailable")}
			ticker := newProductionCleanupRecoveryTickerStub()
			runner := newProductionCleanupRecoveryRunner(
				sweeper, tenants, limit, time.Hour,
				func(time.Duration) productionCleanupRecoveryTicker { return ticker },
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

func TestProductionCleanupRecoveryCancellationStopsTickerAndGoroutine(t *testing.T) {
	tenants := &productionCleanupRecoveryTenantStub{tenants: []*types.Tenant{{ID: 7}}}
	sweeper := &productionCleanupRecoverySweeperStub{}
	ticker := newProductionCleanupRecoveryTickerStub()
	runner := newProductionCleanupRecoveryRunner(
		sweeper, tenants, 100, time.Hour,
		func(time.Duration) productionCleanupRecoveryTicker { return ticker },
	)
	ctx, cancel := context.WithCancel(context.Background())
	runner.Start(ctx)
	require.Eventually(t, func() bool { return len(sweeper.snapshotCalls()) == 1 }, time.Second, time.Millisecond)

	cancel()
	stopped := make(chan struct{})
	go func() {
		runner.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("cleanup recovery runner did not stop after cancellation")
	}
	select {
	case <-ticker.stopped:
	case <-time.After(time.Second):
		t.Fatal("cleanup recovery ticker was not stopped")
	}
}

func TestProductionCleanupRecoveryNeverCleansActiveHead(t *testing.T) {
	_, repo, _ := newProductionReleaseServiceFixture(t)
	chunks := &productionCleanupChunksStub{chunks: []*types.Chunk{{ID: "chunk-active", KnowledgeID: "knowledge-old", IsEnabled: true}}}
	indexes := &productionCleanupIndexStub{}
	cleanup := &ProductionProjectionCleanup{
		releases: repo, chunks: chunks, indexes: indexes,
		uow: productionReleaseUOWStub{repo: repo}, audit: &productionReleaseAuditStub{}, now: time.Now,
	}
	tenants := &productionCleanupRecoveryTenantStub{tenants: []*types.Tenant{{ID: 7}}}
	ticker := newProductionCleanupRecoveryTickerStub()
	runner := newProductionCleanupRecoveryRunner(
		cleanup, tenants, 100, time.Hour,
		func(time.Duration) productionCleanupRecoveryTicker { return ticker },
	)
	runner.Start(context.Background())
	t.Cleanup(runner.Stop)

	require.Eventually(t, func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return repo.cleanupListCalls == 1
	}, time.Second, time.Millisecond)
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-old"].Status)
	require.Zero(t, indexes.calls)
	require.True(t, chunks.chunks[0].IsEnabled)
}
