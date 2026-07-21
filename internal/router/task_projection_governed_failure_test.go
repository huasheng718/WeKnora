package router

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/middleware/asynqdl"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type governedManualWorkerState struct {
	mu           sync.Mutex
	parseStatus  string
	targetStatus types.ProductionReleaseTargetStatus
}

func newGovernedManualWorkerState() *governedManualWorkerState {
	return &governedManualWorkerState{
		parseStatus:  types.ParseStatusPending,
		targetStatus: types.ReleaseTargetBuilding,
	}
}

func (s *governedManualWorkerState) statuses() (string, types.ProductionReleaseTargetStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parseStatus, s.targetStatus
}

type governedManualWorkerKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	state       *governedManualWorkerState
	knowledge   *types.Knowledge
	getErr      error
	updateErr   error
	updateCalls int
}

func (r *governedManualWorkerKnowledgeRepo) GetKnowledgeByID(
	context.Context, uint64, string,
) (*types.Knowledge, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	copyKnowledge := *r.knowledge
	return &copyKnowledge, nil
}

func (r *governedManualWorkerKnowledgeRepo) UpdateKnowledge(
	_ context.Context, knowledge *types.Knowledge,
) error {
	r.updateCalls++
	if r.updateErr != nil {
		return r.updateErr
	}
	r.state.mu.Lock()
	r.state.parseStatus = knowledge.ParseStatus
	r.state.mu.Unlock()
	return nil
}

func (r *governedManualWorkerKnowledgeRepo) UpdateKnowledgeColumns(
	_ context.Context, _ string, values map[string]interface{},
) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if status, ok := values["parse_status"].(string); ok {
		r.state.parseStatus = status
	}
	return nil
}

type governedManualWorkerTenantRepo struct {
	interfaces.TenantRepository
	err error
}

func (r *governedManualWorkerTenantRepo) GetTenantByID(
	context.Context, uint64,
) (*types.Tenant, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &types.Tenant{ID: 7}, nil
}

type governedManualWorkerKBService struct {
	interfaces.KnowledgeBaseService
	err error
}

func (s *governedManualWorkerKBService) GetKnowledgeBaseByID(
	context.Context, string,
) (*types.KnowledgeBase, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &types.KnowledgeBase{ID: "kb-1", TenantID: 7}, nil
}

type governedManualWorkerProjectionRecorder struct {
	state *governedManualWorkerState
}

func (r *governedManualWorkerProjectionRecorder) RecordProjectionKnowledgeFailure(
	context.Context, string, string,
) (bool, error) {
	r.state.mu.Lock()
	r.state.targetStatus = types.ReleaseTargetFailed
	r.state.mu.Unlock()
	return true, nil
}

type governedManualWorkerFixture struct {
	service       interfaces.KnowledgeService
	state         *governedManualWorkerState
	repo          *governedManualWorkerKnowledgeRepo
	dependencyErr error
	persistErr    error
}

func newGovernedManualWorkerFixture(t *testing.T, failure string) *governedManualWorkerFixture {
	t.Helper()
	state := newGovernedManualWorkerState()
	dependencyErr := errors.New(failure + " dependency unavailable")
	persistErr := errors.New("failed status persistence unavailable")
	knowledge := &types.Knowledge{
		ID: "knowledge-1", TenantID: 7, KnowledgeBaseID: "kb-1",
		Type: types.KnowledgeTypeManual, ParseStatus: types.ParseStatusPending,
	}
	repo := &governedManualWorkerKnowledgeRepo{state: state, knowledge: knowledge}
	tenants := &governedManualWorkerTenantRepo{}
	kbs := &governedManualWorkerKBService{}
	switch failure {
	case "tenant":
		tenants.err = dependencyErr
	case "knowledge":
		repo.getErr = dependencyErr
	case "knowledge-base":
		kbs.err = dependencyErr
	case "failed-persistence":
		kbs.err = dependencyErr
		repo.updateErr = persistErr
	default:
		t.Fatalf("unknown failure %q", failure)
	}
	knowledgeService, err := service.NewKnowledgeService(
		nil, repo, nil, kbs, tenants, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil,
	)
	require.NoError(t, err)
	return &governedManualWorkerFixture{
		service: knowledgeService, state: state, repo: repo,
		dependencyErr: dependencyErr, persistErr: persistErr,
	}
}

