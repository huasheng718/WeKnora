package container

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type productionCleanupRecoveryLifecycleStub struct {
	starts int
	stops  int
}

func (s *productionCleanupRecoveryLifecycleStub) Start(context.Context) { s.starts++ }
func (s *productionCleanupRecoveryLifecycleStub) Stop()                 { s.stops++ }

type productionCleanupRecoveryCleanerStub struct {
	interfaces.ResourceCleaner
	name    string
	cleanup types.CleanupFunc
}

func (s *productionCleanupRecoveryCleanerStub) RegisterWithName(name string, cleanup types.CleanupFunc) {
	s.name = name
	s.cleanup = cleanup
}

func TestStartProductionCleanupRecoveryLifecycleStartsAndRegistersShutdown(t *testing.T) {
	runner := &productionCleanupRecoveryLifecycleStub{}
	cleaner := &productionCleanupRecoveryCleanerStub{}

	startProductionCleanupRecoveryLifecycle(runner, cleaner)

	require.Equal(t, 1, runner.starts)
	require.Equal(t, "ProductionCleanupRecoveryRunner", cleaner.name)
	require.NotNil(t, cleaner.cleanup)
	require.NoError(t, cleaner.cleanup())
	require.Equal(t, 1, runner.stops)
}
