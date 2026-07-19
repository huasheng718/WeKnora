package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

const postgresProductionRunLockSQL = `
SELECT * FROM production_runs
WHERE tenant_id = $1 AND id = $2 AND status = $3 AND attempt = $4 AND current_step = $5
FOR UPDATE`

const postgresProductionRunClaimSQL = `
UPDATE production_runs
SET status = 'running',
    attempt = CASE WHEN status = 'running' THEN attempt + 1 ELSE attempt END,
    started_at = COALESCE(started_at, $6),
    updated_at = $6
WHERE tenant_id = $1 AND id = $2 AND status = $3 AND attempt = $4 AND current_step = $5
RETURNING *`

type productionRunRepository struct {
	db *gorm.DB
}

// NewProductionRunRepository creates the durable, tenant-scoped run store.
func NewProductionRunRepository(db *gorm.DB) interfaces.ProductionRunRepository {
	return &productionRunRepository{db: db}
}

func (r *productionRunRepository) Create(ctx context.Context, run *types.ProductionRun) error {
	if run == nil {
		return errors.New("production run is required")
	}
	if run.TenantID == 0 || run.ID == "" {
		return errors.New("production run tenant and id are required")
	}
	if !run.RunType.IsValid() || !run.Status.IsValid() {
		return errors.New("production run type and status must be valid")
	}
	if run.Attempt < 0 || run.CurrentStep < 0 {
		return errors.New("production run attempt and current step must be non-negative")
	}
	var err error
	run.StatePayload, err = canonicalProductionSnapshot(run.StatePayload, `{}`)
	if err != nil {
		return fmt.Errorf("canonicalize production run state: %w", err)
	}
	run.DocumentTypeSnapshot, err = canonicalProductionSnapshot(run.DocumentTypeSnapshot, "")
	if err != nil {
		return fmt.Errorf("canonicalize document type snapshot: %w", err)
	}
	if len(run.RawModelResponse) > 0 {
		run.RawModelResponse, err = canonicalProductionSnapshot(run.RawModelResponse, "")
		if err != nil {
			return fmt.Errorf("canonicalize raw model response: %w", err)
		}
		digest := productionSnapshotDigest(run.RawModelResponse)
		if run.RawModelResponseDigest != nil && *run.RawModelResponseDigest != digest {
			return errors.New("raw model response digest does not match canonical snapshot")
		}
		run.RawModelResponseDigest = &digest
	} else if run.RawModelResponseDigest != nil {
		return errors.New("raw model response digest requires a response snapshot")
	}
	return translateProductionWriteError(
		database.DBFromContext(ctx, r.db).WithContext(ctx).Create(run).Error,
	)
}

func (r *productionRunRepository) Get(
	ctx context.Context,
	tenantID uint64,
	runID string,
) (*types.ProductionRun, error) {
	var run types.ProductionRun
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, runID).
		First(&run).Error
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *productionRunRepository) Claim(
	ctx context.Context,
	tenantID uint64,
	runID string,
	expected interfaces.ProductionRunCAS,
	staleBefore time.Time,
) (*types.ProductionRun, bool, error) {
	if expected.Status != types.ProductionRunQueued && expected.Status != types.ProductionRunRunning {
		return nil, false, errors.New("only queued or expired running production runs can be claimed")
	}
	if expected.Attempt < 1 || expected.CurrentStep < 0 {
		return nil, false, errors.New("invalid production run claim fence")
	}
	if expected.Status == types.ProductionRunRunning && staleBefore.IsZero() {
		return nil, false, errors.New("running production run claims require a stale boundary")
	}

	now := time.Now().UTC()
	db := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.ProductionRun{}).
		Where("tenant_id = ? AND id = ?", tenantID, runID).
		Where("status = ? AND attempt = ? AND current_step = ?", expected.Status, expected.Attempt, expected.CurrentStep)
	if expected.Status == types.ProductionRunRunning {
		db = db.Where("updated_at <= ?", staleBefore)
	}
	updates := map[string]any{
		"status":     types.ProductionRunRunning,
		"started_at": gorm.Expr("COALESCE(started_at, ?)", now),
		"updated_at": now,
	}
	if expected.Status == types.ProductionRunRunning {
		updates["attempt"] = gorm.Expr("attempt + 1")
	}
	result := db.Updates(updates)
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, false, nil
	}
	claimed, err := r.Get(ctx, tenantID, runID)
	if err != nil {
		return nil, false, err
	}
	return claimed, true, nil
}

