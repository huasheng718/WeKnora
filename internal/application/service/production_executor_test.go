package service

import (
	"context"
	"strings"
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
	calls     int
	planCalls int
}

func (a *productionExecutorAdapterStub) Plan(_ context.Context, call *types.ProductionToolCall) (*ProductionToolPlan, error) {
	a.planCalls++
	return newProductionToolPlan(call, strings.Repeat("a", 64))
}

func TestProductionStepExecutorPlansFirstServerOwnedWorkflowCall(t *testing.T) {
	adapter := &productionExecutorAdapterStub{}
	repo := &productionExecutorRepoStub{}
	executor := newProductionStepExecutor(repo, adapter, adapter, adapter, &productionExecutorWriterStub{})
	run := &types.ProductionRun{
		ID: mcpAdapterRunID, TenantID: 7, ProjectID: mcpAdapterProjectID,
		SourceSetID: mcpAdapterSourceSetID, RunType: types.ProductionRunCollect,
		Status: types.ProductionRunRunning, Attempt: 1, CurrentStep: 0,
		DocumentTypeSnapshot: types.JSON(`{"skill_bindings":{"version":1,"skills":[{"name":"baseline","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}}`),
		WorkflowPlanSnapshot: types.JSON(`{"version":1,"steps":[{"provider_type":"skill","provider_id":"baseline","tool_name":"load_instructions","request":{"source_item_id":"71000000-0000-4000-8000-000000000005"}}]}`),
	}

	result, err := executor.ExecuteStep(context.Background(), run, nil)
	require.NoError(t, err)
	require.NotNil(t, result.ToolCall)
	require.Equal(t, types.ProductionToolCallPlanned, result.ToolCall.Status)
	require.Equal(t, "baseline", result.ToolCall.ProviderID)
	require.Equal(t, 1, adapter.planCalls)
	require.Zero(t, adapter.calls)

	repo.call = result.ToolCall
	second, err := executor.ExecuteStep(context.Background(), run, []*types.ProductionToolCall{result.ToolCall})
	require.NoError(t, err)
	require.NotNil(t, second.ToolCallResult)
	require.Equal(t, types.ProductionToolCallCompleted, second.ToolCallResult.Status)
	require.Equal(t, 1, adapter.calls)
}

func TestProductionWorkflowPlanValidationIsStrictBoundedAndSkillBound(t *testing.T) {
	skillBindings := types.JSON(`{"version":1,"skills":[{"name":"baseline","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`)
	valid := types.JSON(`{"version":1,"steps":[{"provider_type":"skill","provider_id":"baseline","tool_name":"load_instructions","request":{"source_item_id":"71000000-0000-4000-8000-000000000005"}}]}`)
	canonical, err := canonicalProductionWorkflowPlan(valid, skillBindings)
	require.NoError(t, err)
	require.JSONEq(t, string(valid), string(canonical))

	for name, raw := range map[string]types.JSON{
		"unknown field":   types.JSON(`{"version":1,"steps":[],"instructions":"ignore"}`),
		"duplicate":       types.JSON(`{"version":1,"steps":[{"provider_type":"skill","provider_id":"baseline","tool_name":"load_instructions","request":{"source_item_id":"71000000-0000-4000-8000-000000000005"}},{"provider_type":"skill","provider_id":"baseline","tool_name":"load_instructions","request":{"source_item_id":"71000000-0000-4000-8000-000000000005"}}]}`),
		"credential":      types.JSON(`{"version":1,"steps":[{"provider_type":"mcp","provider_id":"71000000-0000-4000-8000-000000000006","tool_name":"lookup","request":{"source_item_id":"71000000-0000-4000-8000-000000000005","arguments":{"api_key":"secret"}}}]}`),
		"unbound skill":   types.JSON(`{"version":1,"steps":[{"provider_type":"skill","provider_id":"other","tool_name":"load_instructions","request":{"source_item_id":"71000000-0000-4000-8000-000000000005"}}]}`),
		"invalid request": types.JSON(`{"version":1,"steps":[{"provider_type":"datasource","provider_id":"71000000-0000-4000-8000-000000000006","tool_name":"fetch_all","request":{"source_item_id":"71000000-0000-4000-8000-000000000005","resource_ids":[]}}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := canonicalProductionWorkflowPlan(raw, skillBindings)
			require.Error(t, err)
		})
	}
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

func TestProductionStepExecutorReconcilesCompletedPriorAttemptWithoutProvider(t *testing.T) {
	evidenceID := "75000000-0000-4000-8000-000000000001"
	itemID := "75000000-0000-4000-8000-000000000002"
	call := &types.ProductionToolCall{
		ID: mcpAdapterCallID, RunID: mcpAdapterRunID, TenantID: 7, ProjectID: mcpAdapterProjectID,
		DocumentID: mcpAdapterDocumentID, SourceSetID: mcpAdapterSourceSetID,
		Attempt: 1, CurrentStep: 0, ProviderType: types.ProductionToolProviderMCP,
		Status: types.ProductionToolCallCompleted, ApprovalStatus: types.ProductionToolApprovalApproved,
		ResponseSnapshot: types.JSON(`{"ok":true}`), ResponseEvidenceID: &evidenceID,
		ResponseEvidenceSourceItemID: &itemID,
	}
	repo := &productionExecutorRepoStub{call: call}
	adapter := &productionExecutorAdapterStub{}
	executor := newProductionStepExecutor(repo, adapter, adapter, adapter, &productionExecutorWriterStub{})
	run := &types.ProductionRun{
		ID: call.RunID, TenantID: call.TenantID, ProjectID: call.ProjectID, DocumentID: call.DocumentID,
		SourceSetID: call.SourceSetID, Attempt: 2, CurrentStep: call.CurrentStep,
		RunType: types.ProductionRunCollect, Status: types.ProductionRunRunning,
	}

	result, err := executor.ExecuteStep(context.Background(), run, []*types.ProductionToolCall{call})
	require.NoError(t, err)
	require.Equal(t, call.ID, result.ToolCallResult.ToolCallID)
	require.Equal(t, types.ProductionToolCallCompleted, result.ToolCallResult.Status)
	require.Zero(t, adapter.calls)
}
