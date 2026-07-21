package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type createKnowledgeFileRepoStub struct {
	interfaces.KnowledgeRepository

	createCalls       int
	createErr         error
	createdKnowledge  *types.Knowledge
	existingKnowledge *types.Knowledge
	columnUpdates     map[string]interface{}
}

func (r *createKnowledgeFileRepoStub) UpdateKnowledgeColumns(
	_ context.Context, _ string, values map[string]interface{},
) error {
	r.columnUpdates = values
	if status, ok := values["parse_status"].(string); ok && r.existingKnowledge != nil {
		r.existingKnowledge.ParseStatus = status
	}
	return nil
}

func (r *createKnowledgeFileRepoStub) ClaimFailedKnowledgeRetry(
	_ context.Context, _ string,
) (bool, error) {
	if r.existingKnowledge == nil || r.existingKnowledge.ParseStatus != types.ParseStatusFailed {
		return false, nil
	}
	return true, r.UpdateKnowledgeColumns(context.Background(), r.existingKnowledge.ID, map[string]interface{}{
		"parse_status": types.ParseStatusPending, "error_message": "",
	})
}

func (r *createKnowledgeFileRepoStub) GetKnowledgeByID(
	_ context.Context, _ uint64, _ string,
) (*types.Knowledge, error) {
	if r.existingKnowledge == nil {
		return nil, repository.ErrKnowledgeNotFound
	}
	copyKnowledge := *r.existingKnowledge
	return &copyKnowledge, nil
}

func (r *createKnowledgeFileRepoStub) CheckKnowledgeExists(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	params *types.KnowledgeCheckParams,
) (bool, *types.Knowledge, error) {
	return false, nil, nil
}

func (r *createKnowledgeFileRepoStub) CreateKnowledge(ctx context.Context, knowledge *types.Knowledge) error {
	r.createCalls++
	copied := *knowledge
	r.createdKnowledge = &copied
	return r.createErr
}

// GetKnowledgeTags is invoked by setAndAttachKnowledgeTags after create even
// when no tags were supplied; a fresh knowledge has none, so return empty.
func (r *createKnowledgeFileRepoStub) GetKnowledgeTags(
	ctx context.Context,
	knowledgeIDs []string,
) (map[string][]*types.KnowledgeTag, error) {
	return map[string][]*types.KnowledgeTag{}, nil
}

type createKnowledgeFileKBServiceStub struct {
	interfaces.KnowledgeBaseService

	kb *types.KnowledgeBase
}

func (s *createKnowledgeFileKBServiceStub) GetKnowledgeBaseByID(
	ctx context.Context,
	id string,
) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

type createKnowledgeFileServiceStub struct {
	saveErr              error
	saveCalls            int
	savedWithKnowledgeID string
	deleteCalls          int
	deletedPath          string
}

func (s *createKnowledgeFileServiceStub) CheckConnectivity(ctx context.Context) error {
	return nil
}

func (s *createKnowledgeFileServiceStub) SaveFile(
	ctx context.Context,
	file *multipart.FileHeader,
	tenantID uint64,
	knowledgeID string,
) (string, error) {
	s.saveCalls++
	s.savedWithKnowledgeID = knowledgeID
	if s.saveErr != nil {
		return "", s.saveErr
	}
	return "stored/" + knowledgeID, nil
}

func (s *createKnowledgeFileServiceStub) SaveBytes(
	ctx context.Context,
	data []byte,
	tenantID uint64,
	fileName string,
	temp bool,
) (string, error) {
	return "", errors.New("not implemented")
}

