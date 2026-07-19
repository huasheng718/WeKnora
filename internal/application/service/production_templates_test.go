package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
