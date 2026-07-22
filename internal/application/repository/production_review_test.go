package repository

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	reviewTenantID              = uint64(7)
	reviewProjectID             = "10000000-0000-4000-8000-000000000001"
	reviewTypeID                = "10000000-0000-4000-8000-000000000002"
	reviewSourceID              = "10000000-0000-4000-8000-000000000003"
	reviewDocumentID            = "10000000-0000-4000-8000-000000000004"
	reviewVersionOne            = "10000000-0000-4000-8000-000000000005"
	reviewBlockOne              = "10000000-0000-4000-8000-000000000006"
	reviewVersionTwo            = "10000000-0000-4000-8000-000000000007"
	reviewBlockTwo              = "10000000-0000-4000-8000-000000000008"
	reviewBusinessActor         = "10000000-0000-4000-8000-000000000009"
	reviewEngineeringActor      = "10000000-0000-4000-8000-00000000000a"
	reviewAuthorID              = "10000000-0000-4000-8000-00000000000b"
	reviewOtherDocumentID       = "10000000-0000-4000-8000-00000000000c"
	reviewOtherVersionID        = "10000000-0000-4000-8000-00000000000d"
	reviewTenantEightProjectID  = "10000000-0000-4000-8000-00000000000e"
	reviewTenantEightTypeID     = "10000000-0000-4000-8000-00000000000f"
	reviewTenantEightSourceID   = "10000000-0000-4000-8000-000000000010"
	reviewTenantEightDocumentID = "10000000-0000-4000-8000-000000000011"
	reviewTenantEightVersionID  = "10000000-0000-4000-8000-000000000012"
)

func reviewID(value int) string {
	return fmt.Sprintf("20000000-0000-4000-8000-%012x", value)
}

func productionReviewContext(tenantID uint64, actorID string) context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenantID)
	return context.WithValue(ctx, types.UserIDContextKey, actorID)
}

func productionReviewTenantContext(tenantID uint64) context.Context {
	return context.WithValue(context.Background(), types.TenantIDContextKey, tenantID)
}

func productionReviewRepositoryNestedObject(depth int) types.JSON {
	value := "0"
	for range depth {
		value = `{"x":` + value + `}`
	}
	return types.JSON(value)
}

type productionReviewTestClock struct{ current time.Time }

func (c *productionReviewTestClock) Now() time.Time { return c.current }

func newProductionReviewRepoFixtureWithClock(
	t *testing.T,
	clock ProductionReviewClock,
) (interfaces.ProductionReviewRepository, *gorm.DB) {
	t.Helper()
	_, db := newProductionReviewRepoFixture(t)
	return NewProductionReviewRepositoryWithClock(db, clock), db
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
	require.NoError(t, db.AutoMigrate(&types.TenantMember{}))
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
		`INSERT INTO production_project_members (project_id, user_id, role, assigned_by) VALUES (?, ?, 'author', ?)`,
		`INSERT INTO production_documents (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, current_version_id, status, created_by) VALUES (?, ?, ?, ?, 1, 'Other', NULL, 'draft', ?)`,
		`INSERT INTO production_document_versions (id, document_id, tenant_id, project_id, version_number, source_set_id, origin, content_digest, created_by, frozen_at) VALUES (?, ?, ?, ?, 1, ?, 'human', ?, ?, CURRENT_TIMESTAMP)`,
		`INSERT INTO production_projects (id, tenant_id, name, owner_user_id) VALUES (?, 8, 'Other tenant', ?)`,
		`INSERT INTO production_document_types (id, tenant_id, code, name, schema_version, status, created_by) VALUES (?, 8, 'review', 'Review', 1, 'active', ?)`,
		`INSERT INTO production_source_sets (id, tenant_id, project_id, document_type_id, status, created_by, frozen_at) VALUES (?, 8, ?, ?, 'frozen', ?, CURRENT_TIMESTAMP)`,
		`INSERT INTO production_documents (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, current_version_id, status, created_by) VALUES (?, 8, ?, ?, 1, 'Other tenant', NULL, 'draft', ?)`,
		`INSERT INTO production_document_versions (id, document_id, tenant_id, project_id, version_number, source_set_id, origin, content_digest, created_by, frozen_at) VALUES (?, ?, 8, ?, 1, ?, 'human', ?, ?, CURRENT_TIMESTAMP)`,
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
		{reviewProjectID, reviewAuthorID, reviewAuthorID},
		{reviewOtherDocumentID, reviewTenantID, reviewProjectID, reviewTypeID, reviewAuthorID},
		{reviewOtherVersionID, reviewOtherDocumentID, reviewTenantID, reviewProjectID, reviewSourceID, strings.Repeat("e", 64), reviewAuthorID},
		{reviewTenantEightProjectID, reviewAuthorID},
		{reviewTenantEightTypeID, reviewAuthorID},
		{reviewTenantEightSourceID, reviewTenantEightProjectID, reviewTenantEightTypeID, reviewAuthorID},
		{reviewTenantEightDocumentID, reviewTenantEightProjectID, reviewTenantEightTypeID, reviewAuthorID},
		{reviewTenantEightVersionID, reviewTenantEightDocumentID, reviewTenantEightProjectID, reviewTenantEightSourceID, strings.Repeat("f", 64), reviewAuthorID},
	}
	for index := range statements {
		require.NoError(t, db.Exec(statements[index], args[index]...).Error)
	}
	for _, actorID := range []string{reviewAuthorID, reviewBusinessActor, reviewEngineeringActor} {
		require.NoError(t, db.Create(&types.TenantMember{
			UserID: actorID, TenantID: reviewTenantID, Role: types.TenantRoleContributor,
			Status: types.TenantMemberStatusActive,
		}).Error)
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

func TestProductionReviewRepositoryListsDocumentHistoryNewestFirstAndTenantScoped(t *testing.T) {
	repo, _ := newProductionReviewRepoFixture(t)
	first := productionReviewRequest(reviewID(80), reviewVersionOne)
	second := productionReviewRequest(reviewID(81), reviewVersionTwo)
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), first, productionReviewSteps(first.ID, 80)))
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), second, productionReviewSteps(second.ID, 90)))

	historyRepo, ok := repo.(interface {
		ListReviews(context.Context, uint64, string, int, int) ([]*types.ProductionReviewRequest, int64, error)
	})
	require.True(t, ok)
	history, total, err := historyRepo.ListReviews(productionReviewTenantContext(reviewTenantID), reviewTenantID, reviewDocumentID, 0, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, history, 1)
	require.Equal(t, second.ID, history[0].ID)
	require.Len(t, history[0].Steps, 2)

	_, _, err = historyRepo.ListReviews(productionReviewTenantContext(8), reviewTenantID, reviewDocumentID, 0, 1)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
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
	require.NoError(t, repo.CreateAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), annotation))
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

	ok, err := repo.ResolveAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), reviewTenantID, annotation.ID, reviewAuthorID, types.ProductionAnnotationResolved)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = repo.ResolveAnnotation(productionReviewContext(reviewTenantID, reviewEngineeringActor), reviewTenantID, annotation.ID, reviewEngineeringActor, types.ProductionAnnotationDismissed)
	require.NoError(t, err)
	require.False(t, ok)

	var persisted types.ProductionAnnotation
	require.NoError(t, db.First(&persisted, "id = ?", annotation.ID).Error)
	require.Equal(t, reviewAuthorID, persisted.CreatedBy)
	require.Equal(t, types.ProductionAnnotationResolved, persisted.Status)
	require.NotNil(t, persisted.ResolvedBy)
	require.Equal(t, reviewAuthorID, *persisted.ResolvedBy)
	require.NotNil(t, persisted.ResolvedAt)
}

