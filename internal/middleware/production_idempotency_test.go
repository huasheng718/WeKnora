package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type productionIdempotencyRepoStub struct {
	mu                  sync.Mutex
	txMu                sync.Mutex
	records             map[string]*types.ProductionIdempotencyKey
	normalizeOnComplete bool
	releaseCalls        int
	transactionErr      error
}

func newProductionIdempotencyRepoStub() *productionIdempotencyRepoStub {
	return &productionIdempotencyRepoStub{records: make(map[string]*types.ProductionIdempotencyKey)}
}

func productionIdempotencyRecordKey(record *types.ProductionIdempotencyKey) string {
	return strings.Join([]string{
		strconv.FormatUint(record.TenantID, 10), record.ActorUserID, record.Route, record.IdempotencyKey,
	}, "\x00")
}

func (r *productionIdempotencyRepoStub) Reserve(
	_ context.Context,
	record *types.ProductionIdempotencyKey,
) (*types.ProductionIdempotencyKey, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := productionIdempotencyRecordKey(record)
	if existing, ok := r.records[key]; ok {
		copy := *existing
		copy.ResponseBody = append(types.JSON(nil), existing.ResponseBody...)
		return &copy, false, nil
	}
	copy := *record
	r.records[key] = &copy
	return record, true, nil
}

func (r *productionIdempotencyRepoStub) Complete(
	ctx context.Context,
	id string,
	statusCode int,
	responseBody types.JSON,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tenantID, _ := types.TenantIDFromContext(ctx)
	for _, record := range r.records {
		if record.ID == id && record.TenantID == tenantID {
			status := statusCode
			record.StatusCode = &status
			stored := append(types.JSON(nil), responseBody...)
			if r.normalizeOnComplete {
				var value any
				if json.Unmarshal(stored, &value) == nil {
					stored, _ = json.MarshalIndent(value, "", "  ")
				}
			}
			record.ResponseBody = stored
			return nil
		}
	}
	return nil
}

func (r *productionIdempotencyRepoStub) Release(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseCalls++
	tenantID, _ := types.TenantIDFromContext(ctx)
	for key, record := range r.records {
		if record.ID == id && record.TenantID == tenantID && record.StatusCode == nil {
			delete(r.records, key)
		}
	}
	return nil
}

func (r *productionIdempotencyRepoStub) WithinTransaction(
	ctx context.Context,
	fn func(context.Context) error,
) error {
	r.txMu.Lock()
	defer r.txMu.Unlock()

	r.mu.Lock()
	snapshot := make(map[string]*types.ProductionIdempotencyKey, len(r.records))
	for key, record := range r.records {
		copy := *record
		copy.ResponseBody = append(types.JSON(nil), record.ResponseBody...)
		snapshot[key] = &copy
	}
	r.mu.Unlock()
	committed := false
	defer func() {
		if committed {
			return
		}
		r.mu.Lock()
		r.records = snapshot
		r.mu.Unlock()
	}()

	err := fn(ctx)
	if err == nil {
		err = r.transactionErr
	}
	if err == nil {
		committed = true
	}
	return err
}

type productionTransactionMarker struct{}

type barrierProductionIdempotencyRepo struct {
	*productionIdempotencyRepoStub
	attempts chan struct{}
}

func (r *barrierProductionIdempotencyRepo) WithinTransaction(
	ctx context.Context,
	fn func(context.Context) error,
) error {
	r.attempts <- struct{}{}
	return r.productionIdempotencyRepoStub.WithinTransaction(
		context.WithValue(ctx, productionTransactionMarker{}, true), fn,
	)
}

func (r *barrierProductionIdempotencyRepo) Reserve(
	ctx context.Context,
	record *types.ProductionIdempotencyKey,
) (*types.ProductionIdempotencyKey, bool, error) {
	if active, _ := ctx.Value(productionTransactionMarker{}).(bool); !active {
		return nil, false, errors.New("reservation executed outside transaction")
	}
	return r.productionIdempotencyRepoStub.Reserve(ctx, record)
}

type completionFailingProductionIdempotencyRepo struct {
	interfaces.ProductionIdempotencyRepository
	mu       sync.Mutex
	failNext bool
}

func (r *completionFailingProductionIdempotencyRepo) Complete(
	ctx context.Context,
	id string,
	statusCode int,
	responseBody types.JSON,
) error {
	r.mu.Lock()
	if r.failNext {
		r.failNext = false
		r.mu.Unlock()
		return errors.New("forced idempotency completion failure")
	}
	r.mu.Unlock()
	return r.ProductionIdempotencyRepository.Complete(ctx, id, statusCode, responseBody)
}

