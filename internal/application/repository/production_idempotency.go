package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type productionIdempotencyRepository struct {
	db *gorm.DB
}

// NewProductionIdempotencyRepository creates a durable idempotency repository.
func NewProductionIdempotencyRepository(db *gorm.DB) interfaces.ProductionIdempotencyRepository {
	return &productionIdempotencyRepository{db: db}
}

func (r *productionIdempotencyRepository) Reserve(
	ctx context.Context,
	record *types.ProductionIdempotencyKey,
) (*types.ProductionIdempotencyKey, bool, error) {
	if record == nil {
		return nil, false, errors.New("production idempotency record is required")
	}
	result := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "tenant_id"},
				{Name: "actor_user_id"},
				{Name: "route"},
				{Name: "idempotency_key"},
			},
			DoNothing: true,
		}).
		Create(record)
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 1 {
		return record, true, nil
	}

	var existing types.ProductionIdempotencyKey
	err := r.db.WithContext(ctx).
		Where(
			"tenant_id = ? AND actor_user_id = ? AND route = ? AND idempotency_key = ?",
			record.TenantID, record.ActorUserID, record.Route, record.IdempotencyKey,
		).
		First(&existing).Error
	if err != nil {
		return nil, false, err
	}
	return &existing, false, nil
}

func (r *productionIdempotencyRepository) Complete(
	ctx context.Context,
	id string,
	statusCode int,
	responseBody types.JSON,
) error {
	now := time.Now()
	result := r.db.WithContext(ctx).
		Model(&types.ProductionIdempotencyKey{}).
		Where("id = ? AND completed_at IS NULL", id).
		Updates(map[string]any{
			"status_code":   statusCode,
			"response_body": responseBody,
			"completed_at":  now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := r.db.WithContext(ctx).
			Model(&types.ProductionIdempotencyKey{}).
			Where("id = ?", id).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return gorm.ErrRecordNotFound
		}
	}
	return nil
}

var _ interfaces.ProductionIdempotencyRepository = (*productionIdempotencyRepository)(nil)
