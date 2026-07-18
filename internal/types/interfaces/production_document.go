package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

type CreateProductionDocumentInput struct {
	ProjectID      string
	DocumentTypeID string
	SourceSetID    string
	Title          string
}

type AppendProductionVersionInput struct {
	ParentVersionID string
	SourceSetID     string
	Origin          types.ProductionDocumentOrigin
	ChangeSummary   string
	Blocks          []types.ProductionDocumentBlockInput
	Lineage         []types.ProductionBlockLineageInput
}

type ProductionDocumentRepository interface {
	CreateDocument(ctx context.Context, document *types.ProductionDocument) error
	GetDocument(ctx context.Context, tenantID uint64, id string) (*types.ProductionDocument, error)
	AppendVersion(
		ctx context.Context,
		version *types.ProductionDocumentVersion,
		blocks []*types.ProductionDocumentBlock,
		lineage []*types.ProductionBlockLineage,
	) error
	GetVersion(ctx context.Context, tenantID uint64, versionID string) (*types.ProductionDocumentVersion, error)
	ListVersions(ctx context.Context, tenantID uint64, documentID string) ([]*types.ProductionDocumentVersion, error)
}

type ProductionDocumentService interface {
	CreateDocument(ctx context.Context, input CreateProductionDocumentInput) (*types.ProductionDocument, error)
	AppendVersion(ctx context.Context, documentID string, input AppendProductionVersionInput) (*types.ProductionDocumentVersion, error)
	GetVersion(ctx context.Context, versionID string) (*types.ProductionDocumentVersion, error)
	ListVersions(ctx context.Context, documentID string) ([]*types.ProductionDocumentVersion, error)
}
