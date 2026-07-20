package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

const (
	projectionTenantID    = uint64(7)
	projectionProjectID   = "93000000-0000-4000-8000-000000000001"
	projectionDocumentID  = "93000000-0000-4000-8000-000000000002"
	projectionVersionID   = "93000000-0000-4000-8000-000000000003"
	projectionReviewID    = "93000000-0000-4000-8000-000000000004"
	projectionReleaseID   = "93000000-0000-4000-8000-000000000005"
	projectionTargetID    = "93000000-0000-4000-8000-000000000006"
	projectionKnowledgeID = "93000000-0000-4000-8000-000000000007"
	projectionKBID        = "93000000-0000-4000-8000-000000000008"
)

type projectionBuilderReleaseRepo struct {
	interfaces.ProductionReleaseRepository
	target      *types.ProductionReleaseTarget
	release     *types.ProductionRelease
	events      *[]string
	transitions []types.ProductionReleaseTargetStatus
}

func (r *projectionBuilderReleaseRepo) GetTarget(context.Context, uint64, string) (*types.ProductionReleaseTarget, error) {
	*r.events = append(*r.events, "target.persisted")
	copyTarget := *r.target
	return &copyTarget, nil
}

func (r *projectionBuilderReleaseRepo) GetRelease(context.Context, uint64, string) (*types.ProductionRelease, error) {
	copyRelease := *r.release
	return &copyRelease, nil
}

func (r *projectionBuilderReleaseRepo) TransitionTarget(
	_ context.Context, _ string, _, to types.ProductionReleaseTargetStatus, _ types.JSONMap,
) (bool, error) {
	r.transitions = append(r.transitions, to)
	r.target.Status = to
	return true, nil
}

type projectionBuilderDocumentRepo struct {
	interfaces.ProductionDocumentRepository
	document *types.ProductionDocument
	version  *types.ProductionDocumentVersion
}

func (r *projectionBuilderDocumentRepo) GetDocument(context.Context, uint64, string) (*types.ProductionDocument, error) {
	copyDocument := *r.document
	return &copyDocument, nil
}

func (r *projectionBuilderDocumentRepo) GetVersion(context.Context, uint64, string) (*types.ProductionDocumentVersion, error) {
	copyVersion := *r.version
	copyVersion.Blocks = append([]*types.ProductionDocumentBlock(nil), r.version.Blocks...)
	return &copyVersion, nil
}

type projectionBuilderReviewRepo struct {
	interfaces.ProductionReviewRepository
	review   *types.ProductionReviewRequest
	blocking int64
}

func (r *projectionBuilderReviewRepo) GetReview(context.Context, uint64, string) (*types.ProductionReviewRequest, error) {
	copyReview := *r.review
	copyReview.Steps = append([]*types.ProductionReviewStep(nil), r.review.Steps...)
	return &copyReview, nil
}

func (r *projectionBuilderReviewRepo) CountOpenBlocking(context.Context, uint64, string) (int64, error) {
	return r.blocking, nil
}

type projectionBuilderSourceRepo struct {
	interfaces.ProductionSourceRepository
	evidence []*types.ProductionEvidenceSnapshot
}

func (r *projectionBuilderSourceRepo) ListAcceptedEvidence(context.Context, uint64, string, string) ([]*types.ProductionEvidenceSnapshot, error) {
	return r.evidence, nil
}

type projectionBuilderKBService struct {
	interfaces.KnowledgeBaseService
	kb *types.KnowledgeBase
}

func (s *projectionBuilderKBService) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	copyKB := *s.kb
	return &copyKB, nil
}

type projectionBuilderKnowledgeService struct {
	interfaces.KnowledgeService
	events   *[]string
	existing *types.Knowledge
	created  *types.ProductionProjectionKnowledgePayload
}

func (s *projectionBuilderKnowledgeService) GetKnowledgeByID(context.Context, string) (*types.Knowledge, error) {
	if s.existing == nil {
		return nil, apprepository.ErrKnowledgeNotFound
	}
	copyKnowledge := *s.existing
	return &copyKnowledge, nil
}

func (s *projectionBuilderKnowledgeService) CreateKnowledgeFromProductionProjection(
	_ context.Context, payload *types.ProductionProjectionKnowledgePayload,
) (*types.Knowledge, error) {
	*s.events = append(*s.events, "knowledge.created", "knowledge.enqueued")
	copyPayload := *payload
	s.created = &copyPayload
	knowledge := &types.Knowledge{
		ID: payload.KnowledgeID, TenantID: projectionTenantID,
		KnowledgeBaseID: payload.KnowledgeBaseID, Type: types.KnowledgeTypeManual,
		ParseStatus: types.ParseStatusPending,
	}
	meta := types.NewManualKnowledgeMetadata(payload.Content, types.ManualKnowledgeStatusPublish, 1)
	meta.ProductionProjection = payload.ProductionProjection
	if err := knowledge.SetManualMetadata(meta); err != nil {
		return nil, err
	}
	return knowledge, nil
}

