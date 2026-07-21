package opensearch

import (
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository/retriever/filterutil"
	"github.com/stretchr/testify/require"
)

func TestToBoolMustChunksEveryExcludedKnowledgeIDConjunctively(t *testing.T) {
	ids := make([]string, filterutil.MaxNegativeFilterValues+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("knowledge-%04d", i)
	}

	clauses := (&retrieveFilters{ExcludeKnowledgeIDs: ids}).toBoolMust()

	var represented []string
	var exclusionClauses int
	for _, clause := range clauses {
		boolean, ok := clause["bool"].(map[string]any)
		if !ok {
			continue
		}
		mustNot, ok := boolean["must_not"].(map[string]any)
		if !ok {
			continue
		}
		terms := mustNot["terms"].(map[string]any)
		values, ok := terms["knowledge_id"].([]string)
		if !ok {
			continue
		}
		exclusionClauses++
		require.LessOrEqual(t, len(values), filterutil.MaxNegativeFilterValues)
		represented = append(represented, values...)
	}
	require.Equal(t, 2, exclusionClauses)
	require.Equal(t, ids[0], represented[0])
	require.Equal(t, ids[filterutil.MaxNegativeFilterValues-1], represented[filterutil.MaxNegativeFilterValues-1])
	require.Equal(t, ids[len(ids)-1], represented[len(represented)-1])
	require.Equal(t, ids, represented)
}
