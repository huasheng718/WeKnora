package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

type ProductionValidationIssue struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	LogicalBlockID string `json:"logical_block_id,omitempty"`
	Position       int    `json:"position,omitempty"`
	EvidenceID     string `json:"evidence_id,omitempty"`
	Section        string `json:"section,omitempty"`
}

type ProductionValidationResult struct {
	Errors   []ProductionValidationIssue
	Warnings []ProductionValidationIssue
}

type productionBlockAttributes struct {
	Level             int
	Ordered           bool
	Language          string
	Kind              string
	Factual           bool
	NeedsConfirmation bool
}

var productionEvidenceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func productionSortedBlocks(blocks []*types.ProductionDocumentBlock) []*types.ProductionDocumentBlock {
	sorted := append([]*types.ProductionDocumentBlock(nil), blocks...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i] == nil {
			return sorted[j] != nil
		}
		if sorted[j] == nil {
			return false
		}
		if sorted[i].Position == sorted[j].Position {
			return sorted[i].LogicalBlockID < sorted[j].LogicalBlockID
		}
		return sorted[i].Position < sorted[j].Position
	})
	return sorted
}

func productionDecodeJSON(raw types.JSON, fallback string, target any) error {
	canonical, err := canonicalProductionJSON(raw, fallback)
	if err != nil {
		return err
	}
	return json.Unmarshal(canonical, target)
}

func productionParseAttributes(raw types.JSON) (productionBlockAttributes, error) {
	var values map[string]json.RawMessage
	if err := productionDecodeJSON(raw, `{}`, &values); err != nil {
		return productionBlockAttributes{}, err
	}
	if values == nil {
		return productionBlockAttributes{}, errors.New("attributes must be an object")
	}
	attributes := productionBlockAttributes{Level: 2, Kind: "note"}
	for _, key := range []string{"level", "ordered", "language", "kind", "factual", "needs_confirmation"} {
		rawValue, present := values[key]
		if !present {
			continue
		}
		switch key {
		case "level":
			if err := json.Unmarshal(rawValue, &attributes.Level); err != nil || attributes.Level < 1 || attributes.Level > 6 {
				return productionBlockAttributes{}, errors.New("level must be an integer from 1 through 6")
			}
		case "ordered":
			if err := json.Unmarshal(rawValue, &attributes.Ordered); err != nil {
				return productionBlockAttributes{}, errors.New("ordered must be a boolean")
			}
		case "language":
			if err := json.Unmarshal(rawValue, &attributes.Language); err != nil {
				return productionBlockAttributes{}, errors.New("language must be a string")
			}
		case "kind":
			if err := json.Unmarshal(rawValue, &attributes.Kind); err != nil {
				return productionBlockAttributes{}, errors.New("kind must be a string")
			}
		case "factual":
			if err := json.Unmarshal(rawValue, &attributes.Factual); err != nil {
				return productionBlockAttributes{}, errors.New("factual must be a boolean")
			}
		case "needs_confirmation":
			if err := json.Unmarshal(rawValue, &attributes.NeedsConfirmation); err != nil {
				return productionBlockAttributes{}, errors.New("needs_confirmation must be a boolean")
			}
		}
	}
	return attributes, nil
}

func productionValidateJSONObject(raw types.JSON, fallback string) error {
	var object map[string]json.RawMessage
	if err := productionDecodeJSON(raw, fallback, &object); err != nil {
		return err
	}
	if object == nil {
		return errors.New("value must be a JSON object")
	}
	return nil
}

func productionParseEvidenceRefs(raw types.JSON) ([]string, error) {
	var refs []string
	if err := productionDecodeJSON(raw, `[]`, &refs); err != nil {
		return nil, err
	}
	if refs == nil {
		return nil, errors.New("evidence_refs must be an array")
	}
	for _, ref := range refs {
		if !productionEvidenceIDPattern.MatchString(ref) {
			return nil, fmt.Errorf("invalid evidence id %q", ref)
		}
	}
	return refs, nil
}