type projectionBuilderAudit struct {
	interfaces.AuditLogService
	entries []*types.AuditLog
}

func (a *projectionBuilderAudit) Log(_ context.Context, entry *types.AuditLog) error {
	copyEntry := *entry
	a.entries = append(a.entries, &copyEntry)
	return nil
}

type projectionBuilderUOW struct{}

func (projectionBuilderUOW) WithinTransaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

type projectionBuilderFixture struct {
	builder   *ProductionProjectionBuilder
	releases  *projectionBuilderReleaseRepo
	knowledge *projectionBuilderKnowledgeService
	audit     *projectionBuilderAudit
	events    *[]string
	version   *types.ProductionDocumentVersion
}

func newProjectionBuilderFixture(t *testing.T) *projectionBuilderFixture {
	t.Helper()
	inline := types.JSON(`{"source":"approved"}`)
	sum := sha256.Sum256(inline)
	evidence := &types.ProductionEvidenceSnapshot{
		ID: "evidence-1", SnapshotType: types.ProductionEvidenceSnapshotJSON,
		InlineContent: inline, ContentDigest: hex.EncodeToString(sum[:]),
	}
	block := &types.ProductionDocumentBlock{
		ID: "block-1", VersionID: projectionVersionID, LogicalBlockID: "baseline",
		BlockType: "paragraph", Position: 0, Content: types.JSON(`"Approved baseline"`),
		Attributes: types.JSON(`{}`), EvidenceRefs: types.JSON(`["evidence-1"]`),
		AIProvenance: types.JSON(`{}`),
	}
	block.ContentDigest = types.ComputeProductionBlockDigest(block)
	version := &types.ProductionDocumentVersion{
		ID: projectionVersionID, DocumentID: projectionDocumentID, TenantID: projectionTenantID,
		ProjectID: projectionProjectID, VersionNumber: 3, SourceSetID: "source-set-1",
		Origin: types.ProductionDocumentOriginHuman, FrozenAt: projectionTimePtr(time.Now().UTC()),
		Blocks: []*types.ProductionDocumentBlock{block},
	}
	version.ContentDigest = types.ComputeProductionVersionDigest(version)
	policy, policyDigest, err := types.CanonicalProductionReviewPolicy(types.JSON(`{"steps":[{"role":"business_reviewer"}]}`))
	require.NoError(t, err)
	review := &types.ProductionReviewRequest{
		ID: projectionReviewID, TenantID: projectionTenantID, ProjectID: projectionProjectID,
		DocumentID: projectionDocumentID, VersionID: projectionVersionID,
		PolicySnapshot: policy, PolicyDigest: policyDigest, Status: types.ProductionReviewApproved,
		Steps: []*types.ProductionReviewStep{{
			ID: "step-1", ReviewRequestID: projectionReviewID, TenantID: projectionTenantID,
			ProjectID: projectionProjectID, DocumentID: projectionDocumentID, VersionID: projectionVersionID,
			Decision: types.ProductionReviewApproved,
		}},
	}
	release := &types.ProductionRelease{
		ID: projectionReleaseID, TenantID: projectionTenantID, ProjectID: projectionProjectID,
		DocumentID: projectionDocumentID, VersionID: projectionVersionID, ReviewRequestID: projectionReviewID,
		Status: types.ProductionReleaseBuilding,
	}
	release.ReleaseDigest = types.ComputeProductionReleaseDigest(release, version, review)
	config, configDigest, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(`{
		"chunking":{"chunk_size":777,"chunk_overlap":77},
		"embedding_model_id":"snapshot-embedding",
		"graph":{"enabled":false,"model_id":"snapshot-graph"}
	}`))
	require.NoError(t, err)
	target := &types.ProductionReleaseTarget{
		ID: projectionTargetID, ReleaseID: projectionReleaseID, TenantID: projectionTenantID,
		ProjectID: projectionProjectID, DocumentID: projectionDocumentID, VersionID: projectionVersionID,
		TargetKnowledgeBaseID: projectionKBID, KnowledgeID: projectionKnowledgeID,
		ReleaseDigest: release.ReleaseDigest, ConfigSnapshot: config, ConfigDigest: configDigest,
		Status: types.ReleaseTargetBuilding,
	}
	events := make([]string, 0)
	releases := &projectionBuilderReleaseRepo{target: target, release: release, events: &events}
	knowledge := &projectionBuilderKnowledgeService{events: &events}
	audit := &projectionBuilderAudit{}
	document := &types.ProductionDocument{
		ID: projectionDocumentID, TenantID: projectionTenantID, ProjectID: projectionProjectID,
		Title: "Software Baseline", LatestApprovedVersionID: projectionStringPtr(projectionVersionID),
		Status: types.ProductionDocumentApproved,
	}
	builder := NewProductionProjectionBuilder(
		releases,
		&projectionBuilderDocumentRepo{document: document, version: version},
		&projectionBuilderReviewRepo{review: review},
		&projectionBuilderSourceRepo{evidence: []*types.ProductionEvidenceSnapshot{evidence}},
		nil,
		&projectionBuilderKBService{kb: &types.KnowledgeBase{
			ID: projectionKBID, TenantID: projectionTenantID, Type: types.KnowledgeBaseTypeDocument,
			EmbeddingModelID: "live-embedding", ChunkingConfig: types.ChunkingConfig{ChunkSize: 100},
		}},
		knowledge, projectionBuilderUOW{}, audit,
	)
	return &projectionBuilderFixture{
		builder: builder, releases: releases, knowledge: knowledge, audit: audit,
		events: &events, version: version,
	}
}

