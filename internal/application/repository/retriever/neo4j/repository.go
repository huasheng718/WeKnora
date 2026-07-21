package neo4j

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

// Neo4jRepository is a repository for Neo4j
type Neo4jRepository struct {
	driver     neo4j.Driver
	nodePrefix string
}

const relationshipImportQuery = `
	UNWIND $data AS row
	CALL apoc.merge.node(row.source_labels, {name: row.source, kg: row.knowledge_id}, {}, {}) YIELD node as source
	CALL apoc.merge.node(row.target_labels, {name: row.target, kg: row.knowledge_id}, {}, {}) YIELD node as target
	CALL apoc.merge.relationship(source, row.type, {knowledge_id: row.knowledge_id}, {kg: row.kg}, target) YIELD rel
	RETURN distinct 'done'
`

func relationshipImportRows(namespace types.NameSpace, labels []string, relations []*types.GraphRelation) []map[string]interface{} {
	if strings.TrimSpace(namespace.Knowledge) == "" {
		return nil
	}
	rows := make([]map[string]interface{}, 0, len(relations))
	for _, rel := range relations {
		if rel == nil {
			continue
		}
		rows = append(rows, map[string]interface{}{
			"source": rel.Node1, "target": rel.Node2, "knowledge_id": namespace.Knowledge,
			"kg": []string{namespace.Knowledge}, "type": rel.Type,
			"source_labels": labels, "target_labels": labels,
		})
	}
	return rows
}

// NewNeo4jRepository creates a new Neo4j repository
func NewNeo4jRepository(driver neo4j.Driver) interfaces.RetrieveGraphRepository {
	return &Neo4jRepository{driver: driver, nodePrefix: "ENTITY"}
}

// _remove_hyphen removes hyphens from a string
func _remove_hyphen(s string) string {
	return strings.ReplaceAll(s, "-", "_")
}

// Labels returns the labels for a namespace
func (n *Neo4jRepository) Labels(namespace types.NameSpace) []string {
	res := make([]string, 0)
	for _, label := range namespace.Labels() {
		res = append(res, n.nodePrefix+_remove_hyphen(label))
	}
	return res
}

// Label returns the label for a namespace
func (n *Neo4jRepository) Label(namespace types.NameSpace) string {
	labels := n.Labels(namespace)
	return strings.Join(labels, ":")
}

// AddGraph adds a graph to the Neo4j repository
func (n *Neo4jRepository) AddGraph(ctx context.Context, namespace types.NameSpace, graphs []*types.GraphData) error {
	if n.driver == nil {
		logger.Warnf(ctx, "NOT SUPPORT RETRIEVE GRAPH")
		return nil
	}
	for _, graph := range graphs {
		if err := n.addGraph(ctx, namespace, graph); err != nil {
			return err
		}
	}
	return nil
}

// addGraph adds a graph to the Neo4j repository
func (n *Neo4jRepository) addGraph(ctx context.Context, namespace types.NameSpace, graph *types.GraphData) error {
	session := n.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		// Node import query
		node_import_query := `
			UNWIND $data AS row
			CALL apoc.merge.node(row.labels, {name: row.name, kg: row.knowledge_id}, row.props, {}) YIELD node
			SET node.chunks = apoc.coll.union(node.chunks, row.chunks)
			RETURN distinct 'done' AS result
		`
		nodeData := []map[string]interface{}{}
		for _, node := range graph.Node {
			nodeData = append(nodeData, map[string]interface{}{
				"name":         node.Name,
				"knowledge_id": namespace.Knowledge,
				"props":        map[string][]string{"attributes": node.Attributes},
				"chunks":       node.Chunks,
				"labels":       n.Labels(namespace),
			})
		}
		if _, err := tx.Run(ctx, node_import_query, map[string]interface{}{"data": nodeData}); err != nil {
			return nil, fmt.Errorf("failed to create nodes: %v", err)
		}

		// Relationship import query
		relData := relationshipImportRows(namespace, n.Labels(namespace), graph.Relation)
		if _, err := tx.Run(ctx, relationshipImportQuery, map[string]interface{}{"data": relData}); err != nil {
			return nil, fmt.Errorf("failed to create relationships: %v", err)
		}
		return nil, nil
	})
	if err != nil {
		logger.Errorf(ctx, "failed to add graph: %v", err)
		return err
	}
	return nil
}

