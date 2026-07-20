package types

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	ErrProductionDocumentStaleParent      = errors.New("production document parent is not the current head")
	ErrProductionDocumentSourceSetInvalid = errors.New("production document source set is not frozen or has the wrong identity")
	ErrProductionDocumentTypeInactive     = errors.New("production document type is not active")
	ErrProductionBlockLineageInvalid      = errors.New("production block lineage is invalid")
	ErrProductionDocumentVersionImmutable = errors.New("production document versions are immutable")
	ErrProductionDocumentBlockImmutable   = errors.New("production document blocks are immutable")
	ErrProductionDocumentValidation       = errors.New("production document version failed governed validation")
	ErrProductionDocumentVersionExists    = errors.New("production document version already exists for this run")
)

type ProductionDocumentStatus string

const (
	ProductionDocumentDraft      ProductionDocumentStatus = "draft"
	ProductionDocumentAnnotating ProductionDocumentStatus = "annotating"
	ProductionDocumentInReview   ProductionDocumentStatus = "in_review"
	ProductionDocumentApproved   ProductionDocumentStatus = "approved"
	ProductionDocumentPublishing ProductionDocumentStatus = "publishing"
	ProductionDocumentPublished  ProductionDocumentStatus = "published"
	ProductionDocumentArchived   ProductionDocumentStatus = "archived"
)

func (s ProductionDocumentStatus) IsValid() bool {
	switch s {
	case ProductionDocumentDraft, ProductionDocumentAnnotating, ProductionDocumentInReview,
		ProductionDocumentApproved, ProductionDocumentPublishing, ProductionDocumentPublished,
		ProductionDocumentArchived:
		return true
	default:
		return false
	}
}

type ProductionDocumentOrigin string

const (
	ProductionDocumentOriginAI       ProductionDocumentOrigin = "ai"
	ProductionDocumentOriginHuman    ProductionDocumentOrigin = "human"
	ProductionDocumentOriginMixed    ProductionDocumentOrigin = "mixed"
	ProductionDocumentOriginRollback ProductionDocumentOrigin = "rollback"
)

func (o ProductionDocumentOrigin) IsValid() bool {
	switch o {
	case ProductionDocumentOriginAI, ProductionDocumentOriginHuman,
		ProductionDocumentOriginMixed, ProductionDocumentOriginRollback:
		return true
	default:
		return false
	}
}

type ProductionBlockRelation string

const (
	ProductionBlockRelationSame   ProductionBlockRelation = "same"
	ProductionBlockRelationSplit  ProductionBlockRelation = "split"
	ProductionBlockRelationMerged ProductionBlockRelation = "merged"
)

func (r ProductionBlockRelation) IsValid() bool {
	return r == ProductionBlockRelationSame || r == ProductionBlockRelationSplit || r == ProductionBlockRelationMerged
}

