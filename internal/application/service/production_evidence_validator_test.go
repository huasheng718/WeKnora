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

func validateLegacyProductionVersion(
	version *types.ProductionDocumentVersion,
	acceptedEvidence map[string]struct{},
) ProductionValidationResult {
	code := "software-development-baseline"
	if version != nil && version.DocumentTypeCode != "" {
		code = version.DocumentTypeCode
	}
	config, ok := legacyProductionDocumentTypeConfig(code)
	if !ok {
		return ProductionValidationResult{Errors: []ProductionValidationIssue{{
			Code: "document_type_code_unknown", Message: "legacy production fixture code is unknown",
		}}}
	}
	return ValidateProductionVersion(version, acceptedEvidence, config.BlockSchema, config.QualityRules)
}

func TestValidateProductionVersionUsesSnapshotGovernanceAndQualityRules(t *testing.T) {
	blockSchema := types.ProductionBlockSchemaV1{
		Version: 1, RequiredSections: []string{"Snapshot Required"},
		AllowedBlockTypes: []string{"heading", "paragraph"},
	}
	qualityRules := types.ProductionQualityRulesV1{
		Version: 1, RequireEvidenceForFacts: true, BlockNeedsConfirmation: true,
		Gates: []string{"section_completeness", "fact_evidence", "no_unconfirmed"},
	}
	version := &types.ProductionDocumentVersion{Blocks: []*types.ProductionDocumentBlock{
		productionValidationBlock("factual", "paragraph", 0, `"unsupported fact"`, `{"factual":true}`, `[]`),
		productionValidationBlock("confirmation", "paragraph", 1, `"pending"`, `{"needs_confirmation":true}`, `[]`),
	}}

	result := ValidateProductionVersion(version, nil, blockSchema, qualityRules)

	require.Equal(t, []string{
		"required_section_missing", "factual_evidence_required", "needs_confirmation_blocked",
	}, productionIssueCodes(result.Errors))
}

func TestValidateProductionVersionAppliesEverySnapshotTypeSpecificQualityGate(t *testing.T) {
	tests := []struct {
		gate    string
		heading string
	}{
		{gate: "sop_exception_path", heading: "异常处理"},
		{gate: "policy_approval_control", heading: "审批控制"},
		{gate: "product_scope_boundary", heading: "限制条件"},
		{gate: "faq_effective_date", heading: "来源与生效日期"},
		{gate: "incident_timeline", heading: "诊断过程"},
	}
	for _, test := range tests {
		t.Run(test.gate, func(t *testing.T) {
			blockSchema := types.ProductionBlockSchemaV1{Version: 1, RequiredSections: []string{"Present"}, AllowedBlockTypes: []string{"heading"}}
			qualityRules := types.ProductionQualityRulesV1{Version: 1, Gates: []string{test.gate}}
			version := &types.ProductionDocumentVersion{Blocks: []*types.ProductionDocumentBlock{
				productionValidationBlock("present", "heading", 0, `"Present"`, `{}`, `[]`),
			}}

			result := ValidateProductionVersion(version, nil, blockSchema, qualityRules)

			require.Equal(t, []string{"required_section_missing"}, productionIssueCodes(result.Errors))
			require.Equal(t, test.heading, result.Errors[0].Section)
		})
	}
}

func TestValidateProductionVersionDeduplicatesMissingSectionAcrossQualityGates(t *testing.T) {
	blockSchema := types.ProductionBlockSchemaV1{
		Version: 1, RequiredSections: []string{"异常处理"}, AllowedBlockTypes: []string{"heading"},
	}
	qualityRules := types.ProductionQualityRulesV1{
		Version: 1, Gates: []string{"section_completeness", "sop_exception_path"},
	}

	result := ValidateProductionVersion(&types.ProductionDocumentVersion{}, nil, blockSchema, qualityRules)

	require.Equal(t, []string{"required_section_missing"}, productionIssueCodes(result.Errors))
	require.Equal(t, "异常处理", result.Errors[0].Section)
}

func TestProductionEvidenceValidatorLegacyAdapterRequiresKnownFixtureCode(t *testing.T) {
	missing := productionBaselineVersion()
	missing.DocumentTypeCode = ""
	unknown := productionBaselineVersion()
	unknown.DocumentTypeCode = "unknown"

	require.Empty(t, validateLegacyProductionVersion(missing, nil).Errors)
	require.Equal(t, []string{"document_type_code_unknown"}, productionIssueCodes(validateLegacyProductionVersion(unknown, nil).Errors))
	require.Equal(t, []string{"version_required"}, productionIssueCodes(validateLegacyProductionVersion(nil, nil).Errors))
}

func TestProductionEvidenceValidatorRejectsMissingRequiredSection(t *testing.T) {
	version := productionBaselineVersion()
	version.Blocks = version.Blocks[:len(version.Blocks)-1]

	result := validateLegacyProductionVersion(version, nil)
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

	result := validateLegacyProductionVersion(version, map[string]struct{}{})
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

	result := validateLegacyProductionVersion(version, nil)
	require.Equal(t, []string{"factual_evidence_required"}, productionIssueCodes(result.Errors))

	version.Blocks[len(version.Blocks)-1].Attributes = types.JSON(`{"factual":true,"needs_confirmation":true}`)
	require.Empty(t, validateLegacyProductionVersion(version, nil).Errors)
}

