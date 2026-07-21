package service

import (
	"context"
	"fmt"
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

func TestUpdateChunkRejectsForgedOrdinaryOwnershipForPersistedProjection(t *testing.T) {
	repo := &projectionChunkMutationRepo{}
	service := &chunkService{
		chunkRepository: repo,
		productionReleaseRepo: &projectionResolverReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{
			"kb-1": {AllProductionKnowledgeIDs: []string{"projection"}, InactiveKnowledgeIDs: []string{"projection"}},
		}},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "user-1")
	forged := &types.Chunk{
		ID: "chunk-1", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1", Content: "forged update",
	}

	err := service.UpdateChunk(ctx, forged)

	require.ErrorIs(t, err, types.ErrProductionProjectionImmutable)
	require.Zero(t, repo.mutations)
}

type authoritativeChunkMutationRepo struct {
	interfaces.ChunkRepository
	rows          map[string]*types.Chunk
	listed        []*types.Chunk
	getCalls      int
	listCalls     int
	mutations     int
	updatedSingle *types.Chunk
	updatedBatch  []*types.Chunk
}

func (r *authoritativeChunkMutationRepo) GetChunkByID(_ context.Context, tenantID uint64, id string) (*types.Chunk, error) {
	r.getCalls++
	row := r.rows[id]
	if row == nil || row.TenantID != tenantID {
		return nil, ErrChunkNotFound
	}
	copyRow := *row
	return &copyRow, nil
}

func (r *authoritativeChunkMutationRepo) ListChunksByID(_ context.Context, tenantID uint64, ids []string) ([]*types.Chunk, error) {
	r.listCalls++
	if r.listed != nil {
		return r.listed, nil
	}
	rows := make([]*types.Chunk, 0, len(ids))
	for _, id := range ids {
		row := r.rows[id]
		if row == nil || row.TenantID != tenantID {
			continue
		}
		copyRow := *row
		rows = append(rows, &copyRow)
	}
	return rows, nil
}

func (r *authoritativeChunkMutationRepo) UpdateChunk(_ context.Context, chunk *types.Chunk) error {
	r.mutations++
	r.updatedSingle = chunk
	return nil
}

func (r *authoritativeChunkMutationRepo) UpdateChunks(_ context.Context, chunks []*types.Chunk) error {
	r.mutations++
	r.updatedBatch = chunks
	return nil
}

func authoritativeChunkMutationService(repo interfaces.ChunkRepository) *chunkService {
	return &chunkService{
		chunkRepository: repo,
		productionReleaseRepo: &projectionResolverReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{
			"kb-1": {AllProductionKnowledgeIDs: []string{"projection"}, InactiveKnowledgeIDs: []string{"projection"}},
		}},
	}
}

func authoritativeChunkMutationContext() context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	return context.WithValue(ctx, types.UserIDContextKey, "user-1")
}

