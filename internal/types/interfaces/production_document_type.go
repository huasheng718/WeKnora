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
	QualityRules       types.JSON
	ReviewPolicy       types.JSON
	PublicationPolicy  types.JSON
}

// ProductionDocumentTypeRepository persists versioned document-type definitions.
type ProductionDocumentTypeRepository interface {
	Create(ctx context.Context, documentType *types.ProductionDocumentType) error
	Activate(ctx context.Context, tenantID uint64, code string, schemaVersion int) (*types.ProductionDocumentType, error)
	GetByID(ctx context.Context, tenantID uint64, documentTypeID string) (*types.ProductionDocumentType, error)
	GetActiveByCode(ctx context.Context, tenantID uint64, code string) (*types.ProductionDocumentType, error)
	List(ctx context.Context, tenantID uint64) ([]*types.ProductionDocumentType, error)
}

// ProductionDocumentTypeService is the tenant-scoped document-type lifecycle
// contract. Every operation receives tenantID explicitly.
type ProductionDocumentTypeService interface {
	CreateDocumentType(ctx context.Context, tenantID uint64, input CreateProductionDocumentTypeInput) (*types.ProductionDocumentType, error)
	ActivateDocumentType(ctx context.Context, tenantID uint64, code string, schemaVersion int) (*types.ProductionDocumentType, error)
	GetDocumentType(ctx context.Context, tenantID uint64, documentTypeID string) (*types.ProductionDocumentType, error)
	ListDocumentTypes(ctx context.Context, tenantID uint64) ([]*types.ProductionDocumentType, error)
}