func TestValidateProductionVersionRejectsRawFactAndAcceptsNormalizedFactualParagraph(t *testing.T) {
	rawFact := productionBaselineVersion()
	rawFact.Blocks = append(rawFact.Blocks,
		productionValidationBlock("raw-fact", "fact", len(rawFact.Blocks), `{"text":"claim"}`, `{}`, `[]`),
	)
	require.Contains(t, productionIssueCodes(validateLegacyProductionVersion(rawFact, nil).Errors), "unsupported_block_type")

	withoutEvidence := productionBaselineVersion()
	withoutEvidence.Blocks = append(withoutEvidence.Blocks,
		productionValidationBlock("factual", "paragraph", len(withoutEvidence.Blocks), `"claim"`, `{"factual":true}`, `[]`),
	)
	require.Equal(t, []string{"factual_evidence_required"}, productionIssueCodes(validateLegacyProductionVersion(withoutEvidence, nil).Errors))

	withEvidence := productionBaselineVersion()
	withEvidence.Blocks = append(withEvidence.Blocks,
		productionValidationBlock("factual", "paragraph", len(withEvidence.Blocks), `"claim"`, `{"factual":true}`, `["e-1"]`),
	)
	require.Empty(t, validateLegacyProductionVersion(withEvidence, map[string]struct{}{"e-1": {}}).Errors)

	withConfirmation := productionBaselineVersion()
	withConfirmation.Blocks = append(withConfirmation.Blocks,
		productionValidationBlock("factual", "paragraph", len(withConfirmation.Blocks), `"claim"`, `{"factual":true,"needs_confirmation":true}`, `[]`),
	)
	require.Empty(t, validateLegacyProductionVersion(withConfirmation, nil).Errors)
}

func TestProductionEvidenceValidatorClassifiesClaimCapableBlocksWithoutProducerOptIn(t *testing.T) {
	version := productionBaselineVersion()
	position := len(version.Blocks)
	version.Blocks = append(version.Blocks,
		productionValidationBlock("paragraph", "paragraph", position, `"claim"`, `{"factual":false}`, `[]`),
		productionValidationBlock("list", "list", position+1, `["claim"]`, `{}`, `[]`),
		productionValidationBlock("table", "table", position+2, `{"headers":["claim"],"rows":[["value"]]}`, `{}`, `[]`),
		productionValidationBlock("quote", "quote", position+3, `"claim"`, `{}`, `[]`),
		productionValidationBlock("heading", "heading", position+4, `"Non-claim heading"`, `{}`, `[]`),
		productionValidationBlock("code", "code", position+5, `"const x = 1"`, `{}`, `[]`),
		productionValidationBlock("image", "image", position+6, `{"alt":"x","url":"https://example.com/x.png"}`, `{}`, `[]`),
	)

	result := validateLegacyProductionVersion(version, nil)
	require.Equal(t, 4, strings.Count(strings.Join(productionIssueCodes(result.Errors), ","), "factual_evidence_required"))
	require.Contains(t, productionIssueCodes(result.Errors), "unsupported_block_type")
}

func TestProductionEvidenceValidatorAcceptsGovernedEvidenceOrExplicitConfirmationForClaims(t *testing.T) {
	version := productionBaselineVersion()
	position := len(version.Blocks)
	version.Blocks = append(version.Blocks,
		productionValidationBlock("with-evidence", "paragraph", position, `"claim"`, `{"factual":false}`, `["e-1"]`),
		productionValidationBlock("confirmation", "table", position+1, `{"headers":["claim"],"rows":[]}`, `{"needs_confirmation":true}`, `[]`),
	)

	result := validateLegacyProductionVersion(version, map[string]struct{}{"e-1": {}})
	require.NotContains(t, productionIssueCodes(result.Errors), "factual_evidence_required")
	require.Empty(t, result.Errors)
}

func TestProductionEvidenceValidatorUsesRendererSafeImageURLPolicy(t *testing.T) {
	for _, imageURL := range []string{
		"javascript:alert(1)",
		"https://user:secret@example.com/a.png",
		"https://example.com/a.png?token=secret",
		"https://example.com/a.png#fragment",
	} {
		t.Run(imageURL, func(t *testing.T) {
			version := productionBaselineVersion()
			version.Blocks = append(version.Blocks, productionValidationBlock(
				"image", "image", len(version.Blocks), `{"alt":"x","url":"`+imageURL+`"}`, `{}`, `[]`,
			))

			require.Contains(t, productionIssueCodes(validateLegacyProductionVersion(version, nil).Errors), "invalid_block_content")
		})
	}

	valid := productionBaselineVersion()
	valid.Blocks = append(valid.Blocks, productionValidationBlock(
		"image", "image", len(valid.Blocks), `{"alt":"x","url":"https://example.com/a.png"}`, `{}`, `[]`,
	))
	require.Empty(t, validateLegacyProductionVersion(valid, nil).Errors)
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

	first := validateLegacyProductionVersion(version, nil)
	second := validateLegacyProductionVersion(version, nil)
	require.Equal(t, first, second)
	require.Equal(t, []string{
		"nil_block",
		"invalid_block_attributes",
		"factual_evidence_required",
		"invalid_evidence_refs",
		"factual_evidence_required",
		"invalid_block_content",
		"factual_evidence_required",
		"unsupported_block_type",
	}, productionIssueCodes(first.Errors))
}