func (s *createKnowledgeFileServiceStub) GetFile(ctx context.Context, filePath string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (s *createKnowledgeFileServiceStub) GetFileURL(ctx context.Context, filePath string) (string, error) {
	return "", errors.New("not implemented")
}

func (s *createKnowledgeFileServiceStub) DeleteFile(ctx context.Context, filePath string) error {
	s.deleteCalls++
	s.deletedPath = filePath
	return nil
}

func (s *createKnowledgeFileServiceStub) CopyFile(ctx context.Context, srcPath string, tenantID uint64, knowledgeID string) (string, error) {
	return "", errors.New("not implemented")
}

type createKnowledgeTaskEnqueuerStub struct {
	calls      int
	enqueueErr error
	taskIDs    []string
}

func (s *createKnowledgeTaskEnqueuerStub) Enqueue(
	task *asynq.Task,
	opts ...asynq.Option,
) (*asynq.TaskInfo, error) {
	s.calls++
	for _, opt := range opts {
		if opt.Type() == asynq.TaskIDOpt {
			s.taskIDs = append(s.taskIDs, opt.Value().(string))
		}
	}
	if s.enqueueErr != nil {
		return nil, s.enqueueErr
	}
	return &asynq.TaskInfo{ID: "task-1", Queue: "default"}, nil
}

func TestCreateKnowledgeFromProductionProjectionUsesReservedIDAndImmutableMetadata(t *testing.T) {
	repo := &createKnowledgeFileRepoStub{}
	tasks := &createKnowledgeTaskEnqueuerStub{}
	tracker, spanDB := setupSpanTrackerTest(t)
	seedSpanTrackerKnowledgeTest(t, spanDB, 1, "93000000-0000-4000-8000-000000000007")
	svc := &knowledgeService{
		repo: repo,
		kbService: &createKnowledgeFileKBServiceStub{kb: &types.KnowledgeBase{
			ID: "kb-1", TenantID: 1, Type: types.KnowledgeBaseTypeDocument,
			EmbeddingModelID: "live-model",
		}},
		fileSvc: &createKnowledgeFileServiceStub{}, task: tasks, spanTracker: tracker,
	}
	payload := &types.ProductionProjectionKnowledgePayload{
		KnowledgeID:     "93000000-0000-4000-8000-000000000007",
		KnowledgeBaseID: "kb-1", Title: "Approved baseline", Content: "# Approved\n",
		EmbeddingModelID: "snapshot-model", SummaryModelID: "snapshot-summary",
		IndexingStrategy: types.IndexingStrategy{VectorEnabled: true, KeywordEnabled: true},
		ProcessOverrides: &types.KnowledgeProcessOverrides{
			ChunkingConfig: &types.ChunkingConfig{ChunkSize: 777},
		},
		ProductionProjection: &types.ProductionProjectionMetadata{
			DocumentID: "doc-1", VersionID: "v3", ReleaseTargetID: "target-1",
			ContentDigest:    projectionKnowledgeContentDigest("# Approved\n"),
			SummaryModelID:   "snapshot-summary",
			IndexingStrategy: types.IndexingStrategy{VectorEnabled: true, KeywordEnabled: true},
		},
	}

	knowledge, err := svc.CreateKnowledgeFromProductionProjection(newCreateKnowledgeFileContext(), payload)
	require.NoError(t, err)
	require.Equal(t, payload.KnowledgeID, knowledge.ID)
	require.Equal(t, "snapshot-model", knowledge.EmbeddingModelID)
	require.Equal(t, types.ParseStatusPending, knowledge.ParseStatus)
	require.Equal(t, 1, repo.createCalls)
	require.Equal(t, 1, tasks.calls)
	meta, err := knowledge.ManualMetadata()
	require.NoError(t, err)
	require.Equal(t, payload.ProductionProjection, meta.ProductionProjection)
	overrides, err := knowledge.ProcessOverrides()
	require.NoError(t, err)
	require.Equal(t, 777, overrides.ChunkingConfig.ChunkSize)
}

func TestCreateKnowledgeFromProductionProjectionIsIdempotentAndRejectsForeignOwnership(t *testing.T) {
	owned := &types.Knowledge{
		ID: "knowledge-1", TenantID: 1, KnowledgeBaseID: "kb-1", Type: types.KnowledgeTypeManual,
	}
	ownedMeta := types.NewManualKnowledgeMetadata("# owned", types.ManualKnowledgeStatusPublish, 1)
	ownedMeta.ProductionProjection = &types.ProductionProjectionMetadata{
		DocumentID: "doc-1", VersionID: "v3", ReleaseTargetID: "target-1",
		ContentDigest:  projectionKnowledgeContentDigest("# owned"),
		SummaryModelID: "snapshot-summary", IndexingStrategy: types.IndexingStrategy{VectorEnabled: true},
	}
	require.NoError(t, owned.SetManualMetadata(ownedMeta))
	repo := &createKnowledgeFileRepoStub{existingKnowledge: owned}
	tasks := &createKnowledgeTaskEnqueuerStub{}
	svc := &knowledgeService{repo: repo, task: tasks}
	payload := &types.ProductionProjectionKnowledgePayload{
		KnowledgeID: "knowledge-1", KnowledgeBaseID: "kb-1",
		Content: "# owned", EmbeddingModelID: "snapshot-embedding", SummaryModelID: "snapshot-summary",
		IndexingStrategy:     types.IndexingStrategy{VectorEnabled: true},
		ProcessOverrides:     &types.KnowledgeProcessOverrides{ChunkingConfig: &types.ChunkingConfig{ChunkSize: 512}},
		ProductionProjection: ownedMeta.ProductionProjection,
	}

	got, err := svc.CreateKnowledgeFromProductionProjection(newCreateKnowledgeFileContext(), payload)
	require.NoError(t, err)
	require.Equal(t, owned.ID, got.ID)
	require.Zero(t, repo.createCalls)
	require.Zero(t, tasks.calls)

	foreign := *owned
	foreignMeta := *ownedMeta
	foreignProjection := *ownedMeta.ProductionProjection
	foreignProjection.ReleaseTargetID = "target-other"
	foreignMeta.ProductionProjection = &foreignProjection
	require.NoError(t, foreign.SetManualMetadata(&foreignMeta))
	repo.existingKnowledge = &foreign
	_, err = svc.CreateKnowledgeFromProductionProjection(newCreateKnowledgeFileContext(), payload)
	require.ErrorIs(t, err, types.ErrProductionProjectionConflict)
	require.Zero(t, tasks.calls)
}

func TestCreateKnowledgeFromProductionProjectionRejectsContentDigestMismatchBeforeCreate(t *testing.T) {
	repo := &createKnowledgeFileRepoStub{}
	svc := &knowledgeService{repo: repo}
	_, err := svc.CreateKnowledgeFromProductionProjection(
		newCreateKnowledgeFileContext(),
		&types.ProductionProjectionKnowledgePayload{
			KnowledgeID: "knowledge-1", KnowledgeBaseID: "kb-1", Content: "# tampered",
			ProductionProjection: &types.ProductionProjectionMetadata{
				DocumentID: "doc-1", VersionID: "v3", ReleaseTargetID: "target-1",
				ContentDigest: strings.Repeat("a", 64),
			},
		},
	)
	require.ErrorIs(t, err, types.ErrProductionContentDigestMismatch)
	require.Zero(t, repo.createCalls)
}

func TestCreateKnowledgeFromProductionProjectionRetriesFailedOwnedKnowledgeOnce(t *testing.T) {
	owned := &types.Knowledge{
		ID: "knowledge-1", TenantID: 1, KnowledgeBaseID: "kb-1", Type: types.KnowledgeTypeManual,
		ParseStatus: types.ParseStatusFailed, ErrorMessage: "old internal failure",
	}
	meta := types.NewManualKnowledgeMetadata("# owned", types.ManualKnowledgeStatusPublish, 1)
	meta.ProductionProjection = &types.ProductionProjectionMetadata{
		DocumentID: "doc-1", VersionID: "v3", ReleaseTargetID: "target-1",
		ContentDigest:  projectionKnowledgeContentDigest("# owned"),
		SummaryModelID: "snapshot-summary", IndexingStrategy: types.IndexingStrategy{VectorEnabled: true},
	}
	require.NoError(t, owned.SetManualMetadata(meta))
	repo := &createKnowledgeFileRepoStub{existingKnowledge: owned}
	tasks := &createKnowledgeTaskEnqueuerStub{}
	tracker, spanDB := setupSpanTrackerTest(t)
	seedSpanTrackerKnowledgeTest(t, spanDB, owned.TenantID, owned.ID)
	svc := &knowledgeService{repo: repo, task: tasks, spanTracker: tracker}

	got, err := svc.CreateKnowledgeFromProductionProjection(
		newCreateKnowledgeFileContext(),
		&types.ProductionProjectionKnowledgePayload{
			KnowledgeID: "knowledge-1", KnowledgeBaseID: "kb-1",
			Content: "# owned", EmbeddingModelID: "snapshot-embedding", SummaryModelID: "snapshot-summary",
			IndexingStrategy:     types.IndexingStrategy{VectorEnabled: true},
			ProcessOverrides:     &types.KnowledgeProcessOverrides{ChunkingConfig: &types.ChunkingConfig{ChunkSize: 512}},
			ProductionProjection: meta.ProductionProjection,
		},
	)
	require.NoError(t, err)
	require.Equal(t, types.ParseStatusPending, got.ParseStatus)
	require.Equal(t, types.ParseStatusPending, repo.columnUpdates["parse_status"])
	require.Empty(t, repo.columnUpdates["error_message"])
	require.Equal(t, 1, tasks.calls)
}

func TestCreateKnowledgeFromProductionProjectionRearmsPendingWithDeterministicTaskID(t *testing.T) {
	owned := &types.Knowledge{
		ID: "knowledge-1", TenantID: 1, KnowledgeBaseID: "kb-1", Type: types.KnowledgeTypeManual,
		ParseStatus: types.ParseStatusPending,
	}
	meta := types.NewManualKnowledgeMetadata("# owned", types.ManualKnowledgeStatusPublish, 1)
	meta.ProductionProjection = &types.ProductionProjectionMetadata{
		DocumentID: "doc-1", VersionID: "v3", ReleaseTargetID: "target-1",
		ContentDigest:  projectionKnowledgeContentDigest("# owned"),
		SummaryModelID: "snapshot-summary", IndexingStrategy: types.IndexingStrategy{VectorEnabled: true},
	}
	require.NoError(t, owned.SetManualMetadata(meta))
	repo := &createKnowledgeFileRepoStub{existingKnowledge: owned}
	tasks := &createKnowledgeTaskEnqueuerStub{}
	tracker, spanDB := setupSpanTrackerTest(t)
	seedSpanTrackerKnowledgeTest(t, spanDB, owned.TenantID, owned.ID)
	svc := &knowledgeService{repo: repo, task: tasks, spanTracker: tracker}
	payload := &types.ProductionProjectionKnowledgePayload{
		KnowledgeID: "knowledge-1", KnowledgeBaseID: "kb-1", Content: "# owned",
		EmbeddingModelID: "snapshot-embedding", SummaryModelID: "snapshot-summary",
		IndexingStrategy:     types.IndexingStrategy{VectorEnabled: true},
		ProcessOverrides:     &types.KnowledgeProcessOverrides{ChunkingConfig: &types.ChunkingConfig{ChunkSize: 512}},
		ProductionProjection: meta.ProductionProjection,
	}

	_, err := svc.CreateKnowledgeFromProductionProjection(newCreateKnowledgeFileContext(), payload)
	require.NoError(t, err)
	require.Equal(t, []string{"production-projection-build-target-1-knowledge-1"}, tasks.taskIDs)

	tasks.enqueueErr = asynq.ErrTaskIDConflict
	_, err = svc.CreateKnowledgeFromProductionProjection(newCreateKnowledgeFileContext(), payload)
	require.NoError(t, err, "an ambiguous first enqueue replays as a successful duplicate")
	require.Equal(t, 2, tasks.calls)
}

func TestCreateKnowledgeFromProductionProjectionTreatsSameAttemptRetryTaskConflictAsDurableSuccess(t *testing.T) {
	owned := &types.Knowledge{
		ID: "knowledge-1", TenantID: 1, KnowledgeBaseID: "kb-1", Type: types.KnowledgeTypeManual,
		ParseStatus: types.ParseStatusFailed,
	}
	meta := types.NewManualKnowledgeMetadata("# owned", types.ManualKnowledgeStatusPublish, 1)
	meta.ProductionProjection = &types.ProductionProjectionMetadata{
		DocumentID: "doc-1", VersionID: "v3", ReleaseTargetID: "target-1",
		ContentDigest:  projectionKnowledgeContentDigest("# owned"),
		SummaryModelID: "snapshot-summary", IndexingStrategy: types.IndexingStrategy{VectorEnabled: true},
	}
	require.NoError(t, owned.SetManualMetadata(meta))
	repo := &createKnowledgeFileRepoStub{existingKnowledge: owned}
	tasks := &createKnowledgeTaskEnqueuerStub{enqueueErr: asynq.ErrTaskIDConflict}
	tracker, spanDB := setupSpanTrackerTest(t)
	seedSpanTrackerKnowledgeTest(t, spanDB, owned.TenantID, owned.ID)
	svc := &knowledgeService{repo: repo, task: tasks, spanTracker: tracker}

	_, err := svc.CreateKnowledgeFromProductionProjection(newCreateKnowledgeFileContext(), &types.ProductionProjectionKnowledgePayload{
		KnowledgeID: "knowledge-1", KnowledgeBaseID: "kb-1", Content: "# owned",
		EmbeddingModelID: "snapshot-embedding", SummaryModelID: "snapshot-summary",
		IndexingStrategy:     types.IndexingStrategy{VectorEnabled: true},
		ProcessOverrides:     &types.KnowledgeProcessOverrides{ChunkingConfig: &types.ChunkingConfig{ChunkSize: 512}},
		ProductionProjection: meta.ProductionProjection,
	})
	require.NoError(t, err)
	require.Equal(t, []string{productionProjectionAttemptTaskID("target-1", "knowledge-1", "retry", 1)}, tasks.taskIDs)
	require.Equal(t, types.ParseStatusPending, repo.columnUpdates["parse_status"])
}

func projectionKnowledgeContentDigest(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

func TestCreateKnowledgeFromFileDoesNotPersistWhenStorageSaveFails(t *testing.T) {
	t.Parallel()

	repo := &createKnowledgeFileRepoStub{}
	fileSvc := &createKnowledgeFileServiceStub{saveErr: errors.New("storage unavailable")}
	svc := &knowledgeService{
		repo:      repo,
		kbService: &createKnowledgeFileKBServiceStub{kb: &types.KnowledgeBase{ID: "kb-1"}},
		fileSvc:   fileSvc,
	}

	knowledge, err := svc.CreateKnowledgeFromFile(
		newCreateKnowledgeFileContext(),
		"kb-1",
		newMultipartFileHeader(t, "doc.txt", "hello"),
		nil,
		nil,
		"",
		nil,
		"",
		nil,
	)

	require.Error(t, err)
	require.Nil(t, knowledge)
	require.Equal(t, 1, fileSvc.saveCalls)
	require.Zero(t, repo.createCalls)
}

func TestCreateKnowledgeFromFilePersistsStoredFilePathOnCreate(t *testing.T) {
	t.Parallel()

	repo := &createKnowledgeFileRepoStub{}
	fileSvc := &createKnowledgeFileServiceStub{}
	task := &createKnowledgeTaskEnqueuerStub{}
	svc := &knowledgeService{
		repo:      repo,
		kbService: &createKnowledgeFileKBServiceStub{kb: &types.KnowledgeBase{ID: "kb-1"}},
		fileSvc:   fileSvc,
		task:      task,
	}

	knowledge, err := svc.CreateKnowledgeFromFile(
		newCreateKnowledgeFileContext(),
		"kb-1",
		newMultipartFileHeader(t, "doc.txt", "hello"),
		nil,
		nil,
		"",
		nil,
		"",
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, knowledge)
	require.Equal(t, 1, fileSvc.saveCalls)
	require.NotEmpty(t, fileSvc.savedWithKnowledgeID)
	require.Equal(t, fileSvc.savedWithKnowledgeID, knowledge.ID)
	require.Equal(t, 1, repo.createCalls)
	require.NotNil(t, repo.createdKnowledge)
	require.Equal(t, "stored/"+knowledge.ID, repo.createdKnowledge.FilePath)
	require.Equal(t, 1, task.calls)
}

func TestCreateKnowledgeFromFileDeletesStoredFileWhenCreateFails(t *testing.T) {
	t.Parallel()

	repo := &createKnowledgeFileRepoStub{createErr: errors.New("database unavailable")}
	fileSvc := &createKnowledgeFileServiceStub{}
	svc := &knowledgeService{
		repo:      repo,
		kbService: &createKnowledgeFileKBServiceStub{kb: &types.KnowledgeBase{ID: "kb-1"}},
		fileSvc:   fileSvc,
	}

	knowledge, err := svc.CreateKnowledgeFromFile(
		newCreateKnowledgeFileContext(),
		"kb-1",
		newMultipartFileHeader(t, "doc.txt", "hello"),
		nil,
		nil,
		"",
		nil,
		"",
		nil,
	)

	require.EqualError(t, err, "database unavailable")
	require.Nil(t, knowledge)
	require.Equal(t, 1, fileSvc.saveCalls)
	require.Equal(t, 1, repo.createCalls)
	require.Equal(t, 1, fileSvc.deleteCalls)
	require.Equal(t, "stored/"+fileSvc.savedWithKnowledgeID, fileSvc.deletedPath)
}

func TestCreateKnowledgeFromFile_PersistsProcessOverrides(t *testing.T) {
	t.Parallel()

	repo := &createKnowledgeFileRepoStub{}
	fileSvc := &createKnowledgeFileServiceStub{}
	task := &createKnowledgeTaskEnqueuerStub{}
	svc := &knowledgeService{
		repo:      repo,
		kbService: &createKnowledgeFileKBServiceStub{kb: &types.KnowledgeBase{ID: "kb-1"}},
		fileSvc:   fileSvc,
		task:      task,
	}

	chunkSize := 512
	overrides := &types.KnowledgeProcessOverrides{
		ChunkingConfig: &types.ChunkingConfig{ChunkSize: chunkSize},
	}

	knowledge, err := svc.CreateKnowledgeFromFile(
		newCreateKnowledgeFileContext(),
		"kb-1",
		newMultipartFileHeader(t, "doc.txt", "hello"),
		map[string]string{"source": "test"},
		nil,
		"",
		nil,
		"",
		overrides,
	)

	require.NoError(t, err)
	require.NotNil(t, knowledge)
	require.Equal(t, 1, repo.createCalls)
	require.NotNil(t, repo.createdKnowledge)

	parsed, err := repo.createdKnowledge.ProcessOverrides()
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.NotNil(t, parsed.ChunkingConfig)
	require.Equal(t, chunkSize, parsed.ChunkingConfig.ChunkSize)

	metadataMap, err := repo.createdKnowledge.Metadata.Map()
	require.NoError(t, err)
	require.Equal(t, "test", metadataMap["source"])
}

func newCreateKnowledgeFileContext() context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, &types.Tenant{})
	return withProductionProjectionGeneration(ctx, projectionManualGeneration)
}

func newMultipartFileHeader(t *testing.T, filename string, content string) *multipart.FileHeader {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = part.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest("POST", "/", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	require.NoError(t, req.ParseMultipartForm(1024))
	return req.MultipartForm.File["file"][0]
}
