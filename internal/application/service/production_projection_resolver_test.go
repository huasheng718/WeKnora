package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type projectionResolverReleaseRepo struct {
	interfaces.ProductionReleaseRepository
	scopes map[string]types.ProductionKnowledgeScope
}

func (r *projectionResolverReleaseRepo) ResolveScopes(_ context.Context, _ uint64, kbIDs []string) (map[string]types.ProductionKnowledgeScope, error) {
	if kbIDs == nil {
		return r.scopes, nil
	}
	result := make(map[string]types.ProductionKnowledgeScope, len(kbIDs))
	for _, kbID := range kbIDs {
		result[kbID] = r.scopes[kbID]
	}
	return result, nil
}

func TestProjectionResolverExcludesInactiveProductionKnowledge(t *testing.T) {
	resolver := newProductionProjectionResolver(&projectionResolverReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{
		"kb-1": {ActiveKnowledgeIDs: []string{"knowledge-active"}, InactiveKnowledgeIDs: []string{"knowledge-old", "knowledge-building"}},
	}})
	target := &types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-1"}

	require.NoError(t, resolver.Apply(context.Background(), 7, []*types.SearchTarget{target}))
	require.ElementsMatch(t, []string{"knowledge-old", "knowledge-building"}, target.ExcludeKnowledgeIDs)
}

func TestProjectionResolverRejectsExplicitInactiveKnowledge(t *testing.T) {
	resolver := newProductionProjectionResolver(&projectionResolverReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{
		"kb-1": {InactiveKnowledgeIDs: []string{"knowledge-old"}},
	}})

	err := resolver.AuthorizeExplicitKnowledgeIDs(context.Background(), 7, []string{"knowledge-old"})
	require.ErrorIs(t, err, types.ErrProductionProjectionInactive)
}