func (r *completionFailingProductionIdempotencyRepo) WithinTransaction(
	ctx context.Context,
	fn func(context.Context) error,
) error {
	txRepo, ok := r.ProductionIdempotencyRepository.(interface {
		WithinTransaction(context.Context, func(context.Context) error) error
	})
	if !ok {
		return errors.New("idempotency repository has no transaction boundary")
	}
	return txRepo.WithinTransaction(ctx, fn)
}

func newProductionIdempotencyTestEngine(
	repo interfaces.ProductionIdempotencyRepository,
	handler gin.HandlerFunc,
	outer ...gin.HandlerFunc,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(outer...)
	engine.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "author-1")
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	engine.POST("/production/projects", NewProductionIdempotencyMiddleware(repo).Require(), handler)
	return engine
}

func performProductionIdempotencyRequest(
	engine *gin.Engine,
	key, body string,
) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/production/projects", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	engine.ServeHTTP(recorder, request)
	return recorder
}

func TestProductionIdempotencyRequiresKey(t *testing.T) {
	engine := newProductionIdempotencyTestEngine(newProductionIdempotencyRepoStub(), func(c *gin.Context) {
		c.JSON(http.StatusCreated, gin.H{"success": true})
	})

	response := performProductionIdempotencyRequest(engine, "", `{"name":"Baseline"}`)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.JSONEq(t, `{"success":false,"error":{"code":"PRODUCTION_IDEMPOTENCY_KEY_REQUIRED","message":"Idempotency-Key header is required"}}`, response.Body.String())
}

func TestProductionIdempotencyRejectsOversizedKeyBeforeReservation(t *testing.T) {
	repo := newProductionIdempotencyRepoStub()
	engine := newProductionIdempotencyTestEngine(repo, func(c *gin.Context) {
		c.JSON(http.StatusCreated, gin.H{"success": true})
	})

	response := performProductionIdempotencyRequest(engine, strings.Repeat("k", 256), `{"name":"Baseline"}`)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "PRODUCTION_IDEMPOTENCY_KEY_INVALID")
	require.Empty(t, repo.records)
}

func TestProductionIdempotencyRejectsOversizedBodyBeforeReservation(t *testing.T) {
	repo := newProductionIdempotencyRepoStub()
	engine := newProductionIdempotencyTestEngine(repo, func(c *gin.Context) {
		c.JSON(http.StatusCreated, gin.H{"success": true})
	})

	response := performProductionIdempotencyRequest(
		engine, "request-1", strings.Repeat("x", productionFoundationMaxBodyBytes+1),
	)

	require.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
	require.Contains(t, response.Body.String(), "PRODUCTION_REQUEST_BODY_TOO_LARGE")
	require.Empty(t, repo.records)
}

func TestProductionIdempotencyReplaysCanonicalJSONWithStatusAndBody(t *testing.T) {
	repo := newProductionIdempotencyRepoStub()
	calls := 0
	engine := newProductionIdempotencyTestEngine(repo, func(c *gin.Context) {
		calls++
		c.Header("Content-Type", "application/json; charset=utf-8")
		c.Status(http.StatusCreated)
		_, _ = c.Writer.Write([]byte(`{"success":true,`))
		_, _ = c.Writer.Write([]byte(`"data":{"id":"project-1"}}`))
	})

	first := performProductionIdempotencyRequest(engine, "request-1", `{ "description": "", "name": "Baseline" }`)
	replay := performProductionIdempotencyRequest(engine, "request-1", `{"name":"Baseline","description":""}`)

	require.Equal(t, 1, calls)
	require.Equal(t, http.StatusCreated, first.Code)
	require.Equal(t, first.Code, replay.Code)
	require.Equal(t, first.Body.String(), replay.Body.String())
	require.Equal(t, first.Header().Get("Content-Type"), replay.Header().Get("Content-Type"))
}

func TestProductionIdempotencyReplaySurvivesJSONBNormalization(t *testing.T) {
	repo := newProductionIdempotencyRepoStub()
	repo.normalizeOnComplete = true
	engine := newProductionIdempotencyTestEngine(repo, func(c *gin.Context) {
		c.JSON(http.StatusCreated, gin.H{"success": true, "data": gin.H{"id": "project-1"}})
	})

	first := performProductionIdempotencyRequest(engine, "request-1", `{"name":"Baseline"}`)
	replay := performProductionIdempotencyRequest(engine, "request-1", `{"name":"Baseline"}`)

	require.Equal(t, http.StatusCreated, first.Code)
	require.Equal(t, first.Body.String(), replay.Body.String())
}

