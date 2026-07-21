package chatpipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type projectionEntityGraphRepo struct {
	interfaces.RetrieveGraphRepository
}

func (*projectionEntityGraphRepo) SearchNode(context.Context, types.NameSpace, []string) (*types.GraphData, error) {
	return &types.GraphData{
		Node: []*types.GraphNode{
			{Name: "active", Chunks: []string{"chunk-active"}},
			{Name: "old", Chunks: []string{"chunk-old"}},
			{Name: "building", Chunks: []string{"chunk-building"}},
		},
		Relation: []*types.GraphRelation{
			{Node1: "active", Node2: "active", Type: "legacy-missing-provenance"},
			{Node1: "active", Node2: "active", Type: "inactive-only", KnowledgeIDs: []string{"knowledge-old"}},
			{Node1: "active", Node2: "old", Type: "related", KnowledgeIDs: []string{"knowledge-old"}},
			{Node1: "old", Node2: "building", Type: "related", KnowledgeIDs: []string{"knowledge-building"}},
		},
	}, nil
}

func TestEntitySearchRejectsSameTenantWrongKnowledgeBaseBackReference(t *testing.T) {
	chunks := []*types.Chunk{{ID: "chunk-active", TenantID: 7, KnowledgeID: "knowledge-active", KnowledgeBaseID: "kb-stale", ImageInfo: "[]"}}
	p := &PluginSearchEntity{graphRepo: &projectionEntityGraphRepo{}, chunkRepo: &projectionEntityChunkRepo{chunks: chunks}, knowledgeRepo: &projectionEntityKnowledgeRepo{rows: []*types.Knowledge{{ID: "knowledge-active", TenantID: 7, KnowledgeBaseID: "kb-stale"}}}}
	chat := &types.ChatManage{PipelineRequest: types.PipelineRequest{TenantID: 7, SearchTargets: types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-1", TenantID: 7, ExcludeKnowledgeIDs: []string{"knowledge-old", "knowledge-building"}}}}, PipelineState: types.PipelineState{Entity: []string{"term"}, EntityKBIDs: []string{"kb-1"}}}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	require.Nil(t, p.OnEvent(ctx, types.ENTITY_SEARCH, chat, func() *PluginError { return nil }))
	require.Empty(t, chat.GraphResult.Node)
	require.Empty(t, chat.SearchResult)
}

type projectionEntityChunkRepo struct {
	interfaces.ChunkRepository
	chunks []*types.Chunk
	err    error
}

func (r *projectionEntityChunkRepo) ListChunksByID(_ context.Context, _ uint64, ids []string) ([]*types.Chunk, error) {
	if r.err != nil {
		return nil, r.err
	}
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	var result []*types.Chunk
	for _, chunk := range r.chunks {
		if _, ok := wanted[chunk.ID]; ok {
			result = append(result, chunk)
		}
	}
	return result, nil
}

type projectionEntityKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	rows []*types.Knowledge
	err  error
}

func (r *projectionEntityKnowledgeRepo) GetKnowledgeBatch(_ context.Context, _ uint64, ids []string) ([]*types.Knowledge, error) {
	if r.err != nil {
		return nil, r.err
	}
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	var result []*types.Knowledge
	for _, row := range r.rows {
		if _, ok := wanted[row.ID]; ok {
			result = append(result, row)
		}
	}
	return result, nil
}

func TestEntitySearchPrunesInactiveProductionProjectionGraphAndChunks(t *testing.T) {
	chunks := []*types.Chunk{
		{ID: "chunk-active", TenantID: 7, KnowledgeID: "knowledge-active", KnowledgeBaseID: "kb-1", ImageInfo: "[]"},
		{ID: "chunk-old", TenantID: 7, KnowledgeID: "knowledge-old", KnowledgeBaseID: "kb-1", ImageInfo: "[]"},
		{ID: "chunk-building", TenantID: 7, KnowledgeID: "knowledge-building", KnowledgeBaseID: "kb-1", ImageInfo: "[]"},
	}
	p := &PluginSearchEntity{
		graphRepo: &projectionEntityGraphRepo{}, chunkRepo: &projectionEntityChunkRepo{chunks: chunks},
		knowledgeRepo: &projectionEntityKnowledgeRepo{rows: []*types.Knowledge{
			{ID: "knowledge-active", TenantID: 7, KnowledgeBaseID: "kb-1", Title: "active"},
			{ID: "knowledge-old", KnowledgeBaseID: "kb-1", Title: "old"},
			{ID: "knowledge-building", KnowledgeBaseID: "kb-1", Title: "building"},
		}},
	}
	chat := &types.ChatManage{
		PipelineRequest: types.PipelineRequest{TenantID: 7, SearchTargets: types.SearchTargets{&types.SearchTarget{
			Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-1", TenantID: 7,
			ExcludeKnowledgeIDs: []string{"knowledge-old", "knowledge-building"},
		}}},
		PipelineState: types.PipelineState{Entity: []string{"term"}, EntityKBIDs: []string{"kb-1"}},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	require.Nil(t, p.OnEvent(ctx, types.ENTITY_SEARCH, chat, func() *PluginError { return nil }))
	require.Len(t, chat.GraphResult.Node, 1)
	require.Equal(t, "active", chat.GraphResult.Node[0].Name)
	require.Empty(t, chat.GraphResult.Relation)
	require.Len(t, chat.SearchResult, 1)
	require.Equal(t, "knowledge-active", chat.SearchResult[0].KnowledgeID)
}

func TestEntitySearchPreservesAuthorizedCrossTenantSharedKnowledgeBase(t *testing.T) {
	chunks := []*types.Chunk{
		{ID: "chunk-active", TenantID: 200, KnowledgeID: "knowledge-active", KnowledgeBaseID: "kb-shared", ImageInfo: "[]"},
		{ID: "chunk-old", TenantID: 200, KnowledgeID: "knowledge-old", KnowledgeBaseID: "kb-shared", ImageInfo: "[]"},
		{ID: "chunk-building", TenantID: 200, KnowledgeID: "knowledge-building", KnowledgeBaseID: "kb-shared", ImageInfo: "[]"},
	}
	p := &PluginSearchEntity{
		graphRepo:     &projectionEntityGraphRepo{},
		chunkRepo:     &projectionEntityChunkRepo{chunks: chunks},
		knowledgeRepo: &projectionEntityKnowledgeRepo{rows: []*types.Knowledge{{ID: "knowledge-active", TenantID: 200, KnowledgeBaseID: "kb-shared", Title: "active"}}},
	}
	chat := &types.ChatManage{
		PipelineRequest: types.PipelineRequest{TenantID: 7, SearchTargets: types.SearchTargets{&types.SearchTarget{
			Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-shared", TenantID: 200,
			ExcludeKnowledgeIDs: []string{"knowledge-old", "knowledge-building"},
		}}},
		PipelineState: types.PipelineState{Entity: []string{"term"}, EntityKBIDs: []string{"kb-shared"}},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	require.Nil(t, p.OnEvent(ctx, types.ENTITY_SEARCH, chat, func() *PluginError { return nil }))
	require.Len(t, chat.GraphResult.Node, 1)
	require.Len(t, chat.SearchResult, 1)
	require.Equal(t, "knowledge-active", chat.SearchResult[0].KnowledgeID)
	require.Equal(t, "kb-shared", chat.SearchResult[0].KnowledgeBaseID)
}

func TestEntitySearchFailsClosedOnMissingOrErroredProvenanceDependency(t *testing.T) {
	allChunks := []*types.Chunk{
		{ID: "chunk-active", TenantID: 7, KnowledgeID: "knowledge-active", KnowledgeBaseID: "kb-1", ImageInfo: "[]"},
		{ID: "chunk-old", TenantID: 7, KnowledgeID: "knowledge-old", KnowledgeBaseID: "kb-1", ImageInfo: "[]"},
		{ID: "chunk-building", TenantID: 7, KnowledgeID: "knowledge-building", KnowledgeBaseID: "kb-1", ImageInfo: "[]"},
	}
	allKnowledges := []*types.Knowledge{
		{ID: "knowledge-active", TenantID: 7, KnowledgeBaseID: "kb-1"},
		{ID: "knowledge-old", TenantID: 7, KnowledgeBaseID: "kb-1"},
		{ID: "knowledge-building", TenantID: 7, KnowledgeBaseID: "kb-1"},
	}
	tests := []struct {
		name          string
		chunkRepo     *projectionEntityChunkRepo
		knowledgeRepo *projectionEntityKnowledgeRepo
	}{
		{name: "chunk error", chunkRepo: &projectionEntityChunkRepo{err: errors.New("chunk dependency unavailable")}, knowledgeRepo: &projectionEntityKnowledgeRepo{rows: allKnowledges}},
		{name: "missing chunk", chunkRepo: &projectionEntityChunkRepo{chunks: allChunks[:2]}, knowledgeRepo: &projectionEntityKnowledgeRepo{rows: allKnowledges}},
		{name: "knowledge error", chunkRepo: &projectionEntityChunkRepo{chunks: allChunks}, knowledgeRepo: &projectionEntityKnowledgeRepo{err: errors.New("knowledge dependency unavailable")}},
		{name: "missing knowledge", chunkRepo: &projectionEntityChunkRepo{chunks: allChunks}, knowledgeRepo: &projectionEntityKnowledgeRepo{rows: allKnowledges[:2]}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &PluginSearchEntity{graphRepo: &projectionEntityGraphRepo{}, chunkRepo: tc.chunkRepo, knowledgeRepo: tc.knowledgeRepo}
			chat := &types.ChatManage{
				PipelineRequest: types.PipelineRequest{TenantID: 7, SearchTargets: types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-1", TenantID: 7}}},
				PipelineState:   types.PipelineState{Entity: []string{"term"}, EntityKBIDs: []string{"kb-1"}, SearchResult: []*types.SearchResult{{ID: "preexisting"}}},
			}
			ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
			_ = p.OnEvent(ctx, types.ENTITY_SEARCH, chat, func() *PluginError { return nil })
			require.Empty(t, chat.GraphResult.Node)
			require.Empty(t, chat.GraphResult.Relation)
			require.Empty(t, chat.SearchResult)
		})
	}
}
