package repository

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	releaseIDOne      = "30000000-0000-4000-8000-000000000001"
	releaseIDTwo      = "30000000-0000-4000-8000-000000000002"
	releaseTarget1    = "30000000-0000-4000-8000-000000000003"
	releaseTarget2    = "30000000-0000-4000-8000-000000000004"
	releaseTarget3    = "30000000-0000-4000-8000-000000000005"
	releaseKBOne      = "30000000-0000-4000-8000-000000000006"
	releaseKBTwo      = "30000000-0000-4000-8000-000000000007"
	releaseKnowledge1 = "30000000-0000-4000-8000-000000000008"
	releaseKnowledge2 = "30000000-0000-4000-8000-000000000009"
	releaseKnowledge3 = "30000000-0000-4000-8000-00000000000a"
)

type productionReleaseTestClock struct{ current time.Time }

func (c *productionReleaseTestClock) Now() time.Time { return c.current }

func productionReleaseContext(tenantID uint64, actorID string) context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenantID)
	return context.WithValue(ctx, types.UserIDContextKey, actorID)
}

func newProductionReleaseRepoFixture(t *testing.T, clock ProductionReleaseClock) (interfaces.ProductionReleaseRepository, *gorm.DB) {
	t.Helper()
	reviewRepo, db := newProductionReviewRepoFixture(t)
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	runsMigration, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "../../../migrations/sqlite/000003_knowledge_production_runs.up.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(runsMigration)).Error)
	require.NoError(t, db.Exec(`CREATE TABLE knowledge_bases (id VARCHAR(36) PRIMARY KEY, tenant_id INTEGER NOT NULL, UNIQUE(id, tenant_id))`).Error)
	migration, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "../../../migrations/sqlite/000005_knowledge_production_publication.up.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)
	require.NoError(t, db.Exec(`INSERT INTO knowledge_bases (id, tenant_id) VALUES (?, ?), (?, ?), (?, 8)`, releaseKBOne, reviewTenantID, releaseKBTwo, reviewTenantID, releaseKBTwo+"-other", 8).Error)

	approveProductionReleaseReview(t, db, reviewRepo, reviewID(700), reviewVersionOne, 700)
	approveProductionReleaseReview(t, db, reviewRepo, reviewID(710), reviewVersionTwo, 710)
	return NewProductionReleaseRepositoryWithClock(db, clock), db
}

func approveProductionReleaseReview(t *testing.T, db *gorm.DB, repo interfaces.ProductionReviewRepository, requestID, versionID string, stepStart int) {
	t.Helper()
	request := productionReviewRequest(requestID, versionID)
	steps := productionReviewSteps(request.ID, stepStart)
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, steps))
	for index, actor := range []string{reviewBusinessActor, reviewEngineeringActor} {
		require.NoError(t, db.Model(&types.ProductionReviewStep{}).Where("id = ?", steps[index].ID).Updates(map[string]any{
			"decision": types.ProductionReviewApproved, "reviewer_user_id": actor,
			"comment": "approved", "decided_at": time.Now().UTC(),
		}).Error)
	}
	require.NoError(t, db.Model(&types.ProductionReviewRequest{}).Where("id = ?", requestID).Updates(map[string]any{
		"status": types.ProductionReviewApproved, "terminal_by": reviewEngineeringActor,
		"completed_at": time.Now().UTC(),
	}).Error)
	persisted, err := repo.GetReview(context.Background(), reviewTenantID, requestID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewApproved), persisted.Status)
}

func productionRelease(id, versionID, reviewRequestID string) *types.ProductionRelease {
	return &types.ProductionRelease{
		ID: id, TenantID: reviewTenantID, ProjectID: reviewProjectID, DocumentID: reviewDocumentID,
		VersionID: versionID, ReviewRequestID: reviewRequestID,
		Status: types.ProductionReleaseBuilding, RetentionDays: 30,
	}
}

func productionReleaseTarget(id, kbID, knowledgeID string) *types.ProductionReleaseTarget {
	return &types.ProductionReleaseTarget{
		ID: id, TargetKnowledgeBaseID: kbID, KnowledgeID: knowledgeID,
		ConfigSnapshot: types.JSON(`{ "chunking": { "size": 512 }, "graph": true }`),
	}
}

func TestProductionReleaseRepositoryCreatesReleaseAndTargetsAtomicallyWithTrustedOwnership(t *testing.T) {
	now := time.Date(2026, 7, 21, 3, 4, 5, 123456789, time.UTC)
	ownedNow := now.Truncate(time.Second)
	repo, db := newProductionReleaseRepoFixture(t, &productionReleaseTestClock{current: now})
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	targets := []*types.ProductionReleaseTarget{
		productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1),
		productionReleaseTarget(releaseTarget2, releaseKBTwo, releaseKnowledge2),
	}

	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release, targets))
	require.Equal(t, reviewAuthorID, release.CreatedBy)
	require.Equal(t, ownedNow, release.CreatedAt)
	for _, target := range targets {
		require.Equal(t, release.ID, target.ReleaseID)
		require.Equal(t, release.TenantID, target.TenantID)
		require.Equal(t, release.ProjectID, target.ProjectID)
		require.Equal(t, release.DocumentID, target.DocumentID)
		require.Equal(t, release.VersionID, target.VersionID)
		require.Equal(t, release.ReleaseDigest, target.ReleaseDigest)
		canonicalConfig, _, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(`{"chunking":{"size":512},"graph":true}`))
		require.NoError(t, err)
		require.Equal(t, canonicalConfig, target.ConfigSnapshot)
		require.Len(t, target.ConfigDigest, 64)
		require.Equal(t, ownedNow, target.CreatedAt)
		persisted, err := repo.GetTarget(context.Background(), reviewTenantID, target.ID)
		require.NoError(t, err)
		require.Equal(t, target.ConfigSnapshot, persisted.ConfigSnapshot)
		require.Equal(t, target.ConfigDigest, persisted.ConfigDigest)
	}

	var releaseCount, targetCount int64
	require.NoError(t, db.Model(&types.ProductionRelease{}).Count(&releaseCount).Error)
	require.NoError(t, db.Model(&types.ProductionReleaseTarget{}).Count(&targetCount).Error)
	require.EqualValues(t, 1, releaseCount)
	require.EqualValues(t, 2, targetCount)
}

