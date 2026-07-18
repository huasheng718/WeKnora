package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// ProductionIdempotencyRepository reserves request keys and records the
// completed response used by retries.
type ProductionIdempotencyRepository interface {
	Reserve(ctx context.Context, record *types.ProductionIdempotencyKey) (existing *types.ProductionIdempotencyKey, created bool, err error)
	Complete(ctx context.Context, id string, statusCode int, responseBody types.JSON) error
}
