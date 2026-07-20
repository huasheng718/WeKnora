package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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
	document  *types.ProductionDocument
	review    *types.ProductionReviewRequest
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
	policy, policyDigest, err := types.CanonicalProductionReviewPolicy(types.JSON(`{"steps":["business_reviewer"]}`))
	require.NoError(t, err)
	review := &types.ProductionReviewRequest{
		ID: projectionReviewID, TenantID: projectionTenantID, ProjectID: projectionProjectID,
		DocumentID: projectionDocumentID, VersionID: projectionVersionID,
		PolicySnapshot: policy, PolicyDigest: policyDigest, Status: types.ProductionReviewApproved,
		Steps: []*types.ProductionReviewStep{{
			ID: "step-1", ReviewRequestID: projectionReviewID, TenantID: projectionTenantID,
			ProjectID: projectionProjectID, DocumentID: projectionDocumentID, VersionID: projectionVersionID,
			RequiredRole: types.ProductionRoleBusinessReviewer, Sequence: 1,
			Decision: types.ProductionReviewApproved,
		}},
	}
	release := &types.ProductionRelease{
		ID: projectionReleaseID, TenantID: projectionTenantID, ProjectID: projectionProjectID,
		DocumentID: projectionDocumentID, VersionID: projectionVersionID, ReviewRequestID: projectionReviewID,
		Status:               types.ProductionReleaseBuilding,
		ReleaseDigestVersion: types.ProductionReleaseDigestVersionCurrent,
	}
	release.ReleaseDigest = types.ComputeProductionReleaseDigest(release, version, review)
	config, configDigest, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(`{
			"version":1,
			"indexing_strategy":{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":false,"graph_enabled":false},
			"chunking":{"strategy":"recursive","chunk_size":777,"chunk_overlap":77},
			"embedding_model_id":"snapshot-embedding",
			"summary_model_id":"snapshot-summary",
			"question_generation_config":{"enabled":false,"question_count":3},
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
		events: &events, version: version, document: document, review: review,
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

func TestProjectionBuildRejectsUnknownReleaseDigestVersion(t *testing.T) {
	fixture := newProjectionBuilderFixture(t)
	fixture.releases.release.ReleaseDigestVersion = 0

	_, err := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
	require.ErrorIs(t, err, types.ErrProductionReleaseReprepareRequired)
	require.Nil(t, fixture.knowledge.created)
}

func TestProjectionBuildRequiresExactLatestApprovedVersionButAllowsNewerDraftHead(t *testing.T) {
	t.Run("wrong approved pointer", func(t *testing.T) {
		fixture := newProjectionBuilderFixture(t)
		fixture.document.LatestApprovedVersionID = projectionStringPtr("older-version")
		_, err := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
		require.ErrorIs(t, err, types.ErrProductionReviewScopeInvalid)
	})
	t.Run("newer draft head", func(t *testing.T) {
		fixture := newProjectionBuilderFixture(t)
		fixture.document.CurrentVersionID = projectionStringPtr("newer-draft")
		fixture.document.Status = types.ProductionDocumentDraft
		_, err := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
		require.NoError(t, err)
	})
}

func TestProjectionBuildRequiresPolicySnapshotToMatchApprovedStepsExactly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy types.JSON
		mutate func(*types.ProductionReviewRequest)
	}{
		{name: "empty policy", policy: types.JSON(`{"steps":[]}`)},
		{name: "wrong role", policy: types.JSON(`{"steps":["engineering_reviewer"]}`)},
		{name: "extra policy step", policy: types.JSON(`{"steps":["business_reviewer","engineering_reviewer"]}`)},
		{name: "duplicate persisted sequence", policy: types.JSON(`{"steps":["business_reviewer","business_reviewer"]}`), mutate: func(review *types.ProductionReviewRequest) {
			duplicate := *review.Steps[0]
			duplicate.ID = "step-2"
			review.Steps = append(review.Steps, &duplicate)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newProjectionBuilderFixture(t)
			canonical, digest, err := types.CanonicalProductionReviewPolicy(tc.policy)
			require.NoError(t, err)
			fixture.review.PolicySnapshot = canonical
			fixture.review.PolicyDigest = digest
			if tc.mutate != nil {
				tc.mutate(fixture.review)
			}
			_, err = fixture.builder.Build(projectionBuildContext(), projectionTargetID)
			require.ErrorIs(t, err, types.ErrProductionReviewScopeInvalid)
		})
	}
}

func TestProjectionBuildRejectsIncompleteProcessingSnapshots(t *testing.T) {
	for _, raw := range []types.JSON{
		types.JSON(`{"indexing_strategy":{"vector_enabled":true},"chunking":{"strategy":"recursive","chunk_size":512},"embedding_model_id":"embed","summary_model_id":"summary","graph":{"enabled":false}}`),
		types.JSON(`{"version":1,"indexing_strategy":{"vector_enabled":true},"chunking":{"strategy":"recursive","chunk_size":512},"summary_model_id":"summary","graph":{"enabled":false}}`),
		types.JSON(`{"version":1,"indexing_strategy":{"vector_enabled":true},"embedding_model_id":"embed","summary_model_id":"summary","graph":{"enabled":false}}`),
		types.JSON(`{"version":1,"indexing_strategy":{"graph_enabled":true},"chunking":{"strategy":"recursive","chunk_size":512},"summary_model_id":"summary","graph":{"enabled":true}}`),
	} {
		fixture := newProjectionBuilderFixture(t)
		canonical, digest, err := types.CanonicalProductionReleaseTargetConfig(raw)
		require.NoError(t, err)
		fixture.releases.target.ConfigSnapshot = canonical
		fixture.releases.target.ConfigDigest = digest
		_, err = fixture.builder.Build(projectionBuildContext(), projectionTargetID)
		require.ErrorIs(t, err, types.ErrProductionReleaseConfigInvalid)
		require.Nil(t, fixture.knowledge.created)
	}
}

func TestProductionReleaseRepositoryCreateFeedsProjectionBuilder(t *testing.T) {
	fixture := newProjectionBuilderFixture(t)
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "release-builder.db")+"?_foreign_keys=1"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	migrationDir := filepath.Join(filepath.Dir(filename), "../../../migrations/sqlite")
	for _, name := range []string{
		"000001_knowledge_production_foundation.up.sql",
		"000002_knowledge_production_documents.up.sql",
		"000003_knowledge_production_runs.up.sql",
		"000004_knowledge_production_reviews.up.sql",
	} {
		migration, readErr := os.ReadFile(filepath.Join(migrationDir, name))
		require.NoError(t, readErr)
		require.NoError(t, db.Exec(string(migration)).Error)
	}
	require.NoError(t, db.Exec(`CREATE TABLE knowledge_bases (
		id VARCHAR(36) PRIMARY KEY, tenant_id INTEGER NOT NULL, UNIQUE(id, tenant_id)
	)`).Error)
	for _, name := range []string{
		"000005_knowledge_production_publication.up.sql",
		"000006_knowledge_production_projection_integrity.up.sql",
	} {
		migration, readErr := os.ReadFile(filepath.Join(migrationDir, name))
		require.NoError(t, readErr)
		require.NoError(t, db.Exec(string(migration)).Error)
	}

	actorID := "93000000-0000-4000-8000-000000000099"
	typeID := "93000000-0000-4000-8000-000000000090"
	sourceSetID := fixture.version.SourceSetID
	require.NoError(t, db.Exec(`INSERT INTO production_projects (id, tenant_id, name, owner_user_id)
		VALUES (?, ?, 'Projection', ?)`, projectionProjectID, projectionTenantID, actorID).Error)
	require.NoError(t, db.Exec(`INSERT INTO production_document_types
		(id, tenant_id, code, name, schema_version, status, created_by)
		VALUES (?, ?, 'projection', 'Projection', 1, 'active', ?)`, typeID, projectionTenantID, actorID).Error)
	require.NoError(t, db.Exec(`INSERT INTO production_source_sets
		(id, tenant_id, project_id, document_type_id, status, created_by, frozen_at)
		VALUES (?, ?, ?, ?, 'frozen', ?, CURRENT_TIMESTAMP)`, sourceSetID, projectionTenantID, projectionProjectID, typeID, actorID).Error)
	require.NoError(t, db.Exec(`INSERT INTO production_documents
		(id, tenant_id, project_id, document_type_id, document_type_schema_version, title, status, created_by)
		VALUES (?, ?, ?, ?, 1, 'Software Baseline', 'draft', ?)`, projectionDocumentID, projectionTenantID, projectionProjectID, typeID, actorID).Error)
	require.NoError(t, db.Exec(`INSERT INTO production_document_versions
		(id, document_id, tenant_id, project_id, version_number, source_set_id, origin, content_digest, created_by, frozen_at)
		VALUES (?, ?, ?, ?, 3, ?, 'human', ?, ?, CURRENT_TIMESTAMP)`,
		projectionVersionID, projectionDocumentID, projectionTenantID, projectionProjectID,
		sourceSetID, fixture.version.ContentDigest, actorID,
	).Error)
	require.NoError(t, db.Exec(`UPDATE production_documents
		SET current_version_id = ?, latest_approved_version_id = ? WHERE id = ?`,
		projectionVersionID, projectionVersionID, projectionDocumentID,
	).Error)
	require.NoError(t, db.Exec(`INSERT INTO production_project_members
		(project_id, user_id, role, assigned_by) VALUES (?, ?, 'business_reviewer', ?)`,
		projectionProjectID, actorID, actorID,
	).Error)
	require.NoError(t, db.Exec(`INSERT INTO production_review_requests
		(id, tenant_id, project_id, document_id, version_id, policy_snapshot, policy_digest, submitted_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, projectionReviewID, projectionTenantID, projectionProjectID,
		projectionDocumentID, projectionVersionID, string(fixture.review.PolicySnapshot), fixture.review.PolicyDigest, actorID,
	).Error)
	require.NoError(t, db.Exec(`INSERT INTO production_review_steps
		(id, review_request_id, tenant_id, project_id, document_id, version_id, required_role, sequence)
		VALUES ('step-1', ?, ?, ?, ?, ?, 'business_reviewer', 1)`, projectionReviewID,
		projectionTenantID, projectionProjectID, projectionDocumentID, projectionVersionID,
	).Error)
	require.NoError(t, db.Exec(`UPDATE production_review_steps SET decision = 'approved',
		reviewer_user_id = ?, comment = 'approved', decided_at = CURRENT_TIMESTAMP WHERE id = 'step-1'`, actorID).Error)
	require.NoError(t, db.Exec(`UPDATE production_review_requests SET status = 'approved',
		terminal_by = ?, completed_at = CURRENT_TIMESTAMP WHERE id = ?`, actorID, projectionReviewID).Error)
	require.NoError(t, db.Exec(`INSERT INTO knowledge_bases (id, tenant_id) VALUES (?, ?)`, projectionKBID, projectionTenantID).Error)

	releaseRepo := apprepository.NewProductionReleaseRepository(db)
	release := *fixture.releases.release
	release.ReleaseDigest = ""
	release.ReleaseDigestVersion = 0
	target := *fixture.releases.target
	target.ReleaseDigest = ""
	target.ReleaseID = ""
	target.TenantID = 0
	target.ProjectID = ""
	target.DocumentID = ""
	target.VersionID = ""
	createCtx := context.WithValue(context.Background(), types.TenantIDContextKey, projectionTenantID)
	createCtx = context.WithValue(createCtx, types.UserIDContextKey, actorID)
	require.NoError(t, releaseRepo.CreateRelease(createCtx, &release, []*types.ProductionReleaseTarget{&target}))
	require.Equal(t, types.ProductionReleaseDigestVersionCurrent, release.ReleaseDigestVersion)
	require.Equal(t, release.ReleaseDigest, target.ReleaseDigest)

	fixture.builder.releases = releaseRepo
	knowledge, err := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
	require.NoError(t, err)
	require.Equal(t, projectionKnowledgeID, knowledge.ID)
}

func projectionTimePtr(value time.Time) *time.Time { return &value }
func projectionStringPtr(value string) *string     { return &value }
