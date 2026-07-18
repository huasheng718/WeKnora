package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type productionIdempotencyRepository struct {
	db *gorm.DB
}

// productionIdempotencyLease bounds how long a crashed request can block a
// retry. Reserve atomically renews an incomplete row older than this interval.
const productionIdempotencyLease = 5 * time.Minute

// NewProductionIdempotencyRepository creates a durable idempotency repository.
func NewProductionIdempotencyRepository(db *gorm.DB) interfaces.ProductionIdempotencyRepository {
	return &productionIdempotencyRepository{db: db}
}

func (r *productionIdempotencyRepository) WithinTransaction(
	ctx context.Context,
	fn func(context.Context) error,
) error {
	if fn == nil {
		return errors.New("production transaction callback is required")
	}
	return database.WithTransactionContext(ctx, r.db, fn)
}

func (r *productionIdempotencyRepository) Reserve(
	ctx context.Context,
	record *types.ProductionIdempotencyKey,
) (*types.ProductionIdempotencyKey, bool, error) {
	if record == nil {
		return nil, false, errors.New("production idempotency record is required")
	}
	result := database.DBFromContext(ctx, r.db).WithContext(ctx).
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

	// A process can die after reserving but before completing or releasing.
	// Renew the stale row with one conditional UPDATE: concurrent reclaimers
	// race on created_at, and only the first one can move the lease forward.
	// The digest must match so expiry never weakens key/body conflict semantics.
	// Rotating the primary-key ID fences the expired owner out of Complete and
	// Release, both of which require the current reservation ID.
	now := time.Now()
	reclaimed := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.ProductionIdempotencyKey{}).
		Where(
			"tenant_id = ? AND actor_user_id = ? AND route = ? AND idempotency_key = ? AND request_digest = ?",
			record.TenantID, record.ActorUserID, record.Route, record.IdempotencyKey, record.RequestDigest,
		).
		Where("completed_at IS NULL AND created_at < ?", now.Add(-productionIdempotencyLease)).
		Updates(map[string]any{
			"id":            record.ID,
			"status_code":   nil,
			"response_body": nil,
			"completed_at":  nil,
			"created_at":    now,
		})
	if reclaimed.Error != nil {
		return nil, false, reclaimed.Error
	}

	var existing types.ProductionIdempotencyKey
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Where(
			"tenant_id = ? AND actor_user_id = ? AND route = ? AND idempotency_key = ?",
			record.TenantID, record.ActorUserID, record.Route, record.IdempotencyKey,
		).
		First(&existing).Error
	if err != nil {
		return nil, false, err
	}
	return &existing, reclaimed.RowsAffected == 1, nil
}

func (r *productionIdempotencyRepository) Complete(
	ctx context.Context,
	id string,
	statusCode int,
	responseBody types.JSON,
) error {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return errors.New("production idempotency completion requires tenant context")
	}
	now := time.Now()
	result := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.ProductionIdempotencyKey{}).
		Where("tenant_id = ? AND id = ? AND completed_at IS NULL", tenantID, id).
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
		if err := database.DBFromContext(ctx, r.db).WithContext(ctx).
			Model(&types.ProductionIdempotencyKey{}).
			Where("tenant_id = ? AND id = ?", tenantID, id).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return gorm.ErrRecordNotFound
		}
	}
	return nil
}

func (r *productionIdempotencyRepository) Release(ctx context.Context, id string) error {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return errors.New("production idempotency release requires tenant context")
	}
	result := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Where("tenant_id = ? AND id = ? AND completed_at IS NULL", tenantID, id).
		Delete(&types.ProductionIdempotencyKey{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}

	var count int64
	if err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.ProductionIdempotencyKey{}).
		Where("tenant_id = ? AND id = ?", tenantID, id).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

var _ interfaces.ProductionIdempotencyRepository = (*productionIdempotencyRepository)(nil)
