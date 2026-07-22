package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	productionReviewTenantID      = uint64(7)
	productionReviewProjectID     = "81000000-0000-4000-8000-000000000001"
	productionReviewTypeID        = "81000000-0000-4000-8000-000000000002"
	productionReviewSourceSetID   = "81000000-0000-4000-8000-000000000003"
	productionReviewSourceItemID  = "81000000-0000-4000-8000-000000000004"
	productionReviewEvidenceID    = "81000000-0000-4000-8000-000000000005"
	productionReviewDocumentID    = "81000000-0000-4000-8000-000000000006"
	productionReviewVersionID     = "81000000-0000-4000-8000-000000000007"
	productionReviewBlockID       = "81000000-0000-4000-8000-000000000008"
	productionReviewAuthorID      = "81000000-0000-4000-8000-000000000009"
	productionReviewBusinessID    = "81000000-0000-4000-8000-00000000000a"
	productionReviewEngineeringID = "81000000-0000-4000-8000-00000000000b"
	productionReviewTenantAdminID = "81000000-0000-4000-8000-00000000000c"
	productionReviewTenantOwnerID = "81000000-0000-4000-8000-00000000000d"
)

type productionReviewFixture struct {
	svc       *productionReviewService
	db        *gorm.DB
	reviews   interfaces.ProductionReviewRepository
	documents interfaces.ProductionDocumentRepository
	members   *productionMemberServiceStub
	audit     *productionAuditServiceStub
}

type revokingTerminalReplayRepository struct {
	interfaces.ProductionReviewRepository
	members  *productionMemberServiceStub
	tenantID uint64
	actorID  string
}

type revokingProfessionalMutationRepository struct {
	interfaces.ProductionReviewRepository
	db              *gorm.DB
	actorID         string
	suspendOnSubmit bool
	removeOnDecide  bool
}

func (r *revokingProfessionalMutationRepository) LockCurrentReviewVersion(
	ctx context.Context, request *types.ProductionReviewRequest,
) error {
	if r.suspendOnSubmit {
		if err := database.DBFromContext(ctx, r.db).WithContext(ctx).Model(&types.TenantMember{}).
			Where("tenant_id = ? AND user_id = ?", request.TenantID, r.actorID).
			UpdateColumn("status", types.TenantMemberStatusSuspended).Error; err != nil {
			return err
		}
	}
	return r.ProductionReviewRepository.LockCurrentReviewVersion(ctx, request)
}

func (r *revokingProfessionalMutationRepository) DecideStep(
	ctx context.Context, tenantID uint64, stepID string,
	from, to types.ProductionReviewDecision, actorID, comment string,
) (bool, error) {
	if r.removeOnDecide {
		if err := database.DBFromContext(ctx, r.db).WithContext(ctx).
			Where("tenant_id = ? AND user_id = ?", tenantID, r.actorID).
			Delete(&types.TenantMember{}).Error; err != nil {
			return false, err
		}
	}
	return r.ProductionReviewRepository.DecideStep(
		ctx, tenantID, stepID, from, to, actorID, comment,
	)
}

func (r *revokingTerminalReplayRepository) RejectReviewByTenantAuthority(
	ctx context.Context, tenantID uint64, reviewID, actorID, reason string,
) (bool, error) {
	delete(r.members.members[r.tenantID], r.actorID)
	return r.ProductionReviewRepository.RejectReviewByTenantAuthority(
		ctx, tenantID, reviewID, actorID, reason,
	)
}

type retiringProductionDocumentTypeRepository struct {
	interfaces.ProductionDocumentTypeRepository
	db *gorm.DB
}

func (r *retiringProductionDocumentTypeRepository) GetByID(
	ctx context.Context,
	tenantID uint64,
	documentTypeID string,
) (*types.ProductionDocumentType, error) {
	documentType, err := r.ProductionDocumentTypeRepository.GetByID(ctx, tenantID, documentTypeID)
	if err != nil {
		return nil, err
	}
	err = database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.ProductionDocumentType{}).
		Where("tenant_id = ? AND id = ?", tenantID, documentTypeID).
		UpdateColumn("status", types.ProductionDocumentTypeRetired).Error
	return documentType, err
}