func TestProductionIdempotencyRejectsKeyReusedWithDifferentBody(t *testing.T) {
	engine := newProductionIdempotencyTestEngine(newProductionIdempotencyRepoStub(), func(c *gin.Context) {
		c.JSON(http.StatusCreated, gin.H{"success": true})
	})
	performProductionIdempotencyRequest(engine, "request-1", `{"name":"Baseline"}`)

	response := performProductionIdempotencyRequest(engine, "request-1", `{"name":"Retro"}`)

	require.Equal(t, http.StatusConflict, response.Code)
	require.Contains(t, response.Body.String(), "PRODUCTION_IDEMPOTENCY_KEY_CONFLICT")
}

func TestProductionIdempotencyIncludesNormalizedIfMatchInRequestIdentity(t *testing.T) {
	repo := newProductionIdempotencyRepoStub()
	calls := 0
	engine := newProductionIdempotencyTestEngine(repo, func(c *gin.Context) {
		calls++
		c.JSON(http.StatusCreated, gin.H{"success": true, "version": c.GetHeader("If-Match")})
	})
	perform := func(ifMatch string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/production/projects", strings.NewReader(`{"name":"Baseline"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "request-1")
		request.Header.Set("If-Match", ifMatch)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		return response
	}

	first := perform(" 11111111-1111-4111-8111-111111111111 ")
	replay := perform("11111111-1111-4111-8111-111111111111")
	conflict := perform("22222222-2222-4222-8222-222222222222")

	require.Equal(t, http.StatusCreated, first.Code)
	require.Equal(t, first.Body.String(), replay.Body.String())
	require.Equal(t, http.StatusConflict, conflict.Code)
	require.Contains(t, conflict.Body.String(), productionIdempotencyKeyConflict)
	require.Equal(t, 1, calls)
}

func TestProductionIdempotencyRejectsInProgressDuplicateAsRetryable(t *testing.T) {
	repo := newProductionIdempotencyRepoStub()
	body := `{"name":"Baseline"}`
	_, created, err := repo.Reserve(context.Background(), &types.ProductionIdempotencyKey{
		ID:             "live-reservation",
		TenantID:       7,
		ActorUserID:    "author-1",
		Route:          "POST /production/projects",
		IdempotencyKey: "request-1",
		RequestDigest:  productionRequestDigest([]byte(body)),
	})
	require.NoError(t, err)
	require.True(t, created)
	calls := 0
	engine := newProductionIdempotencyTestEngine(repo, func(c *gin.Context) {
		calls++
		c.Status(http.StatusInternalServerError)
	})

	response := performProductionIdempotencyRequest(engine, "request-1", body)

	require.Equal(t, http.StatusConflict, response.Code)
	require.JSONEq(t, `{"success":false,"error":{"code":"PRODUCTION_IDEMPOTENCY_IN_PROGRESS","message":"request with this idempotency key is still in progress","retryable":true}}`, response.Body.String())
	require.Zero(t, calls)
}

func TestProductionIdempotencyReleasesTerminalResponseForImmediateRetry(t *testing.T) {
	repo := newProductionIdempotencyRepoStub()
	calls := 0
	engine := newProductionIdempotencyTestEngine(repo, func(c *gin.Context) {
		calls++
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false})
	})

	first := performProductionIdempotencyRequest(engine, "request-1", `{"name":"Baseline"}`)
	second := performProductionIdempotencyRequest(engine, "request-1", `{"name":"Baseline"}`)

	require.Equal(t, http.StatusUnprocessableEntity, first.Code)
	require.Equal(t, http.StatusUnprocessableEntity, second.Code)
	require.Equal(t, 2, calls)
	require.Equal(t, 2, repo.releaseCalls)
}

func TestProductionIdempotencyLeavesUnwrittenErrorsForOuterErrorHandler(t *testing.T) {
	engine := newProductionIdempotencyTestEngine(newProductionIdempotencyRepoStub(), func(c *gin.Context) {
		c.Error(apperrors.NewValidationError("invalid request"))
	}, ErrorHandler())

	response := performProductionIdempotencyRequest(engine, "request-1", `{"name":"Baseline"}`)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), `"message":"invalid request"`)
}

func TestProductionIdempotencyRestoresWriterBeforeOuterRecovery(t *testing.T) {
	repo := newProductionIdempotencyRepoStub()
	calls := 0
	engine := newProductionIdempotencyTestEngine(repo, func(*gin.Context) {
		calls++
		panic("write failed")
	}, Recovery())

	first := performProductionIdempotencyRequest(engine, "request-1", `{"name":"Baseline"}`)
	second := performProductionIdempotencyRequest(engine, "request-1", `{"name":"Baseline"}`)

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusInternalServerError, second.Code)
	require.Contains(t, first.Body.String(), "Internal Server Error")
	require.Equal(t, 2, calls)
	require.Empty(t, repo.records)
}

