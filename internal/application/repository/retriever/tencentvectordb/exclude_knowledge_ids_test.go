package tencentvectordb

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository/retriever/filterutil"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestBaseFilterChunksEveryExcludedKnowledgeIDConjunctively(t *testing.T) {
	ids := make([]string, filterutil.MaxNegativeFilterValues+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("knowledge-%04d", i)
	}

	condition := (&repository{}).baseFilter(types.RetrieveParams{ExcludeKnowledgeIDs: ids}).Cond()

	require.Equal(t, 2, strings.Count(condition, "knowledge_id not in ("))
	require.Contains(t, condition, ") and knowledge_id not in (")
	require.Contains(t, condition, `"`+ids[0]+`"`)
	require.Contains(t, condition, `"`+ids[filterutil.MaxNegativeFilterValues-1]+`"`)
	require.Contains(t, condition, `"`+ids[len(ids)-1]+`"`)
}
