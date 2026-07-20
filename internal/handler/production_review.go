package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	appservice "github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const productionReviewMaxBodyBytes = 512 << 10

// ProductionAnnotationService is the governed annotation surface exposed by the review API.
type ProductionAnnotationService interface {
	List(context.Context, appservice.ListProductionAnnotationsInput) (*appservice.ProductionAnnotationPage, error)
	Create(context.Context, appservice.CreateProductionAnnotationInput) (*types.ProductionAnnotation, error)
	Resolve(context.Context, string, types.ProductionAnnotationStatus) error
}

func (h *ProductionReviewHandler) ListAnnotations(c *gin.Context) {
	documentID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(documentID) {
		c.Error(apperrors.NewValidationError("document id must be a canonical UUID"))
		return
	}
	page, pageSize, ok := parseListPagination(c)
	if !ok {
		return
	}
	if page > types.ProductionAnnotationMaxPage ||
		page-1 > int(^uint(0)>>1)/pageSize ||
		(page-1)*pageSize > types.ProductionAnnotationMaxOffset {
		c.Error(apperrors.NewValidationError("page is too large"))
		return
	}
	input := appservice.ListProductionAnnotationsInput{
		DocumentID: documentID,
		VersionID:  strings.TrimSpace(c.Query("version_id")),
		Status:     types.ProductionAnnotationStatus(strings.TrimSpace(c.Query("status"))),
		AnnotationType: types.ProductionAnnotationType(
			strings.TrimSpace(c.Query("annotation_type")),
		),
		Severity: types.ProductionAnnotationSeverity(strings.TrimSpace(c.Query("severity"))),
		Page:     page, PageSize: pageSize,
	}
	if (input.VersionID != "" && !isProductionUUID(input.VersionID)) ||
		(input.Status != "" && !input.Status.IsValid()) ||
		(input.AnnotationType != "" && !input.AnnotationType.IsValid()) ||
		(input.Severity != "" && !input.Severity.IsValid()) {
		c.Error(apperrors.NewValidationError("invalid production annotation list filters"))
		return
	}
	result, err := h.annotations.List(c.Request.Context(), input)
	if err != nil {
		handleProductionReviewServiceError(c, err, "failed to list production annotations")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true, "data": result.Data, "total": result.Total,
		"page": result.Page, "page_size": result.PageSize,
	})
}

// ProductionReviewService is the governed review surface exposed over HTTP.
type ProductionReviewService interface {
	Submit(context.Context, string, string) (*types.ProductionReviewRequest, error)
	Get(context.Context, string) (*types.ProductionReviewRequest, error)
	Decide(context.Context, string, types.ProductionReviewDecision, string) error
	Reject(context.Context, string, string) error
	Cancel(context.Context, string, string) error
}

// ProductionReviewHandler exposes annotation and review governance without
// granting handlers direct repository access.
type ProductionReviewHandler struct {
	annotations ProductionAnnotationService
	reviews     ProductionReviewService
}

func NewProductionReviewHandler(
	annotations ProductionAnnotationService,
	reviews ProductionReviewService,
) *ProductionReviewHandler {
	return &ProductionReviewHandler{annotations: annotations, reviews: reviews}
}

type createProductionAnnotationRequest struct {
	VersionID        string                              `json:"version_id"`
	BlockID          string                              `json:"block_id"`
	AnnotationType   types.ProductionAnnotationType      `json:"annotation_type"`
	QualityTag       *types.ProductionAnnotationCategory `json:"quality_tag"`
	Severity         types.ProductionAnnotationSeverity  `json:"severity"`
	Anchor           json.RawMessage                     `json:"anchor"`
	Body             string                              `json:"body"`
	SuggestedContent *string                             `json:"suggested_content"`
}

type updateProductionAnnotationStatusRequest struct {
	Status types.ProductionAnnotationStatus `json:"status"`
}

type submitProductionReviewRequest struct {
	VersionID string `json:"version_id"`
}

type decideProductionReviewStepRequest struct {
	Decision types.ProductionReviewDecision `json:"decision"`
	Comment  string                         `json:"comment"`
}

type terminalProductionReviewRequest struct {
	Reason string `json:"reason"`
}

