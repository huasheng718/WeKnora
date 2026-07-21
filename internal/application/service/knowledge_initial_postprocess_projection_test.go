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
	"gorm.io/gorm"
)

type initialPostProcessKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	knowledge *types.Knowledge
}

type initialPostProcessOneReadRepo struct {
	interfaces.KnowledgeRepository
	knowledge *types.Knowledge
	calls     int
}

func (r *initialPostProcessOneReadRepo) GetKnowledgeByIDOnly(context.Context, string) (*types.Knowledge, error) {
	r.calls++
	if r.calls == 1 {
		return r.knowledge, nil
	}
	return nil, errors.New("transient second read failure")
}

func (r *initialPostProcessKnowledgeRepo) GetKnowledgeByID(context.Context, uint64, string) (*types.Knowledge, error) {
	return r.knowledge, nil
}

func (r *initialPostProcessKnowledgeRepo) GetKnowledgeByIDOnly(context.Context, string) (*types.Knowledge, error) {
	return r.knowledge, nil
}

func (r *initialPostProcessKnowledgeRepo) UpdateKnowledge(_ context.Context, knowledge *types.Knowledge) error {
	r.knowledge = knowledge
	return nil
}

type initialPostProcessChunkService struct {
	interfaces.ChunkService
}

func (s *initialPostProcessChunkService) DeleteChunksByKnowledgeID(context.Context, string) error {
	return nil
}

func (s *initialPostProcessChunkService) CreateChunks(context.Context, []*types.Chunk) error {
	return nil
}

func (s *initialPostProcessChunkService) ListChunksByKnowledgeIDForSystem(context.Context, string) ([]*types.Chunk, error) {
	return nil, nil
}

type initialPostProcessTenantRepo struct {
	interfaces.TenantRepository
}

func (r *initialPostProcessTenantRepo) AdjustStorageUsed(context.Context, uint64, int64) error {
	return nil
}

func (r *initialPostProcessTenantRepo) GetTenantByID(context.Context, uint64) (*types.Tenant, error) {
	return &types.Tenant{ID: 1}, nil
}

type initialPostProcessKBService struct {
	interfaces.KnowledgeBaseService
	kb *types.KnowledgeBase
}

func (s *initialPostProcessKBService) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

type initialPostProcessGraphRepo struct {
	interfaces.RetrieveGraphRepository
}

func (r *initialPostProcessGraphRepo) DelGraph(context.Context, []types.NameSpace) error {
	return nil
}

type initialPostProcessEnqueuer struct {
	mu       sync.Mutex
	errors   []error
	taskIDs  []string
	payloads []*asynq.Task
}

type manualAttemptRetryEnqueuer struct {
	mu                      sync.Mutex
	accepted                map[string]*asynq.Task
	taskIDs                 []string
	ambiguousPostProcessErr error
	postProcessAccepted     int
}

func (e *manualAttemptRetryEnqueuer) Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	taskID := ""
	for _, opt := range opts {
		if opt.Type() == asynq.TaskIDOpt {
			taskID = opt.Value().(string)
		}
	}
	e.taskIDs = append(e.taskIDs, taskID)
	if _, exists := e.accepted[taskID]; taskID != "" && exists {
		return nil, asynq.ErrTaskIDConflict
	}
	if taskID != "" {
		e.accepted[taskID] = task
	}
	if task.Type() == types.TypeKnowledgePostProcess {
		e.postProcessAccepted++
		if e.ambiguousPostProcessErr != nil {
			err := e.ambiguousPostProcessErr
			e.ambiguousPostProcessErr = nil
			return nil, err
		}
	}
	return &asynq.TaskInfo{ID: taskID, Queue: types.QueueDefault}, nil
}

