package qdrant

import (
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository/retriever/filterutil"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestGetBaseFilterChunksEveryExcludedKnowledgeIDIntoMustNot(t *testing.T) {
	ids := make([]string, filterutil.MaxNegativeFilterValues+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("knowledge-%04d", i)
	}

	filter := (&qdrantRepository{}).getBaseFilter(types.RetrieveParams{ExcludeKnowledgeIDs: ids})

	require.Len(t, filter.MustNot, 2)
	var represented []string
	for _, clause := range filter.MustNot {
		values := clause.GetField().GetMatch().GetKeywords().GetStrings()
		require.LessOrEqual(t, len(values), filterutil.MaxNegativeFilterValues)
		represented = append(represented, values...)
	}
	require.Equal(t, ids[0], represented[0])
	require.Equal(t, ids[filterutil.MaxNegativeFilterValues-1], represented[filterutil.MaxNegativeFilterValues-1])
	require.Equal(t, ids[len(ids)-1], represented[len(represented)-1])
	require.Equal(t, ids, represented)
}
