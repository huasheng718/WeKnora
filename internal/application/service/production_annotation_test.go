package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	annotationTenantID   = uint64(7)
	annotationProjectID  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	annotationDocumentID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	annotationVersionOne = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	annotationVersionTwo = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	annotationBlockOne   = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	annotationBlockTwo   = "ffffffff-ffff-4fff-8fff-ffffffffffff"
)

type productionAnnotationDocumentRepoStub struct {
	document *types.ProductionDocument
	versions map[string]*types.ProductionDocumentVersion
}

func (r *productionAnnotationDocumentRepoStub) CreateDocument(context.Context, *types.ProductionDocument, *types.ProductionDocumentVersion) error {
	return errors.New("unexpected CreateDocument")
}

func (r *productionAnnotationDocumentRepoStub) GetDocument(context.Context, uint64, string) (*types.ProductionDocument, error) {
	return r.document, nil
}

func (r *productionAnnotationDocumentRepoStub) AppendVersion(context.Context, *types.ProductionDocumentVersion, []*types.ProductionDocumentBlock, []*types.ProductionBlockLineage) error {
	return errors.New("unexpected AppendVersion")
}

func (r *productionAnnotationDocumentRepoStub) GetVersion(_ context.Context, _ uint64, versionID string) (*types.ProductionDocumentVersion, error) {
	version, ok := r.versions[versionID]
	if !ok {
		return nil, errors.New("version not found")
	}
	return version, nil
}

func (r *productionAnnotationDocumentRepoStub) ListVersions(context.Context, uint64, string) ([]*types.ProductionDocumentVersion, error) {
	return nil, errors.New("unexpected ListVersions")
}

type productionAnnotationReviewRepoStub struct {
	created    *types.ProductionAnnotation
	annotation *types.ProductionAnnotation
	resolvedID string
	resolvedBy string
	resolution types.ProductionAnnotationStatus
	resolveOK  bool
}

func (r *productionAnnotationReviewRepoStub) ListAnnotations(
	context.Context, uint64, string, interfaces.ListProductionAnnotationsFilter, int, int,
) ([]*types.ProductionAnnotation, int64, error) {
	return nil, 0, errors.New("unexpected ListAnnotations")
}

func (r *productionAnnotationReviewRepoStub) CreateAnnotation(_ context.Context, annotation *types.ProductionAnnotation) error {
	copy := *annotation
	r.created = &copy
	return nil
}

func (r *productionAnnotationReviewRepoStub) GetAnnotation(context.Context, uint64, string) (*types.ProductionAnnotation, error) {
	if r.annotation == nil {
		return nil, errors.New("annotation not found")
	}
	return r.annotation, nil
}

func (r *productionAnnotationReviewRepoStub) ResolveAnnotation(_ context.Context, _ uint64, annotationID, actorID string, resolution types.ProductionAnnotationStatus) (bool, error) {
	r.resolvedID = annotationID
	r.resolvedBy = actorID
	r.resolution = resolution
	return r.resolveOK, nil
}

func (r *productionAnnotationReviewRepoStub) CountOpenBlocking(context.Context, uint64, string) (int64, error) {
	return 0, errors.New("unexpected CountOpenBlocking")
}

func (r *productionAnnotationReviewRepoStub) CreateReview(context.Context, *types.ProductionReviewRequest, []*types.ProductionReviewStep) error {
	return errors.New("unexpected CreateReview")
}

func (r *productionAnnotationReviewRepoStub) CreateCurrentReview(context.Context, *types.ProductionReviewRequest, []*types.ProductionReviewStep) error {
	return errors.New("unexpected CreateCurrentReview")
}

func (r *productionAnnotationReviewRepoStub) LockCurrentReviewVersion(context.Context, *types.ProductionReviewRequest) error {
	return errors.New("unexpected LockCurrentReviewVersion")
}

func (r *productionAnnotationReviewRepoStub) GetReview(context.Context, uint64, string) (*types.ProductionReviewRequest, error) {
	return nil, errors.New("unexpected GetReview")
}

func (r *productionAnnotationReviewRepoStub) DecideStep(context.Context, uint64, string, types.ProductionReviewDecision, types.ProductionReviewDecision, string, string) (bool, error) {
	return false, errors.New("unexpected DecideStep")
}

func (r *productionAnnotationReviewRepoStub) RejectReviewByTenantAuthority(context.Context, uint64, string, string, string) (bool, error) {
	return false, errors.New("unexpected RejectReviewByTenantAuthority")
}

