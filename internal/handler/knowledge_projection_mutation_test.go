package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type projectionMutationKnowledgeService struct {
	interfaces.KnowledgeService
	byID  map[string]*types.Knowledge
	batch []*types.Knowledge
	list  []*types.Knowledge
	calls struct {
		update, manual, image, reparse, cancel, tags, moveProgress atomic.Int32
	}
}

func (s *projectionMutationKnowledgeService) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	knowledge := s.byID[id]
	if knowledge == nil {
		return nil, fmt.Errorf("knowledge not found")
	}
	return knowledge, nil
}

func (s *projectionMutationKnowledgeService) GetKnowledgeByID(ctx context.Context, id string) (*types.Knowledge, error) {
	knowledge, err := s.GetKnowledgeByIDOnly(ctx, id)
	if err != nil {
		return nil, err
	}
	tenantID, _ := types.TenantIDFromContext(ctx)
	if tenantID == 0 || knowledge.TenantID != tenantID {
		return nil, fmt.Errorf("knowledge not found")
	}
	return knowledge, nil
}

func (s *projectionMutationKnowledgeService) GetKnowledgeBatch(_ context.Context, tenantID uint64, ids []string) ([]*types.Knowledge, error) {
	if s.batch != nil {
		return s.batch, nil
	}
	result := make([]*types.Knowledge, 0, len(ids))
	for _, id := range ids {
		knowledge := s.byID[id]
		if knowledge == nil || knowledge.TenantID != tenantID {
			continue
		}
		result = append(result, knowledge)
	}
	return result, nil
}

func (s *projectionMutationKnowledgeService) ListKnowledgeByKnowledgeBaseID(_ context.Context, _ string) ([]*types.Knowledge, error) {
	return s.list, nil
}

func (s *projectionMutationKnowledgeService) UpdateKnowledge(context.Context, *types.Knowledge) error {
	s.calls.update.Add(1)
	return nil
}
func (s *projectionMutationKnowledgeService) UpdateManualKnowledge(context.Context, string, *types.ManualKnowledgePayload) (*types.Knowledge, error) {
	s.calls.manual.Add(1)
	return &types.Knowledge{}, nil
}
func (s *projectionMutationKnowledgeService) UpdateImageInfo(context.Context, string, string, string) error {
	s.calls.image.Add(1)
	return nil
}
func (s *projectionMutationKnowledgeService) ReparseKnowledge(context.Context, string, *types.KnowledgeProcessOverrides) (*types.Knowledge, error) {
	s.calls.reparse.Add(1)
	return &types.Knowledge{}, nil
}
func (s *projectionMutationKnowledgeService) CancelKnowledgeParse(context.Context, string) (*types.Knowledge, error) {
	s.calls.cancel.Add(1)
	return &types.Knowledge{}, nil
}
func (s *projectionMutationKnowledgeService) UpdateKnowledgeTagBatch(context.Context, string, map[string][]string) error {
	s.calls.tags.Add(1)
	return nil
}
func (s *projectionMutationKnowledgeService) SaveKnowledgeMoveProgress(context.Context, *types.KnowledgeMoveProgress) error {
	s.calls.moveProgress.Add(1)
	return nil
}

func (s *projectionMutationKnowledgeService) mutationCalls() int32 {
	return s.calls.update.Load() + s.calls.manual.Load() + s.calls.image.Load() + s.calls.reparse.Load() + s.calls.cancel.Load() + s.calls.tags.Load() + s.calls.moveProgress.Load()
}

func assertOnlyProjectionMutationCall(t *testing.T, service *projectionMutationKnowledgeService, operation string) {
	t.Helper()
	calls := map[string]int32{
		"update": service.calls.update.Load(), "manual": service.calls.manual.Load(), "image": service.calls.image.Load(),
		"reparse": service.calls.reparse.Load(), "cancel": service.calls.cancel.Load(), "tags": service.calls.tags.Load(), "move": service.calls.moveProgress.Load(),
	}
	for name, count := range calls {
		want := int32(0)
		if name == operation {
			want = 1
		}
		require.Equalf(t, want, count, "%s calls", name)
	}
}

type projectionMutationKBService struct {
	interfaces.KnowledgeBaseService
}

func (s *projectionMutationKBService) GetKnowledgeBaseByID(_ context.Context, id string) (*types.KnowledgeBase, error) {
	return &types.KnowledgeBase{ID: id, TenantID: 1, CreatorID: "user-1"}, nil
}

