package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

const productionRunDecisionActor = "73000000-0000-4000-8000-000000000001"

type productionRunServiceRepoStub struct {
	interfaces.ProductionRunRepository
	mu   sync.Mutex
	run  *types.ProductionRun
	call *types.ProductionToolCall
}

func (r *productionRunServiceRepoStub) Get(context.Context, uint64, string) (*types.ProductionRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *r.run
	return &copy, nil
}
func (r *productionRunServiceRepoStub) GetToolCall(context.Context, uint64, string) (*types.ProductionToolCall, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *r.call
	return &copy, nil
}
func (r *productionRunServiceRepoStub) ResolveToolCall(_ context.Context, _ uint64, _ string, expected interfaces.ProductionToolCallCAS, decision types.ProductionToolCallStatus, actor string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.call.Status != expected.Status || r.call.Attempt != expected.Attempt || r.call.CurrentStep != expected.CurrentStep {
		return false, nil
	}
	r.call.Status = decision
	if decision == types.ProductionToolCallApproved {
		r.call.ApprovalStatus = types.ProductionToolApprovalApproved
		r.call.ApprovedBy = &actor
	} else {
		r.call.ApprovalStatus = types.ProductionToolApprovalRejected
		r.call.RejectedBy = &actor
	}
	return true, nil
}
func (r *productionRunServiceRepoStub) Transition(_ context.Context, _ uint64, _ string, expected interfaces.ProductionRunCAS, to types.ProductionRunStatus, patch interfaces.ProductionRunPatch) (*types.ProductionRun, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.run.Status != expected.Status || r.run.Attempt != expected.Attempt || r.run.CurrentStep != expected.CurrentStep || r.run.WakeupVersion != expected.WakeupVersion {
		return nil, false, nil
	}
	r.run.Status = to
	if patch.IncrementWakeup {
		r.run.WakeupVersion++
	}
	r.run.ErrorCode = patch.ErrorCode
	r.run.ErrorMessage = patch.ErrorMessage
	r.run.CompletedAt = patch.CompletedAt
	copy := *r.run
	return &copy, true, nil
}

type productionRunServiceUOWStub struct{ repo *productionRunServiceRepoStub }

func (u productionRunServiceUOWStub) WithinTransaction(ctx context.Context, fn func(context.Context) error) error {
	u.repo.mu.Lock()
	runBefore, callBefore := *u.repo.run, *u.repo.call
	u.repo.mu.Unlock()
	err := fn(ctx)
	if err != nil {
		u.repo.mu.Lock()
		*u.repo.run, *u.repo.call = runBefore, callBefore
		u.repo.mu.Unlock()
	}
	return err
}

type productionRunServiceAuditStub struct {
	interfaces.AuditLogService
	entries []*types.AuditLog
	err     error
}

func (a *productionRunServiceAuditStub) Log(_ context.Context, entry *types.AuditLog) error {
	if a.err != nil {
		return a.err
	}
	a.entries = append(a.entries, entry)
	return nil
}

type productionRunServiceAuthorizerStub struct{ err error }

func (a productionRunServiceAuthorizerStub) RequireProjectRole(context.Context, string, ...types.ProductionRole) error {
	return a.err
}

func (a productionRunServiceAuthorizerStub) HasLiveRoleAssignee(context.Context, uint64, string, types.ProductionRole) (bool, error) {
	return true, a.err
}

type productionRunServiceResumerStub struct {
	calls int
	err   error
}

func (r *productionRunServiceResumerStub) Resume(context.Context, uint64, string, int) (bool, error) {
	r.calls++
	return r.err == nil, r.err
}

