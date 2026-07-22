package types

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	ProductionDocumentTypeConfigMaxBytes = 64 << 10
	ProductionDocumentTypeConfigMaxDepth = 16
	productionDocumentTypeConfigVersion  = 1
)

var ErrProductionDocumentTypeConfigInvalid = errors.New("production document type config is invalid")

type ProductionBlockSchemaV1 struct {
	Version           int      `json:"version"`
	RequiredSections  []string `json:"required_sections"`
	AllowedBlockTypes []string `json:"allowed_block_types"`
}

type ProductionSourceRequirementsV1 struct {
	Version                int                    `json:"version"`
	MinAcceptedEvidence    int                    `json:"min_accepted_evidence"`
	AllowedSourceKinds     []ProductionSourceKind `json:"allowed_source_kinds"`
	RequireEvidenceSection bool                   `json:"require_evidence_section"`
	AllowUnsupportedFacts  bool                   `json:"allow_unsupported_facts"`
}

type ProductionSkillBindingV1 struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type ProductionSkillBindingsV1 struct {
	Version int                        `json:"version"`
	Skills  []ProductionSkillBindingV1 `json:"skills"`
}

type ProductionWorkflowStepV1 struct {
	ProviderType ProductionToolProviderType `json:"provider_type"`
	ProviderID   string                     `json:"provider_id"`
	ToolName     string                     `json:"tool_name"`
	Request      json.RawMessage            `json:"request"`
}

type ProductionWorkflowPlanV1 struct {
	Version int                        `json:"version"`
	Steps   []ProductionWorkflowStepV1 `json:"steps"`
}

type ProductionQualityRulesV1 struct {
	Version                 int      `json:"version"`
	RequireEvidenceForFacts bool     `json:"require_evidence_for_facts"`
	BlockNeedsConfirmation  bool     `json:"block_needs_confirmation"`
	Gates                   []string `json:"gates"`
}

type ProductionReviewPolicyV1 struct {
	Steps []ProductionRole `json:"steps"`
}

type ProductionPublicationPolicyV1 struct {
	Version               int    `json:"version"`
	TargetType            string `json:"target_type"`
	Chunking              string `json:"chunking"`
	KnowledgeGraph        string `json:"knowledge_graph"`
	RequireApprovedReview bool   `json:"require_approved_review"`
}

type ProductionDocumentTypeConfigInput struct {
	BlockSchema, SourceRequirements, SkillBindings, WorkflowPlan JSON
	QualityRules, ReviewPolicy, PublicationPolicy                JSON
}

// ProductionDocumentTypeConfig pairs validated v1 values with the exact
// canonical bytes callers must persist and use for digests.
type ProductionDocumentTypeConfig struct {
	BlockSchema        ProductionBlockSchemaV1
	SourceRequirements ProductionSourceRequirementsV1
	SkillBindings      ProductionSkillBindingsV1
	WorkflowPlan       ProductionWorkflowPlanV1
	QualityRules       ProductionQualityRulesV1
	ReviewPolicy       ProductionReviewPolicyV1
	PublicationPolicy  ProductionPublicationPolicyV1
	Canonical          ProductionDocumentTypeConfigInput
}

var productionAllowedBlockTypes = map[string]struct{}{
	"heading": {}, "paragraph": {}, "code": {}, "callout": {},
	"list": {}, "table": {}, "image": {},
}

var productionAllowedQualityGates = map[string]struct{}{
	"section_completeness": {}, "fact_evidence": {}, "no_unconfirmed": {},
	"sop_exception_path": {}, "policy_approval_control": {},
	"product_scope_boundary": {}, "faq_effective_date": {}, "incident_timeline": {},
}

