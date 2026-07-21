package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

type CreateProductionSourceSetInput struct {
	ProjectID      string
	DocumentTypeID string
	TimeRangeStart *time.Time
	TimeRangeEnd   *time.Time
}

type CreateProductionSourceItemInput struct {
	SourceKind    types.ProductionSourceKind
	SourceSystem  string
	ExternalID    string
	SourceURI     string
	Title         string
	MimeType      string
	ContentDigest string
	CapturedAt    time.Time
	Metadata      types.JSON
}

type CreateEvidenceSnapshotInput struct {
	EvidenceID           string
	SnapshotType         types.ProductionEvidenceSnapshotType
	ResourceReference    string
	InlineContent        types.JSON
	ContentDigest        string
	RedactionMetadata    types.JSON
	CapturedByRunID      string
	CapturedByToolCallID string
}

type ProductionProjectAuthorizer interface {
	RequireProjectRole(ctx context.Context, projectID string, roles ...types.ProductionRole) error
}

type ProductionSourceRepository interface {
	CreateSet(ctx context.Context, sourceSet *types.ProductionSourceSet) error
	ListSets(ctx context.Context, tenantID uint64, projectID string) ([]*types.ProductionSourceSet, error)
	GetSet(ctx context.Context, tenantID uint64, sourceSetID string) (*types.ProductionSourceSet, error)
	CreateItem(ctx context.Context, tenantID uint64, sourceSetID string, item *types.ProductionSourceItem) error
	GetItem(ctx context.Context, tenantID uint64, itemID string) (*types.ProductionSourceItem, *types.ProductionSourceSet, error)
	GetEvidence(ctx context.Context, tenantID uint64, evidenceID string) (*types.ProductionEvidenceSnapshot, *types.ProductionSourceItem, *types.ProductionSourceSet, error)
	ListAcceptedEvidence(ctx context.Context, tenantID uint64, projectID, sourceSetID string) ([]*types.ProductionEvidenceSnapshot, error)
	DecideItem(ctx context.Context, tenantID uint64, itemID string, decision types.ProductionSourceItemStatus) error
	CreateEvidence(ctx context.Context, tenantID uint64, itemID string, evidence *types.ProductionEvidenceSnapshot) error
	Freeze(ctx context.Context, tenantID uint64, sourceSetID string) error
}

type ProductionSourceService interface {
	CreateSet(ctx context.Context, input CreateProductionSourceSetInput) (*types.ProductionSourceSet, error)
	ListSets(ctx context.Context, projectID string) ([]*types.ProductionSourceSet, error)
	AddItem(ctx context.Context, sourceSetID string, input CreateProductionSourceItemInput) (*types.ProductionSourceItem, error)
	DecideItem(ctx context.Context, itemID string, decision types.ProductionSourceItemStatus) error
	AttachEvidence(ctx context.Context, itemID string, evidence CreateEvidenceSnapshotInput) (*types.ProductionEvidenceSnapshot, error)
	GetEvidence(ctx context.Context, evidenceID string) (*types.ProductionEvidenceSnapshot, *types.ProductionSourceItem, *types.ProductionSourceSet, error)
	Freeze(ctx context.Context, sourceSetID string) error
}
