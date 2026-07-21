package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type generationFenceKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	mu        sync.Mutex
	knowledge *types.Knowledge
	updates   int
}

func (r *generationFenceKnowledgeRepo) GetKnowledgeByID(context.Context, uint64, string) (*types.Knowledge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copyKnowledge := *r.knowledge
	return &copyKnowledge, nil
}

func (r *generationFenceKnowledgeRepo) UpdateKnowledge(_ context.Context, knowledge *types.Knowledge) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates++
	copyKnowledge := *knowledge
	r.knowledge = &copyKnowledge
	return nil
}

func (r *generationFenceKnowledgeRepo) setStatus(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.knowledge.ParseStatus = status
	if status != types.ParseStatusFailed {
		r.knowledge.ErrorMessage = ""
	}
}

func (r *generationFenceKnowledgeRepo) status() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.knowledge.ParseStatus
}

type generationFenceChunkRepo struct {
	interfaces.ChunkRepository
	chunks         []*types.Chunk
	imageInfos     []interfaces.ChunkImageInfo
	createCalls    int
	deleteCalls    int
	imageReadCalls int
}

func (r *generationFenceChunkRepo) CreateChunks(_ context.Context, chunks []*types.Chunk) error {
	r.createCalls++
	for _, chunk := range chunks {
		copyChunk := *chunk
		r.chunks = append(r.chunks, &copyChunk)
	}
	return nil
}

func (r *generationFenceChunkRepo) DeleteChunksByKnowledgeID(_ context.Context, _ uint64, knowledgeID string) error {
	r.deleteCalls++
	retained := r.chunks[:0]
	for _, chunk := range r.chunks {
		if chunk.KnowledgeID != knowledgeID {
			retained = append(retained, chunk)
		}
	}
	r.chunks = retained
	return nil
}

func (r *generationFenceChunkRepo) ListImageInfoByKnowledgeIDs(
	context.Context, uint64, []string,
) ([]interfaces.ChunkImageInfo, error) {
	r.imageReadCalls++
	return r.imageInfos, nil
}

type generationFenceTenantRepo struct {
	interfaces.TenantRepository
	tenant      *types.Tenant
	adjustCalls int
}

func (r *generationFenceTenantRepo) GetTenantByID(context.Context, uint64) (*types.Tenant, error) {
	return r.tenant, nil
}

func (r *generationFenceTenantRepo) AdjustStorageUsed(context.Context, uint64, int64) error {
	r.adjustCalls++
	return nil
}

type generationFenceVectorEngine struct {
	interfaces.RetrieveEngineService
	deleteCalls int
}

func (e *generationFenceVectorEngine) EngineType() types.RetrieverEngineType {
	return types.PostgresRetrieverEngineType
}

func (e *generationFenceVectorEngine) Support() []types.RetrieverType {
	return []types.RetrieverType{types.VectorRetrieverType}
}

func (e *generationFenceVectorEngine) DeleteByKnowledgeIDList(
	context.Context, []string, int, string,
) error {
	e.deleteCalls++
	return nil
}

type generationFenceVectorRegistry struct {
	interfaces.RetrieveEngineRegistry
	engine        interfaces.RetrieveEngineService
	byStoreCalls  int
	byEngineCalls int
}

func (r *generationFenceVectorRegistry) GetByStoreID(string) (interfaces.RetrieveEngineService, error) {
	r.byStoreCalls++
	return r.engine, nil
}

func (r *generationFenceVectorRegistry) GetRetrieveEngineService(
	types.RetrieverEngineType,
) (interfaces.RetrieveEngineService, error) {
	r.byEngineCalls++
	return r.engine, nil
}

type generationFenceOwnership struct{ calls int }

func (o *generationFenceOwnership) StoreOwnedBy(context.Context, string, uint64) (bool, error) {
	o.calls++
	return true, nil
}

type generationFenceEmbedder struct{ embedding.Embedder }

func (generationFenceEmbedder) GetDimensions() int { return 3 }

type generationFenceModelService struct {
	interfaces.ModelService
	calls int
}

type generationFenceFileService struct {
	interfaces.FileService
	saveCalls   int
	deleteCalls int
}

