package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type activationBarrierUserService struct {
	interfaces.UserService
	tenantID uint64
}

func (s *activationBarrierUserService) ValidateToken(_ context.Context, token string) (*types.User, uint64, error) {
	switch token {
	case "home":
		return &types.User{ID: "owner", TenantID: s.tenantID, IsActive: true}, 0, nil
	case "claim":
		return &types.User{ID: "owner", IsActive: true}, s.tenantID, nil
	case "header", "membership":
		return &types.User{ID: "owner", IsActive: true}, 0, nil
	default:
		return nil, 0, fmt.Errorf("invalid token")
	}
}

func (s *activationBarrierUserService) GetUserByTenantID(context.Context, uint64) (*types.User, error) {
	return &types.User{ID: "owner", TenantID: s.tenantID, IsActive: true}, nil
}

type activationBarrierAPIKeyService struct {
	interfaces.TenantAPIKeyService
	tenantID uint64
}

func (s *activationBarrierAPIKeyService) AuthenticateAPIKey(_ context.Context, token string) (*types.TenantAPIKey, error) {
	if token != "pending-key" {
		return nil, fmt.Errorf("invalid API key")
	}
	return &types.TenantAPIKey{ID: 1, TenantID: s.tenantID, FullAccess: true}, nil
}

func newActivationBarrierTenantService(t *testing.T) interfaces.TenantService {
	t.Helper()
	t.Setenv("STORAGE_TYPE", "local")
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.StorageBackend{}, &types.ProductionDocumentType{}))
	catalog, err := service.NewProductionBuiltinCatalog()
	require.NoError(t, err)
	return service.NewTenantService(
		repository.NewTenantRepository(db),
		repository.NewStorageBackendRepository(db),
		repository.NewProductionDocumentTypeRepository(db),
		repository.NewProductionUnitOfWork(db),
		catalog,
	)
}

func TestAuthRejectsProvisioningTenantUntilActivationCommits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	tenantService := newActivationBarrierTenantService(t)
	pending, err := tenantService.CreateTenant(ctx, &types.Tenant{Name: "activation-barrier"})
	require.NoError(t, err)
	require.Equal(t, types.TenantStatusProvisioning, pending.Status)

	members := newFakeMemberService()
	members.seedActive("owner", pending.ID, types.TenantRoleOwner)
	users := &activationBarrierUserService{tenantID: pending.ID}
	apiKeys := &activationBarrierAPIKeyService{tenantID: pending.ID}
	rbac := true
	cfg := &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbac}}

	var downstreamCalls atomic.Int32
	var rejectedTenantContexts atomic.Int32
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Next()
		if c.IsAborted() {
			if _, ok := c.Get(types.TenantIDContextKey.String()); ok {
				rejectedTenantContexts.Add(1)
			}
		}
	})
	router.Use(Auth(tenantService, users, members, apiKeys, cfg))
	router.GET("/api/v1/production/projects", func(c *gin.Context) {
		downstreamCalls.Add(1)
		if _, ok := types.TenantIDFromContext(c.Request.Context()); !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "tenant context missing"})
			return
		}
		c.Status(http.StatusOK)
	})

	type requestSpec struct {
		name    string
		headers map[string]string
	}
	requests := []requestSpec{
		{name: "JWT home", headers: map[string]string{"Authorization": "Bearer home"}},
		{name: "JWT claim", headers: map[string]string{"Authorization": "Bearer claim"}},
		{name: "X-Tenant-ID", headers: map[string]string{
			"Authorization": "Bearer header", "X-Tenant-ID": fmt.Sprint(pending.ID),
		}},
		{name: "tenantless first membership", headers: map[string]string{"Authorization": "Bearer membership"}},
		{name: "API key", headers: map[string]string{"X-API-Key": "pending-key"}},
	}

	releaseActivation := make(chan struct{})
	activationResult := make(chan error, 1)
	go func() {
		<-releaseActivation
		_, activateErr := tenantService.ActivateProvisionedTenant(ctx, pending.ID)
		activationResult <- activateErr
	}()

	for _, spec := range requests {
		t.Run("pending "+spec.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/production/projects", nil)
			for key, value := range spec.headers {
				request.Header.Set(key, value)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.NotEqual(t, http.StatusOK, response.Code, response.Body.String())
		})
	}
	require.Zero(t, downstreamCalls.Load())
	require.Zero(t, rejectedTenantContexts.Load())

	close(releaseActivation)
	require.NoError(t, <-activationResult)

	for _, spec := range requests {
		t.Run("active "+spec.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/production/projects", nil)
			for key, value := range spec.headers {
				request.Header.Set(key, value)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		})
	}
	require.Equal(t, int32(len(requests)), downstreamCalls.Load())
}
