package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

type ProductionReviewRepository interface {
	CreateAnnotation(ctx context.Context, annotation *types.ProductionAnnotation) error
	GetAnnotation(ctx context.Context, tenantID uint64, annotationID string) (*types.ProductionAnnotation, error)
	ResolveAnnotation(ctx context.Context, tenantID uint64, annotationID, actorID string, resolution types.ProductionAnnotationStatus) (bool, error)
	CountOpenBlocking(ctx context.Context, tenantID uint64, versionID string) (int64, error)
	CreateReview(ctx context.Context, request *types.ProductionReviewRequest, steps []*types.ProductionReviewStep) error
	CreateCurrentReview(ctx context.Context, request *types.ProductionReviewRequest, steps []*types.ProductionReviewStep) error
	GetReview(ctx context.Context, tenantID uint64, reviewID string) (*types.ProductionReviewRequest, error)
	DecideStep(ctx context.Context, tenantID uint64, stepID string, from, to types.ProductionReviewDecision, actorID, comment string) (bool, error)
	RejectReviewByTenantAuthority(ctx context.Context, tenantID uint64, reviewID, actorID, reason string) (bool, error)
	CancelReviewByTenantAuthority(ctx context.Context, tenantID uint64, reviewID, actorID, reason string) (bool, error)
	ObsoletePendingByDocument(ctx context.Context, tenantID uint64, documentID, exceptVersionID string) error
}
