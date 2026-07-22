package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// CreateProductionDocumentTypeInput contains a complete document-type version
// definition. The service derives CreatedBy from the request context.
type CreateProductionDocumentTypeInput struct {
	Code               string
	Name               string
	Description        string
	SchemaVersion      int
	BlockSchema        types.JSON
	SourceRequirements types.JSON
	SkillBindings      types.JSON
	WorkflowPlan       types.JSON
	QualityRules       types.JSON
	ReviewPolicy       types.JSON
	PublicationPolicy  types.JSON
}

// DeriveProductionDocumentTypeInput contains only the fields a tenant admin
// may edit when deriving a new draft. Identity, lineage, lifecycle state, and
// schema version are assigned by the server.
type DeriveProductionDocumentTypeInput struct {
	Name               string
	Description        string
	BlockSchema        types.JSON
	SourceRequirements types.JSON
	SkillBindings      types.JSON
	WorkflowPlan       types.JSON
	QualityRules       types.JSON
	ReviewPolicy       types.JSON
	PublicationPolicy  types.JSON
}

// ProductionDocumentTypeRepository persists versioned document-type definitions.
type ProductionDocumentTypeRepository interface {
	Create(ctx context.Context, documentType *types.ProductionDocumentType) error
	DeriveDraft(ctx context.Context, tenantID uint64, baseID, actorID string, draft *types.ProductionDocumentType) (*types.ProductionDocumentType, error)
	SeedBuiltins(ctx context.Context, tenantID uint64, actor string, definitions []types.ProductionDocumentType) error
	Activate(ctx context.Context, tenantID uint64, code string, schemaVersion int) (*types.ProductionDocumentType, error)
	GetByID(ctx context.Context, tenantID uint64, documentTypeID string) (*types.ProductionDocumentType, error)
	GetActiveByIDForReview(ctx context.Context, tenantID uint64, documentTypeID string, schemaVersion int) (*types.ProductionDocumentType, error)
	GetActiveByCode(ctx context.Context, tenantID uint64, code string) (*types.ProductionDocumentType, error)
	List(ctx context.Context, tenantID uint64) ([]*types.ProductionDocumentType, error)
}

// ProductionDocumentTypeService is the tenant-scoped document-type lifecycle
// contract. Every operation receives tenantID explicitly.
type ProductionDocumentTypeService interface {
	CreateDocumentType(ctx context.Context, tenantID uint64, input CreateProductionDocumentTypeInput) (*types.ProductionDocumentType, error)
	DeriveDraft(ctx context.Context, tenantID uint64, baseID string, input DeriveProductionDocumentTypeInput) (*types.ProductionDocumentType, error)
	ActivateDocumentType(ctx context.Context, tenantID uint64, code string, schemaVersion int) (*types.ProductionDocumentType, error)
	GetDocumentType(ctx context.Context, tenantID uint64, documentTypeID string) (*types.ProductionDocumentType, error)
	ListDocumentTypes(ctx context.Context, tenantID uint64) ([]*types.ProductionDocumentType, error)
}
