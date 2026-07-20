package container

import (
	"context"
	"errors"
	"fmt"
	"testing"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type productionRecoveryResumerStub struct {
	runs      []string
	tenants   []uint64
	failRunID string
}

func (s *productionRecoveryResumerStub) Resume(_ context.Context, tenantID uint64, runID string, _ int) (bool, error) {
	s.runs = append(s.runs, runID)
	s.tenants = append(s.tenants, tenantID)
	if runID == s.failRunID {
		return false, errors.New("forced recovery failure")
	}
	return true, nil
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