func CanonicalProductionDocumentTypeConfig(input ProductionDocumentTypeConfigInput) (ProductionDocumentTypeConfig, error) {
	var output ProductionDocumentTypeConfig
	if err := decodeProductionDocumentTypeConfig(input.BlockSchema, &output.BlockSchema,
		"version", "required_sections", "allowed_block_types"); err != nil {
		return output, configFieldError("block_schema", err)
	}
	if output.BlockSchema.Version != productionDocumentTypeConfigVersion ||
		validateUniqueStrings(output.BlockSchema.RequiredSections, nil) != nil ||
		validateUniqueStrings(output.BlockSchema.AllowedBlockTypes, productionAllowedBlockTypes) != nil {
		return output, configFieldError("block_schema", ErrProductionDocumentTypeConfigInvalid)
	}

	if err := decodeProductionDocumentTypeConfig(input.SourceRequirements, &output.SourceRequirements,
		"version", "min_accepted_evidence", "allowed_source_kinds", "require_evidence_section", "allow_unsupported_facts"); err != nil {
		return output, configFieldError("source_requirements", err)
	}
	if output.SourceRequirements.Version != productionDocumentTypeConfigVersion ||
		output.SourceRequirements.MinAcceptedEvidence < 1 || len(output.SourceRequirements.AllowedSourceKinds) == 0 {
		return output, configFieldError("source_requirements", ErrProductionDocumentTypeConfigInvalid)
	}
	seenSourceKinds := make(map[ProductionSourceKind]struct{}, len(output.SourceRequirements.AllowedSourceKinds))
	for _, kind := range output.SourceRequirements.AllowedSourceKinds {
		if !kind.IsValid() {
			return output, configFieldError("source_requirements", ErrProductionDocumentTypeConfigInvalid)
		}
		if _, duplicate := seenSourceKinds[kind]; duplicate {
			return output, configFieldError("source_requirements", ErrProductionDocumentTypeConfigInvalid)
		}
		seenSourceKinds[kind] = struct{}{}
	}

	if err := decodeProductionDocumentTypeConfig(input.SkillBindings, &output.SkillBindings, "version", "skills"); err != nil {
		return output, configFieldError("skill_bindings", err)
	}
	if output.SkillBindings.Version != productionDocumentTypeConfigVersion || output.SkillBindings.Skills == nil {
		return output, configFieldError("skill_bindings", ErrProductionDocumentTypeConfigInvalid)
	}
	seenSkills := make(map[string]struct{}, len(output.SkillBindings.Skills))
	for _, skill := range output.SkillBindings.Skills {
		if strings.TrimSpace(skill.Name) != skill.Name || skill.Name == "" || len(skill.Name) > 255 || !validProductionConfigDigest(skill.Digest) {
			return output, configFieldError("skill_bindings", ErrProductionDocumentTypeConfigInvalid)
		}
		if _, duplicate := seenSkills[skill.Name]; duplicate {
			return output, configFieldError("skill_bindings", ErrProductionDocumentTypeConfigInvalid)
		}
		seenSkills[skill.Name] = struct{}{}
	}

	if err := decodeProductionDocumentTypeConfig(input.WorkflowPlan, &output.WorkflowPlan, "version", "steps"); err != nil {
		return output, configFieldError("workflow_plan", err)
	}
	canonicalWorkflow, err := CanonicalProductionWorkflowPlanSnapshot(input.WorkflowPlan)
	if err != nil || output.WorkflowPlan.Version != productionDocumentTypeConfigVersion || output.WorkflowPlan.Steps == nil {
		if err == nil {
			err = ErrProductionDocumentTypeConfigInvalid
		}
		return output, configFieldError("workflow_plan", err)
	}

	if err := decodeProductionDocumentTypeConfig(input.QualityRules, &output.QualityRules,
		"version", "require_evidence_for_facts", "block_needs_confirmation", "gates"); err != nil {
		return output, configFieldError("quality_rules", err)
	}
	if output.QualityRules.Version != productionDocumentTypeConfigVersion ||
		validateUniqueStrings(output.QualityRules.Gates, productionAllowedQualityGates) != nil {
		return output, configFieldError("quality_rules", ErrProductionDocumentTypeConfigInvalid)
	}

	if err := decodeProductionDocumentTypeConfig(input.ReviewPolicy, &output.ReviewPolicy, "steps"); err != nil {
		return output, configFieldError("review_policy", err)
	}
	if len(output.ReviewPolicy.Steps) == 0 {
		return output, configFieldError("review_policy", ErrProductionDocumentTypeConfigInvalid)
	}
	seenReviewers := make(map[ProductionRole]struct{}, len(output.ReviewPolicy.Steps))
	for _, reviewer := range output.ReviewPolicy.Steps {
		if reviewer != ProductionRoleBusinessReviewer && reviewer != ProductionRoleEngineeringReviewer && reviewer != ProductionRoleComplianceReviewer {
			return output, configFieldError("review_policy", ErrProductionDocumentTypeConfigInvalid)
		}
		if _, duplicate := seenReviewers[reviewer]; duplicate {
			return output, configFieldError("review_policy", ErrProductionDocumentTypeConfigInvalid)
		}
		seenReviewers[reviewer] = struct{}{}
	}

	if err := decodeProductionDocumentTypeConfig(input.PublicationPolicy, &output.PublicationPolicy,
		"version", "target_type", "chunking", "knowledge_graph", "require_approved_review"); err != nil {
		return output, configFieldError("publication_policy", err)
	}
	if output.PublicationPolicy.Version != productionDocumentTypeConfigVersion ||
		output.PublicationPolicy.TargetType != "knowledge_base" ||
		output.PublicationPolicy.Chunking != "inherit_target" ||
		output.PublicationPolicy.KnowledgeGraph != "inherit_target" ||
		!output.PublicationPolicy.RequireApprovedReview {
		return output, configFieldError("publication_policy", ErrProductionDocumentTypeConfigInvalid)
	}

	output.Canonical.BlockSchema, err = canonicalProductionDocumentTypeConfigValue(output.BlockSchema)
	if err != nil {
		return ProductionDocumentTypeConfig{}, configFieldError("block_schema", err)
	}
	output.Canonical.SourceRequirements, err = canonicalProductionDocumentTypeConfigValue(output.SourceRequirements)
	if err != nil {
		return ProductionDocumentTypeConfig{}, configFieldError("source_requirements", err)
	}
	output.Canonical.SkillBindings, err = canonicalProductionDocumentTypeConfigValue(output.SkillBindings)
	if err != nil {
		return ProductionDocumentTypeConfig{}, configFieldError("skill_bindings", err)
	}
	output.Canonical.WorkflowPlan = canonicalWorkflow
	output.Canonical.QualityRules, err = canonicalProductionDocumentTypeConfigValue(output.QualityRules)
	if err != nil {
		return ProductionDocumentTypeConfig{}, configFieldError("quality_rules", err)
	}
	output.Canonical.ReviewPolicy, err = canonicalProductionDocumentTypeConfigValue(output.ReviewPolicy)
	if err != nil {
		return ProductionDocumentTypeConfig{}, configFieldError("review_policy", err)
	}
	output.Canonical.PublicationPolicy, err = canonicalProductionDocumentTypeConfigValue(output.PublicationPolicy)
	if err != nil {
		return ProductionDocumentTypeConfig{}, configFieldError("publication_policy", err)
	}
	return output, nil
}

func decodeProductionDocumentTypeConfig(raw JSON, target any, requiredFields ...string) error {
	if err := ValidateProductionJSONResource(raw, ProductionDocumentTypeConfigMaxBytes, ProductionDocumentTypeConfigMaxDepth); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return ErrProductionDocumentTypeConfigInvalid
	}
	for _, field := range requiredFields {
		if _, present := fields[field]; !present {
			return fmt.Errorf("required field %q is missing", field)
		}
	}
	return nil
}

func canonicalProductionDocumentTypeConfigValue(value any) (JSON, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return CanonicalProductionJSON(JSON(encoded))
}

func validateUniqueStrings(values []string, allowed map[string]struct{}) error {
	if len(values) == 0 {
		return ErrProductionDocumentTypeConfigInvalid
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value || len(value) > 255 {
			return ErrProductionDocumentTypeConfigInvalid
		}
		if allowed != nil {
			if _, ok := allowed[value]; !ok {
				return ErrProductionDocumentTypeConfigInvalid
			}
		}
		if _, duplicate := seen[value]; duplicate {
			return ErrProductionDocumentTypeConfigInvalid
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validProductionConfigDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func configFieldError(field string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrProductionDocumentTypeConfigInvalid, field, err)
}
