package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// ProductionRunCAS is the complete optimistic-lock identity for a run state.
// Status alone is insufficient because a reclaimed attempt must fence workers
// that still hold an older copy of the same step.
type ProductionRunCAS struct {
	Status        types.ProductionRunStatus
	Attempt       int
	CurrentStep   int
	WakeupVersion int
}

// ProductionRunPatch contains the mutable output of one orchestration step.
// Identity and status are deliberately excluded and handled by Transition.
type ProductionRunPatch struct {
	CurrentStep            *int
	StatePayload           types.JSON
	RawModelResponse       types.JSON
	RawModelResponseDigest *string
	OutputVersionID        *string
	ErrorCode              *string
	ErrorMessage           *string
	StartedAt              *time.Time
	CompletedAt            *time.Time
	IncrementWakeup        bool
}

// ProductionToolCallCAS fences approval decisions to the invocation attempt
// and persisted run step that originally requested them.
type ProductionToolCallCAS struct {
	Status      types.ProductionToolCallStatus
	Attempt     int
	CurrentStep int
}

// ProductionToolCallPatch contains terminal output for one exact invocation.
type ProductionToolCallPatch struct {
	ResponseSnapshot             types.JSON
	ResponseDigest               *string
	ResponseEvidenceID           *string
	ResponseEvidenceSourceItemID *string
	ErrorCode                    *string
	ErrorMessage                 *string
	StartedAt                    *time.Time
	CompletedAt                  *time.Time
}

// ProductionRunRepository persists tenant-scoped orchestration state. Every
// read or state decision carries tenant scope to prevent cross-tenant access.
type ProductionRunRepository interface {
	Create(ctx context.Context, run *types.ProductionRun) error
	Get(ctx context.Context, tenantID uint64, runID string) (*types.ProductionRun, error)
	Claim(ctx context.Context, tenantID uint64, runID string, expected ProductionRunCAS, leaseTTL time.Duration) (*types.ProductionRun, bool, error)
	Transition(ctx context.Context, tenantID uint64, runID string, expected ProductionRunCAS, to types.ProductionRunStatus, patch ProductionRunPatch) (*types.ProductionRun, bool, error)
	MarkWakeupEnqueued(ctx context.Context, tenantID uint64, runID string, attempt, currentStep, wakeupVersion int) (bool, error)
	CreateToolCall(ctx context.Context, call *types.ProductionToolCall) error
	GetToolCall(ctx context.Context, tenantID uint64, callID string) (*types.ProductionToolCall, error)
	ListToolCalls(ctx context.Context, tenantID uint64, runID string) ([]*types.ProductionToolCall, error)
	ResolveToolCall(ctx context.Context, tenantID uint64, callID string, expected ProductionToolCallCAS, decision types.ProductionToolCallStatus, actor string) (bool, error)
	TransitionToolCall(ctx context.Context, tenantID uint64, runID, callID string, expected ProductionToolCallCAS, to types.ProductionToolCallStatus, patch ProductionToolCallPatch) (bool, error)
}

// ProductionRunRecoveryRepository is the system-owned startup scan. It
// returns the tenant identity needed to re-enter the normal tenant-scoped
// Resume path and exposes no mutation surface of its own.
type ProductionRunRecoveryRepository interface {
	ListPendingWakeups(ctx context.Context, limit int) ([]*types.ProductionRun, error)
}

// ProductionRunOrchestrator processes a durable run wake-up. The payload is
// the tenant-scoped queue contract and must be validated before any read.
type ProductionRunOrchestrator interface {
	HandleRun(ctx context.Context, payload types.ProductionRunPayload) error
}

type ProductionRunResumer interface {
	Resume(ctx context.Context, tenantID uint64, runID string, attempt int) (bool, error)
}

type ProductionToolDecision string

const (
	ProductionToolDecisionApprove ProductionToolDecision = "approve"
	ProductionToolDecisionReject  ProductionToolDecision = "reject"
)

func (d ProductionToolDecision) IsValid() bool {
	return d == ProductionToolDecisionApprove || d == ProductionToolDecisionReject
}

type StartProductionDocumentRunInput struct {
	RunType types.ProductionRunType
	ModelID string
}

type StartProductionSourceSetCollectionInput struct {
	DocumentID string
	ModelID    string
}

// ProductionRunService is the HTTP-facing application boundary. It owns
// authorization, authoritative aggregate checks, required audit writes, and
// the post-commit durable wakeup handoff.
type ProductionRunService interface {
	StartDocumentRun(ctx context.Context, documentID string, input StartProductionDocumentRunInput) (*types.ProductionRun, error)
	StartSourceSetCollection(ctx context.Context, sourceSetID string, input StartProductionSourceSetCollectionInput) (*types.ProductionRun, error)
	GetRun(ctx context.Context, runID string) (*types.ProductionRun, error)
	DecideToolCall(ctx context.Context, callID string, decision ProductionToolDecision) (*types.ProductionToolCall, error)
}