func TestProductionApprovedReviewLookupIsTenantAndVersionScoped(t *testing.T) {
	_, db := newProductionReleaseRepoFixture(t, nil)
	reviews := NewProductionReviewRepository(db)
	ctx := productionReleaseContext(reviewTenantID, reviewAuthorID)
	review, err := reviews.(*productionReviewRepository).GetApprovedReviewForVersion(
		ctx, reviewTenantID, reviewDocumentID, reviewVersionOne,
	)
	require.NoError(t, err)
	require.Equal(t, reviewID(700), review.ID)
	require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewApproved), review.Status)
	require.NotEmpty(t, review.Steps)

	_, err = reviews.(*productionReviewRepository).GetApprovedReviewForVersion(
		productionReleaseContext(reviewTenantID+1, reviewAuthorID), reviewTenantID,
		reviewDocumentID, reviewVersionOne,
	)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionReleaseRepositoryListsOnlyExpiredCleanupTargets(t *testing.T) {
	clock := &productionReleaseTestClock{current: time.Now().UTC().Add(-31 * 24 * time.Hour).Truncate(time.Second)}
	repo, db := newProductionReleaseRepoFixture(t, clock)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	target := productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)
	ctx := productionReleaseContext(reviewTenantID, reviewAuthorID)
	require.NoError(t, repo.CreateRelease(ctx, release, []*types.ProductionReleaseTarget{target}))
	changed, err := repo.TransitionTarget(ctx, target.ID, types.ReleaseTargetBuilding, types.ReleaseTargetFailed, nil)
	require.NoError(t, err)
	require.True(t, changed)

	lister := repo.(interface {
		ListCleanupEligible(context.Context, uint64, int) ([]*types.ProductionReleaseTarget, error)
	})
	eligible, err := lister.ListCleanupEligible(ctx, reviewTenantID, 10)
	require.NoError(t, err)
	require.Empty(t, eligible)

	clock.current = clock.current.Add(31 * 24 * time.Hour)
	eligible, err = lister.ListCleanupEligible(ctx, reviewTenantID, 10)
	require.NoError(t, err)
	require.Len(t, eligible, 1)
	require.Equal(t, target.ID, eligible[0].ID)

	changed, err = repo.TransitionTarget(
		ctx, target.ID, types.ReleaseTargetFailed, types.ReleaseTargetCleanupPending, nil,
	)
	require.NoError(t, err)
	require.True(t, changed)
	eligible, err = lister.ListCleanupEligible(ctx, reviewTenantID, 10)
	require.NoError(t, err)
	require.Len(t, eligible, 1, "restart recovery must resume work committed as cleanup_pending")
	require.Equal(t, types.ReleaseTargetCleanupPending, eligible[0].Status)

	require.NoError(t, db.Exec(`DROP TRIGGER trg_production_projection_heads_validate_insert`).Error)
	require.NoError(t, db.Exec(`DROP TRIGGER trg_production_projection_heads_activate_target_insert`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO production_projection_heads
		(tenant_id, document_id, target_knowledge_base_id, active_release_target_id, lock_version, updated_at)
		VALUES (?, ?, ?, ?, 1, ?)
	`, reviewTenantID, reviewDocumentID, releaseKBOne, target.ID, clock.current).Error)
	eligible, err = lister.ListCleanupEligible(ctx, reviewTenantID, 10)
	require.NoError(t, err)
	require.Empty(t, eligible, "even cleanup_pending recovery must anti-join active heads")
}

func TestProductionReleaseRepositoryListsBoundedBuildingTargetsWithFailedKnowledge(t *testing.T) {
	repo, db := newProductionReleaseRepoFixture(t, nil)
	require.NoError(t, db.Exec(`
		CREATE TABLE knowledges (
			id VARCHAR(36) PRIMARY KEY,
			tenant_id INTEGER NOT NULL,
			knowledge_base_id VARCHAR(36) NOT NULL,
			parse_status VARCHAR(50) NOT NULL,
			deleted_at DATETIME NULL
		)
	`).Error)
	ctx := productionReleaseContext(reviewTenantID, reviewAuthorID)
	first := productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)
	completed := productionReleaseTarget(releaseTarget2, releaseKBTwo, releaseKnowledge2)
	require.NoError(t, repo.CreateRelease(ctx,
		productionRelease(releaseIDOne, reviewVersionOne, reviewID(700)),
		[]*types.ProductionReleaseTarget{first, completed},
	))
	secondFailed := productionReleaseTarget(releaseTarget3, releaseKBOne, releaseKnowledge3)
	require.NoError(t, repo.CreateRelease(ctx,
		productionRelease(releaseIDTwo, reviewVersionTwo, reviewID(710)),
		[]*types.ProductionReleaseTarget{secondFailed},
	))
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, parse_status)
		VALUES (?, ?, ?, ?), (?, ?, ?, ?), (?, ?, ?, ?)
	`,
		first.KnowledgeID, reviewTenantID, first.TargetKnowledgeBaseID, types.ParseStatusFailed,
		completed.KnowledgeID, reviewTenantID, completed.TargetKnowledgeBaseID, types.ParseStatusCompleted,
		secondFailed.KnowledgeID, reviewTenantID, secondFailed.TargetKnowledgeBaseID, types.ParseStatusFailed,
	).Error)

	candidates, err := repo.ListBuildingTargetsWithFailedKnowledge(ctx, reviewTenantID, 1)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, first.ID, candidates[0].ID, "candidate order must be stable before applying the limit")

	candidates, err = repo.ListBuildingTargetsWithFailedKnowledge(ctx, reviewTenantID, 200)
	require.NoError(t, err)
	require.Equal(t, []string{first.ID, secondFailed.ID}, []string{candidates[0].ID, candidates[1].ID})

	changed, err := repo.TransitionTarget(ctx, first.ID, types.ReleaseTargetBuilding, types.ReleaseTargetFailed, nil)
	require.NoError(t, err)
	require.True(t, changed)
	candidates, err = repo.ListBuildingTargetsWithFailedKnowledge(ctx, reviewTenantID, 0)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, secondFailed.ID, candidates[0].ID)

	_, err = repo.ListBuildingTargetsWithFailedKnowledge(
		productionReleaseContext(reviewTenantID+1, reviewAuthorID), reviewTenantID, 10,
	)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionReleaseRepositoryRotatesPermanentRecoveryFailuresAcrossRestart(t *testing.T) {
	_, db := newProductionReviewRepoFixture(t)
	require.NoError(t, db.Exec(`
		CREATE TABLE production_release_targets (
			id VARCHAR(36) PRIMARY KEY,
			release_id VARCHAR(36) NOT NULL,
			tenant_id INTEGER NOT NULL,
			project_id VARCHAR(36) NOT NULL,
			document_id VARCHAR(36) NOT NULL,
			version_id VARCHAR(36) NOT NULL,
			target_knowledge_base_id VARCHAR(36) NOT NULL,
			knowledge_id VARCHAR(36) NOT NULL UNIQUE,
			release_digest VARCHAR(64) NOT NULL,
			config_snapshot TEXT NOT NULL,
			config_digest VARCHAR(64) NOT NULL,
			status VARCHAR(20) NOT NULL,
			failure_code VARCHAR(64) NOT NULL DEFAULT '',
			failure_reason VARCHAR(256) NOT NULL DEFAULT '',
			retention_days INTEGER NOT NULL DEFAULT 30,
			retention_until DATETIME NULL,
			activated_at DATETIME NULL,
			failed_at DATETIME NULL,
			rolled_back_at DATETIME NULL,
			cleanup_requested_at DATETIME NULL,
			cleaned_at DATETIME NULL,
			recovery_attempted_at DATETIME NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);
		CREATE TABLE knowledges (
			id VARCHAR(36) PRIMARY KEY,
			tenant_id INTEGER NOT NULL,
			knowledge_base_id VARCHAR(36) NOT NULL,
			parse_status VARCHAR(50) NOT NULL,
			deleted_at DATETIME NULL
		);
	`).Error)
	canonical, digest, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(
		`{"version":1,"indexing_strategy":{"wiki_enabled":true},"chunking":{"strategy":"recursive","chunk_size":256},"summary_model_id":"summary-1","graph":{"enabled":false}}`,
	))
	require.NoError(t, err)
	createdAt := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	for index := 1; index <= 202; index++ {
		tenantID := reviewTenantID
		if index == 202 {
			tenantID = reviewTenantID + 1
		}
		targetID := fmt.Sprintf("target-%03d", index)
		knowledgeID := fmt.Sprintf("knowledge-%03d", index)
		kbID := fmt.Sprintf("kb-%03d", index)
		require.NoError(t, db.Exec(`
			INSERT INTO production_release_targets
			(id, release_id, tenant_id, project_id, document_id, version_id,
			 target_knowledge_base_id, knowledge_id, release_digest, config_snapshot,
			 config_digest, status, created_at, updated_at)
			VALUES (?, 'release-1', ?, 'project-1', 'document-1', 'version-1', ?, ?, ?, ?, ?, ?, ?, ?)
		`, targetID, tenantID, kbID, knowledgeID, strings.Repeat("a", 64), string(canonical), digest,
			types.ReleaseTargetBuilding, createdAt, createdAt).Error)
		require.NoError(t, db.Exec(`
			INSERT INTO knowledges (id, tenant_id, knowledge_base_id, parse_status)
			VALUES (?, ?, ?, ?)
		`, knowledgeID, tenantID, kbID, types.ParseStatusFailed).Error)
	}

	clock := &productionReleaseTestClock{current: createdAt.Add(time.Hour)}
	repo := NewProductionReleaseRepositoryWithClock(db, clock)
	ctx := productionReleaseContext(reviewTenantID, reviewAuthorID)
	first, err := repo.ListBuildingTargetsWithFailedKnowledge(ctx, reviewTenantID, 100)
	require.NoError(t, err)
	require.Len(t, first, 100)
	require.Equal(t, "target-001", first[0].ID)
	for _, candidate := range first {
		deferred, deferErr := repo.DeferProjectionFailureRecovery(ctx, candidate.ID, candidate.UpdatedAt)
		require.NoError(t, deferErr)
		require.True(t, deferred)
	}

	clock.current = clock.current.Add(time.Hour)
	second, err := repo.ListBuildingTargetsWithFailedKnowledge(ctx, reviewTenantID, 100)
	require.NoError(t, err)
	require.Len(t, second, 100)
	require.Equal(t, "target-101", second[0].ID)
	for _, candidate := range second {
		deferred, deferErr := repo.DeferProjectionFailureRecovery(ctx, candidate.ID, candidate.UpdatedAt)
		require.NoError(t, deferErr)
		require.True(t, deferred)
	}

	restarted := NewProductionReleaseRepositoryWithClock(db, clock)
	third, err := restarted.ListBuildingTargetsWithFailedKnowledge(ctx, reviewTenantID, 100)
	require.NoError(t, err)
	require.NotEmpty(t, third)
	require.Equal(t, "target-201", third[0].ID,
		"untouched candidate 201 must progress ahead of 200 persistent failures after restart")
	for _, candidate := range third {
		require.Equal(t, reviewTenantID, candidate.TenantID)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = restarted.ListBuildingTargetsWithFailedKnowledge(cancelled, reviewTenantID, 100)
	require.ErrorIs(t, err, context.Canceled)
}

