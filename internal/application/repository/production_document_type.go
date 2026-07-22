package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type productionDocumentTypeRepository struct {
	db *gorm.DB
}

// NewProductionDocumentTypeRepository creates a tenant-scoped document-type repository.
func NewProductionDocumentTypeRepository(db *gorm.DB) interfaces.ProductionDocumentTypeRepository {
	return &productionDocumentTypeRepository{db: db}
}

func (r *productionDocumentTypeRepository) Create(
	ctx context.Context,
	documentType *types.ProductionDocumentType,
) error {
	if documentType == nil {
		return errors.New("production document type is required")
	}
	return translateProductionWriteError(
		database.DBFromContext(ctx, r.db).WithContext(ctx).Create(documentType).Error,
	)
}

func (r *productionDocumentTypeRepository) SeedBuiltins(
	ctx context.Context,
	tenantID uint64,
	actor string,
	definitions []types.ProductionDocumentType,
) error {
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	for index := range definitions {
		definition := definitions[index]
		var collisionCount int64
		if err := db.Model(&types.ProductionDocumentType{}).
			Where("tenant_id = ? AND deleted_at IS NULL AND (code = ? OR template_key = ?)", tenantID, definition.Code, definition.Code).
			Count(&collisionCount).Error; err != nil {
			return err
		}
		if collisionCount > 0 {
			continue
		}
		templateKey := definition.Code
		definition.TenantID = tenantID
		definition.CreatedBy = actor
		definition.SchemaVersion = 1
		definition.Status = types.ProductionDocumentTypeActive
		definition.Origin = types.ProductionDocumentTypeOriginBuiltin
		definition.TemplateKey = &templateKey
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&definition).Error; err != nil {
			return translateProductionWriteError(err)
		}
	}

	for _, definition := range definitions {
		var templateCount int64
		if err := db.Model(&types.ProductionDocumentType{}).
			Where("tenant_id = ? AND template_key = ? AND schema_version = ? AND deleted_at IS NULL", tenantID, definition.Code, 1).
			Count(&templateCount).Error; err != nil {
			return err
		}
		if templateCount > 0 {
			continue
		}
		var codeCollisionCount int64
		if err := db.Model(&types.ProductionDocumentType{}).
			Where("tenant_id = ? AND code = ? AND deleted_at IS NULL", tenantID, definition.Code).
			Count(&codeCollisionCount).Error; err != nil {
			return err
		}
		if codeCollisionCount == 0 {
			return fmt.Errorf("production built-in document type %q was not seeded", definition.Code)
		}
	}
	return nil
}

func (r *productionDocumentTypeRepository) Activate(
	ctx context.Context,
	tenantID uint64,
	code string,
	schemaVersion int,
) (*types.ProductionDocumentType, error) {
	var activated types.ProductionDocumentType
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&types.ProductionDocumentType{}).
			Where("tenant_id = ? AND code = ? AND status = ?", tenantID, code, types.ProductionDocumentTypeActive).
			Update("status", types.ProductionDocumentTypeRetired).Error; err != nil {
			return err
		}

		result := tx.Model(&types.ProductionDocumentType{}).
			Where(
				"tenant_id = ? AND code = ? AND schema_version = ? AND status = ?",
				tenantID, code, schemaVersion, types.ProductionDocumentTypeDraft,
			).
			Update("status", types.ProductionDocumentTypeActive)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.Where(
			"tenant_id = ? AND code = ? AND schema_version = ? AND status = ?",
			tenantID, code, schemaVersion, types.ProductionDocumentTypeActive,
		).First(&activated).Error
	})
	if err != nil {
		return nil, err
	}
	return &activated, nil
}

func (r *productionDocumentTypeRepository) GetByID(
	ctx context.Context,
	tenantID uint64,
	documentTypeID string,
) (*types.ProductionDocumentType, error) {
	var documentType types.ProductionDocumentType
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, documentTypeID).
		First(&documentType).Error
	if err != nil {
		return nil, err
	}
	return &documentType, nil
}

func (r *productionDocumentTypeRepository) GetActiveByIDForReview(
	ctx context.Context,
	tenantID uint64,
	documentTypeID string,
	schemaVersion int,
) (*types.ProductionDocumentType, error) {
	var documentType types.ProductionDocumentType
	err := database.WithTransactionContext(ctx, r.db, func(txCtx context.Context) error {
		db := database.DBFromContext(txCtx, r.db).WithContext(txCtx)
		where := db.Where(
			"tenant_id = ? AND id = ? AND schema_version = ? AND status = ?",
			tenantID, documentTypeID, schemaVersion, types.ProductionDocumentTypeActive,
		)
		if db.Dialector.Name() == "postgres" {
			return where.Clauses(clause.Locking{Strength: "SHARE"}).First(&documentType).Error
		}
		// SQLite has no row locks. Reserve its single writer before the read so
		// activation/retirement cannot interleave with policy materialization.
		result := where.Model(&types.ProductionDocumentType{}).
			UpdateColumn("status", gorm.Expr("status"))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return db.Where(
			"tenant_id = ? AND id = ? AND schema_version = ? AND status = ?",
			tenantID, documentTypeID, schemaVersion, types.ProductionDocumentTypeActive,
		).First(&documentType).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, types.ErrProductionDocumentTypeInactive
	}
	if err != nil {
		return nil, err
	}
	return &documentType, nil
}

func (r *productionDocumentTypeRepository) GetActiveByCode(
	ctx context.Context,
	tenantID uint64,
	code string,
) (*types.ProductionDocumentType, error) {
	var documentType types.ProductionDocumentType
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Where("tenant_id = ? AND code = ? AND status = ?", tenantID, code, types.ProductionDocumentTypeActive).
		First(&documentType).Error
	if err != nil {
		return nil, err
	}
	return &documentType, nil
}

func (r *productionDocumentTypeRepository) List(
	ctx context.Context,
	tenantID uint64,
) ([]*types.ProductionDocumentType, error) {
	var documentTypes []*types.ProductionDocumentType
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("code ASC, schema_version DESC, id ASC").
		Find(&documentTypes).Error
	if err != nil {
		return nil, err
	}
	return documentTypes, nil
}

var _ interfaces.ProductionDocumentTypeRepository = (*productionDocumentTypeRepository)(nil)
