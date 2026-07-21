package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	appservice "github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type projectionHTTPKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	rows []*types.Knowledge
}

func (r *projectionHTTPKnowledgeRepo) GetKnowledgeBatch(context.Context, uint64, []string) ([]*types.Knowledge, error) {
	return r.rows, nil
}

type projectionHTTPReleaseRepo struct {
	interfaces.ProductionReleaseRepository
}

func (*projectionHTTPReleaseRepo) ResolveScopesForKnowledgeIDs(context.Context, uint64, []string) (map[string]types.ProductionKnowledgeScope, error) {
	return map[string]types.ProductionKnowledgeScope{
		"kb-1": {
			ActiveKnowledgeIDs:        []string{"active"},
			InactiveKnowledgeIDs:      []string{"inactive"},
			AllProductionKnowledgeIDs: []string{"active", "inactive"},
		},
	}, nil
}

func newProjectionHTTPKnowledgeService(t *testing.T) interfaces.KnowledgeService {
	t.Helper()
	service, err := appservice.NewKnowledgeService(
		nil,
		&projectionHTTPKnowledgeRepo{rows: []*types.Knowledge{
			{ID: "inactive", TenantID: 7, KnowledgeBaseID: "kb-1", Title: "SECRET HISTORY"},
			{ID: "active", TenantID: 7, KnowledgeBaseID: "kb-1", Title: "ACTIVE DOCUMENT"},
			{ID: "ordinary", TenantID: 7, KnowledgeBaseID: "kb-1", Title: "ORDINARY DOCUMENT"},
		}},
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		&projectionHTTPReleaseRepo{},
	)
	require.NoError(t, err)
	return service
}

func TestKnowledgeBatchHTTPDoesNotExposeInactiveProjectionWithoutUserContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.ErrorHandler())
	router.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		c.Request = c.Request.WithContext(ctx)
		c.Set(types.TenantIDContextKey.String(), uint64(7))
		c.Next()
	})
	handler := &KnowledgeHandler{kgService: newProjectionHTTPKnowledgeService(t)}
	router.GET("/knowledge/batch", handler.GetKnowledgeBatch)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/knowledge/batch?ids=inactive&ids=active&ids=ordinary", nil)
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NotContains(t, recorder.Body.String(), "SECRET HISTORY")
	require.NotContains(t, recorder.Body.String(), `"id":"inactive"`)
	require.Contains(t, recorder.Body.String(), `"id":"active"`)
	require.Contains(t, recorder.Body.String(), `"read_only":true`)
	require.Contains(t, recorder.Body.String(), `"id":"ordinary"`)
}
