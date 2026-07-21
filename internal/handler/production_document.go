package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// ProductionDocumentHandler exposes document creation and immutable versions.
type ProductionDocumentHandler struct {
	service interfaces.ProductionDocumentService
}

func NewProductionDocumentHandler(service interfaces.ProductionDocumentService) *ProductionDocumentHandler {
	return &ProductionDocumentHandler{service: service}
}

type createProductionDocumentRequest struct {
	DocumentTypeID string `json:"document_type_id" binding:"required"`
	SourceSetID    string `json:"source_set_id" binding:"required"`
	Title          string `json:"title" binding:"required"`
}

type appendProductionVersionRequest struct {
	SourceSetID   string                           `json:"source_set_id" binding:"required"`
	Origin        types.ProductionDocumentOrigin   `json:"origin"`
	ChangeSummary string                           `json:"change_summary"`
	Blocks        []productionDocumentBlockRequest `json:"blocks" binding:"required,min=1"`
	Lineage       []productionBlockLineageRequest  `json:"lineage"`
}

type productionDocumentBlockRequest struct {
	LogicalBlockID string     `json:"logical_block_id"`
	BlockType      string     `json:"block_type" binding:"required"`
	Content        types.JSON `json:"content"`
	Attributes     types.JSON `json:"attributes"`
	EvidenceRefs   types.JSON `json:"evidence_refs"`
	AIProvenance   types.JSON `json:"ai_provenance"`
}

type productionBlockLineageRequest struct {
	FromLogicalBlockID string                        `json:"from_logical_block_id" binding:"required"`
	ToLogicalBlockID   string                        `json:"to_logical_block_id" binding:"required"`
	Relation           types.ProductionBlockRelation `json:"relation" binding:"required"`
}

type productionDocumentVersionDetailResponse struct {
	*types.ProductionDocumentVersion
	Blocks  []*types.ProductionDocumentBlock `json:"blocks"`
	Lineage []*types.ProductionBlockLineage  `json:"lineage"`
}

func (h *ProductionDocumentHandler) Create(c *gin.Context) {
	projectID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(projectID) {
		c.Error(apperrors.NewValidationError("project id must be a canonical UUID"))
		return
	}
	var request createProductionDocumentRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.Error(apperrors.NewValidationError("invalid production document request"))
		return
	}
	request.DocumentTypeID = strings.TrimSpace(request.DocumentTypeID)
	request.SourceSetID = strings.TrimSpace(request.SourceSetID)
	request.Title = strings.TrimSpace(request.Title)
	if !isProductionUUID(request.DocumentTypeID) {
		c.Error(apperrors.NewValidationError("document_type_id must be a canonical UUID"))
		return
	}
	if !isProductionUUID(request.SourceSetID) {
		c.Error(apperrors.NewValidationError("source_set_id must be a canonical UUID"))
		return
	}
	if request.Title == "" || utf8.RuneCountInString(request.Title) > 255 {
		c.Error(apperrors.NewValidationError("title must contain 1 to 255 characters"))
		return
	}
	created, err := h.service.CreateDocument(c.Request.Context(), interfaces.CreateProductionDocumentInput{
		ProjectID: projectID, DocumentTypeID: request.DocumentTypeID,
		SourceSetID: request.SourceSetID, Title: request.Title,
	})
	if err != nil {
		handleProductionServiceError(c, err, "failed to create production document")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": created})
}

func (h *ProductionDocumentHandler) Get(c *gin.Context) {
	documentID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(documentID) {
		c.Error(apperrors.NewValidationError("document id must be a canonical UUID"))
		return
	}
	document, err := h.service.GetDocument(c.Request.Context(), documentID)
	if err != nil {
		handleProductionServiceError(c, err, "failed to get production document")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": document})
}

func (h *ProductionDocumentHandler) GetVersion(c *gin.Context) {
	documentID := strings.TrimSpace(c.Param("id"))
	versionID := strings.TrimSpace(c.Param("version_id"))
	if !isProductionUUID(documentID) || !isProductionUUID(versionID) {
		c.Error(apperrors.NewValidationError("document and version ids must be canonical UUIDs"))
		return
	}
	version, err := h.service.GetVersionDetail(c.Request.Context(), documentID, versionID)
	if err != nil {
		handleProductionServiceError(c, err, "failed to get production document version")
		return
	}
	blocks := version.Blocks
	if blocks == nil {
		blocks = []*types.ProductionDocumentBlock{}
	}
	lineage := version.Lineage
	if lineage == nil {
		lineage = []*types.ProductionBlockLineage{}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": productionDocumentVersionDetailResponse{
		ProductionDocumentVersion: version,
		Blocks:                    blocks,
		Lineage:                   lineage,
	}})
}

