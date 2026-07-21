package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type productionCleanupChunksStub struct {
	interfaces.ChunkService
	chunks      []*types.Chunk
	updates     int
	userCalls   int
	systemCalls int
}

func (s *productionCleanupChunksStub) ListChunksByKnowledgeID(context.Context, string) ([]*types.Chunk, error) {
	s.userCalls++
	return nil, errors.New("projection cleanup used user-facing chunk read")
}
func (s *productionCleanupChunksStub) ListChunksByKnowledgeIDForSystem(ctx context.Context, _ string) ([]*types.Chunk, error) {
	s.systemCalls++
	actorID, _ := types.UserIDFromContext(ctx)
	tenantID, _ := types.TenantIDFromContext(ctx)
	if actorID != types.ProductionSystemActorID || tenantID != 7 {
		return nil, types.ErrProductionForbidden
	}
	return s.chunks, nil
}
func (s *productionCleanupChunksStub) UpdateChunks(_ context.Context, chunks []*types.Chunk) error {
	s.updates++
	s.chunks = chunks
	return nil
}

type productionCleanupIndexStub struct{ calls int }

func (s *productionCleanupIndexStub) DisableChunks(context.Context, *types.ProductionReleaseTarget, []*types.Chunk) error {
	s.calls++
	return nil
}

type productionCleanupEngineStub struct {
	interfaces.RetrieveEngineService
	calls  int
	status map[string]bool
}

func (s *productionCleanupEngineStub) EngineType() types.RetrieverEngineType {
	return types.PostgresRetrieverEngineType
}

func (s *productionCleanupEngineStub) Support() []types.RetrieverType {
	return []types.RetrieverType{types.VectorRetrieverType}
}

func (s *productionCleanupEngineStub) BatchUpdateChunkEnabledStatus(_ context.Context, status map[string]bool) error {
	s.calls++
	s.status = status
	return nil
}

type productionCleanupRegistryStub struct {
	interfaces.RetrieveEngineRegistry
	engine            interfaces.RetrieveEngineService
	requested         []string
	retrievalRequests []types.RetrieverEngineType
}

func (s *productionCleanupRegistryStub) GetByStoreID(storeID string) (interfaces.RetrieveEngineService, error) {
	s.requested = append(s.requested, storeID)
	return s.engine, nil
}

func (s *productionCleanupRegistryStub) GetRetrieveEngineService(engineType types.RetrieverEngineType) (interfaces.RetrieveEngineService, error) {
	s.retrievalRequests = append(s.retrievalRequests, engineType)
	return s.engine, nil
}

type productionCleanupOwnershipStub struct {
	owned    bool
	storeID  string
	tenantID uint64
}

func (s *productionCleanupOwnershipStub) StoreOwnedBy(_ context.Context, storeID string, tenantID uint64) (bool, error) {
	s.storeID = storeID
	s.tenantID = tenantID
	return s.owned, nil
}

func TestProjectionCleanupNeverTouchesActiveHeadAndIsIdempotent(t *testing.T) {
	_, repo, _ := newProductionReleaseServiceFixture(t)
	chunks := &productionCleanupChunksStub{chunks: []*types.Chunk{{ID: "chunk-1", KnowledgeID: "knowledge-old", IsEnabled: true}}}
	indexes := &productionCleanupIndexStub{}
	cleanup := &ProductionProjectionCleanup{releases: repo, chunks: chunks, indexes: indexes, uow: productionReleaseUOWStub{}, audit: &productionReleaseAuditStub{}, now: time.Now}

	require.ErrorIs(t, cleanup.Cleanup(productionReleaseContext(), "target-old"), types.ErrProductionReleaseLifecycle)
	require.Zero(t, indexes.calls)

	retained := repo.targets["target-old"]
	retained.Status = types.ReleaseTargetRolledBack
	expired := time.Now().Add(-time.Minute)
	retained.RetentionUntil = &expired
	repo.targets["target-new"].Status = types.ReleaseTargetActive
	require.NoError(t, cleanup.Cleanup(productionReleaseContext(), "target-old"))
	require.False(t, chunks.chunks[0].IsEnabled)
	require.Equal(t, types.ReleaseTargetCleaned, repo.targets["target-old"].Status)
	require.NoError(t, cleanup.Cleanup(productionReleaseContext(), "target-old"))
	require.Equal(t, 1, indexes.calls)
	require.Zero(t, chunks.userCalls)
	require.Equal(t, 1, chunks.systemCalls)
}

func TestProjectionCleanupRetentionBoundaryIsFailClosed(t *testing.T) {
	_, repo, _ := newProductionReleaseServiceFixture(t)
	target := repo.targets["target-old"]
	target.Status = types.ReleaseTargetRolledBack
	retained := time.Now().Add(time.Hour)
	target.RetentionUntil = &retained
	cleanup := &ProductionProjectionCleanup{releases: repo, chunks: &productionCleanupChunksStub{}, indexes: &productionCleanupIndexStub{}, uow: productionReleaseUOWStub{}, audit: &productionReleaseAuditStub{}, now: time.Now}
	require.ErrorIs(t, cleanup.Cleanup(productionReleaseContext(), target.ID), types.ErrProductionReleaseLifecycle)
	require.Equal(t, types.ReleaseTargetRolledBack, target.Status)
}

