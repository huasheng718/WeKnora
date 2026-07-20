package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appservice "github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-contrib/cors"
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
func (s *productionRouterSourceService) GetEvidence(context.Context, string) (*types.ProductionEvidenceSnapshot, *types.ProductionSourceItem, *types.ProductionSourceSet, error) {
	return nil, nil, nil, s.err
}
func (s *productionRouterSourceService) Freeze(context.Context, string) error {
	s.freezeCalls++
	return s.err
}

type productionRouterDocumentService struct {
	listCalls int
	err       error
}

type productionRouterRunService struct{}

type productionRouterAnnotationService struct{}

func (*productionRouterAnnotationService) List(context.Context, appservice.ListProductionAnnotationsInput) (*appservice.ProductionAnnotationPage, error) {
	return &appservice.ProductionAnnotationPage{Data: []*types.ProductionAnnotation{}}, nil
}

func (*productionRouterAnnotationService) Create(context.Context, appservice.CreateProductionAnnotationInput) (*types.ProductionAnnotation, error) {
	return &types.ProductionAnnotation{}, nil
}
func (*productionRouterAnnotationService) Resolve(context.Context, string, types.ProductionAnnotationStatus) error {
	return nil
}

type productionRouterReviewService struct {
	rejectCalls int
	cancelCalls int
}

func (*productionRouterReviewService) Submit(context.Context, string, string) (*types.ProductionReviewRequest, error) {
	return &types.ProductionReviewRequest{}, nil
}
func (*productionRouterReviewService) Get(context.Context, string) (*types.ProductionReviewRequest, error) {
	return &types.ProductionReviewRequest{}, nil
}
func (*productionRouterReviewService) Decide(context.Context, string, types.ProductionReviewDecision, string) error {
	return nil
}
func (s *productionRouterReviewService) Reject(context.Context, string, string) error {
	s.rejectCalls++
	return nil
}
func (s *productionRouterReviewService) Cancel(context.Context, string, string) error {
	s.cancelCalls++
	return nil
}

func (s *productionRouterRunService) StartDocumentRun(context.Context, string, interfaces.StartProductionDocumentRunInput) (*types.ProductionRun, error) {
	return &types.ProductionRun{}, nil
}
func (s *productionRouterRunService) StartSourceSetCollection(context.Context, string, interfaces.StartProductionSourceSetCollectionInput) (*types.ProductionRun, error) {
	return &types.ProductionRun{}, nil
}
func (s *productionRouterRunService) GetRun(context.Context, string) (*types.ProductionRun, error) {
	return &types.ProductionRun{}, nil
}
func (s *productionRouterRunService) DecideToolCall(context.Context, string, interfaces.ProductionToolDecision) (*types.ProductionToolCall, error) {
	return &types.ProductionToolCall{}, nil
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
		handler.NewProductionRunHandler(&productionRouterRunService{}),
		repo,
		types.TenantRoleAdmin,
	)
}

func newProductionRouteTestEngineForRole(
	projectHandler *handler.ProductionProjectHandler,
	sourceHandler *handler.ProductionSourceHandler,
	documentHandler *handler.ProductionDocumentHandler,
	runHandler *handler.ProductionRunHandler,
	repo interfaces.ProductionIdempotencyRepository,
	role types.TenantRole,
) *gin.Engine {
	return newProductionRouteTestEngineForRoleAndReview(
		projectHandler, sourceHandler, documentHandler, runHandler,
		&productionRouterReviewService{}, repo, role,
	)
}

func newProductionRouteTestEngineForRoleAndReview(
	projectHandler *handler.ProductionProjectHandler,
	sourceHandler *handler.ProductionSourceHandler,
	documentHandler *handler.ProductionDocumentHandler,
	runHandler *handler.ProductionRunHandler,
	reviewService *productionRouterReviewService,
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
		runHandler,
		handler.NewProductionReviewHandler(&productionRouterAnnotationService{}, reviewService),
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
			handler.NewProductionDocumentHandler(documents), handler.NewProductionRunHandler(&productionRouterRunService{}), repo, types.TenantRoleViewer,
		)
		read := httptest.NewRecorder()
		engine.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/v1/production/documents/"+documentID+"/versions", nil))
		require.Equal(t, http.StatusOK, read.Code)
		require.Equal(t, 1, documents.listCalls)

		annotations := httptest.NewRecorder()
		engine.ServeHTTP(annotations, httptest.NewRequest(http.MethodGet,
			"/api/v1/production/documents/"+documentID+"/annotations", nil))
		require.Equal(t, http.StatusOK, annotations.Code, annotations.Body.String())
		require.Empty(t, repo.records)

		review := httptest.NewRecorder()
		engine.ServeHTTP(review, httptest.NewRequest(http.MethodGet,
			"/api/v1/production/reviews/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", nil))
		require.Equal(t, http.StatusOK, review.Code, review.Body.String())
		require.Empty(t, repo.records)

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
			handler.NewProductionDocumentHandler(&productionRouterDocumentService{}), handler.NewProductionRunHandler(&productionRouterRunService{}), repo, types.TenantRoleContributor,
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

