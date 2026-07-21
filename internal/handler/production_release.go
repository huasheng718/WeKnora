package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ProductionReleaseService is the governed publication surface exposed over HTTP.
type ProductionReleaseService interface {
	Prepare(context.Context, string, string, []string) (*types.ProductionRelease, error)
	GetTarget(context.Context, string) (*types.ProductionReleaseTarget, error)
	Activate(context.Context, string, int) error
	Retry(context.Context, string) error
	Rollback(context.Context, string, int) error
}

// ProductionReleaseHandler exposes release preparation and target lifecycle commands.
type ProductionReleaseHandler struct {
	service ProductionReleaseService
}

func NewProductionReleaseHandler(service ProductionReleaseService) *ProductionReleaseHandler {
	return &ProductionReleaseHandler{service: service}
}

type createProductionReleaseRequest struct {
	VersionID              string   `json:"version_id" binding:"required"`
	TargetKnowledgeBaseIDs []string `json:"target_knowledge_base_ids" binding:"required,min=1"`
}

type productionReleaseTargetCommandRequest struct {
	ExpectedLock *int `json:"expected_lock"`
}

func (h *ProductionReleaseHandler) Create(c *gin.Context) {
	documentID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(documentID) {
		c.Error(apperrors.NewValidationError("document id must be a canonical UUID"))
		return
	}
	var request createProductionReleaseRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.Error(apperrors.NewValidationError("invalid production release request"))
		return
	}
	request.VersionID = strings.TrimSpace(request.VersionID)
	if !isProductionUUID(request.VersionID) {
		c.Error(apperrors.NewValidationError("version_id must be a canonical UUID"))
		return
	}
	seen := make(map[string]struct{}, len(request.TargetKnowledgeBaseIDs))
	for i, targetKBID := range request.TargetKnowledgeBaseIDs {
		targetKBID = strings.TrimSpace(targetKBID)
		if !isProductionUUID(targetKBID) {
			c.Error(apperrors.NewValidationError("target_knowledge_base_ids must contain canonical UUIDs"))
			return
		}
		if _, duplicate := seen[targetKBID]; duplicate {
			c.Error(apperrors.NewValidationError("target_knowledge_base_ids must be unique"))
			return
		}
		seen[targetKBID] = struct{}{}
		request.TargetKnowledgeBaseIDs[i] = targetKBID
	}
	release, err := h.service.Prepare(c.Request.Context(), documentID, request.VersionID, request.TargetKnowledgeBaseIDs)
	if err != nil {
		handleProductionReleaseServiceError(c, err, "failed to create production release")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": release})
}

func (h *ProductionReleaseHandler) GetTarget(c *gin.Context) {
	targetID, ok := productionReleaseTargetIDFromRequest(c)
	if !ok {
		return
	}
	target, err := h.service.GetTarget(c.Request.Context(), targetID)
	if err != nil {
		handleProductionReleaseServiceError(c, err, "failed to get production release target")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": target})
}

func (h *ProductionReleaseHandler) Activate(c *gin.Context) {
	targetID, expectedLock, ok := productionReleaseTargetCommand(c)
	if !ok {
		return
	}
	if err := h.service.Activate(c.Request.Context(), targetID, expectedLock); err != nil {
		handleProductionReleaseServiceError(c, err, "failed to activate production release target")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *ProductionReleaseHandler) Retry(c *gin.Context) {
	targetID, ok := productionReleaseTargetIDFromRequest(c)
	if !ok {
		return
	}
	if err := h.service.Retry(c.Request.Context(), targetID); err != nil {
		handleProductionReleaseServiceError(c, err, "failed to retry production release target")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *ProductionReleaseHandler) Rollback(c *gin.Context) {
	targetID, expectedLock, ok := productionReleaseTargetCommand(c)
	if !ok {
		return
	}
	if err := h.service.Rollback(c.Request.Context(), targetID, expectedLock); err != nil {
		handleProductionReleaseServiceError(c, err, "failed to roll back production release target")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func productionReleaseTargetIDFromRequest(c *gin.Context) (string, bool) {
	targetID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(targetID) {
		c.Error(apperrors.NewValidationError("release target id must be a canonical UUID"))
		return "", false
	}
	return targetID, true
}

func productionReleaseTargetCommand(c *gin.Context) (string, int, bool) {
	targetID, ok := productionReleaseTargetIDFromRequest(c)
	if !ok {
		return "", 0, false
	}
	var request productionReleaseTargetCommandRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.ExpectedLock == nil || *request.ExpectedLock < 0 {
		c.Error(apperrors.NewValidationError("expected_lock must be a non-negative integer"))
		return "", 0, false
	}
	return targetID, *request.ExpectedLock, true
}

func handleProductionReleaseServiceError(c *gin.Context, err error, message string) {
	switch {
	case errors.Is(err, types.ErrProductionForbidden):
		c.Error(apperrors.NewForbiddenError("production operation forbidden"))
	case errors.Is(err, types.ErrProductionReleaseLifecycle),
		errors.Is(err, types.ErrProductionProjectionConflict),
		errors.Is(err, types.ErrProductionProjectionInactive),
		errors.Is(err, types.ErrProductionReleaseReprepareRequired),
		errors.Is(err, types.ErrProductionReviewScopeInvalid),
		errors.Is(err, types.ErrProductionConflict):
		c.Error(apperrors.NewConflictError("production release state conflict"))
	case errors.Is(err, types.ErrProductionReleaseInvalid),
		errors.Is(err, types.ErrProductionReleaseConfigInvalid),
		errors.Is(err, types.ErrProductionReleasePatchInvalid):
		c.Error(apperrors.NewValidationError("invalid production release request"))
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.Error(apperrors.NewNotFoundError("production release target not found"))
	default:
		c.Error(apperrors.NewInternalServerError(message))
	}
}
