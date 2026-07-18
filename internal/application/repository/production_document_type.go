package repository

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
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
	return r.db.WithContext(ctx).Create(documentType).Error
}

func (r *productionDocumentTypeRepository) Activate(
	ctx context.Context,
	tenantID uint64,
	code string,
	schemaVersion int,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		return nil
	})
}

func (r *productionDocumentTypeRepository) GetByID(
	ctx context.Context,
	tenantID uint64,
	documentTypeID string,
) (*types.ProductionDocumentType, error) {
	var documentType types.ProductionDocumentType
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, documentTypeID).
		First(&documentType).Error
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
	err := r.db.WithContext(ctx).
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
	err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("code ASC, schema_version DESC, id ASC").
		Find(&documentTypes).Error
	if err != nil {
		return nil, err
	}
	return documentTypes, nil
}

var _ interfaces.ProductionDocumentTypeRepository = (*productionDocumentTypeRepository)(nil)
