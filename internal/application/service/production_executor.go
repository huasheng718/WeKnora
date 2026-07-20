package service

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type productionDocumentWriter interface {
	Write(ctx context.Context, run *types.ProductionRun) (*types.ProductionDocumentVersion, error)
}

// ProductionStepExecutor composes the governed Task 4 provider adapters and
// Task 5 writer behind the restart-safe Task 3 state machine.
type ProductionExecutor struct {
	runs       interfaces.ProductionRunRepository
	skill      ProductionToolAdapter
	mcp        ProductionToolAdapter
	datasource ProductionToolAdapter
	writer     productionDocumentWriter
	now        func() time.Time
}

func NewProductionStepExecutor(
	runs interfaces.ProductionRunRepository,
	skill *ProductionSkillAdapter,
	mcpAdapter *ProductionMCPAdapter,
	datasource *ProductionDataSourceAdapter,
	writer *ProductionWriter,
) *ProductionExecutor {
	return newProductionStepExecutor(runs, skill, mcpAdapter, datasource, writer)
}

func newProductionStepExecutor(
	runs interfaces.ProductionRunRepository,
	skill ProductionToolAdapter,
	mcpAdapter ProductionToolAdapter,
	datasource ProductionToolAdapter,
	writer productionDocumentWriter,
) *ProductionExecutor {
	return &ProductionExecutor{
		runs: runs, skill: skill, mcp: mcpAdapter, datasource: datasource, writer: writer, now: time.Now,
	}
}

func (e *ProductionExecutor) ExecuteStep(
	ctx context.Context,
	run *types.ProductionRun,
	executable []*types.ProductionToolCall,
) (ProductionStepResult, error) {
	if e == nil || e.runs == nil || e.skill == nil || e.mcp == nil || e.datasource == nil || e.writer == nil || e.now == nil {
		return ProductionStepResult{}, errors.New("production executor dependencies are required")
	}
	if run == nil || run.Status != types.ProductionRunRunning || run.Attempt < 1 || run.CurrentStep < 0 {
		return ProductionStepResult{}, errors.New("production executor run fence is invalid")
	}
	if len(executable) > 1 {
		return ProductionStepResult{}, errors.New("production run step has multiple executable tool calls")
	}
	if len(executable) == 1 {
		return e.executeTool(ctx, run, executable[0])
	}
	switch run.RunType {
	case types.ProductionRunWrite, types.ProductionRunRewrite:
		writerRun := *run
		writerRun.RunType = types.ProductionRunWrite
		version, err := e.writer.Write(ctx, &writerRun)
		if err != nil {
			return ProductionStepResult{}, err
		}
		if version == nil || !canonicalProductionUUID(version.ID) {
			return ProductionStepResult{}, errors.New("production writer returned an invalid version")
		}
		return ProductionStepResult{
			StatePayload: run.StatePayload, OutputVersionID: &version.ID, Complete: true,
		}, nil
	case types.ProductionRunCollect, types.ProductionRunValidate:
		return ProductionStepResult{StatePayload: run.StatePayload, Complete: true}, nil
	default:
		return ProductionStepResult{}, errors.New("unsupported production run type")
	}
}

func (e *ProductionExecutor) executeTool(
	ctx context.Context,
	run *types.ProductionRun,
	call *types.ProductionToolCall,
) (ProductionStepResult, error) {
	if call == nil || call.RunID != run.ID || call.TenantID != run.TenantID || call.ProjectID != run.ProjectID ||
		call.DocumentID != run.DocumentID || call.SourceSetID != run.SourceSetID ||
		call.Attempt != run.Attempt || call.CurrentStep != run.CurrentStep ||
		(call.Status != types.ProductionToolCallApproved && call.Status != types.ProductionToolCallExecuting) {
		return ProductionStepResult{}, errors.New("production tool call does not match the claimed run step")
	}
	if call.Status == types.ProductionToolCallApproved {
		startedAt := e.now().UTC()
		changed, err := e.runs.TransitionToolCall(
			ctx, run.TenantID, run.ID, call.ID,
			interfaces.ProductionToolCallCAS{Status: call.Status, Attempt: call.Attempt, CurrentStep: call.CurrentStep},
			types.ProductionToolCallExecuting, interfaces.ProductionToolCallPatch{StartedAt: &startedAt},
		)
		if err != nil {
			return ProductionStepResult{}, err
		}
		if !changed {
			return ProductionStepResult{}, types.ErrProductionConflict
		}
		call.Status = types.ProductionToolCallExecuting
	}
	adapter := e.adapter(call.ProviderType)
	if adapter == nil {
		return ProductionStepResult{}, errors.New("production tool provider is unsupported")
	}
	result, err := adapter.Execute(ctx, call)
	if err != nil {
		return ProductionStepResult{}, err
	}
	if result == nil || result.Evidence == nil || result.ToolCallID != call.ID ||
		!canonicalProductionUUID(result.Evidence.ID) || !canonicalProductionUUID(result.Evidence.SourceItemID) {
		return ProductionStepResult{}, errors.New("production tool provider returned invalid evidence")
	}
	return ProductionStepResult{
		StatePayload: run.StatePayload,
		ToolCallResult: &ProductionToolCallResult{
			ToolCallID: call.ID, Status: types.ProductionToolCallCompleted,
			ResponseSnapshot: result.ResponseSnapshot, ResponseEvidenceID: result.Evidence.ID,
			ResponseEvidenceSourceItemID: result.Evidence.SourceItemID,
		},
	}, nil
}

func (e *ProductionExecutor) adapter(provider types.ProductionToolProviderType) ProductionToolAdapter {
	switch provider {
	case types.ProductionToolProviderSkill:
		return e.skill
	case types.ProductionToolProviderMCP:
		return e.mcp
	case types.ProductionToolProviderDatasource:
		return e.datasource
	default:
		return nil
	}
}

var _ ProductionStepExecutor = (*ProductionExecutor)(nil)
