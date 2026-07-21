package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const productionReleaseTargetID = "77777777-7777-4777-8777-777777777777"

type productionReleaseServiceStub struct {
	prepared struct {
		documentID string
		versionID  string
		kbIDs      []string
	}
	targetID     string
	expectedLock int
	prepareErr   error
	targetErr    error
	activateErr  error
	retryErr     error
	rollbackErr  error
}

func (s *productionReleaseServiceStub) Prepare(_ context.Context, documentID, versionID string, kbIDs []string) (*types.ProductionRelease, error) {
	s.prepared.documentID, s.prepared.versionID, s.prepared.kbIDs = documentID, versionID, kbIDs
	if s.prepareErr != nil {
		return nil, s.prepareErr
	}
	return &types.ProductionRelease{ID: productionProjectID, Targets: []*types.ProductionReleaseTarget{{ID: productionReleaseTargetID}}}, nil
}

func (s *productionReleaseServiceStub) GetTarget(_ context.Context, targetID string) (*types.ProductionReleaseTarget, error) {
	s.targetID = targetID
	if s.targetErr != nil {
		return nil, s.targetErr
	}
	return &types.ProductionReleaseTarget{ID: targetID, Status: types.ReleaseTargetReady}, nil
}

func (s *productionReleaseServiceStub) Activate(_ context.Context, targetID string, expectedLock int) error {
	s.targetID, s.expectedLock = targetID, expectedLock
	return s.activateErr
}

func (s *productionReleaseServiceStub) Retry(_ context.Context, targetID string) error {
	s.targetID = targetID
	return s.retryErr
}

func (s *productionReleaseServiceStub) Rollback(_ context.Context, targetID string, expectedLock int) error {
	s.targetID, s.expectedLock = targetID, expectedLock
	return s.rollbackErr
}

func TestProductionReleaseHandlerCreatesAndOperatesOnTargets(t *testing.T) {
	service := &productionReleaseServiceStub{}
	h := NewProductionReleaseHandler(service)

	create := performProductionHandlerRequest(
		http.MethodPost, "/production/documents/:id/releases",
		"/production/documents/"+productionProjectID+"/releases",
		`{"version_id":"`+productionDocumentTypeID+`","target_knowledge_base_ids":["`+productionReviewerID+`"]}`,
		h.Create,
	)
	require.Equal(t, http.StatusCreated, create.Code, create.Body.String())
	require.Equal(t, productionProjectID, service.prepared.documentID)
	require.Equal(t, productionDocumentTypeID, service.prepared.versionID)
	require.Equal(t, []string{productionReviewerID}, service.prepared.kbIDs)

	status := performProductionHandlerRequest(
		http.MethodGet, "/production/release-targets/:id", "/production/release-targets/"+productionReleaseTargetID, "", h.GetTarget,
	)
	require.Equal(t, http.StatusOK, status.Code, status.Body.String())
	require.Equal(t, productionReleaseTargetID, service.targetID)

	activate := performProductionHandlerRequest(
		http.MethodPost, "/production/release-targets/:id/activate", "/production/release-targets/"+productionReleaseTargetID+"/activate", `{"expected_lock":0}`, h.Activate,
	)
	require.Equal(t, http.StatusOK, activate.Code, activate.Body.String())
	require.Equal(t, productionReleaseTargetID, service.targetID)
	require.Zero(t, service.expectedLock)

	retry := performProductionHandlerRequest(
		http.MethodPost, "/production/release-targets/:id/retry", "/production/release-targets/"+productionReleaseTargetID+"/retry", "", h.Retry,
	)
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())

	rollback := performProductionHandlerRequest(
		http.MethodPost, "/production/release-targets/:id/rollback", "/production/release-targets/"+productionReleaseTargetID+"/rollback", `{"expected_lock":2}`, h.Rollback,
	)
	require.Equal(t, http.StatusOK, rollback.Code, rollback.Body.String())
	require.Equal(t, 2, service.expectedLock)
}

func TestProductionReleaseHandlerRejectsInvalidRequestsAndMapsGovernanceErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{name: "publisher forbidden", err: types.ErrProductionForbidden, want: http.StatusForbidden},
		{name: "lifecycle conflict", err: types.ErrProductionReleaseLifecycle, want: http.StatusConflict},
		{name: "head conflict", err: types.ErrProductionProjectionConflict, want: http.StatusConflict},
		{name: "invalid release", err: types.ErrProductionReleaseInvalid, want: http.StatusBadRequest},
		{name: "missing target", err: gorm.ErrRecordNotFound, want: http.StatusNotFound},
		{name: "unexpected", err: errors.New("release database secret"), want: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &productionReleaseServiceStub{activateErr: tc.err}
			h := NewProductionReleaseHandler(service)
			response := performProductionHandlerRequest(
				http.MethodPost, "/production/release-targets/:id/activate", "/production/release-targets/"+productionReleaseTargetID+"/activate", `{"expected_lock":1}`, h.Activate,
			)
			require.Equal(t, tc.want, response.Code, response.Body.String())
			require.NotContains(t, response.Body.String(), "release database secret")
		})
	}

	service := &productionReleaseServiceStub{}
	h := NewProductionReleaseHandler(service)
	invalid := performProductionHandlerRequest(
		http.MethodPost, "/production/release-targets/:id/activate", "/production/release-targets/"+productionReleaseTargetID+"/activate", `{}`, h.Activate,
	)
	require.Equal(t, http.StatusBadRequest, invalid.Code)
}

var _ ProductionReleaseService = (*productionReleaseServiceStub)(nil)