func (r *productionRunRepository) Transition(
	ctx context.Context,
	tenantID uint64,
	runID string,
	expected interfaces.ProductionRunCAS,
	to types.ProductionRunStatus,
	patch interfaces.ProductionRunPatch,
) (bool, error) {
	if err := validateProductionRunTransition(expected.Status, to); err != nil {
		return false, err
	}
	if expected.Attempt < 1 || expected.CurrentStep < 0 {
		return false, errors.New("invalid production run transition fence")
	}
	updates := map[string]any{"status": to, "updated_at": time.Now().UTC()}
	if patch.CurrentStep != nil {
		if *patch.CurrentStep < expected.CurrentStep {
			return false, errors.New("production run current step cannot move backwards")
		}
		updates["current_step"] = *patch.CurrentStep
	}
	if len(patch.StatePayload) > 0 {
		canonical, err := canonicalProductionSnapshot(patch.StatePayload, "")
		if err != nil {
			return false, fmt.Errorf("canonicalize production run state: %w", err)
		}
		updates["state_payload"] = canonical
	}
	if len(patch.RawModelResponse) > 0 {
		canonical, err := canonicalProductionSnapshot(patch.RawModelResponse, "")
		if err != nil {
			return false, fmt.Errorf("canonicalize raw model response: %w", err)
		}
		digest := productionSnapshotDigest(canonical)
		if patch.RawModelResponseDigest != nil && *patch.RawModelResponseDigest != digest {
			return false, errors.New("raw model response digest does not match canonical snapshot")
		}
		updates["raw_model_response"] = canonical
		updates["raw_model_response_digest"] = digest
	} else if patch.RawModelResponseDigest != nil {
		return false, errors.New("raw model response digest requires a response snapshot")
	}
	if patch.OutputVersionID != nil {
		updates["output_version_id"] = *patch.OutputVersionID
	}
	if patch.ErrorCode != nil {
		updates["error_code"] = *patch.ErrorCode
	}
	if patch.ErrorMessage != nil {
		updates["error_message"] = *patch.ErrorMessage
	}
	if patch.StartedAt != nil {
		updates["started_at"] = *patch.StartedAt
	}
	if patch.CompletedAt != nil {
		updates["completed_at"] = *patch.CompletedAt
	}
	if isTerminalProductionRunStatus(to) && patch.CompletedAt == nil {
		return false, errors.New("terminal production run transition requires completed_at")
	}
	if !isTerminalProductionRunStatus(to) && patch.CompletedAt != nil {
		return false, errors.New("nonterminal production run transition cannot set completed_at")
	}

	result := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.ProductionRun{}).
		Where("tenant_id = ? AND id = ?", tenantID, runID).
		Where("status = ? AND attempt = ? AND current_step = ?", expected.Status, expected.Attempt, expected.CurrentStep).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func (r *productionRunRepository) CreateToolCall(
	ctx context.Context,
	call *types.ProductionToolCall,
) error {
	if call == nil {
		return errors.New("production tool call is required")
	}
	if call.TenantID == 0 || call.ID == "" || call.RunID == "" {
		return errors.New("production tool call tenant, run and id are required")
	}
	if !call.ProviderType.IsValid() || !call.Status.IsValid() || !call.ApprovalStatus.IsValid() {
		return errors.New("production tool call provider and statuses must be valid")
	}
	canonical, err := canonicalProductionSnapshot(call.RequestSnapshot, "")
	if err != nil {
		return fmt.Errorf("canonicalize production tool request: %w", err)
	}
	call.RequestSnapshot = canonical
	digest := productionSnapshotDigest(canonical)
	if call.RequestDigest != "" && call.RequestDigest != digest {
		return errors.New("production tool request digest does not match canonical snapshot")
	}
	call.RequestDigest = digest
	if len(call.ResponseSnapshot) > 0 {
		canonical, err = canonicalProductionSnapshot(call.ResponseSnapshot, "")
		if err != nil {
			return fmt.Errorf("canonicalize production tool response: %w", err)
		}
		call.ResponseSnapshot = canonical
		responseDigest := productionSnapshotDigest(canonical)
		if call.ResponseDigest != nil && *call.ResponseDigest != responseDigest {
			return errors.New("production tool response digest does not match canonical snapshot")
		}
		call.ResponseDigest = &responseDigest
	} else if call.ResponseDigest != nil {
		return errors.New("production tool response digest requires a response snapshot")
	}
	return translateProductionWriteError(
		database.DBFromContext(ctx, r.db).WithContext(ctx).Create(call).Error,
	)
}