func (r *retiringProductionDocumentTypeRepository) GetActiveByIDForReview(
	ctx context.Context,
	tenantID uint64,
	documentTypeID string,
	schemaVersion int,
) (*types.ProductionDocumentType, error) {
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	if err := db.Model(&types.ProductionDocumentType{}).
		Where("tenant_id = ? AND id = ?", tenantID, documentTypeID).
		UpdateColumn("status", types.ProductionDocumentTypeRetired).Error; err != nil {
		return nil, err
	}
	var documentType types.ProductionDocumentType
	err := db.Where(
		"tenant_id = ? AND id = ? AND schema_version = ? AND status = ?",
		tenantID, documentTypeID, schemaVersion, types.ProductionDocumentTypeActive,
	).First(&documentType).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, types.ErrProductionDocumentTypeInactive
	}
	return &documentType, err
}

func newProductionReviewFixture(t *testing.T) *productionReviewFixture {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "production-review-service.db") +
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
	require.NoError(t, db.Exec(`ALTER TABLE production_document_types ADD COLUMN template_key VARCHAR(255)`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE production_document_types ADD COLUMN origin VARCHAR(16) NOT NULL DEFAULT 'custom'`).Error)
	require.NoError(t, db.AutoMigrate(&types.AuditLog{}, &types.TenantMember{}))

	seedProductionReviewServiceScope(t, db)
	members := newProductionMemberServiceStub()
	for actor, role := range map[string]types.TenantRole{
		productionReviewAuthorID:      types.TenantRoleContributor,
		productionReviewBusinessID:    types.TenantRoleContributor,
		productionReviewEngineeringID: types.TenantRoleContributor,
		productionReviewTenantAdminID: types.TenantRoleAdmin,
		productionReviewTenantOwnerID: types.TenantRoleOwner,
	} {
		members.add(productionReviewTenantID, actor, role)
	}
	reviews := apprepository.NewProductionReviewRepository(db)
	documents := apprepository.NewProductionDocumentRepository(db)
	audit := &productionAuditServiceStub{}
	svc := NewProductionReviewService(
		reviews,
		documents,
		apprepository.NewProductionSourceRepository(db),
		apprepository.NewProductionDocumentTypeRepository(db),
		&productionDocumentAuthorizerStub{},
		nil,
		members,
		audit,
		apprepository.NewProductionUnitOfWork(db),
	)
	return &productionReviewFixture{svc: svc, db: db, reviews: reviews, documents: documents, members: members, audit: audit}
}

func seedProductionReviewServiceScope(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()
	policy := types.JSON(`{"steps":["business_reviewer","engineering_reviewer"]}`)
	require.NoError(t, db.Create(&types.ProductionProject{
		ID: productionReviewProjectID, TenantID: productionReviewTenantID, Name: "Governed review",
		OwnerUserID: productionReviewTenantOwnerID, Status: types.ProductionProjectActive,
	}).Error)
	require.NoError(t, db.Create(&types.ProductionDocumentType{
		ID: productionReviewTypeID, TenantID: productionReviewTenantID, Code: "review", Name: "Review", SchemaVersion: 1,
		BlockSchema: types.JSON(`{}`), SourceRequirements: types.JSON(`{}`), SkillBindings: types.JSON(`{}`),
		QualityRules: types.JSON(`{}`), ReviewPolicy: policy, PublicationPolicy: types.JSON(`{}`),
		Status: types.ProductionDocumentTypeActive, CreatedBy: productionReviewTenantOwnerID,
	}).Error)
	require.NoError(t, db.Create(&types.ProductionSourceSet{
		ID: productionReviewSourceSetID, TenantID: productionReviewTenantID, ProjectID: productionReviewProjectID,
		DocumentTypeID: productionReviewTypeID, Status: types.ProductionSourceSetCollecting,
		CreatedBy: productionReviewAuthorID,
	}).Error)
	require.NoError(t, db.Create(&types.ProductionSourceItem{
		ID: productionReviewSourceItemID, SourceSetID: productionReviewSourceSetID,
		SourceKind: types.ProductionSourceKindManual, Title: "Evidence", MimeType: "application/json",
		ContentDigest: strings.Repeat("a", 64), CapturedAt: now, Metadata: types.JSON(`{}`),
		Status: types.ProductionSourceItemAccepted,
	}).Error)
	evidence := types.JSON(`{"fact":"verified"}`)
	sum := sha256.Sum256(evidence)
	require.NoError(t, db.Exec(`
INSERT INTO production_evidence_snapshots
    (id, source_item_id, snapshot_type, inline_content, content_digest, redaction_metadata)
VALUES (?, ?, ?, ?, ?, '{}')
`, productionReviewEvidenceID, productionReviewSourceItemID, types.ProductionEvidenceSnapshotJSON,
		string(evidence), hex.EncodeToString(sum[:])).Error)
	require.NoError(t, db.Model(&types.ProductionSourceSet{}).
		Where("id = ?", productionReviewSourceSetID).
		Updates(map[string]any{"status": types.ProductionSourceSetFrozen, "frozen_at": now}).Error)

	block := &types.ProductionDocumentBlock{
		ID: productionReviewBlockID, VersionID: productionReviewVersionID, LogicalBlockID: "claim",
		BlockType: "paragraph", Position: 0, Content: types.JSON(`"verified claim"`), Attributes: types.JSON(`{}`),
		EvidenceRefs: types.JSON(`["` + productionReviewEvidenceID + `"]`), AIProvenance: types.JSON(`{}`),
	}
	block.ContentDigest = types.ComputeProductionBlockDigest(block)
	version := &types.ProductionDocumentVersion{
		ID: productionReviewVersionID, DocumentID: productionReviewDocumentID,
		TenantID: productionReviewTenantID, ProjectID: productionReviewProjectID, VersionNumber: 1,
		SourceSetID: productionReviewSourceSetID, Origin: types.ProductionDocumentOriginHuman,
		CreatedBy: productionReviewAuthorID, FrozenAt: &now, Blocks: []*types.ProductionDocumentBlock{block},
	}
	version.ContentDigest = types.ComputeProductionVersionDigest(version)
	require.NoError(t, db.Create(&types.ProductionDocument{
		ID: productionReviewDocumentID, TenantID: productionReviewTenantID, ProjectID: productionReviewProjectID,
		DocumentTypeID: productionReviewTypeID, DocumentTypeSchemaVersion: 1, Title: "Review me",
		Status: types.ProductionDocumentDraft, CreatedBy: productionReviewAuthorID,
	}).Error)
	require.NoError(t, db.Omit("Blocks", "Lineage").Create(version).Error)
	require.NoError(t, db.Create(block).Error)
	require.NoError(t, db.Model(&types.ProductionDocument{}).
		Where("id = ?", productionReviewDocumentID).
		UpdateColumn("current_version_id", productionReviewVersionID).Error)

	for actor, role := range map[string]types.ProductionRole{
		productionReviewAuthorID:      types.ProductionRoleAuthor,
		productionReviewBusinessID:    types.ProductionRoleBusinessReviewer,
		productionReviewEngineeringID: types.ProductionRoleEngineeringReviewer,
	} {
		require.NoError(t, db.Create(&types.ProductionProjectMember{
			ProjectID: productionReviewProjectID, UserID: actor, Role: role, AssignedBy: productionReviewTenantOwnerID,
		}).Error)
	}
	for actor, role := range map[string]types.TenantRole{
		productionReviewAuthorID:      types.TenantRoleContributor,
		productionReviewBusinessID:    types.TenantRoleContributor,
		productionReviewEngineeringID: types.TenantRoleContributor,
		productionReviewTenantAdminID: types.TenantRoleAdmin,
		productionReviewTenantOwnerID: types.TenantRoleOwner,
	} {
		require.NoError(t, db.Create(&types.TenantMember{
			UserID: actor, TenantID: productionReviewTenantID, Role: role, Status: types.TenantMemberStatusActive,
			JoinedAt: now,
		}).Error)
	}
}

func productionReviewServiceContext(actor string, role types.TenantRole) context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, productionReviewTenantID)
	ctx = context.WithValue(ctx, types.UserIDContextKey, actor)
	return context.WithValue(ctx, types.TenantRoleContextKey, role)
}

func (fixture *productionReviewFixture) submit(t *testing.T) *types.ProductionReviewRequest {
	t.Helper()
	request, err := fixture.svc.Submit(
		productionReviewServiceContext(productionReviewAuthorID, types.TenantRoleContributor),
		productionReviewDocumentID,
		productionReviewVersionID,
	)
	require.NoError(t, err)
	return request
}

func TestProductionReviewServiceListsAuthorizedDocumentHistory(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	created := fixture.submit(t)
	historyService, ok := any(fixture.svc).(interface {
		List(context.Context, string, int, int) ([]*types.ProductionReviewRequest, int64, error)
	})
	require.True(t, ok)
	history, total, err := historyService.List(
		productionReviewServiceContext(productionReviewAuthorID, types.TenantRoleContributor),
		productionReviewDocumentID, 0, 20,
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, history, 1)
	require.Equal(t, created.ID, history[0].ID)
	require.Len(t, history[0].Steps, 2)
}

func TestBlockingAnnotationPreventsReviewSubmission(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	require.NoError(t, fixture.db.Create(&types.ProductionAnnotation{
		ID: "82000000-0000-4000-8000-000000000001", TenantID: productionReviewTenantID,
		ProjectID: productionReviewProjectID, DocumentID: productionReviewDocumentID,
		VersionID: productionReviewVersionID, BlockID: productionReviewBlockID,
		AnnotationType: types.ProductionAnnotationQualityTag, QualityTag: productionCategory(types.ProductionQualityTagFactualRisk),
		Severity: types.ProductionAnnotationBlocking, Anchor: types.JSON(`{}`), Body: "must fix",
		Status: types.ProductionAnnotationOpen, CreatedBy: productionReviewBusinessID,
	}).Error)

	request, err := fixture.svc.Submit(
		productionReviewServiceContext(productionReviewAuthorID, types.TenantRoleContributor),
		productionReviewDocumentID,
		productionReviewVersionID,
	)

	require.Nil(t, request)
	require.ErrorIs(t, err, types.ErrProductionBlockingAnnotations)
	require.Zero(t, countServiceRows(t, fixture.db, &types.ProductionReviewRequest{}))
}

func TestDigestMismatchPreventsReviewSubmission(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	require.NoError(t, fixture.db.Exec(`DROP TRIGGER trg_production_document_blocks_prevent_update`).Error)
	require.NoError(t, fixture.db.Model(&types.ProductionDocumentBlock{}).
		Where("id = ?", productionReviewBlockID).UpdateColumn("content", `"tampered"`).Error)

	request, err := fixture.svc.Submit(
		productionReviewServiceContext(productionReviewAuthorID, types.TenantRoleContributor),
		productionReviewDocumentID,
		productionReviewVersionID,
	)

	require.Nil(t, request)
	require.ErrorIs(t, err, types.ErrProductionContentDigestMismatch)
	require.Len(t, fixture.audit.entries, 1)
	require.Equal(t, types.AuditActionProductionContentDigestMismatch, fixture.audit.entries[0].Action)
	require.Equal(t, productionReviewAuthorID, fixture.audit.entries[0].ActorUserID)
}

func TestProductionReviewSubmissionFreezesCanonicalPolicyAndMaterializesSteps(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request := fixture.submit(t)

	require.Equal(t, types.JSON(`{"steps":["business_reviewer","engineering_reviewer"]}`), request.PolicySnapshot)
	canonical, digest, err := types.CanonicalProductionReviewPolicy(request.PolicySnapshot)
	require.NoError(t, err)
	require.Equal(t, canonical, request.PolicySnapshot)
	require.Equal(t, digest, request.PolicyDigest)
	require.Len(t, request.Steps, 2)
	require.Equal(t, types.ProductionRoleBusinessReviewer, request.Steps[0].RequiredRole)
	require.Equal(t, types.ProductionRoleEngineeringReviewer, request.Steps[1].RequiredRole)
	require.Equal(t, types.AuditActionProductionReviewSubmitted, fixture.audit.entries[0].Action)
	require.Equal(t, productionReviewAuthorID, fixture.audit.entries[0].ActorUserID)
}

func TestProductionReviewGetRequiresTenantScopedProjectAccess(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request := fixture.submit(t)
	authorizer := &productionDocumentAuthorizerStub{}
	fixture.svc.projects = authorizer

	loaded, err := fixture.svc.Get(
		productionReviewServiceContext(productionReviewBusinessID, types.TenantRoleContributor),
		request.ID,
	)
	require.NoError(t, err)
	require.Equal(t, request.ID, loaded.ID)
	require.Equal(t, productionReviewProjectID, authorizer.project)
	require.ElementsMatch(t, []types.ProductionRole{
		types.ProductionRoleProjectOwner,
		types.ProductionRoleAuthor,
		types.ProductionRoleBusinessReviewer,
		types.ProductionRoleEngineeringReviewer,
		types.ProductionRoleComplianceReviewer,
		types.ProductionRoleObserver,
	}, authorizer.roles)

	authorizer.err = types.ErrProductionForbidden
	loaded, err = fixture.svc.Get(
		productionReviewServiceContext(productionReviewBusinessID, types.TenantRoleContributor),
		request.ID,
	)
	require.Nil(t, loaded)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionReviewGetAllowsTenantViewerProjectObserverAndDeniesNonMembers(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request := fixture.submit(t)
	observerID := "81000000-0000-4000-8000-00000000000e"
	fixture.members.add(productionReviewTenantID, observerID, types.TenantRoleViewer)
	require.NoError(t, fixture.db.Create(&types.ProductionProjectMember{
		ProjectID: productionReviewProjectID, UserID: observerID,
		Role: types.ProductionRoleObserver, AssignedBy: productionReviewTenantOwnerID,
	}).Error)
	fixture.svc.projects = NewProductionProjectService(
		apprepository.NewProductionProjectRepository(fixture.db), fixture.members, nil,
	)
	observerCtx := productionReviewServiceContext(observerID, types.TenantRoleViewer)

	loaded, err := fixture.svc.Get(observerCtx, request.ID)
	require.NoError(t, err)
	require.Equal(t, request.ID, loaded.ID)

	nonMemberID := "81000000-0000-4000-8000-00000000000f"
	fixture.members.add(productionReviewTenantID, nonMemberID, types.TenantRoleViewer)
	denied, err := fixture.svc.Get(
		productionReviewServiceContext(nonMemberID, types.TenantRoleViewer), request.ID,
	)
	require.Nil(t, denied)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionReviewGetCrossTenantCannotLeakAggregate(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request := fixture.submit(t)
	crossTenant := context.WithValue(context.Background(), types.TenantIDContextKey, productionReviewTenantID+1)
	crossTenant = context.WithValue(crossTenant, types.UserIDContextKey, productionReviewBusinessID)
	crossTenant = context.WithValue(crossTenant, types.TenantRoleContextKey, types.TenantRoleViewer)

	loaded, err := fixture.svc.Get(crossTenant, request.ID)
	require.Nil(t, loaded)
	require.Error(t, err)
	require.NotContains(t, err.Error(), request.PolicyDigest)
}

func TestProductionReviewSubmissionAuditFailureRollsBackAggregate(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	fixture.audit.err = errors.New("review audit unavailable")

	request, err := fixture.svc.Submit(
		productionReviewServiceContext(productionReviewAuthorID, types.TenantRoleContributor),
		productionReviewDocumentID, productionReviewVersionID,
	)

	require.Nil(t, request)
	require.ErrorContains(t, err, "review audit unavailable")
	require.Zero(t, countServiceRows(t, fixture.db, &types.ProductionReviewRequest{}))
	document, loadErr := fixture.documents.GetDocument(context.Background(), productionReviewTenantID, productionReviewDocumentID)
	require.NoError(t, loadErr)
	require.Equal(t, types.ProductionDocumentDraft, document.Status)
}

func TestProductionReviewSubmissionRequiresExactCurrentVersion(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request, err := fixture.svc.Submit(
		productionReviewServiceContext(productionReviewAuthorID, types.TenantRoleContributor),
		productionReviewDocumentID,
		"82000000-0000-4000-8000-000000000099",
	)
	require.Nil(t, request)
	require.ErrorIs(t, err, types.ErrProductionReviewScopeInvalid)
}

func TestProductionReviewSubmissionRejectsTypeRetiredAfterInitialRead(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	fixture.svc.documentTypes = &retiringProductionDocumentTypeRepository{
		ProductionDocumentTypeRepository: fixture.svc.documentTypes,
		db:                               fixture.db,
	}

	request, err := fixture.svc.Submit(
		productionReviewServiceContext(productionReviewAuthorID, types.TenantRoleContributor),
		productionReviewDocumentID, productionReviewVersionID,
	)

	require.Nil(t, request)
	require.ErrorIs(t, err, types.ErrProductionDocumentTypeInactive)
	require.Zero(t, countServiceRows(t, fixture.db, &types.ProductionReviewRequest{}))
}

func TestProductionReviewRevokedSubmitterRoleAtMutationBoundaryCannotSubmit(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	require.NoError(t, fixture.db.Where(
		"project_id = ? AND user_id = ? AND role = ?",
		productionReviewProjectID, productionReviewAuthorID, types.ProductionRoleAuthor,
	).Delete(&types.ProductionProjectMember{}).Error)

	request, err := fixture.svc.Submit(
		productionReviewServiceContext(productionReviewAuthorID, types.TenantRoleContributor),
		productionReviewDocumentID,
		productionReviewVersionID,
	)

	require.Nil(t, request)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Zero(t, countServiceRows(t, fixture.db, &types.ProductionReviewRequest{}))
}

func TestProductionReviewSuspendedTenantAtSubmissionBoundaryCannotSubmit(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	fixture.svc.reviews = &revokingProfessionalMutationRepository{
		ProductionReviewRepository: fixture.reviews, db: fixture.db,
		actorID: productionReviewAuthorID, suspendOnSubmit: true,
	}

	request, err := fixture.svc.Submit(
		productionReviewServiceContext(productionReviewAuthorID, types.TenantRoleContributor),
		productionReviewDocumentID, productionReviewVersionID,
	)
	require.Nil(t, request)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Zero(t, countServiceRows(t, fixture.db, &types.ProductionReviewRequest{}))
}

func TestWrongProjectRoleCannotApproveStep(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request := fixture.submit(t)

	err := fixture.svc.Decide(
		productionReviewServiceContext(productionReviewBusinessID, types.TenantRoleContributor),
		request.Steps[1].ID,
		types.ProductionReviewApproved,
		"ok",
	)

	require.ErrorIs(t, err, types.ErrProductionForbidden)
	loaded, loadErr := fixture.reviews.GetReview(
		productionReviewServiceContext(productionReviewBusinessID, types.TenantRoleContributor),
		productionReviewTenantID,
		request.ID,
	)
	require.NoError(t, loadErr)
	require.Equal(t, types.ProductionReviewDecision(types.ProductionReviewPending), loaded.Steps[1].Decision)
}

func TestProductionReviewDuplicateDecisionIsLifecycleConflict(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request := fixture.submit(t)
	ctx := productionReviewServiceContext(productionReviewBusinessID, types.TenantRoleContributor)

	require.NoError(t, fixture.svc.Decide(
		ctx, request.Steps[0].ID, types.ProductionReviewApproved, "approved",
	))
	err := fixture.svc.Decide(
		ctx, request.Steps[0].ID, types.ProductionReviewApproved, "approved again",
	)

	require.ErrorIs(t, err, types.ErrProductionReviewLifecycle)
}

func TestProductionReviewFinalRequiredApprovalAtomicallyApprovesDocument(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request := fixture.submit(t)
	require.NoError(t, fixture.svc.Decide(
		productionReviewServiceContext(productionReviewBusinessID, types.TenantRoleContributor),
		request.Steps[0].ID, types.ProductionReviewApproved, "business approved",
	))
	require.NoError(t, fixture.svc.Decide(
		productionReviewServiceContext(productionReviewEngineeringID, types.TenantRoleContributor),
		request.Steps[1].ID, types.ProductionReviewApproved, "engineering approved",
	))

	loaded, err := fixture.reviews.GetReview(
		productionReviewServiceContext(productionReviewEngineeringID, types.TenantRoleContributor),
		productionReviewTenantID,
		request.ID,
	)
	require.NoError(t, err)
	require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewApproved), loaded.Status)
	document, err := fixture.documents.GetDocument(context.Background(), productionReviewTenantID, productionReviewDocumentID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionDocumentApproved, document.Status)
	require.Equal(t, productionReviewVersionID, *document.LatestApprovedVersionID)
	require.Len(t, fixture.audit.entries, 3)
	require.Equal(t, types.AuditActionProductionReviewDecided, fixture.audit.entries[2].Action)
	require.Equal(t, productionReviewEngineeringID, fixture.audit.entries[2].ActorUserID)
}

func TestProductionReviewDecisionAuditFailureRollsBackStepAndAggregate(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request := fixture.submit(t)
	fixture.audit.err = errors.New("decision audit unavailable")

	err := fixture.svc.Decide(
		productionReviewServiceContext(productionReviewBusinessID, types.TenantRoleContributor),
		request.Steps[0].ID, types.ProductionReviewApproved, "approved",
	)

	require.ErrorContains(t, err, "decision audit unavailable")
	loaded, loadErr := fixture.reviews.GetReview(
		productionReviewServiceContext(productionReviewBusinessID, types.TenantRoleContributor),
		productionReviewTenantID, request.ID,
	)
	require.NoError(t, loadErr)
	require.Equal(t, types.ProductionReviewDecision(types.ProductionReviewPending), loaded.Steps[0].Decision)
	require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewPending), loaded.Status)
}

func TestProductionReviewConcurrentFinalCosignApprovesOnce(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request := fixture.submit(t)
	fixture.svc.audit = NewAuditLogService(apprepository.NewAuditLogRepository(fixture.db))
	type decision struct {
		actor string
		step  string
	}
	decisions := []decision{
		{actor: productionReviewBusinessID, step: request.Steps[0].ID},
		{actor: productionReviewEngineeringID, step: request.Steps[1].ID},
	}
	errorsByDecision := make(chan error, len(decisions))
	var wait sync.WaitGroup
	for _, item := range decisions {
		wait.Add(1)
		go func(item decision) {
			defer wait.Done()
			errorsByDecision <- fixture.svc.Decide(
				productionReviewServiceContext(item.actor, types.TenantRoleContributor),
				item.step, types.ProductionReviewApproved, "concurrent approval",
			)
		}(item)
	}
	wait.Wait()
	close(errorsByDecision)
	for decisionErr := range errorsByDecision {
		require.NoError(t, decisionErr)
	}

	loaded, err := fixture.reviews.GetReview(
		productionReviewServiceContext(productionReviewEngineeringID, types.TenantRoleContributor),
		productionReviewTenantID, request.ID,
	)
	require.NoError(t, err)
	require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewApproved), loaded.Status)
	require.Equal(t, int64(2), countServiceRows(t, fixture.db, &types.AuditLog{}))
}

func TestProductionReviewChangesRequestedAndProfessionalRejectTerminalizeRequest(t *testing.T) {
	for _, decision := range []types.ProductionReviewDecision{
		types.ProductionReviewChangesRequested,
		types.ProductionReviewRejected,
	} {
		t.Run(string(decision), func(t *testing.T) {
			fixture := newProductionReviewFixture(t)
			request := fixture.submit(t)
			require.NoError(t, fixture.svc.Decide(
				productionReviewServiceContext(productionReviewBusinessID, types.TenantRoleContributor),
				request.Steps[0].ID, decision, "professional decision",
			))
			loaded, err := fixture.reviews.GetReview(
				productionReviewServiceContext(productionReviewBusinessID, types.TenantRoleContributor),
				productionReviewTenantID, request.ID,
			)
			require.NoError(t, err)
			require.Equal(t, types.ProductionReviewStatus(decision), loaded.Status)
			require.Equal(t, types.ProductionReviewDecision(types.ProductionReviewCancelled), loaded.Steps[1].Decision)
		})
	}
}

func TestTenantAdminCannotImpersonateProfessionalReviewer(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request := fixture.submit(t)
	err := fixture.svc.Decide(
		productionReviewServiceContext(productionReviewTenantAdminID, types.TenantRoleAdmin),
		request.Steps[0].ID, types.ProductionReviewApproved, "admin approval",
	)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestTenantAdminAndOwnerUseExplicitRejectAndCancelPaths(t *testing.T) {
	t.Run("admin rejects", func(t *testing.T) {
		fixture := newProductionReviewFixture(t)
		request := fixture.submit(t)
		require.NoError(t, fixture.svc.Reject(
			productionReviewServiceContext(productionReviewTenantAdminID, types.TenantRoleAdmin),
			request.ID, "emergency admin rejection",
		))
		loaded, err := fixture.reviews.GetReview(
			productionReviewServiceContext(productionReviewTenantAdminID, types.TenantRoleAdmin),
			productionReviewTenantID, request.ID,
		)
		require.NoError(t, err)
		require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewRejected), loaded.Status)
	})

	t.Run("owner cancels", func(t *testing.T) {
		fixture := newProductionReviewFixture(t)
		request := fixture.submit(t)
		require.NoError(t, fixture.svc.Cancel(
			productionReviewServiceContext(productionReviewTenantOwnerID, types.TenantRoleOwner),
			request.ID, "owner cancelled",
		))
		loaded, err := fixture.reviews.GetReview(
			productionReviewServiceContext(productionReviewTenantOwnerID, types.TenantRoleOwner),
			productionReviewTenantID, request.ID,
		)
		require.NoError(t, err)
		require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewCancelled), loaded.Status)
	})
}

func TestProductionReviewTenantAuthorityTerminalizationIsIdempotentAndConflictsAcrossOutcomes(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request := fixture.submit(t)
	ctx := productionReviewServiceContext(productionReviewTenantAdminID, types.TenantRoleAdmin)

	require.NoError(t, fixture.svc.Reject(ctx, request.ID, "emergency rejection"))
	require.NoError(t, fixture.svc.Reject(ctx, request.ID, "replayed rejection"))
	require.ErrorIs(t, fixture.svc.Cancel(ctx, request.ID, "conflicting cancellation"), types.ErrProductionConflict)

	loaded, err := fixture.reviews.GetReview(ctx, productionReviewTenantID, request.ID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewRejected), loaded.Status)
	require.NotNil(t, loaded.TerminalReason)
	require.Equal(t, "emergency rejection", *loaded.TerminalReason)
}

func TestProductionReviewTerminalReplayRevalidatesLiveTenantAuthority(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request := fixture.submit(t)
	ctx := productionReviewServiceContext(productionReviewTenantAdminID, types.TenantRoleAdmin)
	require.NoError(t, fixture.svc.Reject(ctx, request.ID, "initial rejection"))
	fixture.svc.reviews = &revokingTerminalReplayRepository{
		ProductionReviewRepository: fixture.reviews,
		members:                    fixture.members, tenantID: productionReviewTenantID, actorID: productionReviewTenantAdminID,
	}

	err := fixture.svc.Reject(ctx, request.ID, "replayed after revocation")
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionReviewRevokedTenantAuthorityAtMutationBoundaryCannotCancel(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request := fixture.submit(t)
	require.NoError(t, fixture.db.Where(
		"tenant_id = ? AND user_id = ?", productionReviewTenantID, productionReviewTenantAdminID,
	).Delete(&types.TenantMember{}).Error)

	err := fixture.svc.Cancel(
		productionReviewServiceContext(productionReviewTenantAdminID, types.TenantRoleAdmin),
		request.ID, "stale admin",
	)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionReviewRevokedRoleAtMutationBoundaryCannotDecide(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request := fixture.submit(t)
	require.NoError(t, fixture.db.Where(
		"project_id = ? AND user_id = ? AND role = ?",
		productionReviewProjectID, productionReviewBusinessID, types.ProductionRoleBusinessReviewer,
	).Delete(&types.ProductionProjectMember{}).Error)

	err := fixture.svc.Decide(
		productionReviewServiceContext(productionReviewBusinessID, types.TenantRoleContributor),
		request.Steps[0].ID, types.ProductionReviewApproved, "stale role",
	)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionReviewRemovedTenantAtProfessionalDecisionBoundaryCannotDecide(t *testing.T) {
	fixture := newProductionReviewFixture(t)
	request := fixture.submit(t)
	fixture.svc.reviews = &revokingProfessionalMutationRepository{
		ProductionReviewRepository: fixture.reviews, db: fixture.db,
		actorID: productionReviewBusinessID, removeOnDecide: true,
	}

	err := fixture.svc.Decide(
		productionReviewServiceContext(productionReviewBusinessID, types.TenantRoleContributor),
		request.Steps[0].ID, types.ProductionReviewApproved, "removed tenant member",
	)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	loaded, loadErr := fixture.reviews.GetReview(
		productionReviewServiceContext(productionReviewBusinessID, types.TenantRoleContributor),
		productionReviewTenantID, request.ID,
	)
	require.NoError(t, loadErr)
	require.Equal(t, types.ProductionReviewDecision(types.ProductionReviewPending), loaded.Steps[0].Decision)
}

func productionCategory(value types.ProductionAnnotationCategory) *types.ProductionAnnotationCategory {
	return &value
}
