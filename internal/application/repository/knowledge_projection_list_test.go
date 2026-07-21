package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestListPagedKnowledgeAppliesProductionAntiJoinAndCallerExclusionToCountAndRows(t *testing.T) {
	db := setupKnowledgeTestDB(t)
	repo := &knowledgeRepository{db: db}
	ctx := context.Background()
	require.NoError(t, db.Exec(`CREATE TABLE production_release_targets (
		id TEXT PRIMARY KEY,
		tenant_id INTEGER NOT NULL,
		document_id TEXT NOT NULL,
		target_knowledge_base_id TEXT NOT NULL,
		knowledge_id TEXT NOT NULL,
		status TEXT NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE production_projection_heads (
		tenant_id INTEGER NOT NULL,
		document_id TEXT NOT NULL,
		target_knowledge_base_id TEXT NOT NULL,
		active_release_target_id TEXT NOT NULL
	)`).Error)
	for _, id := range []string{"knowledge-active", "knowledge-old", "knowledge-building", "knowledge-manual", "caller-hidden"} {
		require.NoError(t, db.Exec(`INSERT INTO knowledges (id, tenant_id, knowledge_base_id, type, title, source)
			VALUES (?, 7, 'kb-1', 'manual', ?, 'manual')`, id, id).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO production_release_targets
		(id, tenant_id, document_id, target_knowledge_base_id, knowledge_id, status) VALUES
		('target-active', 7, 'document-active', 'kb-1', 'knowledge-active', ?),
		('target-old', 7, 'document-old', 'kb-1', 'knowledge-old', ?),
		('target-building', 7, 'document-building', 'kb-1', 'knowledge-building', ?)`,
		types.ReleaseTargetActive, types.ReleaseTargetActive, types.ReleaseTargetBuilding).Error)
	require.NoError(t, db.Exec(`INSERT INTO production_projection_heads
		(tenant_id, document_id, target_knowledge_base_id, active_release_target_id)
		VALUES (7, 'document-active', 'kb-1', 'target-active')`).Error)

	rows, total, err := repo.ListPagedKnowledgeByKnowledgeBaseID(ctx, 7, "kb-1", &types.Pagination{Page: 1, PageSize: 20}, types.KnowledgeListFilter{
		ExcludeKnowledgeIDs:                  []string{"caller-hidden"},
		ExcludeInactiveProductionProjections: true,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.ElementsMatch(t, []string{"knowledge-active", "knowledge-manual"}, projectionRepositoryKnowledgeIDs(rows))
}

func TestKnowledgeSearchAppliesProductionAntiJoinToRowsPaginationAndTotal(t *testing.T) {
	db := setupKnowledgeTestDB(t)
	repo := &knowledgeRepository{db: db}
	ctx := context.Background()
	require.NoError(t, db.Exec(`CREATE TABLE knowledge_bases (
		id TEXT PRIMARY KEY, tenant_id INTEGER NOT NULL, name TEXT NOT NULL, type TEXT NOT NULL, deleted_at DATETIME
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE production_release_targets (
		id TEXT PRIMARY KEY, tenant_id INTEGER NOT NULL, document_id TEXT NOT NULL,
		target_knowledge_base_id TEXT NOT NULL, knowledge_id TEXT NOT NULL, status TEXT NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE production_projection_heads (
		tenant_id INTEGER NOT NULL, document_id TEXT NOT NULL,
		target_knowledge_base_id TEXT NOT NULL, active_release_target_id TEXT NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO knowledge_bases (id, tenant_id, name, type)
		VALUES ('kb-1', 7, 'KB One', ?)`, types.KnowledgeBaseTypeDocument).Error)
	for _, id := range []string{"knowledge-active", "knowledge-old", "knowledge-building", "knowledge-ordinary"} {
		require.NoError(t, db.Exec(`INSERT INTO knowledges (id, tenant_id, knowledge_base_id, type, title, source)
			VALUES (?, 7, 'kb-1', 'manual', ?, 'manual')`, id, id).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO production_release_targets
		(id, tenant_id, document_id, target_knowledge_base_id, knowledge_id, status) VALUES
		('target-active', 7, 'document-active', 'kb-1', 'knowledge-active', ?),
		('target-old', 7, 'document-old', 'kb-1', 'knowledge-old', ?),
		('target-building', 7, 'document-building', 'kb-1', 'knowledge-building', ?)`,
		types.ReleaseTargetActive, types.ReleaseTargetActive, types.ReleaseTargetBuilding).Error)
	require.NoError(t, db.Exec(`INSERT INTO production_projection_heads
		(tenant_id, document_id, target_knowledge_base_id, active_release_target_id)
		VALUES (7, 'document-active', 'kb-1', 'target-active')`).Error)

	rows, hasMore, err := repo.SearchKnowledge(ctx, 7, "knowledge", 0, 10, nil)
	require.NoError(t, err)
	require.False(t, hasMore)
	require.ElementsMatch(t, []string{"knowledge-active", "knowledge-ordinary"}, projectionRepositoryKnowledgeIDs(rows))

	rows, hasMore, total, err := repo.SearchKnowledgeInScopes(ctx,
		[]types.KnowledgeSearchScope{{TenantID: 7, KBID: "kb-1"}}, "knowledge", 0, 1, nil)
	require.NoError(t, err)
	require.True(t, hasMore)
	require.Equal(t, int64(2), total)
	require.Len(t, rows, 1)
	require.NotContains(t, []string{"knowledge-old", "knowledge-building"}, rows[0].ID)
}

func projectionRepositoryKnowledgeIDs(rows []*types.Knowledge) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}
