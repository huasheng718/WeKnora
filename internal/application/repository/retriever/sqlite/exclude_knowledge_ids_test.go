package sqlite

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository/retriever/filterutil"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestBuildFilterWhereRepresentsEveryExcludedKnowledgeIDInOneJSONParameter(t *testing.T) {
	ids := make([]string, filterutil.MaxNegativeFilterValues+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("knowledge-%04d", i)
	}

	parts := buildFilterWhere(types.RetrieveParams{ExcludeKnowledgeIDs: ids})

	require.Len(t, parts, 1)
	require.Contains(t, parts[0].clause, "NOT EXISTS")
	require.Contains(t, parts[0].clause, "json_each(?)")
	require.Len(t, parts[0].args, 1)
	encoded, ok := parts[0].args[0].(string)
	require.True(t, ok)
	var represented []string
	require.NoError(t, json.Unmarshal([]byte(encoded), &represented))
	require.Equal(t, ids[0], represented[0])
	require.Equal(t, ids[filterutil.MaxNegativeFilterValues-1], represented[filterutil.MaxNegativeFilterValues-1])
	require.Equal(t, ids[len(ids)-1], represented[len(represented)-1])
	require.Equal(t, ids, represented)
}

func TestBuildFilterWhereKeepsEmptyAndSingleExclusionBehavior(t *testing.T) {
	require.Empty(t, buildFilterWhere(types.RetrieveParams{}))

	parts := buildFilterWhere(types.RetrieveParams{ExcludeKnowledgeIDs: []string{"knowledge-1"}})
	require.Len(t, parts, 1)
	require.JSONEq(t, `["knowledge-1"]`, parts[0].args[0].(string))
}
