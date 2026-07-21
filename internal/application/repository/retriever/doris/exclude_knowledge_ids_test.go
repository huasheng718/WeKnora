package doris

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository/retriever/filterutil"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestBuildBaseFilterChunksEveryExcludedKnowledgeIDWithoutBindExpansion(t *testing.T) {
	ids := make([]string, filterutil.MaxNegativeFilterValues+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("knowledge-%04d", i)
	}

	clause, args := buildBaseFilter(types.RetrieveParams{ExcludeKnowledgeIDs: ids}).build()

	require.Equal(t, 2, strings.Count(clause, "knowledge_id NOT IN ("))
	require.Contains(t, clause, ") AND knowledge_id NOT IN (")
	require.Contains(t, clause, "FROM_BASE64('"+base64.StdEncoding.EncodeToString([]byte(ids[0]))+"')")
	require.Contains(t, clause, "FROM_BASE64('"+base64.StdEncoding.EncodeToString([]byte(ids[filterutil.MaxNegativeFilterValues-1]))+"')")
	require.Contains(t, clause, "FROM_BASE64('"+base64.StdEncoding.EncodeToString([]byte(ids[len(ids)-1]))+"')")
	require.Equal(t, []any{true}, args, "large exclusions must not consume one SQL placeholder per ID")
}

func TestDorisStringExpressionRepresentsExactBytes(t *testing.T) {
	require.Equal(t, `FROM_BASE64('YSdiXGM=')`, dorisStringExpression(`a'b\c`))
}
