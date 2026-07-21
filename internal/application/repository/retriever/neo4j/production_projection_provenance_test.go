package neo4j

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestRelationshipImportPersistsKnowledgeProvenanceInIdentityAndProperties(t *testing.T) {
	rows := relationshipImportRows(types.NameSpace{KnowledgeBase: "kb-1", Knowledge: "knowledge-old"}, []string{"ENTITYkb"}, []*types.GraphRelation{{Node1: "A", Node2: "B", Type: "rel"}})
	require.Len(t, rows, 1)
	require.Equal(t, "knowledge-old", rows[0]["knowledge_id"])
	require.Equal(t, []string{"knowledge-old"}, rows[0]["kg"])
	require.Contains(t, relationshipImportQuery, "{knowledge_id: row.knowledge_id}")
	require.Contains(t, relationshipImportQuery, "{kg: row.kg}")
	require.False(t, strings.Contains(relationshipImportQuery, "row.attributes"), "provenance must not depend on absent caller attributes")
}

func TestRelationshipImportRejectsMissingKnowledgeProvenance(t *testing.T) {
	rows := relationshipImportRows(types.NameSpace{KnowledgeBase: "kb-1"}, []string{"ENTITYkb"}, []*types.GraphRelation{{Node1: "A", Node2: "B", Type: "rel"}})
	require.Empty(t, rows)
}

func TestGraphPropertyStringsAcceptsScalarAndListKnowledgeProvenance(t *testing.T) {
	require.Equal(t, []string{"knowledge-1"}, graphPropertyStrings(map[string]any{"kg": "knowledge-1"}, "kg"))
	require.Equal(t, []string{"knowledge-1", "knowledge-2"}, graphPropertyStrings(map[string]any{"kg": []any{"knowledge-1", "knowledge-2"}}, "kg"))
	require.Nil(t, graphPropertyStrings(map[string]any{}, "kg"))
}

func TestGraphPropertyStringsRejectsMalformedKnowledgeProvenance(t *testing.T) {
	require.Nil(t, graphPropertyStrings(map[string]any{"kg": 42}, "kg"))
	require.Nil(t, graphPropertyStrings(map[string]any{"kg": []any{"knowledge-1", 42}}, "kg"))
	require.Nil(t, graphPropertyStrings(map[string]any{"kg": []string{"knowledge-1", ""}}, "kg"))
}

func TestGraphDataFromSearchRowsAggregatesSameNameEvidenceRegardlessOfOrder(t *testing.T) {
	active := graphSearchRow{
		Source:             &types.GraphNode{Name: "shared", Chunks: []string{"chunk-active", "chunk-active"}, Attributes: []string{"active-attribute"}},
		SourceKnowledgeIDs: []string{"knowledge-active"},
		Target:             &types.GraphNode{Name: "peer", Chunks: []string{"chunk-peer-active"}},
		TargetKnowledgeIDs: []string{"knowledge-active"},
		Relation:           &types.GraphRelation{Node1: "shared", Node2: "peer", Type: "related", KnowledgeIDs: []string{"knowledge-active"}},
	}
	inactive := graphSearchRow{
		Source:             &types.GraphNode{Name: "shared", Chunks: []string{"chunk-old"}, Attributes: []string{"old-attribute"}},
		SourceKnowledgeIDs: []string{"knowledge-old"},
		Target:             &types.GraphNode{Name: "peer", Chunks: []string{"chunk-peer-old"}},
		TargetKnowledgeIDs: []string{"knowledge-old"},
		Relation:           &types.GraphRelation{Node1: "shared", Node2: "peer", Type: "related", KnowledgeIDs: []string{"knowledge-old"}},
	}
	for _, tc := range []struct {
		name string
		rows []graphSearchRow
	}{
		{name: "active first", rows: []graphSearchRow{active, inactive}},
		{name: "inactive first", rows: []graphSearchRow{inactive, active}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := graphDataFromSearchRows(tc.rows)
			require.Len(t, graph.Node, 2)
			nodes := map[string]*types.GraphNode{}
			for _, node := range graph.Node {
				nodes[node.Name] = node
			}
			require.ElementsMatch(t, []string{"chunk-active", "chunk-old"}, nodes["shared"].Chunks)
			require.ElementsMatch(t, []string{"active-attribute", "old-attribute"}, nodes["shared"].Attributes)
			require.ElementsMatch(t, []string{"chunk-peer-active", "chunk-peer-old"}, nodes["peer"].Chunks)
			require.Len(t, nodes["shared"].ProjectionVariants, 2)
			variants := map[string]*types.GraphNodeVariant{}
			for _, variant := range nodes["shared"].ProjectionVariants {
				variants[variant.KnowledgeIDs[0]] = variant
			}
			require.Equal(t, []string{"chunk-active"}, variants["knowledge-active"].Chunks)
			require.Equal(t, []string{"active-attribute"}, variants["knowledge-active"].Attributes)
			require.Equal(t, []string{"chunk-old"}, variants["knowledge-old"].Chunks)
			require.Equal(t, []string{"old-attribute"}, variants["knowledge-old"].Attributes)
			require.Len(t, graph.Relation, 2)
			require.ElementsMatch(t, []string{"knowledge-active", "knowledge-old"}, []string{
				graph.Relation[0].KnowledgeIDs[0], graph.Relation[1].KnowledgeIDs[0],
			})
			encoded, err := json.Marshal(nodes["shared"])
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "projection_variants")
			require.NotContains(t, string(encoded), "knowledge_ids")
		})
	}
}