func TestProjectionRetentionSweepIsBoundedAndSkipsActiveHead(t *testing.T) {
	_, repo, _ := newProductionReleaseServiceFixture(t)
	expired := time.Now().Add(-time.Minute)
	repo.targets["target-old"].Status = types.ReleaseTargetRolledBack
	repo.targets["target-old"].RetentionUntil = &expired
	repo.targets["target-new"].Status = types.ReleaseTargetActive
	chunks := &productionCleanupChunksStub{chunks: []*types.Chunk{{ID: "chunk-old", KnowledgeID: "knowledge-old", IsEnabled: true}}}
	cleanup := &ProductionProjectionCleanup{releases: repo, chunks: chunks, indexes: &productionCleanupIndexStub{}, uow: productionReleaseUOWStub{repo: repo}, audit: &productionReleaseAuditStub{}, now: time.Now}
	require.NoError(t, cleanup.CleanupExpired(productionReleaseContext(), 10))
	require.Equal(t, types.ReleaseTargetCleaned, repo.targets["target-old"].Status)
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-new"].Status)
}

func TestProjectionCleanupUsesAuthenticatedSnapshotVectorStoreDespiteLiveKBDrift(t *testing.T) {
	snapshot, digest, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(`{"version":1,"indexing_strategy":{"vector_enabled":true},"vector_store_id":"snapshot-store"}`))
	require.NoError(t, err)
	liveStore := "live-store"
	target := &types.ProductionReleaseTarget{
		ID: "target-old", TenantID: 7, TargetKnowledgeBaseID: "kb-1",
		ConfigSnapshot: snapshot, ConfigDigest: digest,
	}
	engine := &productionCleanupEngineStub{}
	registry := &productionCleanupRegistryStub{engine: engine}
	ownership := &productionCleanupOwnershipStub{owned: true}
	updater := NewProductionProjectionRetrieveIndexUpdater(
		productionReleaseKBStub{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7, VectorStoreID: &liveStore}},
		registry,
		ownership,
	)

	require.NoError(t, updater.DisableChunks(productionReleaseContext(), target, []*types.Chunk{{ID: "chunk-1"}}))
	require.Equal(t, "snapshot-store", ownership.storeID)
	require.Equal(t, uint64(7), ownership.tenantID)
	require.Equal(t, []string{"snapshot-store"}, registry.requested)
	require.Equal(t, map[string]bool{"chunk-1": false}, engine.status)
}

func TestProjectionCleanupRejectsTamperedOrIncompleteSnapshot(t *testing.T) {
	valid, digest, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(`{"version":1,"indexing_strategy":{"vector_enabled":true},"vector_store_id":"snapshot-store"}`))
	require.NoError(t, err)
	incomplete, incompleteDigest, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(`{"version":1}`))
	require.NoError(t, err)
	tests := []struct {
		name     string
		snapshot types.JSON
		digest   string
	}{
		{name: "digest mismatch", snapshot: valid, digest: incompleteDigest},
		{name: "noncanonical", snapshot: types.JSON(`{ "version": 1, "indexing_strategy": { "vector_enabled": true }, "vector_store_id": "snapshot-store" }`), digest: digest},
		{name: "missing retrieval configuration", snapshot: incomplete, digest: incompleteDigest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			engine := &productionCleanupEngineStub{}
			registry := &productionCleanupRegistryStub{engine: engine}
			ownership := &productionCleanupOwnershipStub{owned: true}
			updater := NewProductionProjectionRetrieveIndexUpdater(
				productionReleaseKBStub{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7}}, registry, ownership,
			)
			target := &types.ProductionReleaseTarget{ID: "target-old", TenantID: 7, TargetKnowledgeBaseID: "kb-1", ConfigSnapshot: tc.snapshot, ConfigDigest: tc.digest}

			err := updater.DisableChunks(productionReleaseContext(), target, []*types.Chunk{{ID: "chunk-1"}})

			require.ErrorIs(t, err, types.ErrProductionReleaseConfigInvalid)
			require.Empty(t, registry.requested)
			require.Zero(t, engine.calls)
		})
	}
}

func TestProjectionCleanupSkipsExternalIndexesForNonRetrievalStrategies(t *testing.T) {
	tests := []struct {
		name     string
		strategy types.IndexingStrategy
	}{
		{name: "wiki only", strategy: types.IndexingStrategy{WikiEnabled: true}},
		{name: "graph only", strategy: types.IndexingStrategy{GraphEnabled: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"version": 1, "indexing_strategy": tc.strategy})
			require.NoError(t, err)
			snapshot, digest, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(raw))
			require.NoError(t, err)
			engine := &productionCleanupEngineStub{}
			registry := &productionCleanupRegistryStub{engine: engine}
			ownership := &productionCleanupOwnershipStub{owned: true}
			updater := NewProductionProjectionRetrieveIndexUpdater(
				productionReleaseKBStub{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7}}, registry, ownership,
			)
			target := &types.ProductionReleaseTarget{
				ID: "target-old", TenantID: 7, TargetKnowledgeBaseID: "kb-1",
				ConfigSnapshot: snapshot, ConfigDigest: digest,
			}

			require.NoError(t, updater.DisableChunks(productionReleaseContext(), target, []*types.Chunk{{ID: "chunk-1"}}))
			require.Empty(t, registry.requested)
			require.Zero(t, engine.calls)
			require.Empty(t, ownership.storeID)
		})
	}
}