func (s *generationFenceFileService) SaveBytes(
	context.Context, []byte, uint64, string, bool,
) (string, error) {
	s.saveCalls++
	return "local://1/projection/image.png", nil
}

func (s *generationFenceFileService) DeleteFile(context.Context, string) error {
	s.deleteCalls++
	return nil
}

type generationFenceStorageResolver struct {
	interfaces.StorageBackendResolver
	file             interfaces.FileService
	resolvedProvider string
	err              error
	calls            int
	backendID        string
	provider         string
}

func (r *generationFenceStorageResolver) ResolveFileService(
	_ context.Context,
	_ *types.Tenant,
	backendID string,
	provider string,
	_ string,
) (interfaces.FileService, string, error) {
	r.calls++
	r.backendID = backendID
	r.provider = provider
	if r.err != nil {
		return nil, "", r.err
	}
	return r.file, r.resolvedProvider, nil
}

func (s *generationFenceModelService) GetEmbeddingModel(context.Context, string) (embedding.Embedder, error) {
	s.calls++
	return generationFenceEmbedder{}, nil
}

type generationFenceReleaseRepo struct {
	interfaces.ProductionReleaseRepository
	mu               sync.Mutex
	target           *types.ProductionReleaseTarget
	knowledge        *generationFenceKnowledgeRepo
	advanceAfterRead bool
}

func (r *generationFenceReleaseRepo) GetTarget(context.Context, uint64, string) (*types.ProductionReleaseTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copyTarget := *r.target
	if r.advanceAfterRead {
		r.advanceAfterRead = false
		r.target.UpdatedAt = r.target.UpdatedAt.Add(time.Second)
	}
	return &copyTarget, nil
}

func (r *generationFenceReleaseRepo) ClaimProjectionBuildGeneration(
	_ context.Context, targetID, knowledgeID string, expectedUpdatedAt time.Time,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	claimed := r.target != nil && r.target.ID == targetID && r.target.KnowledgeID == knowledgeID &&
		r.target.Status == types.ReleaseTargetBuilding && r.target.UpdatedAt.Equal(expectedUpdatedAt)
	if claimed && r.knowledge != nil {
		r.knowledge.setStatus(types.ParseStatusProcessing)
	}
	return claimed, nil
}

func (r *generationFenceReleaseRepo) advanceRetryGeneration(next time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.knowledge == nil || r.knowledge.status() != types.ParseStatusFailed ||
		r.target.Status != types.ReleaseTargetBuilding {
		return false
	}
	r.target.UpdatedAt = next
	r.knowledge.setStatus(types.ParseStatusPending)
	return true
}

func (r *generationFenceReleaseRepo) ResolveScopesForKnowledgeIDs(
	_ context.Context, _ uint64, knowledgeIDs []string,
) (map[string]types.ProductionKnowledgeScope, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return map[string]types.ProductionKnowledgeScope{
		r.target.TargetKnowledgeBaseID: {
			AllProductionKnowledgeIDs: []string{r.target.KnowledgeID},
			InactiveKnowledgeIDs:      []string{r.target.KnowledgeID},
		},
	}, nil
}

type generationFenceKBService struct {
	interfaces.KnowledgeBaseService
	kb            *types.KnowledgeBase
	secondReadKB  *types.KnowledgeBase
	secondReadErr error
	calls         int
}

func (s *generationFenceKBService) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	s.calls++
	if s.calls > 1 {
		if s.secondReadErr != nil {
			return nil, s.secondReadErr
		}
		if s.secondReadKB != nil {
			return s.secondReadKB, nil
		}
	}
	return s.kb, nil
}

type generationFenceGraphRepo struct {
	interfaces.RetrieveGraphRepository
	deleteCalls int
}

func (r *generationFenceGraphRepo) DelGraph(context.Context, []types.NameSpace) error {
	r.deleteCalls++
	return nil
}

