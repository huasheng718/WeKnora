package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type projectionChunkMutationRepo struct {
	interfaces.ChunkRepository
	mutations int
}

func (*projectionChunkMutationRepo) GetChunkByID(_ context.Context, _ uint64, id string) (*types.Chunk, error) {
	return &types.Chunk{ID: id, TenantID: 7, KnowledgeID: "projection", KnowledgeBaseID: "kb-1"}, nil
}

func (r *projectionChunkMutationRepo) ListChunksByID(context.Context, uint64, []string) ([]*types.Chunk, error) {
	return []*types.Chunk{{ID: "chunk-1", TenantID: 7, KnowledgeID: "projection", KnowledgeBaseID: "kb-1"}}, nil
}

func (r *projectionChunkMutationRepo) UpdateChunk(context.Context, *types.Chunk) error {
	r.mutations++
	return nil
}
func (r *projectionChunkMutationRepo) UpdateChunks(context.Context, []*types.Chunk) error {
	r.mutations++
	return nil
}
func (r *projectionChunkMutationRepo) DeleteChunk(context.Context, uint64, string) error {
	r.mutations++
	return nil
}
func (r *projectionChunkMutationRepo) DeleteChunks(context.Context, uint64, []string) error {
	r.mutations++
	return nil
}
func (r *projectionChunkMutationRepo) DeleteChunksByKnowledgeID(context.Context, uint64, string) error {
	r.mutations++
	return nil
}
func (r *projectionChunkMutationRepo) DeleteByKnowledgeList(context.Context, uint64, []string) error {
	r.mutations++
	return nil
}

func TestChunkMutationPathsRejectProjectionParentBeforeRepositoryMutation(t *testing.T) {
	repo := &projectionChunkMutationRepo{}
	service := &chunkService{
		chunkRepository: repo,
		productionReleaseRepo: &projectionResolverReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{
			"kb-1": {AllProductionKnowledgeIDs: []string{"projection"}, InactiveKnowledgeIDs: []string{"projection"}},
		}},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "user-1")
	projectionChunk := &types.Chunk{ID: "chunk-1", TenantID: 7, KnowledgeID: "projection", KnowledgeBaseID: "kb-1"}

	operations := []func() error{
		func() error { return service.UpdateChunk(ctx, projectionChunk) },
		func() error { return service.UpdateChunks(ctx, []*types.Chunk{projectionChunk}) },
		func() error { return service.DeleteChunk(ctx, projectionChunk.ID) },
		func() error { return service.DeleteChunks(ctx, []string{projectionChunk.ID}) },
		func() error { return service.DeleteChunksByKnowledgeID(ctx, projectionChunk.KnowledgeID) },
		func() error { return service.DeleteByKnowledgeList(ctx, []string{projectionChunk.KnowledgeID}) },
	}
	for _, operation := range operations {
		err := operation()
		require.ErrorIs(t, err, types.ErrProductionProjectionImmutable)
	}
	require.Zero(t, repo.mutations)
}