func (e *initialPostProcessEnqueuer) Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	taskID := ""
	for _, opt := range opts {
		if opt.Type() == asynq.TaskIDOpt {
			taskID = opt.Value().(string)
		}
	}
	e.taskIDs = append(e.taskIDs, taskID)
	e.payloads = append(e.payloads, task)
	call := len(e.taskIDs) - 1
	if call < len(e.errors) && e.errors[call] != nil {
		return nil, e.errors[call]
	}
	return &asynq.TaskInfo{ID: taskID, Queue: types.QueueDefault}, nil
}

func initialPostProcessProjectionKnowledge(t *testing.T, status string) *types.Knowledge {
	t.Helper()
	content := "# governed projection"
	knowledge := &types.Knowledge{
		ID: "projection-knowledge-1", TenantID: 1, KnowledgeBaseID: "kb-1",
		Type: types.KnowledgeTypeManual, ParseStatus: status,
	}
	meta := types.NewManualKnowledgeMetadata(content, types.ManualKnowledgeStatusPublish, 1)
	meta.ProductionProjection = &types.ProductionProjectionMetadata{
		DocumentID: "document-1", VersionID: "version-1", ReleaseTargetID: "target-1",
		ContentDigest: projectionKnowledgeContentDigest(content), SummaryModelID: "summary-1",
		IndexingStrategy: types.IndexingStrategy{WikiEnabled: true},
	}
	require.NoError(t, knowledge.SetManualMetadata(meta))
	graphEnabled := false
	require.NoError(t, knowledge.SetProcessOverrides(&types.KnowledgeProcessOverrides{
		ChunkingConfig: &types.ChunkingConfig{Strategy: "recursive", ChunkSize: 512, ChunkOverlap: 32},
		GraphEnabled:   &graphEnabled,
		ExtractConfig:  &types.ExtractConfig{Enabled: false},
	}))
	return knowledge
}

func initialPostProcessProjectionReleaseRepo(
	t *testing.T,
	knowledge *types.Knowledge,
) interfaces.ProductionReleaseRepository {
	t.Helper()
	meta, err := knowledge.ManualMetadata()
	require.NoError(t, err)
	snapshot, digest, err := types.CanonicalProductionReleaseTargetConfig(
		types.JSON(`{"version":1,"indexing_strategy":{"wiki_enabled":true}}`),
	)
	require.NoError(t, err)
	return &projectionManualReleaseRepo{target: &types.ProductionReleaseTarget{
		ID: meta.ProductionProjection.ReleaseTargetID, TenantID: knowledge.TenantID,
		KnowledgeID: knowledge.ID, TargetKnowledgeBaseID: knowledge.KnowledgeBaseID,
		DocumentID: meta.ProductionProjection.DocumentID, VersionID: meta.ProductionProjection.VersionID,
		Status: types.ReleaseTargetBuilding, ConfigSnapshot: snapshot, ConfigDigest: digest,
	}}
}

func initialPostProcessContext(attempt int) context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, &types.Tenant{ID: 1})
	return withAttempt(ctx, attempt)
}

func TestProductionProjectionDirectPostProcessEnqueueFailureRetriesDeterministically(t *testing.T) {
	t.Setenv("RETRIEVE_DRIVER", "")
	queueErr := errors.New("post-process queue unavailable")
	knowledge := initialPostProcessProjectionKnowledge(t, types.ParseStatusProcessing)
	repo := &initialPostProcessKnowledgeRepo{knowledge: knowledge}
	tasks := &initialPostProcessEnqueuer{errors: []error{queueErr, nil, asynq.ErrTaskIDConflict}}
	service := &knowledgeService{
		repo: repo, chunkService: &initialPostProcessChunkService{},
		tenantRepo: &initialPostProcessTenantRepo{}, graphEngine: &initialPostProcessGraphRepo{},
		task: tasks,
	}
	kb := &types.KnowledgeBase{ID: knowledge.KnowledgeBaseID, TenantID: knowledge.TenantID}
	content := "# governed projection"

	err := service.triggerManualProcessing(initialPostProcessContext(7), kb, knowledge, content, true)
	require.ErrorIs(t, err, queueErr)
	require.NoError(t, service.triggerManualProcessing(initialPostProcessContext(7), kb, knowledge, content, true))
	require.NoError(t, service.triggerManualProcessing(initialPostProcessContext(7), kb, knowledge, content, true))
	require.Equal(t, []string{
		"production-projection-post-process-attempt-7-target-1-projection-knowledge-1",
		"production-projection-post-process-attempt-7-target-1-projection-knowledge-1",
		"production-projection-post-process-attempt-7-target-1-projection-knowledge-1",
	}, tasks.taskIDs)
}