func TestProductionReviewRepositoryRejectsRevokedAnnotationResolverAtWriteBoundary(t *testing.T) {
	repo, db := newProductionReviewRepoFixture(t)
	require.NoError(t, db.Create(&types.ProductionProjectMember{
		ProjectID: reviewProjectID, UserID: reviewBusinessActor, Role: types.ProductionRoleAuthor, AssignedBy: reviewAuthorID,
	}).Error)
	annotation := &types.ProductionAnnotation{
		ID: reviewID(3), TenantID: reviewTenantID, ProjectID: reviewProjectID,
		DocumentID: reviewDocumentID, VersionID: reviewVersionOne, BlockID: reviewBlockOne,
		AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationWarning,
		Anchor: types.JSON(`{}`), Body: "revocation", Status: types.ProductionAnnotationOpen, CreatedBy: reviewAuthorID,
	}
	require.NoError(t, repo.CreateAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), annotation))

	// This deletion models a role revocation after an application-layer role read.
	require.NoError(t, db.Where("project_id = ? AND user_id = ? AND role = ?", reviewProjectID, reviewBusinessActor, types.ProductionRoleAuthor).
		Delete(&types.ProductionProjectMember{}).Error)
	ok, err := repo.ResolveAnnotation(productionReviewContext(reviewTenantID, reviewBusinessActor), reviewTenantID,
		annotation.ID, reviewBusinessActor, types.ProductionAnnotationResolved)
	require.NoError(t, err)
	require.False(t, ok)

	var persisted types.ProductionAnnotation
	require.NoError(t, db.First(&persisted, "id = ?", annotation.ID).Error)
	require.Equal(t, types.ProductionAnnotationOpen, persisted.Status)
}

func TestProductionReviewRepositoryConcurrentAnnotationResolutionsSerialize(t *testing.T) {
	repo, db := newProductionReviewRepoFixture(t)
	require.NoError(t, db.Create(&types.ProductionProjectMember{
		ProjectID: reviewProjectID, UserID: reviewBusinessActor,
		Role: types.ProductionRoleAuthor, AssignedBy: reviewAuthorID,
	}).Error)
	annotations := []*types.ProductionAnnotation{
		{
			ID: reviewID(4), TenantID: reviewTenantID, ProjectID: reviewProjectID,
			DocumentID: reviewDocumentID, VersionID: reviewVersionOne, BlockID: reviewBlockOne,
			AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationWarning,
			Anchor: types.JSON(`{}`), Body: "first resolution race", Status: types.ProductionAnnotationOpen,
			CreatedBy: reviewAuthorID,
		},
		{
			ID: reviewID(5), TenantID: reviewTenantID, ProjectID: reviewProjectID,
			DocumentID: reviewDocumentID, VersionID: reviewVersionOne, BlockID: reviewBlockOne,
			AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationWarning,
			Anchor: types.JSON(`{}`), Body: "second resolution race", Status: types.ProductionAnnotationOpen,
			CreatedBy: reviewAuthorID,
		},
	}
	for _, annotation := range annotations {
		require.NoError(t, repo.CreateAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), annotation))
	}
	annotationReads := make(chan struct{}, 2)
	releaseReads := make(chan struct{})
	var readCount int64
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(
		"test:production_annotation_resolution_read_barrier",
		func(tx *gorm.DB) {
			if tx.Statement.Table != "production_annotations" || atomic.AddInt64(&readCount, 1) > 2 {
				return
			}
			annotationReads <- struct{}{}
			<-releaseReads
		},
	))

	type resolutionResult struct {
		changed bool
		err     error
	}
	attempts := []struct {
		annotationID string
		actor        string
		resolution   types.ProductionAnnotationStatus
	}{
		{annotationID: annotations[0].ID, actor: reviewAuthorID, resolution: types.ProductionAnnotationResolved},
		{annotationID: annotations[1].ID, actor: reviewBusinessActor, resolution: types.ProductionAnnotationDismissed},
	}
	results := make(chan resolutionResult, len(attempts))
	var wg sync.WaitGroup
	resolve := func(attempt struct {
		annotationID string
		actor        string
		resolution   types.ProductionAnnotationStatus
	}, started chan<- struct{}) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if started != nil {
				close(started)
			}
			changed, err := repo.ResolveAnnotation(
				productionReviewContext(reviewTenantID, attempt.actor), reviewTenantID,
				attempt.annotationID, attempt.actor, attempt.resolution,
			)
			results <- resolutionResult{changed: changed, err: err}
		}()
	}
	resolve(attempts[0], nil)
	<-annotationReads
	secondStarted := make(chan struct{})
	resolve(attempts[1], secondStarted)
	<-secondStarted
	secondReadBeforeRelease := false
	select {
	case <-annotationReads:
		secondReadBeforeRelease = true
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseReads)
	wg.Wait()
	close(results)
	require.False(t, secondReadBeforeRelease, "second annotation scope read must wait for the document writer reservation")
	changedCount := 0
	for result := range results {
		require.NoError(t, result.err)
		if result.changed {
			changedCount++
		}
	}
	require.Equal(t, 2, changedCount)
}

