package handler

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

func tenantPolicyErrorCapture() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if len(c.Errors) == 0 {
			return
		}
		if appErr, ok := c.Errors.Last().Err.(*apperrors.AppError); ok {
			c.JSON(appErr.HTTPCode, gin.H{"error": appErr})
		}
	}
}

type tenantPolicySettingService struct {
	interfaces.SystemSettingService
	enabled bool
}

func (s *tenantPolicySettingService) GetBool(context.Context, string, string, bool) bool {
	return s.enabled
}

func (s *tenantPolicySettingService) GetInt(_ context.Context, _ string, _ string, def int64) int64 {
	return def
}

type tenantPolicyUserService struct {
	interfaces.UserService
	user      *types.User
	updateErr error
}

func (s *tenantPolicyUserService) GetCurrentUser(context.Context) (*types.User, error) {
	return s.user, nil
}

func (s *tenantPolicyUserService) BuildLoginMemberships(context.Context, *types.User, *types.Tenant) []types.Membership {
	return []types.Membership{}
}

func (s *tenantPolicyUserService) UpdateUser(context.Context, *types.User) error {
	return s.updateErr
}

type tenantPolicyTenantService struct {
	interfaces.TenantService
	createCalls int
	deleteCalls int
	purgeCalls  int
}

func (s *tenantPolicyTenantService) CreateTenant(_ context.Context, tenant *types.Tenant) (*types.Tenant, error) {
	s.createCalls++
	tenant.ID = 99
	return tenant, nil
}

func (s *tenantPolicyTenantService) DeleteTenant(context.Context, uint64) error {
	s.deleteCalls++
	return nil
}

func (s *tenantPolicyTenantService) PurgeProvisionedTenant(context.Context, uint64) error {
	s.purgeCalls++
	return nil
}

type tenantProvisioningMemberService struct {
	interfaces.TenantMemberService
	ensureErr   error
	lists       [][]*types.TenantMember
	listCalls   int
	removeCalls int
}

func (s *tenantProvisioningMemberService) EnsureOwner(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
	if s.ensureErr != nil {
		return nil, s.ensureErr
	}
	return &types.TenantMember{UserID: userID, TenantID: tenantID, Role: types.TenantRoleOwner}, nil
}

func (s *tenantProvisioningMemberService) ListByUser(context.Context, string) ([]*types.TenantMember, error) {
	index := s.listCalls
	s.listCalls++
	if index >= len(s.lists) {
		return nil, nil
	}
	return s.lists[index], nil
}

func (s *tenantProvisioningMemberService) RemoveMember(context.Context, string, uint64) error {
	s.removeCalls++
	return nil
}

func runSelfServiceTenantCreate(t *testing.T, h *TenantHandler) *httptest.ResponseRecorder {
	t.Helper()
	r := gin.New()
	r.Use(tenantPolicyErrorCapture())
	r.POST("/tenants", h.CreateTenant)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tenants", bytes.NewBufferString(`{"name":"provisioned"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestCreateTenantRejectsRegularUserWhenSelfServiceDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tenants := &tenantPolicyTenantService{}
	h := &TenantHandler{
		service:          tenants,
		userService:      &tenantPolicyUserService{user: &types.User{ID: "regular-user"}},
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: false},
	}
	r := gin.New()
	r.Use(tenantPolicyErrorCapture())
	r.POST("/tenants", h.CreateTenant)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tenants", bytes.NewBufferString(`{"name":"blocked"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if tenants.createCalls != 0 {
		t.Fatalf("CreateTenant called %d times, want 0", tenants.createCalls)
	}
	if !strings.Contains(w.Body.String(), `"code":2005`) {
		t.Fatalf("response missing typed disabled code: %s", w.Body.String())
	}
}

