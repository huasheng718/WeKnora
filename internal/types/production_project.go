package types

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// ErrProductionForbidden is returned when the caller lacks tenant or
// project-scoped authority for a production operation.
var ErrProductionForbidden = errors.New("production operation forbidden")

// ErrProductionConflict is returned when a production write violates a
// domain uniqueness invariant.
var ErrProductionConflict = errors.New("production resource conflict")

// ProductionRole is a project-scoped production responsibility. It is
// intentionally separate from TenantRole, which controls tenant-wide access.
type ProductionRole string

const (
	ProductionRoleProjectOwner        ProductionRole = "project_owner"
	ProductionRoleAuthor              ProductionRole = "author"
	ProductionRoleBusinessReviewer    ProductionRole = "business_reviewer"
	ProductionRoleEngineeringReviewer ProductionRole = "engineering_reviewer"
	ProductionRoleKnowledgeAdmin      ProductionRole = "knowledge_admin"
	ProductionRoleComplianceReviewer  ProductionRole = "compliance_reviewer"
	ProductionRolePublisher           ProductionRole = "publisher"
	ProductionRoleObserver            ProductionRole = "observer"
)

var productionRoles = map[ProductionRole]struct{}{
	ProductionRoleProjectOwner:        {},
	ProductionRoleAuthor:              {},
	ProductionRoleBusinessReviewer:    {},
	ProductionRoleEngineeringReviewer: {},
	ProductionRoleKnowledgeAdmin:      {},
	ProductionRoleComplianceReviewer:  {},
	ProductionRolePublisher:           {},
	ProductionRoleObserver:            {},
}

// IsValid reports whether r is a defined production project role.
func (r ProductionRole) IsValid() bool {
	_, ok := productionRoles[r]
	return ok
}

// ProductionProjectStatus is the lifecycle state of a production project.
type ProductionProjectStatus string

const (
	ProductionProjectActive   ProductionProjectStatus = "active"
	ProductionProjectArchived ProductionProjectStatus = "archived"
)

// IsValid reports whether s is a defined production project state.
func (s ProductionProjectStatus) IsValid() bool {
	return s == ProductionProjectActive || s == ProductionProjectArchived
}

// ProductionProject is the tenant-scoped aggregate root for knowledge
// production work.
type ProductionProject struct {
	ID               string                    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID         uint64                    `json:"tenant_id" gorm:"not null;index"`
	Name             string                    `json:"name" gorm:"type:varchar(255);not null"`
	Description      string                    `json:"description" gorm:"type:text;not null;default:''"`
	OwnerUserID      string                    `json:"owner_user_id" gorm:"type:varchar(36);not null"`
	Status           ProductionProjectStatus   `json:"status" gorm:"type:varchar(20);not null;default:'active'"`
	CreatedAt        time.Time                 `json:"created_at"`
	UpdatedAt        time.Time                 `json:"updated_at"`
	DeletedAt        gorm.DeletedAt            `json:"deleted_at" gorm:"index"`
	CurrentUserRoles []ProductionRole          `json:"current_user_roles,omitempty" gorm:"-"`
	Summary          *ProductionProjectSummary `json:"summary,omitempty" gorm:"-"`
}

// TableName binds ProductionProject to the production_projects table.
func (ProductionProject) TableName() string { return "production_projects" }

// ProductionProjectSummary is the bounded list projection returned with a project.
type ProductionProjectSummary struct {
	DocumentCount  int64     `json:"document_count"`
	SourceSetCount int64     `json:"source_set_count"`
	PendingReviews int64     `json:"pending_reviews"`
	FailedTargets  int64     `json:"failed_targets"`
	InFlightRuns   int64     `json:"in_flight_runs"`
	LatestActivity time.Time `json:"latest_activity"`
}

// ProductionProjectDecoration contains caller-specific, non-persistent list data.
type ProductionProjectDecoration struct {
	CurrentUserRoles []ProductionRole
	Summary          ProductionProjectSummary
}

type ProductionProjectDecorations map[string]ProductionProjectDecoration

// ProductionProjectMember records one role assignment for a project member.
// A user may hold multiple roles in the same project.
type ProductionProjectMember struct {
	ProjectID  string         `json:"project_id" gorm:"type:varchar(36);not null;index"`
	UserID     string         `json:"user_id" gorm:"type:varchar(36);not null;index"`
	Role       ProductionRole `json:"role" gorm:"type:varchar(32);not null"`
	AssignedBy string         `json:"assigned_by" gorm:"type:varchar(36);not null"`
	CreatedAt  time.Time      `json:"created_at"`
	DeletedAt  gorm.DeletedAt `json:"deleted_at" gorm:"index"`
}

// TableName binds ProductionProjectMember to the production_project_members table.
func (ProductionProjectMember) TableName() string { return "production_project_members" }
