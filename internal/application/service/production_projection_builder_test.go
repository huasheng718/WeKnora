package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
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
	mu          sync.Mutex
	target      *types.ProductionReleaseTarget
	release     *types.ProductionRelease
	events      *[]string
	transitions []types.ProductionReleaseTargetStatus
}

func (r *projectionBuilderReleaseRepo) GetTarget(context.Context, uint64, string) (*types.ProductionReleaseTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	r.mu.Lock()
	defer r.mu.Unlock()
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

type projectionBuilderDelegatingKnowledgeService struct {
	interfaces.KnowledgeService
	delegate *knowledgeService
}

func (s *projectionBuilderDelegatingKnowledgeService) GetKnowledgeByID(context.Context, string) (*types.Knowledge, error) {
	return nil, apprepository.ErrKnowledgeNotFound
}

func (s *projectionBuilderDelegatingKnowledgeService) CreateKnowledgeFromProductionProjection(
	ctx context.Context, payload *types.ProductionProjectionKnowledgePayload,
) (*types.Knowledge, error) {
	return s.delegate.CreateKnowledgeFromProductionProjection(ctx, payload)
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

func TestProjectionBuildConcurrentPendingReplaysClaimOneEnqueueAttempt(t *testing.T) {
	fixture := newProjectionBuilderFixture(t)
	pending, err := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
	require.NoError(t, err)
	tracker, spanDB := setupConcurrentSpanTrackerTest(t, pending.TenantID, pending.ID)
	tasks := &manualAttemptRetryEnqueuer{accepted: make(map[string]*asynq.Task)}
	realService := &knowledgeService{
		repo: &initialPostProcessKnowledgeRepo{knowledge: pending}, task: tasks, spanTracker: tracker,
	}
	fixture.builder.knowledge = &projectionBuilderDelegatingKnowledgeService{delegate: realService}

	const builders = 16
	start := make(chan struct{})
	errCh := make(chan error, builders)
	var builds sync.WaitGroup
	for range builders {
		builds.Add(1)
		go func() {
			defer builds.Done()
			<-start
			_, buildErr := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
			errCh <- buildErr
		}()
	}
	close(start)
	builds.Wait()
	close(errCh)
	for buildErr := range errCh {
		require.NoError(t, buildErr)
	}

	manualTaskID := productionProjectionTaskID(projectionTargetID, projectionKnowledgeID, "build")
	manualTask := tasks.accepted[manualTaskID]
	require.NotNil(t, manualTask)
	var payload types.ManualProcessPayload
	require.NoError(t, json.Unmarshal(manualTask.Payload(), &payload))
	require.Equal(t, tracker.LatestAttempt(projectionBuildContext(), pending.ID), payload.Attempt)
	var roots int64
	require.NoError(t, spanDB.Model(&types.KnowledgeProcessingSpan{}).
		Where("knowledge_id = ? AND kind = ?", pending.ID, types.SpanKindRoot).
		Count(&roots).Error)
	require.Equal(t, int64(1), roots)
	require.Len(t, tasks.accepted, 1)
}

func TestProjectionBuildReplayRejectsTamperedPersistedManualContent(t *testing.T) {
	fixture := newProjectionBuilderFixture(t)
	existing, err := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
	require.NoError(t, err)
	meta, err := existing.ManualMetadata()
	require.NoError(t, err)
	meta.Content = "# tampered persisted projection"
	require.NoError(t, existing.SetManualMetadata(meta))
	fixture.knowledge.existing = existing
	fixture.knowledge.created = nil

	_, err = fixture.builder.Build(projectionBuildContext(), projectionTargetID)
	require.ErrorIs(t, err, types.ErrProductionContentDigestMismatch)
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

func newProductionProjectionBuilderIntegrationFixture(
	t *testing.T,
) (*projectionBuilderFixture, *gorm.DB, interfaces.ProductionReleaseRepository, interfaces.KnowledgeRepository, context.Context) {
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
		"000007_production_projection_failure_recovery.up.sql",
	} {
		migration, readErr := os.ReadFile(filepath.Join(migrationDir, name))
		require.NoError(t, readErr)
		require.NoError(t, db.Exec(string(migration)).Error)
	}
	require.NoError(t, db.AutoMigrate(
		&types.Knowledge{}, &types.KnowledgeTag{}, &types.KnowledgeTagRelation{},
	))

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
	return fixture, db, releaseRepo, apprepository.NewKnowledgeRepository(db), createCtx
}

func TestProductionReleaseRepositoryCreateFeedsProjectionBuilder(t *testing.T) {
	fixture, _, releaseRepo, _, _ := newProductionProjectionBuilderIntegrationFixture(t)
	fixture.builder.releases = releaseRepo
	knowledge, err := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
	require.NoError(t, err)
	require.Equal(t, projectionKnowledgeID, knowledge.ID)
}

func TestProductionProjectionBuilderReconcilesCompletedCrashResidueWithRealRepositories(t *testing.T) {
	t.Run("completed building target becomes ready once", func(t *testing.T) {
		fixture, db, releaseRepo, knowledgeRepo, _ := newProductionProjectionBuilderIntegrationFixture(t)
		target, err := releaseRepo.GetTarget(projectionBuildContext(), projectionTenantID, projectionTargetID)
		require.NoError(t, err)
		knowledge := completedProjectionKnowledgeForBuilder(t, fixture, target, 0)
		require.NoError(t, knowledgeRepo.CreateKnowledge(projectionBuildContext(), knowledge))
		fixture.builder.releases = releaseRepo
		fixture.builder.knowledge = &knowledgeService{repo: knowledgeRepo}
		fixture.builder.uow = apprepository.NewProductionUnitOfWork(db)

		first, err := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
		require.NoError(t, err)
		require.Equal(t, knowledge.ID, first.ID)
		ready, err := releaseRepo.GetTarget(projectionBuildContext(), projectionTenantID, projectionTargetID)
		require.NoError(t, err)
		require.Equal(t, types.ReleaseTargetReady, ready.Status)

		second, err := fixture.builder.Build(projectionBuildContext(), projectionTargetID)
		require.NoError(t, err)
		require.Equal(t, knowledge.ID, second.ID)
		replayed, err := releaseRepo.GetTarget(projectionBuildContext(), projectionTenantID, projectionTargetID)
		require.NoError(t, err)
		require.Equal(t, types.ReleaseTargetReady, replayed.Status)
		require.True(t, replayed.UpdatedAt.Equal(ready.UpdatedAt), "ready replay must not mutate the target again")
	})

	t.Run("completed row with pending work stays building", func(t *testing.T) {
		fixture, db, releaseRepo, knowledgeRepo, _ := newProductionProjectionBuilderIntegrationFixture(t)
		target, err := releaseRepo.GetTarget(projectionBuildContext(), projectionTenantID, projectionTargetID)
		require.NoError(t, err)
		knowledge := completedProjectionKnowledgeForBuilder(t, fixture, target, 1)
		require.NoError(t, knowledgeRepo.CreateKnowledge(projectionBuildContext(), knowledge))
		fixture.builder.releases = releaseRepo
		fixture.builder.knowledge = &knowledgeService{repo: knowledgeRepo}
		fixture.builder.uow = apprepository.NewProductionUnitOfWork(db)

		_, err = fixture.builder.Build(projectionBuildContext(), projectionTargetID)
		require.ErrorIs(t, err, types.ErrProductionReleaseLifecycle)
		persisted, err := releaseRepo.GetTarget(projectionBuildContext(), projectionTenantID, projectionTargetID)
		require.NoError(t, err)
		require.Equal(t, types.ReleaseTargetBuilding, persisted.Status)
	})
}

func TestProductionProjectionRolledBackRetryIsRejectedWithoutClearingRetention(t *testing.T) {
	for _, entrypoint := range []string{"service", "builder"} {
		t.Run(entrypoint, func(t *testing.T) {
			fixture, db, releaseRepo, knowledgeRepo, actorCtx := newProductionProjectionBuilderIntegrationFixture(t)
			changed, err := releaseRepo.TransitionTarget(
				actorCtx, projectionTargetID, types.ReleaseTargetBuilding, types.ReleaseTargetRolledBack, nil,
			)
			require.NoError(t, err)
			require.True(t, changed)
			before, err := releaseRepo.GetTarget(actorCtx, projectionTenantID, projectionTargetID)
			require.NoError(t, err)
			require.NotNil(t, before.RolledBackAt)
			require.NotNil(t, before.RetentionUntil)
			knowledge := completedProjectionKnowledgeForBuilder(t, fixture, before, 0)
			require.NoError(t, knowledgeRepo.CreateKnowledge(actorCtx, knowledge))

			switch entrypoint {
			case "service":
				tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}
				svc := &ProductionReleaseService{
					releases: releaseRepo, authorizer: productionReleaseAuthorizerStub{},
					members: productionReleaseMembershipStub{role: types.TenantRoleContributor},
					kbs: productionReleaseKBStub{kb: &types.KnowledgeBase{
						ID: projectionKBID, TenantID: projectionTenantID, CreatorID: "93000000-0000-4000-8000-000000000099",
						Type: types.KnowledgeBaseTypeDocument,
					}},
					knowledge: &knowledgeService{repo: knowledgeRepo},
					uow:       apprepository.NewProductionUnitOfWork(db), tasks: tasks,
				}
				err = svc.Retry(actorCtx, projectionTargetID)
				require.ErrorIs(t, err, types.ErrProductionReleaseLifecycle)
				require.Empty(t, tasks.accepted)
			case "builder":
				fixture.builder.releases = releaseRepo
				fixture.builder.knowledge = &knowledgeService{repo: knowledgeRepo}
				fixture.builder.uow = apprepository.NewProductionUnitOfWork(db)
				_, err = fixture.builder.Build(projectionBuildContext(), projectionTargetID)
				require.ErrorIs(t, err, types.ErrProductionReleaseLifecycle)
			}

			after, err := releaseRepo.GetTarget(actorCtx, projectionTenantID, projectionTargetID)
			require.NoError(t, err)
			require.Equal(t, types.ReleaseTargetRolledBack, after.Status)
			require.Equal(t, before.RolledBackAt, after.RolledBackAt)
			require.Equal(t, before.RetentionUntil, after.RetentionUntil)
			require.True(t, after.UpdatedAt.Equal(before.UpdatedAt))
		})
	}
}

func TestProductionReleaseRetryReconcilesCompletedWithoutRetainedWorker(t *testing.T) {
	t.Run("concurrent and repeated Retry converge ready without enqueue", func(t *testing.T) {
		fixture := newCompletedProductionRetryIntegrationFixture(t, 0, false)
		const callers = 16
		barrier := newProductionRetryBarrierUOW(fixture.realUOW, callers)
		fixture.service.uow = barrier
		retryProductionProjectionConcurrently(
			t, fixture.service, fixture.actorCtx, projectionTargetID, callers, barrier,
		)

		ready, err := fixture.releaseRepo.GetTarget(fixture.actorCtx, projectionTenantID, projectionTargetID)
		require.NoError(t, err)
		require.Equal(t, types.ReleaseTargetReady, ready.Status)
		require.Equal(t, []string{fixture.originalTaskID}, acceptedProductionRetryTaskIDs(fixture.tasks))
		require.Equal(t, 1, productionRetryEnqueueCalls(fixture.tasks), "readiness reconciliation must not enqueue a worker")

		fixture.service.uow = fixture.realUOW
		require.NoError(t, fixture.service.Retry(fixture.actorCtx, projectionTargetID))
		replayed, err := fixture.releaseRepo.GetTarget(fixture.actorCtx, projectionTenantID, projectionTargetID)
		require.NoError(t, err)
		require.Equal(t, types.ReleaseTargetReady, replayed.Status)
		require.True(t, replayed.UpdatedAt.Equal(ready.UpdatedAt))
		require.Equal(t, 1, productionRetryEnqueueCalls(fixture.tasks))
	})

	t.Run("completed with pending work fails closed", func(t *testing.T) {
		fixture := newCompletedProductionRetryIntegrationFixture(t, 1, false)
		err := fixture.service.Retry(fixture.actorCtx, projectionTargetID)
		require.ErrorIs(t, err, types.ErrProductionReleaseLifecycle)
		persisted, loadErr := fixture.releaseRepo.GetTarget(fixture.actorCtx, projectionTenantID, projectionTargetID)
		require.NoError(t, loadErr)
		require.Equal(t, types.ReleaseTargetBuilding, persisted.Status)
		require.Equal(t, 1, productionRetryEnqueueCalls(fixture.tasks))
	})

	t.Run("tampered completed content fails closed", func(t *testing.T) {
		fixture := newCompletedProductionRetryIntegrationFixture(t, 0, true)
		err := fixture.service.Retry(fixture.actorCtx, projectionTargetID)
		require.ErrorIs(t, err, types.ErrProductionContentDigestMismatch)
		persisted, loadErr := fixture.releaseRepo.GetTarget(fixture.actorCtx, projectionTenantID, projectionTargetID)
		require.NoError(t, loadErr)
		require.Equal(t, types.ReleaseTargetBuilding, persisted.Status)
		require.Equal(t, 1, productionRetryEnqueueCalls(fixture.tasks))
	})
}

type completedProductionRetryIntegrationFixture struct {
	service        *ProductionReleaseService
	releaseRepo    interfaces.ProductionReleaseRepository
	actorCtx       context.Context
	tasks          *productionReleaseTaskEnqueuerStub
	realUOW        interfaces.ProductionUnitOfWork
	originalTaskID string
}

func newCompletedProductionRetryIntegrationFixture(
	t *testing.T,
	pendingSubtasks int,
	tamperContent bool,
) *completedProductionRetryIntegrationFixture {
	t.Helper()
	fixture, db, releaseRepo, knowledgeRepo, actorCtx := newProductionProjectionBuilderIntegrationFixture(t)
	target, err := releaseRepo.GetTarget(actorCtx, projectionTenantID, projectionTargetID)
	require.NoError(t, err)
	knowledge := completedProjectionKnowledgeForBuilder(t, fixture, target, pendingSubtasks)
	if tamperContent {
		meta, metaErr := knowledge.ManualMetadata()
		require.NoError(t, metaErr)
		meta.Content = "# tampered completed projection"
		require.NoError(t, knowledge.SetManualMetadata(meta))
	}
	require.NoError(t, knowledgeRepo.CreateKnowledge(actorCtx, knowledge))
	tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}
	originalTaskID := productionProjectionTaskID(target.ID, target.KnowledgeID, "build-initial")
	_, err = tasks.Enqueue(
		asynq.NewTask(types.TypeProductionBuild, nil), asynq.TaskID(originalTaskID),
	)
	require.NoError(t, err)
	realUOW := apprepository.NewProductionUnitOfWork(db)
	service := &ProductionReleaseService{
		releases: releaseRepo, authorizer: productionReleaseAuthorizerStub{},
		members: productionReleaseMembershipStub{role: types.TenantRoleContributor},
		kbs: productionReleaseKBStub{kb: &types.KnowledgeBase{
			ID: projectionKBID, TenantID: projectionTenantID, CreatorID: "93000000-0000-4000-8000-000000000099",
			Type: types.KnowledgeBaseTypeDocument,
		}},
		knowledge: &knowledgeService{repo: knowledgeRepo},
		uow:       realUOW, tasks: tasks,
	}
	return &completedProductionRetryIntegrationFixture{
		service: service, releaseRepo: releaseRepo, actorCtx: actorCtx,
		tasks: tasks, realUOW: realUOW, originalTaskID: originalTaskID,
	}
}

