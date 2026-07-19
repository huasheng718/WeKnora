package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/skills"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const (
	adapterRunID        = "10000000-0000-4000-8000-000000000001"
	adapterCallID       = "10000000-0000-4000-8000-000000000002"
	adapterProjectID    = "10000000-0000-4000-8000-000000000003"
	adapterDocumentID   = "10000000-0000-4000-8000-000000000004"
	adapterSourceSetID  = "10000000-0000-4000-8000-000000000005"
	adapterSourceItemID = "10000000-0000-4000-8000-000000000006"
)

type adapterScopeFixture struct {
	run  *types.ProductionRun
	item *types.ProductionSourceItem
	set  *types.ProductionSourceSet
}

func (f *adapterScopeFixture) Get(_ context.Context, tenantID uint64, runID string) (*types.ProductionRun, error) {
	if f.run == nil || f.run.TenantID != tenantID || f.run.ID != runID {
		return nil, errProductionToolScope
	}
	copy := *f.run
	return &copy, nil
}

func (f *adapterScopeFixture) GetItem(_ context.Context, tenantID uint64, itemID string) (*types.ProductionSourceItem, *types.ProductionSourceSet, error) {
	if f.item == nil || f.set == nil || f.set.TenantID != tenantID || f.item.ID != itemID {
		return nil, nil, errProductionToolScope
	}
	itemCopy, setCopy := *f.item, *f.set
	return &itemCopy, &setCopy, nil
}

type fakeProductionSkillCatalog struct {
	preloaded bool
	skill     *skills.Skill
}

func (f *fakeProductionSkillCatalog) ListPreloadedSkills(context.Context) ([]*skills.SkillMetadata, error) {
	if !f.preloaded || f.skill == nil {
		return nil, nil
	}
	return []*skills.SkillMetadata{{Name: f.skill.Name, Description: f.skill.Description}}, nil
}

func (f *fakeProductionSkillCatalog) GetSkillByName(context.Context, string) (*skills.Skill, error) {
	if f.skill == nil {
		return nil, errProductionSkillNotPreloaded
	}
	copy := *f.skill
	return &copy, nil
}

type fakeProductionSkillRuntime struct{ skill *skills.Skill }

func (f *fakeProductionSkillRuntime) LoadSkill(context.Context, string) (*skills.Skill, error) {
	if f.skill == nil {
		return nil, errProductionSkillNotPreloaded
	}
	copy := *f.skill
	return &copy, nil
}

func parsedAdapterSkill(t *testing.T, document string) *skills.Skill {
	t.Helper()
	skill, err := skills.ParseSkillFile(document)
	require.NoError(t, err)
	return skill
}

func adapterDigest(raw types.JSON) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func skillAdapterFixture(t *testing.T, document string) (*ProductionSkillAdapter, *types.ProductionToolCall, *adapterScopeFixture) {
	t.Helper()
	skill := parsedAdapterSkill(t, document)
	content, digest, err := canonicalProductionSkill(skill)
	require.NoError(t, err)
	require.Equal(t, adapterDigest(content), digest)
	snapshot, err := json.Marshal(map[string]any{"skill_bindings": []any{
		map[string]any{"name": skill.Name, "instruction_digest": digest},
	}})
	require.NoError(t, err)
	scope := &adapterScopeFixture{
		run: &types.ProductionRun{
			ID: adapterRunID, TenantID: 7, ProjectID: adapterProjectID, DocumentID: adapterDocumentID,
			SourceSetID: adapterSourceSetID, Attempt: 2, CurrentStep: 3, DocumentTypeSnapshot: snapshot,
		},
		item: &types.ProductionSourceItem{
			ID: adapterSourceItemID, SourceSetID: adapterSourceSetID, SourceKind: types.ProductionSourceKindSkill,
			ExternalID: skill.Name,
		},
		set: &types.ProductionSourceSet{ID: adapterSourceSetID, TenantID: 7, ProjectID: adapterProjectID},
	}
	request := types.JSON(`{"source_item_id":"` + adapterSourceItemID + `"}`)
	request, err = types.CanonicalProductionJSON(request)
	require.NoError(t, err)
	call := &types.ProductionToolCall{
		ID: adapterCallID, RunID: adapterRunID, TenantID: 7, ProjectID: adapterProjectID,
		DocumentID: adapterDocumentID, SourceSetID: adapterSourceSetID, Attempt: 2, CurrentStep: 3,
		ProviderType: types.ProductionToolProviderSkill, ProviderID: skill.Name, ToolName: productionSkillToolName,
		RequestSnapshot: request, RequestDigest: adapterDigest(request), Status: types.ProductionToolCallExecuting,
	}
	adapter := NewProductionSkillAdapter(
		&fakeProductionSkillRuntime{skill: skill},
		&fakeProductionSkillCatalog{preloaded: true, skill: skill},
		scope,
		scope,
	)
	return adapter, call, scope
}