func TestProjectionCleanupPreparedNonRetrievalTargetsIgnoreDormantRetrievalSelectors(t *testing.T) {
	storeID := "snapshot-store"
	for _, tc := range []struct {
		name     string
		strategy types.IndexingStrategy
		storeID  *string
		extract  *types.ExtractConfig
	}{
		{name: "wiki only with vector store", strategy: types.IndexingStrategy{WikiEnabled: true}, storeID: &storeID},
		{name: "graph only with tenant retrievers", strategy: types.IndexingStrategy{GraphEnabled: true}, extract: &types.ExtractConfig{Enabled: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, _ := newProductionReleaseServiceFixture(t)
			engine := &productionCleanupEngineStub{}
			registry := &productionCleanupRegistryStub{engine: engine}
			ownership := &productionCleanupOwnershipStub{owned: true}
			svc.registry = registry
			svc.ownership = ownership
			svc.kbs = productionReleaseKBStub{kb: &types.KnowledgeBase{
				ID: "kb-1", TenantID: 7, CreatorID: "00000000-0000-4000-8000-000000000007", Type: types.KnowledgeBaseTypeDocument,
				StorageProviderConfig: &types.StorageProviderConfig{Provider: "local"},
				ChunkingConfig:        types.ChunkingConfig{Strategy: "recursive", ChunkSize: 256},
				EmbeddingModelID:      "embedding-1", SummaryModelID: "summary-1",
				IndexingStrategy: tc.strategy, ExtractConfig: tc.extract, VectorStoreID: tc.storeID,
			}}

			release, err := svc.Prepare(productionReleaseContext(), "document-1", "version-3", []string{"kb-1"})
			require.NoError(t, err)
			require.Len(t, release.Targets, 1)
			target := release.Targets[0]
			repo.targets[target.ID] = target
			rolledBackAt := time.Now().Add(-2 * time.Hour)
			retentionUntil := time.Now().Add(-time.Hour)
			target.Status = types.ReleaseTargetRolledBack
			target.RolledBackAt = &rolledBackAt
			target.RetentionUntil = &retentionUntil
			registry.requested = nil
			registry.retrievalRequests = nil
			ownership.storeID = ""
			chunks := &productionCleanupChunksStub{chunks: []*types.Chunk{{
				ID: "chunk-1", KnowledgeID: target.KnowledgeID, IsEnabled: true,
			}}}
			cleanup := &ProductionProjectionCleanup{
				releases: repo, chunks: chunks,
				indexes: NewProductionProjectionRetrieveIndexUpdater(svc.kbs, registry, ownership),
				uow:     productionReleaseUOWStub{repo: repo}, audit: &productionReleaseAuditStub{}, now: time.Now,
			}

			require.NoError(t, cleanup.Cleanup(productionReleaseContext(), target.ID))
			require.Equal(t, types.ReleaseTargetCleaned, target.Status)
			require.Equal(t, 1, chunks.updates)
			require.False(t, chunks.chunks[0].IsEnabled)
			require.Zero(t, engine.calls)
			require.Empty(t, registry.requested)
			require.Empty(t, registry.retrievalRequests)
			require.Empty(t, ownership.storeID)
		})
	}
}

func TestProjectionCleanupRequiresRetrievalConfigForEmbeddingStrategy(t *testing.T) {
	for _, raw := range []types.JSON{
		types.JSON(`{"version":1,"indexing_strategy":{"vector_enabled":true}}`),
		types.JSON(`{"version":1,"vector_store_id":"snapshot-store"}`),
	} {
		snapshot, digest, err := types.CanonicalProductionReleaseTargetConfig(raw)
		require.NoError(t, err)
		engine := &productionCleanupEngineStub{}
		registry := &productionCleanupRegistryStub{engine: engine}
		ownership := &productionCleanupOwnershipStub{owned: true}
		updater := NewProductionProjectionRetrieveIndexUpdater(
			productionReleaseKBStub{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7}}, registry, ownership,
		)
		target := &types.ProductionReleaseTarget{
			ID: "target-old", TenantID: 7, TargetKnowledgeBaseID: "kb-1",
			ConfigSnapshot: snapshot, ConfigDigest: digest,
		}

		err = updater.DisableChunks(productionReleaseContext(), target, []*types.Chunk{{ID: "chunk-1"}})

		require.ErrorIs(t, err, types.ErrProductionReleaseConfigInvalid)
		require.Empty(t, registry.requested)
		require.Zero(t, engine.calls)
	}
}
