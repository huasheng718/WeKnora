package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// ProductionIdempotencyRepository reserves request keys and records the
// completed response used by retries.
type ProductionIdempotencyRepository interface {
	WithinTransaction(ctx context.Context, fn func(txCtx context.Context) error) error
	Reserve(ctx context.Context, record *types.ProductionIdempotencyKey) (existing *types.ProductionIdempotencyKey, created bool, err error)
	Complete(ctx context.Context, id string, statusCode int, responseBody types.JSON) error
	// Release removes an incomplete reservation after a terminal request path.
	// Completed responses are never removed.
	Release(ctx context.Context, id string) error
}
