package database

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

type transactionContextKey struct{}

// DBFromContext returns the transaction bound to ctx, or fallback when the
// caller is not executing inside a shared database transaction.
func DBFromContext(ctx context.Context, fallback *gorm.DB) *gorm.DB {
	if ctx != nil {
		if tx, ok := ctx.Value(transactionContextKey{}).(*gorm.DB); ok && tx != nil {
			return tx
		}
	}
	return fallback
}

// WithTransactionContext starts a transaction (or nested savepoint) and
// supplies a context that repository calls can use to join it.
func WithTransactionContext(
	ctx context.Context,
	db *gorm.DB,
	fn func(context.Context) error,
) error {
	if fn == nil {
		return errors.New("transaction callback is required")
	}
	return DBFromContext(ctx, db).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, transactionContextKey{}, tx))
	})
}

// WithSavepointIfTransaction isolates a best-effort write from an existing
// transaction. Outside a context transaction it executes directly.
func WithSavepointIfTransaction(
	ctx context.Context,
	db *gorm.DB,
	fn func(*gorm.DB) error,
) error {
	if fn == nil {
		return errors.New("savepoint callback is required")
	}
	bound := DBFromContext(ctx, nil)
	if bound == nil {
		return fn(db.WithContext(ctx))
	}
	return bound.WithContext(ctx).Transaction(func(savepoint *gorm.DB) error {
		return fn(savepoint)
	})
}
