package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func TestGraphScopeOmitsInactiveProjectionNamespace(t *testing.T) {
	scope := BuildActiveGraphNamespaces("kb-1", []string{"ordinary"}, []string{"prod-active"}, []string{"prod-old", "prod-building"})
	require.Contains(t, scope, types.NameSpace{KnowledgeBase: "kb-1", Knowledge: "ordinary"})
	require.Contains(t, scope, types.NameSpace{KnowledgeBase: "kb-1", Knowledge: "prod-active"})
	require.NotContains(t, scope, types.NameSpace{KnowledgeBase: "kb-1", Knowledge: "prod-old"})
	require.NotContains(t, scope, types.NameSpace{KnowledgeBase: "kb-1", Knowledge: "prod-building"})
	require.NotContains(t, scope, types.NameSpace{KnowledgeBase: "kb-1"})
}

type graphScopeKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	rows           []*types.Knowledge
	tenantCalls    []uint64
	contextTenants []uint64
	err            error
}

func (r *graphScopeKnowledgeRepo) ListKnowledgeByKnowledgeBaseID(ctx context.Context, tenantID uint64, _ string) ([]*types.Knowledge, error) {
	r.tenantCalls = append(r.tenantCalls, tenantID)
	r.contextTenants = append(r.contextTenants, types.MustTenantIDFromContext(ctx))
	if r.err != nil {
		return nil, r.err
	}
	return r.rows, nil
}

type graphScopeReleaseRepo struct {
	interfaces.ProductionReleaseRepository
	scopes         map[string]types.ProductionKnowledgeScope
	tenantCalls    []uint64
	contextTenants []uint64
	err            error
	omitMissing    bool
}

func (r *graphScopeReleaseRepo) ResolveScopes(ctx context.Context, tenantID uint64, kbIDs []string) (map[string]types.ProductionKnowledgeScope, error) {
	r.tenantCalls = append(r.tenantCalls, tenantID)
	r.contextTenants = append(r.contextTenants, types.MustTenantIDFromContext(ctx))
	if r.err != nil {
		return nil, r.err
	}
	result := make(map[string]types.ProductionKnowledgeScope, len(kbIDs))
	for _, kbID := range kbIDs {
		scope, found := r.scopes[kbID]
		if !found && r.omitMissing {
			continue
		}
		result[kbID] = scope
	}
	return result, nil
}

type graphScopeGraphRepo struct {
	interfaces.RetrieveGraphRepository
	mu         sync.Mutex
	namespaces []types.NameSpace
	bulkCalls  int
	bulkScopes []types.NameSpace
	graphs     map[string]*types.GraphData
	err        error
}

func (r *graphScopeGraphRepo) SearchNodeInNamespaces(_ context.Context, namespaces []types.NameSpace, _ []string) (*types.GraphData, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bulkCalls++
	r.bulkScopes = append(r.bulkScopes, namespaces...)
	if r.err != nil {
		return nil, r.err
	}
	merged := &types.GraphData{}
	for _, namespace := range namespaces {
		merged = types.MergeGraphData(merged, r.graphs[namespace.Knowledge])
	}
	return merged, nil
}

func (r *graphScopeGraphRepo) SearchNode(_ context.Context, namespace types.NameSpace, _ []string) (*types.GraphData, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.namespaces = append(r.namespaces, namespace)
	if r.err != nil {
		return nil, r.err
	}
	return r.graphs[namespace.Knowledge], nil
}

func TestSearchKnowledgeGraphScopesProductionAndDeduplicatesDeterministically(t *testing.T) {
	knowledgeRepo := &graphScopeKnowledgeRepo{rows: []*types.Knowledge{
		{ID: "ordinary", KnowledgeBaseID: "kb-1", TenantID: 7},
		{ID: "prod-active", KnowledgeBaseID: "kb-1", TenantID: 7},
		{ID: "prod-old", KnowledgeBaseID: "kb-1", TenantID: 7},
		{ID: "prod-building", KnowledgeBaseID: "kb-1", TenantID: 7},
	}}
	releases := &graphScopeReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{
		"kb-1": {ActiveKnowledgeIDs: []string{"prod-active"}, InactiveKnowledgeIDs: []string{"prod-old", "prod-building"}, AllProductionKnowledgeIDs: []string{"prod-active", "prod-old", "prod-building"}},
	}}
	graphRepo := &graphScopeGraphRepo{graphs: map[string]*types.GraphData{
		"ordinary":    {Node: []*types.GraphNode{{Name: "common", Chunks: []string{"ordinary-chunk"}}, {Name: "ordinary"}}, Relation: []*types.GraphRelation{{Node1: "common", Node2: "ordinary", Type: "rel"}}},
		"prod-active": {Node: []*types.GraphNode{{Name: "common", Chunks: []string{"active-chunk"}}, {Name: "active"}}, Relation: []*types.GraphRelation{{Node1: "common", Node2: "active", Type: "rel"}, {Node1: "common", Node2: "ordinary", Type: "rel"}}},
	}}

	graph, err := SearchActiveProductionGraph(context.Background(), 7, "kb-1", []string{"common"}, releases, knowledgeRepo, graphRepo)
	require.NoError(t, err)
	require.Equal(t, 1, graphRepo.bulkCalls)
	require.Equal(t, []types.NameSpace{{KnowledgeBase: "kb-1", Knowledge: "ordinary"}, {KnowledgeBase: "kb-1", Knowledge: "prod-active"}}, graphRepo.bulkScopes)
	require.Equal(t, []string{"active", "common", "ordinary"}, graphNodeNames(graph.Node))
	require.Equal(t, []string{"active-chunk", "ordinary-chunk"}, graph.Node[1].Chunks)
	require.Len(t, graph.Relation, 2)
}

