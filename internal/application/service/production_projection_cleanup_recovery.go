package service

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	productionCleanupRecoveryBatchSize = 100
	productionCleanupRecoveryMaxBatch  = 200
	productionCleanupRecoveryInterval  = 5 * time.Minute
)

type productionCleanupRecoverySweeper interface {
	CleanupExpired(context.Context, int) error
}

type productionCleanupRecoveryTenantLister interface {
	ListTenants(context.Context) ([]*types.Tenant, error)
}

type productionCleanupRecoveryTicker interface {
	C() <-chan time.Time
	Stop()
}

type productionCleanupRecoveryTimeTicker struct {
	ticker *time.Ticker
}

func (t *productionCleanupRecoveryTimeTicker) C() <-chan time.Time { return t.ticker.C }
func (t *productionCleanupRecoveryTimeTicker) Stop()               { t.ticker.Stop() }

// ProductionCleanupRecoveryRunner recovers expired retained projections in
// Lite mode, where there is no durable Redis scheduler to wake cleanup tasks.
type ProductionCleanupRecoveryRunner struct {
	sweeper       productionCleanupRecoverySweeper
	tenants       productionCleanupRecoveryTenantLister
	limit         int
	interval      time.Duration
	tickerFactory func(time.Duration) productionCleanupRecoveryTicker

	startOnce sync.Once
	stopOnce  sync.Once
	started   atomic.Bool
	cancel    context.CancelFunc
	doneCh    chan struct{}
}

// NewProductionCleanupRecoveryRunner constructs the Lite recovery runner with
// production cadence and batch bounds. Container wiring decides whether to
// start it; Redis mode only constructs the dormant dependency.
func NewProductionCleanupRecoveryRunner(
	cleanup *ProductionProjectionCleanup,
	tenants interfaces.TenantRepository,
) *ProductionCleanupRecoveryRunner {
	return newProductionCleanupRecoveryRunner(
		cleanup,
		tenants,
		productionCleanupRecoveryBatchSize,
		productionCleanupRecoveryInterval,
		func(interval time.Duration) productionCleanupRecoveryTicker {
			return &productionCleanupRecoveryTimeTicker{ticker: time.NewTicker(interval)}
		},
	)
}

func newProductionCleanupRecoveryRunner(
	sweeper productionCleanupRecoverySweeper,
	tenants productionCleanupRecoveryTenantLister,
	limit int,
	interval time.Duration,
	tickerFactory func(time.Duration) productionCleanupRecoveryTicker,
) *ProductionCleanupRecoveryRunner {
	if limit <= 0 || limit > productionCleanupRecoveryMaxBatch {
		limit = productionCleanupRecoveryBatchSize
	}
	if interval <= 0 {
		interval = productionCleanupRecoveryInterval
	}
	if tickerFactory == nil {
		tickerFactory = func(interval time.Duration) productionCleanupRecoveryTicker {
			return &productionCleanupRecoveryTimeTicker{ticker: time.NewTicker(interval)}
		}
	}
	return &ProductionCleanupRecoveryRunner{
		sweeper: sweeper, tenants: tenants, limit: limit, interval: interval,
		tickerFactory: tickerFactory, doneCh: make(chan struct{}),
	}
}

// Start performs one startup sweep and then continues periodically. Repeated
// calls are idempotent so only one process-local recovery owner can run.
func (r *ProductionCleanupRecoveryRunner) Start(ctx context.Context) {
	if r == nil || r.sweeper == nil || r.tenants == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		r.cancel = cancel
		r.started.Store(true)
		go r.loop(runCtx)
	})
}

// Stop cancels an active sweep and waits for the sole runner goroutine.
func (r *ProductionCleanupRecoveryRunner) Stop() {
	if r == nil || !r.started.Load() {
		return
	}
	r.stopOnce.Do(func() { r.cancel() })
	<-r.doneCh
}

func (r *ProductionCleanupRecoveryRunner) loop(ctx context.Context) {
	defer close(r.doneCh)
	ticker := r.tickerFactory(r.interval)
	defer ticker.Stop()

	select {
	case <-ctx.Done():
		return
	default:
	}
	r.runOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			r.runOnce(ctx)
		}
	}
}

func (r *ProductionCleanupRecoveryRunner) runOnce(ctx context.Context) {
	tenants, err := r.tenants.ListTenants(ctx)
	if err != nil {
		if ctx.Err() == nil {
			logger.Warnf(ctx, "[production-cleanup-recovery] list tenants failed: %v", err)
		}
		return
	}
	for _, tenant := range tenants {
		if tenant == nil || tenant.ID == 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		tenantCtx := context.WithValue(ctx, types.TenantIDContextKey, tenant.ID)
		tenantCtx = context.WithValue(tenantCtx, types.UserIDContextKey, types.ProductionSystemActorID)
		if err := r.sweeper.CleanupExpired(tenantCtx, r.limit); err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Warnf(tenantCtx,
				"[production-cleanup-recovery] tenant sweep failed: tenant_id=%d err=%v",
				tenant.ID, err)
		}
	}
}
