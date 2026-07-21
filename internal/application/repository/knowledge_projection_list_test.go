package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestListPagedKnowledgeExcludesProjectionIDsFromCountAndRows(t *testing.T) {
	db := setupKnowledgeTestDB(t)
	repo := &knowledgeRepository{db: db}
	ctx := context.Background()
	for _, id := range []string{"knowledge-active", "knowledge-old", "knowledge-building"} {
		require.NoError(t, db.Exec(`INSERT INTO knowledges (id, tenant_id, knowledge_base_id, type, title, source)
			VALUES (?, 7, 'kb-1', 'manual', ?, 'manual')`, id, id).Error)
	}

	rows, total, err := repo.ListPagedKnowledgeByKnowledgeBaseID(ctx, 7, "kb-1", &types.Pagination{Page: 1, PageSize: 20}, types.KnowledgeListFilter{
		ExcludeKnowledgeIDs: []string{"knowledge-old", "knowledge-building"},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	require.Equal(t, "knowledge-active", rows[0].ID)
}