type ProductionDocument struct {
	ID                        string                   `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID                  uint64                   `json:"tenant_id" gorm:"not null;index"`
	ProjectID                 string                   `json:"project_id" gorm:"type:varchar(36);not null;index"`
	DocumentTypeID            string                   `json:"document_type_id" gorm:"type:varchar(36);not null;index"`
	DocumentTypeSchemaVersion int                      `json:"document_type_schema_version" gorm:"not null"`
	Title                     string                   `json:"title" gorm:"type:varchar(255);not null"`
	CurrentVersionID          *string                  `json:"current_version_id,omitempty" gorm:"type:varchar(36)"`
	LatestApprovedVersionID   *string                  `json:"latest_approved_version_id,omitempty" gorm:"type:varchar(36)"`
	Status                    ProductionDocumentStatus `json:"status" gorm:"type:varchar(20);not null;default:'draft'"`
	CreatedBy                 string                   `json:"created_by" gorm:"type:varchar(36);not null"`
	CreatedAt                 time.Time                `json:"created_at"`
	UpdatedAt                 time.Time                `json:"updated_at"`
}

func (ProductionDocument) TableName() string { return "production_documents" }

type ProductionDocumentVersion struct {
	ID              string                     `json:"id" gorm:"type:varchar(36);primaryKey"`
	DocumentID      string                     `json:"document_id" gorm:"type:varchar(36);not null;index"`
	TenantID        uint64                     `json:"tenant_id" gorm:"not null;index"`
	ProjectID       string                     `json:"project_id" gorm:"type:varchar(36);not null;index"`
	VersionNumber   int                        `json:"version_number" gorm:"not null"`
	ParentVersionID *string                    `json:"parent_version_id,omitempty" gorm:"type:varchar(36)"`
	SourceSetID     string                     `json:"source_set_id" gorm:"type:varchar(36);not null;index"`
	Origin          ProductionDocumentOrigin   `json:"origin" gorm:"type:varchar(20);not null"`
	ChangeSummary   string                     `json:"change_summary" gorm:"type:text;not null;default:''"`
	ContentDigest   string                     `json:"content_digest" gorm:"type:varchar(64);not null"`
	CreatedBy       string                     `json:"created_by" gorm:"type:varchar(36);not null"`
	CreatedAt       time.Time                  `json:"created_at"`
	FrozenAt        *time.Time                 `json:"frozen_at,omitempty"`
	Blocks          []*ProductionDocumentBlock `json:"blocks,omitempty" gorm:"-"`
	Lineage         []*ProductionBlockLineage  `json:"lineage,omitempty" gorm:"-"`
	// DocumentTypeCode is hydrated from the immutable document-type reference
	// before validation. It is validation context, not persisted version data.
	DocumentTypeCode string `json:"document_type_code,omitempty" gorm:"-"`
}

func (ProductionDocumentVersion) TableName() string { return "production_document_versions" }

type ProductionDocumentBlock struct {
	ID             string `json:"id" gorm:"type:varchar(36);primaryKey"`
	VersionID      string `json:"version_id" gorm:"type:varchar(36);not null;index"`
	LogicalBlockID string `json:"logical_block_id" gorm:"type:varchar(36);not null"`
	BlockType      string `json:"block_type" gorm:"type:varchar(24);not null"`
	Position       int    `json:"position" gorm:"not null"`
	Content        JSON   `json:"content" gorm:"type:jsonb;not null"`
	Attributes     JSON   `json:"attributes" gorm:"type:jsonb;not null"`
	EvidenceRefs   JSON   `json:"evidence_refs" gorm:"type:jsonb;not null"`
	AIProvenance   JSON   `json:"ai_provenance" gorm:"column:ai_provenance;type:jsonb;not null"`
	ContentDigest  string `json:"content_digest" gorm:"type:varchar(64);not null"`
}

func (ProductionDocumentBlock) TableName() string { return "production_document_blocks" }

type ProductionBlockLineage struct {
	ID                 string                  `json:"id" gorm:"type:varchar(36);primaryKey"`
	FromVersionID      string                  `json:"from_version_id" gorm:"type:varchar(36);not null;index"`
	FromLogicalBlockID string                  `json:"from_logical_block_id" gorm:"type:varchar(36);not null"`
	ToVersionID        string                  `json:"to_version_id" gorm:"type:varchar(36);not null;index"`
	ToLogicalBlockID   string                  `json:"to_logical_block_id" gorm:"type:varchar(36);not null"`
	Relation           ProductionBlockRelation `json:"relation" gorm:"type:varchar(24);not null"`
}

func (ProductionBlockLineage) TableName() string { return "production_block_lineage" }

type ProductionDocumentBlockInput struct {
	LogicalBlockID string
	BlockType      string
	Content        JSON
	Attributes     JSON
	EvidenceRefs   JSON
	AIProvenance   JSON
}

type ProductionBlockLineageInput struct {
	FromLogicalBlockID string
	ToLogicalBlockID   string
	Relation           ProductionBlockRelation
}

type productionCanonicalBlock struct {
	LogicalBlockID string          `json:"logical_block_id"`
	BlockType      string          `json:"block_type"`
	Position       int             `json:"position"`
	Content        json.RawMessage `json:"content"`
	Attributes     json.RawMessage `json:"attributes"`
	EvidenceRefs   json.RawMessage `json:"evidence_refs"`
	AIProvenance   json.RawMessage `json:"ai_provenance"`
}

func canonicalProductionDigestJSON(value JSON, fallback string) json.RawMessage {
	if len(value) == 0 {
		value = JSON(fallback)
	}
	canonical, err := CanonicalProductionJSON(value)
	if err != nil {
		return json.RawMessage(strconv.Quote(string(value)))
	}
	return json.RawMessage(canonical)
}

// CanonicalProductionJSON returns the exact arbitrary-precision JSON form used
// by production content digests. Object keys and equivalent numeric spellings
// are normalized without converting numbers through float64.
func CanonicalProductionJSON(value JSON) (JSON, error) {
	if len(value) == 0 {
		return nil, errors.New("production JSON value is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values are not allowed")
		}
		return nil, err
	}
	decoded = normalizeProductionDigestNumbers(decoded)
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	return JSON(canonical), nil
}

type canonicalProductionNumber string

func (number canonicalProductionNumber) MarshalJSON() ([]byte, error) {
	return []byte(number), nil
}

func normalizeProductionDigestNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if normalized, ok := normalizeProductionJSONNumber(typed.String()); ok {
			return canonicalProductionNumber(normalized)
		}
		return typed
	case map[string]any:
		for key, nested := range typed {
			typed[key] = normalizeProductionDigestNumbers(nested)
		}
		return typed
	case []any:
		for index, nested := range typed {
			typed[index] = normalizeProductionDigestNumbers(nested)
		}
		return typed
	default:
		return value
	}
}

func normalizeProductionJSONNumber(value string) (string, bool) {
	negative := strings.HasPrefix(value, "-")
	if negative {
		value = value[1:]
	}
	exponentText := "0"
	if separator := strings.IndexAny(value, "eE"); separator >= 0 {
		exponentText = value[separator+1:]
		value = value[:separator]
	}
	exponent, ok := new(big.Int).SetString(exponentText, 10)
	if !ok {
		return "", false
	}
	fractionDigits := 0
	if point := strings.IndexByte(value, '.'); point >= 0 {
		fractionDigits = len(value) - point - 1
		value = value[:point] + value[point+1:]
	}
	digits := strings.TrimLeft(value, "0")
	if digits == "" {
		return "0", true
	}
	exponent.Sub(exponent, big.NewInt(int64(fractionDigits)))
	trimmedDigits := strings.TrimRight(digits, "0")
	exponent.Add(exponent, big.NewInt(int64(len(digits)-len(trimmedDigits))))
	digits = trimmedDigits

	scientificExponent := new(big.Int).Set(exponent)
	scientificExponent.Add(scientificExponent, big.NewInt(int64(len(digits)-1)))
	coefficient := digits[:1]
	if len(digits) > 1 {
		coefficient += "." + digits[1:]
	}
	if negative {
		coefficient = "-" + coefficient
	}
	if scientificExponent.Sign() != 0 {
		coefficient += "e" + scientificExponent.String()
	}
	return coefficient, true
}

func productionSHA256(value any) string {
	canonical, _ := json.Marshal(value)
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
}

// ComputeProductionBlockDigest excludes storage row and version IDs while
// covering every semantic block field, including its ordered position.
func ComputeProductionBlockDigest(block *ProductionDocumentBlock) string {
	if block == nil {
		return productionSHA256(nil)
	}
	return productionSHA256(productionCanonicalBlock{
		LogicalBlockID: block.LogicalBlockID,
		BlockType:      block.BlockType,
		Position:       block.Position,
		Content:        canonicalProductionDigestJSON(block.Content, "null"),
		Attributes:     canonicalProductionDigestJSON(block.Attributes, "{}"),
		EvidenceRefs:   canonicalProductionDigestJSON(block.EvidenceRefs, "[]"),
		AIProvenance:   canonicalProductionDigestJSON(block.AIProvenance, "{}"),
	})
}

type productionCanonicalVersionBlock struct {
	Position       int    `json:"position"`
	LogicalBlockID string `json:"logical_block_id"`
	ContentDigest  string `json:"content_digest"`
}

// ComputeProductionVersionDigest is independent of database load order and
// storage IDs. It represents the ordered logical block content of a version.
func ComputeProductionVersionDigest(version *ProductionDocumentVersion) string {
	if version == nil {
		return productionSHA256(nil)
	}
	blocks := append([]*ProductionDocumentBlock(nil), version.Blocks...)
	sort.SliceStable(blocks, func(i, j int) bool {
		if blocks[i].Position == blocks[j].Position {
			return blocks[i].LogicalBlockID < blocks[j].LogicalBlockID
		}
		return blocks[i].Position < blocks[j].Position
	})
	canonical := make([]productionCanonicalVersionBlock, 0, len(blocks))
	for _, block := range blocks {
		if block == nil {
			continue
		}
		digest := block.ContentDigest
		if digest == "" {
			digest = ComputeProductionBlockDigest(block)
		}
		canonical = append(canonical, productionCanonicalVersionBlock{
			Position: block.Position, LogicalBlockID: block.LogicalBlockID, ContentDigest: digest,
		})
	}
	return productionSHA256(struct {
		Blocks []productionCanonicalVersionBlock `json:"blocks"`
	}{Blocks: canonical})
}
