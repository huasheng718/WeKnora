package types

import (
	"sort"
	"strings"
)

// ChunkContext represents chunk content with surrounding context
type ChunkContext struct {
	ChunkID     string `json:"chunk_id"`
	Content     string `json:"content"`
	PrevContent string `json:"prev_content,omitempty"` // Previous chunk content for context
	NextContent string `json:"next_content,omitempty"` // Next chunk content for context
}

// PromptTemplateStructured represents the prompt template structured
type PromptTemplateStructured struct {
	Description string      `json:"description"`
	Tags        []string    `json:"tags"`
	Examples    []GraphData `json:"examples"`
}

type GraphNode struct {
	Name               string              `json:"name,omitempty"`
	Chunks             []string            `json:"chunks,omitempty"`
	Attributes         []string            `json:"attributes,omitempty"`
	ProjectionVariants []*GraphNodeVariant `json:"-"`
}

// GraphNodeVariant keeps projection provenance until visibility pruning.
// It is internal graph-processing metadata and is never serialized.
type GraphNodeVariant struct {
	KnowledgeIDs []string `json:"-"`
	Chunks       []string `json:"-"`
	Attributes   []string `json:"-"`
}

// GraphRelation represents the relation of the graph
type GraphRelation struct {
	Node1        string   `json:"node1,omitempty"`
	Node2        string   `json:"node2,omitempty"`
	Type         string   `json:"type,omitempty"`
	KnowledgeIDs []string `json:"knowledge_ids,omitempty"`
}

type GraphData struct {
	Text     string           `json:"text,omitempty"`
	Node     []*GraphNode     `json:"node,omitempty"`
	Relation []*GraphRelation `json:"relation,omitempty"`
}

// NameSpace represents the name space of the knowledge base and knowledge
type NameSpace struct {
	KnowledgeBase string `json:"knowledge_base"`
	Knowledge     string `json:"knowledge"`
}

// MergeGraphData deep-copies and deterministically merges graph query results.
// ProjectionVariants are retained for the Task 4 visibility-pruning boundary.
func MergeGraphData(destination, source *GraphData) *GraphData {
	if destination == nil {
		destination = &GraphData{}
	}
	if source == nil {
		return destination
	}
	nodes := make(map[string]*GraphNode, len(destination.Node))
	for _, node := range destination.Node {
		if node != nil {
			nodes[node.Name] = node
		}
	}
	for _, node := range source.Node {
		if node == nil {
			continue
		}
		if existing := nodes[node.Name]; existing != nil {
			existing.Chunks = mergeGraphStrings(existing.Chunks, node.Chunks)
			existing.Attributes = mergeGraphStrings(existing.Attributes, node.Attributes)
			existing.ProjectionVariants = mergeGraphVariants(existing.ProjectionVariants, node.ProjectionVariants)
			continue
		}
		copyNode := *node
		copyNode.Chunks = append([]string(nil), node.Chunks...)
		copyNode.Attributes = append([]string(nil), node.Attributes...)
		copyNode.ProjectionVariants = cloneGraphVariants(node.ProjectionVariants)
		destination.Node = append(destination.Node, &copyNode)
		nodes[copyNode.Name] = &copyNode
	}
	relations := make(map[string]*GraphRelation, len(destination.Relation))
	for _, relation := range destination.Relation {
		if relation != nil {
			relations[graphRelationKey(relation)] = relation
		}
	}
	for _, relation := range source.Relation {
		if relation == nil {
			continue
		}
		key := graphRelationKey(relation)
		if existing := relations[key]; existing != nil {
			existing.KnowledgeIDs = mergeGraphStrings(existing.KnowledgeIDs, relation.KnowledgeIDs)
			continue
		}
		copyRelation := *relation
		copyRelation.KnowledgeIDs = append([]string(nil), relation.KnowledgeIDs...)
		destination.Relation = append(destination.Relation, &copyRelation)
		relations[key] = &copyRelation
	}
	sort.Slice(destination.Node, func(i, j int) bool { return destination.Node[i].Name < destination.Node[j].Name })
	sort.Slice(destination.Relation, func(i, j int) bool {
		return graphRelationKey(destination.Relation[i]) < graphRelationKey(destination.Relation[j])
	})
	return destination
}

func mergeGraphStrings(existing, additions []string) []string {
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
	sort.Strings(existing)
	return existing
}

func mergeGraphVariants(existing, additions []*GraphNodeVariant) []*GraphNodeVariant {
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
		existing = append(existing, cloneGraphVariant(variant))
	}
	return existing
}

func cloneGraphVariants(variants []*GraphNodeVariant) []*GraphNodeVariant {
	result := make([]*GraphNodeVariant, 0, len(variants))
	for _, variant := range variants {
		if variant != nil {
			result = append(result, cloneGraphVariant(variant))
		}
	}
	return result
}

func cloneGraphVariant(variant *GraphNodeVariant) *GraphNodeVariant {
	return &GraphNodeVariant{KnowledgeIDs: append([]string(nil), variant.KnowledgeIDs...), Chunks: append([]string(nil), variant.Chunks...), Attributes: append([]string(nil), variant.Attributes...)}
}

func graphRelationKey(relation *GraphRelation) string {
	return relation.Node1 + "\x00" + relation.Node2 + "\x00" + relation.Type
}

func graphVariantKey(variant *GraphNodeVariant) string {
	return strings.Join(variant.KnowledgeIDs, "\x00") + "\x01" + strings.Join(variant.Chunks, "\x00") + "\x01" + strings.Join(variant.Attributes, "\x00")
}

// Labels returns the labels of the name space
func (n NameSpace) Labels() []string {
	res := make([]string, 0)
	if n.KnowledgeBase != "" {
		res = append(res, n.KnowledgeBase)
	}
	if n.Knowledge != "" {
		res = append(res, n.Knowledge)
	}
	return res
}
