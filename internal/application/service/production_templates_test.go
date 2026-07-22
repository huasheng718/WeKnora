package service

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestProductionBuiltinCatalogContainsExactGovernedDefinitions(t *testing.T) {
	expectedSections := map[string][]string{
		"sop":                   {"目的与范围", "角色职责", "前置条件", "操作步骤", "异常处理", "风险控制", "验证记录", "证据清单"},
		"policy_process":        {"制定依据", "适用范围", "术语定义", "职责权限", "制度要求", "业务流程", "审批控制", "例外处理", "监督机制", "证据清单"},
		"product_service_guide": {"产品定位", "核心能力", "适用场景", "使用前提", "配置与使用", "限制条件", "服务标准", "常见故障", "升级路径", "证据清单"},
		"faq":                   {"问题分类", "适用条件", "标准问题与回答", "例外情况", "处理建议", "升级路径", "来源与生效日期"},
		"incident_playbook":     {"现象与影响", "事件等级", "止损措施", "诊断过程", "恢复步骤", "结果验证", "沟通升级", "根因分析", "改进行动", "证据清单"},
	}
	expectedGates := map[string][]string{
		"sop":                   {"section_completeness", "fact_evidence", "no_unconfirmed", "sop_exception_path"},
		"policy_process":        {"section_completeness", "fact_evidence", "no_unconfirmed", "policy_approval_control"},
		"product_service_guide": {"section_completeness", "fact_evidence", "no_unconfirmed", "product_scope_boundary"},
		"faq":                   {"section_completeness", "fact_evidence", "no_unconfirmed", "faq_effective_date"},
		"incident_playbook":     {"section_completeness", "fact_evidence", "no_unconfirmed", "incident_timeline"},
	}
	expectedReviews := map[string][]types.ProductionRole{
		"sop":                   {types.ProductionRoleBusinessReviewer},
		"policy_process":        {types.ProductionRoleBusinessReviewer, types.ProductionRoleComplianceReviewer},
		"product_service_guide": {types.ProductionRoleBusinessReviewer},
		"faq":                   {types.ProductionRoleBusinessReviewer},
		"incident_playbook":     {types.ProductionRoleEngineeringReviewer, types.ProductionRoleBusinessReviewer},
	}

	catalog, err := NewProductionBuiltinCatalog()
	require.NoError(t, err)
	definitions := catalog.Definitions()
	require.Len(t, definitions, 5)
	for _, definition := range definitions {
		require.Equal(t, definition.Code, definition.TemplateKey)
		require.Equal(t, expectedSections[definition.Code], definition.RequiredSections)
		require.Equal(t, expectedGates[definition.Code], definition.QualityGates)
		require.Equal(t, expectedReviews[definition.Code], definition.Config.ReviewPolicy.Steps)
		require.Equal(t, types.JSON(`{"skills":[],"version":1}`), definition.Config.Canonical.SkillBindings)
		require.Equal(t, types.JSON(`{"steps":[],"version":1}`), definition.Config.Canonical.WorkflowPlan)
		require.Equal(t, []string{"heading", "paragraph", "code", "callout", "list", "table", "image"}, definition.Config.BlockSchema.AllowedBlockTypes)
		require.Equal(t, "knowledge_base", definition.Config.PublicationPolicy.TargetType)
		require.Equal(t, "inherit_target", definition.Config.PublicationPolicy.Chunking)
		require.Equal(t, "inherit_target", definition.Config.PublicationPolicy.KnowledgeGraph)
	}

	definitions[0].RequiredSections[0] = "mutated"
	fresh := catalog.Definitions()
	require.NotEqual(t, "mutated", fresh[0].RequiredSections[0])
}