func TestSearchKnowledgeGraphUsesOneBulkRepositoryCallForHighNamespaceCount(t *testing.T) {
	const knowledgeCount = 1000
	rows := make([]*types.Knowledge, 0, knowledgeCount)
	active := make([]string, 0, knowledgeCount/2)
	for index := range knowledgeCount {
		id := fmt.Sprintf("knowledge-%04d", index)
		rows = append(rows, &types.Knowledge{ID: id, KnowledgeBaseID: "kb-high", TenantID: 7})
		if index%2 == 0 {
			active = append(active, id)
		}
	}
	graphRepo := &graphScopeGraphRepo{graphs: map[string]*types.GraphData{}}
	_, err := SearchActiveProductionGraph(
		context.Background(), 7, "kb-high", []string{"term"},
		&graphScopeReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{
			"kb-high": {ActiveKnowledgeIDs: active, AllProductionKnowledgeIDs: active},
		}},
		&graphScopeKnowledgeRepo{rows: rows}, graphRepo,
	)
	require.NoError(t, err)
	require.Equal(t, 1, graphRepo.bulkCalls)
	require.Empty(t, graphRepo.namespaces, "bulk scope must replace serial per-namespace calls")
	require.Len(t, graphRepo.bulkScopes, knowledgeCount)
}

func TestSearchKnowledgeGraphUsesUnscopedNamespaceForOrdinaryKnowledgeBase(t *testing.T) {
	knowledgeRepo := &graphScopeKnowledgeRepo{rows: []*types.Knowledge{{ID: "ordinary", KnowledgeBaseID: "kb-1", TenantID: 7}}}
	graphRepo := &graphScopeGraphRepo{graphs: map[string]*types.GraphData{"": {Node: []*types.GraphNode{{Name: "ordinary"}}}}}

	graph, err := SearchActiveProductionGraph(context.Background(), 7, "kb-1", []string{"ordinary"}, &graphScopeReleaseRepo{}, knowledgeRepo, graphRepo)
	require.NoError(t, err)
	require.Len(t, graph.Node, 1)
	require.Equal(t, 1, graphRepo.bulkCalls)
	require.Equal(t, []types.NameSpace{{KnowledgeBase: "kb-1"}}, graphRepo.bulkScopes)
}

func TestSearchKnowledgeGraphFailsClosedForInactiveExplicitKnowledge(t *testing.T) {
	releases := &graphScopeReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{
		"kb-1": {InactiveKnowledgeIDs: []string{"prod-old"}, AllProductionKnowledgeIDs: []string{"prod-old"}},
	}}
	_, err := SearchExplicitProductionGraph(context.Background(), 7, "kb-1", "prod-old", []string{"term"}, releases, &graphScopeGraphRepo{})
	require.ErrorIs(t, err, types.ErrProductionProjectionInactive)
}

func TestSearchKnowledgeGraphFailsClosedWhenReleaseScopeIsMissing(t *testing.T) {
	_, err := SearchActiveProductionGraph(context.Background(), 7, "kb-1", []string{"term"}, &graphScopeReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{}, omitMissing: true}, &graphScopeKnowledgeRepo{}, &graphScopeGraphRepo{})
	require.ErrorIs(t, err, ErrProductionGraphScopeIncomplete)

	_, err = SearchExplicitProductionGraph(context.Background(), 7, "kb-1", "ordinary", []string{"term"}, &graphScopeReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{}, omitMissing: true}, &graphScopeGraphRepo{})
	require.ErrorIs(t, err, ErrProductionGraphScopeIncomplete)
}

