package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestDeleteTenantRejectsProvisioningWithGenericConflict(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.TenantMember{}))
	tenant := &types.Tenant{Name: "pending", Status: types.TenantStatusProvisioning}
	require.NoError(t, db.Create(tenant).Error)
	require.NoError(t, db.Create(&types.TenantMember{
		UserID: "owner", TenantID: tenant.ID, Role: types.TenantRoleOwner,
		Status: types.TenantMemberStatusActive,
	}).Error)

	tenantRepo := repository.NewTenantRepository(db)
	tenantSvc := service.NewTenantService(tenantRepo, nil, nil, nil, nil)
	h := &TenantHandler{service: tenantSvc}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.DELETE("/tenants/:id", h.DeleteTenant)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/tenants/"+"1", nil)
	r.ServeHTTP(recorder, request.WithContext(context.Background()))

	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	require.NotContains(t, recorder.Body.String(), "provision")
	require.NotContains(t, recorder.Body.String(), "not active")
	for name, model := range map[string]any{
		"tenant":     &types.Tenant{},
		"membership": &types.TenantMember{},
	} {
		t.Run(name, func(t *testing.T) {
			var count int64
			require.NoError(t, db.Unscoped().Model(model).Count(&count).Error)
			require.Equal(t, int64(1), count)
		})
	}
}

type lifecycleWeKnoraCloudService struct {
	interfaces.WeKnoraCloudService
	err error
}

func (s *lifecycleWeKnoraCloudService) SaveCredentials(context.Context, string, string) error {
	return s.err
}

func TestWeKnoraCloudCredentialLifecycleConflictUsesGeneric409(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewWeKnoraCloudHandler(&lifecycleWeKnoraCloudService{
		err: apperrors.NewConflictError("Workspace cannot be modified in its current state"),
	})
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.POST("/weknoracloud/credentials", h.SaveCredentials)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/weknoracloud/credentials",
		strings.NewReader(`{"app_id":"id","app_secret":"secret"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	require.NotContains(t, recorder.Body.String(), "provision")
	require.NotContains(t, recorder.Body.String(), "not active")
}
