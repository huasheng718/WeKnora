package handler

import (
	"math"
	"net/http"
	"strings"
	"unicode/utf8"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// ProductionDocumentTypeHandler exposes the versioned document-type API.
type ProductionDocumentTypeHandler struct {
	service interfaces.ProductionDocumentTypeService
}

func NewProductionDocumentTypeHandler(
	service interfaces.ProductionDocumentTypeService,
) *ProductionDocumentTypeHandler {
	return &ProductionDocumentTypeHandler{service: service}
}

type createProductionDocumentTypeRequest struct {
	Code               string     `json:"code" binding:"required"`
	Name               string     `json:"name" binding:"required"`
	Description        string     `json:"description"`
	SchemaVersion      int        `json:"schema_version" binding:"required,min=1"`
	BlockSchema        types.JSON `json:"block_schema" binding:"required"`
	SourceRequirements types.JSON `json:"source_requirements" binding:"required"`
	SkillBindings      types.JSON `json:"skill_bindings" binding:"required"`
	WorkflowPlan       types.JSON `json:"workflow_plan"`
	QualityRules       types.JSON `json:"quality_rules" binding:"required"`
	ReviewPolicy       types.JSON `json:"review_policy" binding:"required"`
	PublicationPolicy  types.JSON `json:"publication_policy" binding:"required"`
}

func (h *ProductionDocumentTypeHandler) List(c *gin.Context) {
	tenantID, _, ok := productionRequestIdentity(c)
	if !ok {
		return
	}
	documentTypes, err := h.service.ListDocumentTypes(c.Request.Context(), tenantID)
	if err != nil {
		handleProductionServiceError(c, err, "failed to list production document types")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": documentTypes})
}

func (h *ProductionDocumentTypeHandler) Create(c *gin.Context) {
	tenantID, _, ok := productionRequestIdentity(c)
	if !ok {
		return
	}
	var request createProductionDocumentTypeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.Error(apperrors.NewValidationError("invalid production document type request").WithDetails(err.Error()))
		return
	}
	request.Code = strings.TrimSpace(request.Code)
	request.Name = strings.TrimSpace(request.Name)
	if request.Code == "" || request.Name == "" {
		c.Error(apperrors.NewValidationError("document type code and name are required"))
		return
	}
	if utf8.RuneCountInString(request.Code) > 255 || utf8.RuneCountInString(request.Name) > 255 {
		c.Error(apperrors.NewValidationError("document type code and name must be at most 255 characters"))
		return
	}
	if request.SchemaVersion > math.MaxInt32 {
		c.Error(apperrors.NewValidationError("schema_version must fit a positive PostgreSQL INTEGER"))
		return
	}
	documentType, err := h.service.CreateDocumentType(c.Request.Context(), tenantID,
		interfaces.CreateProductionDocumentTypeInput{
			Code:               request.Code,
			Name:               request.Name,
			Description:        request.Description,
			SchemaVersion:      request.SchemaVersion,
			BlockSchema:        request.BlockSchema,
			SourceRequirements: request.SourceRequirements,
			SkillBindings:      request.SkillBindings,
			WorkflowPlan:       request.WorkflowPlan,
			QualityRules:       request.QualityRules,
			ReviewPolicy:       request.ReviewPolicy,
			PublicationPolicy:  request.PublicationPolicy,
		})
	if err != nil {
		handleProductionServiceError(c, err, "failed to create production document type")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": documentType})
}

func (h *ProductionDocumentTypeHandler) Activate(c *gin.Context) {
	tenantID, _, ok := productionRequestIdentity(c)
	if !ok {
		return
	}
	documentTypeID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(documentTypeID) {
		c.Error(apperrors.NewValidationError("document type id must be a valid UUID"))
		return
	}
	documentType, err := h.service.GetDocumentType(c.Request.Context(), tenantID, documentTypeID)
	if err != nil {
		handleProductionServiceError(c, err, "failed to get production document type")
		return
	}
	activated, err := h.service.ActivateDocumentType(
		c.Request.Context(), tenantID, documentType.Code, documentType.SchemaVersion,
	)
	if err != nil {
		handleProductionServiceError(c, err, "failed to activate production document type")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": activated})
}
