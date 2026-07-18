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
	return &types.ProductionProject{ID: "project-1", TenantID: 7, Name: input.Name, Description: input.Description, OwnerUserID: "author-1", Status: types.ProductionProjectActive}, nil
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
	require.JSONEq(t, `{"success":true,"data":{"id":"project-1","tenant_id":7,"name":"Baseline","description":"Core docs","owner_user_id":"author-1","status":"active","created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z","deleted_at":null}}`, recorder.Body.String())
}

func TestProductionProjectHandlerListsOnlyContextTenantAndUser(t *testing.T) {
	service := &productionProjectServiceStub{projects: []*types.ProductionProject{{ID: "project-1", TenantID: 7, Name: "Baseline"}}}
	h := NewProductionProjectHandler(service)
	c, recorder := newProductionHandlerContext(http.MethodGet, "/production/projects", "")

	h.List(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, uint64(7), service.listTenant)
	require.Equal(t, "author-1", service.listUser)
	require.Contains(t, recorder.Body.String(), `"project-1"`)
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
	assign, assignRecorder := newProductionHandlerContext(http.MethodPost, "/production/projects/project-1/members", `{"user_id":"reviewer-1","role":"business_reviewer"}`)
	assign.Params = gin.Params{{Key: "id", Value: "project-1"}}
	h.AssignRole(assign)

	remove, removeRecorder := newProductionHandlerContext(http.MethodDelete, "/production/projects/project-1/members/reviewer-1/business_reviewer", "")
	remove.Params = gin.Params{{Key: "id", Value: "project-1"}, {Key: "user_id", Value: "reviewer-1"}, {Key: "role", Value: "business_reviewer"}}
	h.RemoveRole(remove)

	require.Equal(t, http.StatusOK, assignRecorder.Code)
	require.Equal(t, "project-1", service.assigned.projectID)
	require.Equal(t, "reviewer-1", service.assigned.userID)
	require.Equal(t, types.ProductionRoleBusinessReviewer, service.assigned.role)
	require.Equal(t, http.StatusOK, removeRecorder.Code)
	require.Equal(t, types.ProductionRoleBusinessReviewer, service.removed.role)
}

var _ interfaces.ProductionProjectService = (*productionProjectServiceStub)(nil)
