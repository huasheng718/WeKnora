package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type productionIdempotencyRepoStub struct {
	mu                  sync.Mutex
	records             map[string]*types.ProductionIdempotencyKey
	normalizeOnComplete bool
	releaseCalls        int
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

func newProductionIdempotencyTestEngine(
	repo *productionIdempotencyRepoStub,
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
	require.Equal(t, 2, repo.releaseCalls)
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
