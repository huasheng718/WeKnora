package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type productionCleanupChunksStub struct {
	interfaces.ChunkService
	chunks  []*types.Chunk
	updates int
}

func (s *productionCleanupChunksStub) ListChunksByKnowledgeID(context.Context, string) ([]*types.Chunk, error) {
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
