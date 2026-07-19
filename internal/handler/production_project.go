package handler

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ProductionProjectHandler exposes the production project foundation API.
type ProductionProjectHandler struct {
	service interfaces.ProductionProjectService
}

func NewProductionProjectHandler(service interfaces.ProductionProjectService) *ProductionProjectHandler {
	return &ProductionProjectHandler{service: service}
}

type createProductionProjectRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

type assignProductionProjectRoleRequest struct {
	UserID string               `json:"user_id" binding:"required"`
	Role   types.ProductionRole `json:"role" binding:"required"`
}

func (h *ProductionProjectHandler) List(c *gin.Context) {
	tenantID, userID, ok := productionRequestIdentity(c)
	if !ok {
		return
	}
	projects, err := h.service.ListProjects(c.Request.Context(), tenantID, userID)
	if err != nil {
		handleProductionServiceError(c, err, "failed to list production projects")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": projects})
}

func (h *ProductionProjectHandler) Create(c *gin.Context) {
	var request createProductionProjectRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.Error(apperrors.NewValidationError("invalid production project request").WithDetails(err.Error()))
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" {
		c.Error(apperrors.NewValidationError("project name is required"))
		return
	}
	if utf8.RuneCountInString(request.Name) > 255 {
		c.Error(apperrors.NewValidationError("project name must be at most 255 characters"))
		return
	}
	project, err := h.service.CreateProject(c.Request.Context(), interfaces.CreateProductionProjectInput{
		Name: request.Name, Description: request.Description,
	})
	if err != nil {
		handleProductionServiceError(c, err, "failed to create production project")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": project})
}

func (h *ProductionProjectHandler) AssignRole(c *gin.Context) {
	projectID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(projectID) {
		c.Error(apperrors.NewValidationError("project id must be a valid UUID"))
		return
	}
	var request assignProductionProjectRoleRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.Error(apperrors.NewValidationError("invalid project role request").WithDetails(err.Error()))
		return
	}
	request.UserID = strings.TrimSpace(request.UserID)
	if !isProductionUUID(request.UserID) {
		c.Error(apperrors.NewValidationError("user_id must be a valid UUID"))
		return
	}
	if !request.Role.IsValid() {
		c.Error(apperrors.NewValidationError("valid role is required"))
		return
	}
	if err := h.service.AssignRole(c.Request.Context(), projectID, request.UserID, request.Role); err != nil {
		handleProductionServiceError(c, err, "failed to assign production project role")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *ProductionProjectHandler) RemoveRole(c *gin.Context) {
	projectID := strings.TrimSpace(c.Param("id"))
	userID := strings.TrimSpace(c.Param("user_id"))
	role := types.ProductionRole(strings.TrimSpace(c.Param("role")))
	if !isProductionUUID(projectID) {
		c.Error(apperrors.NewValidationError("project id must be a valid UUID"))
		return
	}
	if !isProductionUUID(userID) {
		c.Error(apperrors.NewValidationError("user id must be a valid UUID"))
		return
	}
	if !role.IsValid() {
		c.Error(apperrors.NewValidationError("valid role is required"))
		return
	}
	if err := h.service.RemoveRole(c.Request.Context(), projectID, userID, role); err != nil {
		handleProductionServiceError(c, err, "failed to remove production project role")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func isProductionUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func productionRequestIdentity(c *gin.Context) (uint64, string, bool) {
	tenantID, tenantOK := types.TenantIDFromContext(c.Request.Context())
	userID, userOK := types.UserIDFromContext(c.Request.Context())
	if !tenantOK || tenantID == 0 || !userOK {
		c.Error(apperrors.NewUnauthorizedError("production request identity is missing"))
		return 0, "", false
	}
	return tenantID, userID, true
}

func handleProductionServiceError(c *gin.Context, err error, message string) {
	switch {
	case errors.Is(err, types.ErrProductionForbidden):
		c.Error(apperrors.NewForbiddenError("production operation forbidden"))
	case errors.Is(err, types.ErrProductionConflict),
		errors.Is(err, types.ErrProductionSourceSetFrozen),
		errors.Is(err, types.ErrProductionEvidenceMissing),
		errors.Is(err, types.ErrProductionEvidenceImmutable),
		errors.Is(err, types.ErrProductionDocumentStaleParent),
		errors.Is(err, types.ErrProductionDocumentSourceSetInvalid),
		errors.Is(err, types.ErrProductionDocumentTypeInactive),
		errors.Is(err, types.ErrProductionDocumentTypeImmutable),
		errors.Is(err, types.ErrProductionDocumentVersionImmutable),
		errors.Is(err, types.ErrProductionDocumentBlockImmutable):
		c.Error(apperrors.NewConflictError("production resource state conflict"))
	case errors.Is(err, types.ErrProductionEvidenceDigestMismatch),
		errors.Is(err, types.ErrProductionEvidenceResourceInvalid),
		errors.Is(err, types.ErrProductionBlockLineageInvalid):
		c.Error(apperrors.NewValidationError("invalid production resource"))
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.Error(apperrors.NewNotFoundError("production resource not found"))
	default:
		c.Error(apperrors.NewInternalServerError(message))
	}
}
