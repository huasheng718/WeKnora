package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func validProductionDocumentTypeConfigInput() ProductionDocumentTypeConfigInput {
	return ProductionDocumentTypeConfigInput{
		BlockSchema: JSON(`{
			"allowed_block_types":["heading","paragraph","code","callout","list","table","image"],
			"required_sections":["目的与范围","证据清单"],"version":1}`),
		SourceRequirements: JSON(`{
			"allow_unsupported_facts":false,
			"allowed_source_kinds":["upload","datasource","mcp","skill","manual"],
			"min_accepted_evidence":1,"require_evidence_section":true,"version":1}`),
		SkillBindings: JSON(`{"skills":[],"version":1}`),
		WorkflowPlan:  JSON(`{"steps":[],"version":1}`),
		QualityRules: JSON(`{
			"block_needs_confirmation":true,
			"gates":["section_completeness","fact_evidence","no_unconfirmed","sop_exception_path"],
			"require_evidence_for_facts":true,"version":1}`),
		ReviewPolicy: JSON(`{"steps":["business_reviewer"]}`),
		PublicationPolicy: JSON(`{
			"chunking":"inherit_target","knowledge_graph":"inherit_target",
			"require_approved_review":true,"target_type":"knowledge_base","version":1}`),
	}
}

func TestCanonicalProductionDocumentTypeConfigValidatesAndCanonicalizesV1(t *testing.T) {
	input := validProductionDocumentTypeConfigInput()

	got, err := CanonicalProductionDocumentTypeConfig(input)
	require.NoError(t, err)
	require.Equal(t, []string{"目的与范围", "证据清单"}, got.BlockSchema.RequiredSections)
	require.Equal(t, []ProductionRole{ProductionRoleBusinessReviewer}, got.ReviewPolicy.Steps)
	require.Equal(t, JSON(`{"allowed_block_types":["heading","paragraph","code","callout","list","table","image"],"required_sections":["目的与范围","证据清单"],"version":1}`), got.Canonical.BlockSchema)
	require.Equal(t, JSON(`{"steps":[],"version":1}`), got.Canonical.WorkflowPlan)

	reordered := validProductionDocumentTypeConfigInput()
	reordered.BlockSchema = JSON(`{"version":1,"required_sections":["目的与范围","证据清单"],"allowed_block_types":["heading","paragraph","code","callout","list","table","image"]}`)
	reorderedConfig, err := CanonicalProductionDocumentTypeConfig(reordered)
	require.NoError(t, err)
	require.Equal(t, got.Canonical, reorderedConfig.Canonical)
}

