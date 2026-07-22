package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	appservice "github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	productionReviewDocumentID   = "66666666-6666-4666-8666-666666666666"
	productionReviewVersionID    = "77777777-7777-4777-8777-777777777777"
	productionReviewBlockID      = "88888888-8888-4888-8888-888888888888"
	productionReviewAnnotationID = "99999999-9999-4999-8999-999999999999"
	productionReviewRequestID    = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	productionReviewStepID       = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

type productionReviewAnnotationServiceStub struct {
	created  appservice.CreateProductionAnnotationInput
	listed   appservice.ListProductionAnnotationsInput
	resolved struct {
		id     string
		status types.ProductionAnnotationStatus
	}
	err error
}

func (s *productionReviewAnnotationServiceStub) List(
	_ context.Context, input appservice.ListProductionAnnotationsInput,
) (*appservice.ProductionAnnotationPage, error) {
	s.listed = input
	if s.err != nil {
		return nil, s.err
	}
	return &appservice.ProductionAnnotationPage{
		Data:  []*types.ProductionAnnotation{{ID: productionReviewAnnotationID}},
		Total: 1, Page: input.Page, PageSize: input.PageSize,
	}, nil
}

func (s *productionReviewAnnotationServiceStub) Create(_ context.Context, input appservice.CreateProductionAnnotationInput) (*types.ProductionAnnotation, error) {
	s.created = input
	if s.err != nil {
		return nil, s.err
	}
	return &types.ProductionAnnotation{ID: productionReviewAnnotationID, DocumentID: input.DocumentID, VersionID: input.VersionID}, nil
}

func (s *productionReviewAnnotationServiceStub) Resolve(_ context.Context, id string, status types.ProductionAnnotationStatus) error {
	s.resolved.id, s.resolved.status = id, status
	return s.err
}

type productionReviewServiceStub struct {
	submittedDocumentID string
	submittedVersionID  string
	getID               string
	decidedStepID       string
	decision            types.ProductionReviewDecision
	comment             string
	rejectedReviewID    string
	rejectedReason      string
	cancelledReviewID   string
	cancelledReason     string
	review              *types.ProductionReviewRequest
	err                 error
	listedDocumentID    string
	listOffset          int
	listLimit           int
}

func (s *productionReviewServiceStub) List(_ context.Context, documentID string, offset, limit int) ([]*types.ProductionReviewRequest, int64, error) {
	s.listedDocumentID, s.listOffset, s.listLimit = documentID, offset, limit
	if s.err != nil {
		return nil, 0, s.err
	}
	return []*types.ProductionReviewRequest{{ID: productionReviewRequestID, DocumentID: documentID}}, 21, nil
}

func (s *productionReviewServiceStub) Submit(_ context.Context, documentID, versionID string) (*types.ProductionReviewRequest, error) {
	s.submittedDocumentID, s.submittedVersionID = documentID, versionID
	if s.err != nil {
		return nil, s.err
	}
	return &types.ProductionReviewRequest{ID: productionReviewRequestID, DocumentID: documentID, VersionID: versionID}, nil
}

func (s *productionReviewServiceStub) Get(_ context.Context, reviewID string) (*types.ProductionReviewRequest, error) {
	s.getID = reviewID
	if s.err != nil {
		return nil, s.err
	}
	if s.review != nil {
		return s.review, nil
	}
	return &types.ProductionReviewRequest{
		ID:    reviewID,
		Steps: []*types.ProductionReviewStep{{ID: productionReviewStepID, ReviewRequestID: reviewID}},
	}, nil
}

func (s *productionReviewServiceStub) Decide(_ context.Context, stepID string, decision types.ProductionReviewDecision, comment string) error {
	s.decidedStepID, s.decision, s.comment = stepID, decision, comment
	return s.err
}

func (s *productionReviewServiceStub) Reject(_ context.Context, reviewID, reason string) error {
	s.rejectedReviewID, s.rejectedReason = reviewID, reason
	return s.err
}
func (s *productionReviewServiceStub) Cancel(_ context.Context, reviewID, reason string) error {
	s.cancelledReviewID, s.cancelledReason = reviewID, reason
	return s.err
}

