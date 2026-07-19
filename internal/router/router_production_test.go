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

func (r *productionRouterIdempotencyRepo) WithinTransaction(
	ctx context.Context,
	fn func(context.Context) error,
) error {
	return fn(ctx)
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

func (r *productionRouterIdempotencyRepo) Release(_ context.Context, id string) error {
	for key, record := range r.records {
		if record.ID == id && record.StatusCode == nil {
			delete(r.records, key)
		}
	}
	return nil
}

type productionRouterProjectService struct {
	createCalls int
}

type productionRouterSourceService struct {
	freezeCalls int
	err         error
}

func (s *productionRouterSourceService) CreateSet(context.Context, interfaces.CreateProductionSourceSetInput) (*types.ProductionSourceSet, error) {
	return nil, s.err
}
func (s *productionRouterSourceService) AddItem(context.Context, string, interfaces.CreateProductionSourceItemInput) (*types.ProductionSourceItem, error) {
	return nil, s.err
}
func (s *productionRouterSourceService) DecideItem(context.Context, string, types.ProductionSourceItemStatus) error {
	return s.err
}
func (s *productionRouterSourceService) AttachEvidence(context.Context, string, interfaces.CreateEvidenceSnapshotInput) (*types.ProductionEvidenceSnapshot, error) {
	return nil, s.err
}
func (s *productionRouterSourceService) Freeze(context.Context, string) error {
	s.freezeCalls++
	return s.err
}

type productionRouterDocumentService struct {
	listCalls int
	err       error
}

func (s *productionRouterDocumentService) CreateDocument(context.Context, interfaces.CreateProductionDocumentInput) (*types.ProductionDocument, error) {
	return nil, s.err
}
func (s *productionRouterDocumentService) AppendVersion(context.Context, string, interfaces.AppendProductionVersionInput) (*types.ProductionDocumentVersion, error) {
	return nil, s.err
}
func (s *productionRouterDocumentService) GetVersion(context.Context, string) (*types.ProductionDocumentVersion, error) {
	return nil, s.err
}
func (s *productionRouterDocumentService) ListVersions(context.Context, string) ([]*types.ProductionDocumentVersion, error) {
	s.listCalls++
	return []*types.ProductionDocumentVersion{}, s.err
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
	return newProductionRouteTestEngineForRole(
		projectHandler,
		handler.NewProductionSourceHandler(&productionRouterSourceService{}),
		handler.NewProductionDocumentHandler(&productionRouterDocumentService{}),
		repo,
		types.TenantRoleAdmin,
	)
}

func newProductionRouteTestEngineForRole(
	projectHandler *handler.ProductionProjectHandler,
	sourceHandler *handler.ProductionSourceHandler,
	documentHandler *handler.ProductionDocumentHandler,
	repo interfaces.ProductionIdempotencyRepository,
	role types.TenantRole,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	enforce := true
	guards := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &enforce}}}
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "author-1")
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, role)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	v1 := engine.Group("/api/v1")
	RegisterProductionRoutes(
		v1,
		projectHandler,
		&handler.ProductionDocumentTypeHandler{},
		sourceHandler,
		documentHandler,
		guards,
		middleware.NewProductionIdempotencyMiddleware(repo),
	)
	return engine
}

func TestProductionRouteRBACRunsBeforeIdempotencyAndServiceAuthorizationRemainsAuthoritative(t *testing.T) {
	const documentID = "66666666-6666-4666-8666-666666666666"
	const sourceSetID = "44444444-4444-4444-8444-444444444444"

	t.Run("viewer reads versions but cannot enter write idempotency", func(t *testing.T) {
		repo := newProductionRouterIdempotencyRepo()
		documents := &productionRouterDocumentService{}
		engine := newProductionRouteTestEngineForRole(
			&handler.ProductionProjectHandler{},
			handler.NewProductionSourceHandler(&productionRouterSourceService{}),
			handler.NewProductionDocumentHandler(documents), repo, types.TenantRoleViewer,
		)
		read := httptest.NewRecorder()
		engine.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/v1/production/documents/"+documentID+"/versions", nil))
		require.Equal(t, http.StatusOK, read.Code)
		require.Equal(t, 1, documents.listCalls)

		write := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/production/source-sets/"+sourceSetID+"/freeze", nil)
		request.Header.Set("Idempotency-Key", "viewer-write")
		engine.ServeHTTP(write, request)
		require.Equal(t, http.StatusForbidden, write.Code)
		require.Empty(t, repo.records)
	})

	t.Run("contributor passes route gate but project service can deny", func(t *testing.T) {
		repo := newProductionRouterIdempotencyRepo()
		sources := &productionRouterSourceService{err: types.ErrProductionForbidden}
		engine := newProductionRouteTestEngineForRole(
			&handler.ProductionProjectHandler{}, handler.NewProductionSourceHandler(sources),
			handler.NewProductionDocumentHandler(&productionRouterDocumentService{}), repo, types.TenantRoleContributor,
		)
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/production/source-sets/"+sourceSetID+"/freeze", nil)
		request.Header.Set("Idempotency-Key", "contributor-write")
		engine.ServeHTTP(response, request)
		require.Equal(t, http.StatusForbidden, response.Code)
		require.Equal(t, 1, sources.freezeCalls)
		require.Empty(t, repo.records)
	})
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
		{http.MethodPost, "/api/v1/production/projects/:id/source-sets"},
		{http.MethodPut, "/api/v1/production/source-items/:id/decision"},
		{http.MethodPost, "/api/v1/production/source-sets/:id/freeze"},
		{http.MethodPost, "/api/v1/production/projects/:id/documents"},
		{http.MethodGet, "/api/v1/production/documents/:id/versions"},
		{http.MethodPost, "/api/v1/production/documents/:id/versions"},
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
		{http.MethodPost, "/api/v1/production/projects/project-1/source-sets", `{}`},
		{http.MethodPut, "/api/v1/production/source-items/item-1/decision", `{}`},
		{http.MethodPost, "/api/v1/production/source-sets/set-1/freeze", ""},
		{http.MethodPost, "/api/v1/production/projects/project-1/documents", `{}`},
		{http.MethodPost, "/api/v1/production/documents/document-1/versions", `{}`},
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
var _ interfaces.ProductionSourceService = (*productionRouterSourceService)(nil)
var _ interfaces.ProductionDocumentService = (*productionRouterDocumentService)(nil)