type generationFenceFixture struct {
	service     *knowledgeService
	knowledge   *types.Knowledge
	knowledgeR  *generationFenceKnowledgeRepo
	chunks      *generationFenceChunkRepo
	kbs         *generationFenceKBService
	graph       *generationFenceGraphRepo
	tenant      *generationFenceTenantRepo
	vectors     *generationFenceVectorRegistry
	vector      *generationFenceVectorEngine
	ownership   *generationFenceOwnership
	model       *generationFenceModelService
	files       *generationFenceFileService
	strictFiles *generationFenceFileService
	storage     *generationFenceStorageResolver
	releases    *generationFenceReleaseRepo
	tasks       *initialPostProcessEnqueuer
	generation  time.Time
}

func newGenerationFenceFixture(t *testing.T) *generationFenceFixture {
	t.Helper()
	t.Setenv("RETRIEVE_DRIVER", "")
	knowledge := initialPostProcessProjectionKnowledge(t, types.ParseStatusPending)
	generation := time.Date(2026, 7, 22, 1, 0, 0, 123456000, time.UTC)
	snapshot, digest, err := types.CanonicalProductionReleaseTargetConfig(
		types.JSON(`{"version":1,"indexing_strategy":{"wiki_enabled":true},"storage_backend_id":"backend-retained","storage_provider":"local"}`),
	)
	require.NoError(t, err)
	meta, err := knowledge.ManualMetadata()
	require.NoError(t, err)
	target := &types.ProductionReleaseTarget{
		ID: meta.ProductionProjection.ReleaseTargetID, TenantID: knowledge.TenantID,
		KnowledgeID: knowledge.ID, TargetKnowledgeBaseID: knowledge.KnowledgeBaseID,
		DocumentID: meta.ProductionProjection.DocumentID, VersionID: meta.ProductionProjection.VersionID,
		Status: types.ReleaseTargetBuilding, ConfigSnapshot: snapshot, ConfigDigest: digest,
		CreatedAt: generation, UpdatedAt: generation,
	}
	chunkRepo := &generationFenceChunkRepo{}
	knowledgeRepo := &generationFenceKnowledgeRepo{knowledge: knowledge}
	releases := &generationFenceReleaseRepo{target: target, knowledge: knowledgeRepo}
	chunkService := &chunkService{chunkRepository: chunkRepo, productionReleaseRepo: releases}
	kbs := &generationFenceKBService{kb: &types.KnowledgeBase{
		ID: knowledge.KnowledgeBaseID, TenantID: knowledge.TenantID,
		IndexingStrategy: types.IndexingStrategy{WikiEnabled: true},
	}}
	graph := &generationFenceGraphRepo{}
	tenant := &generationFenceTenantRepo{tenant: &types.Tenant{ID: knowledge.TenantID}}
	vector := &generationFenceVectorEngine{}
	vectors := &generationFenceVectorRegistry{engine: vector}
	ownership := &generationFenceOwnership{}
	model := &generationFenceModelService{}
	files := &generationFenceFileService{}
	strictFiles := &generationFenceFileService{}
	storage := &generationFenceStorageResolver{file: strictFiles, resolvedProvider: "local"}
	tasks := &initialPostProcessEnqueuer{}
	return &generationFenceFixture{
		service: &knowledgeService{
			repo: knowledgeRepo, kbService: kbs,
			tenantRepo: tenant, chunkService: chunkService,
			graphEngine: graph, task: tasks, retrieveEngine: vectors,
			ownership: ownership, modelService: model, fileSvc: files,
			storageResolver:       storage,
			productionReleaseRepo: releases,
		},
		knowledge: knowledge, knowledgeR: knowledgeRepo, chunks: chunkRepo,
		kbs: kbs, graph: graph, tenant: tenant, vectors: vectors, vector: vector,
		ownership: ownership, model: model, files: files,
		strictFiles: strictFiles, storage: storage,
		releases: releases, tasks: tasks, generation: generation,
	}
}

func generationFenceManualTask(
	t *testing.T,
	knowledge *types.Knowledge,
	generation time.Time,
	needCleanup bool,
) *asynq.Task {
	return generationFenceManualTaskWithContent(
		t, knowledge, generation, needCleanup, "# governed projection",
	)
}