func productionRunDecisionFixture(t *testing.T) (*productionRunService, *productionRunServiceRepoStub, *productionRunServiceAuditStub, *productionRunServiceResumerStub, context.Context) {
	t.Helper()
	run := &types.ProductionRun{
		ID: mcpAdapterRunID, TenantID: 7, ProjectID: mcpAdapterProjectID,
		DocumentID: mcpAdapterDocumentID, SourceSetID: mcpAdapterSourceSetID,
		Status: types.ProductionRunWaitingApproval, Attempt: 1, CurrentStep: 2, WakeupVersion: 1,
	}
	call := &types.ProductionToolCall{
		ID: mcpAdapterCallID, RunID: run.ID, TenantID: 7, ProjectID: run.ProjectID,
		DocumentID: run.DocumentID, SourceSetID: run.SourceSetID, Attempt: 1, CurrentStep: 2,
		Status: types.ProductionToolCallPendingApproval, ApprovalStatus: types.ProductionToolApprovalPending,
	}
	repo := &productionRunServiceRepoStub{run: run, call: call}
	audit := &productionRunServiceAuditStub{}
	resumer := &productionRunServiceResumerStub{}
	service := NewProductionRunService(
		repo, nil, nil, nil, nil, productionRunServiceAuthorizerStub{}, audit,
		productionRunServiceUOWStub{repo: repo}, resumer,
	)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, productionRunDecisionActor)
	ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleContributor)
	return service, repo, audit, resumer, ctx
}

func TestProductionRunServiceApprovesOnceAuditsAndResumesAfterCommit(t *testing.T) {
	service, repo, audit, resumer, ctx := productionRunDecisionFixture(t)

	call, err := service.DecideToolCall(ctx, repo.call.ID, interfaces.ProductionToolDecisionApprove)
	require.NoError(t, err)
	require.Equal(t, types.ProductionToolCallApproved, call.Status)
	require.Equal(t, types.ProductionRunQueued, repo.run.Status)
	require.Equal(t, 1, resumer.calls)
	require.Len(t, audit.entries, 1)
	require.Equal(t, types.AuditActionProductionToolCallApproved, audit.entries[0].Action)

	_, err = service.DecideToolCall(ctx, repo.call.ID, interfaces.ProductionToolDecisionApprove)
	require.ErrorIs(t, err, types.ErrProductionConflict)
	require.Equal(t, 1, resumer.calls)
	require.Len(t, audit.entries, 1)
}

func TestProductionRunServiceDecisionRollbackAndQueueFailureRecoveryState(t *testing.T) {
	t.Run("audit failure rolls back call and run", func(t *testing.T) {
		service, repo, audit, resumer, ctx := productionRunDecisionFixture(t)
		audit.err = errors.New("audit unavailable")
		_, err := service.DecideToolCall(ctx, repo.call.ID, interfaces.ProductionToolDecisionApprove)
		require.Error(t, err)
		require.Equal(t, types.ProductionToolCallPendingApproval, repo.call.Status)
		require.Equal(t, types.ProductionRunWaitingApproval, repo.run.Status)
		require.Zero(t, resumer.calls)
	})

	t.Run("enqueue failure leaves one durable queued wakeup", func(t *testing.T) {
		service, repo, _, resumer, ctx := productionRunDecisionFixture(t)
		resumer.err = errors.New("queue unavailable")
		_, err := service.DecideToolCall(ctx, repo.call.ID, interfaces.ProductionToolDecisionApprove)
		require.ErrorContains(t, err, "queue unavailable")
		require.Equal(t, types.ProductionToolCallApproved, repo.call.Status)
		require.Equal(t, types.ProductionRunQueued, repo.run.Status)
		require.Greater(t, repo.run.WakeupVersion, repo.run.WakeupEnqueuedVersion)
		_, err = service.DecideToolCall(ctx, repo.call.ID, interfaces.ProductionToolDecisionApprove)
		require.ErrorIs(t, err, types.ErrProductionConflict)
		require.Equal(t, 1, resumer.calls)
	})
}

func TestProductionRunServiceRejectsWithoutProviderResume(t *testing.T) {
	service, repo, audit, resumer, ctx := productionRunDecisionFixture(t)
	call, err := service.DecideToolCall(ctx, repo.call.ID, interfaces.ProductionToolDecisionReject)
	require.NoError(t, err)
	require.Equal(t, types.ProductionToolCallRejected, call.Status)
	require.Equal(t, types.ProductionRunFailed, repo.run.Status)
	require.Zero(t, resumer.calls)
	require.Equal(t, types.AuditActionProductionToolCallRejected, audit.entries[0].Action)
}
