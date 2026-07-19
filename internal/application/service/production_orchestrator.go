package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
)

const defaultProductionRunLeaseTTL = 5 * time.Minute

// ProductionStepResult is the complete durable output of one executor call.
// Concrete Skill, MCP, writer and validator executors are registered by later
// tasks; this state machine only owns their persistence boundary.
type ProductionStepResult struct {
	StatePayload     types.JSON
	RawModelResponse types.JSON
	OutputVersionID  *string
	ToolCall         *types.ProductionToolCall
	Complete         bool
}

// ProductionStepExecutor executes exactly one resumable step. Approved calls
// are loaded from the database so a new process can resume without memory from
// the worker that requested approval.
type ProductionStepExecutor interface {
	ExecuteStep(ctx context.Context, run *types.ProductionRun, approved []*types.ProductionToolCall) (ProductionStepResult, error)
}

// ProductionOrchestrator advances persistent runs one step per wake-up.
type ProductionOrchestrator struct {
	repo     interfaces.ProductionRunRepository
	uow      interfaces.ProductionUnitOfWork
	executor ProductionStepExecutor
	enqueuer interfaces.TaskEnqueuer
	now      func() time.Time
	leaseTTL time.Duration
}

func NewProductionOrchestrator(
	repo interfaces.ProductionRunRepository,
	uow interfaces.ProductionUnitOfWork,
	executor ProductionStepExecutor,
	enqueuer interfaces.TaskEnqueuer,
) *ProductionOrchestrator {
	return &ProductionOrchestrator{
		repo: repo, uow: uow, executor: executor, enqueuer: enqueuer,
		now: time.Now, leaseTTL: defaultProductionRunLeaseTTL,
	}
}

// CreateRun persists and schedules a fresh run. A queue failure leaves the
// queued row intact so Resume can reissue the deterministic wake-up.
func (o *ProductionOrchestrator) CreateRun(ctx context.Context, run *types.ProductionRun) error {
	if err := o.validateDependencies(); err != nil {
		return err
	}
	if run == nil {
		return errors.New("production run is required")
	}
	if run.Status == "" {
		run.Status = types.ProductionRunQueued
	}
	if run.Status != types.ProductionRunQueued {
		return errors.New("new production run must be queued")
	}
	if run.Attempt == 0 {
		run.Attempt = 1
	}
	if run.Attempt != 1 || run.CurrentStep != 0 {
		return errors.New("new production run must start at attempt 1 step 0")
	}
	if err := (types.ProductionRunPayload{
		TenantID: run.TenantID, RunID: run.ID, Attempt: run.Attempt,
	}).Validate(); err != nil {
		return err
	}
	if err := o.repo.Create(ctx, run); err != nil {
		return err
	}
	return o.enqueue(run)
}

