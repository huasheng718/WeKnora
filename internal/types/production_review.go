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
	ProductionAnnotationAnchorMaxBytes = 32 * 1024
	ProductionAnnotationAnchorMaxDepth = 16
	// ProductionAnnotationMaxOffset bounds the legacy page/offset contract so
	// callers cannot force arbitrarily deep database scans.
	ProductionAnnotationMaxOffset = 10_000
	// ProductionAnnotationMaxPage is the largest reachable page at the minimum
	// supported page size of one.
	ProductionAnnotationMaxPage    = ProductionAnnotationMaxOffset + 1
	ProductionReviewPolicyMaxBytes = 256 * 1024
	ProductionReviewPolicyMaxDepth = 32
)

var (
	ErrProductionAnnotationAnchorInvalid = errors.New("production annotation anchor is invalid")
	ErrProductionAnnotationImmutable     = errors.New("production annotation is immutable")
	ErrProductionAnnotationLifecycle     = errors.New("production annotation lifecycle transition is invalid")
	ErrProductionReviewImmutable         = errors.New("production review is immutable")
	ErrProductionReviewLifecycle         = errors.New("production review lifecycle transition is invalid")
	ErrProductionReviewScopeInvalid      = errors.New("production review scope is invalid")
	ErrProductionReviewPolicyInvalid     = errors.New("production review policy is invalid")
	ErrProductionBlockingAnnotations     = errors.New("open blocking annotations prevent review submission")
	ErrProductionContentDigestMismatch   = errors.New("production content digest mismatch")
	ErrProductionJSONResourceLimit       = errors.New("production JSON resource limit exceeded")
)

type ProductionAnnotationType string

const (
	ProductionAnnotationComment    ProductionAnnotationType = "comment"
	ProductionAnnotationSuggestion ProductionAnnotationType = "suggestion"
	ProductionAnnotationQualityTag ProductionAnnotationType = "quality_tag"
)

func (t ProductionAnnotationType) IsValid() bool {
	return t == ProductionAnnotationComment || t == ProductionAnnotationSuggestion || t == ProductionAnnotationQualityTag
}

type ProductionAnnotationCategory string

const (
	ProductionQualityTagMissingEvidence ProductionAnnotationCategory = "missing_evidence"
	ProductionQualityTagFactualRisk     ProductionAnnotationCategory = "factual_risk"
	ProductionQualityTagUnclear         ProductionAnnotationCategory = "unclear"
	ProductionQualityTagIncomplete      ProductionAnnotationCategory = "incomplete"
	ProductionQualityTagConflict        ProductionAnnotationCategory = "conflict"
	ProductionQualityTagComplianceRisk  ProductionAnnotationCategory = "compliance_risk"
)

func (t ProductionAnnotationCategory) IsValid() bool {
	switch t {
	case ProductionQualityTagMissingEvidence, ProductionQualityTagFactualRisk,
		ProductionQualityTagUnclear, ProductionQualityTagIncomplete,
		ProductionQualityTagConflict, ProductionQualityTagComplianceRisk:
		return true
	default:
		return false
	}
}

type ProductionAnnotationSeverity string

const (
	ProductionAnnotationInfo     ProductionAnnotationSeverity = "info"
	ProductionAnnotationWarning  ProductionAnnotationSeverity = "warning"
	ProductionAnnotationBlocking ProductionAnnotationSeverity = "blocking"
)

func (s ProductionAnnotationSeverity) IsValid() bool {
	return s == ProductionAnnotationInfo || s == ProductionAnnotationWarning || s == ProductionAnnotationBlocking
}

type ProductionAnnotationStatus string

const (
	ProductionAnnotationOpen      ProductionAnnotationStatus = "open"
	ProductionAnnotationResolved  ProductionAnnotationStatus = "resolved"
	ProductionAnnotationDismissed ProductionAnnotationStatus = "dismissed"
)

func (s ProductionAnnotationStatus) IsValid() bool {
	return s == ProductionAnnotationOpen || s == ProductionAnnotationResolved || s == ProductionAnnotationDismissed
}

// Shared untyped constants allow the schema's overlapping request and step
// values to be used with either strongly typed lifecycle.
const (
	ProductionReviewPending          = "pending"
	ProductionReviewApproved         = "approved"
	ProductionReviewRejected         = "rejected"
	ProductionReviewObsolete         = "obsolete"
	ProductionReviewCancelled        = "cancelled"
	ProductionReviewChangesRequested = "changes_requested"
)

type ProductionReviewStatus string

type ProductionReviewRequestStatus = ProductionReviewStatus

func (s ProductionReviewStatus) IsValid() bool {
	switch s {
	case ProductionReviewPending, ProductionReviewApproved, ProductionReviewRejected,
		ProductionReviewObsolete, ProductionReviewCancelled, ProductionReviewChangesRequested:
		return true
	default:
		return false
	}
}

type ProductionReviewDecision string

func (d ProductionReviewDecision) IsValid() bool {
	switch d {
	case ProductionReviewPending, ProductionReviewApproved, ProductionReviewChangesRequested,
		ProductionReviewRejected, ProductionReviewCancelled:
		return true
	default:
		return false
	}
}

func (d ProductionReviewDecision) IsProfessionalDecision() bool {
	return d == ProductionReviewApproved || d == ProductionReviewChangesRequested || d == ProductionReviewRejected
}

func CanTransitionReviewStep(from, to ProductionReviewDecision) bool {
	return from == ProductionReviewPending && to.IsProfessionalDecision()
}