func productionValidateBlockContent(block *types.ProductionDocumentBlock) error {
	switch block.BlockType {
	case "heading", "paragraph", "code", "callout":
		var text string
		if err := productionDecodeJSON(block.Content, "", &text); err != nil {
			return errors.New("content must be a JSON string")
		}
	case "list":
		var items []string
		if err := productionDecodeJSON(block.Content, "", &items); err != nil || items == nil {
			return errors.New("content must be an array of strings")
		}
	case "table":
		var table struct {
			Headers []string   `json:"headers"`
			Rows    [][]string `json:"rows"`
		}
		if err := productionDecodeJSON(block.Content, "", &table); err != nil || len(table.Headers) == 0 {
			return errors.New("content must contain non-empty string headers and rows")
		}
		for _, row := range table.Rows {
			if len(row) != len(table.Headers) {
				return errors.New("table rows must match the header width")
			}
		}
	case "image":
		var image struct {
			Alt string `json:"alt"`
			URL string `json:"url"`
		}
		if err := productionDecodeJSON(block.Content, "", &image); err != nil || strings.TrimSpace(image.URL) == "" {
			return errors.New("content must contain image alt and URL strings")
		}
	default:
		return fmt.Errorf("unsupported block type %q", block.BlockType)
	}
	return nil
}

func productionIssue(code, message string, block *types.ProductionDocumentBlock) ProductionValidationIssue {
	issue := ProductionValidationIssue{Code: code, Message: message}
	if block != nil {
		issue.LogicalBlockID = block.LogicalBlockID
		issue.Position = block.Position
	}
	return issue
}

