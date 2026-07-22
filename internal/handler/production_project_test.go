package handler

import (
	"context"
	"encoding/json"
	"errors"
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
	productionProjectID      = "11111111-1111-4111-8111-111111111111"
	productionReviewerID     = "22222222-2222-4222-8222-222222222222"
	productionDocumentTypeID = "33333333-3333-4333-8333-333333333333"
)

type productionProjectServiceStub struct {
	projects []*types.ProductionProject
	created  interfaces.CreateProductionProjectInput
	assigned struct {
		projectID, userID string
		role              types.ProductionRole
	}
	removed struct {
		projectID, userID string
		role              types.ProductionRole
	}
	listTenant uint64
	listUser   string
	err        error
}

func (s *productionProjectServiceStub) CreateProject(_ context.Context, input interfaces.CreateProductionProjectInput) (*types.ProductionProject, error) {
	s.created = input
	if s.err != nil {
		return nil, s.err
	}
	return &types.ProductionProject{ID: productionProjectID, TenantID: 7, Name: input.Name, Description: input.Description, OwnerUserID: "author-1", Status: types.ProductionProjectActive}, nil
}
func (s *productionProjectServiceStub) GetProject(context.Context, uint64, string) (*types.ProductionProject, error) {
	return nil, s.err
}
func (s *productionProjectServiceStub) ListProjects(_ context.Context, tenantID uint64, userID string) ([]*types.ProductionProject, error) {
	s.listTenant, s.listUser = tenantID, userID
	return s.projects, s.err
}
func (s *productionProjectServiceStub) AssignRole(_ context.Context, projectID, userID string, role types.ProductionRole) error {
	s.assigned.projectID, s.assigned.userID, s.assigned.role = projectID, userID, role
	return s.err
}
func (s *productionProjectServiceStub) RemoveRole(_ context.Context, projectID, userID string, role types.ProductionRole) error {
	s.removed.projectID, s.removed.userID, s.removed.role = projectID, userID, role
	return s.err
}
func (s *productionProjectServiceStub) RequireProjectRole(context.Context, string, ...types.ProductionRole) error {
	return s.err
}
func (s *productionProjectServiceStub) HasLiveRoleAssignee(context.Context, uint64, string, types.ProductionRole) (bool, error) {
	return true, s.err
}

func newProductionHandlerContext(method, target, body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(request.Context(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "author-1")
	c.Request = request.WithContext(ctx)
	return c, recorder
}

func TestProductionProjectHandlerCreatesProject(t *testing.T) {
	service := &productionProjectServiceStub{}
	h := NewProductionProjectHandler(service)
	c, recorder := newProductionHandlerContext(http.MethodPost, "/production/projects", `{"name":"Baseline","description":"Core docs"}`)

	h.Create(c)

	require.Equal(t, http.StatusCreated, recorder.Code)
	require.Equal(t, "Baseline", service.created.Name)
	require.JSONEq(t, `{"success":true,"data":{"id":"11111111-1111-4111-8111-111111111111","tenant_id":7,"name":"Baseline","description":"Core docs","owner_user_id":"author-1","status":"active","created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z","deleted_at":null}}`, recorder.Body.String())
}

func TestProductionProjectHandlerEnforcesTrimmedNameCharacterLimit(t *testing.T) {
	makeBody := func(name string) string {
		body, err := json.Marshal(map[string]string{"name": name})
		require.NoError(t, err)
		return string(body)
	}

	t.Run("accepts 255 trimmed characters", func(t *testing.T) {
		service := &productionProjectServiceStub{}
		h := NewProductionProjectHandler(service)
		name := strings.Repeat("n", 255)

		response := performProductionHandlerRequest(
			http.MethodPost, "/production/projects", "/production/projects", makeBody("  "+name+"  "), h.Create,
		)

		require.Equal(t, http.StatusCreated, response.Code)
		require.Equal(t, name, service.created.Name)
	})

	t.Run("rejects 256 trimmed characters", func(t *testing.T) {
		service := &productionProjectServiceStub{}
		h := NewProductionProjectHandler(service)

		response := performProductionHandlerRequest(
			http.MethodPost, "/production/projects", "/production/projects",
			makeBody(strings.Repeat("n", 256)), h.Create,
		)

		require.Equal(t, http.StatusBadRequest, response.Code)
		require.Empty(t, service.created.Name)
	})
}

func TestProductionProjectHandlerListsOnlyContextTenantAndUser(t *testing.T) {
	service := &productionProjectServiceStub{projects: []*types.ProductionProject{{
		ID: productionProjectID, TenantID: 7, Name: "Baseline",
		CurrentUserRoles: []types.ProductionRole{types.ProductionRoleAuthor},
		Summary:          &types.ProductionProjectSummary{DocumentCount: 2, SourceSetCount: 1},
	}}}
	h := NewProductionProjectHandler(service)
	c, recorder := newProductionHandlerContext(http.MethodGet, "/production/projects", "")

	h.List(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, uint64(7), service.listTenant)
	require.Equal(t, "author-1", service.listUser)
	require.Contains(t, recorder.Body.String(), productionProjectID)
	require.Contains(t, recorder.Body.String(), `"current_user_roles":["author"]`)
	require.Contains(t, recorder.Body.String(), `"document_count":2`)
}

func TestProductionProjectHandlerPreservesServiceAuthorization(t *testing.T) {
	service := &productionProjectServiceStub{err: types.ErrProductionForbidden}
	h := NewProductionProjectHandler(service)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.POST("/production/projects", func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "viewer-1")
		c.Request = c.Request.WithContext(ctx)
		h.Create(c)
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/production/projects", strings.NewReader(`{"name":"Denied"}`))
	request.Header.Set("Content-Type", "application/json")

	engine.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusForbidden, recorder.Code)
}

