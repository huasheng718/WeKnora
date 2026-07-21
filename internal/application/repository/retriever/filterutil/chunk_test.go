package filterutil

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChunkStringsPreservesEveryValueAcrossBoundary(t *testing.T) {
	ids := make([]string, MaxNegativeFilterValues+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("knowledge-%04d", i)
	}

	chunks := ChunkStrings(ids)

	require.Len(t, chunks, 2)
	require.Len(t, chunks[0], MaxNegativeFilterValues)
	require.Len(t, chunks[1], 1)
	require.Equal(t, ids[0], chunks[0][0])
	require.Equal(t, ids[MaxNegativeFilterValues-1], chunks[0][MaxNegativeFilterValues-1])
	require.Equal(t, ids[MaxNegativeFilterValues], chunks[1][0])
}

func TestChunkStringsKeepsOrdinaryBehavior(t *testing.T) {
	require.Nil(t, ChunkStrings(nil))
	require.Equal(t, [][]string{{"knowledge-1"}}, ChunkStrings([]string{"knowledge-1"}))
}

func TestChunkStringsDoesNotAliasCallerStorage(t *testing.T) {
	ids := []string{"knowledge-1", "knowledge-2"}
	chunks := ChunkStrings(ids)

	ids[0] = "mutated-input"
	require.Equal(t, "knowledge-1", chunks[0][0])

	chunks[0][1] = "mutated-chunk"
	require.Equal(t, "knowledge-2", ids[1])
}
