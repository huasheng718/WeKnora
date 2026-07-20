package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	reviewTenantID         = uint64(7)
	reviewProjectID        = "10000000-0000-4000-8000-000000000001"
	reviewTypeID           = "10000000-0000-4000-8000-000000000002"
	reviewSourceID         = "10000000-0000-4000-8000-000000000003"
	reviewDocumentID       = "10000000-0000-4000-8000-000000000004"
	reviewVersionOne       = "10000000-0000-4000-8000-000000000005"
	reviewBlockOne         = "10000000-0000-4000-8000-000000000006"
	reviewVersionTwo       = "10000000-0000-4000-8000-000000000007"
	reviewBlockTwo         = "10000000-0000-4000-8000-000000000008"
	reviewBusinessActor    = "10000000-0000-4000-8000-000000000009"
	reviewEngineeringActor = "10000000-0000-4000-8000-00000000000a"
	reviewAuthorID         = "10000000-0000-4000-8000-00000000000b"
)

func reviewID(value int) string {
	return fmt.Sprintf("20000000-0000-4000-8000-%012x", value)
}

func newProductionReviewRepoFixture(t *testing.T) (interfaces.ProductionReviewRepository, *gorm.DB) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "reviews.db") +
		"?_foreign_keys=1&_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })

	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	for _, name := range []string{
		"000001_knowledge_production_foundation.up.sql",
		"000002_knowledge_production_documents.up.sql",
		"000004_knowledge_production_reviews.up.sql",
	} {
		migration, readErr := os.ReadFile(filepath.Join(filepath.Dir(filename), "../../../migrations/sqlite", name))
		require.NoError(t, readErr)
		require.NoError(t, db.Exec(string(migration)).Error)
	}
	seedProductionReviewScope(t, db)
	return NewProductionReviewRepository(db), db
}