func TestProductionProjectionRetryClaimRollbackRestoresKnowledgeAndTarget(t *testing.T) {
	repo, db := newProductionReleaseRepoFixture(t, nil)
	require.NoError(t, db.Exec(`
		CREATE TABLE knowledges (
			id VARCHAR(36) PRIMARY KEY,
			tenant_id INTEGER NOT NULL,
			knowledge_base_id VARCHAR(36) NOT NULL,
			type VARCHAR(50) NOT NULL,
			parse_status VARCHAR(50) NOT NULL,
			pending_subtasks_count INTEGER NOT NULL DEFAULT 0,
			error_message TEXT,
			metadata TEXT,
			updated_at DATETIME NOT NULL,
			deleted_at DATETIME NULL
		)
	`).Error)
	ctx := productionReleaseContext(reviewTenantID, reviewAuthorID)
	target := productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)
	require.NoError(t, repo.CreateRelease(ctx,
		productionRelease(releaseIDOne, reviewVersionOne, reviewID(700)),
		[]*types.ProductionReleaseTarget{target},
	))
	changed, err := repo.TransitionTarget(ctx, target.ID, types.ReleaseTargetBuilding, types.ReleaseTargetFailed, nil)
	require.NoError(t, err)
	require.True(t, changed)
	failedTarget, err := repo.GetTarget(ctx, reviewTenantID, target.ID)
	require.NoError(t, err)
	content := "# governed projection"
	knowledge := &types.Knowledge{
		ID: target.KnowledgeID, TenantID: target.TenantID, KnowledgeBaseID: target.TargetKnowledgeBaseID,
		Type: types.KnowledgeTypeManual, ParseStatus: types.ParseStatusFailed, UpdatedAt: time.Now().UTC(),
	}
	meta := types.NewManualKnowledgeMetadata(content, types.ManualKnowledgeStatusPublish, 1)
	contentSum := sha256.Sum256([]byte(content))
	meta.ProductionProjection = &types.ProductionProjectionMetadata{
		DocumentID: target.DocumentID, VersionID: target.VersionID, ReleaseTargetID: target.ID,
		ContentDigest: hex.EncodeToString(contentSum[:]), SummaryModelID: "summary-1",
		IndexingStrategy: types.IndexingStrategy{WikiEnabled: true},
	}
	require.NoError(t, knowledge.SetManualMetadata(meta))
	require.NoError(t, db.Table("knowledges").Create(map[string]any{
		"id": knowledge.ID, "tenant_id": knowledge.TenantID,
		"knowledge_base_id": knowledge.KnowledgeBaseID, "type": knowledge.Type,
		"parse_status": knowledge.ParseStatus, "pending_subtasks_count": 0,
		"error_message": "", "metadata": string(knowledge.Metadata), "updated_at": knowledge.UpdatedAt,
	}).Error)
	knowledgeRepo := NewKnowledgeRepository(db)
	uow := NewProductionUnitOfWork(db)

	err = uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		_, lockErr := repo.GetTargetForUpdate(txCtx, reviewTenantID, target.ID)
		if lockErr != nil {
			return lockErr
		}
		_, lockErr = knowledgeRepo.GetKnowledgeByIDOnlyForUpdate(txCtx, knowledge.ID)
		if lockErr != nil {
			return lockErr
		}
		claimed, claimErr := knowledgeRepo.ClaimFailedKnowledgeRetry(txCtx, knowledge.ID)
		if claimErr != nil {
			return claimErr
		}
		require.True(t, claimed)
		_, transitioned, transitionErr := repo.TransitionTargetForRetry(
			txCtx, target.ID, types.ReleaseTargetFailed, failedTarget.UpdatedAt.Add(-time.Second),
		)
		if transitionErr != nil {
			return transitionErr
		}
		require.False(t, transitioned)
		return types.ErrProductionProjectionConflict
	})
	require.ErrorIs(t, err, types.ErrProductionProjectionConflict)
	persistedKnowledge, err := knowledgeRepo.GetKnowledgeByIDOnly(ctx, knowledge.ID)
	require.NoError(t, err)
	require.Equal(t, types.ParseStatusFailed, persistedKnowledge.ParseStatus)
	persistedTarget, err := repo.GetTarget(ctx, reviewTenantID, target.ID)
	require.NoError(t, err)
	require.Equal(t, types.ReleaseTargetFailed, persistedTarget.Status)
}

