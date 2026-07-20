package types

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/google/uuid"
)

// ProductionDocumentID persists an empty value as SQL NULL. Collect runs are
// document-independent; every other run type requires a canonical value.
type ProductionDocumentID string

func (id ProductionDocumentID) Value() (driver.Value, error) {
	if id == "" {
		return nil, nil
	}
	return string(id), nil
}

func (id *ProductionDocumentID) Scan(value any) error {
	if value == nil {
		*id = ""
		return nil
	}
	switch typed := value.(type) {
	case string:
		*id = ProductionDocumentID(typed)
	case []byte:
		*id = ProductionDocumentID(string(typed))
	default:
		return fmt.Errorf("scan production document id from %T", value)
	}
	return nil
}

var ErrProductionRunLeaseActive = errors.New("production run lease is active")

// ProductionRunLeaseActiveError tells the task runner to retry an early
// redelivery instead of acknowledging a run still owned by another worker.
type ProductionRunLeaseActiveError struct {
	RetryAfter time.Duration
}

func (e *ProductionRunLeaseActiveError) Error() string {
	return fmt.Sprintf("%v; retry after %s", ErrProductionRunLeaseActive, e.RetryAfter)
}

func (e *ProductionRunLeaseActiveError) Unwrap() error { return ErrProductionRunLeaseActive }

// ProductionRunType identifies the durable orchestration workflow.
type ProductionRunType string

const (
	ProductionRunCollect  ProductionRunType = "collect"
	ProductionRunWrite    ProductionRunType = "write"
	ProductionRunRewrite  ProductionRunType = "rewrite"
	ProductionRunValidate ProductionRunType = "validate"
)

func (t ProductionRunType) IsValid() bool {
	switch t {
	case ProductionRunCollect, ProductionRunWrite, ProductionRunRewrite, ProductionRunValidate:
		return true
	default:
		return false
	}
}

// ProductionRunStatus is the persisted lifecycle state for an orchestration run.
type ProductionRunStatus string

const (
	ProductionRunQueued          ProductionRunStatus = "queued"
	ProductionRunRunning         ProductionRunStatus = "running"
	ProductionRunWaitingApproval ProductionRunStatus = "waiting_approval"
	ProductionRunCompleted       ProductionRunStatus = "completed"
	ProductionRunFailed          ProductionRunStatus = "failed"
	ProductionRunCancelled       ProductionRunStatus = "cancelled"
)

func (s ProductionRunStatus) IsValid() bool {
	switch s {
	case ProductionRunQueued, ProductionRunRunning, ProductionRunWaitingApproval,
		ProductionRunCompleted, ProductionRunFailed, ProductionRunCancelled:
		return true
	default:
		return false
	}
}

// ProductionToolProviderType identifies the governed provider behind a call.
type ProductionToolProviderType string

const (
	ProductionToolProviderSkill      ProductionToolProviderType = "skill"
	ProductionToolProviderMCP        ProductionToolProviderType = "mcp"
	ProductionToolProviderDatasource ProductionToolProviderType = "datasource"
)

func (p ProductionToolProviderType) IsValid() bool {
	switch p {
	case ProductionToolProviderSkill, ProductionToolProviderMCP, ProductionToolProviderDatasource:
		return true
	default:
		return false
	}
}

// ProductionToolCallStatus is the persisted lifecycle state for one tool invocation.
type ProductionToolCallStatus string

const (
	ProductionToolCallPlanned         ProductionToolCallStatus = "planned"
	ProductionToolCallPendingApproval ProductionToolCallStatus = "pending_approval"
	ProductionToolCallApproved        ProductionToolCallStatus = "approved"
	ProductionToolCallRejected        ProductionToolCallStatus = "rejected"
	ProductionToolCallExecuting       ProductionToolCallStatus = "executing"
	ProductionToolCallCompleted       ProductionToolCallStatus = "completed"
	ProductionToolCallFailed          ProductionToolCallStatus = "failed"
)