func TestProductionBuiltinCatalogRowsAreCanonicalAndTenantScoped(t *testing.T) {
	catalog, err := NewProductionBuiltinCatalog()
	require.NoError(t, err)
	rows := catalog.Rows(17, "seed-actor")
	require.Len(t, rows, 5)
	for _, row := range rows {
		require.Equal(t, uint64(17), row.TenantID)
		require.Equal(t, 1, row.SchemaVersion)
		require.Equal(t, types.ProductionDocumentTypeActive, row.Status)
		require.Equal(t, "seed-actor", row.CreatedBy)
		for _, raw := range []types.JSON{row.BlockSchema, row.SourceRequirements, row.SkillBindings, row.WorkflowPlan, row.QualityRules, row.ReviewPolicy, row.PublicationPolicy} {
			require.True(t, json.Valid(raw))
			canonical, canonicalErr := types.CanonicalProductionJSON(raw)
			require.NoError(t, canonicalErr)
			require.Equal(t, canonical, raw)
		}
	}
}

func TestProductionBuiltinCatalogLookupIncludesOnlyExplicitLegacyAdapters(t *testing.T) {
	catalog, err := NewProductionBuiltinCatalog()
	require.NoError(t, err)
	for _, code := range []string{"sop", "policy_process", "product_service_guide", "faq", "incident_playbook", "software-development-baseline", "project-retrospective"} {
		definition, ok := catalog.Lookup(code)
		require.True(t, ok, code)
		require.Equal(t, code, definition.Code)
	}
	_, ok := catalog.Lookup("unknown")
	require.False(t, ok)
}

func TestProductionBuiltinCatalogRejectsDuplicateCodeAndTemplateKey(t *testing.T) {
	valid := productionBuiltinDefinitions()
	byCode := append([]ProductionBuiltinTemplate(nil), valid...)
	byCode[1].Code = byCode[0].Code
	_, err := newProductionBuiltinCatalog(byCode)
	require.Error(t, err)

	byKey := append([]ProductionBuiltinTemplate(nil), valid...)
	byKey[1].TemplateKey = byKey[0].TemplateKey
	_, err = newProductionBuiltinCatalog(byKey)
	require.Error(t, err)

	wrongCode := append([]ProductionBuiltinTemplate(nil), valid...)
	wrongCode[0].Code = "replacement"
	wrongCode[0].TemplateKey = "replacement"
	_, err = newProductionBuiltinCatalog(wrongCode)
	require.Error(t, err)
}

func TestProductionSoftwareBaselineTemplateMatchesGovernedDefinition(t *testing.T) {
	got := BuiltinSoftwareDevelopmentBaseline()

	require.Equal(t, "software-development-baseline", got.Code)
	require.Equal(t, "软件研发基线", got.Name)
	require.Equal(t, []string{
		"基线范围与目标",
		"需求基线",
		"产品与交互设计基线",
		"技术方案与架构基线",
		"代码仓库、分支与提交基线",
		"依赖和运行环境基线",
		"测试、质量和安全基线",
		"发布、部署和回滚基线",
		"已知风险、例外和遗留项",
		"证据清单",
	}, got.RequiredSections)
	require.Equal(t, []string{
		"必需章节", "基线时间", "仓库和提交引用", "测试结果", "发布版本", "风险责任人", "证据覆盖率",
	}, got.QualityGates)
}

func TestProductionProjectRetrospectiveTemplateMatchesGovernedDefinition(t *testing.T) {
	got := BuiltinProjectRetrospective()

	require.Equal(t, "project-retrospective", got.Code)
	require.Equal(t, "项目复盘", got.Name)
	require.Equal(t, []string{
		"项目背景和目标",
		"关联研发基线",
		"计划与实际结果",
		"需求和范围变化",
		"质量、交付和运营数据",
		"事故、偏差和影响",
		"根因分析",
		"有效实践和经验",
		"改进行动项、负责人和截止时间",
		"证据清单",
	}, got.RequiredSections)
	require.Equal(t, []string{
		"关联基线", "目标数据", "事实与观点区分", "根因和现象区分", "行动项责任人", "截止时间", "证据覆盖率",
	}, got.QualityGates)
}

func TestProductionBuiltinTemplateLookupRejectsUnknownCode(t *testing.T) {
	got, ok := BuiltinProductionTemplate("unknown")
	require.False(t, ok)
	require.Empty(t, got.Code)
}