func TestProductionProjectionBuildGenerationClaimIsAtomicAndFenced(t *testing.T) {
	repo, db := newProductionReleaseRepoFixture(t, nil)
	require.NoError(t, db.Exec(`
		CREATE TABLE knowledges (
			id VARCHAR(36) PRIMARY KEY,
			tenant_id INTEGER NOT NULL,
			knowledge_base_id VARCHAR(36) NOT NULL,
			type VARCHAR(50) NOT NULL,
			parse_status VARCHAR(50) NOT NULL,
			error_message TEXT,
			updated_at DATETIME NOT NULL,
			deleted_at DATETIME NULL
		)
	`).Error)
	ctx := productionReleaseContext(reviewTenantID, reviewAuthorID)
	target := productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)
	require.NoError(t, repo.CreateRelease(ctx,
		productionRelease(releaseIDOne, reviewVersionOne, reviewID(700)),
		[]*types.ProductionReleaseTarget{target},
	))
	persistedTarget, err := repo.GetTarget(ctx, reviewTenantID, target.ID)
	require.NoError(t, err)
	require.NoError(t, db.Table("knowledges").Create(map[string]any{
		"id": target.KnowledgeID, "tenant_id": reviewTenantID,
		"knowledge_base_id": target.TargetKnowledgeBaseID, "type": types.KnowledgeTypeManual,
		"parse_status": types.ParseStatusPending, "error_message": "", "updated_at": time.Now().UTC(),
	}).Error)

	claimed, err := repo.ClaimProjectionBuildGeneration(
		ctx, target.ID, target.KnowledgeID, persistedTarget.UpdatedAt.Add(-time.Second),
	)
	require.NoError(t, err)
	require.False(t, claimed)

	claimed, err = repo.ClaimProjectionBuildGeneration(
		ctx, target.ID, target.KnowledgeID, persistedTarget.UpdatedAt,
	)
	require.NoError(t, err)
	require.True(t, claimed)
	var status string
	require.NoError(t, db.Table("knowledges").Select("parse_status").Where("id = ?", target.KnowledgeID).Scan(&status).Error)
	require.Equal(t, types.ParseStatusProcessing, status)

	changed, err := repo.TransitionTarget(ctx, target.ID, types.ReleaseTargetBuilding, types.ReleaseTargetFailed, nil)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, db.Table("knowledges").Where("id = ?", target.KnowledgeID).
		Update("parse_status", types.ParseStatusFailed).Error)
	claimed, err = repo.ClaimProjectionBuildGeneration(
		ctx, target.ID, target.KnowledgeID, persistedTarget.UpdatedAt,
	)
	require.NoError(t, err)
	require.False(t, claimed)
	require.NoError(t, db.Table("knowledges").Select("parse_status").Where("id = ?", target.KnowledgeID).Scan(&status).Error)
	require.Equal(t, types.ParseStatusFailed, status)
}

func TestProductionProjectionBuildGenerationClaimLocksTargetBeforeKnowledge(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	repo := NewProductionReleaseRepository(db)
	generation := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM "production_release_targets".*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "knowledge_id", "target_knowledge_base_id", "status", "updated_at",
		}).AddRow(
			releaseTarget1, reviewTenantID, releaseKnowledge1, releaseKBOne,
			types.ReleaseTargetBuilding, generation,
		))
	mock.ExpectExec(`UPDATE "knowledges" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	claimed, err := repo.ClaimProjectionBuildGeneration(
		productionReleaseContext(reviewTenantID, reviewAuthorID),
		releaseTarget1, releaseKnowledge1, generation,
	)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProductionReleasePostgresRecoveryQueryUsesPersistedFairOrder(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewProductionReleaseRepository(db)
	ctx := productionReleaseContext(reviewTenantID, reviewAuthorID)

	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT target.* FROM production_release_targets AS target JOIN knowledges AS knowledge
			ON knowledge.id = target.knowledge_id
			AND knowledge.tenant_id = target.tenant_id
			AND knowledge.knowledge_base_id = target.target_knowledge_base_id WHERE (target.tenant_id = $1 AND target.status = $2) AND (knowledge.parse_status = $3 AND knowledge.deleted_at IS NULL) ORDER BY target.recovery_attempted_at ASC NULLS FIRST, target.id ASC LIMIT $4`,
	)).WithArgs(reviewTenantID, types.ReleaseTargetBuilding, types.ParseStatusFailed, 100).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	_, err = repo.ListBuildingTargetsWithFailedKnowledge(ctx, reviewTenantID, 100)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProductionReleaseRepositoryComputesAuthoritativeDigest(t *testing.T) {
	repo, db := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	release.ReleaseDigest = ""

	require.NoError(t, repo.CreateRelease(
		productionReleaseContext(reviewTenantID, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)},
	))

	var version types.ProductionDocumentVersion
	require.NoError(t, db.First(&version, "id = ?", reviewVersionOne).Error)
	var review types.ProductionReviewRequest
	require.NoError(t, db.First(&review, "id = ?", reviewID(700)).Error)
	require.Equal(t, types.ComputeProductionReleaseDigest(release, &version, &review), release.ReleaseDigest)
	require.Equal(t, types.ProductionReleaseDigestVersionCurrent, release.ReleaseDigestVersion)
	require.NotEqual(t, strings.Repeat("a", 64), release.ReleaseDigest)
}

