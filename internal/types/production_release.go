package types

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	ProductionReleaseTargetConfigMaxBytes = 256 * 1024
	ProductionReleaseTargetConfigMaxDepth = 32
	ProductionReleaseDefaultRetentionDays = 30
)

var (
	ErrProductionReleaseInvalid       = errors.New("production release is invalid")
	ErrProductionReleaseConfigInvalid = errors.New("production release target configuration is invalid")
	ErrProductionReleaseLifecycle     = errors.New("production release lifecycle transition is invalid")
	ErrProductionReleasePatchInvalid  = errors.New("production release target patch is invalid")
	ErrProductionProjectionConflict   = errors.New("production projection head conflict")
	ErrProductionProjectionInactive   = errors.New("production projection is inactive")
)

type ProductionReleaseStatus string

const (
	ProductionReleaseBuilding       ProductionReleaseStatus = "building"
	ProductionReleaseReady          ProductionReleaseStatus = "ready"
	ProductionReleaseActive         ProductionReleaseStatus = "active"
	ProductionReleaseFailed         ProductionReleaseStatus = "failed"
	ProductionReleaseRolledBack     ProductionReleaseStatus = "rolled_back"
	ProductionReleaseCleanupPending ProductionReleaseStatus = "cleanup_pending"
	ProductionReleaseCleaned        ProductionReleaseStatus = "cleaned"

	// Short aliases keep release and target call sites visually parallel.
	ReleaseBuilding       = ProductionReleaseBuilding
	ReleaseReady          = ProductionReleaseReady
	ReleaseActive         = ProductionReleaseActive
	ReleaseFailed         = ProductionReleaseFailed
	ReleaseRolledBack     = ProductionReleaseRolledBack
	ReleaseCleanupPending = ProductionReleaseCleanupPending
	ReleaseCleaned        = ProductionReleaseCleaned
)

func (s ProductionReleaseStatus) IsValid() bool {
	switch s {
	case ProductionReleaseBuilding, ProductionReleaseReady, ProductionReleaseActive,
		ProductionReleaseFailed, ProductionReleaseRolledBack, ProductionReleaseCleanupPending,
		ProductionReleaseCleaned:
		return true
	default:
		return false
	}
}

type ProductionReleaseTargetStatus string

const (
	ReleaseTargetBuilding       ProductionReleaseTargetStatus = "building"
	ReleaseTargetReady          ProductionReleaseTargetStatus = "ready"
	ReleaseTargetActive         ProductionReleaseTargetStatus = "active"
	ReleaseTargetFailed         ProductionReleaseTargetStatus = "failed"
	ReleaseTargetRolledBack     ProductionReleaseTargetStatus = "rolled_back"
	ReleaseTargetCleanupPending ProductionReleaseTargetStatus = "cleanup_pending"
	ReleaseTargetCleaned        ProductionReleaseTargetStatus = "cleaned"
)

func (s ProductionReleaseTargetStatus) IsValid() bool {
	switch s {
	case ReleaseTargetBuilding, ReleaseTargetReady, ReleaseTargetActive, ReleaseTargetFailed,
		ReleaseTargetRolledBack, ReleaseTargetCleanupPending, ReleaseTargetCleaned:
		return true
	default:
		return false
	}
}

// CanTransitionReleaseTarget describes aggregate lifecycle edges. Repository
// target mutation deliberately reserves ready -> active and active rollback
// for projection-head triggers.
func CanTransitionReleaseTarget(from, to ProductionReleaseTargetStatus) bool {
	if !from.IsValid() || !to.IsValid() || from == to {
		return false
	}
	switch from {
	case ReleaseTargetBuilding:
		return to == ReleaseTargetReady || to == ReleaseTargetFailed || to == ReleaseTargetRolledBack
	case ReleaseTargetReady:
		return to == ReleaseTargetActive || to == ReleaseTargetFailed || to == ReleaseTargetRolledBack
	case ReleaseTargetActive:
		return false
	case ReleaseTargetFailed:
		return to == ReleaseTargetBuilding || to == ReleaseTargetRolledBack || to == ReleaseTargetCleanupPending
	case ReleaseTargetRolledBack:
		return to == ReleaseTargetBuilding || to == ReleaseTargetCleanupPending
	case ReleaseTargetCleanupPending:
		return to == ReleaseTargetCleaned
	default:
		return false
	}
}

