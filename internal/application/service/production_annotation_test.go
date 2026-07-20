package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
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

func (r *productionAnnotationReviewRepoStub) GetReview(context.Context, uint64, string) (*types.ProductionReviewRequest, error) {
	return nil, errors.New("unexpected GetReview")
}

func (r *productionAnnotationReviewRepoStub) DecideStep(context.Context, uint64, string, types.ProductionReviewDecision, types.ProductionReviewDecision, string, string) (bool, error) {
	return false, errors.New("unexpected DecideStep")
}

func (r *productionAnnotationReviewRepoStub) ObsoletePendingByDocument(context.Context, uint64, string, string) error {
	return errors.New("unexpected ObsoletePendingByDocument")
}

type productionAnnotationAuthorizerStub struct {
	err     error
	require func(...types.ProductionRole) error
	roles   []types.ProductionRole
}

func (a *productionAnnotationAuthorizerStub) RequireProjectRole(_ context.Context, _ string, roles ...types.ProductionRole) error {
	a.roles = append([]types.ProductionRole(nil), roles...)
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

var _ interfaces.ProductionDocumentRepository = (*productionAnnotationDocumentRepoStub)(nil)
var _ interfaces.ProductionReviewRepository = (*productionAnnotationReviewRepoStub)(nil)