func generationFenceManualTaskWithContent(
	t *testing.T,
	knowledge *types.Knowledge,
	generation time.Time,
	needCleanup bool,
	content string,
) *asynq.Task {
	t.Helper()
	payload := map[string]any{
		"tenant_id": knowledge.TenantID, "knowledge_id": knowledge.ID,
		"knowledge_base_id": knowledge.KnowledgeBaseID, "content": content,
		"need_cleanup": needCleanup, "target_updated_at": generation,
	}
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	return asynq.NewTask(types.TypeManualProcess, encoded)
}

func configureGenerationFenceStorageFailure(
	fixture *generationFenceFixture,
	mode string,
	failure error,
) {
	switch mode {
	case "unavailable":
		fixture.service.storageResolver = nil
	case "error":
		fixture.storage.err = failure
	case "nil-service":
		fixture.storage.file = nil
	case "wrong-provider":
		fixture.storage.resolvedProvider = "cos"
	default:
		panic("unknown storage failure mode: " + mode)
	}
}

func TestProductionProjectionManualImageResolutionFailsClosedOnRetainedStorage(t *testing.T) {
	for _, mode := range []string{"unavailable", "error", "nil-service", "wrong-provider"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newGenerationFenceFixture(t)
			storageErr := errors.New("retained storage resolution failed")
			configureGenerationFenceStorageFailure(fixture, mode, storageErr)
			fixture.service.imageResolver = docparser.NewImageResolver()
			content := "![projection](data:image/png;base64,iVBORw0KGgo=)"

			err := fixture.service.ProcessManualUpdate(
				context.Background(),
				generationFenceManualTaskWithContent(
					t, fixture.knowledge, fixture.generation, false, content,
				),
			)

			require.Error(t, err)
			if mode == "error" {
				require.ErrorIs(t, err, storageErr)
			}
			if mode == "unavailable" {
				require.Zero(t, fixture.storage.calls)
			} else {
				require.Equal(t, 1, fixture.storage.calls)
				require.Equal(t, "backend-retained", fixture.storage.backendID)
				require.Equal(t, "local", fixture.storage.provider)
			}
			require.Equal(t, types.ParseStatusFailed, fixture.knowledgeR.status())
			require.Zero(t, fixture.files.saveCalls, "default storage must never receive projection uploads")
			require.Zero(t, fixture.strictFiles.saveCalls)
			require.Zero(t, fixture.chunks.createCalls)
			require.Zero(t, fixture.chunks.deleteCalls)
			require.Zero(t, fixture.graph.deleteCalls)
			require.Empty(t, fixture.tasks.payloads)
		})
	}
}

func TestProductionProjectionCleanupFailsClosedOnRetainedStorage(t *testing.T) {
	for _, mode := range []string{"unavailable", "error", "nil-service", "wrong-provider"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newGenerationFenceFixture(t)
			storageErr := errors.New("retained storage resolution failed")
			configureGenerationFenceStorageFailure(fixture, mode, storageErr)
			fixture.knowledgeR.setStatus(types.ParseStatusFailed)
			fixture.knowledge.StorageSize = 64
			fixture.chunks.imageInfos = []interfaces.ChunkImageInfo{{
				KnowledgeID: fixture.knowledge.ID,
				ImageInfo:   `[{"url":"local://1/projection/image.png"}]`,
			}}

			err := fixture.service.ProcessManualUpdate(
				context.Background(),
				generationFenceManualTask(t, fixture.knowledge, fixture.generation, true),
			)

			require.Error(t, err)
			if mode == "error" {
				require.ErrorIs(t, err, storageErr)
			}
			if mode == "unavailable" {
				require.Zero(t, fixture.storage.calls)
			} else {
				require.Equal(t, 1, fixture.storage.calls)
				require.Equal(t, "backend-retained", fixture.storage.backendID)
				require.Equal(t, "local", fixture.storage.provider)
			}
			require.Equal(t, types.ParseStatusFailed, fixture.knowledgeR.status())
			require.Zero(t, fixture.ownership.calls)
			require.Zero(t, fixture.vectors.byStoreCalls)
			require.Zero(t, fixture.vector.deleteCalls)
			require.Zero(t, fixture.model.calls)
			require.Zero(t, fixture.chunks.imageReadCalls)
			require.Zero(t, fixture.chunks.createCalls)
			require.Zero(t, fixture.chunks.deleteCalls)
			require.Zero(t, fixture.files.deleteCalls, "default storage must never receive projection deletes")
			require.Zero(t, fixture.strictFiles.deleteCalls)
			require.Zero(t, fixture.graph.deleteCalls)
			require.Zero(t, fixture.tenant.adjustCalls)
			require.Empty(t, fixture.tasks.payloads)
		})
	}
}