func ValidateProductionVersion(version *types.ProductionDocumentVersion, acceptedEvidence map[string]struct{}) ProductionValidationResult {
	result := ProductionValidationResult{Errors: []ProductionValidationIssue{}, Warnings: []ProductionValidationIssue{}}
	if version == nil {
		result.Errors = append(result.Errors, ProductionValidationIssue{Code: "version_required", Message: "production document version is required"})
		return result
	}
	template, knownTemplate := BuiltinProductionTemplate(version.DocumentTypeCode)
	if version.DocumentTypeCode == "" {
		result.Errors = append(result.Errors, ProductionValidationIssue{Code: "document_type_code_required", Message: "document type code validation context is required"})
		return result
	}
	if !knownTemplate {
		result.Errors = append(result.Errors, ProductionValidationIssue{Code: "document_type_code_unknown", Message: fmt.Sprintf("unknown production document type code %q", version.DocumentTypeCode)})
		return result
	}

	blocks := productionSortedBlocks(version.Blocks)
	presentSections := make(map[string]struct{}, len(template.RequiredSections))
	for _, block := range blocks {
		if block == nil || block.BlockType != "heading" {
			continue
		}
		var heading string
		if productionDecodeJSON(block.Content, "", &heading) == nil {
			presentSections[strings.TrimSpace(heading)] = struct{}{}
		}
	}
	for _, section := range template.RequiredSections {
		if _, present := presentSections[section]; !present {
			result.Errors = append(result.Errors, ProductionValidationIssue{
				Code: "required_section_missing", Message: fmt.Sprintf("required section %q is missing", section), Section: section,
			})
		}
	}

	logicalIDs := make(map[string]struct{}, len(blocks))
	for _, block := range blocks {
		if block == nil {
			continue
		}
		if _, exists := logicalIDs[block.LogicalBlockID]; exists {
			result.Errors = append(result.Errors, productionIssue("duplicate_logical_block_id", fmt.Sprintf("duplicate logical block id %q", block.LogicalBlockID), block))
		} else {
			logicalIDs[block.LogicalBlockID] = struct{}{}
		}
	}

	for _, block := range blocks {
		if block == nil {
			result.Errors = append(result.Errors, ProductionValidationIssue{Code: "nil_block", Message: "production document contains a nil block"})
			continue
		}
		if strings.TrimSpace(block.LogicalBlockID) == "" {
			result.Errors = append(result.Errors, productionIssue("logical_block_id_required", "logical block id is required", block))
		}
		if block.Position < 0 {
			result.Errors = append(result.Errors, productionIssue("block_position_invalid", "block position must not be negative", block))
		}
		contentErr := productionValidateBlockContent(block)
		if contentErr != nil {
			code := "invalid_block_content"
			if strings.HasPrefix(contentErr.Error(), "unsupported block type") {
				code = "unsupported_block_type"
			}
			result.Errors = append(result.Errors, productionIssue(code, contentErr.Error(), block))
		}
		attributes, attributesErr := productionParseAttributes(block.Attributes)
		if attributesErr != nil {
			result.Errors = append(result.Errors, productionIssue("invalid_block_attributes", attributesErr.Error(), block))
		}
		if provenanceErr := productionValidateJSONObject(block.AIProvenance, `{}`); provenanceErr != nil {
			result.Errors = append(result.Errors, productionIssue("invalid_ai_provenance", provenanceErr.Error(), block))
		}
		refs, refsErr := productionParseEvidenceRefs(block.EvidenceRefs)
		if refsErr != nil {
			result.Errors = append(result.Errors, productionIssue("invalid_evidence_refs", refsErr.Error(), block))
			refs = nil
		}
		seenRefs := make(map[string]struct{}, len(refs))
		unknownRefs := make(map[string]struct{}, len(refs))
		hasAcceptedEvidence := false
		for _, ref := range refs {
			if _, duplicate := seenRefs[ref]; duplicate {
				issue := productionIssue("duplicate_evidence_ref", fmt.Sprintf("duplicate evidence reference %q", ref), block)
				issue.EvidenceID = ref
				result.Errors = append(result.Errors, issue)
				continue
			}
			seenRefs[ref] = struct{}{}
			if _, accepted := acceptedEvidence[ref]; accepted {
				hasAcceptedEvidence = true
				continue
			}
			if _, reported := unknownRefs[ref]; !reported {
				issue := productionIssue("unknown_evidence_id", fmt.Sprintf("unknown evidence id %q", ref), block)
				issue.EvidenceID = ref
				result.Errors = append(result.Errors, issue)
				unknownRefs[ref] = struct{}{}
			}
		}
		claimCapable := block.BlockType == "paragraph" || block.BlockType == "list" || block.BlockType == "table" || block.BlockType == "quote"
		if claimCapable && !hasAcceptedEvidence && !attributes.NeedsConfirmation {
			result.Errors = append(result.Errors, productionIssue("factual_evidence_required", "claim-capable block requires accepted evidence or needs_confirmation=true", block))
		}
	}
	return result
}

func productionVerifyEvidenceDigest(id string, snapshot *types.ProductionEvidenceSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("evidence %s is nil", id)
	}
	if snapshot.ID != id {
		return fmt.Errorf("evidence map key %q does not match snapshot id %q", id, snapshot.ID)
	}
	if !snapshot.SnapshotType.IsValid() {
		return fmt.Errorf("evidence %s has invalid snapshot type", id)
	}
	storedDigest, storedOK := normalizedSHA256(snapshot.ContentDigest)
	if !storedOK {
		return fmt.Errorf("evidence %s content digest must be SHA-256", id)
	}
	hasInline := len(snapshot.InlineContent) != 0
	hasResource := snapshot.StoragePath != ""
	if hasInline == hasResource {
		return fmt.Errorf("evidence %s must contain exactly one inline value or resource reference", id)
	}
	if hasInline {
		canonical, err := types.CanonicalProductionJSON(snapshot.InlineContent)
		if err != nil {
			return fmt.Errorf("evidence %s inline content is invalid: %w", id, err)
		}
		sum := sha256.Sum256(canonical)
		if recomputed := hex.EncodeToString(sum[:]); recomputed != storedDigest {
			return fmt.Errorf("evidence %s digest mismatch", id)
		}
		return nil
	}
	handle, ok := types.ParseResourcePath(snapshot.StoragePath)
	if !ok || types.BuildResourcePath(handle) != snapshot.StoragePath {
		return fmt.Errorf("evidence %s has invalid canonical resource reference", id)
	}
	resolvedDigest, resolvedOK := normalizedSHA256(snapshot.ResolvedContentDigest)
	if !resolvedOK {
		return fmt.Errorf("evidence %s requires a valid resolved content digest", id)
	}
	if resolvedDigest != storedDigest {
		return fmt.Errorf("evidence %s digest mismatch", id)
	}
	return nil
}

