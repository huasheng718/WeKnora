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

func (r *productionDocumentRepository) CreateDocument(
	ctx context.Context,
	document *types.ProductionDocument,
	bootstrapVersion *types.ProductionDocumentVersion,
) error {
	if document == nil || bootstrapVersion == nil || bootstrapVersion.ID == "" ||
		bootstrapVersion.SourceSetID == "" || document.ID == "" || document.CreatedBy == "" {
		return errors.New("production document and bootstrap version are required")
	}
	if bootstrapVersion.DocumentID != "" && bootstrapVersion.DocumentID != document.ID {
		return errors.New("production bootstrap version document id does not match")
	}
	if bootstrapVersion.ParentVersionID != nil || len(bootstrapVersion.Blocks) != 0 || len(bootstrapVersion.Lineage) != 0 {
		return errors.New("production bootstrap version must have no parent, blocks, or lineage")
	}
	return database.WithTransactionContext(ctx, r.db, func(txCtx context.Context) error {
		db := database.DBFromContext(txCtx, r.db).WithContext(txCtx)
		if err := lockProductionCreateDependencies(db, document, bootstrapVersion.SourceSetID); err != nil {
			return err
		}
		document.CurrentVersionID = nil
		if err := translateProductionDocumentError(db.Create(document).Error); err != nil {
			return err
		}

		bootstrapVersion.DocumentID = document.ID
		bootstrapVersion.TenantID = document.TenantID
		bootstrapVersion.ProjectID = document.ProjectID
		bootstrapVersion.VersionNumber = 1
		bootstrapVersion.ParentVersionID = nil
		bootstrapVersion.Origin = types.ProductionDocumentOriginHuman
		bootstrapVersion.ChangeSummary = ""
		bootstrapVersion.CreatedBy = document.CreatedBy
		bootstrapVersion.Blocks = nil
		bootstrapVersion.Lineage = nil
		bootstrapVersion.ContentDigest = types.ComputeProductionVersionDigest(bootstrapVersion)
		now := time.Now().UTC()
		bootstrapVersion.FrozenAt = &now
		if err := translateProductionDocumentError(
			db.Omit("Blocks", "Lineage").Create(bootstrapVersion).Error,
		); err != nil {
			return err
		}

		result := db.Model(&types.ProductionDocument{}).
			Where("id = ? AND current_version_id IS NULL", document.ID).
			UpdateColumn("current_version_id", bootstrapVersion.ID)
		if result.Error != nil {
			return translateProductionDocumentError(result.Error)
		}
		if result.RowsAffected != 1 {
			return types.ErrProductionDocumentStaleParent
		}
		document.CurrentVersionID = productionDocumentStringPointer(bootstrapVersion.ID)
		return nil
	})
}