func (r *productionAnnotationReviewRepoStub) CancelReviewByTenantAuthority(context.Context, uint64, string, string, string) (bool, error) {
	return false, errors.New("unexpected CancelReviewByTenantAuthority")
}

func (r *productionAnnotationReviewRepoStub) ObsoletePendingByDocument(context.Context, uint64, string, string) error {
	return errors.New("unexpected ObsoletePendingByDocument")
}

type productionAnnotationAuthorizerStub struct {
	err     error
	require func(...types.ProductionRole) error
	roles   []types.ProductionRole
	calls   [][]types.ProductionRole
}

func (a *productionAnnotationAuthorizerStub) RequireProjectRole(_ context.Context, _ string, roles ...types.ProductionRole) error {
	a.roles = append([]types.ProductionRole(nil), roles...)
	a.calls = append(a.calls, append([]types.ProductionRole(nil), roles...))
	if a.require != nil {
		return a.require(roles...)
	}
	return a.err
}

func newProductionAnnotationFixture(t *testing.T) (*productionAnnotationService, *productionAnnotationReviewRepoStub, *productionAnnotationAuthorizerStub) {
	t.Helper()
	documents := &productionAnnotationDocumentRepoStub{
		document: &types.ProductionDocument{ID: annotationDocumentID, TenantID: annotationTenantID, ProjectID: annotationProjectID},
		versions: map[string]*types.ProductionDocumentVersion{
			annotationVersionOne: {ID: annotationVersionOne, TenantID: annotationTenantID, ProjectID: annotationProjectID, DocumentID: annotationDocumentID, Blocks: []*types.ProductionDocumentBlock{{ID: annotationBlockOne, VersionID: annotationVersionOne}}},
			annotationVersionTwo: {ID: annotationVersionTwo, TenantID: annotationTenantID, ProjectID: annotationProjectID, DocumentID: annotationDocumentID, Blocks: []*types.ProductionDocumentBlock{{ID: annotationBlockTwo, VersionID: annotationVersionTwo}}},
		},
	}
	reviews := &productionAnnotationReviewRepoStub{}
	authorizer := &productionAnnotationAuthorizerStub{}
	return NewProductionAnnotationService(reviews, documents, authorizer), reviews, authorizer
}

func productionAnnotationContext() context.Context {
	return productionAnnotationContextFor("11111111-1111-4111-8111-111111111111")
}

func productionAnnotationContextFor(actorID string) context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, annotationTenantID)
	return context.WithValue(ctx, types.UserIDContextKey, actorID)
}

func TestProductionAnnotationRejectsBlockFromAnotherVersion(t *testing.T) {
	svc, reviews, _ := newProductionAnnotationFixture(t)
	_, err := svc.Create(productionAnnotationContext(), CreateProductionAnnotationInput{
		DocumentID: annotationDocumentID, VersionID: annotationVersionTwo, BlockID: annotationBlockOne,
	})
	require.ErrorIs(t, err, types.ErrProductionAnnotationAnchorInvalid)
	require.Nil(t, reviews.created)
}

func TestProductionAnnotationCreateDerivesTrustedActorAndScope(t *testing.T) {
	svc, reviews, authorizer := newProductionAnnotationFixture(t)
	annotation, err := svc.Create(productionAnnotationContext(), CreateProductionAnnotationInput{
		DocumentID: annotationDocumentID, VersionID: annotationVersionOne, BlockID: annotationBlockOne,
		AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationInfo,
		Anchor: types.JSON(`{}`), Body: "Needs clarification",
	})
	require.NoError(t, err)
	require.Equal(t, reviews.created, annotation)
	require.Equal(t, annotationTenantID, annotation.TenantID)
	require.Equal(t, annotationProjectID, annotation.ProjectID)
	require.Equal(t, "11111111-1111-4111-8111-111111111111", annotation.CreatedBy)
	require.Equal(t, types.ProductionAnnotationOpen, annotation.Status)
	require.Equal(t, []types.ProductionRole{
		types.ProductionRoleAuthor,
		types.ProductionRoleBusinessReviewer,
		types.ProductionRoleEngineeringReviewer,
		types.ProductionRoleComplianceReviewer,
	}, authorizer.roles)
}