func TestProductionReleaseConcurrentRetryWithoutKnowledgeUsesOneRealGeneration(t *testing.T) {
	const callers = 16
	_, db, releaseRepo, knowledgeRepo, actorCtx := newProductionProjectionBuilderIntegrationFixture(t)
	changed, err := releaseRepo.TransitionTarget(
		actorCtx, projectionTargetID, types.ReleaseTargetBuilding, types.ReleaseTargetFailed, nil,
	)
	require.NoError(t, err)
	require.True(t, changed)
	firstFailure, err := releaseRepo.GetTarget(actorCtx, projectionTenantID, projectionTargetID)
	require.NoError(t, err)
	tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}
	realUOW := apprepository.NewProductionUnitOfWork(db)
	svc := &ProductionReleaseService{
		releases: releaseRepo, authorizer: productionReleaseAuthorizerStub{},
		members: productionReleaseMembershipStub{role: types.TenantRoleContributor},
		kbs: productionReleaseKBStub{kb: &types.KnowledgeBase{
			ID: projectionKBID, TenantID: projectionTenantID, CreatorID: "93000000-0000-4000-8000-000000000099",
			Type: types.KnowledgeBaseTypeDocument,
		}},
		knowledge: &knowledgeService{repo: knowledgeRepo},
		uow:       realUOW, tasks: tasks, audit: &productionReleaseAuditStub{},
	}

	firstBarrier := newProductionRetryBarrierUOW(realUOW, callers)
	svc.uow = firstBarrier
	retryProductionProjectionConcurrently(t, svc, actorCtx, projectionTargetID, callers, firstBarrier)
	firstRetry, err := releaseRepo.GetTarget(actorCtx, projectionTenantID, projectionTargetID)
	require.NoError(t, err)
	require.Equal(t, types.ReleaseTargetBuilding, firstRetry.Status)
	require.True(t, firstRetry.UpdatedAt.After(firstFailure.UpdatedAt))
	firstIDs := acceptedProductionRetryTaskIDs(tasks)
	require.Len(t, firstIDs, 1)
	require.Contains(t, firstIDs[0], fmt.Sprintf("build-retry-%d", firstRetry.UpdatedAt.UnixNano()))

	// A worker can fail before the preassigned Knowledge row is created. The
	// next concurrent request wave owns exactly one new failed generation.
	svc.uow = realUOW
	require.NoError(t, svc.recordProjectionTargetFailure(actorCtx, projectionTargetID, types.TypeProductionBuild))
	secondFailure, err := releaseRepo.GetTarget(actorCtx, projectionTenantID, projectionTargetID)
	require.NoError(t, err)
	require.Equal(t, types.ReleaseTargetFailed, secondFailure.Status)

	secondBarrier := newProductionRetryBarrierUOW(realUOW, callers)
	svc.uow = secondBarrier
	retryProductionProjectionConcurrently(t, svc, actorCtx, projectionTargetID, callers, secondBarrier)
	secondRetry, err := releaseRepo.GetTarget(actorCtx, projectionTenantID, projectionTargetID)
	require.NoError(t, err)
	require.Equal(t, types.ReleaseTargetBuilding, secondRetry.Status)
	require.True(t, secondRetry.UpdatedAt.After(secondFailure.UpdatedAt))
	require.True(t, secondRetry.UpdatedAt.After(firstRetry.UpdatedAt), "next failed retry must own a fresh generation")
	secondIDs := acceptedProductionRetryTaskIDs(tasks)
	require.Len(t, secondIDs, 2)
	require.Contains(t, secondIDs[1], fmt.Sprintf("build-retry-%d", secondRetry.UpdatedAt.UnixNano()))
	require.NotEqual(t, firstIDs[0], secondIDs[1])
}

