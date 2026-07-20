package container

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/dig"
)

func TestBuildContainerResolvesProductionProjectionDependencies(t *testing.T) {
	for _, tc := range []struct {
		name      string
		redisAddr string
	}{
		{name: "lite"},
		{name: "redis", redisAddr: "redis:6379"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("REDIS_ADDR", tc.redisAddr)
			container := dig.New(dig.DryRun(true))

			require.NotPanics(t, func() {
				BuildContainer(container)
			})
		})
	}
}
