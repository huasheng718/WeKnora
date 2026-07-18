package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	productionIdempotencyKeyRequired = "PRODUCTION_IDEMPOTENCY_KEY_REQUIRED"
	productionIdempotencyKeyInvalid  = "PRODUCTION_IDEMPOTENCY_KEY_INVALID"
	productionIdempotencyKeyConflict = "PRODUCTION_IDEMPOTENCY_KEY_CONFLICT"
	productionIdempotencyInProgress  = "PRODUCTION_IDEMPOTENCY_IN_PROGRESS"
	productionIdempotencyUnavailable = "PRODUCTION_IDEMPOTENCY_UNAVAILABLE"
	productionRequestBodyTooLarge    = "PRODUCTION_REQUEST_BODY_TOO_LARGE"

	productionIdempotencyMaxKeyBytes = 255
	// productionFoundationMaxBodyBytes is intentionally smaller than upload
	// limits: foundation commands are JSON metadata/schema definitions.
	productionFoundationMaxBodyBytes = 1 << 20 // 1 MiB
)

var errProductionRequestBodyTooLarge = errors.New("production request body exceeds limit")

// ProductionIdempotencyMiddleware provides durable replay semantics for all
// production write routes.
type ProductionIdempotencyMiddleware struct {
	repo interfaces.ProductionIdempotencyRepository
}

// NewProductionIdempotencyMiddleware constructs the shared write middleware.
func NewProductionIdempotencyMiddleware(
	repo interfaces.ProductionIdempotencyRepository,
) *ProductionIdempotencyMiddleware {
	return &ProductionIdempotencyMiddleware{repo: repo}
}

// Require reserves an Idempotency-Key before executing the write and stores
// the exact successful JSON status/body before releasing it to the client.
func (m *ProductionIdempotencyMiddleware) Require() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if key == "" {
			abortProductionIdempotency(c, http.StatusBadRequest, productionIdempotencyKeyRequired,
				"Idempotency-Key header is required", false)
			return
		}
		if len(key) > productionIdempotencyMaxKeyBytes {
			abortProductionIdempotency(c, http.StatusBadRequest, productionIdempotencyKeyInvalid,
				"Idempotency-Key must be between 1 and 255 bytes", false)
			return
		}

		ctx := c.Request.Context()
		tenantID, tenantOK := types.TenantIDFromContext(ctx)
		actorUserID, actorOK := types.UserIDFromContext(ctx)
		if !tenantOK || tenantID == 0 || !actorOK || m == nil || m.repo == nil {
			abortProductionIdempotency(c, http.StatusInternalServerError, productionIdempotencyUnavailable,
				"idempotency context is unavailable", false)
			return
		}

		body, err := readAndRestoreProductionBody(c)
		if err != nil {
			if errors.Is(err, errProductionRequestBodyTooLarge) {
				abortProductionIdempotency(c, http.StatusRequestEntityTooLarge, productionRequestBodyTooLarge,
					"production request body exceeds 1 MiB limit", false)
				return
			}
			abortProductionIdempotency(c, http.StatusBadRequest, productionIdempotencyKeyRequired,
				"request body could not be read", false)
			return
		}
		digest := productionRequestDigest(body)
		route := c.Request.Method + " " + c.Request.URL.EscapedPath()
		reservation := &types.ProductionIdempotencyKey{
			ID:             uuid.NewString(),
			TenantID:       tenantID,
			ActorUserID:    actorUserID,
			Route:          route,
			IdempotencyKey: key,
			RequestDigest:  digest,
		}
		reserved, created, err := m.repo.Reserve(ctx, reservation)
		if err != nil {
			logger.Errorf(ctx, "production idempotency reservation failed: %v", err)
			abortProductionIdempotency(c, http.StatusInternalServerError, productionIdempotencyUnavailable,
				"idempotency reservation failed", true)
			return
		}
		if !created {
			m.replayOrReject(c, reserved, digest)
			return
		}
		if reserved == nil {
			abortProductionIdempotency(c, http.StatusInternalServerError, productionIdempotencyUnavailable,
				"idempotency reservation is unavailable", true)
			return
		}
		reservationID := reserved.ID

		original := c.Writer
		capture := newProductionResponseCapture(original)
		func() {
			c.Writer = capture
			defer func() {
				c.Writer = original
				if recovered := recover(); recovered != nil {
					m.releaseReservation(ctx, reservationID)
					panic(recovered)
				}
			}()
			c.Next()
		}()
		if !capture.hasResponse() {
			m.releaseReservation(ctx, reservationID)
			return
		}

		if isSuccessfulJSONResponse(capture) {
			responseBody := capture.body.Bytes()
			if canonical, ok := canonicalProductionJSON(responseBody); ok {
				capture.body.Reset()
				_, _ = capture.body.Write(canonical)
				capture.size = len(canonical)
				responseBody = canonical
			}
			if err := m.repo.Complete(ctx, reservationID, capture.Status(), types.JSON(responseBody)); err != nil {
				logger.Errorf(ctx, "production idempotency completion failed: %v", err)
				m.releaseReservation(ctx, reservationID)
				writeProductionIdempotencyResponse(original, http.StatusInternalServerError,
					productionIdempotencyUnavailable, "idempotency completion failed", true)
				return
			}
			capture.flushTo(original)
			return
		}
		m.releaseReservation(ctx, reservationID)
		capture.flushTo(original)
	}
}