type ProductionAnnotation struct {
	ID               string                        `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID         uint64                        `json:"tenant_id" gorm:"not null;index"`
	ProjectID        string                        `json:"project_id" gorm:"type:varchar(36);not null;index"`
	DocumentID       string                        `json:"document_id" gorm:"type:varchar(36);not null;index"`
	VersionID        string                        `json:"version_id" gorm:"type:varchar(36);not null;index"`
	BlockID          string                        `json:"block_id" gorm:"type:varchar(36);not null;index"`
	AnnotationType   ProductionAnnotationType      `json:"annotation_type" gorm:"type:varchar(20);not null"`
	QualityTag       *ProductionAnnotationCategory `json:"quality_tag,omitempty" gorm:"type:varchar(32)"`
	Severity         ProductionAnnotationSeverity  `json:"severity" gorm:"type:varchar(16);not null;default:'info'"`
	Anchor           JSON                          `json:"anchor" gorm:"type:jsonb;not null;default:'{}'"`
	Body             string                        `json:"body" gorm:"type:text;not null"`
	SuggestedContent *string                       `json:"suggested_content,omitempty" gorm:"type:text"`
	Status           ProductionAnnotationStatus    `json:"status" gorm:"type:varchar(16);not null;default:'open'"`
	CreatedBy        string                        `json:"created_by" gorm:"type:varchar(36);not null"`
	ResolvedBy       *string                       `json:"resolved_by,omitempty" gorm:"type:varchar(36)"`
	ResolvedAt       *time.Time                    `json:"resolved_at,omitempty"`
	CreatedAt        time.Time                     `json:"created_at"`
	UpdatedAt        time.Time                     `json:"updated_at"`
}

func (ProductionAnnotation) TableName() string { return "production_annotations" }

type ProductionReviewRequest struct {
	ID             string                  `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID       uint64                  `json:"tenant_id" gorm:"not null;index"`
	ProjectID      string                  `json:"project_id" gorm:"type:varchar(36);not null;index"`
	DocumentID     string                  `json:"document_id" gorm:"type:varchar(36);not null;index"`
	VersionID      string                  `json:"version_id" gorm:"type:varchar(36);not null;index"`
	PolicySnapshot JSON                    `json:"policy_snapshot" gorm:"type:jsonb;not null"`
	PolicyDigest   string                  `json:"policy_digest" gorm:"type:varchar(64);not null"`
	Status         ProductionReviewStatus  `json:"status" gorm:"type:varchar(16);not null;default:'pending'"`
	SubmittedBy    string                  `json:"submitted_by" gorm:"type:varchar(36);not null"`
	SubmittedAt    time.Time               `json:"submitted_at"`
	TerminalBy     *string                 `json:"terminal_by,omitempty" gorm:"type:varchar(36)"`
	TerminalReason *string                 `json:"terminal_reason,omitempty" gorm:"type:text"`
	CompletedAt    *time.Time              `json:"completed_at,omitempty"`
	CreatedAt      time.Time               `json:"created_at"`
	UpdatedAt      time.Time               `json:"updated_at"`
	Steps          []*ProductionReviewStep `json:"steps,omitempty" gorm:"foreignKey:ReviewRequestID"`
}

func (ProductionReviewRequest) TableName() string { return "production_review_requests" }

type ProductionReviewStep struct {
	ID              string                   `json:"id" gorm:"type:varchar(36);primaryKey"`
	ReviewRequestID string                   `json:"review_request_id" gorm:"type:varchar(36);not null;index"`
	TenantID        uint64                   `json:"tenant_id" gorm:"not null;index"`
	ProjectID       string                   `json:"project_id" gorm:"type:varchar(36);not null;index"`
	DocumentID      string                   `json:"document_id" gorm:"type:varchar(36);not null;index"`
	VersionID       string                   `json:"version_id" gorm:"type:varchar(36);not null;index"`
	RequiredRole    ProductionRole           `json:"required_role" gorm:"type:varchar(32);not null"`
	Sequence        int                      `json:"sequence" gorm:"not null"`
	ReviewerUserID  *string                  `json:"reviewer_user_id,omitempty" gorm:"type:varchar(36)"`
	Decision        ProductionReviewDecision `json:"decision" gorm:"type:varchar(24);not null;default:'pending'"`
	Comment         string                   `json:"comment" gorm:"type:text;not null;default:''"`
	DecidedAt       *time.Time               `json:"decided_at,omitempty"`
	CreatedAt       time.Time                `json:"created_at"`
	UpdatedAt       time.Time                `json:"updated_at"`
}

func (ProductionReviewStep) TableName() string { return "production_review_steps" }

// ValidateProductionJSONResource enforces allocation-free limits before JSON
// parsing or canonicalization. Structural characters inside strings do not
// contribute to nesting depth.
func ValidateProductionJSONResource(raw JSON, maxBytes, maxDepth int) error {
	if maxBytes < 1 || maxDepth < 1 || len(raw) > maxBytes {
		return fmt.Errorf("%w: maximum %d bytes", ErrProductionJSONResourceLimit, maxBytes)
	}
	depth := 0
	inString := false
	escaped := false
	for _, character := range raw {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch character {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > maxDepth {
				return fmt.Errorf("%w: maximum nesting depth %d", ErrProductionJSONResourceLimit, maxDepth)
			}
		case '}', ']':
			depth--
		}
	}
	return nil
}

func CanonicalProductionReviewPolicy(raw JSON) (JSON, string, error) {
	if err := ValidateProductionJSONResource(raw, ProductionReviewPolicyMaxBytes, ProductionReviewPolicyMaxDepth); err != nil {
		return nil, "", errors.Join(ErrProductionReviewPolicyInvalid, err)
	}
	canonical, err := CanonicalProductionJSON(raw)
	if err != nil {
		return nil, "", errors.Join(ErrProductionReviewPolicyInvalid, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &object); err != nil || object == nil {
		return nil, "", ErrProductionReviewPolicyInvalid
	}
	digest := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(digest[:]), nil
}
