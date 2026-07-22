package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

func (r *productionSourceRepository) ListSets(
	ctx context.Context,
	tenantID uint64,
	projectID string,
) ([]*types.ProductionSourceSet, error) {
	sets := make([]*types.ProductionSourceSet, 0)
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Where("tenant_id = ? AND project_id = ?", tenantID, projectID).
		Order("created_at DESC, id DESC").
		Find(&sets).Error
	return sets, err
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
	if db.Dialector.Name() == "postgres" {
		var sourceSet types.ProductionSourceSet
		err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND id = ?", tenantID, sourceSetID).
			First(&sourceSet).Error
		if err != nil {
			return nil, translateProductionSourceError(err)
		}
		return &sourceSet, nil
	}

	// SQLite has no SELECT FOR UPDATE. Entering the write lock before the
	// authoritative read serializes this source set against freeze/mutations.
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

func lockProductionSourceSetForItem(
	db *gorm.DB,
	tenantID uint64,
	itemID string,
) (*types.ProductionSourceSet, error) {
	if db.Dialector.Name() == "postgres" {
		var sourceSet types.ProductionSourceSet
		err := db.Table("production_source_sets AS source_set").
			Select("source_set.*").
			Joins("JOIN production_source_items AS item ON item.source_set_id = source_set.id").
			Where("source_set.tenant_id = ? AND item.id = ?", tenantID, itemID).
			Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "source_set"}}).
			First(&sourceSet).Error
		if err != nil {
			return nil, translateProductionSourceError(err)
		}
		return &sourceSet, nil
	}

	sourceSetID := db.Table("production_source_items").
		Select("source_set_id").
		Where("id = ?", itemID)
	result := db.Model(&types.ProductionSourceSet{}).
		Where("tenant_id = ? AND id IN (?)", tenantID, sourceSetID).
		UpdateColumn("status", gorm.Expr("status"))
	if result.Error != nil {
		return nil, translateProductionSourceError(result.Error)
	}
	if result.RowsAffected != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	var sourceSet types.ProductionSourceSet
	err := db.Table("production_source_sets AS source_set").
		Select("source_set.*").
		Joins("JOIN production_source_items AS item ON item.source_set_id = source_set.id").
		Where("source_set.tenant_id = ? AND item.id = ?", tenantID, itemID).
		First(&sourceSet).Error
	if err != nil {
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

func (r *productionSourceRepository) GetEvidence(
	ctx context.Context,
	tenantID uint64,
	evidenceID string,
) (*types.ProductionEvidenceSnapshot, *types.ProductionSourceItem, *types.ProductionSourceSet, error) {
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	var evidence types.ProductionEvidenceSnapshot
	err := db.Table("production_evidence_snapshots AS evidence").
		Select("evidence.*").
		Joins("JOIN production_source_items AS item ON item.id = evidence.source_item_id").
		Joins("JOIN production_source_sets AS source_set ON source_set.id = item.source_set_id").
		Where("source_set.tenant_id = ? AND evidence.id = ?", tenantID, evidenceID).
		First(&evidence).Error
	if err != nil {
		return nil, nil, nil, err
	}
	var item types.ProductionSourceItem
	if err := db.Where("id = ?", evidence.SourceItemID).First(&item).Error; err != nil {
		return nil, nil, nil, err
	}
	var sourceSet types.ProductionSourceSet
	if err := db.Where("tenant_id = ? AND id = ?", tenantID, item.SourceSetID).First(&sourceSet).Error; err != nil {
		return nil, nil, nil, err
	}
	return &evidence, &item, &sourceSet, nil
}

func (r *productionSourceRepository) LockFreezeGovernance(
	ctx context.Context,
	tenantID uint64,
	projectID, sourceSetID string,
) (*types.ProductionSourceSet, *types.ProductionDocumentType, error) {
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	sourceSet, err := lockProductionSourceSet(db, tenantID, sourceSetID)
	if err != nil {
		return nil, nil, err
	}
	if sourceSet.ProjectID != projectID {
		return nil, nil, gorm.ErrRecordNotFound
	}
	if sourceSet.Status == types.ProductionSourceSetFrozen {
		return nil, nil, types.ErrProductionSourceSetFrozen
	}
	typeQuery := db.Where("tenant_id = ? AND id = ?", tenantID, sourceSet.DocumentTypeID)
	if db.Dialector.Name() == "postgres" {
		typeQuery = typeQuery.Clauses(clause.Locking{Strength: "SHARE"})
	}
	var documentType types.ProductionDocumentType
	if err := typeQuery.First(&documentType).Error; err != nil {
		return nil, nil, err
	}
	return sourceSet, &documentType, nil
}

func (r *productionSourceRepository) ListAcceptedEvidence(
	ctx context.Context,
	tenantID uint64,
	projectID, sourceSetID string,
) ([]*types.ProductionEvidenceSnapshot, error) {
	var evidence []*types.ProductionEvidenceSnapshot
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Table("production_evidence_snapshots AS evidence").
		Select("evidence.*").
		Joins("JOIN production_source_items AS item ON item.id = evidence.source_item_id").
		Joins("JOIN production_source_sets AS source_set ON source_set.id = item.source_set_id").
		Where("source_set.id = ? AND source_set.tenant_id = ? AND source_set.project_id = ?", sourceSetID, tenantID, projectID).
		Where("item.status = ?", types.ProductionSourceItemAccepted).
		Order("evidence.id ASC").
		Find(&evidence).Error
	return evidence, err
}

func (r *productionSourceRepository) ListAcceptedSourceKinds(
	ctx context.Context,
	tenantID uint64,
	projectID, sourceSetID string,
) ([]types.ProductionSourceKind, error) {
	kinds := make([]types.ProductionSourceKind, 0)
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Table("production_source_items AS item").
		Distinct("item.source_kind").
		Joins("JOIN production_source_sets AS source_set ON source_set.id = item.source_set_id").
		Where("source_set.id = ? AND source_set.tenant_id = ? AND source_set.project_id = ?", sourceSetID, tenantID, projectID).
		Where("item.status = ?", types.ProductionSourceItemAccepted).
		Order("item.source_kind ASC").
		Pluck("item.source_kind", &kinds).Error
	return kinds, err
}

func (r *productionSourceRepository) DecideItem(
	ctx context.Context,
	tenantID uint64,
	itemID string,
	decision types.ProductionSourceItemStatus,
) error {
	return database.WithTransactionContext(ctx, r.db, func(txCtx context.Context) error {
		db := database.DBFromContext(txCtx, r.db).WithContext(txCtx)
		sourceSet, err := lockProductionSourceSetForItem(db, tenantID, itemID)
		if err != nil {
			return err
		}
		if sourceSet.Status == types.ProductionSourceSetFrozen {
			return types.ErrProductionSourceSetFrozen
		}
		result := db.Model(&types.ProductionSourceItem{}).
			Where("id = ? AND source_set_id = ?", itemID, sourceSet.ID).
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
		sourceSet, err := lockProductionSourceSetForItem(db, tenantID, itemID)
		if err != nil {
			return err
		}
		if sourceSet.Status == types.ProductionSourceSetFrozen {
			var call types.ProductionToolCall
			err = db.Table("production_tool_calls AS tool_call").
				Select("tool_call.*").
				Joins("JOIN production_runs AS run ON run.id = tool_call.run_id AND run.tenant_id = tool_call.tenant_id AND run.project_id = tool_call.project_id AND (run.document_id = tool_call.document_id OR (run.document_id IS NULL AND tool_call.document_id IS NULL)) AND run.source_set_id = tool_call.source_set_id").
				Joins("JOIN production_source_items AS item ON item.id = ? AND item.source_set_id = tool_call.source_set_id", itemID).
				Where("tool_call.id = ? AND tool_call.run_id = ?", evidence.CapturedByToolCallID, evidence.CapturedByRunID).
				Where("tool_call.tenant_id = ? AND tool_call.project_id = ? AND tool_call.source_set_id = ?", tenantID, sourceSet.ProjectID, sourceSet.ID).
				Where("tool_call.status = ? AND item.status = ?", types.ProductionToolCallExecuting, types.ProductionSourceItemAccepted).
				First(&call).Error
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return types.ErrProductionSourceSetFrozen
				}
				return err
			}
			expectedEvidenceID, identityErr := types.ProductionToolEvidenceID(&call)
			canonicalContent, contentErr := types.CanonicalProductionJSON(evidence.InlineContent)
			if identityErr != nil || contentErr != nil || expectedEvidenceID != evidence.ID ||
				evidence.SnapshotType != types.ProductionEvidenceSnapshotToolResult || evidence.StoragePath != "" ||
				!bytes.Equal(canonicalContent, evidence.InlineContent) || productionSnapshotDigest(canonicalContent) != evidence.ContentDigest {
				return types.ErrProductionSourceSetFrozen
			}
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
