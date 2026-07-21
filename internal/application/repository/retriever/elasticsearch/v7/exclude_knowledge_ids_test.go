package v7

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository/retriever/filterutil"
	typesLocal "github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestGetBaseCondsChunksEveryExcludedKnowledgeIDIntoMustNot(t *testing.T) {
	ids := makeExcludedKnowledgeIDs()
	queryJSON := (&elasticsearchRepository{}).getBaseConds(typesLocal.RetrieveParams{ExcludeKnowledgeIDs: ids})

	var query struct {
		Bool struct {
			MustNot []map[string]map[string]any `json:"must_not"`
		} `json:"bool"`
	}
	require.NoError(t, json.Unmarshal([]byte(queryJSON), &query))
	var represented []string
	for _, clause := range query.Bool.MustNot {
		terms, ok := clause["terms"]
		if !ok {
			continue
		}
		values, ok := terms["knowledge_id"].([]any)
		require.True(t, ok)
		require.LessOrEqual(t, len(values), filterutil.MaxNegativeFilterValues)
		for _, value := range values {
			represented = append(represented, value.(string))
		}
	}
	require.Len(t, query.Bool.MustNot, 3) // disabled clause plus two exclusion chunks
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