func TestProductionAnnotationListAllowsTenantViewerProjectObserverAndDeniesNonMembers(t *testing.T) {
	svc, repo, db, _ := newProductionAnnotationWriteBoundaryFixture(t)
	observerID := "22222222-2222-4222-8222-222222222222"
	members := newProductionMemberServiceStub()
	members.add(annotationTenantID, observerID, types.TenantRoleViewer)
	projectService := NewProductionProjectService(apprepository.NewProductionProjectRepository(db), members, nil)
	svc.projects = projectService
	repoAnnotation := &types.ProductionAnnotation{
		ID: "12345678-1234-4234-8234-123456789012", TenantID: annotationTenantID,
		ProjectID: annotationProjectID, DocumentID: annotationDocumentID,
		VersionID: annotationVersionOne, BlockID: annotationBlockOne,
		AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationInfo,
		Anchor: types.JSON(`{}`), Body: "observer-visible", Status: types.ProductionAnnotationOpen,
		CreatedBy: "11111111-1111-4111-8111-111111111111",
	}
	require.NoError(t, repo.CreateAnnotation(productionAnnotationContext(), repoAnnotation))
	require.NoError(t, db.Create(&types.ProductionProjectMember{
		ProjectID: annotationProjectID, UserID: observerID, Role: types.ProductionRoleObserver,
		AssignedBy: "33333333-3333-4333-8333-333333333333",
	}).Error)

	observerCtx := productionAnnotationContextFor(observerID)
	observerCtx = context.WithValue(observerCtx, types.TenantRoleContextKey, types.TenantRoleViewer)
	page, err := svc.List(observerCtx, ListProductionAnnotationsInput{
		DocumentID: annotationDocumentID, VersionID: annotationVersionOne,
		Status: types.ProductionAnnotationOpen, Page: 1, PageSize: 20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, 1, page.Page)
	require.Equal(t, 20, page.PageSize)
	require.Len(t, page.Data, 1)
	require.Equal(t, "observer-visible", page.Data[0].Body)

	nonMemberID := "33333333-3333-4333-8333-333333333334"
	members.add(annotationTenantID, nonMemberID, types.TenantRoleViewer)
	nonMemberCtx := productionAnnotationContextFor(nonMemberID)
	denied, err := svc.List(nonMemberCtx, ListProductionAnnotationsInput{
		DocumentID: annotationDocumentID, Page: 1, PageSize: 20,
	})
	require.Nil(t, denied)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionAnnotationListCrossTenantCannotLeakDocumentContent(t *testing.T) {
	svc, repo, _, _ := newProductionAnnotationWriteBoundaryFixture(t)
	require.NoError(t, repo.CreateAnnotation(productionAnnotationContext(), &types.ProductionAnnotation{
		ID: "12345678-1234-4234-8234-123456789013", TenantID: annotationTenantID,
		ProjectID: annotationProjectID, DocumentID: annotationDocumentID,
		VersionID: annotationVersionOne, BlockID: annotationBlockOne,
		AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationInfo,
		Anchor: types.JSON(`{}`), Body: "cross-tenant-secret", Status: types.ProductionAnnotationOpen,
		CreatedBy: "11111111-1111-4111-8111-111111111111",
	}))
	crossTenant := context.WithValue(context.Background(), types.TenantIDContextKey, annotationTenantID+1)
	crossTenant = context.WithValue(crossTenant, types.UserIDContextKey, "22222222-2222-4222-8222-222222222222")

	page, err := svc.List(crossTenant, ListProductionAnnotationsInput{
		DocumentID: annotationDocumentID, Page: 1, PageSize: 20,
	})
	require.Nil(t, page)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "cross-tenant-secret")
}

func TestProductionAnnotationListRejectsVersionOutsidePathDocument(t *testing.T) {
	svc, _, _ := newProductionAnnotationFixture(t)
	documents := svc.documents.(*productionAnnotationDocumentRepoStub)
	documents.versions[annotationVersionTwo].DocumentID = "12345678-1234-4234-8234-123456789014"

	page, err := svc.List(productionAnnotationContext(), ListProductionAnnotationsInput{
		DocumentID: annotationDocumentID, VersionID: annotationVersionTwo, Page: 1, PageSize: 20,
	})
	require.Nil(t, page)
	require.ErrorIs(t, err, types.ErrProductionReviewScopeInvalid)
}

func TestProductionAnnotationResolveAllowsCreator(t *testing.T) {
	svc, reviews, authorizer := newProductionAnnotationFixture(t)
	reviews.annotation = &types.ProductionAnnotation{
		ID: "99999999-9999-4999-8999-999999999999", TenantID: annotationTenantID, ProjectID: annotationProjectID,
		CreatedBy: "11111111-1111-4111-8111-111111111111", Severity: types.ProductionAnnotationWarning,
		Status: types.ProductionAnnotationOpen,
	}
	reviews.resolveOK = true
	authorizer.err = types.ErrProductionForbidden

	err := svc.Resolve(productionAnnotationContext(), reviews.annotation.ID, types.ProductionAnnotationResolved)
	require.NoError(t, err)
	require.Equal(t, reviews.annotation.ID, reviews.resolvedID)
	require.Equal(t, "11111111-1111-4111-8111-111111111111", reviews.resolvedBy)
	require.Equal(t, types.ProductionAnnotationResolved, reviews.resolution)
}

func TestProductionAnnotationResolveRequiresComplianceForBlockingRisk(t *testing.T) {
	svc, reviews, authorizer := newProductionAnnotationFixture(t)
	category := types.ProductionQualityTagComplianceRisk
	reviews.annotation = &types.ProductionAnnotation{
		ID: "99999999-9999-4999-8999-999999999999", TenantID: annotationTenantID, ProjectID: annotationProjectID,
		CreatedBy: "11111111-1111-4111-8111-111111111111", AnnotationType: types.ProductionAnnotationQualityTag,
		QualityTag: &category, Severity: types.ProductionAnnotationBlocking, Status: types.ProductionAnnotationOpen,
	}
	authorizer.err = types.ErrProductionForbidden

	err := svc.Resolve(productionAnnotationContext(), reviews.annotation.ID, types.ProductionAnnotationDismissed)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Empty(t, reviews.resolvedID)
	require.Equal(t, []types.ProductionRole{types.ProductionRoleProjectOwner, types.ProductionRoleComplianceReviewer}, authorizer.roles)
}

func TestProductionAnnotationResolveComplianceRoleMatrix(t *testing.T) {
	category := types.ProductionQualityTagComplianceRisk
	for _, test := range []struct {
		name            string
		baseAllowed     bool
		complianceAllow bool
		wantErr         bool
	}{
		{name: "compliance reviewer", baseAllowed: true, complianceAllow: true},
		{name: "project owner", baseAllowed: true, complianceAllow: true},
		{name: "author without compliance", baseAllowed: true, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, reviews, authorizer := newProductionAnnotationFixture(t)
			reviews.annotation = &types.ProductionAnnotation{
				ID: "99999999-9999-4999-8999-999999999999", TenantID: annotationTenantID, ProjectID: annotationProjectID,
				CreatedBy: "22222222-2222-4222-8222-222222222222", AnnotationType: types.ProductionAnnotationQualityTag,
				QualityTag: &category, Severity: types.ProductionAnnotationBlocking, Status: types.ProductionAnnotationOpen,
			}
			reviews.resolveOK = true
			authorizer.require = func(roles ...types.ProductionRole) error {
				if annotationRoleSet(roles, types.ProductionRoleProjectOwner, types.ProductionRoleAuthor) && test.baseAllowed {
					return nil
				}
				if annotationRoleSet(roles, types.ProductionRoleProjectOwner, types.ProductionRoleComplianceReviewer) && test.complianceAllow {
					return nil
				}
				return types.ErrProductionForbidden
			}

			err := svc.Resolve(productionAnnotationContext(), reviews.annotation.ID, types.ProductionAnnotationDismissed)
			if test.wantErr {
				require.ErrorIs(t, err, types.ErrProductionForbidden)
				require.Empty(t, reviews.resolvedID)
				return
			}
			require.NoError(t, err)
			require.Equal(t, reviews.annotation.ID, reviews.resolvedID)
			require.Equal(t, [][]types.ProductionRole{
				{types.ProductionRoleProjectOwner, types.ProductionRoleAuthor},
				{types.ProductionRoleProjectOwner, types.ProductionRoleComplianceReviewer},
			}, authorizer.calls)
		})
	}
}