func TestProductionSkillAdapterPinsCanonicalInstructionDigest(t *testing.T) {
	adapter, call, _ := skillAdapterFixture(t, "---\ndescription: x\nname: baseline\n---\nRules\n")

	result, err := adapter.Execute(context.Background(), call)
	require.NoError(t, err)
	require.NotEmpty(t, result.ProviderDigest)
	require.Equal(t, result.ProviderDigest, adapterDigest(result.Evidence.InlineContent))
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(result.Evidence.RedactionMetadata, &metadata))
	require.Equal(t, result.ProviderDigest, metadata["skill_digest"])
	require.Equal(t, result.ProviderDigest, metadata["provider_digest"])
}

func TestProductionSkillAdapterCanonicalizesFrontmatterForDigest(t *testing.T) {
	first := parsedAdapterSkill(t, "---\nname: baseline\ndescription: x\n---\nRules\n")
	second := parsedAdapterSkill(t, "---\ndescription: x\nname: baseline\n---\n\nRules\n")
	firstContent, firstDigest, err := canonicalProductionSkill(first)
	require.NoError(t, err)
	secondContent, secondDigest, err := canonicalProductionSkill(second)
	require.NoError(t, err)
	require.Equal(t, firstContent, secondContent)
	require.Equal(t, firstDigest, secondDigest)
}

func TestProductionSkillAdapterRejectsNonPreloadedUnboundAndDigestMismatch(t *testing.T) {
	adapter, call, scope := skillAdapterFixture(t, "---\nname: baseline\ndescription: x\n---\nRules")

	adapter.catalog = &fakeProductionSkillCatalog{skill: parsedAdapterSkill(t, "---\nname: baseline\ndescription: x\n---\nRules")}
	_, err := adapter.Execute(context.Background(), call)
	require.ErrorIs(t, err, errProductionSkillNotPreloaded)

	adapter.catalog = &fakeProductionSkillCatalog{preloaded: true, skill: parsedAdapterSkill(t, "---\nname: baseline\ndescription: x\n---\nRules")}
	scope.run.DocumentTypeSnapshot = types.JSON(`{"skill_bindings":[]}`)
	_, err = adapter.Execute(context.Background(), call)
	require.ErrorIs(t, err, errProductionSkillUnbound)

	scope.run.DocumentTypeSnapshot = types.JSON(`{"skill_bindings":[{"name":"baseline","instruction_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`)
	_, err = adapter.Execute(context.Background(), call)
	require.ErrorIs(t, err, errProductionSkillDigestMismatch)
}

func TestProductionSkillAdapterRejectsLiveSkillMutation(t *testing.T) {
	adapter, call, _ := skillAdapterFixture(t, "---\nname: baseline\ndescription: x\n---\nRules")
	adapter.runtime = &fakeProductionSkillRuntime{skill: parsedAdapterSkill(t, "---\nname: baseline\ndescription: x\n---\nChanged rules")}

	_, err := adapter.Execute(context.Background(), call)
	require.ErrorIs(t, err, errProductionSkillDigestMismatch)
}

func TestProductionSkillAdapterIsDeterministicAndRetryIdempotent(t *testing.T) {
	adapter, call, _ := skillAdapterFixture(t, "---\nname: baseline\ndescription: x\n---\nRules")
	firstPlan, err := adapter.Plan(context.Background(), call)
	require.NoError(t, err)
	secondPlan, err := adapter.Plan(context.Background(), call)
	require.NoError(t, err)
	require.Equal(t, firstPlan, secondPlan)

	first, err := adapter.Execute(context.Background(), call)
	require.NoError(t, err)
	second, err := adapter.Execute(context.Background(), call)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, firstPlan.Digest, first.PlanDigest)
	require.Equal(t, adapterCallID, first.ToolCallID)
}

func TestProductionSkillAdapterRejectsNilMalformedAndOutOfScopeCalls(t *testing.T) {
	adapter, call, scope := skillAdapterFixture(t, "---\nname: baseline\ndescription: x\n---\nRules")
	_, err := adapter.Execute(context.Background(), nil)
	require.ErrorIs(t, err, errProductionToolCallInvalid)

	malformed := *call
	malformed.ID = "NOT-A-UUID"
	_, err = adapter.Execute(context.Background(), &malformed)
	require.ErrorIs(t, err, errProductionToolCallInvalid)

	scope.run.ProjectID = "10000000-0000-4000-8000-000000000099"
	_, err = adapter.Execute(context.Background(), call)
	require.ErrorIs(t, err, errProductionToolScope)
}
