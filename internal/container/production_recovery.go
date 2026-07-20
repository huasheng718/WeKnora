package container

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const productionRecoveryBatchSize = 1000

func recoverPendingProductionRunsOnce(
	ctx context.Context,
	repository interfaces.ProductionRunRecoveryRepository,
	resumer interfaces.ProductionRunResumer,
	limit int,
) error {
	if repository == nil || resumer == nil {
		return errors.New("production recovery dependencies are required")
	}
	if limit < 1 || limit > productionRecoveryBatchSize {
		limit = productionRecoveryBatchSize
	}
	runs, err := repository.ListPendingWakeups(ctx, limit)
	if err != nil {
		return err
	}
	var joined error
	for _, run := range runs {
		if run == nil {
			continue
		}
		if _, err := resumer.Resume(ctx, run.TenantID, run.ID, run.Attempt); err != nil {
			joined = errors.Join(joined, fmt.Errorf("resume production run %s: %w", run.ID, err))
		}
	}
	return joined
}

func recoverPendingProductionRuns(repository interfaces.ProductionRunRecoveryRepository, resumer interfaces.ProductionRunResumer) {
	ctx := context.Background()
	if err := recoverPendingProductionRunsOnce(ctx, repository, resumer, productionRecoveryBatchSize); err != nil {
		logger.Errorf(ctx, "[Production] durable wakeup recovery failed: %v", err)
	}
}