func TestProductionReleaseRepositoryRejectsCallerDigestMismatch(t *testing.T) {
	repo, db := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	release.ReleaseDigest = strings.Repeat("f", 64)

	err := repo.CreateRelease(
		productionReleaseContext(reviewTenantID, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)},
	)
	require.ErrorIs(t, err, types.ErrProductionContentDigestMismatch)
	var count int64
	require.NoError(t, db.Model(&types.ProductionRelease{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProductionReleaseRepositoryGetsReleaseByTenantAndHydratesTargets(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	targets := []*types.ProductionReleaseTarget{
		productionReleaseTarget(releaseTarget2, releaseKBTwo, releaseKnowledge2),
		productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1),
	}
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release, targets))

	got, err := repo.GetRelease(context.Background(), reviewTenantID, releaseIDOne)
	require.NoError(t, err)
	require.Equal(t, releaseIDOne, got.ID)
	require.Len(t, got.Targets, 2)
	require.Equal(t, releaseTarget1, got.Targets[0].ID)
	require.Equal(t, releaseTarget2, got.Targets[1].ID)
	_, err = repo.GetRelease(productionReleaseContext(8, reviewAuthorID), reviewTenantID, releaseIDOne)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionReleaseTargetFailureReasonIsBoundedSanitizedAndClearedOnRetry(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	require.NoError(t, repo.CreateRelease(
		productionReleaseContext(reviewTenantID, reviewAuthorID),
		productionRelease(releaseIDOne, reviewVersionOne, reviewID(700)),
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)},
	))

	changed, err := repo.TransitionTarget(
		productionReleaseContext(reviewTenantID, reviewAuthorID), releaseTarget1,
		types.ReleaseTargetBuilding, types.ReleaseTargetFailed,
		types.JSONMap{"failure_code": types.ProductionProjectionFailureContentDigestMismatch,
			"failure_reason": types.ProductionProjectionFailureReasonContentDigestMismatch},
	)
	require.NoError(t, err)
	require.True(t, changed)
	failed, err := repo.GetTarget(context.Background(), reviewTenantID, releaseTarget1)
	require.NoError(t, err)
	require.Equal(t, types.ProductionProjectionFailureContentDigestMismatch, failed.FailureCode)
	require.Equal(t, types.ProductionProjectionFailureReasonContentDigestMismatch, failed.FailureReason)

	changed, err = repo.TransitionTarget(
		productionReleaseContext(reviewTenantID, reviewAuthorID), releaseTarget1,
		types.ReleaseTargetFailed, types.ReleaseTargetBuilding, nil,
	)
	require.NoError(t, err)
	require.True(t, changed)
	retried, err := repo.GetTarget(context.Background(), reviewTenantID, releaseTarget1)
	require.NoError(t, err)
	require.Empty(t, retried.FailureCode)
	require.Empty(t, retried.FailureReason)

	_, err = repo.TransitionTarget(
		productionReleaseContext(reviewTenantID, reviewAuthorID), releaseTarget1,
		types.ReleaseTargetBuilding, types.ReleaseTargetFailed,
		types.JSONMap{"failure_code": "RAW_PROVIDER_ERROR", "failure_reason": "token=secret"},
	)
	require.ErrorIs(t, err, types.ErrProductionReleasePatchInvalid)
}

func TestProductionReleaseCreateRejectsCallerSuppliedFailureMetadata(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	target := productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)
	target.FailureCode = "RAW_PROVIDER_ERROR"
	target.FailureReason = "token=secret"

	err := repo.CreateRelease(
		productionReleaseContext(reviewTenantID, reviewAuthorID),
		productionRelease(releaseIDOne, reviewVersionOne, reviewID(700)),
		[]*types.ProductionReleaseTarget{target},
	)
	require.ErrorIs(t, err, types.ErrProductionReleaseInvalid)
}