func TestProductionIdempotencyScopesReservationToConcreteResourcePath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newProductionIdempotencyRepoStub()
	calls := 0
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "author-1")
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	engine.DELETE("/production/projects/:id", NewProductionIdempotencyMiddleware(repo).Require(), func(c *gin.Context) {
		calls++
		c.JSON(http.StatusOK, gin.H{"deleted": c.Param("id")})
	})
	perform := func(path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodDelete, path, nil)
		request.Header.Set("Idempotency-Key", "request-1")
		engine.ServeHTTP(recorder, request)
		return recorder
	}

	first := perform("/production/projects/project-1")
	second := perform("/production/projects/project-2")

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, 2, calls)
	require.Contains(t, second.Body.String(), "project-2")
}

func TestProductionIdempotencyRollsBackBusinessMutationWhenCompletionFails(t *testing.T) {
	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared&_foreign_keys=1&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	migration, err := os.ReadFile("../../migrations/sqlite/000001_knowledge_production_foundation.up.sql")
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)

	projectRepo := apprepository.NewProductionProjectRepository(db)
	idempotencyRepo := &completionFailingProductionIdempotencyRepo{
		ProductionIdempotencyRepository: apprepository.NewProductionIdempotencyRepository(db),
		failNext:                        true,
	}
	projectCalls := 0
	engine := newProductionIdempotencyTestEngine(idempotencyRepo, func(c *gin.Context) {
		projectCalls++
		projectID := fmt.Sprintf("project-%d", projectCalls)
		err := projectRepo.Create(c.Request.Context(), &types.ProductionProject{
			ID: projectID, TenantID: 7, Name: "Foundation", OwnerUserID: "author-1",
			Status: types.ProductionProjectActive,
		}, &types.ProductionProjectMember{AssignedBy: "author-1"})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"success": true, "data": gin.H{"id": projectID}})
	})

	failed := performProductionIdempotencyRequest(engine, "request-1", `{"name":"Foundation"}`)
	require.Equal(t, http.StatusInternalServerError, failed.Code)
	var projectCount int64
	require.NoError(t, db.Model(&types.ProductionProject{}).Count(&projectCount).Error)
	require.Zero(t, projectCount, "completion failure must roll back the business mutation")

	retry := performProductionIdempotencyRequest(engine, "request-1", `{"name":"Foundation"}`)
	require.Equal(t, http.StatusCreated, retry.Code)
	require.NoError(t, db.Model(&types.ProductionProject{}).Count(&projectCount).Error)
	require.Equal(t, int64(1), projectCount)
	require.Equal(t, 2, projectCalls)
}

func TestProductionIdempotencyDoesNotFlushSuccessWhenTransactionCommitFails(t *testing.T) {
	repo := newProductionIdempotencyRepoStub()
	repo.transactionErr = errors.New("forced commit failure")
	engine := newProductionIdempotencyTestEngine(repo, func(c *gin.Context) {
		c.Header("X-Production-Project", "project-1")
		c.JSON(http.StatusCreated, gin.H{"success": true})
	})

	response := performProductionIdempotencyRequest(engine, "request-1", `{"name":"Foundation"}`)

	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.Contains(t, response.Body.String(), productionIdempotencyUnavailable)
	require.NotContains(t, response.Body.String(), `"success":true`)
	require.Empty(t, response.Header().Get("X-Production-Project"))
	require.Empty(t, repo.records)
}

func TestProductionIdempotencyConcurrentRequestsExecuteHandlerOnce(t *testing.T) {
	repo := &barrierProductionIdempotencyRepo{
		productionIdempotencyRepoStub: newProductionIdempotencyRepoStub(),
		attempts:                      make(chan struct{}, 2),
	}
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	var calls int
	engine := newProductionIdempotencyTestEngine(repo, func(c *gin.Context) {
		calls++
		close(handlerStarted)
		<-releaseHandler
		c.JSON(http.StatusCreated, gin.H{"success": true, "data": gin.H{"id": "project-1"}})
	})

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- performProductionIdempotencyRequest(engine, "request-1", `{"name":"Foundation"}`)
	}()
	select {
	case <-handlerStarted:
	case response := <-firstDone:
		t.Fatalf("first request completed before handler barrier: status=%d body=%s", response.Code, response.Body.String())
	}
	<-repo.attempts

	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		secondDone <- performProductionIdempotencyRequest(engine, "request-1", `{"name":"Foundation"}`)
	}()
	<-repo.attempts
	close(releaseHandler)

	first := <-firstDone
	second := <-secondDone
	require.Equal(t, 1, calls)
	require.Equal(t, http.StatusCreated, first.Code)
	require.Equal(t, first.Code, second.Code)
	require.Equal(t, first.Body.String(), second.Body.String())
}
