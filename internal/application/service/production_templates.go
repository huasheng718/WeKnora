package service

import (
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

// ProductionBuiltinTemplate is a governed built-in definition. Config holds
// its decoded and canonical persistence contract.
type ProductionBuiltinTemplate struct {
	Code             string
	TemplateKey      string
	Name             string
	Description      string
	RequiredSections []string
	QualityGates     []string
	Config           types.ProductionDocumentTypeConfig
	configInput      types.ProductionDocumentTypeConfigInput
}

type ProductionBuiltinCatalog struct {
	definitions []ProductionBuiltinTemplate
	lookup      map[string]ProductionBuiltinTemplate
}

func NewProductionBuiltinCatalog() (*ProductionBuiltinCatalog, error) {
	return newProductionBuiltinCatalog(productionBuiltinDefinitions())
}

func newProductionBuiltinCatalog(definitions []ProductionBuiltinTemplate) (*ProductionBuiltinCatalog, error) {
	if len(definitions) != 5 {
		return nil, fmt.Errorf("production built-in catalog requires exactly five definitions")
	}
	catalog := &ProductionBuiltinCatalog{
		definitions: make([]ProductionBuiltinTemplate, 0, len(definitions)),
		lookup:      make(map[string]ProductionBuiltinTemplate, len(definitions)+2),
	}
	seenCodes := make(map[string]struct{}, len(definitions))
	seenTemplateKeys := make(map[string]struct{}, len(definitions))
	expectedCodes := map[string]struct{}{
		"sop": {}, "policy_process": {}, "product_service_guide": {}, "faq": {}, "incident_playbook": {},
	}
	for _, definition := range definitions {
		if definition.Code == "" || definition.TemplateKey == "" || definition.Name == "" {
			return nil, fmt.Errorf("production built-in definition identity is required")
		}
		if _, expected := expectedCodes[definition.Code]; !expected || definition.TemplateKey != definition.Code {
			return nil, fmt.Errorf("unexpected production built-in identity %q/%q", definition.Code, definition.TemplateKey)
		}
		if _, duplicate := seenCodes[definition.Code]; duplicate {
			return nil, fmt.Errorf("duplicate production built-in code %q", definition.Code)
		}
		if _, duplicate := seenTemplateKeys[definition.TemplateKey]; duplicate {
			return nil, fmt.Errorf("duplicate production built-in template key %q", definition.TemplateKey)
		}
		config, err := types.CanonicalProductionDocumentTypeConfig(definition.configInput)
		if err != nil {
			return nil, fmt.Errorf("canonicalize production built-in %q: %w", definition.Code, err)
		}
		if !equalBuiltinStrings(definition.RequiredSections, config.BlockSchema.RequiredSections) ||
			!equalBuiltinStrings(definition.QualityGates, config.QualityRules.Gates) {
			return nil, fmt.Errorf("production built-in %q summary differs from its config", definition.Code)
		}
		definition.Config = config
		definition.configInput = types.ProductionDocumentTypeConfigInput{}
		definition = cloneProductionBuiltinTemplate(definition)
		seenCodes[definition.Code] = struct{}{}
		seenTemplateKeys[definition.TemplateKey] = struct{}{}
		catalog.definitions = append(catalog.definitions, definition)
		catalog.lookup[definition.Code] = definition
	}

	for _, legacy := range []ProductionBuiltinTemplate{BuiltinSoftwareDevelopmentBaseline(), BuiltinProjectRetrospective()} {
		if _, collision := catalog.lookup[legacy.Code]; collision {
			return nil, fmt.Errorf("legacy production template code %q collides with built-in catalog", legacy.Code)
		}
		catalog.lookup[legacy.Code] = cloneProductionBuiltinTemplate(legacy)
	}
	return catalog, nil
}

func (c *ProductionBuiltinCatalog) Definitions() []ProductionBuiltinTemplate {
	if c == nil {
		return nil
	}
	definitions := make([]ProductionBuiltinTemplate, len(c.definitions))
	for index := range c.definitions {
		definitions[index] = cloneProductionBuiltinTemplate(c.definitions[index])
	}
	return definitions
}

func (c *ProductionBuiltinCatalog) Rows(tenantID uint64, actor string) []types.ProductionDocumentType {
	definitions := c.Definitions()
	rows := make([]types.ProductionDocumentType, 0, len(definitions))
	for _, definition := range definitions {
		canonical := definition.Config.Canonical
		rows = append(rows, types.ProductionDocumentType{
			ID:                 uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("production-document-type:%d:%s:v1", tenantID, definition.Code))).String(),
			TenantID:           tenantID,
			Code:               definition.Code,
			Name:               definition.Name,
			Description:        definition.Description,
			SchemaVersion:      1,
			BlockSchema:        cloneBuiltinJSON(canonical.BlockSchema),
			SourceRequirements: cloneBuiltinJSON(canonical.SourceRequirements),
			SkillBindings:      cloneBuiltinJSON(canonical.SkillBindings),
			WorkflowPlan:       cloneBuiltinJSON(canonical.WorkflowPlan),
			QualityRules:       cloneBuiltinJSON(canonical.QualityRules),
			ReviewPolicy:       cloneBuiltinJSON(canonical.ReviewPolicy),
			PublicationPolicy:  cloneBuiltinJSON(canonical.PublicationPolicy),
			Status:             types.ProductionDocumentTypeActive,
			CreatedBy:          actor,
		})
	}
	return rows
}

