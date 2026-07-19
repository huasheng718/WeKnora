package types

import (
	"errors"
	"time"
)

var (
	ErrProductionSourceSetFrozen         = errors.New("production source set is frozen")
	ErrProductionEvidenceMissing         = errors.New("accepted production source item is missing evidence")
	ErrProductionEvidenceImmutable       = errors.New("production evidence snapshots are immutable")
	ErrProductionEvidenceDigestMismatch  = errors.New("production evidence digest does not match content")
	ErrProductionEvidenceResourceInvalid = errors.New("production evidence resource is invalid")
)

type ProductionSourceSetStatus string

const (
	ProductionSourceSetCollecting ProductionSourceSetStatus = "collecting"
	ProductionSourceSetReady      ProductionSourceSetStatus = "ready"
	ProductionSourceSetFailed     ProductionSourceSetStatus = "failed"
	ProductionSourceSetFrozen     ProductionSourceSetStatus = "frozen"
)

func (s ProductionSourceSetStatus) IsValid() bool {
	switch s {
	case ProductionSourceSetCollecting, ProductionSourceSetReady, ProductionSourceSetFailed, ProductionSourceSetFrozen:
		return true
	default:
		return false
	}
}

type ProductionSourceKind string

const (
	ProductionSourceKindUpload     ProductionSourceKind = "upload"
	ProductionSourceKindDatasource ProductionSourceKind = "datasource"
	ProductionSourceKindMCP        ProductionSourceKind = "mcp"
	ProductionSourceKindSkill      ProductionSourceKind = "skill"
	ProductionSourceKindManual     ProductionSourceKind = "manual"
)

func (k ProductionSourceKind) IsValid() bool {
	switch k {
	case ProductionSourceKindUpload, ProductionSourceKindDatasource, ProductionSourceKindMCP,
		ProductionSourceKindSkill, ProductionSourceKindManual:
		return true
	default:
		return false
	}
}

type ProductionSourceItemStatus string

const (
	ProductionSourceItemCandidate   ProductionSourceItemStatus = "candidate"
	ProductionSourceItemAccepted    ProductionSourceItemStatus = "accepted"
	ProductionSourceItemRejected    ProductionSourceItemStatus = "rejected"
	ProductionSourceItemUnavailable ProductionSourceItemStatus = "unavailable"
)

func (s ProductionSourceItemStatus) IsValid() bool {
	switch s {
	case ProductionSourceItemCandidate, ProductionSourceItemAccepted,
		ProductionSourceItemRejected, ProductionSourceItemUnavailable:
		return true
	default:
		return false
	}
}

func (s ProductionSourceItemStatus) IsDecision() bool {
	return s == ProductionSourceItemAccepted || s == ProductionSourceItemRejected || s == ProductionSourceItemUnavailable
}

type ProductionEvidenceSnapshotType string

const (
	ProductionEvidenceSnapshotText       ProductionEvidenceSnapshotType = "text"
	ProductionEvidenceSnapshotJSON       ProductionEvidenceSnapshotType = "json"
	ProductionEvidenceSnapshotFile       ProductionEvidenceSnapshotType = "file"
	ProductionEvidenceSnapshotToolResult ProductionEvidenceSnapshotType = "tool_result"
)

func (s ProductionEvidenceSnapshotType) IsValid() bool {
	switch s {
	case ProductionEvidenceSnapshotText, ProductionEvidenceSnapshotJSON,
		ProductionEvidenceSnapshotFile, ProductionEvidenceSnapshotToolResult:
		return true
	default:
		return false
	}
}

type ProductionSourceSet struct {
	ID             string                    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID       uint64                    `json:"tenant_id" gorm:"not null;index"`
	ProjectID      string                    `json:"project_id" gorm:"type:varchar(36);not null;index"`
	DocumentTypeID string                    `json:"document_type_id" gorm:"type:varchar(36);not null;index"`
	TimeRangeStart *time.Time                `json:"time_range_start,omitempty"`
	TimeRangeEnd   *time.Time                `json:"time_range_end,omitempty"`
	Status         ProductionSourceSetStatus `json:"status" gorm:"type:varchar(20);not null;default:'collecting'"`
	CreatedBy      string                    `json:"created_by" gorm:"type:varchar(36);not null"`
	CreatedAt      time.Time                 `json:"created_at"`
	FrozenAt       *time.Time                `json:"frozen_at,omitempty"`
}

func (ProductionSourceSet) TableName() string { return "production_source_sets" }

type ProductionSourceItem struct {
	ID            string                     `json:"id" gorm:"type:varchar(36);primaryKey"`
	SourceSetID   string                     `json:"source_set_id" gorm:"type:varchar(36);not null;index"`
	SourceKind    ProductionSourceKind       `json:"source_kind" gorm:"type:varchar(24);not null"`
	SourceSystem  string                     `json:"source_system" gorm:"type:varchar(255);not null;default:''"`
	ExternalID    string                     `json:"external_id" gorm:"type:varchar(255);not null;default:''"`
	SourceURI     string                     `json:"source_uri,omitempty" gorm:"type:text"`
	Title         string                     `json:"title" gorm:"type:varchar(255);not null"`
	MimeType      string                     `json:"mime_type" gorm:"type:varchar(127);not null"`
	ContentDigest string                     `json:"content_digest" gorm:"type:varchar(64);not null"`
	CapturedAt    time.Time                  `json:"captured_at" gorm:"not null"`
	Metadata      JSON                       `json:"metadata" gorm:"type:jsonb;not null;default:'{}'"`
	Status        ProductionSourceItemStatus `json:"status" gorm:"type:varchar(20);not null;default:'candidate'"`
	CreatedAt     time.Time                  `json:"created_at"`
}

func (ProductionSourceItem) TableName() string { return "production_source_items" }

type ProductionEvidenceSnapshot struct {
	ID                string                         `json:"id" gorm:"type:varchar(36);primaryKey"`
	SourceItemID      string                         `json:"source_item_id" gorm:"type:varchar(36);not null;index"`
	SnapshotType      ProductionEvidenceSnapshotType `json:"snapshot_type" gorm:"type:varchar(24);not null"`
	StoragePath       string                         `json:"storage_path,omitempty" gorm:"type:text"`
	InlineContent     JSON                           `json:"inline_content,omitempty" gorm:"type:jsonb"`
	ContentDigest     string                         `json:"content_digest" gorm:"type:varchar(64);not null"`
	RedactionMetadata JSON                           `json:"redaction_metadata" gorm:"type:jsonb;not null;default:'{}'"`
	CapturedByRunID   string                         `json:"captured_by_run_id,omitempty" gorm:"type:varchar(36)"`
	CreatedAt         time.Time                      `json:"created_at"`
	// ResolvedContentDigest is hydrated from ResourceCatalog when a governed
	// operation verifies registry-backed evidence. It is never persisted.
	ResolvedContentDigest string `json:"-" gorm:"-"`
}

func (ProductionEvidenceSnapshot) TableName() string { return "production_evidence_snapshots" }
