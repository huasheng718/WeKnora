package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type projectionPostProcessKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	knowledge *types.Knowledge
	updates   map[string]interface{}
}

func (r *projectionPostProcessKnowledgeRepo) GetKnowledgeByIDOnly(context.Context, string) (*types.Knowledge, error) {
	copyKnowledge := *r.knowledge
	return &copyKnowledge, nil
}

func (r *projectionPostProcessKnowledgeRepo) UpdateKnowledgeColumns(_ context.Context, _ string, values map[string]interface{}) error {
	for key, value := range values {
		r.updates[key] = value
	}
	if status, ok := values["parse_status"].(string); ok {
		r.knowledge.ParseStatus = status
	}
	return nil
}

func (r *projectionPostProcessKnowledgeRepo) UpdateKnowledgeColumn(_ context.Context, _ string, column string, value interface{}) error {
	r.updates[column] = value
	return nil
}

func (r *projectionPostProcessKnowledgeRepo) SetFinalizing(_ context.Context, _ string, count int) (bool, error) {
	r.knowledge.ParseStatus = types.ParseStatusFinalizing
	r.knowledge.PendingSubtasksCount = count
	return true, nil
}

func (r *projectionPostProcessKnowledgeRepo) FinalizeSubtask(context.Context, string) (int, bool, error) {
	if r.knowledge.ParseStatus != types.ParseStatusFinalizing || r.knowledge.PendingSubtasksCount < 1 {
		return r.knowledge.PendingSubtasksCount, false, nil
	}
	r.knowledge.PendingSubtasksCount--
	if r.knowledge.PendingSubtasksCount == 0 {
		r.knowledge.ParseStatus = types.ParseStatusCompleted
		return 0, true, nil
	}
	return r.knowledge.PendingSubtasksCount, false, nil
}

type projectionPostProcessKBService struct {
	interfaces.KnowledgeBaseService
	kb *types.KnowledgeBase
}

func (s *projectionPostProcessKBService) GetKnowledgeBaseByIDOnly(context.Context, string) (*types.KnowledgeBase, error) {
	copyKB := *s.kb
	return &copyKB, nil
}

type projectionPostProcessChunkService struct {
	interfaces.ChunkService
	chunks []*types.Chunk
}

func (s *projectionPostProcessChunkService) ListChunksByKnowledgeID(context.Context, string) ([]*types.Chunk, error) {
	return s.chunks, nil
}

type projectionPostProcessEnqueuer struct {
	taskTypes  []string
	taskIDs    []string
	tasks      []*asynq.Task
	enqueueErr error
}

func (e *projectionPostProcessEnqueuer) Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	e.taskTypes = append(e.taskTypes, task.Type())
	e.tasks = append(e.tasks, task)
	for _, opt := range opts {
		if opt.Type() == asynq.TaskIDOpt {
			e.taskIDs = append(e.taskIDs, opt.Value().(string))
		}
	}
	if e.enqueueErr != nil {
		err := e.enqueueErr
		e.enqueueErr = nil
		return nil, err
	}
	return &asynq.TaskInfo{ID: "task-1"}, nil
}

type projectionPostProcessReleaseRepo struct {
	interfaces.ProductionReleaseRepository
	readyTransitions int
	status           types.ProductionReleaseTargetStatus
	mu               sync.Mutex
	transitionErr    error
}

func (r *projectionPostProcessReleaseRepo) GetTarget(
	_ context.Context, _ uint64, targetID string,
) (*types.ProductionReleaseTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := r.status
	if status == "" {
		status = types.ReleaseTargetBuilding
	}
	return &types.ProductionReleaseTarget{
		ID: targetID, TenantID: projectionTenantID, KnowledgeID: projectionKnowledgeID,
		DocumentID: projectionDocumentID, VersionID: projectionVersionID,
		TargetKnowledgeBaseID: projectionKBID, Status: status,
	}, nil
}

func (r *projectionPostProcessReleaseRepo) TransitionTarget(
	_ context.Context, targetID string, from, to types.ProductionReleaseTargetStatus, patch types.JSONMap,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.transitionErr != nil {
		err := r.transitionErr
		r.transitionErr = nil
		return false, err
	}
	if targetID == projectionTargetID && from == types.ReleaseTargetBuilding &&
		to == types.ReleaseTargetReady && len(patch) == 0 &&
		(r.status == "" || r.status == types.ReleaseTargetBuilding) {
		r.readyTransitions++
		r.status = types.ReleaseTargetReady
		return true, nil
	}
	return false, nil
}