func insertRawProductionReleaseTargetConfig(
	t *testing.T,
	db *gorm.DB,
	targetID, kbID, knowledgeID, snapshot, digest string,
) {
	t.Helper()
	var releaseDigest string
	require.NoError(t, db.Raw(`SELECT release_digest FROM production_releases WHERE id = ?`, releaseIDOne).Scan(&releaseDigest).Error)
	require.NoError(t, db.Exec(`INSERT INTO production_release_targets
		(id, release_id, tenant_id, project_id, document_id, version_id,
		 target_knowledge_base_id, knowledge_id, release_digest, config_snapshot, config_digest)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		targetID, releaseIDOne, reviewTenantID, reviewProjectID, reviewDocumentID, reviewVersionOne,
		kbID, knowledgeID, releaseDigest, snapshot, digest,
	).Error)
}

func TestProductionReleaseTargetReadsCanonicalizeAndVerifyPersistedConfig(t *testing.T) {
	repo, db := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))

	raw := types.JSON(`{"z":1,"a":{"size":512}}`)
	canonical, digest, err := types.CanonicalProductionReleaseTargetConfig(raw)
	require.NoError(t, err)
	require.NotEqual(t, raw, canonical)
	insertRawProductionReleaseTargetConfig(t, db, releaseTarget2, releaseKBTwo, releaseKnowledge2, string(raw), digest)

	target, err := repo.GetTarget(context.Background(), reviewTenantID, releaseTarget2)
	require.NoError(t, err)
	require.Equal(t, canonical, target.ConfigSnapshot)
	require.Equal(t, digest, target.ConfigDigest)

	history, err := repo.ListProjectionHistory(context.Background(), reviewTenantID, reviewDocumentID, releaseKBTwo)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, canonical, history[0].ConfigSnapshot)
	require.Equal(t, digest, history[0].ConfigDigest)
}

func TestProductionReleaseTargetReadsRejectPersistedConfigDigestMismatch(t *testing.T) {
	repo, db := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))
	insertRawProductionReleaseTargetConfig(t, db, releaseTarget2, releaseKBTwo, releaseKnowledge2, `{}`, strings.Repeat("f", 64))

	target, err := repo.GetTarget(context.Background(), reviewTenantID, releaseTarget2)
	require.ErrorIs(t, err, types.ErrProductionReleaseConfigInvalid)
	require.Nil(t, target)

	history, err := repo.ListProjectionHistory(context.Background(), reviewTenantID, reviewDocumentID, releaseKBTwo)
	require.ErrorIs(t, err, types.ErrProductionReleaseConfigInvalid)
	require.Nil(t, history)
}

func TestProductionReleaseGetTargetRejectsMalformedPostgresJSONBScan(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewProductionReleaseRepository(db)
	mock.ExpectQuery(`SELECT \* FROM "production_release_targets"`).
		WithArgs(reviewTenantID, releaseTarget1, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "config_snapshot", "config_digest"}).
			AddRow(releaseTarget1, reviewTenantID, []byte(`[]`), strings.Repeat("a", 64)))

	target, err := repo.GetTarget(context.Background(), reviewTenantID, releaseTarget1)
	require.ErrorIs(t, err, types.ErrProductionReleaseConfigInvalid)
	require.Nil(t, target)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProductionReleaseGetTargetCanonicalizesPostgresJSONBScan(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewProductionReleaseRepository(db)
	raw := types.JSON(`{ "z": 1, "a": { "size": 512 } }`)
	canonical, digest, err := types.CanonicalProductionReleaseTargetConfig(raw)
	require.NoError(t, err)
	mock.ExpectQuery(`SELECT \* FROM "production_release_targets"`).
		WithArgs(reviewTenantID, releaseTarget1, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "config_snapshot", "config_digest"}).
			AddRow(releaseTarget1, reviewTenantID, []byte(raw), digest))

	target, err := repo.GetTarget(context.Background(), reviewTenantID, releaseTarget1)
	require.NoError(t, err)
	require.Equal(t, canonical, target.ConfigSnapshot)
	require.Equal(t, digest, target.ConfigDigest)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProductionReleaseGetTargetRejectsNullOrEmptyPersistedConfig(t *testing.T) {
	_, emptyDigest, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(`{}`))
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		snapshot any
	}{
		{name: "null", snapshot: nil},
		{name: "empty bytes", snapshot: []byte{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqlDB, mock, mockErr := sqlmock.New()
			require.NoError(t, mockErr)
			t.Cleanup(func() { _ = sqlDB.Close() })
			db, openErr := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
			require.NoError(t, openErr)
			mock.ExpectQuery(`SELECT \* FROM "production_release_targets"`).
				WithArgs(reviewTenantID, releaseTarget1, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "config_snapshot", "config_digest"}).
					AddRow(releaseTarget1, reviewTenantID, tc.snapshot, emptyDigest))

			target, getErr := NewProductionReleaseRepository(db).GetTarget(context.Background(), reviewTenantID, releaseTarget1)
			require.ErrorIs(t, getErr, types.ErrProductionReleaseConfigInvalid)
			require.Nil(t, target)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestProductionReleaseHistoryRejectsNullOrEmptyPersistedConfig(t *testing.T) {
	_, emptyDigest, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(`{}`))
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		snapshot any
	}{
		{name: "null", snapshot: nil},
		{name: "empty bytes", snapshot: []byte{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqlDB, mock, mockErr := sqlmock.New()
			require.NoError(t, mockErr)
			t.Cleanup(func() { _ = sqlDB.Close() })
			db, openErr := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
			require.NoError(t, openErr)
			mock.ExpectQuery(`SELECT \* FROM "production_release_targets"`).
				WithArgs(reviewTenantID, reviewDocumentID, releaseKBOne).
				WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "document_id", "target_knowledge_base_id", "config_snapshot", "config_digest"}).
					AddRow(releaseTarget1, reviewTenantID, reviewDocumentID, releaseKBOne, tc.snapshot, emptyDigest))

			history, listErr := NewProductionReleaseRepository(db).ListProjectionHistory(
				context.Background(), reviewTenantID, reviewDocumentID, releaseKBOne,
			)
			require.ErrorIs(t, listErr, types.ErrProductionReleaseConfigInvalid)
			require.Nil(t, history)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestProductionReleaseTargetReadsTranslateInvalidJSONScanError(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func(*gorm.DB) error
		args []driver.Value
		row  *sqlmock.Rows
	}{
		{
			name: "get target",
			read: func(db *gorm.DB) error {
				target, err := NewProductionReleaseRepository(db).GetTarget(context.Background(), reviewTenantID, releaseTarget1)
				require.Nil(t, target)
				return err
			},
			args: []driver.Value{reviewTenantID, releaseTarget1, 1},
			row: sqlmock.NewRows([]string{"id", "tenant_id", "config_snapshot", "config_digest"}).
				AddRow(releaseTarget1, reviewTenantID, []byte(`{"broken":`), strings.Repeat("a", 64)),
		},
		{
			name: "history",
			read: func(db *gorm.DB) error {
				history, err := NewProductionReleaseRepository(db).ListProjectionHistory(
					context.Background(), reviewTenantID, reviewDocumentID, releaseKBOne,
				)
				require.Nil(t, history)
				return err
			},
			args: []driver.Value{reviewTenantID, reviewDocumentID, releaseKBOne},
			row: sqlmock.NewRows([]string{"id", "tenant_id", "document_id", "target_knowledge_base_id", "config_snapshot", "config_digest"}).
				AddRow(releaseTarget1, reviewTenantID, reviewDocumentID, releaseKBOne, []byte(`{"broken":`), strings.Repeat("a", 64)),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
				Logger: logger.Default.LogMode(logger.Silent),
			})
			require.NoError(t, err)
			mock.ExpectQuery(`SELECT \* FROM "production_release_targets"`).WithArgs(tc.args...).WillReturnRows(tc.row)

			readErr := tc.read(db)
			require.ErrorIs(t, readErr, types.ErrProductionReleaseConfigInvalid)
			require.NotContains(t, readErr.Error(), "unexpected end of JSON input")
			require.NotContains(t, readErr.Error(), "Scan error")
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestProductionReleasePostgresLiveJSONBRoundTrip(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WEKNORA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set WEKNORA_TEST_POSTGRES_DSN to run the isolated PostgreSQL JSONB round-trip test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	schemaName := fmt.Sprintf("weknora_release_config_%d", time.Now().UnixNano())
	require.NoError(t, db.Exec(`CREATE SCHEMA "`+schemaName+`"`).Error)
	t.Cleanup(func() { _ = db.Exec(`DROP SCHEMA IF EXISTS "` + schemaName + `" CASCADE`).Error })
	require.NoError(t, db.Exec(`SET search_path TO "`+schemaName+`"`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE production_release_targets (
		id TEXT PRIMARY KEY, release_id TEXT NOT NULL, tenant_id BIGINT NOT NULL,
		project_id TEXT NOT NULL, document_id TEXT NOT NULL, version_id TEXT NOT NULL,
		target_knowledge_base_id TEXT NOT NULL, knowledge_id TEXT NOT NULL,
		release_digest TEXT NOT NULL, config_snapshot JSONB NOT NULL, config_digest TEXT NOT NULL,
		status TEXT NOT NULL, retention_days INTEGER NOT NULL,
		retention_until TIMESTAMPTZ, activated_at TIMESTAMPTZ, failed_at TIMESTAMPTZ,
		rolled_back_at TIMESTAMPTZ, cleanup_requested_at TIMESTAMPTZ, cleaned_at TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL
	)`).Error)

	raw := types.JSON(`{ "z": 1, "a": { "size": 512 } }`)
	canonical, digest, err := types.CanonicalProductionReleaseTargetConfig(raw)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`INSERT INTO production_release_targets
		(id, release_id, tenant_id, project_id, document_id, version_id,
		 target_knowledge_base_id, knowledge_id, release_digest, config_snapshot, config_digest,
		 status, retention_days, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CAST(? AS JSONB), ?, ?, 30, now(), now())`,
		releaseTarget1, releaseIDOne, reviewTenantID, reviewProjectID, reviewDocumentID, reviewVersionOne,
		releaseKBOne, releaseKnowledge1, strings.Repeat("a", 64), string(raw), digest, types.ReleaseTargetBuilding,
	).Error)

	repo := NewProductionReleaseRepository(db)
	target, err := repo.GetTarget(context.Background(), reviewTenantID, releaseTarget1)
	require.NoError(t, err)
	require.Equal(t, canonical, target.ConfigSnapshot)
	history, err := repo.ListProjectionHistory(context.Background(), reviewTenantID, reviewDocumentID, releaseKBOne)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, canonical, history[0].ConfigSnapshot)
}

