package repository

import (
	"context"

	"gorm.io/gorm"
)

type productionTransactionContextKey struct{}

func productionDB(ctx context.Context, fallback *gorm.DB) *gorm.DB {
	if ctx != nil {
		if tx, ok := ctx.Value(productionTransactionContextKey{}).(*gorm.DB); ok && tx != nil {
			return tx
		}
	}
	return fallback
}

func withinProductionTransaction(
	ctx context.Context,
	db *gorm.DB,
	fn func(context.Context) error,
) error {
	return productionDB(ctx, db).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, productionTransactionContextKey{}, tx))
	})
}
