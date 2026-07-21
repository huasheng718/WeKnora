package milvus

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository/retriever/filterutil"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestGetBaseFilterChunksEveryExcludedKnowledgeIDWithAnd(t *testing.T) {
	ids := make([]string, filterutil.MaxNegativeFilterValues+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("knowledge-%04d", i)
	}

	expr, params, err := (&milvusRepository{}).getBaseFilterForQuery(types.RetrieveParams{ExcludeKnowledgeIDs: ids})

	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(expr, "knowledge_id not in"))
	require.Contains(t, expr, " and ")
	var represented []string
	for name, value := range params {
		if !strings.HasPrefix(name, fieldKnowledgeID+"_") {
			continue
		}
		chunk, ok := value.([]string)
		require.True(t, ok)
		require.LessOrEqual(t, len(chunk), filterutil.MaxNegativeFilterValues)
		represented = append(represented, chunk...)
	}
	require.ElementsMatch(t, ids, represented)
	require.Contains(t, represented, ids[0])
	require.Contains(t, represented, ids[filterutil.MaxNegativeFilterValues-1])
	require.Contains(t, represented, ids[len(ids)-1])
}
