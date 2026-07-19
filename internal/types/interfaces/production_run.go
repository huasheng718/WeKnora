package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// ProductionRunRepository persists tenant-scoped orchestration state. Every
// read or state decision carries tenant scope to prevent cross-tenant access.
type ProductionRunRepository interface {
	Create(ctx context.Context, run *types.ProductionRun) error
	Get(ctx context.Context, tenantID uint64, runID string) (*types.ProductionRun, error)
	Transition(ctx context.Context, tenantID uint64, runID string, from, to types.ProductionRunStatus, patch types.JSONMap) (bool, error)
	CreateToolCall(ctx context.Context, call *types.ProductionToolCall) error
	GetToolCall(ctx context.Context, tenantID uint64, callID string) (*types.ProductionToolCall, error)
	ResolveToolCall(ctx context.Context, tenantID uint64, callID string, decision types.ProductionToolCallStatus, actor string) (bool, error)
}

// ProductionRunOrchestrator processes a durable run wake-up. The payload is
// the tenant-scoped queue contract and must be validated before any read.
type ProductionRunOrchestrator interface {
	HandleRun(ctx context.Context, payload types.ProductionRunPayload) error
}
