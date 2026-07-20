package container

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const productionRecoveryBatchSize = 1000
const productionRecoveryLeaseTTL = 5 * time.Minute

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
	var joined error
	afterID := ""
	for {
		runs, err := repository.ListPendingWakeups(ctx, afterID, limit, productionRecoveryLeaseTTL)
		if err != nil {
			return errors.Join(joined, err)
		}
		if len(runs) == 0 {
			return joined
		}
		for _, run := range runs {
			if run == nil {
				continue
			}
			candidate := run
			if run.Status == types.ProductionRunRunning {
				var changed bool
				candidate, changed, err = repository.RearmExpiredRunning(
					ctx, run.TenantID, run.ID, run.Attempt, productionRecoveryLeaseTTL,
				)
				if err != nil {
					joined = errors.Join(joined, fmt.Errorf("rearm production run %s: %w", run.ID, err))
					continue
				}
				if !changed || candidate == nil {
					continue
				}
			}
			if _, err := resumer.Resume(ctx, candidate.TenantID, candidate.ID, candidate.Attempt); err != nil {
				joined = errors.Join(joined, fmt.Errorf("resume production run %s: %w", run.ID, err))
			}
		}
		nextID := runs[len(runs)-1].ID
		if nextID == "" || nextID <= afterID {
			return errors.Join(joined, errors.New("production recovery cursor did not advance"))
		}
		afterID = nextID
	}
}

func recoverPendingProductionRuns(repository interfaces.ProductionRunRecoveryRepository, resumer interfaces.ProductionRunResumer) {
	ctx := context.Background()
	if err := recoverPendingProductionRunsOnce(ctx, repository, resumer, productionRecoveryBatchSize); err != nil {
		logger.Errorf(ctx, "[Production] durable wakeup recovery failed: %v", err)
	}
}