func TestProductionEvidenceValidatorIssueOrderDoesNotDependOnLoadedBlockOrder(t *testing.T) {
	version := productionBaselineVersion()
	version.Blocks = append(version.Blocks,
		productionValidationBlock("b", "paragraph", 101, `{"bad":true}`, `{}`, `[]`),
		productionValidationBlock("a", "paragraph", 100, `"fact"`, `{"factual":true}`, `[]`),
	)
	first := validateLegacyProductionVersion(version, nil)
	version.Blocks[len(version.Blocks)-1], version.Blocks[len(version.Blocks)-2] = version.Blocks[len(version.Blocks)-2], version.Blocks[len(version.Blocks)-1]
	second := validateLegacyProductionVersion(version, nil)
	require.Equal(t, first, second)
}

func TestProductionEvidenceValidatorRejectsMalformedIdentityProvenanceAndAttributesDeterministically(t *testing.T) {
	version := productionBaselineVersion()
	block := productionValidationBlock("", "paragraph", -1, `"x"`, `{"level":"bad","ordered":"bad"}`, `[]`)
	block.AIProvenance = types.JSON(`{"broken"`)
	version.Blocks = append(version.Blocks, block)

	first := validateLegacyProductionVersion(version, nil)
	for range 20 {
		require.Equal(t, first, validateLegacyProductionVersion(version, nil))
	}
	require.Equal(t, []string{
		"logical_block_id_required",
		"block_position_invalid",
		"invalid_block_attributes",
		"invalid_ai_provenance",
		"factual_evidence_required",
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

func TestProductionEvidenceDigestVerificationRequiresEveryReferencedSnapshot(t *testing.T) {
	for _, evidenceID := range []string{"inline-1", "registry-1"} {
		t.Run(evidenceID, func(t *testing.T) {
			version := productionBaselineVersion()
			version.Blocks[0].EvidenceRefs = types.JSON(`[` + `"` + evidenceID + `"` + `]`)
			version.Blocks[0].ContentDigest = types.ComputeProductionBlockDigest(version.Blocks[0])
			version.ContentDigest = types.ComputeProductionVersionDigest(version)

			err := VerifyProductionVersionDigests(version, map[string]*types.ProductionEvidenceSnapshot{})
			require.ErrorContains(t, err, "referenced evidence "+evidenceID+" is missing")
		})
	}
}

func TestProductionEvidenceDigestVerificationRejectsMalformedAndDuplicateBlockRefs(t *testing.T) {
	version := productionBaselineVersion()
	version.Blocks[0].EvidenceRefs = types.JSON(`["e-1","e-1"]`)
	version.Blocks[0].ContentDigest = types.ComputeProductionBlockDigest(version.Blocks[0])
	version.ContentDigest = types.ComputeProductionVersionDigest(version)
	require.ErrorContains(t, VerifyProductionVersionDigests(version, nil), "duplicate evidence reference")

	version.Blocks[0].EvidenceRefs = types.JSON(`{"evidence":"e-1"}`)
	version.Blocks[0].ContentDigest = types.ComputeProductionBlockDigest(version.Blocks[0])
	version.ContentDigest = types.ComputeProductionVersionDigest(version)
	require.ErrorContains(t, VerifyProductionVersionDigests(version, nil), "invalid evidence references")
}

func TestProductionEvidenceDigestVerificationCanonicalizesEquivalentNumbersExactly(t *testing.T) {
	canonical, err := types.CanonicalProductionJSON(types.JSON(`{"n":1}`))
	require.NoError(t, err)
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])

	for _, inline := range []types.JSON{types.JSON(`{"n":1}`), types.JSON(`{"n":1.0}`), types.JSON(`{"n":1e0}`)} {
		snapshot := &types.ProductionEvidenceSnapshot{
			ID: "e-1", SnapshotType: types.ProductionEvidenceSnapshotJSON,
			InlineContent: inline, ContentDigest: digest,
		}
		require.NoError(t, VerifyProductionVersionDigests(productionBaselineVersion(), map[string]*types.ProductionEvidenceSnapshot{"e-1": snapshot}))
	}

	largeA, err := types.CanonicalProductionJSON(types.JSON(`{"n":123456789012345678901234567890,"d":0.0000000000000000001234500}`))
	require.NoError(t, err)
	largeB, err := types.CanonicalProductionJSON(types.JSON(`{"d":1.2345e-19,"n":12345678901234567890123456789e1}`))
	require.NoError(t, err)
	require.Equal(t, largeA, largeB)
}