func (m *ProductionIdempotencyMiddleware) releaseReservation(ctx context.Context, id string) {
	if err := m.repo.Release(ctx, id); err != nil {
		logger.Errorf(ctx, "production idempotency release failed: %v", err)
	}
}

func (m *ProductionIdempotencyMiddleware) replayOrReject(
	c *gin.Context,
	existing *types.ProductionIdempotencyKey,
	digest string,
) {
	if existing == nil {
		abortProductionIdempotency(c, http.StatusInternalServerError, productionIdempotencyUnavailable,
			"idempotency reservation is unavailable", true)
		return
	}
	if existing.RequestDigest != digest {
		abortProductionIdempotency(c, http.StatusConflict, productionIdempotencyKeyConflict,
			"Idempotency-Key was already used with a different request body", false)
		return
	}
	if existing.StatusCode == nil {
		abortProductionIdempotency(c, http.StatusConflict, productionIdempotencyInProgress,
			"request with this idempotency key is still in progress", true)
		return
	}
	replayBody := []byte(existing.ResponseBody)
	if canonical, ok := canonicalProductionJSON(replayBody); ok {
		replayBody = canonical
	}
	c.Data(*existing.StatusCode, "application/json; charset=utf-8", replayBody)
	c.Abort()
}

func readAndRestoreProductionBody(c *gin.Context) ([]byte, error) {
	if c.Request.Body == nil {
		return nil, nil
	}
	if c.Request.ContentLength > productionFoundationMaxBodyBytes {
		return nil, errProductionRequestBodyTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, productionFoundationMaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > productionFoundationMaxBodyBytes {
		return nil, errProductionRequestBodyTooLarge
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func productionRequestDigest(body []byte) string {
	canonical := body
	if encoded, ok := canonicalProductionJSON(body); ok {
		canonical = encoded
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func canonicalProductionJSON(body []byte) ([]byte, bool) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, false
	}
	encoded, err := json.Marshal(value)
	return encoded, err == nil
}

func abortProductionIdempotency(
	c *gin.Context,
	status int,
	code, message string,
	retryable bool,
) {
	errorBody := gin.H{"code": code, "message": message}
	if retryable {
		errorBody["retryable"] = true
	}
	c.AbortWithStatusJSON(status, gin.H{"success": false, "error": errorBody})
}

func writeProductionIdempotencyResponse(
	w gin.ResponseWriter,
	status int,
	code, message string,
	retryable bool,
) {
	errorBody := map[string]any{"code": code, "message": message}
	if retryable {
		errorBody["retryable"] = true
	}
	body, _ := json.Marshal(map[string]any{"success": false, "error": errorBody})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

type productionResponseCapture struct {
	gin.ResponseWriter
	body      bytes.Buffer
	status    int
	size      int
	statusSet bool
}

func newProductionResponseCapture(w gin.ResponseWriter) *productionResponseCapture {
	return &productionResponseCapture{ResponseWriter: w, status: http.StatusOK, size: -1}
}

func (w *productionResponseCapture) WriteHeader(code int) {
	if code > 0 && !w.Written() {
		w.status = code
		w.statusSet = true
	}
}

func (w *productionResponseCapture) WriteHeaderNow() {
	if !w.Written() {
		w.size = 0
		w.statusSet = true
	}
}

func (w *productionResponseCapture) Write(data []byte) (int, error) {
	w.WriteHeaderNow()
	n, err := w.body.Write(data)
	w.size += n
	return n, err
}

func (w *productionResponseCapture) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func (w *productionResponseCapture) Status() int { return w.status }
func (w *productionResponseCapture) Size() int   { return w.size }
func (w *productionResponseCapture) Written() bool {
	return w.size != -1
}

func (w *productionResponseCapture) Flush() {
	w.WriteHeaderNow()
}

func (w *productionResponseCapture) hasResponse() bool {
	return w.statusSet || w.Written()
}

func (w *productionResponseCapture) flushTo(destination gin.ResponseWriter) {
	destination.WriteHeader(w.status)
	if w.body.Len() > 0 {
		_, _ = destination.Write(w.body.Bytes())
	} else {
		destination.WriteHeaderNow()
	}
}

func isSuccessfulJSONResponse(w *productionResponseCapture) bool {
	if w.Status() < http.StatusOK || w.Status() >= http.StatusMultipleChoices {
		return false
	}
	if w.Status() == http.StatusNoContent {
		return w.body.Len() == 0
	}
	return strings.Contains(w.Header().Get("Content-Type"), "application/json") && json.Valid(w.body.Bytes())
}

var _ gin.ResponseWriter = (*productionResponseCapture)(nil)
