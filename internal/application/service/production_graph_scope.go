package service

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

var (
	ErrProductionGraphDependencies    = errors.New("production graph scope dependencies are required")
	ErrProductionGraphScopeIncomplete = errors.New("production graph release scope is incomplete")
)

// SearchKnowledgeGraph exposes the scoped graph query to callers outside the
// service package without allowing them to bypass production visibility.
func (s *knowledgeService) SearchKnowledgeGraph(ctx context.Context, tenantID uint64, knowledgeBaseID string, nodes []string) (*types.GraphData, error) {
	if s == nil {
		return nil, ErrProductionGraphDependencies
	}
	return SearchActiveProductionGraph(ctx, tenantID, knowledgeBaseID, nodes, s.productionReleaseRepo, s.repo, s.graphEngine)
}

func (s *knowledgeService) SearchExplicitKnowledgeGraph(ctx context.Context, tenantID uint64, knowledgeBaseID, knowledgeID string, nodes []string) (*types.GraphData, error) {
	if s == nil {
		return nil, ErrProductionGraphDependencies
	}
	return SearchExplicitProductionGraph(ctx, tenantID, knowledgeBaseID, knowledgeID, nodes, s.productionReleaseRepo, s.graphEngine)
}

// BuildActiveGraphNamespaces returns ordinary knowledge and the active
// production projection as individually scoped graph namespaces. A KB with
// production history must never be searched through its empty namespace.
func BuildActiveGraphNamespaces(knowledgeBaseID string, ordinaryKnowledgeIDs, activeKnowledgeIDs, inactiveKnowledgeIDs []string) []types.NameSpace {
	inactive := make(map[string]struct{}, len(inactiveKnowledgeIDs))
	for _, id := range inactiveKnowledgeIDs {
		if id = strings.TrimSpace(id); id != "" {
			inactive[id] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(ordinaryKnowledgeIDs)+len(activeKnowledgeIDs))
	ids := make([]string, 0, len(ordinaryKnowledgeIDs)+len(activeKnowledgeIDs))
	for _, candidates := range [][]string{ordinaryKnowledgeIDs, activeKnowledgeIDs} {
		for _, id := range candidates {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, excluded := inactive[id]; excluded {
				continue
			}
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	namespaces := make([]types.NameSpace, 0, len(ids))
	for _, id := range ids {
		namespaces = append(namespaces, types.NameSpace{KnowledgeBase: knowledgeBaseID, Knowledge: id})
	}
	return namespaces
}

// SearchActiveProductionGraph searches the graph only through ordinary
// knowledge and active production projections. The release resolver remains
// the single source of production visibility.
func SearchActiveProductionGraph(
	ctx context.Context,
	tenantID uint64,
	knowledgeBaseID string,
	nodes []string,
	releases interfaces.ProductionReleaseRepository,
	knowledges interfaces.KnowledgeRepository,
	graphs interfaces.RetrieveGraphRepository,
) (*types.GraphData, error) {
	if graphs == nil || strings.TrimSpace(knowledgeBaseID) == "" {
		return nil, ErrProductionGraphDependencies
	}
	scopeCtx := contextWithGraphOwnerTenant(ctx, tenantID)
	namespaces, err := activeProductionGraphNamespaces(scopeCtx, tenantID, knowledgeBaseID, releases, knowledges)
	if err != nil {
		return nil, err
	}
	return searchGraphNamespaces(scopeCtx, graphs, namespaces, nodes)
}

// SearchExplicitProductionGraph applies the same visibility contract to a
// caller that already selected one Knowledge document.
func SearchExplicitProductionGraph(
	ctx context.Context,
	tenantID uint64,
	knowledgeBaseID, knowledgeID string,
	nodes []string,
	releases interfaces.ProductionReleaseRepository,
	graphs interfaces.RetrieveGraphRepository,
) (*types.GraphData, error) {
	if graphs == nil || strings.TrimSpace(knowledgeBaseID) == "" || strings.TrimSpace(knowledgeID) == "" {
		return nil, ErrProductionGraphDependencies
	}
	if releases == nil {
		return nil, ErrProductionGraphDependencies
	}
	scopeCtx := contextWithGraphOwnerTenant(ctx, tenantID)
	scopes, err := releases.ResolveScopes(scopeCtx, tenantID, []string{knowledgeBaseID})
	if err != nil {
		return nil, err
	}
	scope, found := scopes[knowledgeBaseID]
	if !found {
		return nil, ErrProductionGraphScopeIncomplete
	}
	if intersectsKnowledgeIDs([]string{knowledgeID}, scope.InactiveKnowledgeIDs) {
		return nil, types.ErrProductionProjectionInactive
	}
	return searchGraphNamespaces(scopeCtx, graphs, []types.NameSpace{{KnowledgeBase: knowledgeBaseID, Knowledge: knowledgeID}}, nodes)
}

func activeProductionGraphNamespaces(ctx context.Context, tenantID uint64, knowledgeBaseID string, releases interfaces.ProductionReleaseRepository, knowledges interfaces.KnowledgeRepository) ([]types.NameSpace, error) {
	if releases == nil {
		return nil, ErrProductionGraphDependencies
	}
	scopes, err := releases.ResolveScopes(ctx, tenantID, []string{knowledgeBaseID})
	if err != nil {
		return nil, err
	}
	scope, found := scopes[knowledgeBaseID]
	if !found {
		return nil, ErrProductionGraphScopeIncomplete
	}
	productionIDs := mergeUniqueKnowledgeIDs(scope.AllProductionKnowledgeIDs, scope.ActiveKnowledgeIDs)
	productionIDs = mergeUniqueKnowledgeIDs(productionIDs, scope.InactiveKnowledgeIDs)
	if len(productionIDs) == 0 {
		return []types.NameSpace{{KnowledgeBase: knowledgeBaseID}}, nil
	}
	if knowledges == nil {
		return nil, ErrProductionGraphDependencies
	}
	rows, err := knowledges.ListKnowledgeByKnowledgeBaseID(ctx, tenantID, knowledgeBaseID)
	if err != nil {
		return nil, err
	}
	production := make(map[string]struct{}, len(productionIDs))
	for _, id := range productionIDs {
		production[id] = struct{}{}
	}
	ordinary := make([]string, 0, len(rows))
	for _, knowledge := range rows {
		if knowledge == nil || knowledge.ID == "" {
			continue
		}
		if _, isProduction := production[knowledge.ID]; !isProduction {
			ordinary = append(ordinary, knowledge.ID)
		}
	}
	return BuildActiveGraphNamespaces(knowledgeBaseID, ordinary, scope.ActiveKnowledgeIDs, scope.InactiveKnowledgeIDs), nil
}

func contextWithGraphOwnerTenant(ctx context.Context, tenantID uint64) context.Context {
	if current, ok := types.TenantIDFromContext(ctx); ok && current == tenantID {
		return ctx
	}
	return context.WithValue(ctx, types.TenantIDContextKey, tenantID)
}

func searchGraphNamespaces(ctx context.Context, graphs interfaces.RetrieveGraphRepository, namespaces []types.NameSpace, nodes []string) (*types.GraphData, error) {
	merged, err := graphs.SearchNodeInNamespaces(ctx, namespaces, nodes)
	if err != nil {
		return nil, err
	}
	if merged == nil {
		merged = &types.GraphData{}
	}
	sortGraphData(merged)
	return merged, nil
}

func graphRelationKey(relation *types.GraphRelation) string {
	return relation.Node1 + "\x00" + relation.Node2 + "\x00" + relation.Type
}

func sortGraphData(graph *types.GraphData) {
	sort.SliceStable(graph.Node, func(i, j int) bool { return graph.Node[i].Name < graph.Node[j].Name })
	sort.SliceStable(graph.Relation, func(i, j int) bool { return graphRelationKey(graph.Relation[i]) < graphRelationKey(graph.Relation[j]) })
	for _, node := range graph.Node {
		if node == nil {
			continue
		}
		sort.Strings(node.Chunks)
		sort.Strings(node.Attributes)
	}
	for _, relation := range graph.Relation {
		if relation != nil {
			sort.Strings(relation.KnowledgeIDs)
		}
	}
}