func TestProductionCORSAllowsIfMatchPreflight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(cors.New(routerCORSConfig()))
	engine.POST("/api/v1/production/documents/:id/versions", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(
		http.MethodOptions,
		"/api/v1/production/documents/66666666-6666-4666-8666-666666666666/versions",
		nil,
	)
	request.Header.Set("Origin", "https://workbench.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "Content-Type, Idempotency-Key, If-Match")
	response := httptest.NewRecorder()

	engine.ServeHTTP(response, request)

	require.Equal(t, http.StatusNoContent, response.Code)
	require.Contains(t, strings.ToLower(response.Header().Get("Access-Control-Allow-Headers")), "if-match")
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

func TestProductionReviewRoutesAreRegistered(t *testing.T) {
	engine := newProductionRouteTestEngine(&handler.ProductionProjectHandler{}, newProductionRouterIdempotencyRepo())

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/production/documents/:id/annotations"},
		{http.MethodPost, "/api/v1/production/documents/:id/annotations"},
		{http.MethodPut, "/api/v1/production/annotations/:id/status"},
		{http.MethodPost, "/api/v1/production/documents/:id/reviews"},
		{http.MethodGet, "/api/v1/production/reviews/:id"},
		{http.MethodPost, "/api/v1/production/reviews/:id/steps/:step_id/decision"},
		{http.MethodPost, "/api/v1/production/reviews/:id/reject"},
		{http.MethodPost, "/api/v1/production/reviews/:id/cancel"},
	} {
		assertProductionRoute(t, engine, route.method, route.path)
	}
}

func TestProductionReviewTerminalRoutesRequireAdminBeforeIdempotencyAndReplay(t *testing.T) {
	const reviewID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

	t.Run("admin reject replays", func(t *testing.T) {
		repo := newProductionRouterIdempotencyRepo()
		reviews := &productionRouterReviewService{}
		engine := newProductionRouteTestEngineForRoleAndReview(
			&handler.ProductionProjectHandler{}, handler.NewProductionSourceHandler(&productionRouterSourceService{}),
			handler.NewProductionDocumentHandler(&productionRouterDocumentService{}), handler.NewProductionRunHandler(&productionRouterRunService{}),
			reviews, repo, types.TenantRoleAdmin,
		)
		for range 2 {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/production/reviews/"+reviewID+"/reject", strings.NewReader(`{"reason":"admin rejection"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "terminal-replay")
			engine.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		}
		require.Equal(t, 1, reviews.rejectCalls)
		require.Len(t, repo.records, 1)
	})

	t.Run("contributor professional cannot enter terminal route", func(t *testing.T) {
		repo := newProductionRouterIdempotencyRepo()
		reviews := &productionRouterReviewService{}
		engine := newProductionRouteTestEngineForRoleAndReview(
			&handler.ProductionProjectHandler{}, handler.NewProductionSourceHandler(&productionRouterSourceService{}),
			handler.NewProductionDocumentHandler(&productionRouterDocumentService{}), handler.NewProductionRunHandler(&productionRouterRunService{}),
			reviews, repo, types.TenantRoleContributor,
		)
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/production/reviews/"+reviewID+"/cancel", strings.NewReader(`{"reason":"professional cancellation"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "forbidden-terminal")
		engine.ServeHTTP(response, request)
		require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
		require.Zero(t, reviews.cancelCalls)
		require.Empty(t, repo.records)
	})
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
		{http.MethodPost, "/api/v1/production/documents/:id/runs"},
		{http.MethodPost, "/api/v1/production/source-sets/:id/collect"},
		{http.MethodGet, "/api/v1/production/runs/:id"},
		{http.MethodPost, "/api/v1/production/tool-calls/:id/decision"},
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
		{http.MethodPost, "/api/v1/production/documents/document-1/runs", `{}`},
		{http.MethodPost, "/api/v1/production/source-sets/set-1/collect", `{}`},
		{http.MethodPost, "/api/v1/production/tool-calls/call-1/decision", `{}`},
		{http.MethodPost, "/api/v1/production/documents/document-1/annotations", `{}`},
		{http.MethodPut, "/api/v1/production/annotations/annotation-1/status", `{}`},
		{http.MethodPost, "/api/v1/production/documents/document-1/reviews", `{}`},
		{http.MethodPost, "/api/v1/production/reviews/review-1/steps/step-1/decision", `{}`},
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
var _ interfaces.ProductionRunService = (*productionRouterRunService)(nil)