func TestProductionReleaseRepositoryRejectsTargetConfigDigestMismatchAndRollsBack(t *testing.T) {
	repo, db := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	target := productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)
	target.ConfigDigest = strings.Repeat("f", 64)

	err := repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{target})
	require.ErrorIs(t, err, types.ErrProductionReleaseConfigInvalid)
	var count int64
	require.NoError(t, db.Model(&types.ProductionRelease{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProductionReleaseRepositoryRollsBackReleaseWhenAnyTargetFails(t *testing.T) {
	repo, db := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	targets := []*types.ProductionReleaseTarget{
		productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1),
		productionReleaseTarget(releaseTarget2, "missing-kb", releaseKnowledge2),
	}
	err := repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release, targets)
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&types.ProductionRelease{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProductionReleaseRepositoryRejectsUntrustedTenantAndActor(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	release.CreatedBy = reviewBusinessActor
	err := repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)})
	require.ErrorIs(t, err, types.ErrProductionForbidden)

	release.CreatedBy = ""
	err = repo.CreateRelease(productionReleaseContext(reviewTenantID+1, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)})
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionReleaseTargetTransitionIsScopedAndCannotBypassHeadOrTrustedTime(t *testing.T) {
	now := time.Date(2026, 7, 21, 4, 5, 6, 987654321, time.UTC)
	ownedNow := now.Truncate(time.Second)
	repo, _ := newProductionReleaseRepoFixture(t, &productionReleaseTestClock{current: now})
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))

	changed, err := repo.TransitionTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), releaseTarget1,
		types.ReleaseTargetBuilding, types.ReleaseTargetActive, nil)
	require.ErrorIs(t, err, types.ErrProductionReleaseLifecycle)
	require.False(t, changed)
	changed, err = repo.TransitionTarget(productionReleaseContext(reviewTenantID+1, reviewAuthorID), releaseTarget1,
		types.ReleaseTargetBuilding, types.ReleaseTargetReady, nil)
	require.NoError(t, err)
	require.False(t, changed)
	changed, err = repo.TransitionTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), releaseTarget1,
		types.ReleaseTargetBuilding, types.ReleaseTargetFailed, types.JSONMap{"failed_at": now.Add(-time.Hour)})
	require.ErrorIs(t, err, types.ErrProductionReleasePatchInvalid)
	require.False(t, changed)
	changed, err = repo.TransitionTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), releaseTarget1,
		types.ReleaseTargetBuilding, types.ReleaseTargetFailed, nil)
	require.NoError(t, err)
	require.True(t, changed)
	target, err := repo.GetTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, releaseTarget1)
	require.NoError(t, err)
	require.Equal(t, ownedNow, *target.FailedAt)
	require.Equal(t, ownedNow.Add(30*24*time.Hour), *target.RetentionUntil)
}

func TestProductionProjectionHeadInitialActivationAndCASSwitchRefreshTriggerState(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	first := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	second := productionRelease(releaseIDTwo, reviewVersionTwo, reviewID(710))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), first,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), second,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget2, releaseKBOne, releaseKnowledge2)}))
	for _, targetID := range []string{releaseTarget1, releaseTarget2} {
		changed, err := repo.TransitionTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), targetID,
			types.ReleaseTargetBuilding, types.ReleaseTargetReady, nil)
		require.NoError(t, err)
		require.True(t, changed)
	}

	head, err := repo.SwitchHead(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID,
		reviewDocumentID, releaseKBOne, releaseTarget1, 0)
	require.NoError(t, err)
	require.Equal(t, 1, head.LockVersion)
	firstTarget, err := repo.GetTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, releaseTarget1)
	require.NoError(t, err)
	require.Equal(t, types.ReleaseTargetActive, firstTarget.Status)

	head, err = repo.SwitchHead(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID,
		reviewDocumentID, releaseKBOne, releaseTarget2, 1)
	require.NoError(t, err)
	require.Equal(t, 2, head.LockVersion)
	require.Equal(t, releaseTarget2, head.ActiveReleaseTargetID)
	firstTarget, err = repo.GetTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, releaseTarget1)
	require.NoError(t, err)
	secondTarget, err := repo.GetTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, releaseTarget2)
	require.NoError(t, err)
	require.Equal(t, types.ReleaseTargetRolledBack, firstTarget.Status)
	require.NotNil(t, firstTarget.RetentionUntil)
	require.Equal(t, types.ReleaseTargetActive, secondTarget.Status)
}

func TestProductionReleaseRepositoryAllowsSupersedingSameApprovedVersion(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	first := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	second := productionRelease(releaseIDTwo, reviewVersionOne, reviewID(700))
	second.SupersedesReleaseID = &first.ID
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), first,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))

	err := repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), second,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget2, releaseKBOne, releaseKnowledge2)})
	require.NoError(t, err)
	latest, err := repo.GetLatestReleaseForVersion(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, reviewDocumentID, reviewVersionOne)
	require.NoError(t, err)
	require.Equal(t, second.ID, latest.ID)
	require.Equal(t, first.ID, *latest.SupersedesReleaseID)
}

func TestProductionReleaseRepositoryRejectsForkedSuccessors(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	root := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	firstSuccessor := productionRelease(releaseIDTwo, reviewVersionOne, reviewID(700))
	secondSuccessor := productionRelease("30000000-0000-4000-8000-00000000000b", reviewVersionOne, reviewID(700))
	firstSuccessor.SupersedesReleaseID = &root.ID
	secondSuccessor.SupersedesReleaseID = &root.ID
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), root,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), firstSuccessor,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget2, releaseKBOne, releaseKnowledge2)}))

	err := repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), secondSuccessor,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget3, releaseKBOne, releaseKnowledge3)})
	require.ErrorIs(t, err, types.ErrProductionConflict)
}

func TestProductionReleaseRepositoryConcurrentSuccessorHasOneWinner(t *testing.T) {
	const callers = 16
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	root := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), root,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))

	start := make(chan struct{})
	results := make(chan error, callers)
	for index := range callers {
		go func() {
			<-start
			release := productionRelease(fmt.Sprintf("release-successor-%02d", index), reviewVersionOne, reviewID(700))
			release.SupersedesReleaseID = &root.ID
			target := productionReleaseTarget(
				fmt.Sprintf("target-successor-%02d", index), releaseKBOne, fmt.Sprintf("knowledge-successor-%02d", index),
			)
			results <- repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release,
				[]*types.ProductionReleaseTarget{target})
		}()
	}
	close(start)

	successes := 0
	for range callers {
		err := <-results
		if err == nil {
			successes++
			continue
		}
		require.ErrorIs(t, err, types.ErrProductionConflict)
	}
	require.Equal(t, 1, successes)
	latest, err := repo.GetLatestReleaseForVersion(
		productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, reviewDocumentID, reviewVersionOne,
	)
	require.NoError(t, err)
	require.NotNil(t, latest.SupersedesReleaseID)
	require.Equal(t, root.ID, *latest.SupersedesReleaseID)
}