func seedProductionReviewScope(t *testing.T, db *gorm.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO production_projects (id, tenant_id, name, owner_user_id) VALUES (?, ?, 'Reviews', ?)`,
		`INSERT INTO production_document_types (id, tenant_id, code, name, schema_version, status, created_by) VALUES (?, ?, 'review', 'Review', 1, 'active', ?)`,
		`INSERT INTO production_source_sets (id, tenant_id, project_id, document_type_id, status, created_by, frozen_at) VALUES (?, ?, ?, ?, 'frozen', ?, CURRENT_TIMESTAMP)`,
		`INSERT INTO production_documents (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, current_version_id, status, created_by) VALUES (?, ?, ?, ?, 1, 'Governed', NULL, 'draft', ?)`,
		`INSERT INTO production_document_versions (id, document_id, tenant_id, project_id, version_number, source_set_id, origin, content_digest, created_by, frozen_at) VALUES (?, ?, ?, ?, 1, ?, 'human', ?, ?, CURRENT_TIMESTAMP)`,
		`INSERT INTO production_document_blocks (id, version_id, logical_block_id, block_type, position, content, attributes, evidence_refs, ai_provenance, content_digest) VALUES (?, ?, ?, 'fact', 1, '{}', '{}', '[]', '{}', ?)`,
		`INSERT INTO production_document_versions (id, document_id, tenant_id, project_id, version_number, parent_version_id, source_set_id, origin, content_digest, created_by, frozen_at) VALUES (?, ?, ?, ?, 2, ?, ?, 'human', ?, ?, CURRENT_TIMESTAMP)`,
		`INSERT INTO production_document_blocks (id, version_id, logical_block_id, block_type, position, content, attributes, evidence_refs, ai_provenance, content_digest) VALUES (?, ?, ?, 'fact', 1, '{}', '{}', '[]', '{}', ?)`,
		`UPDATE production_documents SET current_version_id = ? WHERE id = ?`,
		`INSERT INTO production_project_members (project_id, user_id, role, assigned_by) VALUES (?, ?, 'business_reviewer', ?)`,
		`INSERT INTO production_project_members (project_id, user_id, role, assigned_by) VALUES (?, ?, 'engineering_reviewer', ?)`,
	}
	args := [][]any{
		{reviewProjectID, reviewTenantID, reviewAuthorID},
		{reviewTypeID, reviewTenantID, reviewAuthorID},
		{reviewSourceID, reviewTenantID, reviewProjectID, reviewTypeID, reviewAuthorID},
		{reviewDocumentID, reviewTenantID, reviewProjectID, reviewTypeID, reviewAuthorID},
		{reviewVersionOne, reviewDocumentID, reviewTenantID, reviewProjectID, reviewSourceID, strings.Repeat("a", 64), reviewAuthorID},
		{reviewBlockOne, reviewVersionOne, reviewID(900), strings.Repeat("b", 64)},
		{reviewVersionTwo, reviewDocumentID, reviewTenantID, reviewProjectID, reviewVersionOne, reviewSourceID, strings.Repeat("c", 64), reviewAuthorID},
		{reviewBlockTwo, reviewVersionTwo, reviewID(901), strings.Repeat("d", 64)},
		{reviewVersionTwo, reviewDocumentID},
		{reviewProjectID, reviewBusinessActor, reviewAuthorID},
		{reviewProjectID, reviewEngineeringActor, reviewAuthorID},
	}
	for index := range statements {
		require.NoError(t, db.Exec(statements[index], args[index]...).Error)
	}
}

func productionReviewRequest(id, versionID string) *types.ProductionReviewRequest {
	policy := types.JSON(`{ "roles": ["business_reviewer", "engineering_reviewer"], "n": 1.0 }`)
	_, digest, _ := types.CanonicalProductionReviewPolicy(policy)
	return &types.ProductionReviewRequest{
		ID: id, TenantID: reviewTenantID, ProjectID: reviewProjectID,
		DocumentID: reviewDocumentID, VersionID: versionID,
		PolicySnapshot: policy, PolicyDigest: digest,
		Status: types.ProductionReviewPending, SubmittedBy: reviewAuthorID,
	}
}

func productionReviewSteps(requestID string, start int) []*types.ProductionReviewStep {
	return []*types.ProductionReviewStep{
		{ID: reviewID(start + 1), ReviewRequestID: requestID, RequiredRole: types.ProductionRoleBusinessReviewer, Sequence: 1, Decision: types.ProductionReviewPending},
		{ID: reviewID(start + 2), ReviewRequestID: requestID, RequiredRole: types.ProductionRoleEngineeringReviewer, Sequence: 2, Decision: types.ProductionReviewPending},
	}
}

func TestProductionReviewRepositoryCreatesAndResolvesNormalizedAnnotation(t *testing.T) {
	repo, db := newProductionReviewRepoFixture(t)
	category := types.ProductionQualityTagMissingEvidence
	annotation := &types.ProductionAnnotation{
		ID: reviewID(1), TenantID: reviewTenantID, ProjectID: reviewProjectID,
		DocumentID: reviewDocumentID, VersionID: reviewVersionOne, BlockID: reviewBlockOne,
		AnnotationType: types.ProductionAnnotationQualityTag, QualityTag: &category,
		Severity: types.ProductionAnnotationBlocking, Anchor: types.JSON(`{ "path": "/title", "offset": 1.0 }`),
		Body: "needs evidence", Status: types.ProductionAnnotationOpen, CreatedBy: reviewAuthorID,
	}
	require.NoError(t, repo.CreateAnnotation(context.Background(), annotation))
	require.Equal(t, types.JSON(`{"offset":1,"path":"/title"}`), annotation.Anchor)
	var anchorStorageType string
	require.NoError(t, db.Raw(`SELECT typeof(anchor) FROM production_annotations WHERE id = ?`, annotation.ID).Scan(&anchorStorageType).Error)
	require.Equal(t, "text", anchorStorageType)

	count, err := repo.CountOpenBlocking(context.Background(), reviewTenantID, reviewVersionOne)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	count, err = repo.CountOpenBlocking(context.Background(), reviewTenantID+1, reviewVersionOne)
	require.NoError(t, err)
	require.Zero(t, count)

	ok, err := repo.ResolveAnnotation(context.Background(), reviewTenantID, annotation.ID, reviewBusinessActor, types.ProductionAnnotationResolved)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = repo.ResolveAnnotation(context.Background(), reviewTenantID, annotation.ID, reviewEngineeringActor, types.ProductionAnnotationDismissed)
	require.NoError(t, err)
	require.False(t, ok)

	var persisted types.ProductionAnnotation
	require.NoError(t, db.First(&persisted, "id = ?", annotation.ID).Error)
	require.Equal(t, reviewAuthorID, persisted.CreatedBy)
	require.Equal(t, types.ProductionAnnotationResolved, persisted.Status)
	require.NotNil(t, persisted.ResolvedBy)
	require.Equal(t, reviewBusinessActor, *persisted.ResolvedBy)
	require.NotNil(t, persisted.ResolvedAt)
}

func TestProductionReviewPostgresVersionLockScopesExactImmutableVersion(t *testing.T) {
	upper := strings.ToUpper(postgresProductionReviewLockSQL)
	for _, fragment := range []string{"FROM PRODUCTION_DOCUMENT_VERSIONS", "ID = ?", "DOCUMENT_ID = ?", "TENANT_ID = ?", "PROJECT_ID = ?", "FOR UPDATE"} {
		require.Contains(t, upper, fragment)
	}
}

func TestProductionReviewRepositoryRejectsInvalidAnnotationScopeAndLifecycle(t *testing.T) {
	repo, db := newProductionReviewRepoFixture(t)
	annotation := &types.ProductionAnnotation{
		ID: reviewID(10), TenantID: reviewTenantID, ProjectID: reviewProjectID,
		DocumentID: reviewDocumentID, VersionID: reviewVersionOne, BlockID: reviewBlockTwo,
		AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationInfo,
		Anchor: types.JSON(`{}`), Body: "cross version", Status: types.ProductionAnnotationOpen, CreatedBy: reviewAuthorID,
	}
	err := repo.CreateAnnotation(context.Background(), annotation)
	require.ErrorIs(t, err, types.ErrProductionAnnotationAnchorInvalid)

	annotation.ID = reviewID(11)
	annotation.BlockID = reviewBlockOne
	annotation.Status = types.ProductionAnnotationResolved
	err = repo.CreateAnnotation(context.Background(), annotation)
	require.ErrorIs(t, err, types.ErrProductionAnnotationLifecycle)
	ok, err := repo.ResolveAnnotation(context.Background(), reviewTenantID, reviewID(999), reviewBusinessActor, types.ProductionAnnotationOpen)
	require.ErrorIs(t, err, types.ErrProductionAnnotationLifecycle)
	require.False(t, ok)

	var count int64
	require.NoError(t, db.Model(&types.ProductionAnnotation{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProductionReviewRepositoryCreatesCanonicalReviewAtomicallyAndHydratesOrderedSteps(t *testing.T) {
	repo, _ := newProductionReviewRepoFixture(t)
	request := productionReviewRequest(reviewID(20), reviewVersionOne)
	steps := productionReviewSteps(request.ID, 20)
	steps[0], steps[1] = steps[1], steps[0]

	require.NoError(t, repo.CreateReview(context.Background(), request, steps))
	require.Equal(t, types.JSON(`{"n":1,"roles":["business_reviewer","engineering_reviewer"]}`), request.PolicySnapshot)
	_, digest, err := types.CanonicalProductionReviewPolicy(request.PolicySnapshot)
	require.NoError(t, err)
	require.Equal(t, digest, request.PolicyDigest)

	got, err := repo.GetReview(context.Background(), reviewTenantID, request.ID)
	require.NoError(t, err)
	require.Len(t, got.Steps, 2)
	require.Equal(t, 1, got.Steps[0].Sequence)
	require.Equal(t, 2, got.Steps[1].Sequence)
	require.Equal(t, request.ID, got.Steps[0].ReviewRequestID)
	require.Equal(t, reviewTenantID, got.Steps[0].TenantID)
	require.Equal(t, reviewProjectID, got.Steps[0].ProjectID)
	require.Equal(t, reviewDocumentID, got.Steps[0].DocumentID)
	require.Equal(t, reviewVersionOne, got.Steps[0].VersionID)

	otherTenant, err := repo.GetReview(context.Background(), reviewTenantID+1, request.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Nil(t, otherTenant)
}

func TestProductionReviewRepositoryRejectsPolicyDigestMismatchAndRollsBackSteps(t *testing.T) {
	repo, db := newProductionReviewRepoFixture(t)
	request := productionReviewRequest(reviewID(30), reviewVersionOne)
	request.PolicyDigest = strings.Repeat("f", 64)
	err := repo.CreateReview(context.Background(), request, productionReviewSteps(request.ID, 30))
	require.ErrorIs(t, err, types.ErrProductionReviewPolicyInvalid)

	request = productionReviewRequest(reviewID(31), reviewVersionOne)
	steps := productionReviewSteps(request.ID, 31)
	require.NoError(t, db.Exec(`CREATE TRIGGER fail_review_step_insert BEFORE INSERT ON production_review_steps BEGIN SELECT RAISE(ABORT, 'forced review step failure'); END`).Error)
	err = repo.CreateReview(context.Background(), request, steps)
	require.ErrorContains(t, err, "forced review step failure")
	var requests, persistedSteps int64
	require.NoError(t, db.Model(&types.ProductionReviewRequest{}).Count(&requests).Error)
	require.NoError(t, db.Model(&types.ProductionReviewStep{}).Count(&persistedSteps).Error)
	require.Zero(t, requests)
	require.Zero(t, persistedSteps)
}

func TestProductionReviewRepositoryRejectsStepScopeSequenceRoleAndInitialState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]*types.ProductionReviewStep)
	}{
		{name: "scope", mutate: func(steps []*types.ProductionReviewStep) { steps[0].VersionID = reviewVersionTwo }},
		{name: "sequence gap", mutate: func(steps []*types.ProductionReviewStep) { steps[1].Sequence = 3 }},
		{name: "duplicate role", mutate: func(steps []*types.ProductionReviewStep) {
			steps[1].RequiredRole = types.ProductionRoleBusinessReviewer
		}},
		{name: "non reviewer role", mutate: func(steps []*types.ProductionReviewStep) { steps[1].RequiredRole = types.ProductionRoleAuthor }},
		{name: "terminal insert", mutate: func(steps []*types.ProductionReviewStep) { steps[0].Decision = types.ProductionReviewApproved }},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, db := newProductionReviewRepoFixture(t)
			request := productionReviewRequest(reviewID(100+index*10), reviewVersionOne)
			steps := productionReviewSteps(request.ID, 100+index*10)
			test.mutate(steps)
			err := repo.CreateReview(context.Background(), request, steps)
			require.Error(t, err)
			var count int64
			require.NoError(t, db.Model(&types.ProductionReviewRequest{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestProductionReviewRepositoryDecisionIsTenantScopedCompareAndSwap(t *testing.T) {
	repo, _ := newProductionReviewRepoFixture(t)
	request := productionReviewRequest(reviewID(200), reviewVersionOne)
	steps := productionReviewSteps(request.ID, 200)
	require.NoError(t, repo.CreateReview(context.Background(), request, steps))

	ok, err := repo.DecideStep(context.Background(), reviewTenantID+1, steps[0].ID,
		types.ProductionReviewPending, types.ProductionReviewApproved, reviewBusinessActor, "ok")
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = repo.DecideStep(context.Background(), reviewTenantID, steps[0].ID,
		types.ProductionReviewPending, types.ProductionReviewApproved, reviewBusinessActor, "ok")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = repo.DecideStep(context.Background(), reviewTenantID, steps[0].ID,
		types.ProductionReviewPending, types.ProductionReviewRejected, reviewBusinessActor, "again")
	require.NoError(t, err)
	require.False(t, ok)

	step, err := repo.GetReviewStep(context.Background(), reviewTenantID, steps[0].ID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionReviewDecision(types.ProductionReviewApproved), step.Decision)
	require.NotNil(t, step.ReviewerUserID)
	require.Equal(t, reviewBusinessActor, *step.ReviewerUserID)
}

func TestProductionReviewRepositoryConcurrentDecisionHasSingleWinner(t *testing.T) {
	repo, _ := newProductionReviewRepoFixture(t)
	request := productionReviewRequest(reviewID(300), reviewVersionOne)
	steps := productionReviewSteps(request.ID, 300)
	require.NoError(t, repo.CreateReview(context.Background(), request, steps))

	var winners atomic.Int32
	var wg sync.WaitGroup
	for _, decision := range []types.ProductionReviewDecision{types.ProductionReviewApproved, types.ProductionReviewRejected} {
		wg.Add(1)
		go func(decision types.ProductionReviewDecision) {
			defer wg.Done()
			ok, err := repo.DecideStep(context.Background(), reviewTenantID, steps[0].ID,
				types.ProductionReviewPending, decision, reviewBusinessActor, "race")
			if err == nil && ok {
				winners.Add(1)
			}
		}(decision)
	}
	wg.Wait()
	require.Equal(t, int32(1), winners.Load())
}

func TestProductionReviewRepositoryConcurrentDuplicateReviewHasSingleWinner(t *testing.T) {
	repo, db := newProductionReviewRepoFixture(t)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			request := productionReviewRequest(reviewID(400+index*10), reviewVersionOne)
			if err := repo.CreateReview(context.Background(), request, productionReviewSteps(request.ID, 400+index*10)); err == nil {
				winners.Add(1)
			}
		}(index)
	}
	wg.Wait()
	require.Equal(t, int32(1), winners.Load())
	var requests, steps int64
	require.NoError(t, db.Model(&types.ProductionReviewRequest{}).Count(&requests).Error)
	require.NoError(t, db.Model(&types.ProductionReviewStep{}).Count(&steps).Error)
	require.Equal(t, int64(1), requests)
	require.Equal(t, int64(2), steps)
}

func TestProductionReviewRepositoryObsoletesOnlyPendingOtherVersions(t *testing.T) {
	repo, _ := newProductionReviewRepoFixture(t)
	first := productionReviewRequest(reviewID(500), reviewVersionOne)
	firstSteps := productionReviewSteps(first.ID, 500)
	second := productionReviewRequest(reviewID(510), reviewVersionTwo)
	require.NoError(t, repo.CreateReview(context.Background(), first, firstSteps))
	require.NoError(t, repo.CreateReview(context.Background(), second, productionReviewSteps(second.ID, 510)))

	require.NoError(t, repo.ObsoletePendingByDocument(context.Background(), reviewTenantID, reviewDocumentID, reviewVersionTwo))
	obsolete, err := repo.GetReview(context.Background(), reviewTenantID, first.ID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewObsolete), obsolete.Status)
	require.NotNil(t, obsolete.TerminalBy)
	require.Equal(t, types.ProductionSystemActorID, *obsolete.TerminalBy)
	for _, step := range obsolete.Steps {
		require.Equal(t, types.ProductionReviewDecision(types.ProductionReviewCancelled), step.Decision)
		require.Nil(t, step.ReviewerUserID)
		require.NotNil(t, step.DecidedAt)
	}
	kept, err := repo.GetReview(context.Background(), reviewTenantID, second.ID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewPending), kept.Status)
}

func TestProductionReviewRepositoryTransitionsAggregateAndJoinsSharedTransaction(t *testing.T) {
	repo, db := newProductionReviewRepoFixture(t)
	request := productionReviewRequest(reviewID(600), reviewVersionOne)
	steps := productionReviewSteps(request.ID, 600)
	require.NoError(t, repo.CreateReview(context.Background(), request, steps))
	for index, actor := range []string{reviewBusinessActor, reviewEngineeringActor} {
		ok, err := repo.DecideStep(context.Background(), reviewTenantID, steps[index].ID,
			types.ProductionReviewPending, types.ProductionReviewApproved, actor, "ok")
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, err := repo.TransitionReview(context.Background(), reviewTenantID, request.ID,
		types.ProductionReviewPending, types.ProductionReviewApproved, reviewEngineeringActor, "")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = repo.TransitionReview(context.Background(), reviewTenantID, request.ID,
		types.ProductionReviewPending, types.ProductionReviewRejected, reviewEngineeringActor, "late")
	require.NoError(t, err)
	require.False(t, ok)

	rollbackRequest := productionReviewRequest(reviewID(610), reviewVersionTwo)
	err = database.WithTransactionContext(context.Background(), db, func(txCtx context.Context) error {
		require.NoError(t, repo.CreateReview(txCtx, rollbackRequest, productionReviewSteps(rollbackRequest.ID, 610)))
		return fmt.Errorf("force outer rollback")
	})
	require.ErrorContains(t, err, "force outer rollback")
	_, err = repo.GetReview(context.Background(), reviewTenantID, rollbackRequest.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