func productionDocumentStringPointer(value string) *string { return &value }

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
	fromRelations := make(map[string]map[types.ProductionBlockRelation]struct{})
	toRelations := make(map[string]map[types.ProductionBlockRelation]struct{})
	fromTargets := make(map[string]map[string]struct{})
	toSources := make(map[string]map[string]struct{})
	edges := make(map[string]struct{}, len(lineage))
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
		edgeKey := edge.FromLogicalBlockID + "\x00" + edge.ToLogicalBlockID
		if _, duplicate := edges[edgeKey]; duplicate {
			return types.ErrProductionBlockLineageInvalid
		}
		edges[edgeKey] = struct{}{}
		if fromRelations[edge.FromLogicalBlockID] == nil {
			fromRelations[edge.FromLogicalBlockID] = make(map[types.ProductionBlockRelation]struct{})
			fromTargets[edge.FromLogicalBlockID] = make(map[string]struct{})
		}
		if toRelations[edge.ToLogicalBlockID] == nil {
			toRelations[edge.ToLogicalBlockID] = make(map[types.ProductionBlockRelation]struct{})
			toSources[edge.ToLogicalBlockID] = make(map[string]struct{})
		}
		fromRelations[edge.FromLogicalBlockID][edge.Relation] = struct{}{}
		toRelations[edge.ToLogicalBlockID][edge.Relation] = struct{}{}
		fromTargets[edge.FromLogicalBlockID][edge.ToLogicalBlockID] = struct{}{}
		toSources[edge.ToLogicalBlockID][edge.FromLogicalBlockID] = struct{}{}
		edge.FromVersionID = parentID
		edge.ToVersionID = version.ID
	}
	for _, edge := range lineage {
		switch edge.Relation {
		case types.ProductionBlockRelationSame:
			if len(fromRelations[edge.FromLogicalBlockID]) != 1 || len(toRelations[edge.ToLogicalBlockID]) != 1 ||
				len(fromTargets[edge.FromLogicalBlockID]) != 1 || len(toSources[edge.ToLogicalBlockID]) != 1 {
				return types.ErrProductionBlockLineageInvalid
			}
		case types.ProductionBlockRelationSplit:
			if len(fromRelations[edge.FromLogicalBlockID]) != 1 || len(fromTargets[edge.FromLogicalBlockID]) < 2 ||
				len(toRelations[edge.ToLogicalBlockID]) != 1 || len(toSources[edge.ToLogicalBlockID]) != 1 {
				return types.ErrProductionBlockLineageInvalid
			}
		case types.ProductionBlockRelationMerged:
			if len(toRelations[edge.ToLogicalBlockID]) != 1 || len(toSources[edge.ToLogicalBlockID]) < 2 ||
				len(fromRelations[edge.FromLogicalBlockID]) != 1 || len(fromTargets[edge.FromLogicalBlockID]) != 1 {
				return types.ErrProductionBlockLineageInvalid
			}
		default:
			return types.ErrProductionBlockLineageInvalid
		}
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

func lockProductionCreateDependencies(
	db *gorm.DB,
	document *types.ProductionDocument,
	sourceSetID string,
) error {
	if db.Dialector.Name() != "postgres" {
		// SQLite has one writer. Lock the dependencies in the same type-then-source
		// order as PostgreSQL before their authoritative lifecycle reads.
		typeLock := db.Model(&types.ProductionDocumentType{}).
			Where(
				"id = ? AND tenant_id = ? AND schema_version = ?",
				document.DocumentTypeID, document.TenantID, document.DocumentTypeSchemaVersion,
			).
			UpdateColumn("status", gorm.Expr("status"))
		if typeLock.Error != nil {
			return translateProductionDocumentError(typeLock.Error)
		}
		if typeLock.RowsAffected != 1 {
			return types.ErrProductionDocumentTypeInactive
		}
		sourceLock := db.Model(&types.ProductionSourceSet{}).
			Where(
				"id = ? AND tenant_id = ? AND project_id = ? AND document_type_id = ?",
				sourceSetID, document.TenantID, document.ProjectID, document.DocumentTypeID,
			).
			UpdateColumn("status", gorm.Expr("status"))
		if sourceLock.Error != nil {
			return translateProductionDocumentError(sourceLock.Error)
		}
		if sourceLock.RowsAffected != 1 {
			return types.ErrProductionDocumentSourceSetInvalid
		}
	}
	return validateProductionAppendDependencies(db, document, sourceSetID)
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

func sameProductionVersionCandidate(
	existing, candidate *types.ProductionDocumentVersion,
	existingBlocks, candidateBlocks []*types.ProductionDocumentBlock,
	existingLineage, candidateLineage []*types.ProductionBlockLineage,
) bool {
	if existing == nil || candidate == nil || existing.ID != candidate.ID ||
		existing.DocumentID != candidate.DocumentID || existing.TenantID != candidate.TenantID ||
		existing.ProjectID != candidate.ProjectID || normalizeProductionParent(existing.ParentVersionID) != normalizeProductionParent(candidate.ParentVersionID) ||
		existing.SourceSetID != candidate.SourceSetID || existing.Origin != candidate.Origin ||
		existing.ChangeSummary != candidate.ChangeSummary || existing.ContentDigest != candidate.ContentDigest ||
		existing.CreatedBy != candidate.CreatedBy || len(existingBlocks) != len(candidateBlocks) ||
		len(existingLineage) != len(candidateLineage) {
		return false
	}
	for i := range existingBlocks {
		left, right := existingBlocks[i], candidateBlocks[i]
		if left == nil || right == nil || left.LogicalBlockID != right.LogicalBlockID ||
			left.BlockType != right.BlockType || left.Position != right.Position ||
			string(left.Content) != string(right.Content) || string(left.Attributes) != string(right.Attributes) ||
			string(left.EvidenceRefs) != string(right.EvidenceRefs) || string(left.AIProvenance) != string(right.AIProvenance) ||
			left.ContentDigest != right.ContentDigest {
			return false
		}
	}
	for i := range existingLineage {
		left, right := existingLineage[i], candidateLineage[i]
		if left == nil || right == nil || left.FromVersionID != right.FromVersionID ||
			left.FromLogicalBlockID != right.FromLogicalBlockID || left.ToVersionID != right.ToVersionID ||
			left.ToLogicalBlockID != right.ToLogicalBlockID || left.Relation != right.Relation {
			return false
		}
	}
	return true
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
		version.TenantID = document.TenantID
		version.ProjectID = document.ProjectID

		var existing types.ProductionDocumentVersion
		existingErr := db.Where("id = ?", version.ID).First(&existing).Error
		if existingErr == nil {
			if document.CurrentVersionID == nil || *document.CurrentVersionID != existing.ID {
				return types.ErrProductionDocumentStaleParent
			}
			var existingBlocks []*types.ProductionDocumentBlock
			if err := db.Where("version_id = ?", existing.ID).Order("position ASC").Find(&existingBlocks).Error; err != nil {
				return err
			}
			var existingLineage []*types.ProductionBlockLineage
			if err := db.Where("to_version_id = ?", existing.ID).Order("id ASC").Find(&existingLineage).Error; err != nil {
				return err
			}
			if sameProductionVersionCandidate(&existing, version, existingBlocks, blocks, existingLineage, lineage) {
				return types.ErrProductionDocumentVersionExists
			}
			return types.ErrProductionConflict
		}
		if !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return existingErr
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
		result := update.Updates(map[string]any{
			"current_version_id": version.ID,
			"status":             types.ProductionDocumentDraft,
			"updated_at":         now,
		})
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