func TestProductionProjectHandlerAssignsAndRemovesRoles(t *testing.T) {
	service := &productionProjectServiceStub{}
	h := NewProductionProjectHandler(service)
	assign, assignRecorder := newProductionHandlerContext(http.MethodPost, "/production/projects/"+productionProjectID+"/members", `{"user_id":"`+productionReviewerID+`","role":"business_reviewer"}`)
	assign.Params = gin.Params{{Key: "id", Value: productionProjectID}}
	h.AssignRole(assign)

	remove, removeRecorder := newProductionHandlerContext(http.MethodDelete, "/production/projects/"+productionProjectID+"/members/"+productionReviewerID+"/business_reviewer", "")
	remove.Params = gin.Params{{Key: "id", Value: productionProjectID}, {Key: "user_id", Value: productionReviewerID}, {Key: "role", Value: "business_reviewer"}}
	h.RemoveRole(remove)

	require.Equal(t, http.StatusOK, assignRecorder.Code)
	require.Equal(t, productionProjectID, service.assigned.projectID)
	require.Equal(t, productionReviewerID, service.assigned.userID)
	require.Equal(t, types.ProductionRoleBusinessReviewer, service.assigned.role)
	require.Equal(t, http.StatusOK, removeRecorder.Code)
	require.Equal(t, types.ProductionRoleBusinessReviewer, service.removed.role)
}

func performProductionHandlerRequest(
	method, pattern, path, body string,
	handler gin.HandlerFunc,
) *httptest.ResponseRecorder {
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Handle(method, pattern, func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "author-1")
		c.Request = c.Request.WithContext(ctx)
		handler(c)
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)
	return recorder
}

func TestProductionProjectHandlerRejectsMalformedResourceAndUserIDs(t *testing.T) {
	service := &productionProjectServiceStub{}
	h := NewProductionProjectHandler(service)
	tests := []struct {
		name, method, pattern, path, body string
		handler                           gin.HandlerFunc
	}{
		{
			name: "assign malformed project", method: http.MethodPost,
			pattern: "/production/projects/:id/members", path: "/production/projects/not-a-uuid/members",
			body: `{"user_id":"` + productionReviewerID + `","role":"author"}`, handler: h.AssignRole,
		},
		{
			name: "assign malformed user", method: http.MethodPost,
			pattern: "/production/projects/:id/members", path: "/production/projects/" + productionProjectID + "/members",
			body: `{"user_id":"not-a-uuid","role":"author"}`, handler: h.AssignRole,
		},
		{
			name: "remove malformed user", method: http.MethodDelete,
			pattern: "/production/projects/:id/members/:user_id/:role",
			path:    "/production/projects/" + productionProjectID + "/members/not-a-uuid/author",
			handler: h.RemoveRole,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performProductionHandlerRequest(
				test.method, test.pattern, test.path, test.body, test.handler,
			)
			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Contains(t, response.Body.String(), "valid UUID")
		})
	}
	require.Empty(t, service.assigned.projectID)
	require.Empty(t, service.removed.projectID)
}

func TestProductionHandlersMapConflictsAndUnexpectedFailures(t *testing.T) {
	t.Run("duplicate project role is conflict", func(t *testing.T) {
		service := &productionProjectServiceStub{err: types.ErrProductionConflict}
		h := NewProductionProjectHandler(service)
		response := performProductionHandlerRequest(
			http.MethodPost,
			"/production/projects/:id/members",
			"/production/projects/"+productionProjectID+"/members",
			`{"user_id":"`+productionReviewerID+`","role":"author"}`,
			h.AssignRole,
		)

		require.Equal(t, http.StatusConflict, response.Code)
	})

	t.Run("duplicate document type version is conflict", func(t *testing.T) {
		service := &productionDocumentTypeServiceStub{err: types.ErrProductionConflict}
		h := NewProductionDocumentTypeHandler(service)
		response := performProductionHandlerRequest(
			http.MethodPost,
			"/production/document-types",
			"/production/document-types",
			productionDocumentTypeRequestBody(t, "baseline", "Baseline", 1),
			h.Create,
		)

		require.Equal(t, http.StatusConflict, response.Code)
	})

	t.Run("non conflict failure remains internal error", func(t *testing.T) {
		service := &productionProjectServiceStub{err: errors.New("database unavailable")}
		h := NewProductionProjectHandler(service)
		response := performProductionHandlerRequest(
			http.MethodPost,
			"/production/projects/:id/members",
			"/production/projects/"+productionProjectID+"/members",
			`{"user_id":"`+productionReviewerID+`","role":"author"}`,
			h.AssignRole,
		)

		require.Equal(t, http.StatusInternalServerError, response.Code)
	})
}

var _ interfaces.ProductionProjectService = (*productionProjectServiceStub)(nil)
