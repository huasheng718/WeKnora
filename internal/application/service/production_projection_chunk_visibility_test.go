package service

import (
	"context"
	"testing"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type projectionChunkVisibilityRepo struct {
	interfaces.ChunkRepository
	listCalls int
}

type projectionGraphReadinessSpanRepo struct {
	apprepository.KnowledgeSpanRepository
}

func (*projectionGraphReadinessSpanRepo) LatestAttempt(context.Context, string) (int, error) {
	return 1, nil
}

func (*projectionGraphReadinessSpanRepo) ListByAttempt(context.Context, string, int) ([]types.KnowledgeProcessingSpan, error) {
	return []types.KnowledgeProcessingSpan{{
		ID: 1, Name: "postprocess.graph.chunk[0]", Status: types.SpanStatusDone,
	}}, nil
}

func (*projectionChunkVisibilityRepo) GetChunkByID(context.Context, uint64, string) (*types.Chunk, error) {
	return &types.Chunk{ID: "chunk-inactive", TenantID: 7, KnowledgeID: "inactive", KnowledgeBaseID: "kb-1", Content: "SECRET CHUNK"}, nil
}

func (*projectionChunkVisibilityRepo) GetChunkByIDOnly(context.Context, string) (*types.Chunk, error) {
	return &types.Chunk{ID: "chunk-inactive", TenantID: 7, KnowledgeID: "inactive", KnowledgeBaseID: "kb-1", Content: "SECRET CHUNK"}, nil
}

func (r *projectionChunkVisibilityRepo) ListPagedChunksByKnowledgeID(
	context.Context, uint64, string, *types.Pagination, []types.ChunkType,
	string, string, string, string, string,
) ([]*types.Chunk, int64, error) {
	r.listCalls++
	return []*types.Chunk{{Content: "SECRET CHUNK"}}, 1, nil
}

func (r *projectionChunkVisibilityRepo) ListChunksByKnowledgeID(
	_ context.Context, tenantID uint64, knowledgeID string,
) ([]*types.Chunk, error) {
	r.listCalls++
	return []*types.Chunk{{
		ID: "chunk-inactive", TenantID: tenantID, KnowledgeID: knowledgeID,
		KnowledgeBaseID: "kb-1", ChunkType: types.ChunkTypeText, Content: "SECRET CHUNK",
	}}, nil
}

func TestChunkUserReadsRejectInactiveProjectionBeforeReturningContent(t *testing.T) {
	repo := &projectionChunkVisibilityRepo{}
	service := &chunkService{
		chunkRepository: repo,
		productionReleaseRepo: &projectionResolverReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{
			"kb-1": {InactiveKnowledgeIDs: []string{"inactive"}, AllProductionKnowledgeIDs: []string{"inactive"}},
		}},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))

	_, err := service.GetChunkByID(ctx, "chunk-inactive")
	require.ErrorIs(t, err, apprepository.ErrKnowledgeNotFound)
	_, err = service.GetChunkByIDOnly(ctx, "chunk-inactive")
	require.ErrorIs(t, err, apprepository.ErrKnowledgeNotFound)
	_, err = service.ListPagedChunksByKnowledgeID(ctx, "inactive", &types.Pagination{Page: 1, PageSize: 10}, nil)
	require.ErrorIs(t, err, apprepository.ErrKnowledgeNotFound)
	require.Zero(t, repo.listCalls)
}

func TestChunkSystemReadRequiresSystemActorAndBypassesProjectionVisibility(t *testing.T) {
	repo := &projectionChunkVisibilityRepo{}
	service := &chunkService{
		chunkRepository: repo,
		productionReleaseRepo: &projectionResolverReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{
			"kb-1": {InactiveKnowledgeIDs: []string{"inactive"}, AllProductionKnowledgeIDs: []string{"inactive"}},
		}},
	}
	reader, ok := interface{}(service).(interface {
		ListChunksByKnowledgeIDForSystem(context.Context, string) ([]*types.Chunk, error)
	})
	require.True(t, ok, "chunk service must expose a clearly named trusted read")
	if !ok {
		return
	}
	userCtx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	userCtx = context.WithValue(userCtx, types.UserIDContextKey, "user-1")
	_, err := reader.ListChunksByKnowledgeIDForSystem(userCtx, "inactive")
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Zero(t, repo.listCalls)

	systemCtx := context.WithValue(userCtx, types.UserIDContextKey, types.ProductionSystemActorID)
	chunks, err := reader.ListChunksByKnowledgeIDForSystem(systemCtx, "inactive")
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	require.Equal(t, "SECRET CHUNK", chunks[0].Content)
	require.Equal(t, 1, repo.listCalls)
}

func TestProductionGraphReadinessUsesTrustedChunkReadForInactiveProjection(t *testing.T) {
	repo := &projectionChunkVisibilityRepo{}
	chunks := &chunkService{
		chunkRepository: repo,
		productionReleaseRepo: &projectionResolverReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{
			"kb-1": {InactiveKnowledgeIDs: []string{"inactive"}, AllProductionKnowledgeIDs: []string{"inactive"}},
		}},
	}
	snapshot, _, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(`{
		"version":1,
		"indexing_strategy":{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":false,"graph_enabled":true},
		"chunking":{"strategy":"recursive","chunk_size":256},
		"embedding_model_id":"embedding-model",
		"summary_model_id":"summary-model",
		"retriever_engines":[{"retriever_engine_type":"postgres","retriever_type":"vector"}],
		"graph":{"enabled":true,"model_id":"graph-model","extract_config":{"enabled":true}}
	}`))
	require.NoError(t, err)
	readiness := NewProductionGraphSpanReadiness(&projectionGraphReadinessSpanRepo{}, chunks)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "publisher-1")

	err = readiness.RequireSuccessfulGraphSubtasks(ctx,
		&types.ProductionReleaseTarget{TenantID: 7, ConfigSnapshot: snapshot},
		&types.Knowledge{ID: "inactive", TenantID: 7, KnowledgeBaseID: "kb-1"})
	require.NoError(t, err)
	require.Equal(t, 1, repo.listCalls)
}