func TestProductionReviewRepositoryResolveComplianceRiskRoleMatrix(t *testing.T) {
	for _, test := range []struct {
		name  string
		roles []types.ProductionRole
		want  bool
	}{
		{name: "compliance reviewer", roles: []types.ProductionRole{types.ProductionRoleAuthor, types.ProductionRoleComplianceReviewer}, want: true},
		{name: "project owner", roles: []types.ProductionRole{types.ProductionRoleProjectOwner}, want: true},
		{name: "author without compliance", roles: []types.ProductionRole{types.ProductionRoleAuthor}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, db := newProductionReviewRepoFixture(t)
			for _, role := range test.roles {
				require.NoError(t, db.Create(&types.ProductionProjectMember{
					ProjectID: reviewProjectID, UserID: reviewBusinessActor, Role: role, AssignedBy: reviewAuthorID,
				}).Error)
			}
			category := types.ProductionQualityTagComplianceRisk
			annotation := &types.ProductionAnnotation{
				ID: reviewID(20), TenantID: reviewTenantID, ProjectID: reviewProjectID,
				DocumentID: reviewDocumentID, VersionID: reviewVersionOne, BlockID: reviewBlockOne,
				AnnotationType: types.ProductionAnnotationQualityTag, QualityTag: &category,
				Severity: types.ProductionAnnotationBlocking, Anchor: types.JSON(`{}`), Body: "compliance",
				Status: types.ProductionAnnotationOpen, CreatedBy: reviewAuthorID,
			}
			require.NoError(t, repo.CreateAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), annotation))

			ok, err := repo.ResolveAnnotation(productionReviewContext(reviewTenantID, reviewBusinessActor), reviewTenantID,
				annotation.ID, reviewBusinessActor, types.ProductionAnnotationDismissed)
			require.NoError(t, err)
			require.Equal(t, test.want, ok)
		})
	}
}

func TestProductionReviewRepositoryGetsAnnotationWithinTenantScope(t *testing.T) {
	repo, _ := newProductionReviewRepoFixture(t)
	annotation := &types.ProductionAnnotation{
		ID: reviewID(2), TenantID: reviewTenantID, ProjectID: reviewProjectID,
		DocumentID: reviewDocumentID, VersionID: reviewVersionOne, BlockID: reviewBlockOne,
		AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationInfo,
		Anchor: types.JSON(`{}`), Body: "scope check", Status: types.ProductionAnnotationOpen, CreatedBy: reviewAuthorID,
	}
	require.NoError(t, repo.CreateAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), annotation))

	got, err := repo.GetAnnotation(productionReviewTenantContext(reviewTenantID), reviewTenantID, annotation.ID)
	require.NoError(t, err)
	require.Equal(t, annotation.ID, got.ID)
	require.Equal(t, reviewAuthorID, got.CreatedBy)

	got, err = repo.GetAnnotation(productionReviewTenantContext(reviewTenantID+1), reviewTenantID+1, annotation.ID)
	require.Nil(t, got)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestProductionReviewRepositoryListsAnnotationsWithinTenantDocumentAndBounds(t *testing.T) {
	repo, db := newProductionReviewRepoFixture(t)
	require.NoError(t, db.Create(&types.ProductionDocumentBlock{
		ID: reviewID(930), VersionID: reviewOtherVersionID, LogicalBlockID: reviewID(931),
		BlockType: "fact", Position: 0, Content: types.JSON(`{}`), Attributes: types.JSON(`{}`),
		EvidenceRefs: types.JSON(`[]`), AIProvenance: types.JSON(`{}`), ContentDigest: strings.Repeat("9", 64),
	}).Error)
	annotations := []*types.ProductionAnnotation{
		{
			ID: reviewID(31), TenantID: reviewTenantID, ProjectID: reviewProjectID,
			DocumentID: reviewDocumentID, VersionID: reviewVersionOne, BlockID: reviewBlockOne,
			AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationInfo,
			Anchor: types.JSON(`{}`), Body: "tenant seven first", Status: types.ProductionAnnotationOpen, CreatedBy: reviewAuthorID,
		},
		{
			ID: reviewID(32), TenantID: reviewTenantID, ProjectID: reviewProjectID,
			DocumentID: reviewDocumentID, VersionID: reviewVersionTwo, BlockID: reviewBlockTwo,
			AnnotationType: types.ProductionAnnotationSuggestion, Severity: types.ProductionAnnotationWarning,
			Anchor: types.JSON(`{}`), Body: "tenant seven second", Status: types.ProductionAnnotationOpen, CreatedBy: reviewAuthorID,
		},
		{
			ID: reviewID(33), TenantID: reviewTenantID, ProjectID: reviewProjectID,
			DocumentID: reviewOtherDocumentID, VersionID: reviewOtherVersionID, BlockID: reviewID(930),
			AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationInfo,
			Anchor: types.JSON(`{}`), Body: "other document secret", Status: types.ProductionAnnotationOpen, CreatedBy: reviewAuthorID,
		},
	}
	for _, annotation := range annotations {
		require.NoError(t, db.Create(annotation).Error)
	}

	first, total, err := repo.ListAnnotations(
		productionReviewTenantContext(reviewTenantID), reviewTenantID, reviewDocumentID,
		interfaces.ListProductionAnnotationsFilter{}, 0, 1,
	)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, first, 1)
	require.NotContains(t, first[0].Body, "secret")

	second, total, err := repo.ListAnnotations(
		productionReviewTenantContext(reviewTenantID), reviewTenantID, reviewDocumentID,
		interfaces.ListProductionAnnotationsFilter{VersionID: reviewVersionOne, Status: types.ProductionAnnotationOpen,
			AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationInfo}, 0, 100,
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, second, 1)
	require.Equal(t, reviewVersionOne, second[0].VersionID)

	crossTenant, total, err := repo.ListAnnotations(
		productionReviewTenantContext(reviewTenantID+1), reviewTenantID+1, reviewDocumentID,
		interfaces.ListProductionAnnotationsFilter{}, 0, 100,
	)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, crossTenant)
}

func TestProductionReviewRepositoryRejectsUnboundedAnnotationList(t *testing.T) {
	repo, _ := newProductionReviewRepoFixture(t)
	for _, limit := range []int{0, 101} {
		items, total, err := repo.ListAnnotations(
			productionReviewTenantContext(reviewTenantID), reviewTenantID, reviewDocumentID,
			interfaces.ListProductionAnnotationsFilter{}, 0, limit,
		)
		require.Nil(t, items)
		require.Zero(t, total)
		require.ErrorIs(t, err, types.ErrProductionReviewScopeInvalid)
	}
	items, total, err := repo.ListAnnotations(
		productionReviewTenantContext(reviewTenantID), reviewTenantID, reviewDocumentID,
		interfaces.ListProductionAnnotationsFilter{}, types.ProductionAnnotationMaxOffset+1, 1,
	)
	require.Nil(t, items)
	require.Zero(t, total)
	require.ErrorIs(t, err, types.ErrProductionReviewScopeInvalid)

	items, total, err = repo.ListAnnotations(
		productionReviewTenantContext(reviewTenantID), reviewTenantID, reviewDocumentID,
		interfaces.ListProductionAnnotationsFilter{}, types.ProductionAnnotationMaxOffset, 1,
	)
	require.NoError(t, err)
	require.Empty(t, items)
	require.Zero(t, total)
}