func TestOrdinaryDirectPostProcessEnqueueFailureRemainsBestEffort(t *testing.T) {
	t.Setenv("RETRIEVE_DRIVER", "")
	knowledge := &types.Knowledge{
		ID: "ordinary-knowledge-1", TenantID: 1, KnowledgeBaseID: "kb-1",
		Type: types.KnowledgeTypeManual, ParseStatus: types.ParseStatusProcessing,
	}
	require.NoError(t, knowledge.SetManualMetadata(types.NewManualKnowledgeMetadata(
		"# ordinary", types.ManualKnowledgeStatusPublish, 1,
	)))
	tasks := &initialPostProcessEnqueuer{errors: []error{errors.New("queue unavailable")}}
	service := &knowledgeService{
		repo:         &initialPostProcessKnowledgeRepo{knowledge: knowledge},
		chunkService: &initialPostProcessChunkService{}, tenantRepo: &initialPostProcessTenantRepo{},
		graphEngine: &initialPostProcessGraphRepo{}, task: tasks,
	}

	require.NoError(t, service.triggerManualProcessing(
		initialPostProcessContext(3), &types.KnowledgeBase{ID: "kb-1"}, knowledge, "# ordinary", true,
	))
}

func TestProductionProjectionMultimodalPostProcessEnqueueFailureRetriesDeterministically(t *testing.T) {
	queueErr := errors.New("post-process queue unavailable")
	knowledge := initialPostProcessProjectionKnowledge(t, types.ParseStatusCancelled)
	tasks := &initialPostProcessEnqueuer{errors: []error{queueErr, nil, asynq.ErrTaskIDConflict}}
	service := &ImageMultimodalService{
		knowledgeRepo: &initialPostProcessKnowledgeRepo{knowledge: knowledge}, taskEnqueuer: tasks,
	}
	payload := types.ImageMultimodalPayload{
		TenantID: knowledge.TenantID, KnowledgeID: knowledge.ID, KnowledgeBaseID: knowledge.KnowledgeBaseID,
		Attempt: 7,
	}
	payloadBytes, err := json.Marshal(payload)
	require.NoError(t, err)
	task := asynq.NewTask(types.TypeImageMultimodal, payloadBytes)

	require.ErrorIs(t, service.Handle(context.Background(), task), queueErr)
	require.NoError(t, service.Handle(context.Background(), task))
	require.NoError(t, service.Handle(context.Background(), task))
	require.Equal(t, []string{
		"production-projection-post-process-attempt-7-target-1-projection-knowledge-1",
		"production-projection-post-process-attempt-7-target-1-projection-knowledge-1",
		"production-projection-post-process-attempt-7-target-1-projection-knowledge-1",
	}, tasks.taskIDs)
	var postProcessPayload types.KnowledgePostProcessPayload
	require.NoError(t, json.Unmarshal(tasks.payloads[1].Payload(), &postProcessPayload))
	require.Equal(t, 7, postProcessPayload.Attempt)
}