func (r *productionRunRepository) GetToolCall(
	ctx context.Context,
	tenantID uint64,
	callID string,
) (*types.ProductionToolCall, error) {
	var call types.ProductionToolCall
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, callID).
		First(&call).Error
	if err != nil {
		return nil, err
	}
	return &call, nil
}

func (r *productionRunRepository) ListToolCalls(
	ctx context.Context,
	tenantID uint64,
	runID string,
) ([]*types.ProductionToolCall, error) {
	var calls []*types.ProductionToolCall
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Where("tenant_id = ? AND run_id = ?", tenantID, runID).
		Order("attempt ASC, current_step ASC, id ASC").
		Find(&calls).Error
	return calls, err
}

func (r *productionRunRepository) ResolveToolCall(
	ctx context.Context,
	tenantID uint64,
	callID string,
	expected interfaces.ProductionToolCallCAS,
	decision types.ProductionToolCallStatus,
	actor string,
) (bool, error) {
	if decision != types.ProductionToolCallApproved && decision != types.ProductionToolCallRejected {
		return false, errors.New("production tool call decision must be approved or rejected")
	}
	if strings.TrimSpace(actor) == "" {
		return false, errors.New("production tool call decision actor is required")
	}
	if expected.Status != types.ProductionToolCallPendingApproval || expected.Attempt < 1 || expected.CurrentStep < 0 {
		return false, errors.New("invalid production tool call decision fence")
	}
	now := time.Now().UTC()
	updates := map[string]any{"status": decision, "updated_at": now}
	if decision == types.ProductionToolCallApproved {
		updates["approval_status"] = types.ProductionToolApprovalApproved
		updates["approved_by"] = actor
		updates["approved_at"] = now
	} else {
		updates["approval_status"] = types.ProductionToolApprovalRejected
		updates["rejected_by"] = actor
		updates["rejected_at"] = now
		updates["completed_at"] = now
	}
	result := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.ProductionToolCall{}).
		Where("tenant_id = ? AND id = ?", tenantID, callID).
		Where("status = ? AND approval_status = ? AND attempt = ? AND current_step = ?",
			expected.Status, types.ProductionToolApprovalPending, expected.Attempt, expected.CurrentStep).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func validateProductionRunTransition(from, to types.ProductionRunStatus) error {
	if !from.IsValid() || !to.IsValid() {
		return errors.New("invalid production run status")
	}
	if isTerminalProductionRunStatus(from) {
		return errors.New("terminal production runs are immutable")
	}
	allowed := false
	switch from {
	case types.ProductionRunQueued:
		allowed = to == types.ProductionRunRunning || to == types.ProductionRunCancelled
	case types.ProductionRunRunning:
		allowed = to == types.ProductionRunQueued || to == types.ProductionRunWaitingApproval ||
			to == types.ProductionRunCompleted || to == types.ProductionRunFailed || to == types.ProductionRunCancelled
	case types.ProductionRunWaitingApproval:
		allowed = to == types.ProductionRunQueued || to == types.ProductionRunFailed || to == types.ProductionRunCancelled
	}
	if !allowed {
		return fmt.Errorf("invalid production run transition %s -> %s", from, to)
	}
	return nil
}

func isTerminalProductionRunStatus(status types.ProductionRunStatus) bool {
	return status == types.ProductionRunCompleted || status == types.ProductionRunFailed || status == types.ProductionRunCancelled
}

func canonicalProductionSnapshot(raw types.JSON, fallback string) (types.JSON, error) {
	if len(raw) == 0 {
		if fallback == "" {
			return nil, errors.New("JSON snapshot is required")
		}
		raw = types.JSON(fallback)
	}
	if err := rejectProductionCredentials(raw); err != nil {
		return nil, err
	}
	return types.CanonicalProductionJSON(raw)
}

func productionSnapshotDigest(raw types.JSON) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func rejectProductionCredentials(raw types.JSON) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var inspect func(any) error
	inspect = func(candidate any) error {
		switch typed := candidate.(type) {
		case map[string]any:
			for key, nested := range typed {
				normalized := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(key))
				switch normalized {
				case "api_key", "apikey", "access_key", "authorization", "credential", "credentials",
					"password", "passwd", "private_key", "refresh_token", "secret", "token":
					return fmt.Errorf("credential field %q is not allowed in production snapshots", key)
				}
				if err := inspect(nested); err != nil {
					return err
				}
			}
		case []any:
			for _, nested := range typed {
				if err := inspect(nested); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return inspect(value)
}

var _ interfaces.ProductionRunRepository = (*productionRunRepository)(nil)