// DelGraph deletes a graph from the Neo4j repository
func (n *Neo4jRepository) DelGraph(ctx context.Context, namespaces []types.NameSpace) error {
	if n.driver == nil {
		logger.Warnf(ctx, "NOT SUPPORT RETRIEVE GRAPH")
		return nil
	}
	session := n.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	result, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		for _, namespace := range namespaces {
			labelExpr := n.Label(namespace)

			deleteRelsQuery := `
				CALL apoc.periodic.iterate(
					"MATCH (n:` + labelExpr + ` {kg: $knowledge_id})-[r]-(m:` + labelExpr + ` {kg: $knowledge_id}) RETURN r",
					"DELETE r",
					{batchSize: 1000, parallel: true, params: {knowledge_id: $knowledge_id}}
				) YIELD batches, total
				RETURN total
        	`
			if _, err := tx.Run(ctx, deleteRelsQuery, map[string]interface{}{"knowledge_id": namespace.Knowledge}); err != nil {
				return nil, fmt.Errorf("failed to delete relationships: %v", err)
			}

			deleteNodesQuery := `
				CALL apoc.periodic.iterate(
					"MATCH (n:` + labelExpr + ` {kg: $knowledge_id}) RETURN n",
					"DELETE n",
					{batchSize: 1000, parallel: true, params: {knowledge_id: $knowledge_id}}
				) YIELD batches, total
				RETURN total
        	`
			if _, err := tx.Run(ctx, deleteNodesQuery, map[string]interface{}{"knowledge_id": namespace.Knowledge}); err != nil {
				return nil, fmt.Errorf("failed to delete nodes: %v", err)
			}
		}
		return nil, nil
	})
	if err != nil {
		return err
	}
	logger.Infof(ctx, "delete graph result: %v", result)
	return nil
}

type graphSearchRow struct {
	Source             *types.GraphNode
	SourceKnowledgeIDs []string
	Target             *types.GraphNode
	TargetKnowledgeIDs []string
	Relation           *types.GraphRelation
}

func graphSearchInNamespacesQuery(labelExpr string) string {
	return `
		MATCH (n:` + labelExpr + `)-[r]-(m:` + labelExpr + `)
		WHERE ANY(nodeText IN $nodes WHERE n.name CONTAINS nodeText)
		  AND ($allow_all OR (n.kg IN $knowledge_ids AND m.kg IN $knowledge_ids
		       AND ANY(knowledgeID IN coalesce(r.kg, []) WHERE knowledgeID IN $knowledge_ids)))
		RETURN n, r, m
	`
}

func graphDataFromSearchRows(rows []graphSearchRow) *types.GraphData {
	graph := &types.GraphData{}
	nodesByName := make(map[string]*types.GraphNode)
	variantsByNode := make(map[string]map[string]*types.GraphNodeVariant)
	mergeNode := func(node *types.GraphNode, knowledgeIDs []string) {
		if node == nil || node.Name == "" {
			return
		}
		existing := nodesByName[node.Name]
		if existing == nil {
			copy := *node
			copy.Chunks = appendUniqueGraphStrings(nil, node.Chunks)
			copy.Attributes = appendUniqueGraphStrings(nil, node.Attributes)
			copy.ProjectionVariants = nil
			nodesByName[node.Name] = &copy
			graph.Node = append(graph.Node, &copy)
			existing = &copy
		} else {
			existing.Chunks = appendUniqueGraphStrings(existing.Chunks, node.Chunks)
			existing.Attributes = appendUniqueGraphStrings(existing.Attributes, node.Attributes)
		}
		if variantsByNode[node.Name] == nil {
			variantsByNode[node.Name] = make(map[string]*types.GraphNodeVariant)
		}
		variantKey := strings.Join(knowledgeIDs, "\x00")
		variant := variantsByNode[node.Name][variantKey]
		if variant == nil {
			variant = &types.GraphNodeVariant{
				KnowledgeIDs: append([]string(nil), knowledgeIDs...),
				Chunks:       appendUniqueGraphStrings(nil, node.Chunks),
				Attributes:   appendUniqueGraphStrings(nil, node.Attributes),
			}
			variantsByNode[node.Name][variantKey] = variant
			existing.ProjectionVariants = append(existing.ProjectionVariants, variant)
			return
		}
		variant.Chunks = appendUniqueGraphStrings(variant.Chunks, node.Chunks)
		variant.Attributes = appendUniqueGraphStrings(variant.Attributes, node.Attributes)
	}
	for _, row := range rows {
		mergeNode(row.Source, row.SourceKnowledgeIDs)
		mergeNode(row.Target, row.TargetKnowledgeIDs)
		if row.Relation != nil {
			copy := *row.Relation
			copy.KnowledgeIDs = append([]string(nil), row.Relation.KnowledgeIDs...)
			graph.Relation = append(graph.Relation, &copy)
		}
	}
	return graph
}