func TestProductionReleaseBuildingFailedKnowledgeClaimEscapesRetainedGenerationOnce(t *testing.T) {
	const callers = 16
	fixture, db, releaseRepo, knowledgeRepo, actorCtx := newProductionProjectionBuilderIntegrationFixture(t)
	before, err := releaseRepo.GetTarget(actorCtx, projectionTenantID, projectionTargetID)
	require.NoError(t, err)
	knowledge := completedProjectionKnowledgeForBuilder(t, fixture, before, 0)
	knowledge.ParseStatus = types.ParseStatusFailed
	knowledge.ErrorMessage = "sanitized worker failure"
	require.NoError(t, knowledgeRepo.CreateKnowledge(actorCtx, knowledge))
	tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}
	originalTaskID := productionProjectionTaskID(before.ID, before.KnowledgeID, "build-initial")
	_, err = tasks.Enqueue(
		asynq.NewTask(types.TypeProductionBuild, nil), asynq.TaskID(originalTaskID),
	)
	require.NoError(t, err)
	realUOW := apprepository.NewProductionUnitOfWork(db)
	svc := &ProductionReleaseService{
		releases: releaseRepo, authorizer: productionReleaseAuthorizerStub{},
		members: productionReleaseMembershipStub{role: types.TenantRoleContributor},
		kbs: productionReleaseKBStub{kb: &types.KnowledgeBase{
			ID: projectionKBID, TenantID: projectionTenantID, CreatorID: "93000000-0000-4000-8000-000000000099",
			Type: types.KnowledgeBaseTypeDocument,
		}},
		knowledge: &knowledgeService{repo: knowledgeRepo},
		uow:       realUOW, tasks: tasks,
	}

	firstBarrier := newProductionRetryBarrierUOW(realUOW, callers)
	svc.uow = firstBarrier
	retryProductionProjectionConcurrently(t, svc, actorCtx, projectionTargetID, callers, firstBarrier)
	afterClaim, err := releaseRepo.GetTarget(actorCtx, projectionTenantID, projectionTargetID)
	require.NoError(t, err)
	require.Equal(t, types.ReleaseTargetBuilding, afterClaim.Status)
	require.True(t, afterClaim.UpdatedAt.After(before.UpdatedAt), "claim winner must escape the retained task generation")
	claimed, err := knowledgeRepo.GetKnowledgeByIDOnly(actorCtx, knowledge.ID)
	require.NoError(t, err)
	require.Equal(t, types.ParseStatusPending, claimed.ParseStatus)
	require.Empty(t, claimed.ErrorMessage)
	firstIDs := acceptedProductionRetryTaskIDs(tasks)
	require.Len(t, firstIDs, 2)
	require.Contains(t, firstIDs, originalTaskID)
	var retryTaskID string
	for _, taskID := range firstIDs {
		if taskID != originalTaskID {
			retryTaskID = taskID
		}
	}
	require.NotEmpty(t, retryTaskID)
	require.Contains(t, retryTaskID, fmt.Sprintf("build-retry-%d", afterClaim.UpdatedAt.UnixNano()))

	secondBarrier := newProductionRetryBarrierUOW(realUOW, callers)
	svc.uow = secondBarrier
	retryProductionProjectionConcurrently(t, svc, actorCtx, projectionTargetID, callers, secondBarrier)
	afterReplay, err := releaseRepo.GetTarget(actorCtx, projectionTenantID, projectionTargetID)
	require.NoError(t, err)
	require.True(t, afterReplay.UpdatedAt.Equal(afterClaim.UpdatedAt), "pending replay must retain the claimed generation")
	require.Equal(t, firstIDs, acceptedProductionRetryTaskIDs(tasks))
}