func VerifyProductionVersionDigests(version *types.ProductionDocumentVersion, evidenceByID map[string]*types.ProductionEvidenceSnapshot) error {
	if version == nil {
		return errors.New("production document version is required")
	}
	blocks := productionSortedBlocks(version.Blocks)
	verifiedEvidence := make(map[string]struct{}, len(evidenceByID))
	for _, block := range blocks {
		if block == nil {
			return errors.New("production document version contains a nil block")
		}
		refs, err := productionParseEvidenceRefs(block.EvidenceRefs)
		if err != nil {
			return fmt.Errorf("block %s has invalid evidence references: %w", block.LogicalBlockID, err)
		}
		seenRefs := make(map[string]struct{}, len(refs))
		for _, ref := range refs {
			if _, duplicate := seenRefs[ref]; duplicate {
				return fmt.Errorf("block %s has duplicate evidence reference %q", block.LogicalBlockID, ref)
			}
			seenRefs[ref] = struct{}{}
		}
		for _, ref := range refs {
			if _, verified := verifiedEvidence[ref]; verified {
				continue
			}
			snapshot, exists := evidenceByID[ref]
			if !exists {
				return fmt.Errorf("referenced evidence %s is missing", ref)
			}
			if err := productionVerifyEvidenceDigest(ref, snapshot); err != nil {
				return err
			}
			verifiedEvidence[ref] = struct{}{}
		}
	}

	// Extra snapshots are also verified in sorted ID order so callers cannot
	// smuggle an invalid snapshot through a larger verification batch.
	extraEvidenceIDs := make([]string, 0, len(evidenceByID))
	for id := range evidenceByID {
		if _, verified := verifiedEvidence[id]; !verified {
			extraEvidenceIDs = append(extraEvidenceIDs, id)
		}
	}
	sort.Strings(extraEvidenceIDs)
	for _, id := range extraEvidenceIDs {
		if err := productionVerifyEvidenceDigest(id, evidenceByID[id]); err != nil {
			return err
		}
	}

	recomputedBlocks := make([]*types.ProductionDocumentBlock, 0, len(blocks))
	for _, block := range blocks {
		for _, field := range []struct {
			name     string
			value    types.JSON
			fallback string
		}{
			{name: "content", value: block.Content, fallback: "null"},
			{name: "attributes", value: block.Attributes, fallback: `{}`},
			{name: "evidence_refs", value: block.EvidenceRefs, fallback: `[]`},
			{name: "ai_provenance", value: block.AIProvenance, fallback: `{}`},
		} {
			if _, err := canonicalProductionJSON(field.value, field.fallback); err != nil {
				return fmt.Errorf("block %s has invalid %s JSON: %w", block.LogicalBlockID, field.name, err)
			}
		}
		recomputed := types.ComputeProductionBlockDigest(block)
		if recomputed != block.ContentDigest {
			return fmt.Errorf("block %s digest mismatch", block.LogicalBlockID)
		}
		copyBlock := *block
		copyBlock.ContentDigest = recomputed
		recomputedBlocks = append(recomputedBlocks, &copyBlock)
	}
	copyVersion := *version
	copyVersion.Blocks = recomputedBlocks
	recomputedVersion := types.ComputeProductionVersionDigest(&copyVersion)
	if recomputedVersion != version.ContentDigest {
		return errors.New("production document version digest mismatch")
	}
	return nil
}