func (c *ProductionBuiltinCatalog) Lookup(code string) (ProductionBuiltinTemplate, bool) {
	if c == nil {
		return ProductionBuiltinTemplate{}, false
	}
	definition, ok := c.lookup[code]
	if !ok {
		return ProductionBuiltinTemplate{}, false
	}
	return cloneProductionBuiltinTemplate(definition), true
}

func productionBuiltinDefinitions() []ProductionBuiltinTemplate {
	return []ProductionBuiltinTemplate{
		newProductionBuiltinDefinition(
			"sop", "标准作业程序", "面向可重复执行作业的受治理步骤、异常和证据规范",
			[]string{"目的与范围", "角色职责", "前置条件", "操作步骤", "异常处理", "风险控制", "验证记录", "证据清单"},
			[]string{"section_completeness", "fact_evidence", "no_unconfirmed", "sop_exception_path"},
			[]types.ProductionRole{types.ProductionRoleBusinessReviewer},
		),
		newProductionBuiltinDefinition(
			"policy_process", "制度与流程规范", "面向制度依据、权责、审批和例外控制的企业规范",
			[]string{"制定依据", "适用范围", "术语定义", "职责权限", "制度要求", "业务流程", "审批控制", "例外处理", "监督机制", "证据清单"},
			[]string{"section_completeness", "fact_evidence", "no_unconfirmed", "policy_approval_control"},
			[]types.ProductionRole{types.ProductionRoleBusinessReviewer, types.ProductionRoleComplianceReviewer},
		),
		newProductionBuiltinDefinition(
			"product_service_guide", "产品与服务知识", "面向产品能力、适用边界、服务标准和升级路径的知识指南",
			[]string{"产品定位", "核心能力", "适用场景", "使用前提", "配置与使用", "限制条件", "服务标准", "常见故障", "升级路径", "证据清单"},
			[]string{"section_completeness", "fact_evidence", "no_unconfirmed", "product_scope_boundary"},
			[]types.ProductionRole{types.ProductionRoleBusinessReviewer},
		),
		newProductionBuiltinDefinition(
			"faq", "常见问题与标准回答", "面向适用条件、标准回答、例外和时效的问答知识",
			[]string{"问题分类", "适用条件", "标准问题与回答", "例外情况", "处理建议", "升级路径", "来源与生效日期"},
			[]string{"section_completeness", "fact_evidence", "no_unconfirmed", "faq_effective_date"},
			[]types.ProductionRole{types.ProductionRoleBusinessReviewer},
		),
		newProductionBuiltinDefinition(
			"incident_playbook", "故障处理与案例复盘", "面向故障止损、诊断恢复、根因和改进行动的处置手册",
			[]string{"现象与影响", "事件等级", "止损措施", "诊断过程", "恢复步骤", "结果验证", "沟通升级", "根因分析", "改进行动", "证据清单"},
			[]string{"section_completeness", "fact_evidence", "no_unconfirmed", "incident_timeline"},
			[]types.ProductionRole{types.ProductionRoleEngineeringReviewer, types.ProductionRoleBusinessReviewer},
		),
	}
}

