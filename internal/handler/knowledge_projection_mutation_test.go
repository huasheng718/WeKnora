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
}

func (s *projectionMutationKnowledgeService) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	return s.byID[id], nil
}

func (s *projectionMutationKnowledgeService) GetKnowledgeBatch(_ context.Context, _ uint64, _ []string) ([]*types.Knowledge, error) {
	return s.batch, nil
}

func (s *projectionMutationKnowledgeService) ListKnowledgeByKnowledgeBaseID(_ context.Context, _ string) ([]*types.Knowledge, error) {
	return s.list, nil
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
