package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	appservice "github.com/Tencent/WeKnora/internal/application/service"
	appRouter "github.com/Tencent/WeKnora/internal/router"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type productionRecoveryResumerStub struct {
	runs      []string
	tenants   []uint64
	attempts  []int
	failRunID string
}

func (s *productionRecoveryResumerStub) Resume(_ context.Context, tenantID uint64, runID string, attempt int) (bool, error) {
	s.runs = append(s.runs, runID)
	s.tenants = append(s.tenants, tenantID)
	s.attempts = append(s.attempts, attempt)
	if runID == s.failRunID {
		return false, errors.New("forced recovery failure")
	}
	return true, nil
}

func TestRecoverPendingProductionRunsRearmsOnlyDBExpiredRunningLease(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.ProductionRun{}))
	now := time.Now().UTC()
	for _, run := range []*types.ProductionRun{
		{ID: "75100000-0000-4000-8000-000000000001", Status: types.ProductionRunRunning, UpdatedAt: now.Add(-10 * time.Minute)},
		{ID: "75100000-0000-4000-8000-000000000002", Status: types.ProductionRunRunning, UpdatedAt: now},
		{ID: "75100000-0000-4000-8000-000000000003", Status: types.ProductionRunWaitingApproval, UpdatedAt: now.Add(-10 * time.Minute)},
		{ID: "75100000-0000-4000-8000-000000000004", Status: types.ProductionRunCompleted, UpdatedAt: now.Add(-10 * time.Minute), CompletedAt: &now},
	} {
		run.TenantID = 7
		run.ProjectID = "75000000-0000-4000-8000-000000000010"
		run.DocumentID = "75000000-0000-4000-8000-000000000011"
		run.SourceSetID = "75000000-0000-4000-8000-000000000012"
		run.RunType = types.ProductionRunWrite
		run.Attempt = 1
		run.WakeupVersion = 1
		run.WakeupEnqueuedVersion = 1
		run.StatePayload = types.JSON(`{}`)
		run.DocumentTypeSnapshot = types.JSON(`{}`)
		run.ModelID = "model"
		run.IdempotencyKey = run.ID
		require.NoError(t, db.Create(run).Error)
	}
	resumer := &productionRecoveryResumerStub{}

	err = recoverPendingProductionRunsOnce(context.Background(), apprepository.NewProductionRunRecoveryRepository(db), resumer, 100)

	require.NoError(t, err)
	require.Equal(t, []string{"75100000-0000-4000-8000-000000000001"}, resumer.runs)
	require.Equal(t, []int{2}, resumer.attempts)
	var recovered types.ProductionRun
	require.NoError(t, db.First(&recovered, "id = ?", resumer.runs[0]).Error)
	require.Equal(t, types.ProductionRunQueued, recovered.Status)
	require.Equal(t, 2, recovered.Attempt)
	require.Equal(t, 2, recovered.WakeupVersion)
	require.Equal(t, 1, recovered.WakeupEnqueuedVersion)
}

func TestLiteProductionRecoveryReopensFileAndCompletesExpiredRunningRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lite-production-recovery.db")
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open("file:"+path+"?_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, err)
		return db
	}
	processA := open()
	require.NoError(t, processA.AutoMigrate(&types.ProductionRun{}, &types.ProductionToolCall{}))
	workflow := types.JSON(`{"steps":[],"version":1}`)
	sum := sha256.Sum256(workflow)
	run := &types.ProductionRun{
		ID: "75200000-0000-4000-8000-000000000001", TenantID: 7,
		ProjectID: "75200000-0000-4000-8000-000000000002", SourceSetID: "75200000-0000-4000-8000-000000000003",
		RunType: types.ProductionRunCollect, Status: types.ProductionRunQueued, Attempt: 1,
		WakeupVersion: 1, WakeupEnqueuedVersion: 1, StatePayload: types.JSON(`{}`), ModelID: "model",
		DocumentTypeSnapshot: types.JSON(`{"skill_bindings":{"skills":[],"version":1}}`),
		WorkflowPlanSnapshot: workflow, WorkflowPlanDigest: hex.EncodeToString(sum[:]), IdempotencyKey: "lite-recovery",
	}
	require.NoError(t, processA.Create(run).Error)
	runsA := apprepository.NewProductionRunRepository(processA)
	claimed, won, err := runsA.Claim(context.Background(), 7, run.ID, interfaces.ProductionRunCAS{
		Status: run.Status, Attempt: run.Attempt, CurrentStep: run.CurrentStep, WakeupVersion: run.WakeupVersion,
	}, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.Equal(t, types.ProductionRunRunning, claimed.Status)
	require.NoError(t, processA.Model(&types.ProductionRun{}).Where("id = ?", run.ID).
		UpdateColumn("updated_at", time.Now().UTC().Add(-10*time.Minute)).Error)
	sqlA, err := processA.DB()
	require.NoError(t, err)
	require.NoError(t, sqlA.Close())

	processB := open()
	runsB := apprepository.NewProductionRunRepository(processB)
	syncExec := appRouter.NewSyncTaskExecutor()
	executor := appservice.NewProductionStepExecutor(
		runsB, &appservice.ProductionSkillAdapter{}, &appservice.ProductionMCPAdapter{},
		&appservice.ProductionDataSourceAdapter{}, &appservice.ProductionWriter{}, &appservice.ProductionValidator{},
	)
	orchestrator := appservice.NewProductionOrchestrator(
		runsB, apprepository.NewProductionUnitOfWork(processB), executor, syncExec,
	)
	handler := appRouter.NewProductionRunTaskHandler(orchestrator, runsB)
	handlerErrors := make(chan error, 1)
	syncExec.RegisterHandler(types.TypeProductionCollect, func(ctx context.Context, task *asynq.Task) error {
		handleErr := handler.Handle(ctx, task)
		select {
		case handlerErrors <- handleErr:
		default:
		}
		return handleErr
	})

	err = recoverPendingProductionRunsOnce(
		context.Background(), apprepository.NewProductionRunRecoveryRepository(processB), orchestrator, 100,
	)
	require.NoError(t, err)
	select {
	case handleErr := <-handlerErrors:
		require.NoError(t, handleErr)
	case <-time.After(3 * time.Second):
		t.Fatal("recovered Lite handler did not run")
	}
	var persisted *types.ProductionRun
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		persisted, err = runsB.Get(context.Background(), 7, run.ID)
		if err == nil && persisted.Status == types.ProductionRunCompleted && persisted.Attempt == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NotNil(t, persisted)
	require.Equal(t, types.ProductionRunCompleted, persisted.Status, "run=%+v", persisted)
	require.Equal(t, 2, persisted.Attempt)
	sqlB, err := processB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlB.Close())
}