func TestCreateTenantAllowsCrossTenantSuperuserWhenSelfServiceDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tenants := &tenantPolicyTenantService{}
	h := &TenantHandler{
		service: tenants,
		userService: &tenantPolicyUserService{user: &types.User{
			ID:                  "super-user",
			TenantID:            1,
			CanAccessAllTenants: true,
		}},
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: false},
	}
	r := gin.New()
	r.Use(errorCapture())
	r.POST("/tenants", h.CreateTenant)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tenants", bytes.NewBufferString(`{"name":"admin-created"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if tenants.createCalls != 1 {
		t.Fatalf("CreateTenant called %d times, want 1", tenants.createCalls)
	}
}

func TestAuthMeProjectsTenantCreationCapability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AuthHandler{
		userService: &tenantPolicyUserService{user: &types.User{
			ID:       "tenantless-user",
			Username: "tenantless",
			Email:    "tenantless@example.com",
		}},
		configInfo:       &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: false},
	}
	r := gin.New()
	r.GET("/auth/me", h.GetCurrentUser)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/me", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"can_create_tenant":false`) {
		t.Fatalf("response missing capability: %s", w.Body.String())
	}
}

func TestCreateTenantPurgesProvisioningWhenOwnerBootstrapFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tenants := &tenantPolicyTenantService{}
	members := &tenantProvisioningMemberService{
		ensureErr: errors.New("owner failed"), lists: [][]*types.TenantMember{{}},
	}
	h := &TenantHandler{
		service: tenants, memberService: members,
		userService:      &tenantPolicyUserService{user: &types.User{ID: "regular", TenantID: 1}},
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: true},
	}

	w := runSelfServiceTenantCreate(t, h)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if tenants.purgeCalls != 1 || tenants.deleteCalls != 0 {
		t.Fatalf("rollback calls: purge=%d delete=%d, want 1/0", tenants.purgeCalls, tenants.deleteCalls)
	}
}

func TestCreateTenantPurgesProvisioningWhenPostCreateQuotaFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tenants := &tenantPolicyTenantService{}
	members := &tenantProvisioningMemberService{lists: [][]*types.TenantMember{
		{},
		{{UserID: "regular", TenantID: 98, Role: types.TenantRoleOwner}, {UserID: "regular", TenantID: 99, Role: types.TenantRoleOwner}},
	}}
	h := &TenantHandler{
		service: tenants, memberService: members,
		userService:      &tenantPolicyUserService{user: &types.User{ID: "regular", TenantID: 1}},
		config:           &config.Config{Tenant: &config.TenantConfig{MaxOwnedPerUser: 1}},
		systemSettingSvc: &tenantPolicySettingService{enabled: true},
	}

	w := runSelfServiceTenantCreate(t, h)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if members.removeCalls != 1 {
		t.Fatalf("RemoveMember calls = %d, want 1", members.removeCalls)
	}
	if tenants.purgeCalls != 1 || tenants.deleteCalls != 0 {
		t.Fatalf("rollback calls: purge=%d delete=%d, want 1/0", tenants.purgeCalls, tenants.deleteCalls)
	}
}

func TestCreateTenantPurgesProvisioningWhenDefaultTenantUpdateFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tenants := &tenantPolicyTenantService{}
	members := &tenantProvisioningMemberService{lists: [][]*types.TenantMember{{}, {{Role: types.TenantRoleOwner}}}}
	h := &TenantHandler{
		service: tenants, memberService: members,
		userService: &tenantPolicyUserService{
			user: &types.User{ID: "tenantless", TenantID: 0}, updateErr: errors.New("update failed"),
		},
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: true},
	}

	w := runSelfServiceTenantCreate(t, h)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if members.removeCalls != 1 {
		t.Fatalf("RemoveMember calls = %d, want 1", members.removeCalls)
	}
	if tenants.purgeCalls != 1 || tenants.deleteCalls != 0 {
		t.Fatalf("rollback calls: purge=%d delete=%d, want 1/0", tenants.purgeCalls, tenants.deleteCalls)
	}
}
