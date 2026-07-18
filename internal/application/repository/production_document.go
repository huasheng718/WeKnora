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
	"gorm.io/gorm/clause"
)

type productionDocumentRepository struct{ db *gorm.DB }

func NewProductionDocumentRepository(db *gorm.DB) interfaces.ProductionDocumentRepository {
	return &productionDocumentRepository{db: db}
}

func translateProductionDocumentError(err error) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "document versions are append-only"):
		return errors.Join(types.ErrProductionDocumentVersionImmutable, err)
	case strings.Contains(lower, "document blocks are append-only"):
		return errors.Join(types.ErrProductionDocumentBlockImmutable, err)
	default:
		return translateProductionWriteError(err)
	}
}

func (r *productionDocumentRepository) CreateDocument(ctx context.Context, document *types.ProductionDocument) error {
	if document == nil {
		return errors.New("production document is required")
	}
	return translateProductionDocumentError(
		database.DBFromContext(ctx, r.db).WithContext(ctx).Create(document).Error,
	)
}

func (r *productionDocumentRepository) GetDocument(
	ctx context.Context,
	tenantID uint64,
	documentID string,
) (*types.ProductionDocument, error) {
	var document types.ProductionDocument
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, documentID).
		First(&document).Error
	if err != nil {
		return nil, err
	}
	return &document, nil
}

func lockProductionDocumentHead(db *gorm.DB, documentID string) (*types.ProductionDocument, error) {
	if db.Dialector.Name() == "postgres" {
		var document types.ProductionDocument
		err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", documentID).
			First(&document).Error
		if err != nil {
			return nil, translateProductionDocumentError(err)
		}
		return &document, nil
	}

	// SQLite has no SELECT FOR UPDATE. Acquiring its write lock before the
	// authoritative head read serializes competing append transactions.
	result := db.Model(&types.ProductionDocument{}).
		Where("id = ?", documentID).
		UpdateColumn("current_version_id", gorm.Expr("current_version_id"))
	if result.Error != nil {
		return nil, translateProductionDocumentError(result.Error)
	}
	if result.RowsAffected != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	var document types.ProductionDocument
	if err := db.Where("id = ?", documentID).First(&document).Error; err != nil {
		return nil, err
	}
	return &document, nil
}

func normalizeProductionParent(parent *string) string {
	if parent == nil {
		return ""
	}
	return *parent
}

func validateProductionLineage(
	db *gorm.DB,
	version *types.ProductionDocumentVersion,
	blocks []*types.ProductionDocumentBlock,
	lineage []*types.ProductionBlockLineage,
) error {
	if len(lineage) == 0 {
		return nil
	}
	parentID := normalizeProductionParent(version.ParentVersionID)
	if parentID == "" {
		return types.ErrProductionBlockLineageInvalid
	}
	var parentBlockIDs []string
	if err := db.Model(&types.ProductionDocumentBlock{}).
		Where("version_id = ?", parentID).
		Pluck("logical_block_id", &parentBlockIDs).Error; err != nil {
		return err
	}
	parentBlocks := make(map[string]struct{}, len(parentBlockIDs))
	for _, logicalID := range parentBlockIDs {
		parentBlocks[logicalID] = struct{}{}
	}
	newBlocks := make(map[string]struct{}, len(blocks))
	for _, block := range blocks {
		if block != nil {
			newBlocks[block.LogicalBlockID] = struct{}{}
		}
	}
	for _, edge := range lineage {
		if edge == nil || edge.ID == "" || !edge.Relation.IsValid() {
			return types.ErrProductionBlockLineageInvalid
		}
		if edge.FromVersionID != "" && edge.FromVersionID != parentID {
			return types.ErrProductionBlockLineageInvalid
		}
		if edge.ToVersionID != "" && edge.ToVersionID != version.ID {
			return types.ErrProductionBlockLineageInvalid
		}
		if _, ok := parentBlocks[edge.FromLogicalBlockID]; !ok {
			return types.ErrProductionBlockLineageInvalid
		}
		if _, ok := newBlocks[edge.ToLogicalBlockID]; !ok {
			return types.ErrProductionBlockLineageInvalid
		}
		edge.FromVersionID = parentID
		edge.ToVersionID = version.ID
	}
	return nil
}

func validateProductionAppendDependencies(
	db *gorm.DB,
	document *types.ProductionDocument,
	sourceSetID string,
) error {
	typeQuery := db.Where(
		"id = ? AND tenant_id = ? AND schema_version = ? AND status = ?",
		document.DocumentTypeID, document.TenantID, document.DocumentTypeSchemaVersion, types.ProductionDocumentTypeActive,
	)
	if db.Dialector.Name() == "postgres" {
		typeQuery = typeQuery.Clauses(clause.Locking{Strength: "SHARE"})
	}
	var documentType types.ProductionDocumentType
	if err := typeQuery.First(&documentType).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return types.ErrProductionDocumentTypeInactive
		}
		return err
	}

	sourceQuery := db.Where(
		"id = ? AND tenant_id = ? AND project_id = ? AND document_type_id = ? AND status = ?",
		sourceSetID, document.TenantID, document.ProjectID, document.DocumentTypeID, types.ProductionSourceSetFrozen,
	)
	if db.Dialector.Name() == "postgres" {
		sourceQuery = sourceQuery.Clauses(clause.Locking{Strength: "SHARE"})
	}
	var sourceSet types.ProductionSourceSet
	if err := sourceQuery.First(&sourceSet).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return types.ErrProductionDocumentSourceSetInvalid
		}
		return err
	}
	return nil
}

