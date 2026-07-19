package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func productionValidationBlock(logicalID, blockType string, position int, content, attributes, evidenceRefs string) *types.ProductionDocumentBlock {
	block := &types.ProductionDocumentBlock{
		LogicalBlockID: logicalID,
		BlockType:      blockType,
		Position:       position,
		Content:        types.JSON(content),
		Attributes:     types.JSON(attributes),
		EvidenceRefs:   types.JSON(evidenceRefs),
		AIProvenance:   types.JSON(`{}`),
	}
	block.ContentDigest = types.ComputeProductionBlockDigest(block)
	return block
}

func productionBaselineVersion() *types.ProductionDocumentVersion {
	template := BuiltinSoftwareDevelopmentBaseline()
	version := &types.ProductionDocumentVersion{DocumentTypeCode: template.Code}
	for position, section := range template.RequiredSections {
		version.Blocks = append(version.Blocks, productionValidationBlock(
			"section-"+string(rune('a'+position)), "heading", position,
			`"`+section+`"`, `{"level":2}`, `[]`,
		))
	}
	version.ContentDigest = types.ComputeProductionVersionDigest(version)
	return version
}

func productionIssueCodes(issues []ProductionValidationIssue) []string {
	codes := make([]string, 0, len(issues))
	for _, issue := range issues {
		codes = append(codes, issue.Code)
	}
	return codes
}

func TestProductionEvidenceValidatorRequiresExplicitKnownTemplateContext(t *testing.T) {
	missing := productionBaselineVersion()
	missing.DocumentTypeCode = ""
	unknown := productionBaselineVersion()
	unknown.DocumentTypeCode = "unknown"

	require.Equal(t, []string{"document_type_code_required"}, productionIssueCodes(ValidateProductionVersion(missing, nil).Errors))
	require.Equal(t, []string{"document_type_code_unknown"}, productionIssueCodes(ValidateProductionVersion(unknown, nil).Errors))
	require.Equal(t, []string{"version_required"}, productionIssueCodes(ValidateProductionVersion(nil, nil).Errors))
}

func TestProductionEvidenceValidatorRejectsMissingRequiredSection(t *testing.T) {
	version := productionBaselineVersion()
	version.Blocks = version.Blocks[:len(version.Blocks)-1]

	result := ValidateProductionVersion(version, nil)
	require.Equal(t, []string{"required_section_missing"}, productionIssueCodes(result.Errors))
	require.Equal(t, "证据清单", result.Errors[0].Section)
}

func TestProductionEvidenceValidatorRejectsDuplicateLogicalIDsUnknownAndDuplicateEvidenceRefs(t *testing.T) {
	version := productionBaselineVersion()
	position := len(version.Blocks)
	version.Blocks = append(version.Blocks,
		productionValidationBlock("fact-a", "paragraph", position, `"fact"`, `{"factual":true}`, `["missing","missing"]`),
		productionValidationBlock("fact-a", "paragraph", position+1, `"confirmed later"`, `{"factual":true,"needs_confirmation":true}`, `[]`),
	)

	result := ValidateProductionVersion(version, map[string]struct{}{})
	require.Equal(t, []string{
		"duplicate_logical_block_id",
		"unknown_evidence_id",
		"duplicate_evidence_ref",
		"factual_evidence_required",
	}, productionIssueCodes(result.Errors))
	require.Equal(t, "fact-a", result.Errors[0].LogicalBlockID)
	require.Equal(t, "missing", result.Errors[1].EvidenceID)
}

func TestProductionEvidenceValidatorRequiresEvidenceOrExplicitConfirmationForFacts(t *testing.T) {
	version := productionBaselineVersion()
	version.Blocks = append(version.Blocks,
		productionValidationBlock("fact-a", "paragraph", len(version.Blocks), `"fact"`, `{"factual":true}`, `[]`),
	)

	result := ValidateProductionVersion(version, nil)
	require.Equal(t, []string{"factual_evidence_required"}, productionIssueCodes(result.Errors))

	version.Blocks[len(version.Blocks)-1].Attributes = types.JSON(`{"factual":true,"needs_confirmation":true}`)
	require.Empty(t, ValidateProductionVersion(version, nil).Errors)
}