func TestNormalizeProductionDocumentTypeConfigAdaptsOnlyExactKnownLegacyShapes(t *testing.T) {
	legacy := ProductionDocumentTypeConfigInput{
		BlockSchema: JSON(`{}`), SourceRequirements: JSON(`{}`), SkillBindings: JSON(`{}`),
		WorkflowPlan: JSON(`{"steps":[],"version":1}`), QualityRules: JSON(`{}`),
		ReviewPolicy: JSON(`{}`), PublicationPolicy: JSON(`{}`),
	}
	for _, code := range []string{"software-development-baseline", "project-retrospective"} {
		config, adapted, err := NormalizeProductionDocumentTypeConfig(code, legacy)
		require.NoError(t, err)
		require.True(t, adapted)
		require.NotEmpty(t, config.BlockSchema.RequiredSections)
		require.Equal(t, JSON(`{"steps":[],"version":1}`), config.Canonical.WorkflowPlan)
	}

	for name, mutate := range map[string]func(*ProductionDocumentTypeConfigInput, *string){
		"unknown code": func(_ *ProductionDocumentTypeConfigInput, code *string) { *code = "unknown" },
		"reordered workflow": func(input *ProductionDocumentTypeConfigInput, _ *string) {
			input.WorkflowPlan = JSON(`{"version":1,"steps":[]}`)
		},
		"workflow step": func(input *ProductionDocumentTypeConfigInput, _ *string) {
			input.WorkflowPlan = JSON(`{"steps":[{"provider_type":"mcp","provider_id":"33333333-3333-4333-8333-333333333333","tool_name":"lookup","request":{}}],"version":1}`)
		},
		"populated governance": func(input *ProductionDocumentTypeConfigInput, _ *string) {
			input.SkillBindings = JSON(`{"skills":[],"version":1}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := legacy
			code := "software-development-baseline"
			mutate(&input, &code)
			_, adapted, err := NormalizeProductionDocumentTypeConfig(code, input)
			require.Error(t, err)
			require.False(t, adapted)
		})
	}
}

func TestCanonicalProductionDocumentTypeConfigRejectsUnknownFieldsAndWrongVersions(t *testing.T) {
	for name, mutate := range map[string]func(*ProductionDocumentTypeConfigInput){
		"unknown block field": func(input *ProductionDocumentTypeConfigInput) {
			input.BlockSchema = JSON(`{"version":1,"required_sections":["证据清单"],"allowed_block_types":["paragraph"],"future":true}`)
		},
		"block version": func(input *ProductionDocumentTypeConfigInput) {
			input.BlockSchema = JSON(`{"version":2,"required_sections":["证据清单"],"allowed_block_types":["paragraph"]}`)
		},
		"source version": func(input *ProductionDocumentTypeConfigInput) {
			input.SourceRequirements = JSON(`{"version":2,"min_accepted_evidence":1,"allowed_source_kinds":["upload"],"require_evidence_section":true,"allow_unsupported_facts":false}`)
		},
		"skill version": func(input *ProductionDocumentTypeConfigInput) {
			input.SkillBindings = JSON(`{"version":2,"skills":[]}`)
		},
		"workflow version": func(input *ProductionDocumentTypeConfigInput) {
			input.WorkflowPlan = JSON(`{"version":2,"steps":[]}`)
		},
		"quality version": func(input *ProductionDocumentTypeConfigInput) {
			input.QualityRules = JSON(`{"version":2,"require_evidence_for_facts":true,"block_needs_confirmation":true,"gates":["section_completeness"]}`)
		},
		"publication version": func(input *ProductionDocumentTypeConfigInput) {
			input.PublicationPolicy = JSON(`{"version":2,"target_type":"knowledge_base","chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := validProductionDocumentTypeConfigInput()
			mutate(&input)
			_, err := CanonicalProductionDocumentTypeConfig(input)
			require.Error(t, err)
		})
	}
}

func TestCanonicalProductionDocumentTypeConfigRejectsMissingRequiredFields(t *testing.T) {
	for name, mutate := range map[string]func(*ProductionDocumentTypeConfigInput){
		"source boolean": func(input *ProductionDocumentTypeConfigInput) {
			input.SourceRequirements = JSON(`{"version":1,"min_accepted_evidence":1,"allowed_source_kinds":["upload"],"require_evidence_section":true}`)
		},
		"quality boolean": func(input *ProductionDocumentTypeConfigInput) {
			input.QualityRules = JSON(`{"version":1,"require_evidence_for_facts":true,"gates":["section_completeness"]}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := validProductionDocumentTypeConfigInput()
			mutate(&input)
			_, err := CanonicalProductionDocumentTypeConfig(input)
			require.Error(t, err)
		})
	}
}

func TestCanonicalProductionDocumentTypeConfigRejectsDuplicateOrUnknownValues(t *testing.T) {
	for name, mutate := range map[string]func(*ProductionDocumentTypeConfigInput){
		"duplicate sections": func(input *ProductionDocumentTypeConfigInput) {
			input.BlockSchema = JSON(`{"version":1,"required_sections":["证据清单","证据清单"],"allowed_block_types":["paragraph"]}`)
		},
		"unknown block type": func(input *ProductionDocumentTypeConfigInput) {
			input.BlockSchema = JSON(`{"version":1,"required_sections":["证据清单"],"allowed_block_types":["fact"]}`)
		},
		"unknown source kind": func(input *ProductionDocumentTypeConfigInput) {
			input.SourceRequirements = JSON(`{"version":1,"min_accepted_evidence":1,"allowed_source_kinds":["web"],"require_evidence_section":true,"allow_unsupported_facts":false}`)
		},
		"duplicate gates": func(input *ProductionDocumentTypeConfigInput) {
			input.QualityRules = JSON(`{"version":1,"require_evidence_for_facts":true,"block_needs_confirmation":true,"gates":["fact_evidence","fact_evidence"]}`)
		},
		"unknown reviewer": func(input *ProductionDocumentTypeConfigInput) {
			input.ReviewPolicy = JSON(`{"steps":["publisher"]}`)
		},
		"duplicate reviewers": func(input *ProductionDocumentTypeConfigInput) {
			input.ReviewPolicy = JSON(`{"steps":["business_reviewer","business_reviewer"]}`)
		},
		"empty review steps": func(input *ProductionDocumentTypeConfigInput) {
			input.ReviewPolicy = JSON(`{"steps":[]}`)
		},
		"publication target": func(input *ProductionDocumentTypeConfigInput) {
			input.PublicationPolicy = JSON(`{"version":1,"target_type":"wiki","chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true}`)
		},
		"publication chunking": func(input *ProductionDocumentTypeConfigInput) {
			input.PublicationPolicy = JSON(`{"version":1,"target_type":"knowledge_base","chunking":"fixed","knowledge_graph":"inherit_target","require_approved_review":true}`)
		},
		"publication graph": func(input *ProductionDocumentTypeConfigInput) {
			input.PublicationPolicy = JSON(`{"version":1,"target_type":"knowledge_base","chunking":"inherit_target","knowledge_graph":"disabled","require_approved_review":true}`)
		},
		"publication without review": func(input *ProductionDocumentTypeConfigInput) {
			input.PublicationPolicy = JSON(`{"version":1,"target_type":"knowledge_base","chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":false}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := validProductionDocumentTypeConfigInput()
			mutate(&input)
			_, err := CanonicalProductionDocumentTypeConfig(input)
			require.Error(t, err)
		})
	}
}

func TestCanonicalProductionDocumentTypeConfigEnforcesResourceLimits(t *testing.T) {
	input := validProductionDocumentTypeConfigInput()
	input.BlockSchema = JSON(`{"version":1,"required_sections":["` + strings.Repeat("x", ProductionDocumentTypeConfigMaxBytes) + `"],"allowed_block_types":["paragraph"]}`)
	_, err := CanonicalProductionDocumentTypeConfig(input)
	require.ErrorIs(t, err, ErrProductionJSONResourceLimit)

	input = validProductionDocumentTypeConfigInput()
	input.BlockSchema = JSON(strings.Repeat("[", ProductionDocumentTypeConfigMaxDepth+1) + strings.Repeat("]", ProductionDocumentTypeConfigMaxDepth+1))
	_, err = CanonicalProductionDocumentTypeConfig(input)
	require.ErrorIs(t, err, ErrProductionJSONResourceLimit)
}
