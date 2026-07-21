package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type projectionListKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	rows       []*types.Knowledge
	lastFilter types.KnowledgeListFilter
}

func (r *projectionListKnowledgeRepo) ListPagedKnowledgeByKnowledgeBaseID(_ context.Context, _ uint64, _ string, _ *types.Pagination, filter types.KnowledgeListFilter) ([]*types.Knowledge, int64, error) {
	r.lastFilter = filter
	excluded := make(map[string]struct{}, len(filter.ExcludeKnowledgeIDs))
	for _, id := range filter.ExcludeKnowledgeIDs {
		excluded[id] = struct{}{}
	}
	result := make([]*types.Knowledge, 0, len(r.rows))
	for _, row := range r.rows {
		if _, ok := excluded[row.ID]; !ok {
			result = append(result, row)
		}
	}
	return result, int64(len(result)), nil
}

func (r *projectionListKnowledgeRepo) GetKnowledgeTags(context.Context, []string) (map[string][]*types.KnowledgeTag, error) {
	return nil, nil
}

func TestKnowledgeListOmitsInactiveProductionProjectionsAndMarksActiveResponse(t *testing.T) {
	repo := &projectionListKnowledgeRepo{rows: []*types.Knowledge{
		{ID: "knowledge-active", Source: "manual"},
		{ID: "knowledge-old", Source: "manual"},
		{ID: "knowledge-building", Source: "manual"},
	}}
	svc := &knowledgeService{
		repo: repo,
		productionReleaseRepo: &projectionResolverReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{
			"kb-1": {ActiveKnowledgeIDs: []string{"knowledge-active"}, InactiveKnowledgeIDs: []string{"knowledge-old", "knowledge-building"}},
		}},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))

	result, err := svc.ListPagedKnowledgeByKnowledgeBaseID(ctx, "kb-1", &types.Pagination{Page: 1, PageSize: 20}, types.KnowledgeListFilter{})
	require.NoError(t, err)
	rows := result.Data.([]*types.Knowledge)
	require.Equal(t, []string{"knowledge-active"}, projectionKnowledgeIDs(rows))
	require.ElementsMatch(t, []string{"knowledge-old", "knowledge-building"}, repo.lastFilter.ExcludeKnowledgeIDs)
	require.Equal(t, "production", rows[0].Source)
	require.True(t, rows[0].ReadOnly)
	require.Equal(t, "manual", repo.rows[0].Source)
	require.False(t, repo.rows[0].ReadOnly)
}

func projectionKnowledgeIDs(rows []*types.Knowledge) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}