func TestProductionProjectionMultimodalCarriesGuardedKnowledgeIntoEnqueue(t *testing.T) {
	queueErr := errors.New("post-process queue unavailable")
	knowledge := initialPostProcessProjectionKnowledge(t, types.ParseStatusCancelled)
	repo := &initialPostProcessOneReadRepo{knowledge: knowledge}
	service := &ImageMultimodalService{
		knowledgeRepo: repo,
		taskEnqueuer:  &initialPostProcessEnqueuer{errors: []error{queueErr}},
	}
	payload, err := json.Marshal(types.ImageMultimodalPayload{
		TenantID: 1, KnowledgeID: knowledge.ID, KnowledgeBaseID: knowledge.KnowledgeBaseID, Attempt: 7,
	})
	require.NoError(t, err)

	err = service.Handle(context.Background(), asynq.NewTask(types.TypeImageMultimodal, payload))
	require.ErrorIs(t, err, queueErr)
	require.Equal(t, 1, repo.calls, "the guarded Knowledge row must be reused at the enqueue boundary")
}

func TestOrdinaryMultimodalPostProcessEnqueueFailureRemainsBestEffort(t *testing.T) {
	knowledge := &types.Knowledge{
		ID: "ordinary-knowledge-1", TenantID: 1, KnowledgeBaseID: "kb-1",
		ParseStatus: types.ParseStatusCancelled,
	}
	service := &ImageMultimodalService{
		knowledgeRepo: &initialPostProcessKnowledgeRepo{knowledge: knowledge},
		taskEnqueuer:  &initialPostProcessEnqueuer{errors: []error{errors.New("queue unavailable")}},
	}
	payload, err := json.Marshal(types.ImageMultimodalPayload{
		TenantID: 1, KnowledgeID: knowledge.ID, KnowledgeBaseID: knowledge.KnowledgeBaseID, Attempt: 2,
	})
	require.NoError(t, err)
	require.NoError(t, service.Handle(context.Background(), asynq.NewTask(types.TypeImageMultimodal, payload)))
}

func TestProductionProjectionManualRetryReusesPersistedAttempt(t *testing.T) {
	t.Setenv("RETRIEVE_DRIVER", "")
	tracker, spanDB := setupSpanTrackerTest(t)
	queueErr := errors.New("post-process enqueue response lost")
	knowledge := initialPostProcessProjectionKnowledge(t, types.ParseStatusPending)
	seedSpanTrackerKnowledgeTest(t, spanDB, knowledge.TenantID, knowledge.ID)
	repo := &initialPostProcessKnowledgeRepo{knowledge: knowledge}
	tasks := &manualAttemptRetryEnqueuer{
		accepted:                make(map[string]*asynq.Task),
		ambiguousPostProcessErr: queueErr,
	}
	service := &knowledgeService{
		repo: repo,
		kbService: &initialPostProcessKBService{kb: &types.KnowledgeBase{
			ID: knowledge.KnowledgeBaseID, TenantID: knowledge.TenantID,
		}},
		tenantRepo:            &initialPostProcessTenantRepo{},
		chunkService:          &initialPostProcessChunkService{},
		graphEngine:           &initialPostProcessGraphRepo{},
		task:                  tasks,
		spanTracker:           tracker,
		productionReleaseRepo: initialPostProcessProjectionReleaseRepo(t, knowledge),
	}

	require.NoError(t, service.enqueueManualProcessing(context.Background(), knowledge, "# governed projection", false))
	manualTaskID := "production-projection-build-target-1-projection-knowledge-1"
	manualTask := tasks.accepted[manualTaskID]
	require.NotNil(t, manualTask)
	var payload types.ManualProcessPayload
	require.NoError(t, json.Unmarshal(manualTask.Payload(), &payload))
	require.Equal(t, 1, payload.Attempt)

	require.NoError(t, service.enqueueManualProcessing(context.Background(), knowledge, "# governed projection", false))
	require.Equal(t, 1, tracker.LatestAttempt(context.Background(), knowledge.ID),
		"pending replay must reuse the attempt persisted before the first enqueue")

	require.ErrorIs(t, service.ProcessManualUpdate(context.Background(), manualTask), queueErr)
	require.NoError(t, service.ProcessManualUpdate(context.Background(), manualTask))
	postProcessTaskID := "production-projection-post-process-attempt-1-target-1-projection-knowledge-1"
	require.Equal(t, []string{manualTaskID, manualTaskID, postProcessTaskID, postProcessTaskID}, tasks.taskIDs)
	require.Equal(t, 1, tasks.postProcessAccepted,
		"the ambiguous first response accepted the task; retry must conflict instead of duplicating it")
	require.Equal(t, 1, tracker.LatestAttempt(context.Background(), knowledge.ID),
		"an Asynq retry of the same payload must not allocate a second parse attempt")
}

