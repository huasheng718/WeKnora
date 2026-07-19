package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

type productionDocumentService struct {
	documents     interfaces.ProductionDocumentRepository
	sources       interfaces.ProductionSourceRepository
	documentTypes interfaces.ProductionDocumentTypeRepository
	projects      interfaces.ProductionProjectAuthorizer
	resources     interfaces.ResourceCatalog
	audit         interfaces.AuditLogService
	uow           interfaces.ProductionUnitOfWork
}

func NewProductionDocumentService(
	documents interfaces.ProductionDocumentRepository,
	sources interfaces.ProductionSourceRepository,
	documentTypes interfaces.ProductionDocumentTypeRepository,
	projects interfaces.ProductionProjectAuthorizer,
	resources interfaces.ResourceCatalog,
	audit interfaces.AuditLogService,
	uow interfaces.ProductionUnitOfWork,
) *productionDocumentService {
	return &productionDocumentService{
		documents: documents, sources: sources, documentTypes: documentTypes, projects: projects,
		resources: resources, audit: audit, uow: uow,
	}
}

type ProductionDocumentValidationError struct {
	Issues []ProductionValidationIssue
	Cause  error
}

func (e *ProductionDocumentValidationError) Error() string {
	parts := make([]string, 0, len(e.Issues)+1)
	for _, issue := range e.Issues {
		parts = append(parts, issue.Code)
	}
	if e.Cause != nil {
		parts = append(parts, e.Cause.Error())
	}
	if len(parts) == 0 {
		return types.ErrProductionDocumentValidation.Error()
	}
	sort.Strings(parts)
	return types.ErrProductionDocumentValidation.Error() + ": " + strings.Join(parts, ", ")
}

func (e *ProductionDocumentValidationError) Unwrap() []error {
	errors := []error{types.ErrProductionDocumentValidation}
	if e.Cause != nil {
		errors = append(errors, e.Cause)
	}
	return errors
}

func requireProductionDocumentAuthor(
	ctx context.Context,
	projects interfaces.ProductionProjectAuthorizer,
	projectID string,
) error {
	if projects == nil {
		return types.ErrProductionForbidden
	}
	return projects.RequireProjectRole(
		ctx,
		projectID,
		types.ProductionRoleProjectOwner,
		types.ProductionRoleAuthor,
	)
}

func requireProductionDocumentReader(
	ctx context.Context,
	projects interfaces.ProductionProjectAuthorizer,
	projectID string,
) error {
	if projects == nil {
		return types.ErrProductionForbidden
	}
	return projects.RequireProjectRole(ctx, projectID, allProductionProjectRoles...)
}

func validateProductionDocumentSourceSet(
	sourceSet *types.ProductionSourceSet,
	projectID, documentTypeID string,
) error {
	if sourceSet == nil || sourceSet.Status != types.ProductionSourceSetFrozen ||
		sourceSet.ProjectID != projectID || sourceSet.DocumentTypeID != documentTypeID {
		return types.ErrProductionDocumentSourceSetInvalid
	}
	return nil
}

func validateProductionDocumentType(
	documentType *types.ProductionDocumentType,
	documentTypeID string,
	schemaVersion int,
) error {
	if documentType == nil || documentType.ID != documentTypeID ||
		documentType.Status != types.ProductionDocumentTypeActive ||
		(schemaVersion > 0 && documentType.SchemaVersion != schemaVersion) {
		return types.ErrProductionDocumentTypeInactive
	}
	return nil
}

