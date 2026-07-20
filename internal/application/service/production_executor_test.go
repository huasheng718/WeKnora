package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type productionExecutorRepoStub struct {
	interfaces.ProductionRunRepository
	call *types.ProductionToolCall
}

func (r *productionExecutorRepoStub) TransitionToolCall(_ context.Context, _ uint64, _, _ string, expected interfaces.ProductionToolCallCAS, to types.ProductionToolCallStatus, _ interfaces.ProductionToolCallPatch) (bool, error) {
	if r.call.Status != expected.Status {
		return false, nil
	}
	r.call.Status = to
	return true, nil
}

type productionExecutorAdapterStub struct {
	calls int
}

func (a *productionExecutorAdapterStub) Plan(context.Context, *types.ProductionToolCall) (*ProductionToolPlan, error) {
	return nil, nil
}
func (a *productionExecutorAdapterStub) Execute(_ context.Context, call *types.ProductionToolCall) (*ProductionToolResult, error) {
	a.calls++
	return &ProductionToolResult{
		ToolCallID: call.ID, ResponseSnapshot: types.JSON(`{"ok":true}`),
		Evidence: &types.ProductionEvidenceSnapshot{ID: mcpAdapterSourceID, SourceItemID: mcpAdapterSourceID},
	}, nil
}

type productionExecutorWriterStub struct {
	calls int
}

func (w *productionExecutorWriterStub) Write(context.Context, *types.ProductionRun) (*types.ProductionDocumentVersion, error) {
	w.calls++
	return &types.ProductionDocumentVersion{ID: mcpAdapterSourceID}, nil
}

func TestProductionStepExecutorRunsPersistedMCPCallAndWriter(t *testing.T) {
	call := &types.ProductionToolCall{
		ID: mcpAdapterCallID, RunID: mcpAdapterRunID, TenantID: 7, ProjectID: mcpAdapterProjectID,
		DocumentID: mcpAdapterDocumentID, SourceSetID: mcpAdapterSourceSetID,
		Attempt: 1, CurrentStep: 0, ProviderType: types.ProductionToolProviderMCP,
		Status: types.ProductionToolCallApproved, ApprovalStatus: types.ProductionToolApprovalApproved,
	}
	repo := &productionExecutorRepoStub{call: call}
	mcpAdapter := &productionExecutorAdapterStub{}
	writer := &productionExecutorWriterStub{}
	executor := newProductionStepExecutor(repo, &productionExecutorAdapterStub{}, mcpAdapter, &productionExecutorAdapterStub{}, writer)
	run := &types.ProductionRun{
		ID: call.RunID, TenantID: call.TenantID, ProjectID: call.ProjectID, DocumentID: call.DocumentID,
		SourceSetID: call.SourceSetID, Attempt: call.Attempt, CurrentStep: call.CurrentStep,
		RunType: types.ProductionRunCollect, Status: types.ProductionRunRunning,
	}

	result, err := executor.ExecuteStep(context.Background(), run, []*types.ProductionToolCall{call})
	require.NoError(t, err)
	require.Equal(t, types.ProductionToolCallExecuting, call.Status)
	require.Equal(t, call.ID, result.ToolCallResult.ToolCallID)
	require.Equal(t, 1, mcpAdapter.calls)

	run.RunType = types.ProductionRunWrite
	result, err = executor.ExecuteStep(context.Background(), run, nil)
	require.NoError(t, err)
	require.True(t, result.Complete)
	require.Equal(t, mcpAdapterSourceID, *result.OutputVersionID)
	require.Equal(t, 1, writer.calls)
}
