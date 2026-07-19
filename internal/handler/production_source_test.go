package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	productionSourceSetID  = "44444444-4444-4444-8444-444444444444"
	productionSourceItemID = "55555555-5555-4555-8555-555555555555"
)

type productionSourceServiceStub struct {
	created interfaces.CreateProductionSourceSetInput
	decided struct {
		itemID   string
		decision types.ProductionSourceItemStatus
	}
	frozenID string
	err      error
}

func (s *productionSourceServiceStub) CreateSet(_ context.Context, input interfaces.CreateProductionSourceSetInput) (*types.ProductionSourceSet, error) {
	s.created = input
	if s.err != nil {
		return nil, s.err
	}
	return &types.ProductionSourceSet{
		ID: productionSourceSetID, TenantID: 7, ProjectID: input.ProjectID,
		DocumentTypeID: input.DocumentTypeID, Status: types.ProductionSourceSetCollecting,
	}, nil
}

func (*productionSourceServiceStub) AddItem(context.Context, string, interfaces.CreateProductionSourceItemInput) (*types.ProductionSourceItem, error) {
	panic("unexpected AddItem")
}

func (s *productionSourceServiceStub) DecideItem(_ context.Context, itemID string, decision types.ProductionSourceItemStatus) error {
	s.decided.itemID, s.decided.decision = itemID, decision
	return s.err
}

func (*productionSourceServiceStub) AttachEvidence(context.Context, string, interfaces.CreateEvidenceSnapshotInput) (*types.ProductionEvidenceSnapshot, error) {
	panic("unexpected AttachEvidence")
}

func (*productionSourceServiceStub) GetEvidence(context.Context, string) (*types.ProductionEvidenceSnapshot, *types.ProductionSourceItem, *types.ProductionSourceSet, error) {
	panic("unexpected GetEvidence")
}

func (s *productionSourceServiceStub) Freeze(_ context.Context, sourceSetID string) error {
	s.frozenID = sourceSetID
	return s.err
}

func TestProductionSourceHandlerCreatesDecidesAndFreezes(t *testing.T) {
	service := &productionSourceServiceStub{}
	h := NewProductionSourceHandler(service)

	create := performProductionHandlerRequest(
		http.MethodPost,
		"/production/projects/:id/source-sets",
		"/production/projects/"+productionProjectID+"/source-sets",
		`{"document_type_id":"`+productionDocumentTypeID+`","time_range_start":"2026-07-01T00:00:00Z","time_range_end":"2026-07-18T00:00:00Z"}`,
		h.CreateSet,
	)
	decide := performProductionHandlerRequest(
		http.MethodPut,
		"/production/source-items/:id/decision",
		"/production/source-items/"+productionSourceItemID+"/decision",
		`{"decision":"accepted"}`,
		h.DecideItem,
	)
	freeze := performProductionHandlerRequest(
		http.MethodPost,
		"/production/source-sets/:id/freeze",
		"/production/source-sets/"+productionSourceSetID+"/freeze",
		"",
		h.Freeze,
	)

	require.Equal(t, http.StatusCreated, create.Code)
	require.Equal(t, productionProjectID, service.created.ProjectID)
	require.Equal(t, productionDocumentTypeID, service.created.DocumentTypeID)
	require.Equal(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), *service.created.TimeRangeStart)
	require.Equal(t, http.StatusOK, decide.Code)
	require.Equal(t, productionSourceItemID, service.decided.itemID)
	require.Equal(t, types.ProductionSourceItemAccepted, service.decided.decision)
	require.Equal(t, http.StatusOK, freeze.Code)
	require.Equal(t, productionSourceSetID, service.frozenID)
}

func TestProductionSourceHandlerRejectsCanonicalUUIDAndBodyViolations(t *testing.T) {
	service := &productionSourceServiceStub{}
	h := NewProductionSourceHandler(service)
	upperProjectID := strings.ToUpper("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")

	responses := []*struct {
		name     string
		response int
	}{
		{"noncanonical project id", performProductionHandlerRequest(http.MethodPost, "/production/projects/:id/source-sets", "/production/projects/"+upperProjectID+"/source-sets", `{"document_type_id":"`+productionDocumentTypeID+`"}`, h.CreateSet).Code},
		{"invalid document type id", performProductionHandlerRequest(http.MethodPost, "/production/projects/:id/source-sets", "/production/projects/"+productionProjectID+"/source-sets", `{"document_type_id":"not-a-uuid"}`, h.CreateSet).Code},
		{"reversed time range", performProductionHandlerRequest(http.MethodPost, "/production/projects/:id/source-sets", "/production/projects/"+productionProjectID+"/source-sets", `{"document_type_id":"`+productionDocumentTypeID+`","time_range_start":"2026-07-19T00:00:00Z","time_range_end":"2026-07-18T00:00:00Z"}`, h.CreateSet).Code},
		{"candidate is not a decision", performProductionHandlerRequest(http.MethodPut, "/production/source-items/:id/decision", "/production/source-items/"+productionSourceItemID+"/decision", `{"decision":"candidate"}`, h.DecideItem).Code},
		{"malformed freeze id", performProductionHandlerRequest(http.MethodPost, "/production/source-sets/:id/freeze", "/production/source-sets/not-a-uuid/freeze", "", h.Freeze).Code},
	}
	for _, response := range responses {
		t.Run(response.name, func(t *testing.T) { require.Equal(t, http.StatusBadRequest, response.response) })
	}
	require.Empty(t, service.created.ProjectID)
	require.Empty(t, service.decided.itemID)
	require.Empty(t, service.frozenID)
}

func TestProductionSourceHandlerMapsLifecycleAuthorizationAndUnexpectedErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "missing evidence", err: types.ErrProductionEvidenceMissing, want: http.StatusConflict},
		{name: "already frozen", err: types.ErrProductionSourceSetFrozen, want: http.StatusConflict},
		{name: "forbidden", err: types.ErrProductionForbidden, want: http.StatusForbidden},
		{name: "not found", err: gorm.ErrRecordNotFound, want: http.StatusNotFound},
		{name: "unexpected", err: errors.New("database password secret"), want: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := NewProductionSourceHandler(&productionSourceServiceStub{err: test.err})
			response := performProductionHandlerRequest(
				http.MethodPost, "/production/source-sets/:id/freeze",
				"/production/source-sets/"+productionSourceSetID+"/freeze", "", h.Freeze,
			)
			require.Equal(t, test.want, response.Code)
			require.NotContains(t, response.Body.String(), "database password secret")
		})
	}
}

var _ interfaces.ProductionSourceService = (*productionSourceServiceStub)(nil)