func (s *productionDocumentService) CreateDocument(
	ctx context.Context,
	input interfaces.CreateProductionDocumentInput,
) (*types.ProductionDocument, error) {
	tenantID, userID, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	for value, name := range map[string]string{
		input.ProjectID: "project id", input.DocumentTypeID: "document type id", input.SourceSetID: "source set id",
	} {
		if err := requireProductionSourceID(value, name); err != nil {
			return nil, err
		}
	}
	title := strings.TrimSpace(input.Title)
	if title == "" || utf8.RuneCountInString(title) > 255 {
		return nil, errors.New("production document title must contain 1 to 255 characters")
	}

	sourceSet, err := s.sources.GetSet(ctx, tenantID, input.SourceSetID)
	if err != nil {
		return nil, err
	}
	if err := validateProductionDocumentSourceSet(sourceSet, input.ProjectID, input.DocumentTypeID); err != nil {
		return nil, err
	}
	documentType, err := s.documentTypes.GetByID(ctx, tenantID, input.DocumentTypeID)
	if err != nil {
		return nil, err
	}
	if err := validateProductionDocumentType(documentType, input.DocumentTypeID, 0); err != nil {
		return nil, err
	}
	if err := requireProductionDocumentAuthor(ctx, s.projects, input.ProjectID); err != nil {
		return nil, err
	}

	document := &types.ProductionDocument{
		ID: uuid.NewString(), TenantID: tenantID, ProjectID: input.ProjectID,
		DocumentTypeID: input.DocumentTypeID, DocumentTypeSchemaVersion: documentType.SchemaVersion,
		Title: title, Status: types.ProductionDocumentDraft, CreatedBy: userID,
	}
	bootstrapVersion := &types.ProductionDocumentVersion{
		ID: uuid.NewString(), DocumentID: document.ID, SourceSetID: input.SourceSetID,
		Origin: types.ProductionDocumentOriginHuman, CreatedBy: userID,
	}
	bootstrapVersion.ContentDigest = types.ComputeProductionVersionDigest(bootstrapVersion)
	if s.uow == nil {
		return nil, errors.New("production unit of work is required")
	}
	if err := s.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.documents.CreateDocument(txCtx, document, bootstrapVersion); err != nil {
			return err
		}
		return emitRequiredProductionAudit(txCtx, s.audit, &types.AuditLog{
			TenantID: tenantID, ActorUserID: userID, ActorRole: string(types.TenantRoleFromContext(ctx)),
			Action: types.AuditActionProductionVersionCreated, TargetType: "production_document_version",
			TargetID: bootstrapVersion.ID, Outcome: types.AuditOutcomeSuccess,
		})
	}); err != nil {
		return nil, err
	}
	return document, nil
}

func canonicalProductionDocumentBlock(
	input types.ProductionDocumentBlockInput,
	position int,
) (*types.ProductionDocumentBlock, error) {
	logicalID := input.LogicalBlockID
	if logicalID == "" {
		logicalID = uuid.NewString()
	}
	if len(logicalID) > 36 {
		return nil, errors.New("production logical block id exceeds 36 characters")
	}
	blockType := strings.TrimSpace(input.BlockType)
	if blockType == "" || len(blockType) > 24 {
		return nil, errors.New("production document block type is required and must not exceed 24 characters")
	}
	content, err := canonicalProductionJSON(input.Content, "null")
	if err != nil {
		return nil, fmt.Errorf("invalid production document block content: %w", err)
	}
	attributes, err := canonicalProductionJSON(input.Attributes, `{}`)
	if err != nil {
		return nil, fmt.Errorf("invalid production document block attributes: %w", err)
	}
	evidenceRefs, err := canonicalProductionJSON(input.EvidenceRefs, `[]`)
	if err != nil {
		return nil, fmt.Errorf("invalid production document block evidence references: %w", err)
	}
	aiProvenance, err := canonicalProductionJSON(input.AIProvenance, `{}`)
	if err != nil {
		return nil, fmt.Errorf("invalid production document block AI provenance: %w", err)
	}
	block := &types.ProductionDocumentBlock{
		ID: uuid.NewString(), LogicalBlockID: logicalID, BlockType: blockType, Position: position,
		Content: content, Attributes: attributes, EvidenceRefs: evidenceRefs, AIProvenance: aiProvenance,
	}
	block.ContentDigest = types.ComputeProductionBlockDigest(block)
	return block, nil
}

