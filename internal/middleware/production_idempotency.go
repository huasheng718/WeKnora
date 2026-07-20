package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

var (
	errProductionRequestBodyTooLarge       = errors.New("production request body exceeds limit")
	errProductionTerminalResponse          = errors.New("production handler returned a terminal response")
	errProductionHandlerResponseMissing    = errors.New("production handler did not write a response")
	errProductionIdempotencyReserveFailed  = errors.New("production idempotency reservation failed")
	errProductionIdempotencyCompleteFailed = errors.New("production idempotency completion failed")
)

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
		digest := productionRequestDigest(body, c.GetHeader("If-Match"))
		ctx, runAfterCommit := types.WithProductionAfterCommit(ctx)
		route := c.Request.Method + " " + c.Request.URL.EscapedPath()
		reservation := &types.ProductionIdempotencyKey{
			ID:             uuid.NewString(),
			TenantID:       tenantID,
			ActorUserID:    actorUserID,
			Route:          route,
			IdempotencyKey: key,
			RequestDigest:  digest,
		}
		original := c.Writer
		capture := newProductionResponseCapture(original)
		var reserved *types.ProductionIdempotencyKey
		var created bool
		transactionErr := m.repo.WithinTransaction(ctx, func(txCtx context.Context) error {
			var err error
			reserved, created, err = m.repo.Reserve(txCtx, reservation)
			if err != nil {
				return fmt.Errorf("%w: %v", errProductionIdempotencyReserveFailed, err)
			}
			if !created {
				return nil
			}
			if reserved == nil {
				return fmt.Errorf("%w: reservation is unavailable", errProductionIdempotencyReserveFailed)
			}

			originalRequest := c.Request
			c.Writer = capture
			c.Request = originalRequest.WithContext(txCtx)
			defer func() {
				c.Writer = original
				c.Request = originalRequest
			}()
			c.Next()

			if len(c.Errors) > 0 {
				m.releaseReservation(txCtx, reserved.ID)
				return errProductionHandlerResponseMissing
			}
			if !capture.hasResponse() {
				m.releaseReservation(txCtx, reserved.ID)
				return errProductionHandlerResponseMissing
			}
			if !isSuccessfulJSONResponse(capture) {
				m.releaseReservation(txCtx, reserved.ID)
				return errProductionTerminalResponse
			}

			responseBody := capture.body.Bytes()
			if canonical, ok := canonicalProductionJSON(responseBody); ok {
				capture.body.Reset()
				_, _ = capture.body.Write(canonical)
				capture.size = len(canonical)
				responseBody = canonical
			}
			if err := m.repo.Complete(txCtx, reserved.ID, capture.Status(), types.JSON(responseBody)); err != nil {
				return fmt.Errorf("%w: %v", errProductionIdempotencyCompleteFailed, err)
			}
			return nil
		})

		if transactionErr != nil {
			switch {
			case errors.Is(transactionErr, errProductionTerminalResponse):
				capture.flushTo(original)
			case errors.Is(transactionErr, errProductionHandlerResponseMissing):
				return
			case errors.Is(transactionErr, errProductionIdempotencyReserveFailed):
				logger.Errorf(ctx, "production idempotency reservation failed: %v", transactionErr)
				writeProductionIdempotencyResponse(original, http.StatusInternalServerError,
					productionIdempotencyUnavailable, "idempotency reservation failed", true)
			case errors.Is(transactionErr, errProductionIdempotencyCompleteFailed):
				logger.Errorf(ctx, "production idempotency completion failed: %v", transactionErr)
				writeProductionIdempotencyResponse(original, http.StatusInternalServerError,
					productionIdempotencyUnavailable, "idempotency completion failed", true)
			default:
				logger.Errorf(ctx, "production idempotency transaction failed: %v", transactionErr)
				writeProductionIdempotencyResponse(original, http.StatusInternalServerError,
					productionIdempotencyUnavailable, "idempotency transaction failed", true)
			}
			return
		}
		if !created {
			m.replayOrReject(c, reserved, digest)
			return
		}
		if err := runAfterCommit(ctx); err != nil {
			logger.Errorf(ctx, "production post-commit wakeup failed: %v", err)
		}
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

func productionRequestDigest(body []byte, ifMatch ...string) string {
	canonical := body
	if encoded, ok := canonicalProductionJSON(body); ok {
		canonical = encoded
	}
	digest := sha256.New()
	_, _ = digest.Write(canonical)
	if len(ifMatch) > 0 {
		normalized := strings.TrimSpace(ifMatch[0])
		if normalized != "" {
			_, _ = digest.Write([]byte{0})
			_, _ = digest.Write([]byte("if-match"))
			_, _ = digest.Write([]byte{0})
			_, _ = digest.Write([]byte(normalized))
		}
	}
	return hex.EncodeToString(digest.Sum(nil))
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
	header    http.Header
	body      bytes.Buffer
	status    int
	size      int
	statusSet bool
}

func newProductionResponseCapture(w gin.ResponseWriter) *productionResponseCapture {
	return &productionResponseCapture{
		ResponseWriter: w,
		header:         w.Header().Clone(),
		status:         http.StatusOK,
		size:           -1,
	}
}

func (w *productionResponseCapture) Header() http.Header { return w.header }

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
	for key := range destination.Header() {
		destination.Header().Del(key)
	}
	for key, values := range w.header {
		destination.Header()[key] = append([]string(nil), values...)
	}
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