func productionProjectionKnowledgeForPostProcess(t *testing.T) *types.Knowledge {
	t.Helper()
	knowledge := &types.Knowledge{
		ID: projectionKnowledgeID, TenantID: projectionTenantID,
		KnowledgeBaseID: projectionKBID, Type: types.KnowledgeTypeManual,
		ParseStatus: types.ParseStatusProcessing, EmbeddingModelID: "snapshot-embedding",
	}
	meta := types.NewManualKnowledgeMetadata("# baseline", types.ManualKnowledgeStatusPublish, 1)
	meta.ProductionProjection = &types.ProductionProjectionMetadata{
		DocumentID: projectionDocumentID, VersionID: projectionVersionID,
		ReleaseTargetID: projectionTargetID, ContentDigest: projectionKnowledgeContentDigest("# baseline"),
		SummaryModelID:   "snapshot-summary",
		IndexingStrategy: types.IndexingStrategy{VectorEnabled: true, KeywordEnabled: true},
	}
	require.NoError(t, knowledge.SetManualMetadata(meta))
	return knowledge
}

func projectionPostProcessTask(t *testing.T) *asynq.Task {
	t.Helper()
	payload, err := json.Marshal(types.KnowledgePostProcessPayload{
		TenantID: projectionTenantID, KnowledgeBaseID: projectionKBID,
		KnowledgeID: projectionKnowledgeID,
	})
	require.NoError(t, err)
	return asynq.NewTask(types.TypeKnowledgePostProcess, payload)
}

func TestProductionProjectionBuildDoesNotEnqueueWikiBeforeActivation(t *testing.T) {
	knowledgeRepo := &projectionPostProcessKnowledgeRepo{
		knowledge: productionProjectionKnowledgeForPostProcess(t), updates: map[string]interface{}{},
	}
	enqueuer := &projectionPostProcessEnqueuer{}
	releases := &projectionPostProcessReleaseRepo{}
	handler := NewKnowledgePostProcessService(
		knowledgeRepo,
		&projectionPostProcessKBService{kb: &types.KnowledgeBase{
			ID: projectionKBID, TenantID: projectionTenantID,
			IndexingStrategy: types.IndexingStrategy{WikiEnabled: true},
		}},
		&projectionPostProcessChunkService{chunks: []*types.Chunk{{
			ID: "chunk-1", KnowledgeID: projectionKnowledgeID, ChunkType: types.ChunkTypeText,
		}}},
		enqueuer, nil, nil, nil, releases,
	)

	require.NoError(t, handler.Handle(context.Background(), projectionPostProcessTask(t)))
	require.NotContains(t, enqueuer.taskTypes, types.TypeWikiIngest)
	require.Equal(t, 1, knowledgeRepo.knowledge.PendingSubtasksCount,
		"only summary remains; suppressed Wiki must not reserve a completion slot")
	require.Zero(t, releases.readyTransitions, "summary work has not completed")
}

func TestProductionProjectionPostProcessMarksTargetReadyAfterKnowledgeCompletes(t *testing.T) {
	knowledgeRepo := &projectionPostProcessKnowledgeRepo{
		knowledge: productionProjectionKnowledgeForPostProcess(t), updates: map[string]interface{}{},
	}
	releases := &projectionPostProcessReleaseRepo{}
	handler := NewKnowledgePostProcessService(
		knowledgeRepo,
		&projectionPostProcessKBService{kb: &types.KnowledgeBase{
			ID: projectionKBID, TenantID: projectionTenantID,
		}},
		&projectionPostProcessChunkService{}, &projectionPostProcessEnqueuer{},
		nil, nil, nil, releases,
	)

	require.NoError(t, handler.Handle(context.Background(), projectionPostProcessTask(t)))
	require.Equal(t, types.ParseStatusCompleted, knowledgeRepo.knowledge.ParseStatus)
	require.Equal(t, 1, releases.readyTransitions)

	// Delivery is idempotent: the completed Knowledge no longer enters the
	// processing fast path and cannot produce a second lifecycle transition.
	require.NoError(t, handler.Handle(context.Background(), projectionPostProcessTask(t)))
	require.Equal(t, 1, releases.readyTransitions)
}