type projectionMutationTaskEnqueuer struct {
	calls atomic.Int32
}

func (e *projectionMutationTaskEnqueuer) Enqueue(*asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error) {
	e.calls.Add(1)
	return &asynq.TaskInfo{ID: "unexpected-task"}, nil
}

func projectionMutationKnowledge(t *testing.T, id, kbID string) *types.Knowledge {
	t.Helper()
	content := "# governed"
	digest := sha256.Sum256([]byte(content))
	knowledge := &types.Knowledge{
		ID: id, TenantID: 1, KnowledgeBaseID: kbID, Type: types.KnowledgeTypeManual,
	}
	meta := types.NewManualKnowledgeMetadata(content, types.ManualKnowledgeStatusPublish, 1)
	meta.ProductionProjection = &types.ProductionProjectionMetadata{
		DocumentID: "document-1", VersionID: "version-1", ReleaseTargetID: "target-1",
		ContentDigest: fmt.Sprintf("%x", digest[:]), SummaryModelID: "summary-1",
	}
	require.NoError(t, knowledge.SetManualMetadata(meta))
	return knowledge
}

func newProjectionMutationRouter(
	kg interfaces.KnowledgeService, tasks interfaces.TaskEnqueuer,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.ErrorHandler())
	router.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(1))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "user-1")
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleAdmin)
		c.Request = c.Request.WithContext(ctx)
		c.Set(types.TenantIDContextKey.String(), uint64(1))
		c.Set(types.UserIDContextKey.String(), "user-1")
		c.Next()
	})
	handler := &KnowledgeHandler{
		kgService: kg, kbService: &projectionMutationKBService{}, asynqClient: tasks,
	}
	router.DELETE("/knowledge/:id", handler.DeleteKnowledge)
	router.PUT("/knowledge/:id", handler.UpdateKnowledge)
	router.PUT("/knowledge/manual/:id", handler.UpdateManualKnowledge)
	router.PUT("/knowledge/image/:id/:chunk_id", handler.UpdateImageInfo)
	router.POST("/knowledge/:id/reparse", handler.ReparseKnowledge)
	router.POST("/knowledge/:id/cancel-parse", handler.CancelKnowledgeParse)
	router.PUT("/knowledge/tags", handler.UpdateKnowledgeTagBatch)
	router.POST("/knowledge/move", handler.MoveKnowledge)
	router.POST("/knowledge/batch-delete", handler.BatchDeleteKnowledge)
	router.POST("/knowledge/batch-reparse", handler.BatchReparseKnowledge)
	router.DELETE("/knowledge-bases/:id/knowledge", handler.ClearKnowledgeBaseContents)
	return router
}