func projectionBuildContext() context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, projectionTenantID)
	return context.WithValue(ctx, types.UserIDContextKey, types.ProductionSystemActorID)
}

func TestProjectionTargetExistsBeforeKnowledgeIsQueued(t *testing.T) {
	fixture := newProjectionBuilderFixture(t)
	_, err := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
	require.NoError(t, err)
	require.Less(t, strings.Index(strings.Join(*fixture.events, ","), "target.persisted"),
		strings.Index(strings.Join(*fixture.events, ","), "knowledge.enqueued"))
	require.Equal(t, projectionKnowledgeID, fixture.knowledge.created.KnowledgeID)
	require.Equal(t, "snapshot-embedding", fixture.knowledge.created.EmbeddingModelID)
	require.Equal(t, "snapshot-graph", fixture.knowledge.created.GraphModelID)
	require.Equal(t, 777, fixture.knowledge.created.ProcessOverrides.ChunkingConfig.ChunkSize)
	require.False(t, *fixture.knowledge.created.ProcessOverrides.GraphEnabled)
}

func TestProjectionRejectsTamperedApprovedVersionAtomicallyBeforeKnowledge(t *testing.T) {
	fixture := newProjectionBuilderFixture(t)
	fixture.version.Blocks[0].Content = types.JSON(`"tampered"`)

	_, err := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
	require.ErrorIs(t, err, types.ErrProductionContentDigestMismatch)
	require.Nil(t, fixture.knowledge.created)
	require.Equal(t, []types.ProductionReleaseTargetStatus{types.ReleaseTargetFailed}, fixture.releases.transitions)
	require.Len(t, fixture.audit.entries, 1)
	require.Equal(t, types.AuditActionProductionContentDigestMismatch, fixture.audit.entries[0].Action)
	require.NotContains(t, string(fixture.audit.entries[0].Details), "tampered")
}

func TestProjectionBuildRejectsKnowledgeOwnedByAnotherTarget(t *testing.T) {
	fixture := newProjectionBuilderFixture(t)
	foreign := &types.Knowledge{
		ID: projectionKnowledgeID, TenantID: projectionTenantID,
		KnowledgeBaseID: projectionKBID, Type: types.KnowledgeTypeManual,
	}
	meta := types.NewManualKnowledgeMetadata("# foreign", types.ManualKnowledgeStatusPublish, 1)
	meta.ProductionProjection = &types.ProductionProjectionMetadata{
		DocumentID: projectionDocumentID, VersionID: projectionVersionID,
		ReleaseTargetID: "93000000-0000-4000-8000-000000000099",
	}
	require.NoError(t, foreign.SetManualMetadata(meta))
	fixture.knowledge.existing = foreign

	_, err := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
	require.ErrorIs(t, err, types.ErrProductionProjectionConflict)
	require.Nil(t, fixture.knowledge.created)
}

func TestProjectionBuildRetryReturnsExistingOwnedKnowledgeWithoutDuplicateEnqueue(t *testing.T) {
	fixture := newProjectionBuilderFixture(t)
	first, err := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
	require.NoError(t, err)
	fixture.knowledge.existing = first
	fixture.knowledge.created = nil

	second, err := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	require.Nil(t, fixture.knowledge.created)
}

func TestProjectionBuildRejectsReleaseReviewScopeOrDigestDrift(t *testing.T) {
	for name, mutate := range map[string]func(*projectionBuilderFixture){
		"review_not_approved":  func(f *projectionBuilderFixture) { f.releases.release.ReviewRequestID = "wrong-review" },
		"release_digest":       func(f *projectionBuilderFixture) { f.releases.release.ReleaseDigest = strings.Repeat("f", 64) },
		"target_config_digest": func(f *projectionBuilderFixture) { f.releases.target.ConfigDigest = strings.Repeat("f", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newProjectionBuilderFixture(t)
			mutate(fixture)
			_, err := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
			require.Error(t, err)
			require.Nil(t, fixture.knowledge.created)
		})
	}
}

func projectionTimePtr(value time.Time) *time.Time { return &value }
func projectionStringPtr(value string) *string     { return &value }
