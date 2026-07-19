package interfaces

import "context"

// ProductionUnitOfWork atomically commits governed mutations and their
// required audit event while reusing any transaction already bound to ctx.
type ProductionUnitOfWork interface {
	WithinTransaction(ctx context.Context, fn func(context.Context) error) error
}
