package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

type ProductionReleaseRepository interface {
	CreateRelease(ctx context.Context, release *types.ProductionRelease, targets []*types.ProductionReleaseTarget) error
	GetRelease(ctx context.Context, tenantID uint64, releaseID string) (*types.ProductionRelease, error)
	GetTarget(ctx context.Context, tenantID uint64, targetID string) (*types.ProductionReleaseTarget, error)
	TransitionTarget(ctx context.Context, targetID string, from, to types.ProductionReleaseTargetStatus, patch types.JSONMap) (bool, error)
	ResolveScopes(ctx context.Context, tenantID uint64, kbIDs []string) (map[string]types.ProductionKnowledgeScope, error)
	ResolveScopesForKnowledgeIDs(ctx context.Context, tenantID uint64, knowledgeIDs []string) (map[string]types.ProductionKnowledgeScope, error)
	SwitchHead(ctx context.Context, tenantID uint64, documentID, kbID, targetID string, expectedLock int) (*types.ProductionProjectionHead, error)
	ListProjectionHistory(ctx context.Context, tenantID uint64, documentID, kbID string) ([]*types.ProductionReleaseTarget, error)
	ListBuildingTargetsWithFailedKnowledge(ctx context.Context, tenantID uint64, limit int) ([]*types.ProductionReleaseTarget, error)
}