func TestProductionProjectionFailedRetryAllocatesFreshPersistedAttempt(t *testing.T) {
	tracker, spanDB := setupSpanTrackerTest(t)
	knowledge := initialPostProcessProjectionKnowledge(t, types.ParseStatusPending)
	seedSpanTrackerKnowledgeTest(t, spanDB, knowledge.TenantID, knowledge.ID)
	tasks := &initialPostProcessEnqueuer{}
	service := &knowledgeService{task: tasks, spanTracker: tracker}

	require.NoError(t, service.enqueueManualProcessing(context.Background(), knowledge, "# governed projection", false))
	require.NoError(t, service.enqueueManualProcessing(context.Background(), knowledge, "# governed projection", true))
	require.Len(t, tasks.payloads, 2)

	var initialPayload, retryPayload types.ManualProcessPayload
	require.NoError(t, json.Unmarshal(tasks.payloads[0].Payload(), &initialPayload))
	require.NoError(t, json.Unmarshal(tasks.payloads[1].Payload(), &retryPayload))
	require.Equal(t, 1, initialPayload.Attempt)
	require.Equal(t, 2, retryPayload.Attempt)
	require.Equal(t, 2, tracker.LatestAttempt(context.Background(), knowledge.ID))
}

func TestProductionProjectionFailedRetryEscapesArchivedPriorAttemptTaskID(t *testing.T) {
	tracker, spanDB := setupSpanTrackerTest(t)
	knowledge := initialPostProcessProjectionKnowledge(t, types.ParseStatusPending)
	seedSpanTrackerKnowledgeTest(t, spanDB, knowledge.TenantID, knowledge.ID)
	tasks := &manualAttemptRetryEnqueuer{accepted: make(map[string]*asynq.Task)}
	service := &knowledgeService{task: tasks, spanTracker: tracker}

	require.NoError(t, service.enqueueManualProcessing(context.Background(), knowledge, "# governed projection", true))
	firstRetryID := productionProjectionAttemptTaskID("target-1", knowledge.ID, "retry", 1)
	firstTask := tasks.accepted[firstRetryID]
	require.NotNil(t, firstTask)
	tracker.FinalizeAttempt(context.Background(), knowledge.ID, 1, types.SpanStatusFailed, nil,
		"MANUAL_TASK_DEAD_LETTERED", "manual retry exhausted")

	require.NoError(t, service.enqueueManualProcessing(context.Background(), knowledge, "# governed projection", true))
	secondRetryID := productionProjectionAttemptTaskID("target-1", knowledge.ID, "retry", 2)
	secondTask := tasks.accepted[secondRetryID]
	require.NotNil(t, secondTask, "an archived attempt-1 task ID must not block attempt 2")
	var secondPayload types.ManualProcessPayload
	require.NoError(t, json.Unmarshal(secondTask.Payload(), &secondPayload))
	require.Equal(t, 2, secondPayload.Attempt)
	require.Equal(t, 2, tracker.LatestAttempt(context.Background(), knowledge.ID))
	require.Len(t, tasks.accepted, 2)

	require.NoError(t, service.enqueueManualProcessing(
		context.Background(), knowledge, "# governed projection", true, secondPayload.Attempt,
	))
	require.Equal(t, 2, tracker.LatestAttempt(context.Background(), knowledge.ID),
		"same-attempt replay must not open another root")
	require.Len(t, tasks.accepted, 2, "same-attempt TaskID conflict must not enqueue another worker")
	require.Equal(t, []string{firstRetryID, secondRetryID, secondRetryID}, tasks.taskIDs)
}

