package repository

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

type productionUnitOfWork struct{ db *gorm.DB }

func NewProductionUnitOfWork(db *gorm.DB) interfaces.ProductionUnitOfWork {
	return &productionUnitOfWork{db: db}
}

func (u *productionUnitOfWork) WithinTransaction(
	ctx context.Context,
	fn func(context.Context) error,
) error {
	if fn == nil {
		return errors.New("production unit of work callback is required")
	}
	if database.DBFromContext(ctx, nil) != nil {
		return fn(ctx)
	}
	return database.WithTransactionContext(ctx, u.db, fn)
}

var _ interfaces.ProductionUnitOfWork = (*productionUnitOfWork)(nil)