func productionReviewHandlerEngine(annotation *productionReviewAnnotationServiceStub, review *productionReviewServiceStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "author-1")
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleContributor)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	h := NewProductionReviewHandler(annotation, review)
	engine.POST("/documents/:id/annotations", h.CreateAnnotation)
	engine.GET("/documents/:id/annotations", h.ListAnnotations)
	engine.PUT("/annotations/:id/status", h.UpdateAnnotationStatus)
	engine.POST("/documents/:id/reviews", h.Submit)
	engine.GET("/documents/:id/reviews", h.List)
	engine.GET("/reviews/:id", h.Get)
	engine.POST("/reviews/:id/steps/:step_id/decision", h.Decide)
	engine.POST("/reviews/:id/reject", h.Reject)
	engine.POST("/reviews/:id/cancel", h.Cancel)
	return engine
}

func TestProductionReviewHandlerListsBoundedDocumentHistory(t *testing.T) {
	reviews := &productionReviewServiceStub{}
	response := productionReviewHandlerRequest(
		productionReviewHandlerEngine(&productionReviewAnnotationServiceStub{}, reviews),
		http.MethodGet,
		"/documents/"+productionReviewDocumentID+"/reviews?page=2&page_size=20",
		"",
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, productionReviewDocumentID, reviews.listedDocumentID)
	require.Equal(t, 20, reviews.listOffset)
	require.Equal(t, 20, reviews.listLimit)
	require.Contains(t, response.Body.String(), `"total":21`)
	require.Contains(t, response.Body.String(), `"has_more":false`)
}

func productionReviewHandlerRequest(engine *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	engine.ServeHTTP(recorder, request)
	return recorder
}

func TestProductionReviewHandlerExposesApprovedActions(t *testing.T) {
	annotations := &productionReviewAnnotationServiceStub{}
	reviews := &productionReviewServiceStub{}
	engine := productionReviewHandlerEngine(annotations, reviews)

	create := productionReviewHandlerRequest(engine, http.MethodPost, "/documents/"+productionReviewDocumentID+"/annotations",
		`{"version_id":"`+productionReviewVersionID+`","block_id":"`+productionReviewBlockID+`","annotation_type":"quality_tag","quality_tag":"missing_evidence","severity":"blocking","anchor":{"path":"/title"},"body":" Needs evidence ","suggested_content":"Cite source"}`)
	list := productionReviewHandlerRequest(engine, http.MethodGet, "/documents/"+productionReviewDocumentID+"/annotations?version_id="+productionReviewVersionID+"&status=open&annotation_type=comment&severity=info&page=2&page_size=10", "")
	resolve := productionReviewHandlerRequest(engine, http.MethodPut, "/annotations/"+productionReviewAnnotationID+"/status", `{"status":"resolved"}`)
	submit := productionReviewHandlerRequest(engine, http.MethodPost, "/documents/"+productionReviewDocumentID+"/reviews", `{"version_id":"`+productionReviewVersionID+`"}`)
	get := productionReviewHandlerRequest(engine, http.MethodGet, "/reviews/"+productionReviewRequestID, "")
	decide := productionReviewHandlerRequest(engine, http.MethodPost, "/reviews/"+productionReviewRequestID+"/steps/"+productionReviewStepID+"/decision", `{"decision":"approved","comment":" checked "}`)
	reject := productionReviewHandlerRequest(engine, http.MethodPost, "/reviews/"+productionReviewRequestID+"/reject", `{"reason":" rejected by admin "}`)
	cancel := productionReviewHandlerRequest(engine, http.MethodPost, "/reviews/"+productionReviewRequestID+"/cancel", `{"reason":" cancelled by owner "}`)

	require.Equal(t, http.StatusCreated, create.Code, create.Body.String())
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	require.Equal(t, productionReviewDocumentID, annotations.listed.DocumentID)
	require.Equal(t, productionReviewVersionID, annotations.listed.VersionID)
	require.Equal(t, types.ProductionAnnotationOpen, annotations.listed.Status)
	require.Equal(t, 2, annotations.listed.Page)
	require.Equal(t, 10, annotations.listed.PageSize)
	require.Contains(t, list.Body.String(), `"total":1`)
	require.Equal(t, productionReviewDocumentID, annotations.created.DocumentID)
	require.Equal(t, productionReviewVersionID, annotations.created.VersionID)
	require.Equal(t, types.JSON(`{"path":"/title"}`), annotations.created.Anchor)
	require.Equal(t, "Needs evidence", annotations.created.Body)
	require.Equal(t, http.StatusOK, resolve.Code, resolve.Body.String())
	require.Equal(t, productionReviewAnnotationID, annotations.resolved.id)
	require.Equal(t, types.ProductionAnnotationResolved, annotations.resolved.status)
	require.Equal(t, http.StatusCreated, submit.Code, submit.Body.String())
	require.Equal(t, productionReviewDocumentID, reviews.submittedDocumentID)
	require.Equal(t, productionReviewVersionID, reviews.submittedVersionID)
	require.Equal(t, http.StatusOK, get.Code, get.Body.String())
	require.Equal(t, productionReviewRequestID, reviews.getID)
	require.Equal(t, http.StatusOK, decide.Code, decide.Body.String())
	require.Equal(t, productionReviewStepID, reviews.decidedStepID)
	require.Equal(t, types.ProductionReviewDecision(types.ProductionReviewApproved), reviews.decision)
	require.Equal(t, "checked", reviews.comment)
	require.Equal(t, http.StatusOK, reject.Code, reject.Body.String())
	require.Equal(t, productionReviewRequestID, reviews.rejectedReviewID)
	require.Equal(t, "rejected by admin", reviews.rejectedReason)
	require.Equal(t, http.StatusOK, cancel.Code, cancel.Body.String())
	require.Equal(t, productionReviewRequestID, reviews.cancelledReviewID)
	require.Equal(t, "cancelled by owner", reviews.cancelledReason)
}

