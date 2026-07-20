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

const (
	defaultProductionRunLeaseTTL = 5 * time.Minute
	productionSystemActorID      = "00000000-0000-4000-8000-000000000001"
)

// ProductionStepResult is the complete durable output of one executor call.
// Concrete Skill, MCP, writer and validator executors are registered by later
// tasks; this state machine only owns their persistence boundary.
type ProductionStepResult struct {
	StatePayload types.JSON
	// RawModelResponse must be persisted by the dedicated running-to-running
	// audit transition before a step result is returned. Nonempty values are
	// rejected here so ordinary run transitions cannot write audit state.
	RawModelResponse types.JSON
	OutputVersionID  *string
	ToolCall         *types.ProductionToolCall
	ToolCallResult   *ProductionToolCallResult
	Complete         bool
}

// ProductionToolCallResult binds executor output to one explicit persisted
// invocation; the orchestrator never infers identity from call order.
type ProductionToolCallResult struct {
	ToolCallID                   string
	Status                       types.ProductionToolCallStatus
	ResponseSnapshot             types.JSON
	ResponseEvidenceID           string
	ResponseEvidenceSourceItemID string
	ErrorCode                    string
	ErrorMessage                 string
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
	if run.WakeupVersion == 0 {
		run.WakeupVersion = 1
	}
	if run.WakeupVersion != 1 || run.WakeupEnqueuedVersion != 0 {
		return errors.New("new production run must start with pending wakeup version 1")
	}
	if err := (types.ProductionRunPayload{
		TenantID: run.TenantID, RunID: run.ID, Attempt: run.Attempt,
	}).Validate(); err != nil {
		return err
	}
	if err := o.repo.Create(ctx, run); err != nil {
		return err
	}
	_, err := o.enqueuePending(ctx, run)
	return err
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
		productionRunCAS(run),
		o.leaseTTL,
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
		return o.persistExecutionFailure(ctx, claimed, calls, executeErr)
	}
	if result.ToolCall != nil {
		if len(approved) > 0 || result.ToolCallResult != nil {
			return o.persistExecutionFailure(ctx, claimed, calls, errors.New("approved tool execution cannot request another tool call"))
		}
		return o.persistApprovalWait(ctx, claimed, result.ToolCall)
	}
	if len(approved) > 0 && result.ToolCallResult == nil {
		return o.persistExecutionFailure(ctx, claimed, calls, errors.New("approved tool execution result is required"))
	}
	if len(approved) == 0 && result.ToolCallResult != nil {
		return o.persistExecutionFailure(ctx, claimed, calls, errors.New("tool result does not match a persisted approved call"))
	}
	return o.persistStepResult(ctx, claimed, calls, result)
}

