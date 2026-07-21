package handler

import (
	"net/http"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// ProductionSourceHandler exposes source-set lifecycle commands.
type ProductionSourceHandler struct {
	service interfaces.ProductionSourceService
}

func NewProductionSourceHandler(service interfaces.ProductionSourceService) *ProductionSourceHandler {
	return &ProductionSourceHandler{service: service}
}

type createProductionSourceSetRequest struct {
	DocumentTypeID string     `json:"document_type_id" binding:"required"`
	TimeRangeStart *time.Time `json:"time_range_start"`
	TimeRangeEnd   *time.Time `json:"time_range_end"`
}

type decideProductionSourceItemRequest struct {
	Decision types.ProductionSourceItemStatus `json:"decision" binding:"required"`
}

func (h *ProductionSourceHandler) ListSets(c *gin.Context) {
	projectID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(projectID) {
		c.Error(apperrors.NewValidationError("project id must be a canonical UUID"))
		return
	}
	sets, err := h.service.ListSets(c.Request.Context(), projectID)
	if err != nil {
		handleProductionServiceError(c, err, "failed to list production source sets")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": sets})
}

func (h *ProductionSourceHandler) CreateSet(c *gin.Context) {
	projectID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(projectID) {
		c.Error(apperrors.NewValidationError("project id must be a canonical UUID"))
		return
	}
	var request createProductionSourceSetRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.Error(apperrors.NewValidationError("invalid production source set request"))
		return
	}
	request.DocumentTypeID = strings.TrimSpace(request.DocumentTypeID)
	if !isProductionUUID(request.DocumentTypeID) {
		c.Error(apperrors.NewValidationError("document_type_id must be a canonical UUID"))
		return
	}
	if request.TimeRangeStart != nil && request.TimeRangeEnd != nil && request.TimeRangeStart.After(*request.TimeRangeEnd) {
		c.Error(apperrors.NewValidationError("time_range_start must not be after time_range_end"))
		return
	}
	created, err := h.service.CreateSet(c.Request.Context(), interfaces.CreateProductionSourceSetInput{
		ProjectID: projectID, DocumentTypeID: request.DocumentTypeID,
		TimeRangeStart: request.TimeRangeStart, TimeRangeEnd: request.TimeRangeEnd,
	})
	if err != nil {
		handleProductionServiceError(c, err, "failed to create production source set")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": created})
}

func (h *ProductionSourceHandler) DecideItem(c *gin.Context) {
	itemID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(itemID) {
		c.Error(apperrors.NewValidationError("source item id must be a canonical UUID"))
		return
	}
	var request decideProductionSourceItemRequest
	if err := c.ShouldBindJSON(&request); err != nil || !request.Decision.IsDecision() {
		c.Error(apperrors.NewValidationError("decision must be accepted, rejected, or unavailable"))
		return
	}
	if err := h.service.DecideItem(c.Request.Context(), itemID, request.Decision); err != nil {
		handleProductionServiceError(c, err, "failed to decide production source item")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *ProductionSourceHandler) Freeze(c *gin.Context) {
	sourceSetID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(sourceSetID) {
		c.Error(apperrors.NewValidationError("source set id must be a canonical UUID"))
		return
	}
	if err := h.service.Freeze(c.Request.Context(), sourceSetID); err != nil {
		handleProductionServiceError(c, err, "failed to freeze production source set")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}
