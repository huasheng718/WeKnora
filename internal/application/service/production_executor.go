package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
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
	workflow, err := productionWorkflowForRun(run)
	if err != nil {
		return ProductionStepResult{}, err
	}
	if run.CurrentStep < len(workflow.Steps) {
		return e.planTool(ctx, run, workflow.Steps[run.CurrentStep])
	}
	if run.CurrentStep > len(workflow.Steps) {
		return ProductionStepResult{}, errors.New("production run current step exceeds workflow plan")
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

func (e *ProductionExecutor) planTool(
	ctx context.Context,
	run *types.ProductionRun,
	step productionWorkflowStep,
) (ProductionStepResult, error) {
	callID := uuid.NewSHA1(uuid.MustParse(run.ID), []byte(fmt.Sprintf("workflow:%d:%s", run.CurrentStep, step.Request))).String()
	request, err := types.CanonicalProductionJSON(types.JSON(step.Request))
	if err != nil {
		return ProductionStepResult{}, err
	}
	call := &types.ProductionToolCall{
		ID: callID, RunID: run.ID, TenantID: run.TenantID, ProjectID: run.ProjectID,
		DocumentID: run.DocumentID, SourceSetID: run.SourceSetID, Attempt: run.Attempt, CurrentStep: run.CurrentStep,
		IdempotencyKey: fmt.Sprintf("workflow:%d", run.CurrentStep), ProviderType: step.ProviderType,
		ProviderID: step.ProviderID, ToolName: step.ToolName, RequestSnapshot: request,
		RequestDigest: productionToolDigest(request), Status: types.ProductionToolCallPlanned,
		ApprovalStatus: types.ProductionToolApprovalNotRequired,
	}
	adapter := e.adapter(step.ProviderType)
	if adapter == nil {
		return ProductionStepResult{}, errors.New("production workflow provider is unsupported")
	}
	planned, err := adapter.Plan(ctx, call)
	if err != nil {
		return ProductionStepResult{}, err
	}
	if planned == nil || planned.ToolCallID != call.ID || planned.ProviderType != call.ProviderType ||
		planned.ProviderID != call.ProviderID || planned.ToolName != call.ToolName ||
		!canonicalProductionSHA256(planned.ProviderDigest) || !canonicalProductionSHA256(planned.RequestDigest) ||
		len(planned.RequestSnapshot) == 0 || productionToolDigest(planned.RequestSnapshot) != planned.RequestDigest {
		return ProductionStepResult{}, errors.New("production adapter returned an invalid plan")
	}
	call.RequestSnapshot, call.RequestDigest = planned.RequestSnapshot, planned.RequestDigest
	if planned.RequiresApproval {
		requestedAt := e.now().UTC()
		call.Status = types.ProductionToolCallPendingApproval
		call.ApprovalStatus = types.ProductionToolApprovalPending
		call.ApprovalRequestedAt = &requestedAt
	}
	return ProductionStepResult{StatePayload: run.StatePayload, ToolCall: call}, nil
}

func productionWorkflowForRun(run *types.ProductionRun) (*productionWorkflowPlan, error) {
	var snapshot struct {
		SkillBindings json.RawMessage `json:"skill_bindings"`
	}
	if len(run.DocumentTypeSnapshot) != 0 {
		if err := decodeProductionJSON(run.DocumentTypeSnapshot, &snapshot, false); err != nil {
			return nil, err
		}
	}
	bindings := types.JSON(snapshot.SkillBindings)
	canonical, err := canonicalProductionWorkflowPlan(run.WorkflowPlanSnapshot, bindings)
	if err != nil {
		return nil, err
	}
	if !canonicalProductionSHA256(run.WorkflowPlanDigest) || productionToolDigest(canonical) != run.WorkflowPlanDigest {
		return nil, errors.New("production workflow plan digest does not match canonical snapshot")
	}
	var workflow productionWorkflowPlan
	if err := decodeProductionJSON(canonical, &workflow, true); err != nil {
		return nil, err
	}
	return &workflow, nil
}

func (e *ProductionExecutor) executeTool(
	ctx context.Context,
	run *types.ProductionRun,
	call *types.ProductionToolCall,
) (ProductionStepResult, error) {
	if call == nil || call.RunID != run.ID || call.TenantID != run.TenantID || call.ProjectID != run.ProjectID ||
		call.DocumentID != run.DocumentID || call.SourceSetID != run.SourceSetID ||
		call.Attempt < 1 || call.Attempt > run.Attempt || call.CurrentStep != run.CurrentStep ||
		(call.Status != types.ProductionToolCallPlanned && call.Status != types.ProductionToolCallApproved && call.Status != types.ProductionToolCallExecuting &&
			call.Status != types.ProductionToolCallCompleted) {
		return ProductionStepResult{}, errors.New("production tool call does not match the claimed run step")
	}
	if call.Status == types.ProductionToolCallCompleted {
		if len(call.ResponseSnapshot) == 0 || call.ResponseEvidenceID == nil || call.ResponseEvidenceSourceItemID == nil ||
			!canonicalProductionUUID(*call.ResponseEvidenceID) || !canonicalProductionUUID(*call.ResponseEvidenceSourceItemID) {
			return ProductionStepResult{}, errors.New("completed production tool call has invalid durable output")
		}
		return ProductionStepResult{
			StatePayload: run.StatePayload,
			ToolCallResult: &ProductionToolCallResult{
				ToolCallID: call.ID, Status: types.ProductionToolCallCompleted,
				ResponseSnapshot: call.ResponseSnapshot, ResponseEvidenceID: *call.ResponseEvidenceID,
				ResponseEvidenceSourceItemID: *call.ResponseEvidenceSourceItemID,
			},
		}, nil
	}
	if call.Status == types.ProductionToolCallApproved || call.Status == types.ProductionToolCallPlanned {
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