func newProductionReviewPostgresMock(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return db, mock
}

func expectProductionMutationScopeLocks(
	mock sqlmock.Sqlmock,
	tenantID uint64,
	projectID, documentID, versionID string,
	documentStatus types.ProductionDocumentStatus,
) {
	mock.ExpectQuery(`SELECT id, current_version_id, status FROM production_documents .* FOR UPDATE`).
		WithArgs(documentID, tenantID, projectID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "current_version_id", "status"}).
			AddRow(documentID, versionID, string(documentStatus)))
	mock.ExpectQuery(`SELECT id FROM production_document_versions .* FOR UPDATE`).
		WithArgs(versionID, documentID, tenantID, projectID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(versionID))
}

func expectProductionTenantMembershipLock(mock sqlmock.Sqlmock, tenantID uint64, actorID string) {
	mock.ExpectQuery(`SELECT id FROM tenant_members .* FOR UPDATE`).
		WithArgs(tenantID, actorID, types.TenantMemberStatusActive).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uint64(1)))
}

func expectProductionProjectMembershipLock(
	mock sqlmock.Sqlmock,
	tenantID uint64,
	projectID, actorID string,
	roles ...types.ProductionRole,
) {
	args := []driver.Value{tenantID, projectID, actorID}
	for _, role := range roles {
		args = append(args, string(role))
	}
	mock.ExpectQuery(`SELECT member.project_id FROM production_project_members AS member .* FOR UPDATE OF member`).
		WithArgs(args...).
		WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(projectID))
}