func (o *ProductionOrchestrator) persistStepResult(
	ctx context.Context,
	run *types.ProductionRun,
	calls []*types.ProductionToolCall,
	result ProductionStepResult,
) error {
	if len(result.RawModelResponse) != 0 {
		return errors.New("raw model response must be audited before returning a step result")
	}
	nextStep := run.CurrentStep + 1
	patch := interfaces.ProductionRunPatch{
		CurrentStep: &nextStep, StatePayload: result.StatePayload,
		OutputVersionID: result.OutputVersionID,
	}
	to := types.ProductionRunQueued
	patch.IncrementWakeup = true
	if result.Complete {
		to = types.ProductionRunCompleted
		patch.IncrementWakeup = false
		completedAt := o.now().UTC()
		patch.CompletedAt = &completedAt
	}
	var transitioned *types.ProductionRun
	err := o.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if result.ToolCallResult != nil {
			call, matchErr := matchingToolResultCall(run, calls, result.ToolCallResult.ToolCallID)
			if matchErr != nil {
				return matchErr
			}
			completedAt := o.now().UTC()
			callPatch := interfaces.ProductionToolCallPatch{CompletedAt: &completedAt}
			if result.ToolCallResult.Status == types.ProductionToolCallCompleted {
				callPatch.ResponseSnapshot = result.ToolCallResult.ResponseSnapshot
				callPatch.ResponseEvidenceID = stringPtr(result.ToolCallResult.ResponseEvidenceID)
				callPatch.ResponseEvidenceSourceItemID = stringPtr(result.ToolCallResult.ResponseEvidenceSourceItemID)
			} else if result.ToolCallResult.Status == types.ProductionToolCallFailed {
				callPatch.ErrorCode = stringPtr(result.ToolCallResult.ErrorCode)
				callPatch.ErrorMessage = stringPtr(result.ToolCallResult.ErrorMessage)
				to = types.ProductionRunFailed
				patch.IncrementWakeup = false
				patch.CompletedAt = &completedAt
				patch.ErrorCode = callPatch.ErrorCode
				patch.ErrorMessage = callPatch.ErrorMessage
			} else {
				return errors.New("tool call result status must be completed or failed")
			}
			changed, callErr := o.repo.TransitionToolCall(
				txCtx, run.TenantID, run.ID, call.ID,
				interfaces.ProductionToolCallCAS{Status: call.Status, Attempt: call.Attempt, CurrentStep: call.CurrentStep},
				result.ToolCallResult.Status, callPatch,
			)
			if callErr != nil {
				return callErr
			}
			if !changed {
				return errors.New("production tool call became stale before output was persisted")
			}
		}
		if to == types.ProductionRunCompleted {
			remaining := calls
			if result.ToolCallResult != nil {
				remaining = toolCallsExcept(calls, result.ToolCallResult.ToolCallID)
			}
			if err := o.failActiveToolCalls(
				txCtx, run, remaining, "PARENT_COMPLETED", "parent run completed",
				productionSystemActorID, o.now().UTC(),
			); err != nil {
				return err
			}
		}
		var changed bool
		var transitionErr error
		transitioned, changed, transitionErr = o.repo.Transition(
			txCtx, run.TenantID, run.ID, productionRunCAS(run), to, patch,
		)
		if transitionErr != nil {
			return transitionErr
		}
		if !changed {
			return errors.New("production run became stale before step output was persisted")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if transitioned.Status != types.ProductionRunQueued {
		return nil
	}
	_, err = o.enqueuePending(ctx, transitioned)
	return err
}

func (o *ProductionOrchestrator) persistExecutionFailure(
	ctx context.Context,
	run *types.ProductionRun,
	active []*types.ProductionToolCall,
	executeErr error,
) error {
	code := "STEP_EXECUTION_FAILED"
	message := executeErr.Error()
	completedAt := o.now().UTC()
	return o.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := o.failActiveToolCalls(
			txCtx, run, active, code, message, productionSystemActorID, completedAt,
		); err != nil {
			return err
		}
		_, changed, err := o.repo.Transition(
			txCtx, run.TenantID, run.ID, productionRunCAS(run), types.ProductionRunFailed,
			interfaces.ProductionRunPatch{ErrorCode: &code, ErrorMessage: &message, CompletedAt: &completedAt},
		)
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("production run became stale before failure was persisted")
		}
		return nil
	})
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
		_, changed, err := o.repo.Transition(
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
	run, err := o.repo.Get(ctx, tenantID, call.RunID)
	if err != nil {
		return false, err
	}
	if call.Status != types.ProductionToolCallPendingApproval {
		if call.Status == types.ProductionToolCallApproved && run.Status == types.ProductionRunQueued &&
			run.Attempt == call.Attempt && run.CurrentStep == call.CurrentStep {
			return o.enqueuePending(ctx, run)
		}
		return false, nil
	}
	if run.Status != types.ProductionRunWaitingApproval || run.Attempt != call.Attempt || run.CurrentStep != call.CurrentStep {
		latest, latestErr := o.repo.GetToolCall(ctx, tenantID, callID)
		if latestErr != nil {
			return false, latestErr
		}
		if latest.Status != types.ProductionToolCallPendingApproval {
			if latest.Status == types.ProductionToolCallApproved && run.Status == types.ProductionRunQueued {
				return o.enqueuePending(ctx, run)
			}
			return false, nil
		}
		return false, errors.New("production approval does not match the persisted waiting step")
	}

	decisionWon := false
	var transitioned *types.ProductionRun
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
		patch := interfaces.ProductionRunPatch{IncrementWakeup: true}
		if decision == types.ProductionToolCallRejected {
			to = types.ProductionRunFailed
			patch.IncrementWakeup = false
			code := "TOOL_CALL_REJECTED"
			message := "production tool call was rejected"
			completedAt := o.now().UTC()
			patch.ErrorCode = &code
			patch.ErrorMessage = &message
			patch.CompletedAt = &completedAt
		}
		var changed bool
		var transitionErr error
		transitioned, changed, transitionErr = o.repo.Transition(
			txCtx, tenantID, run.ID, productionRunCAS(run), to, patch,
		)
		if transitionErr != nil {
			return transitionErr
		}
		if !changed {
			return errors.New("production run became stale while resolving approval")
		}
		decisionWon = true
		return nil
	})
	if err != nil || !decisionWon {
		return false, err
	}
	if decision == types.ProductionToolCallRejected {
		return true, nil
	}
	return o.enqueuePending(ctx, transitioned)
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
		return o.enqueuePending(ctx, run)
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
	transitioned, changed, err := o.repo.Transition(
		ctx, tenantID, runID, productionRunCAS(run), types.ProductionRunQueued,
		interfaces.ProductionRunPatch{IncrementWakeup: true},
	)
	if err != nil || !changed {
		return false, err
	}
	return o.enqueuePending(ctx, transitioned)
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
	calls, err := o.repo.ListToolCalls(ctx, tenantID, runID)
	if err != nil {
		return false, err
	}
	won := false
	err = o.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := o.terminalizeCallsForCancellation(txCtx, run, calls, actor, completedAt); err != nil {
			return err
		}
		_, changed, transitionErr := o.repo.Transition(
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

func (o *ProductionOrchestrator) enqueuePending(ctx context.Context, run *types.ProductionRun) (bool, error) {
	if run == nil || run.Status != types.ProductionRunQueued || run.WakeupVersion <= run.WakeupEnqueuedVersion {
		return false, nil
	}
	payload := types.ProductionRunPayload{TenantID: run.TenantID, RunID: run.ID, Attempt: run.Attempt}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	taskType, err := productionTaskType(run.RunType)
	if err != nil {
		return false, err
	}
	taskID := fmt.Sprintf("production:%d:%s:%d:wakeup:%d", run.TenantID, run.ID, run.Attempt, run.WakeupVersion)
	_, err = o.enqueuer.Enqueue(
		asynq.NewTask(taskType, encoded),
		asynq.Queue(types.QueueProduction),
		asynq.TaskID(taskID),
	)
	owned := err == nil
	if err != nil && !errors.Is(err, asynq.ErrTaskIDConflict) {
		return false, err
	}
	marked, markErr := o.repo.MarkWakeupEnqueued(
		ctx, run.TenantID, run.ID, run.Attempt, run.CurrentStep, run.WakeupVersion,
	)
	if markErr != nil {
		return owned, markErr
	}
	if !marked {
		latest, getErr := o.repo.Get(ctx, run.TenantID, run.ID)
		if getErr != nil {
			return owned, getErr
		}
		if latest.WakeupEnqueuedVersion < run.WakeupVersion {
			return owned, errors.New("production wakeup became stale before enqueue mark")
		}
	}
	return owned, nil
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
		WakeupVersion: run.WakeupVersion,
	}
}