func performProjectionMutationRequest(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func activeProductionKnowledge(t *testing.T, id string) *types.Knowledge {
	t.Helper()
	return projectionMutationKnowledge(t, id, "kb-1")
}

func TestDeleteKnowledgeRejectsActiveProductionProjection(t *testing.T) {
	projection := activeProductionKnowledge(t, "knowledge-1")
	tasks := &projectionMutationTaskEnqueuer{}
	router := newProjectionMutationRouter(&projectionMutationKnowledgeService{
		byID: map[string]*types.Knowledge{projection.ID: projection},
	}, tasks)

	response := performProjectionMutationRequest(t, router, http.MethodDelete, "/knowledge/"+projection.ID, nil)
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	require.Zero(t, tasks.calls.Load())
}

func TestBatchDeleteKnowledgeRejectsMixedProjectionBeforeEnqueue(t *testing.T) {
	ordinary := &types.Knowledge{ID: "ordinary-1", TenantID: 1, KnowledgeBaseID: "kb-1"}
	projection := projectionMutationKnowledge(t, "projection-1", "kb-1")
	tasks := &projectionMutationTaskEnqueuer{}
	router := newProjectionMutationRouter(&projectionMutationKnowledgeService{
		batch: []*types.Knowledge{ordinary, projection},
	}, tasks)

	response := performProjectionMutationRequest(t, router, http.MethodPost, "/knowledge/batch-delete", map[string]any{
		"kb_id": "kb-1", "ids": []string{ordinary.ID, projection.ID},
	})
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	require.Zero(t, tasks.calls.Load())
}

func TestBatchReparseKnowledgeRejectsMixedProjectionBeforeEnqueue(t *testing.T) {
	ordinary := &types.Knowledge{ID: "ordinary-1", TenantID: 1, KnowledgeBaseID: "kb-1"}
	projection := projectionMutationKnowledge(t, "projection-1", "kb-1")
	tasks := &projectionMutationTaskEnqueuer{}
	router := newProjectionMutationRouter(&projectionMutationKnowledgeService{
		batch: []*types.Knowledge{ordinary, projection},
	}, tasks)

	response := performProjectionMutationRequest(t, router, http.MethodPost, "/knowledge/batch-reparse", map[string]any{
		"kb_id": "kb-1", "ids": []string{ordinary.ID, projection.ID},
	})
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	require.Zero(t, tasks.calls.Load())
}

func TestClearKnowledgeBaseRejectsProjectionBeforeEnqueue(t *testing.T) {
	ordinary := &types.Knowledge{ID: "ordinary-1", TenantID: 1, KnowledgeBaseID: "kb-1"}
	projection := projectionMutationKnowledge(t, "projection-1", "kb-1")
	tasks := &projectionMutationTaskEnqueuer{}
	router := newProjectionMutationRouter(&projectionMutationKnowledgeService{
		list: []*types.Knowledge{ordinary, projection},
	}, tasks)

	response := performProjectionMutationRequest(t, router, http.MethodDelete, "/knowledge-bases/kb-1/knowledge", nil)
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	require.Zero(t, tasks.calls.Load())
}

func TestDirectKnowledgeMutationsRejectProductionProjectionWithoutSideEffects(t *testing.T) {
	projection := activeProductionKnowledge(t, "projection-1")
	for _, tc := range []struct {
		name, method, path string
		body               any
	}{
		{"update", http.MethodPut, "/knowledge/" + projection.ID, map[string]any{}},
		{"manual update", http.MethodPut, "/knowledge/manual/" + projection.ID, map[string]any{}},
		{"image update", http.MethodPut, "/knowledge/image/" + projection.ID + "/chunk-1", map[string]any{}},
		{"reparse", http.MethodPost, "/knowledge/" + projection.ID + "/reparse", nil},
		{"cancel", http.MethodPost, "/knowledge/" + projection.ID + "/cancel-parse", nil},
		{"tags", http.MethodPut, "/knowledge/tags", map[string]any{"updates": map[string][]string{projection.ID: {}}}},
		{"move", http.MethodPost, "/knowledge/move", map[string]any{"source_kb_id": "kb-1", "target_kb_id": "kb-2", "knowledge_ids": []string{projection.ID}, "mode": "reparse"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &projectionMutationKnowledgeService{byID: map[string]*types.Knowledge{projection.ID: projection}}
			tasks := &projectionMutationTaskEnqueuer{}
			response := performProjectionMutationRequest(t, newProjectionMutationRouter(service, tasks), tc.method, tc.path, tc.body)
			require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
			require.Zero(t, service.mutationCalls())
			require.Zero(t, tasks.calls.Load())
		})
	}
}

func TestDirectKnowledgeMutationsPermitOrdinaryKnowledgeAndRejectMixedOrUnauthorizedInputs(t *testing.T) {
	ordinary := &types.Knowledge{ID: "ordinary-1", TenantID: 1, KnowledgeBaseID: "kb-1", ParseStatus: types.ParseStatusCompleted}
	projection := activeProductionKnowledge(t, "projection-1")

	for _, tc := range []struct {
		name, operation, method, path string
		body                          any
	}{
		{"update", "update", http.MethodPut, "/knowledge/" + ordinary.ID, map[string]any{}},
		{"manual update", "manual", http.MethodPut, "/knowledge/manual/" + ordinary.ID, map[string]any{}},
		{"image update", "image", http.MethodPut, "/knowledge/image/" + ordinary.ID + "/chunk-1", map[string]any{}},
		{"reparse", "reparse", http.MethodPost, "/knowledge/" + ordinary.ID + "/reparse", nil},
		{"cancel", "cancel", http.MethodPost, "/knowledge/" + ordinary.ID + "/cancel-parse", nil},
		{"tags", "tags", http.MethodPut, "/knowledge/tags", map[string]any{"updates": map[string][]string{ordinary.ID: {}}}},
		{"move", "move", http.MethodPost, "/knowledge/move", map[string]any{"source_kb_id": "kb-1", "target_kb_id": "kb-2", "knowledge_ids": []string{ordinary.ID}, "mode": "reparse"}},
	} {
		t.Run("ordinary "+tc.name, func(t *testing.T) {
			service := &projectionMutationKnowledgeService{byID: map[string]*types.Knowledge{ordinary.ID: ordinary}}
			tasks := &projectionMutationTaskEnqueuer{}
			response := performProjectionMutationRequest(t, newProjectionMutationRouter(service, tasks), tc.method, tc.path, tc.body)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			assertOnlyProjectionMutationCall(t, service, tc.operation)
			if tc.operation == "move" {
				require.Equal(t, int32(1), tasks.calls.Load())
			} else {
				require.Zero(t, tasks.calls.Load())
			}
		})
	}

	for _, tc := range []struct {
		name, method, path string
		body               any
	}{
		{"mixed tags", http.MethodPut, "/knowledge/tags", map[string]any{"updates": map[string][]string{ordinary.ID: {}, projection.ID: {}}}},
		{"mixed move", http.MethodPost, "/knowledge/move", map[string]any{"source_kb_id": "kb-1", "target_kb_id": "kb-2", "knowledge_ids": []string{ordinary.ID, projection.ID}, "mode": "reparse"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &projectionMutationKnowledgeService{byID: map[string]*types.Knowledge{ordinary.ID: ordinary, projection.ID: projection}}
			tasks := &projectionMutationTaskEnqueuer{}
			response := performProjectionMutationRequest(t, newProjectionMutationRouter(service, tasks), tc.method, tc.path, tc.body)
			require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
			require.Zero(t, service.mutationCalls())
			require.Zero(t, tasks.calls.Load())
		})
	}

	t.Run("not found", func(t *testing.T) {
		service := &projectionMutationKnowledgeService{byID: map[string]*types.Knowledge{}}
		response := performProjectionMutationRequest(t, newProjectionMutationRouter(service, &projectionMutationTaskEnqueuer{}), http.MethodPut, "/knowledge/missing", map[string]any{})
		require.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
		require.Zero(t, service.mutationCalls())
	})

	t.Run("cross tenant", func(t *testing.T) {
		foreign := &types.Knowledge{ID: "foreign-1", TenantID: 2, KnowledgeBaseID: "kb-foreign"}
		service := &projectionMutationKnowledgeService{byID: map[string]*types.Knowledge{foreign.ID: foreign}}
		response := performProjectionMutationRequest(t, newProjectionMutationRouter(service, &projectionMutationTaskEnqueuer{}), http.MethodPut, "/knowledge/"+foreign.ID, map[string]any{})
		require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
		require.Zero(t, service.mutationCalls())
	})

	for _, tc := range []struct {
		name, method, path string
		body               any
		want               int
	}{
		{"tag missing", http.MethodPut, "/knowledge/tags", map[string]any{"updates": map[string][]string{ordinary.ID: {}, "missing": {}}}, http.StatusBadRequest},
		{"tag cross tenant", http.MethodPut, "/knowledge/tags", map[string]any{"updates": map[string][]string{ordinary.ID: {}, "foreign-1": {}}}, http.StatusBadRequest},
		{"move missing", http.MethodPost, "/knowledge/move", map[string]any{"source_kb_id": "kb-1", "target_kb_id": "kb-2", "knowledge_ids": []string{ordinary.ID, "missing"}, "mode": "reparse"}, http.StatusBadRequest},
		{"move cross tenant", http.MethodPost, "/knowledge/move", map[string]any{"source_kb_id": "kb-1", "target_kb_id": "kb-2", "knowledge_ids": []string{ordinary.ID, "foreign-1"}, "mode": "reparse"}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			foreign := &types.Knowledge{ID: "foreign-1", TenantID: 2, KnowledgeBaseID: "kb-1", ParseStatus: types.ParseStatusCompleted}
			service := &projectionMutationKnowledgeService{byID: map[string]*types.Knowledge{ordinary.ID: ordinary, foreign.ID: foreign}}
			tasks := &projectionMutationTaskEnqueuer{}
			response := performProjectionMutationRequest(t, newProjectionMutationRouter(service, tasks), tc.method, tc.path, tc.body)
			require.Equal(t, tc.want, response.Code, response.Body.String())
			require.Zero(t, service.mutationCalls())
			require.Zero(t, tasks.calls.Load())
		})
	}
}