func (h *ProductionDocumentHandler) AppendVersion(c *gin.Context) {
	documentID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(documentID) {
		c.Error(apperrors.NewValidationError("document id must be a canonical UUID"))
		return
	}
	parentVersionID := strings.TrimSpace(c.GetHeader("If-Match"))
	if !isProductionUUID(parentVersionID) {
		c.Error(apperrors.NewValidationError("If-Match must contain the canonical current version UUID"))
		return
	}
	var request appendProductionVersionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.Error(apperrors.NewValidationError("invalid production document version request"))
		return
	}
	input, err := request.toInput(parentVersionID)
	if err != nil {
		c.Error(apperrors.NewValidationError(err.Error()))
		return
	}
	version, err := h.service.AppendVersion(c.Request.Context(), documentID, input)
	if err != nil {
		handleProductionServiceError(c, err, "failed to append production document version")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": version})
}

func (r appendProductionVersionRequest) toInput(parentVersionID string) (interfaces.AppendProductionVersionInput, error) {
	r.SourceSetID = strings.TrimSpace(r.SourceSetID)
	if !isProductionUUID(r.SourceSetID) {
		return interfaces.AppendProductionVersionInput{}, errors.New("source_set_id must be a canonical UUID")
	}
	if r.Origin != "" && !r.Origin.IsValid() {
		return interfaces.AppendProductionVersionInput{}, errors.New("origin is invalid")
	}
	blocks := make([]types.ProductionDocumentBlockInput, 0, len(r.Blocks))
	logicalIDs := make(map[string]struct{}, len(r.Blocks))
	for _, request := range r.Blocks {
		request.LogicalBlockID = strings.TrimSpace(request.LogicalBlockID)
		request.BlockType = strings.TrimSpace(request.BlockType)
		if len(request.LogicalBlockID) > 36 {
			return interfaces.AppendProductionVersionInput{}, errors.New("logical_block_id must be at most 36 bytes")
		}
		if request.LogicalBlockID != "" {
			if _, duplicate := logicalIDs[request.LogicalBlockID]; duplicate {
				return interfaces.AppendProductionVersionInput{}, errors.New("logical_block_id values must be unique")
			}
			logicalIDs[request.LogicalBlockID] = struct{}{}
		}
		if request.BlockType == "" || len(request.BlockType) > 24 {
			return interfaces.AppendProductionVersionInput{}, errors.New("block_type must contain 1 to 24 bytes")
		}
		for _, value := range []types.JSON{request.Content, request.Attributes, request.EvidenceRefs, request.AIProvenance} {
			if len(value) > 0 && !json.Valid(value) {
				return interfaces.AppendProductionVersionInput{}, errors.New("block JSON fields must contain valid JSON")
			}
		}
		blocks = append(blocks, types.ProductionDocumentBlockInput{
			LogicalBlockID: request.LogicalBlockID, BlockType: request.BlockType,
			Content: request.Content, Attributes: request.Attributes,
			EvidenceRefs: request.EvidenceRefs, AIProvenance: request.AIProvenance,
		})
	}
	lineage := make([]types.ProductionBlockLineageInput, 0, len(r.Lineage))
	for _, request := range r.Lineage {
		request.FromLogicalBlockID = strings.TrimSpace(request.FromLogicalBlockID)
		request.ToLogicalBlockID = strings.TrimSpace(request.ToLogicalBlockID)
		if request.FromLogicalBlockID == "" || request.ToLogicalBlockID == "" ||
			len(request.FromLogicalBlockID) > 36 || len(request.ToLogicalBlockID) > 36 || !request.Relation.IsValid() {
			return interfaces.AppendProductionVersionInput{}, errors.New("lineage fields are invalid")
		}
		lineage = append(lineage, types.ProductionBlockLineageInput{
			FromLogicalBlockID: request.FromLogicalBlockID,
			ToLogicalBlockID:   request.ToLogicalBlockID,
			Relation:           request.Relation,
		})
	}
	return interfaces.AppendProductionVersionInput{
		ParentVersionID: parentVersionID, SourceSetID: r.SourceSetID,
		Origin: r.Origin, ChangeSummary: r.ChangeSummary, Blocks: blocks, Lineage: lineage,
	}, nil
}

func (h *ProductionDocumentHandler) ListVersions(c *gin.Context) {
	documentID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(documentID) {
		c.Error(apperrors.NewValidationError("document id must be a canonical UUID"))
		return
	}
	versions, err := h.service.ListVersions(c.Request.Context(), documentID)
	if err != nil {
		handleProductionServiceError(c, err, "failed to list production document versions")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": versions})
}
