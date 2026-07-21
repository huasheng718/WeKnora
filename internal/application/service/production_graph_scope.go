package service

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

var errProductionGraphDependencies = errors.New("production graph scope dependencies are required")

// SearchKnowledgeGraph exposes the scoped graph query to callers outside the
// service package without allowing them to bypass production visibility.
func (s *knowledgeService) SearchKnowledgeGraph(ctx context.Context, tenantID uint64, knowledgeBaseID string, nodes []string) (*types.GraphData, error) {
	if s == nil {
		return nil, errProductionGraphDependencies
	}
	return SearchActiveProductionGraph(ctx, tenantID, knowledgeBaseID, nodes, s.productionReleaseRepo, s.repo, s.graphEngine)
}

func (s *knowledgeService) SearchExplicitKnowledgeGraph(ctx context.Context, tenantID uint64, knowledgeBaseID, knowledgeID string, nodes []string) (*types.GraphData, error) {
	if s == nil {
		return nil, errProductionGraphDependencies
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
		return nil, errProductionGraphDependencies
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
		return nil, errProductionGraphDependencies
	}
	if releases == nil {
		return searchGraphNamespaces(contextWithGraphOwnerTenant(ctx, tenantID), graphs, []types.NameSpace{{KnowledgeBase: knowledgeBaseID, Knowledge: knowledgeID}}, nodes)
	}
	scopeCtx := contextWithGraphOwnerTenant(ctx, tenantID)
	scopes, err := releases.ResolveScopes(scopeCtx, tenantID, []string{knowledgeBaseID})
	if err != nil {
		return nil, err
	}
	scope := scopes[knowledgeBaseID]
	if intersectsKnowledgeIDs([]string{knowledgeID}, scope.InactiveKnowledgeIDs) {
		return nil, types.ErrProductionProjectionInactive
	}
	return searchGraphNamespaces(scopeCtx, graphs, []types.NameSpace{{KnowledgeBase: knowledgeBaseID, Knowledge: knowledgeID}}, nodes)
}

func activeProductionGraphNamespaces(ctx context.Context, tenantID uint64, knowledgeBaseID string, releases interfaces.ProductionReleaseRepository, knowledges interfaces.KnowledgeRepository) ([]types.NameSpace, error) {
	if releases == nil {
		return []types.NameSpace{{KnowledgeBase: knowledgeBaseID}}, nil
	}
	scopes, err := releases.ResolveScopes(ctx, tenantID, []string{knowledgeBaseID})
	if err != nil {
		return nil, err
	}
	scope := scopes[knowledgeBaseID]
	productionIDs := mergeUniqueKnowledgeIDs(scope.AllProductionKnowledgeIDs, scope.ActiveKnowledgeIDs)
	productionIDs = mergeUniqueKnowledgeIDs(productionIDs, scope.InactiveKnowledgeIDs)
	if len(productionIDs) == 0 {
		return []types.NameSpace{{KnowledgeBase: knowledgeBaseID}}, nil
	}
	if knowledges == nil {
		return nil, errProductionGraphDependencies
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
	merged := &types.GraphData{}
	for _, namespace := range namespaces {
		graph, err := graphs.SearchNode(ctx, namespace, nodes)
		if err != nil {
			return nil, err
		}
		mergeGraphData(merged, graph)
	}
	sortGraphData(merged)
	return merged, nil
}

func mergeGraphData(destination, source *types.GraphData) {
	if source == nil {
		return
	}
	byName := make(map[string]*types.GraphNode, len(destination.Node))
	for _, node := range destination.Node {
		if node != nil {
			byName[node.Name] = node
		}
	}
	for _, node := range source.Node {
		if node == nil {
			continue
		}
		if existing, found := byName[node.Name]; found {
			existing.Chunks = mergeUniqueKnowledgeIDs(existing.Chunks, node.Chunks)
			existing.Attributes = mergeUniqueKnowledgeIDs(existing.Attributes, node.Attributes)
			existing.ProjectionVariants = appendUniqueGraphVariants(existing.ProjectionVariants, node.ProjectionVariants)
			continue
		}
		copyNode := *node
		copyNode.Chunks = append([]string(nil), node.Chunks...)
		copyNode.Attributes = append([]string(nil), node.Attributes...)
		copyNode.ProjectionVariants = append([]*types.GraphNodeVariant(nil), node.ProjectionVariants...)
		destination.Node = append(destination.Node, &copyNode)
		byName[copyNode.Name] = &copyNode
	}
	byRelation := make(map[string]*types.GraphRelation, len(destination.Relation))
	for _, relation := range destination.Relation {
		if relation != nil {
			byRelation[graphRelationKey(relation)] = relation
		}
	}
	for _, relation := range source.Relation {
		if relation == nil {
			continue
		}
		key := graphRelationKey(relation)
		if existing, found := byRelation[key]; found {
			existing.KnowledgeIDs = mergeUniqueKnowledgeIDs(existing.KnowledgeIDs, relation.KnowledgeIDs)
			continue
		}
		copyRelation := *relation
		copyRelation.KnowledgeIDs = append([]string(nil), relation.KnowledgeIDs...)
		destination.Relation = append(destination.Relation, &copyRelation)
		byRelation[key] = &copyRelation
	}
}

func appendUniqueGraphVariants(existing, additions []*types.GraphNodeVariant) []*types.GraphNodeVariant {
	seen := make(map[string]struct{}, len(existing)+len(additions))
	for _, variant := range existing {
		if variant != nil {
			seen[graphVariantKey(variant)] = struct{}{}
		}
	}
	for _, variant := range additions {
		if variant == nil {
			continue
		}
		key := graphVariantKey(variant)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		existing = append(existing, variant)
	}
	return existing
}

func graphRelationKey(relation *types.GraphRelation) string {
	return relation.Node1 + "\x00" + relation.Node2 + "\x00" + relation.Type
}

func graphVariantKey(variant *types.GraphNodeVariant) string {
	return strings.Join(variant.KnowledgeIDs, "\x00") + "\x01" + strings.Join(variant.Chunks, "\x00") + "\x01" + strings.Join(variant.Attributes, "\x00")
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