func TestProductionProjectionClaimedRetryAllocatesOnceAndReplaysSameAttempt(t *testing.T) {
	tracker, spanDB := setupSpanTrackerTest(t)
	knowledge := initialPostProcessProjectionKnowledge(t, types.ParseStatusPending)
	seedSpanTrackerKnowledgeTest(t, spanDB, knowledge.TenantID, knowledge.ID)
	tasks := &manualAttemptRetryEnqueuer{accepted: make(map[string]*asynq.Task)}
	service := &knowledgeService{task: tasks, spanTracker: tracker}

	root, attempt, err := tracker.OpenAttempt(context.Background(), knowledge.ID, "")
	require.NoError(t, err)
	require.NotNil(t, root)
	tracker.FinalizeAttempt(context.Background(), knowledge.ID, attempt,
		types.SpanStatusFailed, nil, "MANUAL_TASK_DEAD_LETTERED", "prior attempt failed")

	require.NoError(t, service.enqueueClaimedProjectionRetry(context.Background(), knowledge, "# governed projection"))
	require.NoError(t, service.enqueueClaimedProjectionRetry(context.Background(), knowledge, "# governed projection"))
	require.Equal(t, attempt+1, tracker.LatestAttempt(context.Background(), knowledge.ID))
	retryID := productionProjectionAttemptTaskID("target-1", knowledge.ID, "retry", attempt+1)
	require.NotNil(t, tasks.accepted[retryID])
	require.Len(t, tasks.accepted, 1)
	require.Equal(t, []string{retryID, retryID}, tasks.taskIDs)
}

func TestOrdinaryManualEnqueueRemainsCompatibleWithoutSpanTracker(t *testing.T) {
	knowledge := &types.Knowledge{
		ID: "ordinary-knowledge-1", TenantID: 1, KnowledgeBaseID: "kb-1", Type: types.KnowledgeTypeManual,
	}
	tasks := &initialPostProcessEnqueuer{}
	service := &knowledgeService{task: tasks}

	require.NoError(t, service.enqueueManualProcessing(context.Background(), knowledge, "# ordinary", false))
	require.Len(t, tasks.payloads, 1)
	var payload types.ManualProcessPayload
	require.NoError(t, json.Unmarshal(tasks.payloads[0].Payload(), &payload))
	require.Zero(t, payload.Attempt)
	require.Equal(t, []string{""}, tasks.taskIDs)
}

func TestOrdinaryManualEnqueueReusesPresetReparseAttempt(t *testing.T) {
	tracker, _ := setupSpanTrackerTest(t)
	knowledge := &types.Knowledge{
		ID: "ordinary-knowledge-1", TenantID: 1, KnowledgeBaseID: "kb-1", Type: types.KnowledgeTypeManual,
	}
	_, reparseAttempt, err := tracker.OpenAttempt(context.Background(), knowledge.ID, "")
	require.NoError(t, err)
	require.Equal(t, 1, reparseAttempt)
	tasks := &initialPostProcessEnqueuer{}
	service := &knowledgeService{task: tasks, spanTracker: tracker}

	require.NoError(t, service.enqueueManualProcessing(
		context.Background(), knowledge, "# ordinary", true, reparseAttempt,
	))
	var payload types.ManualProcessPayload
	require.NoError(t, json.Unmarshal(tasks.payloads[0].Payload(), &payload))
	require.Equal(t, reparseAttempt, payload.Attempt)
	require.Equal(t, reparseAttempt, tracker.LatestAttempt(context.Background(), knowledge.ID),
		"enqueue must not allocate a second attempt after reparse already opened one")
}