func prepareProductionBlocks(version *types.ProductionDocumentVersion, blocks []*types.ProductionDocumentBlock) error {
	if len(blocks) == 0 {
		return errors.New("production document version requires at least one block")
	}
	logicalIDs := make(map[string]struct{}, len(blocks))
	for position, block := range blocks {
		if block == nil || block.ID == "" || block.LogicalBlockID == "" || block.BlockType == "" || len(block.Content) == 0 {
			return errors.New("production document block requires row id, logical id, type, and content")
		}
		if _, duplicate := logicalIDs[block.LogicalBlockID]; duplicate {
			return fmt.Errorf("%w: duplicate logical block id %q", types.ErrProductionBlockLineageInvalid, block.LogicalBlockID)
		}
		logicalIDs[block.LogicalBlockID] = struct{}{}
		block.VersionID = version.ID
		block.Position = position
		block.ContentDigest = types.ComputeProductionBlockDigest(block)
	}
	version.Blocks = blocks
	version.ContentDigest = types.ComputeProductionVersionDigest(version)
	return nil
}

func (r *productionDocumentRepository) AppendVersion(
	ctx context.Context,
	version *types.ProductionDocumentVersion,
	blocks []*types.ProductionDocumentBlock,
	lineage []*types.ProductionBlockLineage,
) error {
	if version == nil || version.ID == "" || version.DocumentID == "" || version.SourceSetID == "" || version.CreatedBy == "" {
		return errors.New("production document version, document id, source set id, and creator are required")
	}
	if !version.Origin.IsValid() {
		return errors.New("invalid production document version origin")
	}
	if err := prepareProductionBlocks(version, blocks); err != nil {
		return err
	}

	err := database.WithTransactionContext(ctx, r.db, func(txCtx context.Context) error {
		db := database.DBFromContext(txCtx, r.db).WithContext(txCtx)
		document, err := lockProductionDocumentHead(db, version.DocumentID)
		if err != nil {
			return err
		}

		if err := validateProductionAppendDependencies(db, document, version.SourceSetID); err != nil {
			return err
		}

		parentID := normalizeProductionParent(version.ParentVersionID)
		if document.CurrentVersionID == nil {
			if parentID != "" {
				return types.ErrProductionDocumentStaleParent
			}
			version.VersionNumber = 1
		} else {
			if parentID == "" || parentID != *document.CurrentVersionID {
				return types.ErrProductionDocumentStaleParent
			}
			var parent types.ProductionDocumentVersion
			if err := db.Where("id = ? AND document_id = ?", parentID, document.ID).First(&parent).Error; err != nil {
				return errors.Join(types.ErrProductionDocumentStaleParent, err)
			}
			version.VersionNumber = parent.VersionNumber + 1
		}

		version.TenantID = document.TenantID
		version.ProjectID = document.ProjectID
		now := time.Now().UTC()
		version.FrozenAt = &now
		if err := validateProductionLineage(db, version, blocks, lineage); err != nil {
			return err
		}

		if err := db.Omit("Blocks", "Lineage").Create(version).Error; err != nil {
			return translateProductionDocumentError(err)
		}
		if err := db.Create(&blocks).Error; err != nil {
			return translateProductionDocumentError(err)
		}
		if len(lineage) > 0 {
			if err := db.Create(&lineage).Error; err != nil {
				return translateProductionDocumentError(err)
			}
		}

		update := db.Model(&types.ProductionDocument{}).Where("id = ?", document.ID)
		if document.CurrentVersionID == nil {
			update = update.Where("current_version_id IS NULL")
		} else {
			update = update.Where("current_version_id = ?", *document.CurrentVersionID)
		}
		result := update.UpdateColumn("current_version_id", version.ID)
		if result.Error != nil {
			return translateProductionDocumentError(result.Error)
		}
		if result.RowsAffected != 1 {
			return types.ErrProductionDocumentStaleParent
		}
		return nil
	})
	if err != nil {
		return err
	}
	version.Blocks = blocks
	version.Lineage = lineage
	return nil
}

func (r *productionDocumentRepository) GetVersion(
	ctx context.Context,
	tenantID uint64,
	versionID string,
) (*types.ProductionDocumentVersion, error) {
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	var version types.ProductionDocumentVersion
	err := db.Table("production_document_versions AS version").
		Select("version.*").
		Joins("JOIN production_documents AS document ON document.id = version.document_id").
		Where("document.tenant_id = ? AND version.id = ?", tenantID, versionID).
		First(&version).Error
	if err != nil {
		return nil, err
	}
	if err := db.Where("version_id = ?", version.ID).
		Order("position ASC, id ASC").
		Find(&version.Blocks).Error; err != nil {
		return nil, err
	}
	if err := db.Where("to_version_id = ?", version.ID).
		Order("id ASC").
		Find(&version.Lineage).Error; err != nil {
		return nil, err
	}
	return &version, nil
}

func (r *productionDocumentRepository) ListVersions(
	ctx context.Context,
	tenantID uint64,
	documentID string,
) ([]*types.ProductionDocumentVersion, error) {
	var versions []*types.ProductionDocumentVersion
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Table("production_document_versions AS version").
		Select("version.*").
		Joins("JOIN production_documents AS document ON document.id = version.document_id").
		Where("document.tenant_id = ? AND document.id = ?", tenantID, documentID).
		Order("version.version_number ASC, version.id ASC").
		Find(&versions).Error
	if err != nil {
		return nil, err
	}
	return versions, nil
}

var _ interfaces.ProductionDocumentRepository = (*productionDocumentRepository)(nil)
