package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const postgresProductionRunLockSQL = `
SELECT * FROM production_runs
WHERE tenant_id = $1 AND id = $2 AND status = $3 AND attempt = $4 AND current_step = $5 AND wakeup_version = $6
FOR UPDATE`

const postgresProductionRunClaimSQL = `
UPDATE production_runs
SET status = 'running',
    attempt = CASE WHEN status = 'running' THEN attempt + 1 ELSE attempt END,
    started_at = COALESCE(started_at, CURRENT_TIMESTAMP),
    updated_at = CURRENT_TIMESTAMP
WHERE tenant_id = $1 AND id = $2 AND status = $3 AND attempt = $4
  AND current_step = $5 AND wakeup_version = $6
  AND (status = 'queued' OR updated_at <= CURRENT_TIMESTAMP - ($7 * INTERVAL '1 second'))
RETURNING *`

const sqliteProductionRunClaimSQL = `
UPDATE production_runs
SET status = 'running',
    attempt = CASE WHEN status = 'running' THEN attempt + 1 ELSE attempt END,
    started_at = COALESCE(started_at, CURRENT_TIMESTAMP),
    updated_at = CURRENT_TIMESTAMP
WHERE tenant_id = ? AND id = ? AND status = ? AND attempt = ?
  AND current_step = ? AND wakeup_version = ?
  AND (status = 'queued' OR julianday(updated_at) <= julianday(CURRENT_TIMESTAMP, ?))
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
	for name, value := range map[string]string{
		"id": run.ID, "project_id": run.ProjectID, "document_id": run.DocumentID, "source_set_id": run.SourceSetID,
	} {
		if err := requireCanonicalProductionUUID(name, value); err != nil {
			return err
		}
	}
	for name, value := range map[string]*string{
		"input_version_id": run.InputVersionID, "output_version_id": run.OutputVersionID,
	} {
		if value != nil {
			if err := requireCanonicalProductionUUID(name, *value); err != nil {
				return err
			}
		}
	}
	if !run.RunType.IsValid() || !run.Status.IsValid() {
		return errors.New("production run type and status must be valid")
	}
	if run.Attempt < 0 || run.CurrentStep < 0 {
		return errors.New("production run attempt and current step must be non-negative")
	}
	if run.WakeupVersion < 0 || run.WakeupEnqueuedVersion < 0 || run.WakeupEnqueuedVersion > run.WakeupVersion {
		return errors.New("production run wakeup versions are invalid")
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
	leaseTTL time.Duration,
) (*types.ProductionRun, bool, error) {
	if expected.Status != types.ProductionRunQueued && expected.Status != types.ProductionRunRunning {
		return nil, false, errors.New("only queued or expired running production runs can be claimed")
	}
	if expected.Attempt < 1 || expected.CurrentStep < 0 || expected.WakeupVersion < 1 {
		return nil, false, fmt.Errorf("invalid production run claim fence: attempt=%d current_step=%d wakeup_version=%d",
			expected.Attempt, expected.CurrentStep, expected.WakeupVersion)
	}
	if leaseTTL <= 0 {
		return nil, false, errors.New("production run claim lease must be positive")
	}
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	var claimed types.ProductionRun
	var result *gorm.DB
	seconds := leaseTTL.Seconds()
	if db.Dialector.Name() == "postgres" {
		result = db.Raw(postgresProductionRunClaimSQL,
			tenantID, runID, expected.Status, expected.Attempt, expected.CurrentStep, expected.WakeupVersion, seconds,
		).Scan(&claimed)
	} else {
		modifier := fmt.Sprintf("-%g seconds", seconds)
		result = db.Raw(sqliteProductionRunClaimSQL,
			tenantID, runID, expected.Status, expected.Attempt, expected.CurrentStep, expected.WakeupVersion, modifier,
		).Scan(&claimed)
	}
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 1 {
		return &claimed, true, nil
	}
	active, err := r.hasActiveLease(ctx, tenantID, runID, leaseTTL)
	if err != nil {
		return nil, false, err
	}
	if active {
		return nil, false, &types.ProductionRunLeaseActiveError{RetryAfter: leaseTTL}
	}
	return nil, false, nil
}

func (r *productionRunRepository) Transition(
	ctx context.Context,
	tenantID uint64,
	runID string,
	expected interfaces.ProductionRunCAS,
	to types.ProductionRunStatus,
	patch interfaces.ProductionRunPatch,
) (*types.ProductionRun, bool, error) {
	rawAuditTransition := expected.Status == types.ProductionRunRunning && to == types.ProductionRunRunning
	if rawAuditTransition {
		if len(patch.RawModelResponse) == 0 || patch.RawModelResponseDigest == nil ||
			patch.CurrentStep != nil || len(patch.StatePayload) != 0 || patch.OutputVersionID != nil ||
			patch.ErrorCode != nil || patch.ErrorMessage != nil || patch.StartedAt != nil ||
			patch.CompletedAt != nil || patch.IncrementWakeup {
			return nil, false, errors.New("running self-transition is reserved for raw model response audit persistence")
		}
	} else if err := validateProductionRunTransition(expected.Status, to); err != nil {
		return nil, false, err
	}
	if expected.Attempt < 1 || expected.CurrentStep < 0 || expected.WakeupVersion < 1 {
		return nil, false, errors.New("invalid production run transition fence")
	}
	if (to == types.ProductionRunQueued) != patch.IncrementWakeup {
		return nil, false, errors.New("queued production run transitions must increment wakeup exactly once")
	}
	updates := map[string]any{"status": to, "updated_at": time.Now().UTC()}
	if patch.IncrementWakeup {
		updates["wakeup_version"] = gorm.Expr("wakeup_version + 1")
	}
	if patch.CurrentStep != nil {
		if *patch.CurrentStep < expected.CurrentStep {
			return nil, false, errors.New("production run current step cannot move backwards")
		}
		updates["current_step"] = *patch.CurrentStep
	}
	if len(patch.StatePayload) > 0 {
		canonical, err := canonicalProductionSnapshot(patch.StatePayload, "")
		if err != nil {
			return nil, false, fmt.Errorf("canonicalize production run state: %w", err)
		}
		updates["state_payload"] = canonical
	}
	if len(patch.RawModelResponse) > 0 {
		canonical, err := canonicalProductionSnapshot(patch.RawModelResponse, "")
		if err != nil {
			return nil, false, fmt.Errorf("canonicalize raw model response: %w", err)
		}
		digest := productionSnapshotDigest(canonical)
		if patch.RawModelResponseDigest != nil && *patch.RawModelResponseDigest != digest {
			return nil, false, errors.New("raw model response digest does not match canonical snapshot")
		}
		updates["raw_model_response"] = canonical
		updates["raw_model_response_digest"] = digest
	} else if patch.RawModelResponseDigest != nil {
		return nil, false, errors.New("raw model response digest requires a response snapshot")
	}
	if patch.OutputVersionID != nil {
		if err := requireCanonicalProductionUUID("output_version_id", *patch.OutputVersionID); err != nil {
			return nil, false, err
		}
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
		return nil, false, errors.New("terminal production run transition requires completed_at")
	}
	if !isTerminalProductionRunStatus(to) && patch.CompletedAt != nil {
		return nil, false, errors.New("nonterminal production run transition cannot set completed_at")
	}
	var transitioned types.ProductionRun
	query := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&transitioned).Clauses(clause.Returning{}).
		Where("tenant_id = ? AND id = ?", tenantID, runID).
		Where("status = ? AND attempt = ? AND current_step = ? AND wakeup_version = ?",
			expected.Status, expected.Attempt, expected.CurrentStep, expected.WakeupVersion)
	if rawAuditTransition {
		query = query.Where("raw_model_response IS NULL AND raw_model_response_digest IS NULL")
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, false, nil
	}
	return &transitioned, true, nil
}

func (r *productionRunRepository) MarkWakeupEnqueued(
	ctx context.Context,
	tenantID uint64,
	runID string,
	attempt, currentStep,
	wakeupVersion int,
) (bool, error) {
	if attempt < 1 || currentStep < 0 || wakeupVersion < 1 {
		return false, errors.New("wakeup mark fence is invalid")
	}
	result := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.ProductionRun{}).
		Where("tenant_id = ? AND id = ?", tenantID, runID).
		Where("attempt = ? AND current_step = ? AND wakeup_version = ?", attempt, currentStep, wakeupVersion).
		Where("status IN ?", []types.ProductionRunStatus{types.ProductionRunQueued, types.ProductionRunRunning}).
		Where("wakeup_enqueued_version < ?", wakeupVersion).
		UpdateColumn("wakeup_enqueued_version", wakeupVersion)
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
	for name, value := range map[string]string{
		"id": call.ID, "run_id": call.RunID, "project_id": call.ProjectID,
		"document_id": call.DocumentID, "source_set_id": call.SourceSetID,
	} {
		if err := requireCanonicalProductionUUID(name, value); err != nil {
			return err
		}
	}
	for name, value := range map[string]*string{
		"response_evidence_id":             call.ResponseEvidenceID,
		"response_evidence_source_item_id": call.ResponseEvidenceSourceItemID,
		"approved_by":                      call.ApprovedBy,
		"rejected_by":                      call.RejectedBy,
	} {
		if value != nil {
			if err := requireCanonicalProductionUUID(name, *value); err != nil {
				return err
			}
		}
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
	if err := requireCanonicalProductionUUID("actor", actor); err != nil {
		return false, err
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

func (r *productionRunRepository) TransitionToolCall(
	ctx context.Context,
	tenantID uint64,
	runID, callID string,
	expected interfaces.ProductionToolCallCAS,
	to types.ProductionToolCallStatus,
	patch interfaces.ProductionToolCallPatch,
) (bool, error) {
	if err := requireCanonicalProductionUUID("run_id", runID); err != nil {
		return false, err
	}
	if err := requireCanonicalProductionUUID("call_id", callID); err != nil {
		return false, err
	}
	if expected.Attempt < 1 || expected.CurrentStep < 0 {
		return false, errors.New("invalid production tool call transition fence")
	}
	if err := validateProductionToolCallTransition(expected.Status, to); err != nil {
		return false, err
	}
	updates := map[string]any{"status": to, "updated_at": time.Now().UTC()}
	if patch.StartedAt != nil {
		updates["started_at"] = *patch.StartedAt
	}
	if patch.ErrorCode != nil {
		updates["error_code"] = *patch.ErrorCode
	}
	if patch.ErrorMessage != nil {
		updates["error_message"] = *patch.ErrorMessage
	}
	if patch.CompletedAt != nil {
		updates["completed_at"] = *patch.CompletedAt
	}
	if to == types.ProductionToolCallCompleted {
		if patch.CompletedAt == nil || len(patch.ResponseSnapshot) == 0 ||
			patch.ResponseEvidenceID == nil || patch.ResponseEvidenceSourceItemID == nil {
			return false, errors.New("completed production tool call requires response, evidence and completed_at")
		}
		if err := requireCanonicalProductionUUID("response_evidence_id", *patch.ResponseEvidenceID); err != nil {
			return false, err
		}
		if err := requireCanonicalProductionUUID("response_evidence_source_item_id", *patch.ResponseEvidenceSourceItemID); err != nil {
			return false, err
		}
		canonical, err := canonicalProductionSnapshot(patch.ResponseSnapshot, "")
		if err != nil {
			return false, fmt.Errorf("canonicalize production tool response: %w", err)
		}
		digest := productionSnapshotDigest(canonical)
		if patch.ResponseDigest != nil && *patch.ResponseDigest != digest {
			return false, errors.New("production tool response digest does not match canonical snapshot")
		}
		updates["response_snapshot"] = canonical
		updates["response_digest"] = digest
		updates["response_evidence_id"] = *patch.ResponseEvidenceID
		updates["response_evidence_source_item_id"] = *patch.ResponseEvidenceSourceItemID
	} else if to == types.ProductionToolCallFailed && patch.CompletedAt == nil {
		return false, errors.New("failed production tool call requires completed_at")
	}
	result := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.ProductionToolCall{}).
		Where("tenant_id = ? AND run_id = ? AND id = ?", tenantID, runID, callID).
		Where("status = ? AND attempt = ? AND current_step = ?",
			expected.Status, expected.Attempt, expected.CurrentStep).
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

func validateProductionToolCallTransition(from, to types.ProductionToolCallStatus) error {
	if !from.IsValid() || !to.IsValid() {
		return errors.New("invalid production tool call status")
	}
	allowed := false
	switch from {
	case types.ProductionToolCallPlanned:
		allowed = to == types.ProductionToolCallExecuting || to == types.ProductionToolCallFailed
	case types.ProductionToolCallApproved:
		allowed = to == types.ProductionToolCallExecuting || to == types.ProductionToolCallCompleted || to == types.ProductionToolCallFailed
	case types.ProductionToolCallExecuting:
		allowed = to == types.ProductionToolCallCompleted || to == types.ProductionToolCallFailed
	}
	if !allowed {
		return fmt.Errorf("invalid production tool call transition %s -> %s", from, to)
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
	return types.RejectProductionCredentialFields(raw)
}

func requireCanonicalProductionUUID(name, value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return fmt.Errorf("%s must be a canonical UUID", name)
	}
	return nil
}

func (r *productionRunRepository) hasActiveLease(
	ctx context.Context,
	tenantID uint64,
	runID string,
	leaseTTL time.Duration,
) (bool, error) {
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	var active bool
	if db.Dialector.Name() == "postgres" {
		err := db.Raw(`SELECT EXISTS (
            SELECT 1 FROM production_runs
            WHERE tenant_id = ? AND id = ? AND status = 'running'
              AND updated_at > CURRENT_TIMESTAMP - (? * INTERVAL '1 second')
        )`, tenantID, runID, leaseTTL.Seconds()).Scan(&active).Error
		return active, err
	}
	modifier := fmt.Sprintf("-%g seconds", leaseTTL.Seconds())
	err := db.Raw(`SELECT EXISTS (
        SELECT 1 FROM production_runs
        WHERE tenant_id = ? AND id = ? AND status = 'running'
          AND julianday(updated_at) > julianday(CURRENT_TIMESTAMP, ?)
    )`, tenantID, runID, modifier).Scan(&active).Error
	return active, err
}

var _ interfaces.ProductionRunRepository = (*productionRunRepository)(nil)