func TestRecoverPendingProductionRunsOnlyRearmsDurableWakeupLag(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.ProductionRun{}))
	for _, run := range []*types.ProductionRun{
		{ID: "75000000-0000-4000-8000-000000000001", TenantID: 7, Status: types.ProductionRunQueued, Attempt: 1, WakeupVersion: 2, WakeupEnqueuedVersion: 1},
		{ID: "75000000-0000-4000-8000-000000000002", TenantID: 7, Status: types.ProductionRunQueued, Attempt: 1, WakeupVersion: 2, WakeupEnqueuedVersion: 2},
		{ID: "75000000-0000-4000-8000-000000000003", TenantID: 7, Status: types.ProductionRunCompleted, Attempt: 1, WakeupVersion: 2, WakeupEnqueuedVersion: 1},
	} {
		run.ProjectID = "75000000-0000-4000-8000-000000000010"
		run.DocumentID = "75000000-0000-4000-8000-000000000011"
		run.SourceSetID = "75000000-0000-4000-8000-000000000012"
		run.RunType = types.ProductionRunWrite
		run.StatePayload = types.JSON(`{}`)
		run.DocumentTypeSnapshot = types.JSON(`{}`)
		run.ModelID = "75000000-0000-4000-8000-000000000013"
		run.IdempotencyKey = run.ID
		require.NoError(t, db.Create(run).Error)
	}
	resumer := &productionRecoveryResumerStub{}

	err = recoverPendingProductionRunsOnce(context.Background(), apprepository.NewProductionRunRecoveryRepository(db), resumer, 100)

	require.NoError(t, err)
	require.Equal(t, []string{"75000000-0000-4000-8000-000000000001"}, resumer.runs)
}

func TestRecoverPendingProductionRunsDrainsEveryCursorPageDespiteFailure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.ProductionRun{}))
	const total = 1005
	for index := 0; index < total; index++ {
		run := &types.ProductionRun{
			ID: fmt.Sprintf("%036d", index+1), TenantID: uint64(index%3 + 1),
			ProjectID:   "75000000-0000-4000-8000-000000000010",
			DocumentID:  "75000000-0000-4000-8000-000000000011",
			SourceSetID: "75000000-0000-4000-8000-000000000012",
			RunType:     types.ProductionRunWrite, Status: types.ProductionRunQueued, Attempt: 1,
			WakeupVersion: 2, WakeupEnqueuedVersion: 1, StatePayload: types.JSON(`{}`),
			DocumentTypeSnapshot: types.JSON(`{}`), ModelID: "model", IdempotencyKey: fmt.Sprintf("recovery-%d", index),
		}
		require.NoError(t, db.Create(run).Error)
	}
	resumer := &productionRecoveryResumerStub{failRunID: fmt.Sprintf("%036d", 3)}

	err = recoverPendingProductionRunsOnce(context.Background(), apprepository.NewProductionRunRecoveryRepository(db), resumer, 100)

	require.ErrorContains(t, err, "forced recovery failure")
	require.Len(t, resumer.runs, total)
	require.Len(t, resumer.tenants, total)
	require.Equal(t, uint64(1), resumer.tenants[0])
	require.Equal(t, uint64(3), resumer.tenants[1004])
}