func approvedCallsForStep(
	calls []*types.ProductionToolCall,
	attempt, currentStep int,
) []*types.ProductionToolCall {
	approved := make([]*types.ProductionToolCall, 0, len(calls))
	for _, call := range calls {
		if call.Attempt == attempt && call.CurrentStep == currentStep &&
			(call.Status == types.ProductionToolCallApproved || call.Status == types.ProductionToolCallExecuting) &&
			call.ApprovalStatus == types.ProductionToolApprovalApproved {
			approved = append(approved, call)
		}
	}
	return approved
}

func matchingToolResultCall(
	run *types.ProductionRun,
	calls []*types.ProductionToolCall,
	callID string,
) (*types.ProductionToolCall, error) {
	for _, call := range calls {
		if call.ID != callID {
			continue
		}
		if call.TenantID != run.TenantID || call.RunID != run.ID ||
			call.Attempt != run.Attempt || call.CurrentStep != run.CurrentStep {
			return nil, errors.New("tool call result does not own the claimed run step")
		}
		if call.Status != types.ProductionToolCallApproved && call.Status != types.ProductionToolCallExecuting {
			return nil, errors.New("tool call result is not approved or executing")
		}
		return call, nil
	}
	return nil, errors.New("tool call result id does not match the approved run step")
}

func toolCallsExcept(calls []*types.ProductionToolCall, excludedID string) []*types.ProductionToolCall {
	filtered := make([]*types.ProductionToolCall, 0, len(calls))
	for _, call := range calls {
		if call.ID != excludedID {
			filtered = append(filtered, call)
		}
	}
	return filtered
}

func (o *ProductionOrchestrator) failActiveToolCalls(
	ctx context.Context,
	run *types.ProductionRun,
	calls []*types.ProductionToolCall,
	code, message string,
	actor string,
	completedAt time.Time,
) error {
	for _, call := range calls {
		if call.TenantID != run.TenantID || call.RunID != run.ID {
			return errors.New("production tool call ownership mismatch")
		}
		if call.Status == types.ProductionToolCallPendingApproval {
			changed, err := o.repo.ResolveToolCall(
				ctx, run.TenantID, call.ID,
				interfaces.ProductionToolCallCAS{Status: call.Status, Attempt: call.Attempt, CurrentStep: call.CurrentStep},
				types.ProductionToolCallRejected, actor,
			)
			if err != nil {
				return err
			}
			if !changed {
				return errors.New("pending production tool call became stale while terminalizing parent")
			}
			continue
		}
		if call.Status != types.ProductionToolCallPlanned &&
			call.Status != types.ProductionToolCallApproved &&
			call.Status != types.ProductionToolCallExecuting {
			continue
		}
		changed, err := o.repo.TransitionToolCall(
			ctx, run.TenantID, run.ID, call.ID,
			interfaces.ProductionToolCallCAS{Status: call.Status, Attempt: call.Attempt, CurrentStep: call.CurrentStep},
			types.ProductionToolCallFailed,
			interfaces.ProductionToolCallPatch{
				ErrorCode: &code, ErrorMessage: &message, CompletedAt: &completedAt,
			},
		)
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("production tool call became stale while failing parent run")
		}
	}
	return nil
}

func (o *ProductionOrchestrator) terminalizeCallsForCancellation(
	ctx context.Context,
	run *types.ProductionRun,
	calls []*types.ProductionToolCall,
	actor string,
	completedAt time.Time,
) error {
	return o.failActiveToolCalls(
		ctx, run, calls, "RUN_CANCELLED", "production run was cancelled", actor, completedAt,
	)
}

func stringPtr(value string) *string { return &value }

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
