package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

const productionWorkbenchEvidenceID = "66666666-6666-4666-8666-666666666666"

type productionWorkbenchRunRepoStub struct {
	interfaces.ProductionRunRepository
	run           *types.ProductionRun
	runs          []*types.ProductionRun
	toolCalls     []*types.ProductionToolCall
	listRunCalls  int
	listToolCalls int
}

func (r *productionWorkbenchRunRepoStub) Get(context.Context, uint64, string) (*types.ProductionRun, error) {
	return r.run, nil
}

func (r *productionWorkbenchRunRepoStub) ListDocumentRuns(context.Context, uint64, string, int) ([]*types.ProductionRun, error) {
	r.listRunCalls++
	return r.runs, nil
}

func (r *productionWorkbenchRunRepoStub) ListToolCalls(context.Context, uint64, string) ([]*types.ProductionToolCall, error) {
	r.listToolCalls++
	return r.toolCalls, nil
}

type productionWorkbenchDocumentRepoStub struct {
	interfaces.ProductionDocumentRepository
	document *types.ProductionDocument
}

func (r productionWorkbenchDocumentRepoStub) GetDocument(context.Context, uint64, string) (*types.ProductionDocument, error) {
	return r.document, nil
}

func productionWorkbenchReadContext() context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, productionRunDecisionActor)
	ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleViewer)
	return ctx
}

func TestProductionRunServiceListsAuthorizedDocumentRunsAndToolCalls(t *testing.T) {
	run := &types.ProductionRun{
		ID: mcpAdapterRunID, TenantID: 7, ProjectID: mcpAdapterProjectID,
		DocumentID: mcpAdapterDocumentID, SourceSetID: mcpAdapterSourceSetID,
		Status: types.ProductionRunWaitingApproval, CreatedAt: time.Now().UTC(),
	}
	repo := &productionWorkbenchRunRepoStub{
		run: run, runs: []*types.ProductionRun{run},
		toolCalls: []*types.ProductionToolCall{{ID: mcpAdapterCallID, RunID: run.ID, TenantID: 7}},
	}
	documents := productionWorkbenchDocumentRepoStub{document: &types.ProductionDocument{
		ID: string(mcpAdapterDocumentID), TenantID: 7, ProjectID: mcpAdapterProjectID,
	}}
	service := NewProductionRunService(repo, documents, nil, nil, nil, productionRunServiceAuthorizerStub{}, nil, nil, nil)

	runs, err := service.ListDocumentRuns(productionWorkbenchReadContext(), string(mcpAdapterDocumentID))
	require.NoError(t, err)
	require.Len(t, runs, 1)
	calls, err := service.ListToolCalls(productionWorkbenchReadContext(), run.ID)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	require.Equal(t, 1, repo.listRunCalls)
	require.Equal(t, 1, repo.listToolCalls)
}

func TestProductionRunServiceDoesNotListBeforeProjectAuthorization(t *testing.T) {
	run := &types.ProductionRun{
		ID: mcpAdapterRunID, TenantID: 7, ProjectID: mcpAdapterProjectID,
		DocumentID: mcpAdapterDocumentID, SourceSetID: mcpAdapterSourceSetID,
	}
	repo := &productionWorkbenchRunRepoStub{run: run}
	documents := productionWorkbenchDocumentRepoStub{document: &types.ProductionDocument{
		ID: string(mcpAdapterDocumentID), TenantID: 7, ProjectID: mcpAdapterProjectID,
	}}
	service := NewProductionRunService(repo, documents, nil, nil, nil,
		productionRunServiceAuthorizerStub{err: types.ErrProductionForbidden}, nil, nil, nil)

	runs, err := service.ListDocumentRuns(productionWorkbenchReadContext(), string(mcpAdapterDocumentID))
	require.Nil(t, runs)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	calls, err := service.ListToolCalls(productionWorkbenchReadContext(), run.ID)
	require.Nil(t, calls)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Zero(t, repo.listRunCalls)
	require.Zero(t, repo.listToolCalls)
}

func TestProductionSourceServiceListsAcceptedEvidenceForAuthorizedReaders(t *testing.T) {
	svc, repo, _, authorizer, _ := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)
	require.NoError(t, repo.CreateEvidence(sourceServiceContext(7), 7, serviceItemID, &types.ProductionEvidenceSnapshot{
		ID: productionWorkbenchEvidenceID, SnapshotType: types.ProductionEvidenceSnapshotText,
		InlineContent: types.JSON(`"evidence"`), ContentDigest: strings.Repeat("a", 64),
		RedactionMetadata: types.JSON(`{}`),
	}))
	require.NoError(t, repo.Freeze(sourceServiceContext(7), 7, serviceSetID))

	evidence, err := svc.ListEvidence(sourceServiceContext(7), serviceSetID)
	require.NoError(t, err)
	require.Len(t, evidence, 1)
	require.Equal(t, productionWorkbenchEvidenceID, evidence[0].ID)
	require.Equal(t, allProductionProjectRoles, authorizer.roles)
}
