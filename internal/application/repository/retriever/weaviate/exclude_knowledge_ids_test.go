package weaviate

import (
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository/retriever/filterutil"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestGetBaseFilterChunksEveryExcludedKnowledgeIDWithAndContainsNone(t *testing.T) {
	ids := make([]string, filterutil.MaxNegativeFilterValues+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("knowledge-%04d", i)
	}

	filter := (&weaviateRepository{}).getBaseFilter(types.RetrieveParams{ExcludeKnowledgeIDs: ids}).Build()

	require.Equal(t, "And", filter.Operator)
	var represented []string
	var exclusionClauses int
	for _, operand := range filter.Operands {
		if len(operand.Path) != 1 || operand.Path[0] != fieldKnowledgeID {
			continue
		}
		exclusionClauses++
		require.Equal(t, "ContainsNone", operand.Operator)
		require.LessOrEqual(t, len(operand.ValueTextArray), filterutil.MaxNegativeFilterValues)
		represented = append(represented, operand.ValueTextArray...)
	}
	require.Equal(t, 2, exclusionClauses)
	require.Equal(t, ids[0], represented[0])
	require.Equal(t, ids[filterutil.MaxNegativeFilterValues-1], represented[filterutil.MaxNegativeFilterValues-1])
	require.Equal(t, ids[len(ids)-1], represented[len(represented)-1])
	require.Equal(t, ids, represented)
}

func TestGetBaseFilterKeepsEmptyAndSingleExclusionBehavior(t *testing.T) {
	empty := (&weaviateRepository{}).getBaseFilter(types.RetrieveParams{}).Build()
	for _, operand := range empty.Operands {
		require.NotEqual(t, []string{fieldKnowledgeID}, operand.Path)
	}

	single := (&weaviateRepository{}).getBaseFilter(types.RetrieveParams{
		ExcludeKnowledgeIDs: []string{"knowledge-1"},
	}).Build()
	var found bool
	for _, operand := range single.Operands {
		if len(operand.Path) == 1 && operand.Path[0] == fieldKnowledgeID {
			found = true
			require.Equal(t, "ContainsNone", operand.Operator)
			require.Equal(t, []string{"knowledge-1"}, operand.ValueTextArray)
		}
	}
	require.True(t, found)
}