type productionRetryBarrierUOW struct {
	delegate interfaces.ProductionUnitOfWork
	arrived  chan struct{}
	release  chan struct{}
	serial   sync.Mutex
}

func newProductionRetryBarrierUOW(
	delegate interfaces.ProductionUnitOfWork,
	callers int,
) *productionRetryBarrierUOW {
	return &productionRetryBarrierUOW{
		delegate: delegate, arrived: make(chan struct{}, callers), release: make(chan struct{}),
	}
}

func (u *productionRetryBarrierUOW) WithinTransaction(
	ctx context.Context,
	fn func(context.Context) error,
) error {
	u.arrived <- struct{}{}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-u.release:
	}
	u.serial.Lock()
	defer u.serial.Unlock()
	return u.delegate.WithinTransaction(ctx, fn)
}

func retryProductionProjectionConcurrently(
	t *testing.T,
	svc *ProductionReleaseService,
	ctx context.Context,
	targetID string,
	callers int,
	barrier *productionRetryBarrierUOW,
) {
	t.Helper()
	start := make(chan struct{})
	errs := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			errs <- svc.Retry(ctx, targetID)
		}()
	}
	close(start)
	for range callers {
		select {
		case <-barrier.arrived:
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent Retry callers did not reach the claim transaction barrier")
		}
	}
	close(barrier.release)
	for range callers {
		require.NoError(t, <-errs)
	}
}

