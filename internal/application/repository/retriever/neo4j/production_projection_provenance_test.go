package neo4j

import (
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