func TestProductionProjectionManualRetryCleansPersistedChunksWithTrustedWorker(t *testing.T) {
	fixture := newGenerationFenceFixture(t)
	queueErr := errors.New("post-process enqueue unavailable")
	fixture.tasks.errors = []error{queueErr, nil}
	task := generationFenceManualTask(t, fixture.knowledge, fixture.generation, false)

	firstErr := fixture.service.ProcessManualUpdate(context.Background(), task)
	secondErr := fixture.service.ProcessManualUpdate(context.Background(), task)

	require.ErrorIs(t, firstErr, queueErr)
	require.NoError(t, secondErr)
	require.Equal(t, types.ParseStatusProcessing, fixture.knowledgeR.status())
	require.Len(t, fixture.chunks.chunks, 1, "retry must replace prior chunks instead of appending duplicates")
	require.GreaterOrEqual(t, fixture.chunks.deleteCalls, 1)
}

func TestProductionProjectionExplicitCleanupUsesTrustedWorkerAndCompletes(t *testing.T) {
	fixture := newGenerationFenceFixture(t)
	fixture.knowledgeR.setStatus(types.ParseStatusFailed)
	fixture.chunks.chunks = []*types.Chunk{{
		ID: "stale-chunk", TenantID: fixture.knowledge.TenantID,
		KnowledgeID: fixture.knowledge.ID, KnowledgeBaseID: fixture.knowledge.KnowledgeBaseID,
	}}

	err := fixture.service.ProcessManualUpdate(
		context.Background(), generationFenceManualTask(t, fixture.knowledge, fixture.generation, true),
	)

	require.NoError(t, err)
	require.Equal(t, types.ParseStatusProcessing, fixture.knowledgeR.status())
	require.Len(t, fixture.chunks.chunks, 1)
	require.NotEqual(t, "stale-chunk", fixture.chunks.chunks[0].ID)
	require.GreaterOrEqual(t, fixture.chunks.deleteCalls, 1)
	require.Equal(t, 1, fixture.kbs.calls, "authenticated cleanup must not reload the live KB")
}

func TestProductionProjectionManualTaskRejectsStaleGenerationBeforeWrites(t *testing.T) {
	fixture := newGenerationFenceFixture(t)
	staleGeneration := fixture.generation.Add(-time.Second)

	err := fixture.service.ProcessManualUpdate(
		context.Background(), generationFenceManualTask(t, fixture.knowledge, staleGeneration, false),
	)

	require.ErrorIs(t, err, asynq.SkipRetry)
	require.Zero(t, fixture.knowledgeR.updates)
	require.Zero(t, fixture.chunks.createCalls)
	require.Zero(t, fixture.chunks.deleteCalls)
	require.Zero(t, fixture.graph.deleteCalls)
	require.Equal(t, types.ParseStatusPending, fixture.knowledgeR.status())
}