func (h *ProductionReviewHandler) CreateAnnotation(c *gin.Context) {
	documentID := strings.TrimSpace(c.Param("id"))
	var request createProductionAnnotationRequest
	if !isProductionUUID(documentID) || decodeStrictProductionReviewBody(c, &request) != nil {
		c.Error(apperrors.NewValidationError("invalid production annotation request"))
		return
	}
	input, err := request.toInput(documentID)
	if err != nil {
		if errors.Is(err, types.ErrProductionAnnotationAnchorInvalid) {
			handleProductionReviewServiceError(c, err, "invalid production annotation anchor")
			return
		}
		c.Error(apperrors.NewValidationError(err.Error()))
		return
	}
	annotation, err := h.annotations.Create(c.Request.Context(), input)
	if err != nil {
		handleProductionReviewServiceError(c, err, "failed to create production annotation")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": annotation})
}

func (r createProductionAnnotationRequest) toInput(documentID string) (appservice.CreateProductionAnnotationInput, error) {
	r.VersionID = strings.TrimSpace(r.VersionID)
	r.BlockID = strings.TrimSpace(r.BlockID)
	r.Body = strings.TrimSpace(r.Body)
	if !isProductionUUID(r.VersionID) || !isProductionUUID(r.BlockID) {
		return appservice.CreateProductionAnnotationInput{}, errors.New("version_id and block_id must be canonical UUIDs")
	}
	if !r.AnnotationType.IsValid() || !r.Severity.IsValid() {
		return appservice.CreateProductionAnnotationInput{}, errors.New("annotation_type and severity are invalid")
	}
	if r.AnnotationType == types.ProductionAnnotationQualityTag {
		if r.QualityTag == nil || !r.QualityTag.IsValid() {
			return appservice.CreateProductionAnnotationInput{}, errors.New("quality_tag is required for quality annotations")
		}
	} else if r.QualityTag != nil {
		return appservice.CreateProductionAnnotationInput{}, errors.New("quality_tag is only valid for quality annotations")
	}
	if count := utf8.RuneCountInString(r.Body); count < 1 || count > 20000 {
		return appservice.CreateProductionAnnotationInput{}, errors.New("body must contain 1 to 20000 characters")
	}
	if r.SuggestedContent != nil && utf8.RuneCountInString(*r.SuggestedContent) > 20000 {
		return appservice.CreateProductionAnnotationInput{}, errors.New("suggested_content must be at most 20000 characters")
	}
	anchor, err := validateProductionAnnotationAnchor(r.Anchor)
	if err != nil {
		return appservice.CreateProductionAnnotationInput{}, errors.Join(
			types.ErrProductionAnnotationAnchorInvalid,
			errors.New("anchor must be a bounded JSON object"),
		)
	}
	return appservice.CreateProductionAnnotationInput{
		DocumentID: documentID, VersionID: r.VersionID, BlockID: r.BlockID,
		AnnotationType: r.AnnotationType, QualityTag: r.QualityTag, Severity: r.Severity,
		Anchor: anchor, Body: r.Body, SuggestedContent: r.SuggestedContent,
	}, nil
}

func validateProductionAnnotationAnchor(raw json.RawMessage) (types.JSON, error) {
	if len(raw) == 0 {
		return nil, errors.New("anchor is required")
	}
	if err := types.ValidateProductionJSONResource(
		types.JSON(raw), types.ProductionAnnotationAnchorMaxBytes, types.ProductionAnnotationAnchorMaxDepth,
	); err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, errors.New("anchor must be an object")
	}
	return types.CanonicalProductionJSON(types.JSON(raw))
}

