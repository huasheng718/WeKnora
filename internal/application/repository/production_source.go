package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

type productionSourceRepository struct{ db *gorm.DB }

func NewProductionSourceRepository(db *gorm.DB) interfaces.ProductionSourceRepository {
	return &productionSourceRepository{db: db}
}

func translateProductionSourceError(err error) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "frozen source set"),
		strings.Contains(lower, "source set is frozen"),
		strings.Contains(lower, "immutable when their source set is frozen"):
		return errors.Join(types.ErrProductionSourceSetFrozen, err)
	case strings.Contains(lower, "evidence snapshots are immutable"):
		return errors.Join(types.ErrProductionEvidenceImmutable, err)
	default:
		return translateProductionWriteError(err)
	}
}

func (r *productionSourceRepository) CreateSet(ctx context.Context, sourceSet *types.ProductionSourceSet) error {
	if sourceSet == nil {
		return errors.New("production source set is required")
	}
	return translateProductionSourceError(
		database.DBFromContext(ctx, r.db).WithContext(ctx).Create(sourceSet).Error,
	)
}

func (r *productionSourceRepository) GetSet(ctx context.Context, tenantID uint64, sourceSetID string) (*types.ProductionSourceSet, error) {
	var sourceSet types.ProductionSourceSet
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, sourceSetID).
		First(&sourceSet).Error
	if err != nil {
		return nil, err
	}
	return &sourceSet, nil
}

func lockProductionSourceSet(db *gorm.DB, tenantID uint64, sourceSetID string) (*types.ProductionSourceSet, error) {
	result := db.Model(&types.ProductionSourceSet{}).
		Where("tenant_id = ? AND id = ?", tenantID, sourceSetID).
		UpdateColumn("status", gorm.Expr("status"))
	if result.Error != nil {
		return nil, translateProductionSourceError(result.Error)
	}
	if result.RowsAffected != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	var sourceSet types.ProductionSourceSet
	if err := db.Where("tenant_id = ? AND id = ?", tenantID, sourceSetID).First(&sourceSet).Error; err != nil {
		return nil, err
	}
	return &sourceSet, nil
}

func (r *productionSourceRepository) CreateItem(
	ctx context.Context,
	tenantID uint64,
	sourceSetID string,
	item *types.ProductionSourceItem,
) error {
	if item == nil {
		return errors.New("production source item is required")
	}
	return database.WithTransactionContext(ctx, r.db, func(txCtx context.Context) error {
		db := database.DBFromContext(txCtx, r.db).WithContext(txCtx)
		sourceSet, err := lockProductionSourceSet(db, tenantID, sourceSetID)
		if err != nil {
			return err
		}
		if sourceSet.Status == types.ProductionSourceSetFrozen {
			return types.ErrProductionSourceSetFrozen
		}
		item.SourceSetID = sourceSetID
		return translateProductionSourceError(db.Create(item).Error)
	})
}

func (r *productionSourceRepository) GetItem(
	ctx context.Context,
	tenantID uint64,
	itemID string,
) (*types.ProductionSourceItem, *types.ProductionSourceSet, error) {
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	var item types.ProductionSourceItem
	err := db.Table("production_source_items AS item").
		Select("item.*").
		Joins("JOIN production_source_sets AS source_set ON source_set.id = item.source_set_id").
		Where("source_set.tenant_id = ? AND item.id = ?", tenantID, itemID).
		First(&item).Error
	if err != nil {
		return nil, nil, err
	}
	var sourceSet types.ProductionSourceSet
	if err := db.Where("tenant_id = ? AND id = ?", tenantID, item.SourceSetID).First(&sourceSet).Error; err != nil {
		return nil, nil, err
	}
	return &item, &sourceSet, nil
}

func sourceSetIDForItem(db *gorm.DB, tenantID uint64, itemID string) (string, error) {
	var sourceSetID string
	err := db.Table("production_source_items AS item").
		Select("item.source_set_id").
		Joins("JOIN production_source_sets AS source_set ON source_set.id = item.source_set_id").
		Where("source_set.tenant_id = ? AND item.id = ?", tenantID, itemID).
		Scan(&sourceSetID).Error
	if err != nil {
		return "", err
	}
	if sourceSetID == "" {
		return "", gorm.ErrRecordNotFound
	}
	return sourceSetID, nil
}

