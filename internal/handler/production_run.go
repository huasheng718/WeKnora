package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

const productionRunMaxBodyBytes = 64 << 10

type ProductionRunHandler struct {
	service interfaces.ProductionRunService
}

func NewProductionRunHandler(service interfaces.ProductionRunService) *ProductionRunHandler {
	return &ProductionRunHandler{service: service}
}

type startProductionDocumentRunRequest struct {
	RunType types.ProductionRunType `json:"run_type"`
	ModelID string                  `json:"model_id"`
}

type startProductionCollectionRequest struct {
	ModelID string `json:"model_id"`
}

type productionToolDecisionRequest struct {
	Decision interfaces.ProductionToolDecision `json:"decision"`
}

func (h *ProductionRunHandler) Start(c *gin.Context) {
	documentID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(documentID) {
		c.Error(apperrors.NewValidationError("document id must be a canonical UUID"))
		return
	}
	var request startProductionDocumentRunRequest
	if err := decodeStrictProductionRunBody(c, &request); err != nil ||
		!isProductionUUID(request.ModelID) ||
		(request.RunType != types.ProductionRunWrite && request.RunType != types.ProductionRunRewrite && request.RunType != types.ProductionRunValidate) {
		c.Error(apperrors.NewValidationError("invalid production run request"))
		return
	}
	run, err := h.service.StartDocumentRun(c.Request.Context(), documentID, interfaces.StartProductionDocumentRunInput{
		RunType: request.RunType, ModelID: request.ModelID,
	})
	if err != nil {
		handleProductionServiceError(c, err, "failed to start production run")
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": run})
}

func (h *ProductionRunHandler) StartCollection(c *gin.Context) {
	sourceSetID := strings.TrimSpace(c.Param("id"))
	var request startProductionCollectionRequest
	if !isProductionUUID(sourceSetID) || decodeStrictProductionRunBody(c, &request) != nil ||
		!isProductionUUID(request.ModelID) {
		c.Error(apperrors.NewValidationError("invalid production collection request"))
		return
	}
	run, err := h.service.StartSourceSetCollection(c.Request.Context(), sourceSetID, interfaces.StartProductionSourceSetCollectionInput{
		ModelID: request.ModelID,
	})
	if err != nil {
		handleProductionServiceError(c, err, "failed to start production collection")
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": run})
}

func (h *ProductionRunHandler) Get(c *gin.Context) {
	runID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(runID) {
		c.Error(apperrors.NewValidationError("run id must be a canonical UUID"))
		return
	}
	run, err := h.service.GetRun(c.Request.Context(), runID)
	if err != nil {
		handleProductionServiceError(c, err, "failed to get production run")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": run})
}

func (h *ProductionRunHandler) ListDocumentRuns(c *gin.Context) {
	documentID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(documentID) {
		c.Error(apperrors.NewValidationError("document id must be a canonical UUID"))
		return
	}
	runs, err := h.service.ListDocumentRuns(c.Request.Context(), documentID)
	if err != nil {
		handleProductionServiceError(c, err, "failed to list production document runs")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": runs})
}

func (h *ProductionRunHandler) ListToolCalls(c *gin.Context) {
	runID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(runID) {
		c.Error(apperrors.NewValidationError("run id must be a canonical UUID"))
		return
	}
	calls, err := h.service.ListToolCalls(c.Request.Context(), runID)
	if err != nil {
		handleProductionServiceError(c, err, "failed to list production tool calls")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": calls})
}

func (h *ProductionRunHandler) DecideToolCall(c *gin.Context) {
	callID := strings.TrimSpace(c.Param("id"))
	var request productionToolDecisionRequest
	if !isProductionUUID(callID) || decodeStrictProductionRunBody(c, &request) != nil || !request.Decision.IsValid() {
		c.Error(apperrors.NewValidationError("invalid production tool decision request"))
		return
	}
	call, err := h.service.DecideToolCall(c.Request.Context(), callID, request.Decision)
	if err != nil {
		handleProductionServiceError(c, err, "failed to decide production tool call")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": call})
}

func decodeStrictProductionRunBody(c *gin.Context, target any) error {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return errors.New("request body is required")
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, productionRunMaxBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > productionRunMaxBodyBytes {
		return errors.New("invalid request body")
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values are not allowed")
	}
	return nil
}
