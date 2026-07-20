package types

import (
	"context"
	"errors"
	"sync"
)

type productionAfterCommitKey struct{}

type productionAfterCommitRegistry struct {
	mu        sync.Mutex
	callbacks []func(context.Context) error
}

// WithProductionAfterCommit installs a request-scoped post-transaction queue.
// Application services register wakeups without importing HTTP middleware.
func WithProductionAfterCommit(ctx context.Context) (context.Context, func(context.Context) error) {
	registry := &productionAfterCommitRegistry{}
	registered := context.WithValue(ctx, productionAfterCommitKey{}, registry)
	return registered, func(runCtx context.Context) error {
		registry.mu.Lock()
		callbacks := append([]func(context.Context) error(nil), registry.callbacks...)
		registry.callbacks = nil
		registry.mu.Unlock()
		var joined error
		for _, callback := range callbacks {
			if callback != nil {
				joined = errors.Join(joined, callback(runCtx))
			}
		}
		return joined
	}
}

// RegisterProductionAfterCommit returns false outside a transaction-owning
// request boundary; callers then execute their wakeup directly.
func RegisterProductionAfterCommit(ctx context.Context, callback func(context.Context) error) bool {
	if ctx == nil || callback == nil {
		return false
	}
	registry, ok := ctx.Value(productionAfterCommitKey{}).(*productionAfterCommitRegistry)
	if !ok || registry == nil {
		return false
	}
	registry.mu.Lock()
	registry.callbacks = append(registry.callbacks, callback)
	registry.mu.Unlock()
	return true
}
