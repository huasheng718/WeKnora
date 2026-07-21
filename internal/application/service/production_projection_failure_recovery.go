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
	productionFailureRecoveryBatchSize = 100
	productionFailureRecoveryMaxBatch  = 200
	productionFailureRecoveryInterval  = 5 * time.Minute
)

type productionFailureRecoverySweeper interface {
	ReconcileFailedProjectionKnowledge(context.Context, int) error
}

type productionFailureRecoveryTenantLister interface {
	ListTenants(context.Context) ([]*types.Tenant, error)
}

type productionFailureRecoveryTicker interface {
	C() <-chan time.Time
	Stop()
}

type productionFailureRecoveryTimeTicker struct {
	ticker *time.Ticker
}

func (t *productionFailureRecoveryTimeTicker) C() <-chan time.Time { return t.ticker.C }
func (t *productionFailureRecoveryTimeTicker) Stop()               { t.ticker.Stop() }

// ProductionFailureRecoveryRunner reconciles persisted failed Knowledge rows
// with release targets in both Redis and Lite deployments.
type ProductionFailureRecoveryRunner struct {
	sweeper       productionFailureRecoverySweeper
	tenants       productionFailureRecoveryTenantLister
	limit         int
	interval      time.Duration
	tickerFactory func(time.Duration) productionFailureRecoveryTicker

	startOnce sync.Once
	stopOnce  sync.Once
	started   atomic.Bool
	cancel    context.CancelFunc
	doneCh    chan struct{}
}

func NewProductionFailureRecoveryRunner(
	releases *ProductionReleaseService,
	tenants interfaces.TenantRepository,
) *ProductionFailureRecoveryRunner {
	return newProductionFailureRecoveryRunner(
		releases,
		tenants,
		productionFailureRecoveryBatchSize,
		productionFailureRecoveryInterval,
		func(interval time.Duration) productionFailureRecoveryTicker {
			return &productionFailureRecoveryTimeTicker{ticker: time.NewTicker(interval)}
		},
	)
}

func newProductionFailureRecoveryRunner(
	sweeper productionFailureRecoverySweeper,
	tenants productionFailureRecoveryTenantLister,
	limit int,
	interval time.Duration,
	tickerFactory func(time.Duration) productionFailureRecoveryTicker,
) *ProductionFailureRecoveryRunner {
	if limit <= 0 || limit > productionFailureRecoveryMaxBatch {
		limit = productionFailureRecoveryBatchSize
	}
	if interval <= 0 {
		interval = productionFailureRecoveryInterval
	}
	if tickerFactory == nil {
		tickerFactory = func(interval time.Duration) productionFailureRecoveryTicker {
			return &productionFailureRecoveryTimeTicker{ticker: time.NewTicker(interval)}
		}
	}
	return &ProductionFailureRecoveryRunner{
		sweeper: sweeper, tenants: tenants, limit: limit, interval: interval,
		tickerFactory: tickerFactory, doneCh: make(chan struct{}),
	}
}

func (r *ProductionFailureRecoveryRunner) Start(ctx context.Context) {
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

func (r *ProductionFailureRecoveryRunner) Stop() {
	if r == nil || !r.started.Load() {
		return
	}
	r.stopOnce.Do(func() { r.cancel() })
	<-r.doneCh
}

func (r *ProductionFailureRecoveryRunner) loop(ctx context.Context) {
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

func (r *ProductionFailureRecoveryRunner) runOnce(ctx context.Context) {
	tenants, err := r.tenants.ListTenants(ctx)
	if err != nil {
		if ctx.Err() == nil {
			logger.Warnf(ctx, "[production-failure-recovery] list tenants failed: %v", err)
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
		if err := r.sweeper.ReconcileFailedProjectionKnowledge(tenantCtx, r.limit); err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Warnf(tenantCtx,
				"[production-failure-recovery] tenant sweep failed: tenant_id=%d err=%v",
				tenant.ID, err)
		}
	}
}