func TestProductionProjectionConcurrentPendingBuildEnqueueClaimsOneAttempt(t *testing.T) {
	t.Setenv("RETRIEVE_DRIVER", "")
	knowledge := initialPostProcessProjectionKnowledge(t, types.ParseStatusPending)
	tracker, spanDB := setupConcurrentSpanTrackerTest(t, knowledge.TenantID, knowledge.ID)
	repo := &initialPostProcessKnowledgeRepo{knowledge: knowledge}
	tasks := &manualAttemptRetryEnqueuer{accepted: make(map[string]*asynq.Task)}
	service := &knowledgeService{
		repo: repo,
		kbService: &initialPostProcessKBService{kb: &types.KnowledgeBase{
			ID: knowledge.KnowledgeBaseID, TenantID: knowledge.TenantID,
		}},
		tenantRepo:            &initialPostProcessTenantRepo{},
		chunkService:          &initialPostProcessChunkService{},
		graphEngine:           &initialPostProcessGraphRepo{},
		task:                  tasks,
		spanTracker:           tracker,
		productionReleaseRepo: initialPostProcessProjectionReleaseRepo(t, knowledge),
	}
	meta, err := knowledge.ManualMetadata()
	require.NoError(t, err)
	overrides, err := knowledge.ProcessOverrides()
	require.NoError(t, err)
	payload := &types.ProductionProjectionKnowledgePayload{
		KnowledgeID: knowledge.ID, KnowledgeBaseID: knowledge.KnowledgeBaseID,
		Title: "Projection", Content: meta.Content, SummaryModelID: meta.ProductionProjection.SummaryModelID,
		IndexingStrategy: meta.ProductionProjection.IndexingStrategy, ProcessOverrides: overrides,
		ProductionProjection: meta.ProductionProjection,
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, knowledge.TenantID)

	const builders = 16
	start := make(chan struct{})
	errs := make(chan error, builders)
	var builds sync.WaitGroup
	for range builders {
		builds.Add(1)
		go func() {
			defer builds.Done()
			<-start
			_, buildErr := service.CreateKnowledgeFromProductionProjection(ctx, payload)
			errs <- buildErr
		}()
	}
	close(start)
	builds.Wait()
	close(errs)
	for buildErr := range errs {
		require.NoError(t, buildErr)
	}

	manualTaskID := "production-projection-build-target-1-projection-knowledge-1"
	manualTask := tasks.accepted[manualTaskID]
	require.NotNil(t, manualTask)
	var acceptedPayload types.ManualProcessPayload
	require.NoError(t, json.Unmarshal(manualTask.Payload(), &acceptedPayload))
	latestAttempt := tracker.LatestAttempt(ctx, knowledge.ID)
	require.Equal(t, latestAttempt, acceptedPayload.Attempt)
	require.False(t, attemptSuperseded(ctx, tracker, knowledge.ID, acceptedPayload.Attempt))

	var rootCount int64
	require.NoError(t, spanDB.Table("knowledge_processing_spans").
		Where("knowledge_id = ? AND kind = ?", knowledge.ID, types.SpanKindRoot).
		Count(&rootCount).Error)
	require.Equal(t, int64(1), rootCount)
	require.Len(t, tasks.accepted, 1, "only one deterministic manual task may be accepted")
	require.NoError(t, service.ProcessManualUpdate(context.Background(), manualTask),
		"the queue winner must remain on the latest attempt through finalization dispatch")
	postProcessTaskID := "production-projection-post-process-attempt-1-target-1-projection-knowledge-1"
	postProcessTask := tasks.accepted[postProcessTaskID]
	require.NotNil(t, postProcessTask)
	var postProcessPayload types.KnowledgePostProcessPayload
	require.NoError(t, json.Unmarshal(postProcessTask.Payload(), &postProcessPayload))
	require.Equal(t, acceptedPayload.Attempt, postProcessPayload.Attempt)
	require.False(t, attemptSuperseded(ctx, tracker, knowledge.ID, postProcessPayload.Attempt))
}