func newProductionBuiltinDefinition(code, name, description string, sections, gates []string, reviewers []types.ProductionRole) ProductionBuiltinTemplate {
	return ProductionBuiltinTemplate{
		Code: code, TemplateKey: code, Name: name, Description: description,
		RequiredSections: append([]string(nil), sections...),
		QualityGates:     append([]string(nil), gates...),
		configInput: types.ProductionDocumentTypeConfigInput{
			BlockSchema: productionBuiltinJSON(map[string]any{
				"version": 1, "required_sections": sections,
				"allowed_block_types": []string{"heading", "paragraph", "code", "callout", "list", "table", "image"},
			}),
			SourceRequirements: types.JSON(`{"version":1,"min_accepted_evidence":1,"allowed_source_kinds":["upload","datasource","mcp","skill","manual"],"require_evidence_section":true,"allow_unsupported_facts":false}`),
			SkillBindings:      types.JSON(`{"version":1,"skills":[]}`),
			WorkflowPlan:       types.JSON(`{"version":1,"steps":[]}`),
			QualityRules: productionBuiltinJSON(map[string]any{
				"version": 1, "require_evidence_for_facts": true,
				"block_needs_confirmation": true, "gates": gates,
			}),
			ReviewPolicy:      productionBuiltinJSON(map[string]any{"steps": reviewers}),
			PublicationPolicy: types.JSON(`{"version":1,"target_type":"knowledge_base","chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true}`),
		},
	}
}

func productionBuiltinJSON(value any) types.JSON {
	encoded, _ := json.Marshal(value)
	return types.JSON(encoded)
}

func BuiltinSoftwareDevelopmentBaseline() ProductionBuiltinTemplate {
	config, _ := types.LegacyProductionDocumentTypeConfig("software-development-baseline")
	return ProductionBuiltinTemplate{
		Code: "software-development-baseline", TemplateKey: "software-development-baseline", Name: "软件研发基线",
		RequiredSections: append([]string(nil), config.BlockSchema.RequiredSections...),
		QualityGates:     []string{"必需章节", "基线时间", "仓库和提交引用", "测试结果", "发布版本", "风险责任人", "证据覆盖率"},
	}
}

func BuiltinProjectRetrospective() ProductionBuiltinTemplate {
	config, _ := types.LegacyProductionDocumentTypeConfig("project-retrospective")
	return ProductionBuiltinTemplate{
		Code: "project-retrospective", TemplateKey: "project-retrospective", Name: "项目复盘",
		RequiredSections: append([]string(nil), config.BlockSchema.RequiredSections...),
		QualityGates:     []string{"关联基线", "目标数据", "事实与观点区分", "根因和现象区分", "行动项责任人", "截止时间", "证据覆盖率"},
	}
}

func BuiltinProductionTemplate(code string) (ProductionBuiltinTemplate, bool) {
	catalog, err := NewProductionBuiltinCatalog()
	if err != nil {
		return ProductionBuiltinTemplate{}, false
	}
	return catalog.Lookup(code)
}

func cloneProductionBuiltinTemplate(definition ProductionBuiltinTemplate) ProductionBuiltinTemplate {
	definition.RequiredSections = append([]string(nil), definition.RequiredSections...)
	definition.QualityGates = append([]string(nil), definition.QualityGates...)
	definition.Config.BlockSchema.RequiredSections = append([]string(nil), definition.Config.BlockSchema.RequiredSections...)
	definition.Config.BlockSchema.AllowedBlockTypes = append([]string(nil), definition.Config.BlockSchema.AllowedBlockTypes...)
	definition.Config.SourceRequirements.AllowedSourceKinds = append([]types.ProductionSourceKind(nil), definition.Config.SourceRequirements.AllowedSourceKinds...)
	definition.Config.SkillBindings.Skills = append([]types.ProductionSkillBindingV1(nil), definition.Config.SkillBindings.Skills...)
	definition.Config.WorkflowPlan.Steps = append([]types.ProductionWorkflowStepV1(nil), definition.Config.WorkflowPlan.Steps...)
	definition.Config.QualityRules.Gates = append([]string(nil), definition.Config.QualityRules.Gates...)
	definition.Config.ReviewPolicy.Steps = append([]types.ProductionRole(nil), definition.Config.ReviewPolicy.Steps...)
	definition.Config.Canonical = cloneProductionConfigInput(definition.Config.Canonical)
	return definition
}

func cloneProductionConfigInput(input types.ProductionDocumentTypeConfigInput) types.ProductionDocumentTypeConfigInput {
	return types.ProductionDocumentTypeConfigInput{
		BlockSchema: cloneBuiltinJSON(input.BlockSchema), SourceRequirements: cloneBuiltinJSON(input.SourceRequirements),
		SkillBindings: cloneBuiltinJSON(input.SkillBindings), WorkflowPlan: cloneBuiltinJSON(input.WorkflowPlan),
		QualityRules: cloneBuiltinJSON(input.QualityRules), ReviewPolicy: cloneBuiltinJSON(input.ReviewPolicy),
		PublicationPolicy: cloneBuiltinJSON(input.PublicationPolicy),
	}
}

func cloneBuiltinJSON(value types.JSON) types.JSON {
	return append(types.JSON(nil), value...)
}

func equalBuiltinStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