func TestProductionReviewHandlerRejectsInvalidAnnotationListPaginationAndFilters(t *testing.T) {
	annotations := &productionReviewAnnotationServiceStub{}
	engine := productionReviewHandlerEngine(annotations, &productionReviewServiceStub{})
	for _, query := range []string{
		"?page=0", "?page=9223372036854775807&page_size=100", "?page_size=101", "?page_size=not-a-number", "?version_id=not-a-uuid",
		"?page=" + strconv.Itoa(types.ProductionAnnotationMaxPage+1) + "&page_size=1",
		"?page=" + strconv.Itoa(types.ProductionAnnotationMaxOffset/100+2) + "&page_size=100",
		"?status=unknown", "?annotation_type=unknown", "?severity=unknown",
	} {
		response := productionReviewHandlerRequest(engine, http.MethodGet,
			"/documents/"+productionReviewDocumentID+"/annotations"+query, "")
		require.Equal(t, http.StatusBadRequest, response.Code, query+" "+response.Body.String())
	}
	require.Empty(t, annotations.listed.DocumentID)

	boundary := productionReviewHandlerRequest(engine, http.MethodGet,
		"/documents/"+productionReviewDocumentID+"/annotations?page="+
			strconv.Itoa(types.ProductionAnnotationMaxPage)+"&page_size=1", "")
	require.Equal(t, http.StatusOK, boundary.Code, boundary.Body.String())
	require.Equal(t, types.ProductionAnnotationMaxPage, annotations.listed.Page)
	require.Equal(t, 1, annotations.listed.PageSize)
}