func appendUniqueGraphStrings(existing, additions []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(additions))
	for _, value := range existing {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		existing = append(existing, value)
	}
	return existing
}

// SearchNode searches for nodes in the Neo4j repository
func (n *Neo4jRepository) SearchNode(
	ctx context.Context,
	namespace types.NameSpace,
	nodes []string,
) (*types.GraphData, error) {
	return n.SearchNodeInNamespaces(ctx, []types.NameSpace{namespace}, nodes)
}

func (n *Neo4jRepository) SearchNodeInNamespaces(
	ctx context.Context,
	namespaces []types.NameSpace,
	nodes []string,
) (*types.GraphData, error) {
	if n.driver == nil {
		logger.Warnf(ctx, "NOT SUPPORT RETRIEVE GRAPH")
		return nil, nil
	}
	if len(namespaces) == 0 {
		return &types.GraphData{}, nil
	}
	knowledgeBaseID := strings.TrimSpace(namespaces[0].KnowledgeBase)
	if knowledgeBaseID == "" {
		return nil, errors.New("graph namespaces require one knowledge base")
	}
	allowAll := false
	seen := make(map[string]struct{}, len(namespaces))
	knowledgeIDs := make([]string, 0, len(namespaces))
	for _, namespace := range namespaces {
		if strings.TrimSpace(namespace.KnowledgeBase) != knowledgeBaseID {
			return nil, errors.New("graph namespaces must share one knowledge base")
		}
		knowledgeID := strings.TrimSpace(namespace.Knowledge)
		if knowledgeID == "" {
			allowAll = true
			continue
		}
		if _, duplicate := seen[knowledgeID]; duplicate {
			continue
		}
		seen[knowledgeID] = struct{}{}
		knowledgeIDs = append(knowledgeIDs, knowledgeID)
	}
	session := n.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		labelExpr := n.Label(types.NameSpace{KnowledgeBase: knowledgeBaseID})
		query := graphSearchInNamespacesQuery(labelExpr)
		params := map[string]interface{}{
			"nodes": nodes, "allow_all": allowAll, "knowledge_ids": knowledgeIDs,
		}
		result, err := tx.Run(ctx, query, params)
		if err != nil {
			return nil, fmt.Errorf("failed to run query: %v", err)
		}

		rows := make([]graphSearchRow, 0)
		for result.Next(ctx) {
			record := result.Record()
			node, _ := record.Get("n")
			rel, _ := record.Get("r")
			targetNode, _ := record.Get("m")

			nodeData := node.(neo4j.Node)
			targetNodeData := targetNode.(neo4j.Node)

			relData := rel.(neo4j.Relationship)
			rows = append(rows, graphSearchRow{
				Source: &types.GraphNode{
					Name:       nodeData.Props["name"].(string),
					Chunks:     listI2listS(nodeData.Props["chunks"].([]interface{})),
					Attributes: listI2listS(nodeData.Props["attributes"].([]interface{})),
				},
				SourceKnowledgeIDs: graphPropertyStrings(nodeData.Props, "kg"),
				Target: &types.GraphNode{
					Name:       targetNodeData.Props["name"].(string),
					Chunks:     listI2listS(targetNodeData.Props["chunks"].([]interface{})),
					Attributes: listI2listS(targetNodeData.Props["attributes"].([]interface{})),
				},
				TargetKnowledgeIDs: graphPropertyStrings(targetNodeData.Props, "kg"),
				Relation: &types.GraphRelation{
					Node1:        nodeData.Props["name"].(string),
					Node2:        targetNodeData.Props["name"].(string),
					Type:         relData.Type,
					KnowledgeIDs: graphPropertyStrings(relData.Props, "kg"),
				},
			})
		}
		return graphDataFromSearchRows(rows), nil
	})
	if err != nil {
		logger.Errorf(ctx, "search node failed: %v", err)
		return nil, err
	}
	return result.(*types.GraphData), nil
}

func graphPropertyStrings(properties map[string]any, key string) []string {
	switch raw := properties[key].(type) {
	case string:
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		return []string{raw}
	case []string:
		result := make([]string, len(raw))
		for i, value := range raw {
			if strings.TrimSpace(value) == "" {
				return nil
			}
			result[i] = value
		}
		return result
	case []interface{}:
		result := make([]string, len(raw))
		for i, value := range raw {
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil
			}
			result[i] = text
		}
		return result
	default:
		return nil
	}
}

func listI2listS(list []any) []string {
	result := make([]string, len(list))
	for i, v := range list {
		result[i] = fmt.Sprintf("%v", v)
	}
	return result
}