func (h *ProductionReviewHandler) UpdateAnnotationStatus(c *gin.Context) {
	annotationID := strings.TrimSpace(c.Param("id"))
	var request updateProductionAnnotationStatusRequest
	if !isProductionUUID(annotationID) || decodeStrictProductionReviewBody(c, &request) != nil ||
		(request.Status != types.ProductionAnnotationResolved && request.Status != types.ProductionAnnotationDismissed) {
		c.Error(apperrors.NewValidationError("invalid production annotation status request"))
		return
	}
	if err := h.annotations.Resolve(c.Request.Context(), annotationID, request.Status); err != nil {
		handleProductionReviewServiceError(c, err, "failed to update production annotation status")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *ProductionReviewHandler) Submit(c *gin.Context) {
	documentID := strings.TrimSpace(c.Param("id"))
	var request submitProductionReviewRequest
	if !isProductionUUID(documentID) || decodeStrictProductionReviewBody(c, &request) != nil {
		c.Error(apperrors.NewValidationError("invalid production review submission"))
		return
	}
	request.VersionID = strings.TrimSpace(request.VersionID)
	if !isProductionUUID(request.VersionID) {
		c.Error(apperrors.NewValidationError("version_id must be a canonical UUID"))
		return
	}
	review, err := h.reviews.Submit(c.Request.Context(), documentID, request.VersionID)
	if err != nil {
		handleProductionReviewServiceError(c, err, "failed to submit production review")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": review})
}

func (h *ProductionReviewHandler) Get(c *gin.Context) {
	reviewID := strings.TrimSpace(c.Param("id"))
	if !isProductionUUID(reviewID) {
		c.Error(apperrors.NewValidationError("review id must be a canonical UUID"))
		return
	}
	review, err := h.reviews.Get(c.Request.Context(), reviewID)
	if err != nil {
		handleProductionReviewServiceError(c, err, "failed to get production review")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": review})
}

func (h *ProductionReviewHandler) Decide(c *gin.Context) {
	reviewID := strings.TrimSpace(c.Param("id"))
	stepID := strings.TrimSpace(c.Param("step_id"))
	var request decideProductionReviewStepRequest
	if !isProductionUUID(reviewID) || !isProductionUUID(stepID) ||
		decodeStrictProductionReviewBody(c, &request) != nil || !request.Decision.IsProfessionalDecision() {
		c.Error(apperrors.NewValidationError("invalid production review decision request"))
		return
	}
	request.Comment = strings.TrimSpace(request.Comment)
	if utf8.RuneCountInString(request.Comment) > 5000 ||
		(request.Decision != types.ProductionReviewApproved && request.Comment == "") {
		c.Error(apperrors.NewValidationError("invalid production review decision comment"))
		return
	}
	review, err := h.reviews.Get(c.Request.Context(), reviewID)
	if err != nil {
		handleProductionReviewServiceError(c, err, "failed to load production review decision scope")
		return
	}
	if !productionReviewOwnsStep(review, reviewID, stepID) ||
		(review.Status != "" && review.Status != types.ProductionReviewPending) {
		handleProductionReviewServiceError(c, types.ErrProductionReviewLifecycle, "production review decision conflict")
		return
	}
	if err := h.reviews.Decide(c.Request.Context(), stepID, request.Decision, request.Comment); err != nil {
		handleProductionReviewServiceError(c, err, "failed to decide production review step")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *ProductionReviewHandler) Reject(c *gin.Context) {
	reviewID, reason, ok := productionReviewTerminalRequest(c)
	if !ok {
		return
	}
	if err := h.reviews.Reject(c.Request.Context(), reviewID, reason); err != nil {
		handleProductionReviewServiceError(c, err, "failed to reject production review")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *ProductionReviewHandler) Cancel(c *gin.Context) {
	reviewID, reason, ok := productionReviewTerminalRequest(c)
	if !ok {
		return
	}
	if err := h.reviews.Cancel(c.Request.Context(), reviewID, reason); err != nil {
		handleProductionReviewServiceError(c, err, "failed to cancel production review")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func productionReviewTerminalRequest(c *gin.Context) (string, string, bool) {
	reviewID := strings.TrimSpace(c.Param("id"))
	var request terminalProductionReviewRequest
	if !isProductionUUID(reviewID) || decodeStrictProductionReviewBody(c, &request) != nil {
		c.Error(apperrors.NewValidationError("invalid production review terminal request"))
		return "", "", false
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if length := utf8.RuneCountInString(request.Reason); length < 1 || length > 5000 {
		c.Error(apperrors.NewValidationError("production review terminal reason must contain 1 to 5000 characters"))
		return "", "", false
	}
	return reviewID, request.Reason, true
}

func productionReviewOwnsStep(review *types.ProductionReviewRequest, reviewID, stepID string) bool {
	if review == nil || review.ID != reviewID {
		return false
	}
	for _, step := range review.Steps {
		if step != nil && step.ID == stepID && step.ReviewRequestID == reviewID {
			return true
		}
	}
	return false
}

func decodeStrictProductionReviewBody(c *gin.Context, target any) error {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return errors.New("request body is required")
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, productionReviewMaxBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > productionReviewMaxBodyBytes {
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

func handleProductionReviewServiceError(c *gin.Context, err error, message string) {
	switch {
	case errors.Is(err, types.ErrProductionBlockingAnnotations),
		errors.Is(err, types.ErrProductionAnnotationAnchorInvalid):
		c.Error(&apperrors.AppError{
			Code: apperrors.ErrValidation, Message: "production review request cannot be processed",
			HTTPCode: http.StatusUnprocessableEntity,
		})
	case errors.Is(err, types.ErrProductionConflict),
		errors.Is(err, types.ErrProductionDocumentStaleParent),
		errors.Is(err, types.ErrProductionReviewScopeInvalid),
		errors.Is(err, types.ErrProductionReviewLifecycle),
		errors.Is(err, types.ErrProductionReviewImmutable),
		errors.Is(err, types.ErrProductionReviewPolicyInvalid),
		errors.Is(err, types.ErrProductionAnnotationLifecycle),
		errors.Is(err, types.ErrProductionAnnotationImmutable),
		errors.Is(err, types.ErrProductionContentDigestMismatch):
		c.Error(apperrors.NewConflictError("production review state conflict"))
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.Error(apperrors.NewNotFoundError("production review resource not found"))
	default:
		handleProductionServiceError(c, err, message)
	}
}