func requireManualAttemptRootState(
	t *testing.T, db *gorm.DB, knowledgeID string, attempt int, status, errorCode string,
) {
	t.Helper()
	var root types.KnowledgeProcessingSpan
	require.NoError(t, db.Table("knowledge_processing_spans").
		Where("knowledge_id = ? AND attempt = ? AND kind = ?", knowledgeID, attempt, types.SpanKindRoot).
		Take(&root).Error)
	require.Equal(t, status, root.Status)
	require.Equal(t, errorCode, root.ErrorCode)
}

func TestProductionProjectionManualMarshalFailureFinalizesClaimedAttempt(t *testing.T) {
	knowledge := initialPostProcessProjectionKnowledge(t, types.ParseStatusPending)
	tracker, spanDB := setupConcurrentSpanTrackerTest(t, knowledge.TenantID, knowledge.ID)
	service := &knowledgeService{task: &initialPostProcessEnqueuer{}, spanTracker: tracker}
	marshalErr := errors.New("manual payload cannot be encoded")

	err := service.enqueueManualProcessingWithEncoder(
		context.Background(), knowledge, "# governed projection", false,
		func(types.ManualProcessPayload) ([]byte, error) { return nil, marshalErr },
	)
	require.ErrorIs(t, err, marshalErr)
	requireManualAttemptRootState(t, spanDB, knowledge.ID, 1, types.SpanStatusFailed, "MANUAL_TASK_PAYLOAD_MARSHAL_FAILED")
}

func TestOrdinaryManualDefinitiveEnqueueFailureFinalizesNewAttempt(t *testing.T) {
	tracker, spanDB := setupSpanTrackerTest(t)
	knowledge := &types.Knowledge{
		ID: "ordinary-enqueue-failure", TenantID: 1, KnowledgeBaseID: "kb-1", Type: types.KnowledgeTypeManual,
	}
	queueErr := errors.New("queue rejected task")
	service := &knowledgeService{
		task: &initialPostProcessEnqueuer{errors: []error{queueErr}}, spanTracker: tracker,
	}

	err := service.enqueueManualProcessing(context.Background(), knowledge, "# ordinary", false)
	require.ErrorIs(t, err, queueErr)
	requireManualAttemptRootState(t, spanDB, knowledge.ID, 1, types.SpanStatusFailed, "MANUAL_TASK_ENQUEUE_FAILED")
}

func TestOrdinaryManualReparseEnqueueFailureFinalizesPresetAttempt(t *testing.T) {
	tracker, spanDB := setupSpanTrackerTest(t)
	knowledge := &types.Knowledge{
		ID: "ordinary-reparse-failure", TenantID: 1, KnowledgeBaseID: "kb-1", Type: types.KnowledgeTypeManual,
	}
	_, attempt, err := tracker.OpenAttempt(context.Background(), knowledge.ID, "")
	require.NoError(t, err)
	queueErr := errors.New("queue rejected reparse")
	service := &knowledgeService{
		task: &initialPostProcessEnqueuer{errors: []error{queueErr}}, spanTracker: tracker,
	}

	err = service.enqueueManualProcessing(context.Background(), knowledge, "# ordinary", true, attempt)
	require.ErrorIs(t, err, queueErr)
	requireManualAttemptRootState(t, spanDB, knowledge.ID, attempt, types.SpanStatusFailed, "MANUAL_TASK_ENQUEUE_FAILED")
}

func TestProductionProjectionTaskIDConflictPreservesClaimedAttempt(t *testing.T) {
	knowledge := initialPostProcessProjectionKnowledge(t, types.ParseStatusPending)
	tracker, spanDB := setupConcurrentSpanTrackerTest(t, knowledge.TenantID, knowledge.ID)
	service := &knowledgeService{
		task: &initialPostProcessEnqueuer{errors: []error{asynq.ErrTaskIDConflict}}, spanTracker: tracker,
	}

	require.NoError(t, service.enqueueManualProcessing(context.Background(), knowledge, "# governed projection", false))
	requireManualAttemptRootState(t, spanDB, knowledge.ID, 1, types.SpanStatusRunning, "")
}