func TestProductionEvidenceValidatorRejectsMalformedAndUnsupportedBlocksInStableOrder(t *testing.T) {
	version := productionBaselineVersion()
	position := len(version.Blocks)
	version.Blocks = append(version.Blocks,
		nil,
		productionValidationBlock("z", "video", position+3, `"x"`, `{}`, `[]`),
		productionValidationBlock("b", "paragraph", position+2, `{"text":"x"}`, `{}`, `[]`),
		productionValidationBlock("a", "paragraph", position, `"x"`, `[]`, `[]`),
		productionValidationBlock("c", "paragraph", position+1, `"x"`, `{}`, `{"id":"e-1"}`),
	)

	first := ValidateProductionVersion(version, nil)
	second := ValidateProductionVersion(version, nil)
	require.Equal(t, first, second)
	require.Equal(t, []string{
		"nil_block",
		"invalid_block_attributes",
		"invalid_evidence_refs",
		"invalid_block_content",
		"unsupported_block_type",
	}, productionIssueCodes(first.Errors))
}

func TestProductionEvidenceValidatorIssueOrderDoesNotDependOnLoadedBlockOrder(t *testing.T) {
	version := productionBaselineVersion()
	version.Blocks = append(version.Blocks,
		productionValidationBlock("b", "paragraph", 101, `{"bad":true}`, `{}`, `[]`),
		productionValidationBlock("a", "paragraph", 100, `"fact"`, `{"factual":true}`, `[]`),
	)
	first := ValidateProductionVersion(version, nil)
	version.Blocks[len(version.Blocks)-1], version.Blocks[len(version.Blocks)-2] = version.Blocks[len(version.Blocks)-2], version.Blocks[len(version.Blocks)-1]
	second := ValidateProductionVersion(version, nil)
	require.Equal(t, first, second)
}

func TestProductionEvidenceValidatorRejectsMalformedIdentityProvenanceAndAttributesDeterministically(t *testing.T) {
	version := productionBaselineVersion()
	block := productionValidationBlock("", "paragraph", -1, `"x"`, `{"level":"bad","ordered":"bad"}`, `[]`)
	block.AIProvenance = types.JSON(`{"broken"`)
	version.Blocks = append(version.Blocks, block)

	first := ValidateProductionVersion(version, nil)
	for range 20 {
		require.Equal(t, first, ValidateProductionVersion(version, nil))
	}
	require.Equal(t, []string{
		"logical_block_id_required",
		"block_position_invalid",
		"invalid_block_attributes",
		"invalid_ai_provenance",
	}, productionIssueCodes(first.Errors))
	require.Contains(t, first.Errors[2].Message, "level")
}

func TestProductionEvidenceDigestVerificationRecomputesInlineEvidenceBlocksAndVersion(t *testing.T) {
	version := productionBaselineVersion()
	block := version.Blocks[0]
	block.Content = types.JSON(`{"value":1}`)
	block.ContentDigest = types.ComputeProductionBlockDigest(block)
	version.ContentDigest = types.ComputeProductionVersionDigest(version)

	canonical := []byte(`{"value":1}`)
	sum := sha256.Sum256(canonical)
	evidence := &types.ProductionEvidenceSnapshot{
		ID: "e-1", SnapshotType: types.ProductionEvidenceSnapshotJSON,
		InlineContent: canonical, ContentDigest: hex.EncodeToString(sum[:]),
	}

	// Task 3 canonical number behavior makes equivalent JSON numbers hash identically.
	block.Content = types.JSON(`{"value":1.000e0}`)
	require.NoError(t, VerifyProductionVersionDigests(version, map[string]*types.ProductionEvidenceSnapshot{"e-1": evidence}))
}

