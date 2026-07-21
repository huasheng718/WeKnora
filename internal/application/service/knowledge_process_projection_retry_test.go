package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

var projectionManualGeneration = time.Date(2026, 7, 22, 0, 30, 0, 0, time.UTC)

type projectionManualKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	knowledge *types.Knowledge
	updates   int
	statuses  []string
}

func (r *projectionManualKnowledgeRepo) GetKnowledgeByID(context.Context, uint64, string) (*types.Knowledge, error) {
	return r.knowledge, nil
}

func (r *projectionManualKnowledgeRepo) UpdateKnowledge(_ context.Context, knowledge *types.Knowledge) error {
	r.updates++
	r.statuses = append(r.statuses, knowledge.ParseStatus)
	r.knowledge = knowledge
	return nil
}

type projectionManualKBService struct {
	interfaces.KnowledgeBaseService
	kb *types.KnowledgeBase
}

func (s *projectionManualKBService) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

type projectionManualTenantRepo struct {
	interfaces.TenantRepository
	tenant      *types.Tenant
	adjustCalls int
}

func (r *projectionManualTenantRepo) GetTenantByID(context.Context, uint64) (*types.Tenant, error) {
	return r.tenant, nil
}

func (r *projectionManualTenantRepo) AdjustStorageUsed(context.Context, uint64, int64) error {
	r.adjustCalls++
	return nil
}

type projectionManualChunkRepo struct {
	interfaces.ChunkRepository
	imageListCalls int
}

func (r *projectionManualChunkRepo) ListImageInfoByKnowledgeIDs(
	context.Context, uint64, []string,
) ([]interfaces.ChunkImageInfo, error) {
	r.imageListCalls++
	return nil, nil
}

type projectionManualChunkService struct {
	interfaces.ChunkService
	repo        *projectionManualChunkRepo
	deleteCalls int
	createCalls int
}

func (s *projectionManualChunkService) GetRepository() interfaces.ChunkRepository {
	return s.repo
}

func (s *projectionManualChunkService) DeleteChunksByKnowledgeID(context.Context, string) error {
	s.deleteCalls++
	return nil
}

func (s *projectionManualChunkService) CreateChunks(context.Context, []*types.Chunk) error {
	s.createCalls++
	return nil
}

type projectionManualGraphRepo struct {
	interfaces.RetrieveGraphRepository
	deleteCalls int
}

func (r *projectionManualGraphRepo) DelGraph(context.Context, []types.NameSpace) error {
	r.deleteCalls++
	return nil
}

type projectionManualModelService struct {
	interfaces.ModelService
	err   error
	calls int
}

func (s *projectionManualModelService) GetEmbeddingModel(context.Context, string) (embedding.Embedder, error) {
	s.calls++
	return nil, s.err
}

type projectionManualTaskEnqueuer struct {
	calls int
}

func (e *projectionManualTaskEnqueuer) Enqueue(*asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error) {
	e.calls++
	return &asynq.TaskInfo{ID: "task-1"}, nil
}

type projectionManualReleaseRepo struct {
	interfaces.ProductionReleaseRepository
	target *types.ProductionReleaseTarget
}

func (r *projectionManualReleaseRepo) GetTarget(context.Context, uint64, string) (*types.ProductionReleaseTarget, error) {
	return r.target, nil
}

func (r *projectionManualReleaseRepo) ClaimProjectionBuildGeneration(
	_ context.Context, targetID, knowledgeID string, expectedUpdatedAt time.Time,
) (bool, error) {
	return r.target != nil && r.target.ID == targetID && r.target.KnowledgeID == knowledgeID &&
		r.target.Status == types.ReleaseTargetBuilding && r.target.UpdatedAt.Equal(expectedUpdatedAt), nil
}

type projectionManualFixture struct {
	service   *knowledgeService
	knowledge *types.Knowledge
	repo      *projectionManualKnowledgeRepo
	chunks    *projectionManualChunkService
	chunkRepo *projectionManualChunkRepo
	graph     *projectionManualGraphRepo
	model     *projectionManualModelService
	tenant    *projectionManualTenantRepo
	tasks     *projectionManualTaskEnqueuer
}

