package types

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// ErrProductionDocumentTypeImmutable is returned when a caller attempts to
// change the definition of an active document-type version.
var ErrProductionDocumentTypeImmutable = errors.New("active production document type definitions are immutable")

// ProductionDocumentTypeStatus is the lifecycle state of a document-type version.
type ProductionDocumentTypeStatus string

const (
	ProductionDocumentTypeDraft   ProductionDocumentTypeStatus = "draft"
	ProductionDocumentTypeActive  ProductionDocumentTypeStatus = "active"
	ProductionDocumentTypeRetired ProductionDocumentTypeStatus = "retired"
)

// IsValid reports whether s is a defined document-type state.
func (s ProductionDocumentTypeStatus) IsValid() bool {
	return s == ProductionDocumentTypeDraft || s == ProductionDocumentTypeActive || s == ProductionDocumentTypeRetired
}

// ProductionDocumentType defines a versioned document schema and its quality,
// review, and publication policies.
type ProductionDocumentType struct {
	ID                 string                       `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID           uint64                       `json:"tenant_id" gorm:"not null;index"`
	Code               string                       `json:"code" gorm:"type:varchar(255);not null"`
	Name               string                       `json:"name" gorm:"type:varchar(255);not null"`
	Description        string                       `json:"description" gorm:"type:text;not null;default:''"`
	SchemaVersion      int                          `json:"schema_version" gorm:"not null"`
	BlockSchema        JSON                         `json:"block_schema" gorm:"type:jsonb;not null;default:'{}'"`
	SourceRequirements JSON                         `json:"source_requirements" gorm:"type:jsonb;not null;default:'{}'"`
	SkillBindings      JSON                         `json:"skill_bindings" gorm:"type:jsonb;not null;default:'{}'"`
	QualityRules       JSON                         `json:"quality_rules" gorm:"type:jsonb;not null;default:'{}'"`
	ReviewPolicy       JSON                         `json:"review_policy" gorm:"type:jsonb;not null;default:'{}'"`
	PublicationPolicy  JSON                         `json:"publication_policy" gorm:"type:jsonb;not null;default:'{}'"`
	Status             ProductionDocumentTypeStatus `json:"status" gorm:"type:varchar(20);not null;default:'draft'"`
	CreatedBy          string                       `json:"created_by" gorm:"type:varchar(36);not null"`
	CreatedAt          time.Time                    `json:"created_at"`
	UpdatedAt          time.Time                    `json:"updated_at"`
	DeletedAt          gorm.DeletedAt               `json:"deleted_at" gorm:"index"`
}

// CanMutateDefinition rejects edits once a definition has been activated,
// including after that version is retired.
func (d *ProductionDocumentType) CanMutateDefinition() error {
	if d != nil && (d.Status == ProductionDocumentTypeActive || d.Status == ProductionDocumentTypeRetired) {
		return ErrProductionDocumentTypeImmutable
	}
	return nil
}

// TableName binds ProductionDocumentType to the production_document_types table.
func (ProductionDocumentType) TableName() string { return "production_document_types" }