func buildProductionDocumentBlocks(
	inputs []types.ProductionDocumentBlockInput,
) ([]*types.ProductionDocumentBlock, error) {
	if len(inputs) == 0 {
		return nil, errors.New("production document version requires at least one block")
	}
	blocks := make([]*types.ProductionDocumentBlock, 0, len(inputs))
	logicalIDs := make(map[string]struct{}, len(inputs))
	for position, input := range inputs {
		block, err := canonicalProductionDocumentBlock(input, position)
		if err != nil {
			return nil, err
		}
		if _, duplicate := logicalIDs[block.LogicalBlockID]; duplicate {
			return nil, fmt.Errorf("duplicate production logical block id %q", block.LogicalBlockID)
		}
		logicalIDs[block.LogicalBlockID] = struct{}{}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func buildProductionBlockLineage(
	parentVersionID, versionID string,
	inputs []types.ProductionBlockLineageInput,
) ([]*types.ProductionBlockLineage, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if parentVersionID == "" {
		return nil, types.ErrProductionBlockLineageInvalid
	}
	lineage := make([]*types.ProductionBlockLineage, 0, len(inputs))
	for _, input := range inputs {
		if input.FromLogicalBlockID == "" || input.ToLogicalBlockID == "" || !input.Relation.IsValid() {
			return nil, types.ErrProductionBlockLineageInvalid
		}
		lineage = append(lineage, &types.ProductionBlockLineage{
			ID: uuid.NewString(), FromVersionID: parentVersionID, FromLogicalBlockID: input.FromLogicalBlockID,
			ToVersionID: versionID, ToLogicalBlockID: input.ToLogicalBlockID, Relation: input.Relation,
		})
	}
	return lineage, nil
}

func (s *productionDocumentService) loadAcceptedProductionEvidence(
	ctx context.Context,
	tenantID uint64,
	projectID, sourceSetID string,
) (map[string]struct{}, map[string]*types.ProductionEvidenceSnapshot, error) {
	snapshots, err := s.sources.ListAcceptedEvidence(ctx, tenantID, projectID, sourceSetID)
	if err != nil {
		return nil, nil, err
	}
	accepted := make(map[string]struct{}, len(snapshots))
	evidenceByID := make(map[string]*types.ProductionEvidenceSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot == nil || snapshot.ID == "" {
			return nil, nil, &ProductionDocumentValidationError{Cause: errors.New("accepted evidence snapshot is invalid")}
		}
		if snapshot.StoragePath != "" {
			if s.resources == nil {
				return nil, nil, &ProductionDocumentValidationError{Cause: errors.New("resource catalog is required for registry-backed evidence")}
			}
			resource, resolveErr := s.resources.ResolveBound(ctx, snapshot.StoragePath, interfaces.ResourceBindingRequirement{
				TenantID: tenantID, OwnerType: types.ResourceOwnerTypeProductionProject, OwnerID: projectID,
			})
			if resolveErr != nil {
				return nil, nil, &ProductionDocumentValidationError{Cause: fmt.Errorf("resolve evidence %s: %w", snapshot.ID, resolveErr)}
			}
			if resource == nil || resource.TenantID != tenantID || resource.State != types.ResourceStateActive ||
				resource.Lifecycle != types.ResourceLifecyclePersistent {
				return nil, nil, &ProductionDocumentValidationError{Cause: fmt.Errorf("evidence %s resource is invalid", snapshot.ID)}
			}
			snapshot.ResolvedContentDigest = resource.ContentHash
		}
		accepted[snapshot.ID] = struct{}{}
		evidenceByID[snapshot.ID] = snapshot
	}
	return accepted, evidenceByID, nil
}

func (s *productionDocumentService) AppendVersion(
	ctx context.Context,
	documentID string,
	input interfaces.AppendProductionVersionInput,
) (*types.ProductionDocumentVersion, error) {
	tenantID, userID, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireProductionSourceID(documentID, "document id"); err != nil {
		return nil, err
	}
	if err := requireProductionSourceID(input.SourceSetID, "source set id"); err != nil {
		return nil, err
	}
	if input.ParentVersionID != "" {
		if err := requireProductionSourceID(input.ParentVersionID, "parent version id"); err != nil {
			return nil, err
		}
	}

	document, err := s.documents.GetDocument(ctx, tenantID, documentID)
	if err != nil {
		return nil, err
	}
	if err := requireProductionDocumentAuthor(ctx, s.projects, document.ProjectID); err != nil {
		return nil, err
	}
	sourceSet, err := s.sources.GetSet(ctx, tenantID, input.SourceSetID)
	if err != nil {
		return nil, err
	}
	if err := validateProductionDocumentSourceSet(sourceSet, document.ProjectID, document.DocumentTypeID); err != nil {
		return nil, err
	}
	documentType, err := s.documentTypes.GetByID(ctx, tenantID, document.DocumentTypeID)
	if err != nil {
		return nil, err
	}
	if err := validateProductionDocumentType(
		documentType,
		document.DocumentTypeID,
		document.DocumentTypeSchemaVersion,
	); err != nil {
		return nil, err
	}

	blocks, err := buildProductionDocumentBlocks(input.Blocks)
	if err != nil {
		return nil, err
	}
	origin := input.Origin
	if origin == "" {
		origin = types.ProductionDocumentOriginHuman
	}
	if !origin.IsValid() {
		return nil, errors.New("invalid production document version origin")
	}
	version := &types.ProductionDocumentVersion{
		ID: uuid.NewString(), DocumentID: document.ID, SourceSetID: input.SourceSetID,
		Origin: origin, ChangeSummary: input.ChangeSummary, CreatedBy: userID, Blocks: blocks,
	}
	version.DocumentTypeCode = documentType.Code
	if input.ParentVersionID != "" {
		version.ParentVersionID = &input.ParentVersionID
	}
	version.ContentDigest = types.ComputeProductionVersionDigest(version)
	lineage, err := buildProductionBlockLineage(input.ParentVersionID, version.ID, input.Lineage)
	if err != nil {
		return nil, err
	}
	acceptedEvidence, evidenceByID, err := s.loadAcceptedProductionEvidence(
		ctx, tenantID, document.ProjectID, input.SourceSetID,
	)
	if err != nil {
		return nil, err
	}
	validation := ValidateProductionVersion(version, acceptedEvidence)
	if len(validation.Errors) != 0 {
		return nil, &ProductionDocumentValidationError{Issues: validation.Errors}
	}
	if err := VerifyProductionVersionDigests(version, evidenceByID); err != nil {
		return nil, &ProductionDocumentValidationError{Cause: err}
	}
	if s.uow == nil {
		return nil, errors.New("production unit of work is required")
	}
	if err := s.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.documents.AppendVersion(txCtx, version, blocks, lineage); err != nil {
			return err
		}
		return emitRequiredProductionAudit(txCtx, s.audit, &types.AuditLog{
			TenantID: tenantID, ActorUserID: userID, ActorRole: string(types.TenantRoleFromContext(ctx)),
			Action: types.AuditActionProductionVersionCreated, TargetType: "production_document_version",
			TargetID: version.ID, Outcome: types.AuditOutcomeSuccess,
		})
	}); err != nil {
		return nil, err
	}
	return version, nil
}

func (s *productionDocumentService) GetVersion(
	ctx context.Context,
	versionID string,
) (*types.ProductionDocumentVersion, error) {
	tenantID, _, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireProductionSourceID(versionID, "version id"); err != nil {
		return nil, err
	}
	version, err := s.documents.GetVersion(ctx, tenantID, versionID)
	if err != nil {
		return nil, err
	}
	if err := requireProductionDocumentReader(ctx, s.projects, version.ProjectID); err != nil {
		return nil, err
	}
	return version, nil
}

func (s *productionDocumentService) ListVersions(
	ctx context.Context,
	documentID string,
) ([]*types.ProductionDocumentVersion, error) {
	tenantID, _, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireProductionSourceID(documentID, "document id"); err != nil {
		return nil, err
	}
	document, err := s.documents.GetDocument(ctx, tenantID, documentID)
	if err != nil {
		return nil, err
	}
	if err := requireProductionDocumentReader(ctx, s.projects, document.ProjectID); err != nil {
		return nil, err
	}
	return s.documents.ListVersions(ctx, tenantID, document.ID)
}

var _ interfaces.ProductionDocumentService = (*productionDocumentService)(nil)