func newProjectionManualFixture(t *testing.T, snapshotStore, liveStore string, modelErr error) *projectionManualFixture {
	t.Helper()
	knowledge := initialPostProcessProjectionKnowledge(t, types.ParseStatusPending)
	meta, err := knowledge.ManualMetadata()
	require.NoError(t, err)
	meta.ProductionProjection.IndexingStrategy = types.IndexingStrategy{VectorEnabled: true}
	require.NoError(t, knowledge.SetManualMetadata(meta))
	snapshot, digest, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(
		`{"version":1,"indexing_strategy":{"vector_enabled":true},"vector_store_id":"` + snapshotStore + `","storage_backend_id":"backend-retained","storage_provider":"local"}`,
	))
	require.NoError(t, err)
	kb := &types.KnowledgeBase{
		ID: knowledge.KnowledgeBaseID, TenantID: knowledge.TenantID,
		EmbeddingModelID: "embedding-1", VectorStoreID: &liveStore,
		IndexingStrategy: types.IndexingStrategy{VectorEnabled: true},
	}
	repo := &projectionManualKnowledgeRepo{knowledge: knowledge}
	chunkRepo := &projectionManualChunkRepo{}
	chunks := &projectionManualChunkService{repo: chunkRepo}
	graph := &projectionManualGraphRepo{}
	model := &projectionManualModelService{err: modelErr}
	tenant := &projectionManualTenantRepo{tenant: &types.Tenant{ID: knowledge.TenantID}}
	tasks := &projectionManualTaskEnqueuer{}
	target := &types.ProductionReleaseTarget{
		ID: meta.ProductionProjection.ReleaseTargetID, TenantID: knowledge.TenantID,
		KnowledgeID: knowledge.ID, TargetKnowledgeBaseID: knowledge.KnowledgeBaseID,
		DocumentID: meta.ProductionProjection.DocumentID, VersionID: meta.ProductionProjection.VersionID,
		Status: types.ReleaseTargetBuilding, ConfigSnapshot: snapshot, ConfigDigest: digest,
		CreatedAt: projectionManualGeneration, UpdatedAt: projectionManualGeneration,
	}
	return &projectionManualFixture{
		service: &knowledgeService{
			repo: repo, kbService: &projectionManualKBService{kb: kb}, tenantRepo: tenant,
			chunkService: chunks, graphEngine: graph, modelService: model, task: tasks,
			storageResolver: &generationFenceStorageResolver{
				file: &generationFenceFileService{}, resolvedProvider: "local",
			},
			productionReleaseRepo: &projectionManualReleaseRepo{target: target},
		},
		knowledge: knowledge, repo: repo, chunks: chunks, chunkRepo: chunkRepo,
		graph: graph, model: model, tenant: tenant, tasks: tasks,
	}
}

func projectionManualTask(t *testing.T, knowledge *types.Knowledge, needCleanup bool) *asynq.Task {
	t.Helper()
	payload, err := json.Marshal(types.ManualProcessPayload{
		TenantID: knowledge.TenantID, KnowledgeID: knowledge.ID,
		KnowledgeBaseID: knowledge.KnowledgeBaseID, Content: "# governed projection", NeedCleanup: needCleanup,
		TargetUpdatedAt: projectionManualGeneration,
	})
	require.NoError(t, err)
	return asynq.NewTask(types.TypeManualProcess, payload)
}

func TestProductionProjectionManualUpdateRejectsRoutingDriftBeforeSideEffects(t *testing.T) {
	fixture := newProjectionManualFixture(t, "prepared-store", "live-store", errors.New("model must not be loaded"))

	err := fixture.service.ProcessManualUpdate(
		context.Background(), projectionManualTask(t, fixture.knowledge, true),
	)

	require.ErrorIs(t, err, types.ErrProductionReleaseReprepareRequired)
	require.Zero(t, fixture.repo.updates)
	require.Zero(t, fixture.chunks.deleteCalls)
	require.Zero(t, fixture.chunks.createCalls)
	require.Zero(t, fixture.chunkRepo.imageListCalls)
	require.Zero(t, fixture.graph.deleteCalls)
	require.Zero(t, fixture.model.calls)
	require.Zero(t, fixture.tenant.adjustCalls)
	require.Zero(t, fixture.tasks.calls)
	require.Equal(t, types.ParseStatusPending, fixture.knowledge.ParseStatus)
}

func TestProductionProjectionManualUpdateEmbeddingLookupFailureIsRetryable(t *testing.T) {
	lookupErr := errors.New("embedding model lookup unavailable")
	fixture := newProjectionManualFixture(t, "live-store", "live-store", lookupErr)
	task := projectionManualTask(t, fixture.knowledge, false)

	firstErr := fixture.service.ProcessManualUpdate(context.Background(), task)
	firstStatus := fixture.knowledge.ParseStatus
	secondErr := fixture.service.ProcessManualUpdate(context.Background(), task)

	require.ErrorIs(t, firstErr, lookupErr)
	require.Equal(t, types.ParseStatusFailed, firstStatus)
	require.ErrorIs(t, secondErr, lookupErr)
	require.Equal(t, types.ParseStatusFailed, fixture.knowledge.ParseStatus)
	require.Equal(t, 2, fixture.model.calls, "a failed projection remains reachable by Asynq retry")
	require.Equal(t, []string{
		types.ParseStatusFailed, types.ParseStatusFailed,
	}, fixture.repo.statuses)
	require.Zero(t, fixture.chunks.deleteCalls)
	require.Zero(t, fixture.chunks.createCalls)
	require.Zero(t, fixture.graph.deleteCalls)
	require.Zero(t, fixture.tasks.calls)
}
