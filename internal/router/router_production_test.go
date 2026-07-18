package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type productionRouterIdempotencyRepo struct {
	records map[string]*types.ProductionIdempotencyKey
}

func newProductionRouterIdempotencyRepo() *productionRouterIdempotencyRepo {
	return &productionRouterIdempotencyRepo{records: make(map[string]*types.ProductionIdempotencyKey)}
}

func (r *productionRouterIdempotencyRepo) Reserve(
	_ context.Context,
	record *types.ProductionIdempotencyKey,
) (*types.ProductionIdempotencyKey, bool, error) {
	key := strings.Join([]string{record.ActorUserID, record.Route, record.IdempotencyKey}, "\x00")
	if existing, ok := r.records[key]; ok {
		copy := *existing
		return &copy, false, nil
	}
	copy := *record
	r.records[key] = &copy
	return record, true, nil
}

func (r *productionRouterIdempotencyRepo) Complete(
	_ context.Context,
	id string,
	statusCode int,
	responseBody types.JSON,
) error {
	for _, record := range r.records {
		if record.ID == id {
			status := statusCode
			record.StatusCode = &status
			record.ResponseBody = append(types.JSON(nil), responseBody...)
		}
	}
	return nil
}

type productionRouterProjectService struct {
	createCalls int
}

func (s *productionRouterProjectService) CreateProject(_ context.Context, input interfaces.CreateProductionProjectInput) (*types.ProductionProject, error) {
	s.createCalls++
	return &types.ProductionProject{ID: "project-1", TenantID: 7, Name: input.Name, OwnerUserID: "author-1", Status: types.ProductionProjectActive}, nil
}
func (s *productionRouterProjectService) GetProject(context.Context, uint64, string) (*types.ProductionProject, error) {
	return nil, nil
}
func (s *productionRouterProjectService) ListProjects(context.Context, uint64, string) ([]*types.ProductionProject, error) {
	return nil, nil
}
func (s *productionRouterProjectService) AssignRole(context.Context, string, string, types.ProductionRole) error {
	return nil
}
func (s *productionRouterProjectService) RemoveRole(context.Context, string, string, types.ProductionRole) error {
	return nil
}
func (s *productionRouterProjectService) RequireProjectRole(context.Context, string, ...types.ProductionRole) error {
	return nil
}

func newProductionRouteTestEngine(
	projectHandler *handler.ProductionProjectHandler,
	repo interfaces.ProductionIdempotencyRepository,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	enforce := true
	guards := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &enforce}}}
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "author-1")
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleAdmin)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	v1 := engine.Group("/api/v1")
	RegisterProductionRoutes(
		v1,
		projectHandler,
		&handler.ProductionDocumentTypeHandler{},
		guards,
		middleware.NewProductionIdempotencyMiddleware(repo),
	)
	return engine
}

func assertProductionRoute(t *testing.T, engine *gin.Engine, method, path string) {
	t.Helper()
	for _, route := range engine.Routes() {
		if route.Method == method && route.Path == path {
			return
		}
	}
	t.Fatalf("route %s %s is not registered", method, path)
}

func TestProductionFoundationRoutesAreRegistered(t *testing.T) {
	engine := newProductionRouteTestEngine(&handler.ProductionProjectHandler{}, newProductionRouterIdempotencyRepo())

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/production/projects"},
		{http.MethodPost, "/api/v1/production/projects"},
		{http.MethodPost, "/api/v1/production/projects/:id/members"},
		{http.MethodDelete, "/api/v1/production/projects/:id/members/:user_id/:role"},
		{http.MethodGet, "/api/v1/production/document-types"},
		{http.MethodPost, "/api/v1/production/document-types"},
		{http.MethodPut, "/api/v1/production/document-types/:id/activate"},
	} {
		assertProductionRoute(t, engine, route.method, route.path)
	}
}

func TestEveryProductionWriteRouteRequiresIdempotencyKey(t *testing.T) {
	engine := newProductionRouteTestEngine(&handler.ProductionProjectHandler{}, newProductionRouterIdempotencyRepo())

	for _, request := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/production/projects", `{}`},
		{http.MethodPost, "/api/v1/production/projects/project-1/members", `{}`},
		{http.MethodDelete, "/api/v1/production/projects/project-1/members/user-1/author", ""},
		{http.MethodPost, "/api/v1/production/document-types", `{}`},
		{http.MethodPut, "/api/v1/production/document-types/type-1/activate", ""},
	} {
		t.Run(request.method+" "+request.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			httpRequest := httptest.NewRequest(request.method, request.path, strings.NewReader(request.body))
			httpRequest.Header.Set("Content-Type", "application/json")
			engine.ServeHTTP(recorder, httpRequest)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), "PRODUCTION_IDEMPOTENCY_KEY_REQUIRED")
		})
	}
}

func TestProductionWriteReplaysSameIdempotencyKey(t *testing.T) {
	service := &productionRouterProjectService{}
	engine := newProductionRouteTestEngine(handler.NewProductionProjectHandler(service), newProductionRouterIdempotencyRepo())

	post := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/production/projects", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "request-1")
		engine.ServeHTTP(recorder, request)
		return recorder
	}
	first := post(`{"name":"Baseline"}`)
	replay := post(`{ "name": "Baseline" }`)

	require.Equal(t, http.StatusCreated, first.Code)
	require.Equal(t, first.Code, replay.Code)
	require.Equal(t, first.Body.String(), replay.Body.String())
	require.Equal(t, 1, service.createCalls)
}

var _ interfaces.ProductionIdempotencyRepository = (*productionRouterIdempotencyRepo)(nil)
var _ interfaces.ProductionProjectService = (*productionRouterProjectService)(nil)