func TestProductionProjectionHeadRejectsStaleLockAndPreservesCurrentHead(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	first := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	second := productionRelease(releaseIDTwo, reviewVersionTwo, reviewID(710))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), first,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), second,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget2, releaseKBOne, releaseKnowledge2)}))
	for _, targetID := range []string{releaseTarget1, releaseTarget2} {
		changed, err := repo.TransitionTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), targetID, types.ReleaseTargetBuilding, types.ReleaseTargetReady, nil)
		require.NoError(t, err)
		require.True(t, changed)
	}
	_, err := repo.SwitchHead(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, reviewDocumentID, releaseKBOne, releaseTarget1, 0)
	require.NoError(t, err)
	_, err = repo.SwitchHead(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, reviewDocumentID, releaseKBOne, releaseTarget2, 0)
	require.ErrorIs(t, err, types.ErrProductionProjectionConflict)
	scope, err := repo.ResolveScopes(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, []string{releaseKBOne})
	require.NoError(t, err)
	require.Equal(t, []string{releaseKnowledge1}, scope[releaseKBOne].ActiveKnowledgeIDs)
}

func TestProductionProjectionHeadConcurrentCASHasSingleWinner(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	releases := []*types.ProductionRelease{
		productionRelease(releaseIDOne, reviewVersionOne, reviewID(700)),
		productionRelease(releaseIDTwo, reviewVersionTwo, reviewID(710)),
	}
	targetIDs := []string{releaseTarget1, releaseTarget2}
	knowledgeIDs := []string{releaseKnowledge1, releaseKnowledge2}
	for index := range releases {
		require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), releases[index],
			[]*types.ProductionReleaseTarget{productionReleaseTarget(targetIDs[index], releaseKBOne, knowledgeIDs[index])}))
		changed, err := repo.TransitionTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), targetIDs[index], types.ReleaseTargetBuilding, types.ReleaseTargetReady, nil)
		require.NoError(t, err)
		require.True(t, changed)
	}
	_, err := repo.SwitchHead(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, reviewDocumentID, releaseKBOne, releaseTarget1, 0)
	require.NoError(t, err)

	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, candidate := range []string{releaseTarget1, releaseTarget2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, switchErr := repo.SwitchHead(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, reviewDocumentID, releaseKBOne, candidate, 1)
			results <- switchErr
		}()
	}
	wg.Wait()
	close(results)
	var successes int
	for switchErr := range results {
		if switchErr == nil {
			successes++
		} else {
			require.ErrorIs(t, switchErr, types.ErrProductionProjectionConflict)
		}
	}
	require.Equal(t, 1, successes)
}

func TestProductionReleaseResolveScopesClassifiesRequestedKBsDeterministically(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{
			productionReleaseTarget(releaseTarget2, releaseKBOne, releaseKnowledge2),
			productionReleaseTarget(releaseTarget1, releaseKBTwo, releaseKnowledge1),
		}))
	changed, err := repo.TransitionTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), releaseTarget2, types.ReleaseTargetBuilding, types.ReleaseTargetReady, nil)
	require.NoError(t, err)
	require.True(t, changed)
	_, err = repo.SwitchHead(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, reviewDocumentID, releaseKBOne, releaseTarget2, 0)
	require.NoError(t, err)

	scopes, err := repo.ResolveScopes(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, []string{releaseKBTwo, releaseKBOne, releaseKBOne, "missing-kb"})
	require.NoError(t, err)
	require.Len(t, scopes, 3)
	require.Equal(t, []string{releaseKnowledge2}, scopes[releaseKBOne].ActiveKnowledgeIDs)
	require.Empty(t, scopes[releaseKBOne].InactiveKnowledgeIDs)
	require.Equal(t, []string{releaseKnowledge2}, scopes[releaseKBOne].AllProductionKnowledgeIDs)
	require.Empty(t, scopes[releaseKBTwo].ActiveKnowledgeIDs)
	require.Equal(t, []string{releaseKnowledge1}, scopes[releaseKBTwo].InactiveKnowledgeIDs)
	require.Equal(t, []string{releaseKnowledge1}, scopes[releaseKBTwo].AllProductionKnowledgeIDs)
	require.Empty(t, scopes["missing-kb"].AllProductionKnowledgeIDs)
}

func TestProductionReleaseResolveScopesForKnowledgeIDsIsBounded(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1), productionReleaseTarget(releaseTarget2, releaseKBTwo, releaseKnowledge2)}))

	scopes, err := repo.ResolveScopesForKnowledgeIDs(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, []string{releaseKnowledge1})
	require.NoError(t, err)
	require.Len(t, scopes, 1)
	require.NotContains(t, scopes, releaseKBTwo)
	require.Equal(t, []string{releaseKnowledge1}, scopes[releaseKBOne].InactiveKnowledgeIDs)
}

func TestProductionReleaseHistoryIsTenantScopedAndDeterministic(t *testing.T) {
	clock := &productionReleaseTestClock{current: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)}
	repo, _ := newProductionReleaseRepoFixture(t, clock)
	first := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	second := productionRelease(releaseIDTwo, reviewVersionTwo, reviewID(710))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), first,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))
	clock.current = time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), second,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget2, releaseKBOne, releaseKnowledge2)}))

	history, err := repo.ListProjectionHistory(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, reviewDocumentID, releaseKBOne)
	require.NoError(t, err)
	require.Equal(t, []string{releaseTarget2, releaseTarget1}, []string{history[0].ID, history[1].ID})
	_, err = repo.ListProjectionHistory(productionReleaseContext(reviewTenantID+1, reviewAuthorID), reviewTenantID, reviewDocumentID, releaseKBOne)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionReleaseRepositoryJoinsExistingUnitOfWorkTransaction(t *testing.T) {
	repo, db := newProductionReleaseRepoFixture(t, nil)
	uow := NewProductionUnitOfWork(db)
	errRollback := context.Canceled
	err := uow.WithinTransaction(productionReleaseContext(reviewTenantID, reviewAuthorID), func(txCtx context.Context) error {
		release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
		require.NoError(t, repo.CreateRelease(txCtx, release,
			[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)
	var count int64
	require.NoError(t, db.Model(&types.ProductionRelease{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProductionProjectionHeadPostgresUsesExactCASPredicate(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewProductionReleaseRepository(db)
	ctx := productionReleaseContext(reviewTenantID, reviewAuthorID)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE production_projection_heads`).
		WithArgs(releaseTarget2, sqlmock.AnyArg(), reviewTenantID, reviewDocumentID, releaseKBOne, 7).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	_, err = repo.SwitchHead(ctx, reviewTenantID, reviewDocumentID, releaseKBOne, releaseTarget2, 7)
	require.ErrorIs(t, err, types.ErrProductionProjectionConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}
