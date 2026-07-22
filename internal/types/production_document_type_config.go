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

var legacyProductionRequiredSections = map[string][]string{
	"software-development-baseline": {
		"基线范围与目标", "需求基线", "产品与交互设计基线", "技术方案与架构基线",
		"代码仓库、分支与提交基线", "依赖和运行环境基线", "测试、质量和安全基线",
		"发布、部署和回滚基线", "已知风险、例外和遗留项", "证据清单",
	},
	"project-retrospective": {
		"项目背景和目标", "关联研发基线", "计划与实际结果", "需求和范围变化", "质量、交付和运营数据",
		"事故、偏差和影响", "根因分析", "有效实践和经验", "改进行动项、负责人和截止时间", "证据清单",
	},
}

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

// NormalizeProductionDocumentTypeConfig returns strict canonical governance,
// adapting only the two pre-governance built-in document types.
func NormalizeProductionDocumentTypeConfig(
	code string,
	input ProductionDocumentTypeConfigInput,
) (ProductionDocumentTypeConfig, bool, error) {
	config, err := CanonicalProductionDocumentTypeConfig(input)
	if err == nil {
		return config, false, nil
	}
	if !legacyProductionDocumentTypeConfigInput(input) {
		return ProductionDocumentTypeConfig{}, false, err
	}
	legacy, ok := LegacyProductionDocumentTypeConfig(code)
	if !ok {
		return ProductionDocumentTypeConfig{}, false, err
	}
	return legacy, true, nil
}

// LegacyProductionDocumentTypeConfig returns the canonical governance assigned
// to the two built-in document types that predate persisted governance fields.
func LegacyProductionDocumentTypeConfig(code string) (ProductionDocumentTypeConfig, bool) {
	sections, ok := legacyProductionRequiredSections[code]
	if !ok {
		return ProductionDocumentTypeConfig{}, false
	}
	values := ProductionDocumentTypeConfig{
		BlockSchema: ProductionBlockSchemaV1{
			Version: 1, RequiredSections: append([]string(nil), sections...),
			AllowedBlockTypes: []string{"heading", "paragraph", "code", "callout", "list", "table", "image"},
		},
		SourceRequirements: ProductionSourceRequirementsV1{
			Version: 1, MinAcceptedEvidence: 1,
			AllowedSourceKinds: []ProductionSourceKind{
				ProductionSourceKindUpload, ProductionSourceKindDatasource, ProductionSourceKindMCP,
				ProductionSourceKindSkill, ProductionSourceKindManual,
			},
			RequireEvidenceSection: true,
		},
		SkillBindings: ProductionSkillBindingsV1{Version: 1, Skills: []ProductionSkillBindingV1{}},
		WorkflowPlan:  ProductionWorkflowPlanV1{Version: 1, Steps: []ProductionWorkflowStepV1{}},
		QualityRules: ProductionQualityRulesV1{
			Version: 1, RequireEvidenceForFacts: true,
			Gates: []string{"section_completeness", "fact_evidence"},
		},
		ReviewPolicy: ProductionReviewPolicyV1{Steps: []ProductionRole{ProductionRoleBusinessReviewer}},
		PublicationPolicy: ProductionPublicationPolicyV1{
			Version: 1, TargetType: "knowledge_base", Chunking: "inherit_target",
			KnowledgeGraph: "inherit_target", RequireApprovedReview: true,
		},
	}
	input := ProductionDocumentTypeConfigInput{}
	var err error
	input.BlockSchema, err = canonicalProductionDocumentTypeConfigValue(values.BlockSchema)
	if err != nil {
		return ProductionDocumentTypeConfig{}, false
	}
	input.SourceRequirements, err = canonicalProductionDocumentTypeConfigValue(values.SourceRequirements)
	if err != nil {
		return ProductionDocumentTypeConfig{}, false
	}
	input.SkillBindings, err = canonicalProductionDocumentTypeConfigValue(values.SkillBindings)
	if err != nil {
		return ProductionDocumentTypeConfig{}, false
	}
	input.WorkflowPlan, err = canonicalProductionDocumentTypeConfigValue(values.WorkflowPlan)
	if err != nil {
		return ProductionDocumentTypeConfig{}, false
	}
	input.QualityRules, err = canonicalProductionDocumentTypeConfigValue(values.QualityRules)
	if err != nil {
		return ProductionDocumentTypeConfig{}, false
	}
	input.ReviewPolicy, err = canonicalProductionDocumentTypeConfigValue(values.ReviewPolicy)
	if err != nil {
		return ProductionDocumentTypeConfig{}, false
	}
	input.PublicationPolicy, err = canonicalProductionDocumentTypeConfigValue(values.PublicationPolicy)
	if err != nil {
		return ProductionDocumentTypeConfig{}, false
	}
	canonical, err := CanonicalProductionDocumentTypeConfig(input)
	return canonical, err == nil
}

func legacyProductionDocumentTypeConfigInput(input ProductionDocumentTypeConfigInput) bool {
	for _, raw := range []JSON{
		input.BlockSchema, input.SourceRequirements, input.SkillBindings,
		input.QualityRules, input.ReviewPolicy, input.PublicationPolicy,
	} {
		if !emptyProductionJSONObject(raw) {
			return false
		}
	}
	if emptyProductionJSONObject(input.WorkflowPlan) {
		return len(input.WorkflowPlan) != 0
	}
	canonical, err := CanonicalProductionWorkflowPlanSnapshot(input.WorkflowPlan)
	return err == nil && bytes.Equal(canonical, input.WorkflowPlan) &&
		bytes.Equal(canonical, JSON(`{"steps":[],"version":1}`))
}

func emptyProductionJSONObject(raw JSON) bool {
	if len(raw) == 0 {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil && len(object) == 0
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