func TestUpdateChunksRejectsEntireBatchWhenPersistedProjectionAppearsLater(t *testing.T) {
	repo := &authoritativeChunkMutationRepo{rows: map[string]*types.Chunk{
		"ordinary-chunk":   {ID: "ordinary-chunk", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
		"projection-chunk": {ID: "projection-chunk", TenantID: 7, KnowledgeID: "projection", KnowledgeBaseID: "kb-1"},
	}}
	service := authoritativeChunkMutationService(repo)
	updates := []*types.Chunk{
		{ID: "ordinary-chunk", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
		{ID: "projection-chunk", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
	}

	err := service.UpdateChunks(authoritativeChunkMutationContext(), updates)

	require.ErrorIs(t, err, types.ErrProductionProjectionImmutable)
	require.Equal(t, 1, repo.listCalls)
	require.Zero(t, repo.mutations)
}

func TestUpdateChunksRequiresExactTenantScopedPersistedSet(t *testing.T) {
	tests := []struct {
		name    string
		updates []*types.Chunk
		rows    map[string]*types.Chunk
		listed  []*types.Chunk
	}{
		{
			name: "missing later id",
			updates: []*types.Chunk{
				{ID: "chunk-1", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
				{ID: "missing", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
			},
			rows: map[string]*types.Chunk{
				"chunk-1": {ID: "chunk-1", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
			},
		},
		{
			name: "cross tenant id",
			updates: []*types.Chunk{
				{ID: "chunk-1", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
				{ID: "other-tenant", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
			},
			rows: map[string]*types.Chunk{
				"chunk-1":      {ID: "chunk-1", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
				"other-tenant": {ID: "other-tenant", TenantID: 8, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
			},
		},
		{
			name: "duplicate request id",
			updates: []*types.Chunk{
				{ID: "chunk-1", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
				{ID: "chunk-1", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
			},
			rows: map[string]*types.Chunk{
				"chunk-1": {ID: "chunk-1", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
			},
		},
		{
			name: "unexpected repository row",
			updates: []*types.Chunk{
				{ID: "chunk-1", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
			},
			rows: map[string]*types.Chunk{},
			listed: []*types.Chunk{
				{ID: "unexpected", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &authoritativeChunkMutationRepo{rows: tc.rows, listed: tc.listed}
			err := authoritativeChunkMutationService(repo).UpdateChunks(authoritativeChunkMutationContext(), tc.updates)
			require.Error(t, err, fmt.Sprintf("%s must fail closed", tc.name))
			require.Equal(t, 1, repo.listCalls)
			require.Zero(t, repo.mutations)
		})
	}
}

func TestOrdinaryChunkUpdatesUsePersistedOwnership(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		repo := &authoritativeChunkMutationRepo{rows: map[string]*types.Chunk{
			"chunk-1": {ID: "chunk-1", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
		}}
		update := &types.Chunk{
			ID: "chunk-1", TenantID: 99, KnowledgeID: "forged", KnowledgeBaseID: "other-kb", Content: "updated",
		}

		require.NoError(t, authoritativeChunkMutationService(repo).UpdateChunk(authoritativeChunkMutationContext(), update))
		require.Equal(t, 1, repo.getCalls)
		require.Equal(t, uint64(7), repo.updatedSingle.TenantID)
		require.Equal(t, "ordinary", repo.updatedSingle.KnowledgeID)
		require.Equal(t, "kb-1", repo.updatedSingle.KnowledgeBaseID)
	})

	t.Run("batch", func(t *testing.T) {
		repo := &authoritativeChunkMutationRepo{rows: map[string]*types.Chunk{
			"chunk-1": {ID: "chunk-1", TenantID: 7, KnowledgeID: "ordinary", KnowledgeBaseID: "kb-1"},
			"chunk-2": {ID: "chunk-2", TenantID: 7, KnowledgeID: "ordinary-2", KnowledgeBaseID: "kb-2"},
		}}
		updates := []*types.Chunk{
			{ID: "chunk-1", TenantID: 99, KnowledgeID: "forged", KnowledgeBaseID: "other-kb", Content: "one"},
			{ID: "chunk-2", TenantID: 99, KnowledgeID: "forged", KnowledgeBaseID: "other-kb", Content: "two"},
		}

		require.NoError(t, authoritativeChunkMutationService(repo).UpdateChunks(authoritativeChunkMutationContext(), updates))
		require.Equal(t, 1, repo.listCalls)
		require.Len(t, repo.updatedBatch, 2)
		require.Equal(t, uint64(7), repo.updatedBatch[0].TenantID)
		require.Equal(t, "ordinary", repo.updatedBatch[0].KnowledgeID)
		require.Equal(t, "kb-1", repo.updatedBatch[0].KnowledgeBaseID)
		require.Equal(t, uint64(7), repo.updatedBatch[1].TenantID)
		require.Equal(t, "ordinary-2", repo.updatedBatch[1].KnowledgeID)
		require.Equal(t, "kb-2", repo.updatedBatch[1].KnowledgeBaseID)
	})
}
