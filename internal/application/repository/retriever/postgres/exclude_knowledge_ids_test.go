package postgres

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository/retriever/filterutil"
	"github.com/stretchr/testify/require"
)

func TestPostgresExcludeKnowledgeIDsUsesOneCompleteArrayParameter(t *testing.T) {
	ids := make([]string, filterutil.MaxNegativeFilterValues+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("knowledge-%04d", i)
	}

	for name, placeholder := range map[string]string{"keyword": "?", "vector": "$4"} {
		t.Run(name, func(t *testing.T) {
			clause, arg := postgresExcludeKnowledgeIDsClause(ids, placeholder)

			require.Contains(t, clause, "knowledge_id <> ALL")
			require.Contains(t, clause, placeholder+"::jsonb")
			require.Equal(t, 1, strings.Count(clause, placeholder))
			var represented []string
			require.NoError(t, json.Unmarshal([]byte(arg), &represented))
			require.Equal(t, ids[0], represented[0])
			require.Equal(t, ids[filterutil.MaxNegativeFilterValues-1], represented[filterutil.MaxNegativeFilterValues-1])
			require.Equal(t, ids[len(ids)-1], represented[len(represented)-1])
			require.Equal(t, ids, represented)
		})
	}
}

func TestPostgresExcludeKnowledgeIDsKeepsEmptyAndSingleBehavior(t *testing.T) {
	clause, arg := postgresExcludeKnowledgeIDsClause(nil, "?")
	require.Empty(t, clause)
	require.Empty(t, arg)

	clause, arg = postgresExcludeKnowledgeIDsClause([]string{"knowledge-1"}, "?")
	require.Contains(t, clause, "knowledge_id <> ALL")
	require.JSONEq(t, `["knowledge-1"]`, arg)
}