func (r *productionSourceRepository) DecideItem(
	ctx context.Context,
	tenantID uint64,
	itemID string,
	decision types.ProductionSourceItemStatus,
) error {
	return database.WithTransactionContext(ctx, r.db, func(txCtx context.Context) error {
		db := database.DBFromContext(txCtx, r.db).WithContext(txCtx)
		sourceSetID, err := sourceSetIDForItem(db, tenantID, itemID)
		if err != nil {
			return err
		}
		sourceSet, err := lockProductionSourceSet(db, tenantID, sourceSetID)
		if err != nil {
			return err
		}
		if sourceSet.Status == types.ProductionSourceSetFrozen {
			return types.ErrProductionSourceSetFrozen
		}
		result := db.Model(&types.ProductionSourceItem{}).
			Where("id = ? AND source_set_id = ?", itemID, sourceSetID).
			UpdateColumn("status", decision)
		if result.Error != nil {
			return translateProductionSourceError(result.Error)
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func (r *productionSourceRepository) CreateEvidence(
	ctx context.Context,
	tenantID uint64,
	itemID string,
	evidence *types.ProductionEvidenceSnapshot,
) error {
	if evidence == nil {
		return errors.New("production evidence snapshot is required")
	}
	if evidence.StoragePath == "" && len(evidence.InlineContent) == 0 {
		return errors.New("production evidence content is required")
	}
	return database.WithTransactionContext(ctx, r.db, func(txCtx context.Context) error {
		db := database.DBFromContext(txCtx, r.db).WithContext(txCtx)
		sourceSetID, err := sourceSetIDForItem(db, tenantID, itemID)
		if err != nil {
			return err
		}
		sourceSet, err := lockProductionSourceSet(db, tenantID, sourceSetID)
		if err != nil {
			return err
		}
		if sourceSet.Status == types.ProductionSourceSetFrozen {
			return types.ErrProductionSourceSetFrozen
		}
		evidence.SourceItemID = itemID
		create := db
		if evidence.StoragePath == "" {
			create = create.Omit("StoragePath")
		}
		if len(evidence.InlineContent) == 0 {
			create = create.Omit("InlineContent")
		}
		return translateProductionSourceError(create.Create(evidence).Error)
	})
}

func (r *productionSourceRepository) Freeze(ctx context.Context, tenantID uint64, sourceSetID string) error {
	return database.WithTransactionContext(ctx, r.db, func(txCtx context.Context) error {
		db := database.DBFromContext(txCtx, r.db).WithContext(txCtx)
		sourceSet, err := lockProductionSourceSet(db, tenantID, sourceSetID)
		if err != nil {
			return err
		}
		if sourceSet.Status == types.ProductionSourceSetFrozen {
			return types.ErrProductionSourceSetFrozen
		}

		var missing int64
		err = db.Table("production_source_items AS item").
			Joins("LEFT JOIN production_evidence_snapshots AS evidence ON evidence.source_item_id = item.id").
			Where("item.source_set_id = ? AND item.status = ? AND evidence.id IS NULL", sourceSetID, types.ProductionSourceItemAccepted).
			Count(&missing).Error
		if err != nil {
			return err
		}
		if missing > 0 {
			return types.ErrProductionEvidenceMissing
		}

		now := time.Now().UTC()
		result := db.Model(&types.ProductionSourceSet{}).
			Where("tenant_id = ? AND id = ? AND status <> ?", tenantID, sourceSetID, types.ProductionSourceSetFrozen).
			Updates(map[string]any{"status": types.ProductionSourceSetFrozen, "frozen_at": now})
		if result.Error != nil {
			return translateProductionSourceError(result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: source set changed during freeze", types.ErrProductionSourceSetFrozen)
		}
		return nil
	})
}

var _ interfaces.ProductionSourceRepository = (*productionSourceRepository)(nil)
