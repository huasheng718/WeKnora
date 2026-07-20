package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	runHandlerDocumentID  = "72000000-0000-4000-8000-000000000001"
	runHandlerSourceSetID = "72000000-0000-4000-8000-000000000002"
	runHandlerRunID       = "72000000-0000-4000-8000-000000000003"
	runHandlerCallID      = "72000000-0000-4000-8000-000000000004"
	runHandlerModelID     = "72000000-0000-4000-8000-000000000005"
)

type productionRunHandlerServiceStub struct {
	startCalls      int
	collectionCalls int
	getCalls        int
	decisionCalls   int
	lastDecision    interfaces.ProductionToolDecision
	err             error
}

func (s *productionRunHandlerServiceStub) StartDocumentRun(_ context.Context, _ string, _ interfaces.StartProductionDocumentRunInput) (*types.ProductionRun, error) {
	s.startCalls++
	return &types.ProductionRun{ID: runHandlerRunID}, s.err
}
func (s *productionRunHandlerServiceStub) StartSourceSetCollection(_ context.Context, _ string, _ interfaces.StartProductionSourceSetCollectionInput) (*types.ProductionRun, error) {
	s.collectionCalls++
	return &types.ProductionRun{ID: runHandlerRunID}, s.err
}
func (s *productionRunHandlerServiceStub) GetRun(_ context.Context, _ string) (*types.ProductionRun, error) {
	s.getCalls++
	return &types.ProductionRun{ID: runHandlerRunID}, s.err
}
func (s *productionRunHandlerServiceStub) DecideToolCall(_ context.Context, _ string, decision interfaces.ProductionToolDecision) (*types.ProductionToolCall, error) {
	s.decisionCalls++
	s.lastDecision = decision
	return &types.ProductionToolCall{ID: runHandlerCallID}, s.err
}

func productionRunHandlerEngine(service interfaces.ProductionRunService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "72000000-0000-4000-8000-000000000006")
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleContributor)
		c.Request = c.Request.WithContext(ctx)
	})
	h := NewProductionRunHandler(service)
	engine.POST("/documents/:id/runs", h.Start)
	engine.POST("/source-sets/:id/collect", h.StartCollection)
	engine.GET("/runs/:id", h.Get)
	engine.POST("/tool-calls/:id/decision", h.DecideToolCall)
	return engine
}

func productionRunHandlerRequest(t *testing.T, engine *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)
	return recorder
}

func TestProductionRunHandlerStartsGetsAndCollects(t *testing.T) {
	service := &productionRunHandlerServiceStub{}
	engine := productionRunHandlerEngine(service)

	start := productionRunHandlerRequest(t, engine, http.MethodPost, "/documents/"+runHandlerDocumentID+"/runs",
		`{"run_type":"write","model_id":"`+runHandlerModelID+`"}`)
	collect := productionRunHandlerRequest(t, engine, http.MethodPost, "/source-sets/"+runHandlerSourceSetID+"/collect",
		`{"model_id":"`+runHandlerModelID+`"}`)
	get := productionRunHandlerRequest(t, engine, http.MethodGet, "/runs/"+runHandlerRunID, "")

	require.Equal(t, http.StatusAccepted, start.Code)
	require.Equal(t, http.StatusAccepted, collect.Code)
	require.Equal(t, http.StatusOK, get.Code)
	require.Equal(t, 1, service.startCalls)
	require.Equal(t, 1, service.collectionCalls)
	require.Equal(t, 1, service.getCalls)
}

func TestProductionRunHandlerStrictlyRejectsInvalidIDsBodiesAndDecisions(t *testing.T) {
	service := &productionRunHandlerServiceStub{}
	engine := productionRunHandlerEngine(service)

	for _, request := range []struct{ path, body string }{
		{"/documents/not-a-uuid/runs", `{"run_type":"write","model_id":"` + runHandlerModelID + `"}`},
		{"/documents/" + runHandlerDocumentID + "/runs", `{"run_type":"write","model_id":"` + runHandlerModelID + `","unknown":true}`},
		{"/tool-calls/" + runHandlerCallID + "/decision", `{"decision":"maybe"}`},
		{"/tool-calls/" + runHandlerCallID + "/decision", `{"decision":"approve","extra":1}`},
	} {
		response := productionRunHandlerRequest(t, engine, http.MethodPost, request.path, request.body)
		require.Equal(t, http.StatusBadRequest, response.Code, request.path+" "+request.body)
	}
	require.Zero(t, service.startCalls)
	require.Zero(t, service.decisionCalls)
}

func TestProductionRunHandlerAcceptsOnlyApproveOrReject(t *testing.T) {
	service := &productionRunHandlerServiceStub{}
	engine := productionRunHandlerEngine(service)

	approve := productionRunHandlerRequest(t, engine, http.MethodPost, "/tool-calls/"+runHandlerCallID+"/decision", `{"decision":"approve"}`)
	require.Equal(t, http.StatusOK, approve.Code)
	require.Equal(t, interfaces.ProductionToolDecisionApprove, service.lastDecision)

	reject := productionRunHandlerRequest(t, engine, http.MethodPost, "/tool-calls/"+runHandlerCallID+"/decision", `{"decision":"reject"}`)
	require.Equal(t, http.StatusOK, reject.Code)
	require.Equal(t, interfaces.ProductionToolDecisionReject, service.lastDecision)
}

var _ interfaces.ProductionRunService = (*productionRunHandlerServiceStub)(nil)
