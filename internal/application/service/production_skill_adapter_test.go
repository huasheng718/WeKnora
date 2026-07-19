package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/skills"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
	run      *types.ProductionRun
	item     *types.ProductionSourceItem
	set      *types.ProductionSourceSet
	evidence map[string]*types.ProductionEvidenceSnapshot
}

func (f *adapterScopeFixture) Get(_ context.Context, tenantID uint64, runID string) (*types.ProductionRun, error) {
	if f.run == nil || f.run.TenantID != tenantID || f.run.ID != runID {
		return nil, errProductionToolScope
	}
	copy := *f.run
	return &copy, nil
}

func (f *adapterScopeFixture) GetEvidence(_ context.Context, tenantID uint64, evidenceID string) (*types.ProductionEvidenceSnapshot, *types.ProductionSourceItem, *types.ProductionSourceSet, error) {
	evidence := f.evidence[evidenceID]
	if evidence == nil || f.set == nil || f.set.TenantID != tenantID {
		return nil, nil, nil, gorm.ErrRecordNotFound
	}
	evidenceCopy, itemCopy, setCopy := *evidence, *f.item, *f.set
	return &evidenceCopy, &itemCopy, &setCopy, nil
}

type fakeProductionEvidenceService struct {
	mu          sync.Mutex
	scope       *adapterScopeFixture
	attachCalls int
}

func (f *fakeProductionEvidenceService) AttachEvidence(_ context.Context, itemID string, input interfaces.CreateEvidenceSnapshotInput) (*types.ProductionEvidenceSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attachCalls++
	canonical, err := types.CanonicalProductionJSON(input.InlineContent)
	if err != nil {
		return nil, err
	}
	metadata, err := types.CanonicalProductionJSON(input.RedactionMetadata)
	if err != nil {
		return nil, err
	}
	candidate := &types.ProductionEvidenceSnapshot{
		ID: input.EvidenceID, SourceItemID: itemID, SnapshotType: input.SnapshotType,
		InlineContent: canonical, ContentDigest: adapterDigest(canonical), RedactionMetadata: metadata,
		CapturedByRunID: input.CapturedByRunID,
	}
	if existing := f.scope.evidence[input.EvidenceID]; existing != nil {
		if existing.SourceItemID != candidate.SourceItemID || existing.SnapshotType != candidate.SnapshotType ||
			!bytes.Equal(existing.InlineContent, candidate.InlineContent) || !bytes.Equal(existing.RedactionMetadata, candidate.RedactionMetadata) {
			return nil, types.ErrProductionEvidenceConflict
		}
		copy := *existing
		return &copy, nil
	}
	f.scope.evidence[input.EvidenceID] = candidate
	copy := *candidate
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
	listCalls int
	getCalls  int
}

func (f *fakeProductionSkillCatalog) ListPreloadedSkills(context.Context) ([]*skills.SkillMetadata, error) {
	f.listCalls++
	if !f.preloaded || f.skill == nil {
		return nil, nil
	}
	return []*skills.SkillMetadata{{Name: f.skill.Name, Description: f.skill.Description}}, nil
}

func (f *fakeProductionSkillCatalog) GetSkillByName(context.Context, string) (*skills.Skill, error) {
	f.getCalls++
	if f.skill == nil {
		return nil, errProductionSkillNotPreloaded
	}
	copy := *f.skill
	return &copy, nil
}

type fakeProductionSkillRuntime struct {
	skill *skills.Skill
	calls int
}

func (f *fakeProductionSkillRuntime) LoadSkill(context.Context, string) (*skills.Skill, error) {
	f.calls++
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
	snapshot, err := json.Marshal(map[string]any{"skill_bindings": map[string]any{
		"version": 1, "skills": []any{map[string]any{"name": skill.Name, "digest": digest}},
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
		set:      &types.ProductionSourceSet{ID: adapterSourceSetID, TenantID: 7, ProjectID: adapterProjectID},
		evidence: make(map[string]*types.ProductionEvidenceSnapshot),
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
	runtime := &fakeProductionSkillRuntime{skill: skill}
	catalog := &fakeProductionSkillCatalog{preloaded: true, skill: skill}
	adapter := NewProductionSkillAdapter(
		runtime, catalog,
		scope,
		scope,
		&fakeProductionEvidenceService{scope: scope},
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
	scope.run.DocumentTypeSnapshot = types.JSON(`{"skill_bindings":{"version":1,"skills":[]}}`)
	_, err = adapter.Execute(context.Background(), call)
	require.ErrorIs(t, err, errProductionSkillUnbound)

	scope.run.DocumentTypeSnapshot = types.JSON(`{"skill_bindings":{"version":1,"skills":[{"name":"baseline","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}}`)
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
	planned := *call
	planned.Status = types.ProductionToolCallPlanned
	firstPlan, err := adapter.Plan(context.Background(), &planned)
	require.NoError(t, err)
	secondPlan, err := adapter.Plan(context.Background(), &planned)
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

func TestProductionSkillAdapterPlanDoesNotTouchSkillRuntimeOrCatalog(t *testing.T) {
	adapter, call, _ := skillAdapterFixture(t, "---\nname: baseline\ndescription: x\n---\nRules")
	runtime := adapter.runtime.(*fakeProductionSkillRuntime)
	catalog := adapter.catalog.(*fakeProductionSkillCatalog)
	call.Status = types.ProductionToolCallPlanned

	_, err := adapter.Plan(context.Background(), call)
	require.NoError(t, err)
	require.Zero(t, runtime.calls)
	require.Zero(t, catalog.listCalls)
	require.Zero(t, catalog.getCalls)
}

func TestProductionSkillAdapterRetryReturnsPersistedEvidenceWithoutRuntimeExecution(t *testing.T) {
	adapter, call, _ := skillAdapterFixture(t, "---\nname: baseline\ndescription: x\n---\nRules")
	runtime := adapter.runtime.(*fakeProductionSkillRuntime)
	first, err := adapter.Execute(context.Background(), call)
	require.NoError(t, err)
	require.Equal(t, 1, runtime.calls)
	runtime.skill = parsedAdapterSkill(t, "---\nname: baseline\ndescription: x\n---\nChanged")

	second, err := adapter.Execute(context.Background(), call)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, runtime.calls)
}

func TestProductionSkillAdapterRejectsAmbiguousBindingsAndInvalidLifecycleBeforeRuntime(t *testing.T) {
	adapter, call, scope := skillAdapterFixture(t, "---\nname: baseline\ndescription: x\n---\nRules")
	runtime := adapter.runtime.(*fakeProductionSkillRuntime)
	invalid := []types.JSON{
		types.JSON(`{"skill_bindings":[{"name":"baseline","instruction_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`),
		types.JSON(`{"skill_bindings":{"version":1,"skills":[{"name":"baseline","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","extra":true}]}}`),
		types.JSON(`{"skill_bindings":{"version":1,"skills":[{"name":"baseline","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"name":"baseline","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}}`),
	}
	for _, snapshot := range invalid {
		scope.run.DocumentTypeSnapshot = snapshot
		planned := *call
		planned.Status = types.ProductionToolCallPlanned
		_, err := adapter.Plan(context.Background(), &planned)
		require.Error(t, err)
	}
	call.Status = types.ProductionToolCallCompleted
	_, err := adapter.Execute(context.Background(), call)
	require.ErrorIs(t, err, errProductionToolCallInvalid)
	require.Zero(t, runtime.calls)
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