func TestProductionProjectionFinalGraphDrainMarksTargetReady(t *testing.T) {
	knowledgeRepo := &projectionPostProcessKnowledgeRepo{
		knowledge: productionProjectionKnowledgeForPostProcess(t), updates: map[string]interface{}{},
	}
	knowledgeRepo.knowledge.ParseStatus = types.ParseStatusFinalizing
	knowledgeRepo.knowledge.PendingSubtasksCount = 1
	releases := &projectionPostProcessReleaseRepo{}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, projectionTenantID)

	err := finalizeSubtaskDetached(
		ctx, knowledgeRepo, releases, projectionKnowledgeID, "graph_chunk[0]", nil, false, true,
	)
	require.NoError(t, err)
	require.Equal(t, types.ParseStatusCompleted, knowledgeRepo.knowledge.ParseStatus)
	require.Equal(t, 1, releases.readyTransitions)

	// Duplicate terminal delivery cannot promote either Knowledge or target twice.
	require.NoError(t, finalizeSubtaskDetached(
		ctx, knowledgeRepo, releases, projectionKnowledgeID, "graph_chunk[0]", nil, false, true,
	))
	require.Equal(t, 1, releases.readyTransitions)
}

func TestProductionProjectionReadinessFailureRetriesAfterKnowledgeAlreadyCompleted(t *testing.T) {
	knowledgeRepo := &projectionPostProcessKnowledgeRepo{
		knowledge: productionProjectionKnowledgeForPostProcess(t), updates: map[string]interface{}{},
	}
	knowledgeRepo.knowledge.ParseStatus = types.ParseStatusFinalizing
	knowledgeRepo.knowledge.PendingSubtasksCount = 1
	releases := &projectionPostProcessReleaseRepo{transitionErr: errors.New("ready CAS unavailable")}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, projectionTenantID)

	err := finalizeSubtaskDetached(ctx, knowledgeRepo, releases, projectionKnowledgeID, "summary", nil, false, true)
	require.ErrorContains(t, err, "ready CAS unavailable")
	require.Equal(t, types.ParseStatusCompleted, knowledgeRepo.knowledge.ParseStatus)

	require.NoError(t, finalizeSubtaskDetached(ctx, knowledgeRepo, releases, projectionKnowledgeID, "summary", nil, false, true))
	require.Equal(t, 1, releases.readyTransitions)
}

func TestProductionProjectionConcurrentReadinessReconciliationIsIdempotent(t *testing.T) {
	knowledgeRepo := &projectionPostProcessKnowledgeRepo{
		knowledge: productionProjectionKnowledgeForPostProcess(t), updates: map[string]interface{}{},
	}
	knowledgeRepo.knowledge.ParseStatus = types.ParseStatusCompleted
	releases := &projectionPostProcessReleaseRepo{}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, projectionTenantID)

	var wg sync.WaitGroup
	errorsSeen := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsSeen <- finalizeSubtaskDetached(ctx, knowledgeRepo, releases, projectionKnowledgeID, "replay", nil, false, true)
		}()
	}
	wg.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		require.NoError(t, err)
	}
	require.Equal(t, 1, releases.readyTransitions)
}

func TestProductionProjectionPostProcessEnqueueFailureRetriesDeterministically(t *testing.T) {
	knowledgeRepo := &projectionPostProcessKnowledgeRepo{
		knowledge: productionProjectionKnowledgeForPostProcess(t), updates: map[string]interface{}{},
	}
	enqueuer := &projectionPostProcessEnqueuer{enqueueErr: errors.New("queue unavailable")}
	handler := NewKnowledgePostProcessService(
		knowledgeRepo,
		&projectionPostProcessKBService{kb: &types.KnowledgeBase{
			ID: projectionKBID, TenantID: projectionTenantID,
			SummaryModelID: "live-summary", EmbeddingModelID: "live-embedding",
		}},
		&projectionPostProcessChunkService{chunks: []*types.Chunk{{
			ID: "chunk-1", KnowledgeID: projectionKnowledgeID, ChunkType: types.ChunkTypeText,
		}}},
		enqueuer, nil, nil, nil, &projectionPostProcessReleaseRepo{},
	)

	err := handler.Handle(context.Background(), projectionPostProcessTask(t))
	require.ErrorContains(t, err, "queue unavailable")
	require.Equal(t, types.ParseStatusFinalizing, knowledgeRepo.knowledge.ParseStatus)
	require.Equal(t, 1, knowledgeRepo.knowledge.PendingSubtasksCount)
	require.Equal(t, []string{
		"production-projection-summary-" + projectionTargetID + "-" + projectionKnowledgeID,
	}, enqueuer.taskIDs)

	require.NoError(t, handler.Handle(context.Background(), projectionPostProcessTask(t)))
	require.Len(t, enqueuer.taskIDs, 2)
	var payload types.SummaryGenerationPayload
	require.NoError(t, json.Unmarshal(enqueuer.tasks[1].Payload(), &payload))
	require.Equal(t, "snapshot-summary", payload.SummaryModelID)
	require.Equal(t, "snapshot-embedding", payload.EmbeddingModelID)
}