func TestProductionReviewPostgresAnnotationCreateUsesUniversalMutationLockOrder(t *testing.T) {
	db, mock := newProductionReviewPostgresMock(t)
	repo := NewProductionReviewRepository(db)
	annotation := &types.ProductionAnnotation{
		ID: reviewID(300), TenantID: reviewTenantID, ProjectID: reviewProjectID,
		DocumentID: reviewDocumentID, VersionID: reviewVersionOne, BlockID: reviewBlockOne,
		AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationWarning,
		Anchor: types.JSON(`{}`), Body: "ordered create", Status: types.ProductionAnnotationOpen,
		CreatedBy: reviewAuthorID,
	}

	mock.ExpectBegin()
	expectProductionMutationScopeLocks(mock, reviewTenantID, reviewProjectID, reviewDocumentID, reviewVersionOne, types.ProductionDocumentDraft)
	expectProductionTenantMembershipLock(mock, reviewTenantID, reviewAuthorID)
	expectProductionProjectMembershipLock(
		mock, reviewTenantID, reviewProjectID, reviewAuthorID,
		types.ProductionRoleAuthor,
		types.ProductionRoleBusinessReviewer,
		types.ProductionRoleEngineeringReviewer,
		types.ProductionRoleComplianceReviewer,
	)
	mock.ExpectQuery(`INSERT INTO "production_annotations" .* RETURNING "anchor"`).
		WillReturnRows(sqlmock.NewRows([]string{"anchor"}).AddRow(`{}`))
	mock.ExpectCommit()

	require.NoError(t, repo.CreateAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), annotation))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProductionReviewPostgresAnnotationResolveUsesUniversalMutationLockOrder(t *testing.T) {
	db, mock := newProductionReviewPostgresMock(t)
	repo := NewProductionReviewRepository(db)
	annotationID := reviewID(301)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM "production_annotations"`).
		WithArgs(reviewTenantID, annotationID, 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "project_id", "document_id", "version_id", "created_by",
			"severity", "quality_tag", "status",
		}).AddRow(
			annotationID, reviewTenantID, reviewProjectID, reviewDocumentID, reviewVersionOne, reviewAuthorID,
			string(types.ProductionAnnotationWarning), nil, string(types.ProductionAnnotationOpen),
		))
	expectProductionMutationScopeLocks(mock, reviewTenantID, reviewProjectID, reviewDocumentID, reviewVersionOne, types.ProductionDocumentDraft)
	expectProductionTenantMembershipLock(mock, reviewTenantID, reviewBusinessActor)
	expectProductionProjectMembershipLock(
		mock, reviewTenantID, reviewProjectID, reviewBusinessActor,
		types.ProductionRoleProjectOwner, types.ProductionRoleAuthor,
	)
	mock.ExpectExec(`UPDATE "production_annotations"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	changed, err := repo.ResolveAnnotation(
		productionReviewContext(reviewTenantID, reviewBusinessActor), reviewTenantID,
		annotationID, reviewBusinessActor, types.ProductionAnnotationResolved,
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProductionReviewPostgresSubmissionUsesUniversalMutationLockOrder(t *testing.T) {
	db, mock := newProductionReviewPostgresMock(t)
	repo := NewProductionReviewRepository(db)
	request := productionReviewRequest(reviewID(310), reviewVersionOne)
	steps := productionReviewSteps(request.ID, 310)

	mock.ExpectBegin()
	expectProductionMutationScopeLocks(mock, reviewTenantID, reviewProjectID, reviewDocumentID, reviewVersionOne, types.ProductionDocumentDraft)
	expectProductionTenantMembershipLock(mock, reviewTenantID, reviewAuthorID)
	expectProductionProjectMembershipLock(
		mock, reviewTenantID, reviewProjectID, reviewAuthorID,
		types.ProductionRoleProjectOwner, types.ProductionRoleAuthor,
	)
	mock.ExpectExec(`INSERT INTO "production_review_requests"`).WillReturnResult(sqlmock.NewResult(1, 1))
	for range steps {
		mock.ExpectExec(`INSERT INTO "production_review_steps"`).WillReturnResult(sqlmock.NewResult(1, 1))
	}
	mock.ExpectExec(`UPDATE "production_documents"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.CreateCurrentReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, steps))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProductionReviewPostgresDecisionUsesUniversalMutationLockOrder(t *testing.T) {
	db, mock := newProductionReviewPostgresMock(t)
	repo := NewProductionReviewRepository(db)
	stepID := reviewID(321)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT request\.project_id, request\.document_id, request\.version_id, step\.required_role`).
		WithArgs(reviewTenantID, reviewTenantID, stepID).
		WillReturnRows(sqlmock.NewRows([]string{"project_id", "document_id", "version_id", "required_role"}).
			AddRow(reviewProjectID, reviewDocumentID, reviewVersionOne, string(types.ProductionRoleBusinessReviewer)))
	expectProductionMutationScopeLocks(mock, reviewTenantID, reviewProjectID, reviewDocumentID, reviewVersionOne, types.ProductionDocumentInReview)
	expectProductionTenantMembershipLock(mock, reviewTenantID, reviewBusinessActor)
	expectProductionProjectMembershipLock(
		mock, reviewTenantID, reviewProjectID, reviewBusinessActor, types.ProductionRoleBusinessReviewer,
	)
	mock.ExpectQuery(`SELECT request\.id AS review_id.*FOR UPDATE OF request, step`).
		WithArgs(reviewTenantID, reviewTenantID, stepID).
		WillReturnRows(sqlmock.NewRows([]string{
			"review_id", "project_id", "document_id", "version_id", "status",
		}).AddRow(reviewID(320), reviewProjectID, reviewDocumentID, reviewVersionOne, string(types.ProductionReviewPending)))
	mock.ExpectExec(`UPDATE "production_review_steps"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM "production_review_steps"`).
		WithArgs(reviewID(320), types.ProductionReviewApproved).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectCommit()

	changed, err := repo.DecideStep(
		productionReviewContext(reviewTenantID, reviewBusinessActor), reviewTenantID, stepID,
		types.ProductionReviewPending, types.ProductionReviewApproved, reviewBusinessActor, "ordered decision",
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProductionReviewPostgresLiveMutationLockSmoke(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WEKNORA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set WEKNORA_TEST_POSTGRES_DSN to run the isolated PostgreSQL lock smoke test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	schemaName := fmt.Sprintf("weknora_review_lock_%d", time.Now().UnixNano())
	require.NoError(t, db.Exec(`CREATE SCHEMA "`+schemaName+`"`).Error)
	t.Cleanup(func() { _ = db.Exec(`DROP SCHEMA IF EXISTS "` + schemaName + `" CASCADE`).Error })
	require.NoError(t, db.Exec(`SET search_path TO "`+schemaName+`"`).Error)

	for _, statement := range []string{
		`CREATE TABLE production_documents (
			id TEXT PRIMARY KEY, tenant_id BIGINT NOT NULL, project_id TEXT NOT NULL,
			current_version_id TEXT, status TEXT NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE production_document_versions (
			id TEXT PRIMARY KEY, document_id TEXT NOT NULL, tenant_id BIGINT NOT NULL, project_id TEXT NOT NULL
		)`,
		`CREATE TABLE tenant_members (
			id BIGSERIAL PRIMARY KEY, tenant_id BIGINT NOT NULL, user_id TEXT NOT NULL,
			status TEXT NOT NULL, deleted_at TIMESTAMPTZ
		)`,
		`CREATE TABLE production_projects (
			id TEXT PRIMARY KEY, tenant_id BIGINT NOT NULL, deleted_at TIMESTAMPTZ
		)`,
		`CREATE TABLE production_project_members (
			project_id TEXT NOT NULL, user_id TEXT NOT NULL, role TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(), deleted_at TIMESTAMPTZ
		)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	require.NoError(t, db.Exec(
		`INSERT INTO production_documents (id, tenant_id, project_id, current_version_id, status) VALUES (?, ?, ?, ?, ?)`,
		reviewDocumentID, reviewTenantID, reviewProjectID, reviewVersionOne, types.ProductionDocumentDraft,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO production_document_versions (id, document_id, tenant_id, project_id) VALUES (?, ?, ?, ?)`,
		reviewVersionOne, reviewDocumentID, reviewTenantID, reviewProjectID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO tenant_members (tenant_id, user_id, status) VALUES (?, ?, ?)`,
		reviewTenantID, reviewAuthorID, types.TenantMemberStatusActive,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO production_projects (id, tenant_id) VALUES (?, ?)`, reviewProjectID, reviewTenantID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO production_project_members (project_id, user_id, role) VALUES (?, ?, ?)`,
		reviewProjectID, reviewAuthorID, types.ProductionRoleAuthor,
	).Error)

	annotation := &types.ProductionAnnotation{
		TenantID: reviewTenantID, ProjectID: reviewProjectID,
		DocumentID: reviewDocumentID, VersionID: reviewVersionOne,
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return lockProductionAnnotationCreateAuthority(tx, annotation, reviewAuthorID)
	}))
}

func TestProductionReviewRepositoryExactGovernedSurface(t *testing.T) {
	repositoryType := reflect.TypeOf((*interfaces.ProductionReviewRepository)(nil)).Elem()
	require.Equal(t, 13, repositoryType.NumMethod())
	methods := make([]string, 0, repositoryType.NumMethod())
	for index := range repositoryType.NumMethod() {
		methods = append(methods, repositoryType.Method(index).Name)
	}
	require.ElementsMatch(t, []string{
		"CreateAnnotation",
		"GetAnnotation",
		"ListAnnotations",
		"ResolveAnnotation",
		"CountOpenBlocking",
		"CreateReview",
		"LockCurrentReviewVersion",
		"CreateCurrentReview",
		"GetReview",
		"DecideStep",
		"RejectReviewByTenantAuthority",
		"CancelReviewByTenantAuthority",
		"ObsoletePendingByDocument",
	}, methods)
}

func TestProductionReviewRepositoryRejectsInvalidAnnotationScopeAndLifecycle(t *testing.T) {
	repo, db := newProductionReviewRepoFixture(t)
	annotation := &types.ProductionAnnotation{
		ID: reviewID(10), TenantID: reviewTenantID, ProjectID: reviewProjectID,
		DocumentID: reviewDocumentID, VersionID: reviewVersionOne, BlockID: reviewBlockTwo,
		AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationInfo,
		Anchor: types.JSON(`{}`), Body: "cross version", Status: types.ProductionAnnotationOpen, CreatedBy: reviewAuthorID,
	}
	err := repo.CreateAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), annotation)
	require.ErrorIs(t, err, types.ErrProductionAnnotationAnchorInvalid)

	annotation.ID = reviewID(11)
	annotation.BlockID = reviewBlockOne
	annotation.Status = types.ProductionAnnotationResolved
	err = repo.CreateAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), annotation)
	require.ErrorIs(t, err, types.ErrProductionAnnotationLifecycle)
	ok, err := repo.ResolveAnnotation(productionReviewContext(reviewTenantID, reviewBusinessActor), reviewTenantID, reviewID(999), reviewBusinessActor, types.ProductionAnnotationOpen)
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

	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, steps))
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
	err := repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, productionReviewSteps(request.ID, 30))
	require.ErrorIs(t, err, types.ErrProductionReviewPolicyInvalid)

	request = productionReviewRequest(reviewID(31), reviewVersionOne)
	steps := productionReviewSteps(request.ID, 31)
	require.NoError(t, db.Exec(`CREATE TRIGGER fail_review_step_insert BEFORE INSERT ON production_review_steps BEGIN SELECT RAISE(ABORT, 'forced review step failure'); END`).Error)
	err = repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, steps)
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
			err := repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, steps)
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
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, steps))

	ok, err := repo.DecideStep(productionReviewContext(reviewTenantID+1, reviewBusinessActor), reviewTenantID+1, steps[0].ID,
		types.ProductionReviewPending, types.ProductionReviewApproved, reviewBusinessActor, "ok")
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = repo.DecideStep(productionReviewContext(reviewTenantID, reviewBusinessActor), reviewTenantID, steps[0].ID,
		types.ProductionReviewPending, types.ProductionReviewApproved, reviewBusinessActor, "ok")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = repo.DecideStep(productionReviewContext(reviewTenantID, reviewBusinessActor), reviewTenantID, steps[0].ID,
		types.ProductionReviewPending, types.ProductionReviewRejected, reviewBusinessActor, "again")
	require.NoError(t, err)
	require.False(t, ok)

	persisted, err := repo.GetReview(context.Background(), reviewTenantID, request.ID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionReviewDecision(types.ProductionReviewApproved), persisted.Steps[0].Decision)
	require.NotNil(t, persisted.Steps[0].ReviewerUserID)
	require.Equal(t, reviewBusinessActor, *persisted.Steps[0].ReviewerUserID)
}

func TestProductionReviewRepositoryConcurrentDecisionHasSingleWinner(t *testing.T) {
	repo, _ := newProductionReviewRepoFixture(t)
	request := productionReviewRequest(reviewID(300), reviewVersionOne)
	steps := productionReviewSteps(request.ID, 300)
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, steps))

	type decisionResult struct {
		ok  bool
		err error
	}
	results := make(chan decisionResult, 2)
	var wg sync.WaitGroup
	for _, decision := range []types.ProductionReviewDecision{types.ProductionReviewApproved, types.ProductionReviewRejected} {
		wg.Add(1)
		go func(decision types.ProductionReviewDecision) {
			defer wg.Done()
			ok, err := repo.DecideStep(productionReviewContext(reviewTenantID, reviewBusinessActor), reviewTenantID, steps[0].ID,
				types.ProductionReviewPending, decision, reviewBusinessActor, "race")
			results <- decisionResult{ok: ok, err: err}
		}(decision)
	}
	wg.Wait()
	close(results)
	var trueResults, falseResults int
	for result := range results {
		require.NoError(t, result.err)
		if result.ok {
			trueResults++
		} else {
			falseResults++
		}
	}
	require.Equal(t, 1, trueResults)
	require.Equal(t, 1, falseResults)
}

func TestProductionReviewRepositoryConcurrentDuplicateReviewHasSingleWinner(t *testing.T) {
	repo, db := newProductionReviewRepoFixture(t)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			request := productionReviewRequest(reviewID(400+index*10), reviewVersionOne)
			results <- repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, productionReviewSteps(request.ID, 400+index*10))
		}(index)
	}
	wg.Wait()
	close(results)
	var winners, conflicts int
	for err := range results {
		if err == nil {
			winners++
			continue
		}
		require.ErrorIs(t, err, types.ErrProductionConflict)
		conflicts++
	}
	require.Equal(t, 1, winners)
	require.Equal(t, 1, conflicts)
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
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), first, firstSteps))
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), second, productionReviewSteps(second.ID, 510)))

	require.NoError(t, repo.ObsoletePendingByDocument(productionReviewTenantContext(reviewTenantID), reviewTenantID, reviewDocumentID, reviewVersionTwo))
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

func TestProductionReviewRepositoryInvalidExceptVersionPreservesPendingReview(t *testing.T) {
	for _, test := range []struct {
		name      string
		versionID string
	}{
		{name: "missing", versionID: reviewID(9999)},
		{name: "other document", versionID: reviewOtherVersionID},
		{name: "cross tenant", versionID: reviewTenantEightVersionID},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, _ := newProductionReviewRepoFixture(t)
			request := productionReviewRequest(reviewID(550), reviewVersionOne)
			require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, productionReviewSteps(request.ID, 550)))

			err := repo.ObsoletePendingByDocument(productionReviewTenantContext(reviewTenantID), reviewTenantID, reviewDocumentID, test.versionID)
			require.ErrorIs(t, err, types.ErrProductionReviewScopeInvalid)
			persisted, getErr := repo.GetReview(context.Background(), reviewTenantID, request.ID)
			require.NoError(t, getErr)
			require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewPending), persisted.Status)
			for _, step := range persisted.Steps {
				require.Equal(t, types.ProductionReviewDecision(types.ProductionReviewPending), step.Decision)
			}
		})
	}
}

func TestProductionReviewRepositoryObsoleteRollsBackParentWhenChildCancellationFails(t *testing.T) {
	repo, db := newProductionReviewRepoFixture(t)
	request := productionReviewRequest(reviewID(560), reviewVersionOne)
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, productionReviewSteps(request.ID, 560)))
	require.NoError(t, db.Exec(`CREATE TRIGGER fail_review_obsolete_child BEFORE UPDATE ON production_review_steps WHEN NEW.decision = 'cancelled' BEGIN SELECT RAISE(ABORT, 'forced obsolete child failure'); END`).Error)

	err := repo.ObsoletePendingByDocument(productionReviewTenantContext(reviewTenantID), reviewTenantID, reviewDocumentID, reviewVersionTwo)
	require.ErrorContains(t, err, "forced obsolete child failure")
	persisted, getErr := repo.GetReview(context.Background(), reviewTenantID, request.ID)
	require.NoError(t, getErr)
	require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewPending), persisted.Status)
	for _, step := range persisted.Steps {
		require.Equal(t, types.ProductionReviewDecision(types.ProductionReviewPending), step.Decision)
	}
}

func TestProductionReviewRepositoryObsoleteJoinsOuterTransactionRollback(t *testing.T) {
	repo, db := newProductionReviewRepoFixture(t)
	request := productionReviewRequest(reviewID(565), reviewVersionOne)
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, productionReviewSteps(request.ID, 565)))
	err := database.WithTransactionContext(productionReviewTenantContext(reviewTenantID), db, func(txCtx context.Context) error {
		require.NoError(t, repo.ObsoletePendingByDocument(txCtx, reviewTenantID, reviewDocumentID, reviewVersionTwo))
		return errors.New("force outer obsolete rollback")
	})
	require.ErrorContains(t, err, "force outer obsolete rollback")
	persisted, getErr := repo.GetReview(context.Background(), reviewTenantID, request.ID)
	require.NoError(t, getErr)
	require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewPending), persisted.Status)
	for _, step := range persisted.Steps {
		require.Equal(t, types.ProductionReviewDecision(types.ProductionReviewPending), step.Decision)
	}
}

func TestProductionReviewRepositoryRejectsSystemStepTerminalizationSpoofing(t *testing.T) {
	repo, _ := newProductionReviewRepoFixture(t)
	request := productionReviewRequest(reviewID(570), reviewVersionOne)
	steps := productionReviewSteps(request.ID, 570)
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, steps))

	ok, err := repo.DecideStep(productionReviewContext(reviewTenantID, reviewBusinessActor), reviewTenantID, steps[0].ID,
		types.ProductionReviewPending, types.ProductionReviewCancelled, reviewBusinessActor, "spoof")
	require.ErrorIs(t, err, types.ErrProductionReviewLifecycle)
	require.False(t, ok)

	persisted, getErr := repo.GetReview(context.Background(), reviewTenantID, request.ID)
	require.NoError(t, getErr)
	require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewPending), persisted.Status)
	for _, step := range persisted.Steps {
		require.Equal(t, types.ProductionReviewDecision(types.ProductionReviewPending), step.Decision)
	}
}

func TestProductionReviewRepositoryDerivesCreateActorsAndRejectsSpoofing(t *testing.T) {
	repo, db := newProductionReviewRepoFixture(t)
	spoofed := &types.ProductionAnnotation{
		ID: reviewID(580), TenantID: reviewTenantID, ProjectID: reviewProjectID,
		DocumentID: reviewDocumentID, VersionID: reviewVersionOne, BlockID: reviewBlockOne,
		AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationInfo,
		Anchor: types.JSON(`{}`), Body: "spoofed", Status: types.ProductionAnnotationOpen,
		CreatedBy: reviewBusinessActor,
	}
	err := repo.CreateAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), spoofed)
	require.ErrorIs(t, err, types.ErrProductionForbidden)

	derived := *spoofed
	derived.ID = reviewID(581)
	derived.CreatedBy = ""
	require.NoError(t, repo.CreateAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), &derived))
	require.Equal(t, reviewAuthorID, derived.CreatedBy)

	spoofedRequest := productionReviewRequest(reviewID(582), reviewVersionOne)
	spoofedRequest.SubmittedBy = reviewBusinessActor
	err = repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), spoofedRequest, productionReviewSteps(spoofedRequest.ID, 582))
	require.ErrorIs(t, err, types.ErrProductionForbidden)

	derivedRequest := productionReviewRequest(reviewID(585), reviewVersionTwo)
	derivedRequest.SubmittedBy = ""
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), derivedRequest, productionReviewSteps(derivedRequest.ID, 585)))
	require.Equal(t, reviewAuthorID, derivedRequest.SubmittedBy)

	var annotationCount, reviewCount int64
	require.NoError(t, db.Model(&types.ProductionAnnotation{}).Count(&annotationCount).Error)
	require.NoError(t, db.Model(&types.ProductionReviewRequest{}).Count(&reviewCount).Error)
	require.Equal(t, int64(1), annotationCount)
	require.Equal(t, int64(1), reviewCount)
}

func TestProductionReviewRepositoryMutationActorsMustMatchTrustedContext(t *testing.T) {
	repo, _ := newProductionReviewRepoFixture(t)
	annotation := &types.ProductionAnnotation{
		ID: reviewID(590), TenantID: reviewTenantID, ProjectID: reviewProjectID,
		DocumentID: reviewDocumentID, VersionID: reviewVersionOne, BlockID: reviewBlockOne,
		AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationInfo,
		Anchor: types.JSON(`{}`), Body: "note", Status: types.ProductionAnnotationOpen, CreatedBy: reviewAuthorID,
	}
	require.NoError(t, repo.CreateAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), annotation))
	request := productionReviewRequest(reviewID(591), reviewVersionOne)
	steps := productionReviewSteps(request.ID, 591)
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, steps))

	ok, err := repo.ResolveAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), reviewTenantID,
		annotation.ID, reviewBusinessActor, types.ProductionAnnotationResolved)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.False(t, ok)
	ok, err = repo.DecideStep(productionReviewContext(reviewTenantID, reviewAuthorID), reviewTenantID, steps[0].ID,
		types.ProductionReviewPending, types.ProductionReviewApproved, reviewBusinessActor, "spoof")
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.False(t, ok)
	ok, err = repo.DecideStep(productionReviewContext(8, reviewBusinessActor), reviewTenantID, steps[0].ID,
		types.ProductionReviewPending, types.ProductionReviewApproved, reviewBusinessActor, "cross tenant")
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.False(t, ok)
	err = repo.ObsoletePendingByDocument(productionReviewTenantContext(8), reviewTenantID, reviewDocumentID, reviewVersionTwo)
	require.ErrorIs(t, err, types.ErrProductionForbidden)

	persisted, getErr := repo.GetReview(context.Background(), reviewTenantID, request.ID)
	require.NoError(t, getErr)
	require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewPending), persisted.Status)
	for _, step := range persisted.Steps {
		require.Equal(t, types.ProductionReviewDecision(types.ProductionReviewPending), step.Decision)
	}
}

func TestProductionReviewRepositoryAnnotationAnchorResourceBounds(t *testing.T) {
	for _, test := range []struct {
		name    string
		anchor  types.JSON
		wantErr bool
	}{
		{name: "exact bytes", anchor: types.JSON(`{"x":"` + strings.Repeat("a", types.ProductionAnnotationAnchorMaxBytes-8) + `"}`)},
		{name: "bytes limit plus one", anchor: types.JSON(`{"x":"` + strings.Repeat("a", types.ProductionAnnotationAnchorMaxBytes-7) + `"}`), wantErr: true},
		{name: "exact depth", anchor: productionReviewRepositoryNestedObject(types.ProductionAnnotationAnchorMaxDepth)},
		{name: "depth limit plus one", anchor: productionReviewRepositoryNestedObject(types.ProductionAnnotationAnchorMaxDepth + 1), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, db := newProductionReviewRepoFixture(t)
			annotation := &types.ProductionAnnotation{
				ID: reviewID(700), TenantID: reviewTenantID, ProjectID: reviewProjectID,
				DocumentID: reviewDocumentID, VersionID: reviewVersionOne, BlockID: reviewBlockOne,
				AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationInfo,
				Anchor: test.anchor, Body: "bounded", Status: types.ProductionAnnotationOpen, CreatedBy: reviewAuthorID,
			}
			err := repo.CreateAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), annotation)
			if test.wantErr {
				require.ErrorIs(t, err, types.ErrProductionJSONResourceLimit)
			} else {
				require.NoError(t, err)
			}
			var count int64
			require.NoError(t, db.Model(&types.ProductionAnnotation{}).Count(&count).Error)
			if test.wantErr {
				require.Zero(t, count)
			} else {
				require.Equal(t, int64(1), count)
			}
		})
	}
}

func TestProductionReviewRepositoryOwnsEveryLifecycleTimestamp(t *testing.T) {
	createdAt := time.Date(2026, 7, 20, 1, 2, 3, 456000000, time.FixedZone("hostile", 8*60*60))
	clock := &productionReviewTestClock{current: createdAt}
	repo, db := newProductionReviewRepoFixtureWithClock(t, clock)
	callerPast := createdAt.Add(-365 * 24 * time.Hour)
	callerFuture := createdAt.Add(365 * 24 * time.Hour)
	annotation := &types.ProductionAnnotation{
		ID: reviewID(720), TenantID: reviewTenantID, ProjectID: reviewProjectID,
		DocumentID: reviewDocumentID, VersionID: reviewVersionOne, BlockID: reviewBlockOne,
		AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationInfo,
		Anchor: types.JSON(`{}`), Body: "timed", Status: types.ProductionAnnotationOpen, CreatedBy: reviewAuthorID,
		CreatedAt: callerPast, UpdatedAt: callerFuture,
	}
	require.NoError(t, repo.CreateAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), annotation))
	expectedCreatedAt := createdAt.UTC()
	require.Equal(t, expectedCreatedAt, annotation.CreatedAt)
	require.Equal(t, expectedCreatedAt, annotation.UpdatedAt)

	request := productionReviewRequest(reviewID(721), reviewVersionOne)
	request.CreatedAt, request.SubmittedAt, request.UpdatedAt = callerPast, callerFuture, callerPast
	steps := productionReviewSteps(request.ID, 721)
	for index := range steps {
		steps[index].CreatedAt = callerFuture.Add(time.Duration(index) * time.Hour)
		steps[index].UpdatedAt = callerPast.Add(-time.Duration(index) * time.Hour)
	}
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, steps))
	require.Equal(t, expectedCreatedAt, request.CreatedAt)
	require.Equal(t, expectedCreatedAt, request.SubmittedAt)
	require.Equal(t, expectedCreatedAt, request.UpdatedAt)
	for _, step := range steps {
		require.Equal(t, expectedCreatedAt, step.CreatedAt)
		require.Equal(t, expectedCreatedAt, step.UpdatedAt)
	}

	mutatedAt := createdAt.Add(2 * time.Hour)
	clock.current = mutatedAt
	ok, err := repo.ResolveAnnotation(productionReviewContext(reviewTenantID, reviewAuthorID), reviewTenantID,
		annotation.ID, reviewAuthorID, types.ProductionAnnotationResolved)
	require.NoError(t, err)
	require.True(t, ok)
	for index, actor := range []string{reviewBusinessActor, reviewEngineeringActor} {
		ok, err = repo.DecideStep(productionReviewContext(reviewTenantID, actor), reviewTenantID, steps[index].ID,
			types.ProductionReviewPending, types.ProductionReviewApproved, actor, "timed")
		require.NoError(t, err)
		require.True(t, ok)
	}
	var persistedAnnotation types.ProductionAnnotation
	require.NoError(t, db.First(&persistedAnnotation, "id = ?", annotation.ID).Error)
	require.NotNil(t, persistedAnnotation.ResolvedAt)
	require.Equal(t, mutatedAt.UTC(), *persistedAnnotation.ResolvedAt)
	persistedReview, err := repo.GetReview(context.Background(), reviewTenantID, request.ID)
	require.NoError(t, err)
	for _, step := range persistedReview.Steps {
		require.NotNil(t, step.DecidedAt)
		require.Equal(t, mutatedAt.UTC(), *step.DecidedAt)
	}
}

func TestProductionReviewRepositoryObsoleteUsesClockForParentAndChildren(t *testing.T) {
	createdAt := time.Date(2026, 7, 20, 2, 0, 0, 0, time.UTC)
	clock := &productionReviewTestClock{current: createdAt}
	repo, _ := newProductionReviewRepoFixtureWithClock(t, clock)
	request := productionReviewRequest(reviewID(750), reviewVersionOne)
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, productionReviewSteps(request.ID, 750)))
	obsoleteAt := createdAt.Add(90 * time.Minute)
	clock.current = obsoleteAt
	require.NoError(t, repo.ObsoletePendingByDocument(productionReviewTenantContext(reviewTenantID), reviewTenantID, reviewDocumentID, reviewVersionTwo))

	persisted, err := repo.GetReview(context.Background(), reviewTenantID, request.ID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewObsolete), persisted.Status)
	require.NotNil(t, persisted.CompletedAt)
	require.Equal(t, obsoleteAt, *persisted.CompletedAt)
	require.NotNil(t, persisted.TerminalBy)
	require.Equal(t, types.ProductionSystemActorID, *persisted.TerminalBy)
	for _, step := range persisted.Steps {
		require.Equal(t, types.ProductionReviewDecision(types.ProductionReviewCancelled), step.Decision)
		require.NotNil(t, step.DecidedAt)
		require.Equal(t, obsoleteAt, *step.DecidedAt)
	}
}

func TestProductionReviewRepositoryDecisionsAndCreateJoinSharedTransaction(t *testing.T) {
	repo, db := newProductionReviewRepoFixture(t)
	request := productionReviewRequest(reviewID(600), reviewVersionOne)
	steps := productionReviewSteps(request.ID, 600)
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, steps))
	for index, actor := range []string{reviewBusinessActor, reviewEngineeringActor} {
		ok, err := repo.DecideStep(productionReviewContext(reviewTenantID, actor), reviewTenantID, steps[index].ID,
			types.ProductionReviewPending, types.ProductionReviewApproved, actor, "ok")
		require.NoError(t, err)
		require.True(t, ok)
	}
	rollbackRequest := productionReviewRequest(reviewID(610), reviewVersionTwo)
	err := database.WithTransactionContext(productionReviewContext(reviewTenantID, reviewAuthorID), db, func(txCtx context.Context) error {
		require.NoError(t, repo.CreateReview(txCtx, rollbackRequest, productionReviewSteps(rollbackRequest.ID, 610)))
		return fmt.Errorf("force outer rollback")
	})
	require.ErrorContains(t, err, "force outer rollback")
	_, err = repo.GetReview(context.Background(), reviewTenantID, rollbackRequest.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