func TestProductionAnnotationResolveConcurrentRoleRevocationIsRejected(t *testing.T) {
	svc, repo, db, authorizer := newProductionAnnotationWriteBoundaryFixture(t)
	const creatorID = "22222222-2222-4222-8222-222222222222"
	annotation := &types.ProductionAnnotation{
		ID: "99999999-9999-4999-8999-999999999999", TenantID: annotationTenantID, ProjectID: annotationProjectID,
		DocumentID: annotationDocumentID, VersionID: annotationVersionOne, BlockID: annotationBlockOne,
		AnnotationType: types.ProductionAnnotationComment, Severity: types.ProductionAnnotationWarning,
		Anchor: types.JSON(`{}`), Body: "concurrent revocation", Status: types.ProductionAnnotationOpen, CreatedBy: creatorID,
	}
	require.NoError(t, repo.CreateAnnotation(productionAnnotationContextFor(creatorID), annotation))
	authorizer.require = func(...types.ProductionRole) error {
		return db.Where("project_id = ? AND user_id = ? AND role = ?", annotationProjectID,
			"11111111-1111-4111-8111-111111111111", types.ProductionRoleAuthor).
			Delete(&types.ProductionProjectMember{}).Error
	}

	err := svc.Resolve(productionAnnotationContext(), annotation.ID, types.ProductionAnnotationResolved)
	require.ErrorIs(t, err, types.ErrProductionAnnotationLifecycle)
	var persisted types.ProductionAnnotation
	require.NoError(t, db.First(&persisted, "id = ?", annotation.ID).Error)
	require.Equal(t, types.ProductionAnnotationOpen, persisted.Status)
}

