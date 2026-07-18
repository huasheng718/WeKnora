package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	appservice "github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type productionCompletionFailingRepo struct {
	interfaces.ProductionIdempotencyRepository
	mu            sync.Mutex
	failNext      bool
	completeCalls int
}

func (r *productionCompletionFailingRepo) Complete(
	ctx context.Context,
	id string,
	statusCode int,
	responseBody types.JSON,
) error {
	r.mu.Lock()
	r.completeCalls++
	if r.failNext {
		r.failNext = false
		r.mu.Unlock()
		return errors.New("forced idempotency completion failure")
	}
	r.mu.Unlock()
	return r.ProductionIdempotencyRepository.Complete(ctx, id, statusCode, responseBody)
}

func newConcreteProductionProjectHTTPFixture(
	t *testing.T,
	wrap func(interfaces.ProductionIdempotencyRepository) interfaces.ProductionIdempotencyRepository,
) (*gorm.DB, *gin.Engine) {
	t.Helper()
	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared&_foreign_keys=1&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	migration, err := os.ReadFile("../../migrations/sqlite/000001_knowledge_production_foundation.up.sql")
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)
	require.NoError(t, db.AutoMigrate(&types.TenantMember{}, &types.AuditLog{}))
	require.NoError(t, db.Create(&types.TenantMember{
		UserID: "author-1", TenantID: 7, Role: types.TenantRoleContributor,
		Status: types.TenantMemberStatusActive, JoinedAt: time.Now(),
	}).Error)
	sqlDB.SetMaxOpenConns(1)

	members := appservice.NewTenantMemberService(apprepository.NewTenantMemberRepository(db), nil)
	audit := appservice.NewAuditLogService(apprepository.NewAuditLogRepository(db))
	projects := appservice.NewProductionProjectService(
		apprepository.NewProductionProjectRepository(db), members, audit,
	)
	projectHandler := NewProductionProjectHandler(projects)
	idempotencyRepo := interfaces.ProductionIdempotencyRepository(
		apprepository.NewProductionIdempotencyRepository(db),
	)
	if wrap != nil {
		idempotencyRepo = wrap(idempotencyRepo)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "author-1")
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	engine.POST(
		"/production/projects",
		middleware.NewProductionIdempotencyMiddleware(idempotencyRepo).Require(),
		projectHandler.Create,
	)
	return db, engine
}

func performConcreteProductionProjectRequest(
	t *testing.T,
	engine *gin.Engine,
	key string,
) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost, "/production/projects", strings.NewReader(`{"name":"Foundation"}`),
	).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	engine.ServeHTTP(recorder, request)
	return recorder
}

func TestProductionProjectCreateCompletesWithSingleSQLiteConnectionAndReplays(t *testing.T) {
	db, engine := newConcreteProductionProjectHTTPFixture(t, nil)

	first := performConcreteProductionProjectRequest(t, engine, "request-1")
	replay := performConcreteProductionProjectRequest(t, engine, "request-1")

	require.Equal(t, http.StatusCreated, first.Code)
	require.Equal(t, first.Code, replay.Code)
	require.Equal(t, first.Body.String(), replay.Body.String())
	for model, expected := range map[any]int64{
		&types.ProductionProject{}:        1,
		&types.ProductionProjectMember{}:  1,
		&types.AuditLog{}:                 1,
		&types.ProductionIdempotencyKey{}: 1,
	} {
		var count int64
		require.NoError(t, db.Model(model).Count(&count).Error)
		require.Equal(t, expected, count)
	}
	var idempotency types.ProductionIdempotencyKey
	require.NoError(t, db.First(&idempotency).Error)
	require.NotNil(t, idempotency.StatusCode)
	require.Equal(t, http.StatusCreated, *idempotency.StatusCode)
}

func TestProductionProjectCompletionFailureRollsBackBusinessAuditAndIdempotency(t *testing.T) {
	var failing *productionCompletionFailingRepo
	db, engine := newConcreteProductionProjectHTTPFixture(t, func(
		repo interfaces.ProductionIdempotencyRepository,
	) interfaces.ProductionIdempotencyRepository {
		failing = &productionCompletionFailingRepo{
			ProductionIdempotencyRepository: repo,
			failNext:                        true,
		}
		return failing
	})

	response := performConcreteProductionProjectRequest(t, engine, "request-1")

	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.Equal(t, 1, failing.completeCalls)
	for _, model := range []any{
		&types.ProductionProject{},
		&types.ProductionProjectMember{},
		&types.AuditLog{},
		&types.ProductionIdempotencyKey{},
	} {
		var count int64
		require.NoError(t, db.Model(model).Count(&count).Error)
		require.Zero(t, count)
	}
}