func (s ProductionToolCallStatus) IsValid() bool {
	switch s {
	case ProductionToolCallPlanned, ProductionToolCallPendingApproval, ProductionToolCallApproved,
		ProductionToolCallRejected, ProductionToolCallExecuting, ProductionToolCallCompleted, ProductionToolCallFailed:
		return true
	default:
		return false
	}
}

// ProductionToolApprovalStatus records the durable approval decision, if any.
type ProductionToolApprovalStatus string

const (
	ProductionToolApprovalNotRequired ProductionToolApprovalStatus = "not_required"
	ProductionToolApprovalPending     ProductionToolApprovalStatus = "pending"
	ProductionToolApprovalApproved    ProductionToolApprovalStatus = "approved"
	ProductionToolApprovalRejected    ProductionToolApprovalStatus = "rejected"
)

func (s ProductionToolApprovalStatus) IsValid() bool {
	switch s {
	case ProductionToolApprovalNotRequired, ProductionToolApprovalPending,
		ProductionToolApprovalApproved, ProductionToolApprovalRejected:
		return true
	default:
		return false
	}
}

// ProductionRun is the durable, tenant-scoped state for an AI orchestration workflow.
type ProductionRun struct {
	ID                     string               `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID               uint64               `json:"tenant_id" gorm:"not null;index"`
	ProjectID              string               `json:"project_id" gorm:"type:varchar(36);not null;index"`
	DocumentID             ProductionDocumentID `json:"document_id,omitempty" gorm:"type:varchar(36);index"`
	SourceSetID            string               `json:"source_set_id" gorm:"type:varchar(36);not null;index"`
	RunType                ProductionRunType    `json:"run_type" gorm:"type:varchar(20);not null"`
	Status                 ProductionRunStatus  `json:"status" gorm:"type:varchar(24);not null;default:'queued'"`
	Attempt                int                  `json:"attempt" gorm:"not null;default:0"`
	CurrentStep            int                  `json:"current_step" gorm:"not null;default:0"`
	WakeupVersion          int                  `json:"wakeup_version" gorm:"not null;default:0"`
	WakeupEnqueuedVersion  int                  `json:"wakeup_enqueued_version" gorm:"not null;default:0"`
	StatePayload           JSON                 `json:"state_payload" gorm:"type:jsonb;not null"`
	ModelID                string               `json:"model_id" gorm:"type:varchar(64);not null"`
	DocumentTypeSnapshot   JSON                 `json:"document_type_snapshot" gorm:"type:jsonb;not null"`
	WorkflowPlanSnapshot   JSON                 `json:"workflow_plan_snapshot" gorm:"type:jsonb;not null;default:'{\"steps\":[],\"version\":1}'"`
	InputVersionID         *string              `json:"input_version_id,omitempty" gorm:"type:varchar(36)"`
	OutputVersionID        *string              `json:"output_version_id,omitempty" gorm:"type:varchar(36)"`
	IdempotencyKey         string               `json:"idempotency_key" gorm:"type:varchar(255);not null"`
	RawModelResponse       JSON                 `json:"raw_model_response,omitempty" gorm:"type:jsonb"`
	RawModelResponseDigest *string              `json:"raw_model_response_digest,omitempty" gorm:"type:varchar(64)"`
	ErrorCode              *string              `json:"error_code,omitempty" gorm:"type:varchar(64)"`
	ErrorMessage           *string              `json:"error_message,omitempty" gorm:"type:text"`
	StartedAt              *time.Time           `json:"started_at,omitempty"`
	CompletedAt            *time.Time           `json:"completed_at,omitempty"`
	CreatedAt              time.Time            `json:"created_at"`
	UpdatedAt              time.Time            `json:"updated_at"`
}

func (ProductionRun) TableName() string { return "production_runs" }

// ProductionToolCall is one durable, tenant-scoped invocation within a run.
type ProductionToolCall struct {
	ID                           string                       `json:"id" gorm:"type:varchar(36);primaryKey"`
	RunID                        string                       `json:"run_id" gorm:"type:varchar(36);not null;index"`
	TenantID                     uint64                       `json:"tenant_id" gorm:"not null;index"`
	ProjectID                    string                       `json:"project_id" gorm:"type:varchar(36);not null;index"`
	DocumentID                   ProductionDocumentID         `json:"document_id,omitempty" gorm:"type:varchar(36);index"`
	SourceSetID                  string                       `json:"source_set_id" gorm:"type:varchar(36);not null;index"`
	Attempt                      int                          `json:"attempt" gorm:"not null"`
	CurrentStep                  int                          `json:"current_step" gorm:"not null"`
	IdempotencyKey               string                       `json:"idempotency_key" gorm:"type:varchar(255);not null"`
	ProviderType                 ProductionToolProviderType   `json:"provider_type" gorm:"type:varchar(20);not null"`
	ProviderID                   string                       `json:"provider_id" gorm:"type:varchar(255);not null"`
	ToolName                     string                       `json:"tool_name" gorm:"type:varchar(255);not null"`
	RequestSnapshot              JSON                         `json:"request_snapshot" gorm:"type:jsonb;not null"`
	RequestDigest                string                       `json:"request_digest" gorm:"type:varchar(64);not null"`
	ResponseSnapshot             JSON                         `json:"response_snapshot,omitempty" gorm:"type:jsonb"`
	ResponseDigest               *string                      `json:"response_digest,omitempty" gorm:"type:varchar(64)"`
	ResponseEvidenceID           *string                      `json:"response_evidence_id,omitempty" gorm:"type:varchar(36)"`
	ResponseEvidenceSourceItemID *string                      `json:"response_evidence_source_item_id,omitempty" gorm:"type:varchar(36)"`
	Status                       ProductionToolCallStatus     `json:"status" gorm:"type:varchar(24);not null;default:'planned'"`
	ApprovalStatus               ProductionToolApprovalStatus `json:"approval_status" gorm:"type:varchar(20);not null;default:'not_required'"`
	ApprovalRequestedAt          *time.Time                   `json:"approval_requested_at,omitempty"`
	ApprovedBy                   *string                      `json:"approved_by,omitempty" gorm:"type:varchar(36)"`
	ApprovedAt                   *time.Time                   `json:"approved_at,omitempty"`
	RejectedBy                   *string                      `json:"rejected_by,omitempty" gorm:"type:varchar(36)"`
	RejectedAt                   *time.Time                   `json:"rejected_at,omitempty"`
	ErrorCode                    *string                      `json:"error_code,omitempty" gorm:"type:varchar(64)"`
	ErrorMessage                 *string                      `json:"error_message,omitempty" gorm:"type:text"`
	StartedAt                    *time.Time                   `json:"started_at,omitempty"`
	CompletedAt                  *time.Time                   `json:"completed_at,omitempty"`
	CreatedAt                    time.Time                    `json:"created_at"`
	UpdatedAt                    time.Time                    `json:"updated_at"`
}

func (ProductionToolCall) TableName() string { return "production_tool_calls" }

// ProductionRunPayload is the complete queue transport contract. Durable
// orchestration state remains in ProductionRun and ProductionToolCall rows.
type ProductionRunPayload struct {
	TracingContext
	TenantID uint64 `json:"tenant_id"`
	RunID    string `json:"run_id"`
	Attempt  int    `json:"attempt"`
}

func (p ProductionRunPayload) Validate() error {
	if p.TenantID == 0 {
		return apperrors.NewValidationError("tenant_id is required")
	}
	if p.Attempt < 1 {
		return apperrors.NewValidationError("attempt must be positive")
	}
	parsed, err := uuid.Parse(p.RunID)
	if err != nil || parsed == uuid.Nil || parsed.String() != p.RunID {
		return apperrors.NewValidationError("run_id must be a canonical UUID")
	}
	return nil
}
