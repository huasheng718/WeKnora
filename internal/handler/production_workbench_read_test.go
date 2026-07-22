package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func (s *productionSourceServiceStub) ListEvidence(context.Context, string) ([]*types.ProductionEvidenceSnapshot, error) {
	return []*types.ProductionEvidenceSnapshot{{ID: productionSourceItemID}}, s.err
}

func (s *productionRunHandlerServiceStub) ListDocumentRuns(context.Context, string) ([]*types.ProductionRun, error) {
	return []*types.ProductionRun{{ID: runHandlerRunID}}, s.err
}

func (s *productionRunHandlerServiceStub) ListToolCalls(context.Context, string) ([]*types.ProductionToolCall, error) {
	return []*types.ProductionToolCall{{ID: runHandlerCallID}}, s.err
}

func TestProductionSourceHandlerListsAcceptedEvidence(t *testing.T) {
	h := NewProductionSourceHandler(&productionSourceServiceStub{})
	response := performProductionHandlerRequest(
		http.MethodGet,
		"/production/source-sets/:id/evidence",
		"/production/source-sets/"+productionSourceSetID+"/evidence",
		"",
		h.ListEvidence,
	)
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), productionSourceItemID)
}

func TestProductionRunHandlerListsDocumentRunsAndToolCalls(t *testing.T) {
	service := &productionRunHandlerServiceStub{}
	h := NewProductionRunHandler(service)
	runs := performProductionHandlerRequest(
		http.MethodGet,
		"/production/documents/:id/runs",
		"/production/documents/"+runHandlerDocumentID+"/runs",
		"",
		h.ListDocumentRuns,
	)
	calls := performProductionHandlerRequest(
		http.MethodGet,
		"/production/runs/:id/tool-calls",
		"/production/runs/"+runHandlerRunID+"/tool-calls",
		"",
		h.ListToolCalls,
	)
	require.Equal(t, http.StatusOK, runs.Code)
	require.Equal(t, http.StatusOK, calls.Code)
	require.Contains(t, runs.Body.String(), runHandlerRunID)
	require.Contains(t, calls.Body.String(), runHandlerCallID)
}