// HandleRun claims and executes at most one resumable step, persists its
// output, schedules at most one follow-up wake-up, and returns.
func (o *ProductionOrchestrator) HandleRun(ctx context.Context, payload types.ProductionRunPayload) error {
	if err := o.validateDependencies(); err != nil {
		return err
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	run, err := o.repo.Get(ctx, payload.TenantID, payload.RunID)
	if err != nil {
		return err
	}
	if isProductionRunTerminal(run.Status) || run.Status == types.ProductionRunWaitingApproval {
		return nil
	}
	if run.Attempt != payload.Attempt {
		return nil
	}
	claimed, won, err := o.repo.Claim(
		ctx,
		payload.TenantID,
		payload.RunID,
		interfaces.ProductionRunCAS{Status: run.Status, Attempt: run.Attempt, CurrentStep: run.CurrentStep},
		o.now().UTC().Add(-o.leaseTTL),
	)
	if err != nil {
		return err
	}
	if !won {
		return nil
	}

	calls, err := o.repo.ListToolCalls(ctx, claimed.TenantID, claimed.ID)
	if err != nil {
		return err
	}
	approved := approvedCallsForStep(calls, claimed.Attempt, claimed.CurrentStep)
	result, executeErr := o.executor.ExecuteStep(ctx, claimed, approved)
	if executeErr != nil {
		return o.persistExecutionFailure(ctx, claimed, executeErr)
	}
	if result.ToolCall != nil {
		return o.persistApprovalWait(ctx, claimed, result.ToolCall)
	}
	return o.persistStepResult(ctx, claimed, result)
}

func (o *ProductionOrchestrator) persistStepResult(
	ctx context.Context,
	run *types.ProductionRun,
	result ProductionStepResult,
) error {
	nextStep := run.CurrentStep + 1
	patch := interfaces.ProductionRunPatch{
		CurrentStep: &nextStep, StatePayload: result.StatePayload,
		RawModelResponse: result.RawModelResponse, OutputVersionID: result.OutputVersionID,
	}
	to := types.ProductionRunQueued
	if result.Complete {
		to = types.ProductionRunCompleted
		completedAt := o.now().UTC()
		patch.CompletedAt = &completedAt
	}
	changed, err := o.repo.Transition(ctx, run.TenantID, run.ID, productionRunCAS(run), to, patch)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("production run became stale before step output was persisted")
	}
	if result.Complete {
		return nil
	}
	run.Status = types.ProductionRunQueued
	run.CurrentStep = nextStep
	run.StatePayload = result.StatePayload
	return o.enqueue(run)
}

func (o *ProductionOrchestrator) persistExecutionFailure(
	ctx context.Context,
	run *types.ProductionRun,
	executeErr error,
) error {
	code := "STEP_EXECUTION_FAILED"
	message := executeErr.Error()
	completedAt := o.now().UTC()
	changed, err := o.repo.Transition(
		ctx, run.TenantID, run.ID, productionRunCAS(run), types.ProductionRunFailed,
		interfaces.ProductionRunPatch{ErrorCode: &code, ErrorMessage: &message, CompletedAt: &completedAt},
	)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("production run became stale before failure was persisted")
	}
	return nil
}

func (o *ProductionOrchestrator) persistApprovalWait(
	ctx context.Context,
	run *types.ProductionRun,
	call *types.ProductionToolCall,
) error {
	if call.Status != types.ProductionToolCallPendingApproval ||
		call.ApprovalStatus != types.ProductionToolApprovalPending {
		return errors.New("approval step must return a pending approval tool call")
	}
	call.RunID = run.ID
	call.TenantID = run.TenantID
	call.ProjectID = run.ProjectID
	call.DocumentID = run.DocumentID
	call.SourceSetID = run.SourceSetID
	call.Attempt = run.Attempt
	call.CurrentStep = run.CurrentStep
	if call.ApprovalRequestedAt == nil {
		requestedAt := o.now().UTC()
		call.ApprovalRequestedAt = &requestedAt
	}
	return o.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := o.repo.CreateToolCall(txCtx, call); err != nil {
			return err
		}
		changed, err := o.repo.Transition(
			txCtx, run.TenantID, run.ID, productionRunCAS(run),
			types.ProductionRunWaitingApproval, interfaces.ProductionRunPatch{},
		)
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("production run became stale before approval wait was persisted")
		}
		return nil
	})
}