func TestProductionProjectionManualTaskRejectsGenerationAdvancedAfterPreflight(t *testing.T) {
	fixture := newGenerationFenceFixture(t)
	storeID := "prepared-vector-store"
	snapshot, digest, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(
		`{"version":1,"indexing_strategy":{"vector_enabled":true},"vector_store_id":"prepared-vector-store"}`,
	))
	require.NoError(t, err)
	fixture.releases.target.ConfigSnapshot = snapshot
	fixture.releases.target.ConfigDigest = digest
	fixture.kbs.kb.VectorStoreID = &storeID
	fixture.knowledge.EmbeddingModelID = "embedding-1"
	fixture.knowledge.StorageSize = 64
	fixture.chunks.imageInfos = []interfaces.ChunkImageInfo{{
		KnowledgeID: fixture.knowledge.ID,
		ImageInfo:   `[{"url":"local://1/projection/image.png"}]`,
	}}
	fixture.releases.advanceAfterRead = true
	fixture.kbs.secondReadErr = errors.New("live KB changed after authenticated preflight")

	err = fixture.service.ProcessManualUpdate(
		context.Background(), generationFenceManualTask(t, fixture.knowledge, fixture.generation, true),
	)

	require.ErrorIs(t, err, asynq.SkipRetry)
	require.Equal(t, 1, fixture.kbs.calls, "generation claim must fence the task before cleanup can reload the KB")
	require.Zero(t, fixture.knowledgeR.updates)
	require.Zero(t, fixture.ownership.calls)
	require.Zero(t, fixture.vectors.byStoreCalls)
	require.Zero(t, fixture.vectors.byEngineCalls)
	require.Zero(t, fixture.vector.deleteCalls)
	require.Zero(t, fixture.model.calls)
	require.Zero(t, fixture.chunks.imageReadCalls)
	require.Zero(t, fixture.chunks.createCalls)
	require.Zero(t, fixture.chunks.deleteCalls)
	require.Zero(t, fixture.files.deleteCalls)
	require.Zero(t, fixture.graph.deleteCalls)
	require.Zero(t, fixture.tenant.adjustCalls)
	require.Equal(t, types.ParseStatusPending, fixture.knowledgeR.status())
}

func TestProductionProjectionExplicitCleanupDoesNotReloadDriftedLiveKB(t *testing.T) {
	fixture := newGenerationFenceFixture(t)
	fixture.knowledgeR.setStatus(types.ParseStatusFailed)
	fixture.chunks.chunks = []*types.Chunk{{
		ID: "stale-chunk", TenantID: fixture.knowledge.TenantID,
		KnowledgeID: fixture.knowledge.ID, KnowledgeBaseID: fixture.knowledge.KnowledgeBaseID,
	}}
	fixture.kbs.secondReadKB = &types.KnowledgeBase{
		ID: fixture.knowledge.KnowledgeBaseID, TenantID: fixture.knowledge.TenantID,
		IndexingStrategy: types.IndexingStrategy{VectorEnabled: true},
	}
	ctx := context.WithValue(context.Background(), types.UserIDContextKey, types.ProductionSystemActorID)

	err := fixture.service.ProcessManualUpdate(
		ctx, generationFenceManualTask(t, fixture.knowledge, fixture.generation, true),
	)

	require.NoError(t, err)
	require.Equal(t, 1, fixture.kbs.calls, "cleanup must use the authenticated snapshot instead of a second live KB read")
}

func TestProductionProjectionManualGenerationFenceSerializesWorkerAndRetry(t *testing.T) {
	const iterations = 100
	for i := 0; i < iterations; i++ {
		fixture := newGenerationFenceFixture(t)
		fixture.knowledgeR.setStatus(types.ParseStatusFailed)
		task := generationFenceManualTask(t, fixture.knowledge, fixture.generation, true)
		nextGeneration := fixture.generation.Add(time.Duration(i+1) * time.Second)
		start := make(chan struct{})
		workerErr := make(chan error, 1)
		retryWon := make(chan bool, 1)
		go func() {
			<-start
			workerErr <- fixture.service.ProcessManualUpdate(context.Background(), task)
		}()
		go func() {
			<-start
			retryWon <- fixture.releases.advanceRetryGeneration(nextGeneration)
		}()
		close(start)
		err := <-workerErr
		advanced := <-retryWon

		if advanced {
			require.ErrorIs(t, err, asynq.SkipRetry)
			require.Zero(t, fixture.chunks.createCalls)
			require.Zero(t, fixture.chunks.deleteCalls)
			require.Equal(t, types.ParseStatusPending, fixture.knowledgeR.status())
			continue
		}
		require.NoError(t, err)
		require.Equal(t, 1, fixture.chunks.createCalls)
		require.Equal(t, types.ParseStatusProcessing, fixture.knowledgeR.status())
	}
}