func governedManualWorkerTask(t *testing.T, governed bool) *asynq.Task {
	t.Helper()
	payload := types.ManualProcessPayload{
		TenantID: 7, KnowledgeID: "knowledge-1", KnowledgeBaseID: "kb-1",
		Content: "# governed projection",
	}
	if governed {
		payload.TargetUpdatedAt = time.Date(2026, 7, 22, 3, 0, 0, 0, time.UTC)
	}
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	return asynq.NewTask(types.TypeManualProcess, encoded)
}

func requireGovernedManualTerminalState(t *testing.T, state *governedManualWorkerState) {
	t.Helper()
	parseStatus, targetStatus := state.statuses()
	require.Equal(t, types.ParseStatusFailed, parseStatus)
	require.Equal(t, types.ReleaseTargetFailed, targetStatus)
}

func requireGovernedManualPreClaimWrites(
	t *testing.T,
	fixture *governedManualWorkerFixture,
	failure string,
) {
	t.Helper()
	wantFailureStateWrites := 0
	if failure == "knowledge-base" || failure == "failed-persistence" {
		wantFailureStateWrites = 1
	}
	require.Equal(t, wantFailureStateWrites, fixture.repo.updateCalls)
}

func TestRedisGovernedManualDependencyFailuresReachTerminalRecovery(t *testing.T) {
	for _, failure := range []string{"tenant", "knowledge", "knowledge-base", "failed-persistence"} {
		t.Run(failure, func(t *testing.T) {
			fixture := newGovernedManualWorkerFixture(t, failure)
			callback := newDeadLetterKnowledgeFailer(
				fixture.service, nil, &governedManualWorkerProjectionRecorder{state: fixture.state},
			)
			handler := asynqdl.MiddlewareWithCallback(nil, callback)(
				asynq.HandlerFunc(fixture.service.ProcessManualUpdate),
			)

			err := handler.ProcessTask(context.Background(), governedManualWorkerTask(t, true))

			require.ErrorIs(t, err, fixture.dependencyErr)
			if failure == "failed-persistence" {
				require.ErrorIs(t, err, fixture.persistErr)
			}
			requireGovernedManualPreClaimWrites(t, fixture, failure)
			requireGovernedManualTerminalState(t, fixture.state)
		})
	}
}

func TestLiteGovernedManualDependencyFailuresReachTerminalRecovery(t *testing.T) {
	for _, failure := range []string{"tenant", "knowledge", "knowledge-base", "failed-persistence"} {
		t.Run(failure, func(t *testing.T) {
			fixture := newGovernedManualWorkerFixture(t, failure)
			executor := NewSyncTaskExecutor()
			callback := newDeadLetterKnowledgeFailer(
				fixture.service, nil, &governedManualWorkerProjectionRecorder{state: fixture.state},
			)
			callbackDone := make(chan struct{})
			executor.SetTerminalFailureHandler(func(ctx context.Context, task *asynq.Task, taskErr error) {
				callback(ctx, task, taskErr)
				close(callbackDone)
			})
			handlerResult := make(chan error, 1)
			executor.RegisterHandler(types.TypeManualProcess, func(ctx context.Context, task *asynq.Task) error {
				err := fixture.service.ProcessManualUpdate(ctx, task)
				handlerResult <- err
				return err
			})

			_, err := executor.Enqueue(governedManualWorkerTask(t, true), asynq.MaxRetry(0))
			require.NoError(t, err)
			var processErr error
			select {
			case processErr = <-handlerResult:
			case <-time.After(time.Second):
				t.Fatal("Lite manual worker did not execute")
			}
			require.ErrorIs(t, processErr, fixture.dependencyErr)
			if failure == "failed-persistence" {
				require.ErrorIs(t, processErr, fixture.persistErr)
			}
			requireGovernedManualPreClaimWrites(t, fixture, failure)
			select {
			case <-callbackDone:
			case <-time.After(time.Second):
				t.Fatal("Lite manual worker did not reach terminal recovery")
			}
			requireGovernedManualTerminalState(t, fixture.state)
		})
	}
}

func TestOrdinaryManualDependencyFailuresRemainBestEffort(t *testing.T) {
	for _, failure := range []string{"tenant", "knowledge", "knowledge-base", "failed-persistence"} {
		t.Run(failure, func(t *testing.T) {
			fixture := newGovernedManualWorkerFixture(t, failure)
			require.NoError(t, fixture.service.ProcessManualUpdate(
				context.Background(), governedManualWorkerTask(t, false),
			))
			_, targetStatus := fixture.state.statuses()
			require.Equal(t, types.ReleaseTargetBuilding, targetStatus)
		})
	}
}