type ProductionRelease struct {
	ID              string                  `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64                  `json:"tenant_id" gorm:"not null;index"`
	ProjectID       string                  `json:"project_id" gorm:"type:varchar(36);not null;index"`
	DocumentID      string                  `json:"document_id" gorm:"type:varchar(36);not null;index"`
	VersionID       string                  `json:"version_id" gorm:"type:varchar(36);not null;index"`
	ReviewRequestID string                  `json:"review_request_id" gorm:"type:varchar(36);not null"`
	ReleaseDigest   string                  `json:"release_digest" gorm:"type:varchar(64);not null"`
	Status          ProductionReleaseStatus `json:"status" gorm:"type:varchar(20);not null;default:'building'"`
	RetentionDays   int                     `json:"retention_days" gorm:"not null;default:30"`
	CreatedBy       string                  `json:"created_by" gorm:"type:varchar(36);not null"`
	CreatedAt       time.Time               `json:"created_at"`
	UpdatedAt       time.Time               `json:"updated_at"`
}

func (ProductionRelease) TableName() string { return "production_releases" }

type ProductionReleaseTarget struct {
	ID                    string                        `json:"id" gorm:"type:varchar(36);primaryKey"`
	ReleaseID             string                        `json:"release_id" gorm:"type:varchar(36);not null;index"`
	TenantID              uint64                        `json:"tenant_id" gorm:"not null;index"`
	ProjectID             string                        `json:"project_id" gorm:"type:varchar(36);not null;index"`
	DocumentID            string                        `json:"document_id" gorm:"type:varchar(36);not null;index"`
	VersionID             string                        `json:"version_id" gorm:"type:varchar(36);not null;index"`
	TargetKnowledgeBaseID string                        `json:"target_knowledge_base_id" gorm:"type:varchar(36);not null;index"`
	KnowledgeID           string                        `json:"knowledge_id" gorm:"type:varchar(36);not null;uniqueIndex"`
	ReleaseDigest         string                        `json:"release_digest" gorm:"type:varchar(64);not null"`
	ConfigSnapshot        JSON                          `json:"config_snapshot" gorm:"type:jsonb;not null"`
	ConfigDigest          string                        `json:"config_digest" gorm:"type:varchar(64);not null"`
	Status                ProductionReleaseTargetStatus `json:"status" gorm:"type:varchar(20);not null;default:'building'"`
	RetentionDays         int                           `json:"retention_days" gorm:"not null;default:30"`
	RetentionUntil        *time.Time                    `json:"retention_until,omitempty"`
	ActivatedAt           *time.Time                    `json:"activated_at,omitempty"`
	FailedAt              *time.Time                    `json:"failed_at,omitempty"`
	RolledBackAt          *time.Time                    `json:"rolled_back_at,omitempty"`
	CleanupRequestedAt    *time.Time                    `json:"cleanup_requested_at,omitempty"`
	CleanedAt             *time.Time                    `json:"cleaned_at,omitempty"`
	CreatedAt             time.Time                     `json:"created_at"`
	UpdatedAt             time.Time                     `json:"updated_at"`
}

func (ProductionReleaseTarget) TableName() string { return "production_release_targets" }

type ProductionProjectionHead struct {
	TenantID              uint64    `json:"tenant_id" gorm:"primaryKey"`
	DocumentID            string    `json:"document_id" gorm:"type:varchar(36);primaryKey"`
	TargetKnowledgeBaseID string    `json:"target_knowledge_base_id" gorm:"type:varchar(36);primaryKey"`
	ActiveReleaseTargetID string    `json:"active_release_target_id" gorm:"type:varchar(36);not null"`
	LockVersion           int       `json:"lock_version" gorm:"not null;default:1"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func (ProductionProjectionHead) TableName() string { return "production_projection_heads" }

type ProductionKnowledgeScope struct {
	ActiveKnowledgeIDs        []string `json:"active_knowledge_ids"`
	InactiveKnowledgeIDs      []string `json:"inactive_knowledge_ids"`
	AllProductionKnowledgeIDs []string `json:"all_production_knowledge_ids"`
}

func CanonicalProductionReleaseTargetConfig(raw JSON) (JSON, string, error) {
	if len(raw) == 0 {
		raw = JSON(`{}`)
	}
	if err := ValidateProductionJSONResource(raw, ProductionReleaseTargetConfigMaxBytes, ProductionReleaseTargetConfigMaxDepth); err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrProductionReleaseConfigInvalid, err)
	}
	if err := RejectProductionCredentialFields(raw); err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrProductionReleaseConfigInvalid, err)
	}
	canonical, err := CanonicalProductionJSON(raw)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrProductionReleaseConfigInvalid, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &object); err != nil || object == nil {
		return nil, "", fmt.Errorf("%w: configuration must be a JSON object", ErrProductionReleaseConfigInvalid)
	}
	digest := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(digest[:]), nil
}