// ResolveDecision durably records one approval decision and, for approval,
// changes the waiting run back to queued in the same transaction.
func (o *ProductionOrchestrator) ResolveDecision(
	ctx context.Context,
	tenantID uint64,
	callID string,
	decision types.ProductionToolCallStatus,
	actor string,
) (bool, error) {
	if err := o.validateDependencies(); err != nil {
		return false, err
	}
	call, err := o.repo.GetToolCall(ctx, tenantID, callID)
	if err != nil {
		return false, err
	}
	if call.Status != types.ProductionToolCallPendingApproval {
		return false, nil
	}
	run, err := o.repo.Get(ctx, tenantID, call.RunID)
	if err != nil {
		return false, err
	}
	if run.Status != types.ProductionRunWaitingApproval || run.Attempt != call.Attempt || run.CurrentStep != call.CurrentStep {
		latest, latestErr := o.repo.GetToolCall(ctx, tenantID, callID)
		if latestErr != nil {
			return false, latestErr
		}
		if latest.Status != types.ProductionToolCallPendingApproval {
			return false, nil
		}
		return false, errors.New("production approval does not match the persisted waiting step")
	}

	won := false
	err = o.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		resolved, resolveErr := o.repo.ResolveToolCall(
			txCtx, tenantID, call.ID,
			interfaces.ProductionToolCallCAS{Status: call.Status, Attempt: call.Attempt, CurrentStep: call.CurrentStep},
			decision, actor,
		)
		if resolveErr != nil {
			return resolveErr
		}
		if !resolved {
			return nil
		}
		to := types.ProductionRunQueued
		patch := interfaces.ProductionRunPatch{}
		if decision == types.ProductionToolCallRejected {
			to = types.ProductionRunFailed
			code := "TOOL_CALL_REJECTED"
			message := "production tool call was rejected"
			completedAt := o.now().UTC()
			patch.ErrorCode = &code
			patch.ErrorMessage = &message
			patch.CompletedAt = &completedAt
		}
		changed, transitionErr := o.repo.Transition(
			txCtx, tenantID, run.ID, productionRunCAS(run), to, patch,
		)
		if transitionErr != nil {
			return transitionErr
		}
		if !changed {
			return errors.New("production run became stale while resolving approval")
		}
		won = true
		return nil
	})
	if err != nil || !won {
		return false, err
	}
	if decision == types.ProductionToolCallRejected {
		return true, nil
	}
	run.Status = types.ProductionRunQueued
	if err := o.enqueue(run); err != nil {
		return true, err
	}
	return true, nil
}

// Resume reissues a deterministic wake-up for a queued run or resumes a
// waiting step whose matching tool call was already durably approved.
func (o *ProductionOrchestrator) Resume(
	ctx context.Context,
	tenantID uint64,
	runID string,
	attempt int,
) (bool, error) {
	if err := o.validateDependencies(); err != nil {
		return false, err
	}
	run, err := o.repo.Get(ctx, tenantID, runID)
	if err != nil {
		return false, err
	}
	if isProductionRunTerminal(run.Status) {
		return false, errors.New("terminal production runs cannot resume")
	}
	if run.Attempt != attempt {
		return false, nil
	}
	if run.Status == types.ProductionRunQueued {
		return true, o.enqueue(run)
	}
	if run.Status != types.ProductionRunWaitingApproval {
		return false, nil
	}
	calls, err := o.repo.ListToolCalls(ctx, tenantID, runID)
	if err != nil {
		return false, err
	}
	if len(approvedCallsForStep(calls, run.Attempt, run.CurrentStep)) == 0 {
		return false, errors.New("production run has no approved tool call for the persisted step")
	}
	changed, err := o.repo.Transition(
		ctx, tenantID, runID, productionRunCAS(run), types.ProductionRunQueued, interfaces.ProductionRunPatch{},
	)
	if err != nil || !changed {
		return false, err
	}
	run.Status = types.ProductionRunQueued
	return true, o.enqueue(run)
}

