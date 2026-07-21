package v8

import (
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository/retriever/filterutil"
	typesLocal "github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestGetBaseCondsChunksEveryExcludedKnowledgeIDIntoMustNot(t *testing.T) {
	ids := makeExcludedKnowledgeIDs()
	queries := (&elasticsearchRepository{}).getBaseConds(typesLocal.RetrieveParams{ExcludeKnowledgeIDs: ids})

	require.Len(t, queries, 1)
	require.NotNil(t, queries[0].Bool)
	require.Len(t, queries[0].Bool.MustNot, 3) // disabled clause plus two exclusion chunks
	var represented []string
	for _, clause := range queries[0].Bool.MustNot {
		if clause.Terms == nil {
			continue
		}
		values, ok := clause.Terms.TermsQuery["knowledge_id"].([]string)
		require.True(t, ok)
		require.LessOrEqual(t, len(values), filterutil.MaxNegativeFilterValues)
		represented = append(represented, values...)
	}
	require.Equal(t, ids[0], represented[0])
	require.Equal(t, ids[filterutil.MaxNegativeFilterValues-1], represented[filterutil.MaxNegativeFilterValues-1])
	require.Equal(t, ids[len(ids)-1], represented[len(represented)-1])
	require.Equal(t, ids, represented)
}

func makeExcludedKnowledgeIDs() []string {
	ids := make([]string, filterutil.MaxNegativeFilterValues+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("knowledge-%04d", i)
	}
	return ids
}