func acceptedProductionRetryTaskIDs(tasks *productionReleaseTaskEnqueuerStub) []string {
	tasks.mu.Lock()
	defer tasks.mu.Unlock()
	ids := make([]string, 0, len(tasks.accepted))
	for id := range tasks.accepted {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func productionRetryEnqueueCalls(tasks *productionReleaseTaskEnqueuerStub) int {
	tasks.mu.Lock()
	defer tasks.mu.Unlock()
	return tasks.calls
}

func completedProjectionKnowledgeForBuilder(
	t *testing.T,
	fixture *projectionBuilderFixture,
	target *types.ProductionReleaseTarget,
	pendingSubtasks int,
) *types.Knowledge {
	t.Helper()
	markdown, err := RenderProductionMarkdown(fixture.version)
	require.NoError(t, err)
	_, _, summaryModelID, graphModelID, indexingStrategy, err := productionProjectionProcessSnapshot(target.ConfigSnapshot)
	require.NoError(t, err)
	contentSum := sha256.Sum256([]byte(markdown))
	meta := types.NewManualKnowledgeMetadata(markdown, types.ManualKnowledgeStatusPublish, 1)
	meta.ProductionProjection = &types.ProductionProjectionMetadata{
		DocumentID: target.DocumentID, VersionID: target.VersionID, ReleaseTargetID: target.ID,
		ContentDigest: hex.EncodeToString(contentSum[:]), GraphModelID: graphModelID,
		SummaryModelID: summaryModelID, IndexingStrategy: indexingStrategy,
	}
	knowledge := &types.Knowledge{
		ID: target.KnowledgeID, TenantID: target.TenantID, KnowledgeBaseID: target.TargetKnowledgeBaseID,
		Type: types.KnowledgeTypeManual, ParseStatus: types.ParseStatusCompleted,
		PendingSubtasksCount: pendingSubtasks,
	}
	require.NoError(t, knowledge.SetManualMetadata(meta))
	return knowledge
}

func projectionTimePtr(value time.Time) *time.Time { return &value }
func projectionStringPtr(value string) *string     { return &value }