func TestProductionReviewHandlerStrictlyRejectsInvalidScopeAndBodies(t *testing.T) {
	annotations := &productionReviewAnnotationServiceStub{}
	reviews := &productionReviewServiceStub{}
	engine := productionReviewHandlerEngine(annotations, reviews)

	tests := []struct {
		name, method, path, body string
		want                     int
	}{
		{"noncanonical document", http.MethodPost, "/documents/not-a-uuid/annotations", `{"version_id":"` + productionReviewVersionID + `"}`, http.StatusBadRequest},
		{"body document scope", http.MethodPost, "/documents/" + productionReviewDocumentID + "/annotations", `{"document_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","version_id":"` + productionReviewVersionID + `","block_id":"` + productionReviewBlockID + `","annotation_type":"comment","severity":"info","anchor":{},"body":"note"}`, http.StatusBadRequest},
		{"unknown field", http.MethodPost, "/documents/" + productionReviewDocumentID + "/reviews", `{"version_id":"` + productionReviewVersionID + `","extra":true}`, http.StatusBadRequest},
		{"invalid anchor", http.MethodPost, "/documents/" + productionReviewDocumentID + "/annotations", `{"version_id":"` + productionReviewVersionID + `","block_id":"` + productionReviewBlockID + `","annotation_type":"comment","severity":"info","anchor":[],"body":"note"}`, http.StatusUnprocessableEntity},
		{"quality tag missing category", http.MethodPost, "/documents/" + productionReviewDocumentID + "/annotations", `{"version_id":"` + productionReviewVersionID + `","block_id":"` + productionReviewBlockID + `","annotation_type":"quality_tag","severity":"warning","anchor":{},"body":"note"}`, http.StatusBadRequest},
		{"comment carries category", http.MethodPost, "/documents/" + productionReviewDocumentID + "/annotations", `{"version_id":"` + productionReviewVersionID + `","block_id":"` + productionReviewBlockID + `","annotation_type":"comment","quality_tag":"unclear","severity":"info","anchor":{},"body":"note"}`, http.StatusBadRequest},
		{"open status unsupported", http.MethodPut, "/annotations/" + productionReviewAnnotationID + "/status", `{"status":"open"}`, http.StatusBadRequest},
		{"pending decision unsupported", http.MethodPost, "/reviews/" + productionReviewRequestID + "/steps/" + productionReviewStepID + "/decision", `{"decision":"pending"}`, http.StatusBadRequest},
		{"nonapproval needs comment", http.MethodPost, "/reviews/" + productionReviewRequestID + "/steps/" + productionReviewStepID + "/decision", `{"decision":"rejected","comment":" "}`, http.StatusBadRequest},
		{"multiple json values", http.MethodPost, "/documents/" + productionReviewDocumentID + "/reviews", `{"version_id":"` + productionReviewVersionID + `"}{}`, http.StatusBadRequest},
		{"terminal unknown field", http.MethodPost, "/reviews/" + productionReviewRequestID + "/reject", `{"reason":"valid","extra":true}`, http.StatusBadRequest},
		{"terminal empty reason", http.MethodPost, "/reviews/" + productionReviewRequestID + "/cancel", `{"reason":" "}`, http.StatusBadRequest},
		{"terminal invalid review", http.MethodPost, "/reviews/not-a-uuid/reject", `{"reason":"valid"}`, http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := productionReviewHandlerRequest(engine, test.method, test.path, test.body)
			require.Equal(t, test.want, response.Code, response.Body.String())
		})
	}
	require.Empty(t, annotations.created.DocumentID)
	require.Empty(t, annotations.resolved.id)
	require.Empty(t, reviews.submittedDocumentID)
	require.Empty(t, reviews.decidedStepID)
	require.Empty(t, reviews.rejectedReviewID)
	require.Empty(t, reviews.cancelledReviewID)
}