// Cancel terminalizes a queued/running run, or rejects its pending approval
// and cancels the parent atomically.
func (o *ProductionOrchestrator) Cancel(
	ctx context.Context,
	tenantID uint64,
	runID string,
	actor string,
) (bool, error) {
	if err := o.validateDependencies(); err != nil {
		return false, err
	}
	run, err := o.repo.Get(ctx, tenantID, runID)
	if err != nil {
		return false, err
	}
	if isProductionRunTerminal(run.Status) {
		return false, errors.New("terminal production runs are immutable")
	}
	completedAt := o.now().UTC()
	if run.Status != types.ProductionRunWaitingApproval {
		return o.repo.Transition(
			ctx, tenantID, runID, productionRunCAS(run), types.ProductionRunCancelled,
			interfaces.ProductionRunPatch{CompletedAt: &completedAt},
		)
	}
	calls, err := o.repo.ListToolCalls(ctx, tenantID, runID)
	if err != nil {
		return false, err
	}
	var pending *types.ProductionToolCall
	for _, call := range calls {
		if call.Attempt == run.Attempt && call.CurrentStep == run.CurrentStep &&
			call.Status == types.ProductionToolCallPendingApproval {
			pending = call
			break
		}
	}
	if pending == nil {
		return false, errors.New("waiting production run has no pending tool call")
	}
	won := false
	err = o.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		resolved, resolveErr := o.repo.ResolveToolCall(
			txCtx, tenantID, pending.ID,
			interfaces.ProductionToolCallCAS{Status: pending.Status, Attempt: pending.Attempt, CurrentStep: pending.CurrentStep},
			types.ProductionToolCallRejected, actor,
		)
		if resolveErr != nil || !resolved {
			return resolveErr
		}
		changed, transitionErr := o.repo.Transition(
			txCtx, tenantID, runID, productionRunCAS(run), types.ProductionRunCancelled,
			interfaces.ProductionRunPatch{CompletedAt: &completedAt},
		)
		if transitionErr != nil {
			return transitionErr
		}
		won = changed
		if !changed {
			return errors.New("production run became stale while cancelling")
		}
		return nil
	})
	return won, err
}

func (o *ProductionOrchestrator) enqueue(run *types.ProductionRun) error {
	payload := types.ProductionRunPayload{TenantID: run.TenantID, RunID: run.ID, Attempt: run.Attempt}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	taskType, err := productionTaskType(run.RunType)
	if err != nil {
		return err
	}
	taskID := fmt.Sprintf("production:%d:%s:%d:%d", run.TenantID, run.ID, run.Attempt, run.CurrentStep)
	_, err = o.enqueuer.Enqueue(
		asynq.NewTask(taskType, encoded),
		asynq.Queue(types.QueueProduction),
		asynq.TaskID(taskID),
	)
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil
	}
	return err
}

func (o *ProductionOrchestrator) validateDependencies() error {
	if o == nil || o.repo == nil || o.uow == nil || o.executor == nil || o.enqueuer == nil {
		return errors.New("production orchestrator dependencies are required")
	}
	if o.now == nil || o.leaseTTL <= 0 {
		return errors.New("production orchestrator clock and lease must be configured")
	}
	return nil
}

func productionRunCAS(run *types.ProductionRun) interfaces.ProductionRunCAS {
	return interfaces.ProductionRunCAS{
		Status: run.Status, Attempt: run.Attempt, CurrentStep: run.CurrentStep,
	}
}

func approvedCallsForStep(
	calls []*types.ProductionToolCall,
	attempt, currentStep int,
) []*types.ProductionToolCall {
	approved := make([]*types.ProductionToolCall, 0, len(calls))
	for _, call := range calls {
		if call.Attempt == attempt && call.CurrentStep == currentStep &&
			call.Status == types.ProductionToolCallApproved &&
			call.ApprovalStatus == types.ProductionToolApprovalApproved {
			approved = append(approved, call)
		}
	}
	return approved
}

func productionTaskType(runType types.ProductionRunType) (string, error) {
	switch runType {
	case types.ProductionRunCollect:
		return types.TypeProductionCollect, nil
	case types.ProductionRunWrite, types.ProductionRunRewrite:
		return types.TypeProductionWrite, nil
	case types.ProductionRunValidate:
		return types.TypeProductionValidate, nil
	default:
		return "", errors.New("invalid production run type")
	}
}

func isProductionRunTerminal(status types.ProductionRunStatus) bool {
	return status == types.ProductionRunCompleted || status == types.ProductionRunFailed || status == types.ProductionRunCancelled
}

var _ interfaces.ProductionRunOrchestrator = (*ProductionOrchestrator)(nil)