func TestProductionEvidenceDigestVerificationDetectsEveryInlineDigestMismatch(t *testing.T) {
	base := productionBaselineVersion()
	evidence := &types.ProductionEvidenceSnapshot{
		ID: "e-1", SnapshotType: types.ProductionEvidenceSnapshotText,
		InlineContent: types.JSON(`"evidence"`), ContentDigest: strings.Repeat("0", 64),
	}
	require.ErrorContains(t, VerifyProductionVersionDigests(base, map[string]*types.ProductionEvidenceSnapshot{"e-1": evidence}), "evidence e-1")

	blockMismatch := productionBaselineVersion()
	blockMismatch.Blocks[0].ContentDigest = strings.Repeat("0", 64)
	require.ErrorContains(t, VerifyProductionVersionDigests(blockMismatch, nil), "block section-a")

	versionMismatch := productionBaselineVersion()
	versionMismatch.ContentDigest = strings.Repeat("0", 64)
	require.ErrorContains(t, VerifyProductionVersionDigests(versionMismatch, nil), "version")
}

func TestProductionEvidenceDigestVerificationValidatesRegistrySnapshotsWithoutReadingStorage(t *testing.T) {
	version := productionBaselineVersion()
	registry := &types.ProductionEvidenceSnapshot{
		ID: "file-1", SnapshotType: types.ProductionEvidenceSnapshotFile,
		StoragePath:   types.BuildResourcePath(strings.Repeat("a", types.ResourceHandleLength)),
		ContentDigest: strings.Repeat("c", 64), ResolvedContentDigest: strings.Repeat("c", 64),
	}
	require.NoError(t, VerifyProductionVersionDigests(version, map[string]*types.ProductionEvidenceSnapshot{"file-1": registry}))

	registry.ResolvedContentDigest = ""
	require.ErrorContains(t, VerifyProductionVersionDigests(version, map[string]*types.ProductionEvidenceSnapshot{"file-1": registry}), "resolved content digest")
	registry.ResolvedContentDigest = strings.Repeat("d", 64)
	require.ErrorContains(t, VerifyProductionVersionDigests(version, map[string]*types.ProductionEvidenceSnapshot{"file-1": registry}), "digest mismatch")
	registry.ResolvedContentDigest = strings.Repeat("c", 64)
	registry.StoragePath = "/physical/secret/path"
	require.ErrorContains(t, VerifyProductionVersionDigests(version, map[string]*types.ProductionEvidenceSnapshot{"file-1": registry}), "resource reference")
	registry.StoragePath = types.BuildResourcePath(strings.Repeat("a", types.ResourceHandleLength))
	registry.ContentDigest = "not-a-digest"
	require.ErrorContains(t, VerifyProductionVersionDigests(version, map[string]*types.ProductionEvidenceSnapshot{"file-1": registry}), "SHA-256")
}

func TestProductionEvidenceDigestVerificationRejectsNilAndMalformedEvidence(t *testing.T) {
	require.ErrorContains(t, VerifyProductionVersionDigests(nil, nil), "version")
	version := productionBaselineVersion()
	require.ErrorContains(t, VerifyProductionVersionDigests(version, map[string]*types.ProductionEvidenceSnapshot{"e-1": nil}), "nil")
	require.ErrorContains(t, VerifyProductionVersionDigests(version, map[string]*types.ProductionEvidenceSnapshot{
		"wrong": {ID: "e-1", SnapshotType: types.ProductionEvidenceSnapshotText, InlineContent: types.JSON(`"x"`)},
	}), "map key")
}

func TestProductionEvidenceDigestVerificationUsesStableMalformedFieldOrder(t *testing.T) {
	version := productionBaselineVersion()
	version.Blocks[0].Content = types.JSON(`{"bad"`)
	version.Blocks[0].Attributes = types.JSON(`{"also_bad"`)

	for range 20 {
		err := VerifyProductionVersionDigests(version, nil)
		require.ErrorContains(t, err, "content JSON")
	}
}