func TestProductionReviewHandlerRejectsOversizedFieldsBeforeService(t *testing.T) {
	annotations := &productionReviewAnnotationServiceStub{}
	reviews := &productionReviewServiceStub{}
	engine := productionReviewHandlerEngine(annotations, reviews)

	annotation := productionReviewHandlerRequest(engine, http.MethodPost, "/documents/"+productionReviewDocumentID+"/annotations",
		`{"version_id":"`+productionReviewVersionID+`","block_id":"`+productionReviewBlockID+`","annotation_type":"comment","severity":"info","anchor":{},"body":"`+strings.Repeat("x", 20001)+`"}`)
	decision := productionReviewHandlerRequest(engine, http.MethodPost, "/reviews/"+productionReviewRequestID+"/steps/"+productionReviewStepID+"/decision",
		`{"decision":"approved","comment":"`+strings.Repeat("x", 5001)+`"}`)
	terminal := productionReviewHandlerRequest(engine, http.MethodPost, "/reviews/"+productionReviewRequestID+"/reject",
		`{"reason":"`+strings.Repeat("x", 5001)+`"}`)

	require.Equal(t, http.StatusBadRequest, annotation.Code)
	require.Equal(t, http.StatusBadRequest, decision.Code)
	require.Equal(t, http.StatusBadRequest, terminal.Code)
	require.Empty(t, annotations.created.DocumentID)
	require.Empty(t, reviews.decidedStepID)
	require.Empty(t, reviews.rejectedReviewID)
}

func TestProductionReviewHandlerMapsTerminalConflictAndForbidden(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "conflict", err: types.ErrProductionConflict, want: http.StatusConflict},
		{name: "forbidden", err: types.ErrProductionForbidden, want: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			reviews := &productionReviewServiceStub{err: test.err}
			engine := productionReviewHandlerEngine(&productionReviewAnnotationServiceStub{}, reviews)
			response := productionReviewHandlerRequest(engine, http.MethodPost,
				"/reviews/"+productionReviewRequestID+"/reject", `{"reason":"governed reason"}`)
			require.Equal(t, test.want, response.Code, response.Body.String())
			require.Equal(t, productionReviewRequestID, reviews.rejectedReviewID)
		})
	}
}

func TestProductionReviewHandlerRequiresStepToBelongToPathReview(t *testing.T) {
	reviews := &productionReviewServiceStub{review: &types.ProductionReviewRequest{
		ID:    productionReviewRequestID,
		Steps: []*types.ProductionReviewStep{{ID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", ReviewRequestID: productionReviewRequestID}},
	}}
	engine := productionReviewHandlerEngine(&productionReviewAnnotationServiceStub{}, reviews)

	response := productionReviewHandlerRequest(engine, http.MethodPost, "/reviews/"+productionReviewRequestID+"/steps/"+productionReviewStepID+"/decision", `{"decision":"approved"}`)

	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	require.Empty(t, reviews.decidedStepID)
}

func TestProductionReviewHandlerMapsGovernanceErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"stale scope", types.ErrProductionReviewScopeInvalid, http.StatusConflict},
		{"duplicate decision", types.ErrProductionReviewLifecycle, http.StatusConflict},
		{"obsolete review", types.ErrProductionReviewImmutable, http.StatusConflict},
		{"digest mismatch", types.ErrProductionContentDigestMismatch, http.StatusConflict},
		{"blocking annotation", types.ErrProductionBlockingAnnotations, http.StatusUnprocessableEntity},
		{"invalid anchor", types.ErrProductionAnnotationAnchorInvalid, http.StatusUnprocessableEntity},
		{"forbidden", types.ErrProductionForbidden, http.StatusForbidden},
		{"not found", gorm.ErrRecordNotFound, http.StatusNotFound},
		{"unexpected", errors.New("database password secret"), http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			annotations := &productionReviewAnnotationServiceStub{err: test.err}
			engine := productionReviewHandlerEngine(annotations, &productionReviewServiceStub{})
			response := productionReviewHandlerRequest(engine, http.MethodPost, "/documents/"+productionReviewDocumentID+"/annotations",
				`{"version_id":"`+productionReviewVersionID+`","block_id":"`+productionReviewBlockID+`","annotation_type":"comment","severity":"info","anchor":{},"body":"note"}`)
			require.Equal(t, test.want, response.Code, response.Body.String())
			require.NotContains(t, response.Body.String(), "database password secret")
		})
	}
}