func TestMergeGraphDataPreservesProjectionVariantsAcrossKnowledgeScopes(t *testing.T) {
	left := &types.GraphData{Node: []*types.GraphNode{{
		Name: "shared", Chunks: []string{"chunk-one"}, Attributes: []string{"one"},
		ProjectionVariants: []*types.GraphNodeVariant{{KnowledgeIDs: []string{"knowledge-one"}, Chunks: []string{"chunk-one"}, Attributes: []string{"one"}}},
	}}, Relation: []*types.GraphRelation{{Node1: "shared", Node2: "peer", Type: "related", KnowledgeIDs: []string{"knowledge-one"}}}}
	right := &types.GraphData{Node: []*types.GraphNode{{
		Name: "shared", Chunks: []string{"chunk-two"}, Attributes: []string{"two"},
		ProjectionVariants: []*types.GraphNodeVariant{{KnowledgeIDs: []string{"knowledge-two"}, Chunks: []string{"chunk-two"}, Attributes: []string{"two"}}},
	}}, Relation: []*types.GraphRelation{{Node1: "shared", Node2: "peer", Type: "related", KnowledgeIDs: []string{"knowledge-two"}}}}

	merged := types.MergeGraphData(nil, left)
	merged = types.MergeGraphData(merged, right)
	require.Len(t, merged.Node, 1)
	require.ElementsMatch(t, []string{"chunk-one", "chunk-two"}, merged.Node[0].Chunks)
	require.Len(t, merged.Node[0].ProjectionVariants, 2)
	require.Equal(t, []string{"knowledge-one"}, merged.Node[0].ProjectionVariants[0].KnowledgeIDs)
	require.Len(t, merged.Relation, 1)
	require.ElementsMatch(t, []string{"knowledge-one", "knowledge-two"}, merged.Relation[0].KnowledgeIDs)
	encoded, err := json.Marshal(merged)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "projection_variants")
}

func TestSearchKnowledgeGraphUsesOwnerTenantAndPropagatesRepositoryErrors(t *testing.T) {
	errExpected := errors.New("neo4j unavailable")
	knowledgeRepo := &graphScopeKnowledgeRepo{rows: []*types.Knowledge{{ID: "ordinary", KnowledgeBaseID: "kb-shared", TenantID: 200}}}
	releases := &graphScopeReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{
		"kb-shared": {InactiveKnowledgeIDs: []string{"prod-old"}, AllProductionKnowledgeIDs: []string{"prod-old"}},
	}}
	graphRepo := &graphScopeGraphRepo{err: errExpected}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))

	_, err := SearchActiveProductionGraph(ctx, 200, "kb-shared", []string{"term"}, releases, knowledgeRepo, graphRepo)
	require.ErrorIs(t, err, errExpected)
	require.Equal(t, []uint64{200}, releases.tenantCalls)
	require.Equal(t, []uint64{200}, releases.contextTenants)
	require.Equal(t, []uint64{200}, knowledgeRepo.tenantCalls)
	require.Equal(t, []uint64{200}, knowledgeRepo.contextTenants)
}

func TestSearchKnowledgeGraphUsesSharedOwnerScopeForActiveProjectionNamespaces(t *testing.T) {
	knowledgeRepo := &graphScopeKnowledgeRepo{rows: []*types.Knowledge{
		{ID: "ordinary", TenantID: 200, KnowledgeBaseID: "kb-shared"},
		{ID: "prod-active", TenantID: 200, KnowledgeBaseID: "kb-shared"},
		{ID: "prod-old", TenantID: 200, KnowledgeBaseID: "kb-shared"},
		{ID: "prod-building", TenantID: 200, KnowledgeBaseID: "kb-shared"},
	}}
	releases := &graphScopeReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{
		"kb-shared": {ActiveKnowledgeIDs: []string{"prod-active"}, InactiveKnowledgeIDs: []string{"prod-old", "prod-building"}, AllProductionKnowledgeIDs: []string{"prod-active", "prod-old", "prod-building"}},
	}}
	graphRepo := &graphScopeGraphRepo{graphs: map[string]*types.GraphData{"ordinary": {}, "prod-active": {}}}
	callerCtx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))

	_, err := SearchActiveProductionGraph(callerCtx, 200, "kb-shared", []string{"term"}, releases, knowledgeRepo, graphRepo)
	require.NoError(t, err)
	require.Equal(t, 1, graphRepo.bulkCalls)
	require.Equal(t, []types.NameSpace{{KnowledgeBase: "kb-shared", Knowledge: "ordinary"}, {KnowledgeBase: "kb-shared", Knowledge: "prod-active"}}, graphRepo.bulkScopes)
	require.Equal(t, []uint64{200}, releases.contextTenants)
	require.Equal(t, []uint64{200}, knowledgeRepo.contextTenants)
}

func graphNodeNames(nodes []*types.GraphNode) []string {
	result := make([]string, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, node.Name)
	}
	return result
}