func annotationRoleSet(roles []types.ProductionRole, first, second types.ProductionRole) bool {
	return len(roles) == 2 && roles[0] == first && roles[1] == second
}

func newProductionAnnotationWriteBoundaryFixture(t *testing.T) (*productionAnnotationService, interfaces.ProductionReviewRepository, *gorm.DB, *productionAnnotationAuthorizerStub) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "annotation-write-boundary.db") + "?_foreign_keys=1&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
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
	statements := []string{
		`INSERT INTO production_projects (id, tenant_id, name, owner_user_id) VALUES (?, ?, 'Annotations', ?)`,
		`INSERT INTO production_document_types (id, tenant_id, code, name, schema_version, status, created_by) VALUES (?, ?, 'annotation', 'Annotation', 1, 'active', ?)`,
		`INSERT INTO production_source_sets (id, tenant_id, project_id, document_type_id, status, created_by, frozen_at) VALUES (?, ?, ?, ?, 'frozen', ?, CURRENT_TIMESTAMP)`,
		`INSERT INTO production_documents (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, current_version_id, status, created_by) VALUES (?, ?, ?, ?, 1, 'Annotation', NULL, 'draft', ?)`,
		`INSERT INTO production_document_versions (id, document_id, tenant_id, project_id, version_number, source_set_id, origin, content_digest, created_by, frozen_at) VALUES (?, ?, ?, ?, 1, ?, 'human', ?, ?, CURRENT_TIMESTAMP)`,
		`UPDATE production_documents SET current_version_id = ? WHERE id = ?`,
		`INSERT INTO production_document_blocks (id, version_id, logical_block_id, block_type, position, content, attributes, evidence_refs, ai_provenance, content_digest) VALUES (?, ?, ?, 'fact', 1, '{}', '{}', '[]', '{}', ?)`,
		`INSERT INTO production_project_members (project_id, user_id, role, assigned_by) VALUES (?, ?, 'author', ?)`,
	}
	args := [][]any{
		{annotationProjectID, annotationTenantID, "33333333-3333-4333-8333-333333333333"},
		{"44444444-4444-4444-8444-444444444444", annotationTenantID, "33333333-3333-4333-8333-333333333333"},
		{"55555555-5555-4555-8555-555555555555", annotationTenantID, annotationProjectID, "44444444-4444-4444-8444-444444444444", "33333333-3333-4333-8333-333333333333"},
		{annotationDocumentID, annotationTenantID, annotationProjectID, "44444444-4444-4444-8444-444444444444", "33333333-3333-4333-8333-333333333333"},
		{annotationVersionOne, annotationDocumentID, annotationTenantID, annotationProjectID, "55555555-5555-4555-8555-555555555555", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "33333333-3333-4333-8333-333333333333"},
		{annotationVersionOne, annotationDocumentID},
		{annotationBlockOne, annotationVersionOne, "block", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{annotationProjectID, "11111111-1111-4111-8111-111111111111", "33333333-3333-4333-8333-333333333333"},
	}
	for index := range statements {
		require.NoError(t, db.Exec(statements[index], args[index]...).Error)
	}
	authorizer := &productionAnnotationAuthorizerStub{}
	repo := apprepository.NewProductionReviewRepository(db)
	return NewProductionAnnotationService(repo, apprepository.NewProductionDocumentRepository(db), authorizer), repo, db, authorizer
}

var _ interfaces.ProductionDocumentRepository = (*productionAnnotationDocumentRepoStub)(nil)
var _ interfaces.ProductionReviewRepository = (*productionAnnotationReviewRepoStub)(nil)
