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
	serviceProjectID = "aaaaaaaa-1111-4111-8111-111111111111"
	serviceTypeID    = "22222222-2222-4222-8222-222222222222"
	serviceSetID     = "33333333-3333-4333-8333-333333333333"
	serviceItemID    = "44444444-4444-4444-8444-444444444444"
)

type productionSourceAuthorizerStub struct {
	err     error
	calls   int
	project string
	roles   []types.ProductionRole
}

func (a *productionSourceAuthorizerStub) RequireProjectRole(_ context.Context, projectID string, roles ...types.ProductionRole) error {
	a.calls++
	a.project = projectID
	a.roles = append([]types.ProductionRole(nil), roles...)
	return a.err
}

type productionSourceResourceCatalogStub struct {
	resource *types.StoredResource
	err      error
	resolved string
	bound    interfaces.ResourceBindingRequirement
	boundErr error
}

func (r *productionSourceResourceCatalogStub) Register(context.Context, uint64, string, interfaces.ResourceRegistration) (string, error) {
	return "", errors.New("unexpected register")
}
func (r *productionSourceResourceCatalogStub) Resolve(_ context.Context, reference string) (*types.StoredResource, error) {
	r.resolved = reference
	return r.resource, r.err
}
func (r *productionSourceResourceCatalogStub) ResolveBound(_ context.Context, reference string, requirement interfaces.ResourceBindingRequirement) (*types.StoredResource, error) {
	r.resolved = reference
	r.bound = requirement
	return r.resource, r.boundErr
}
func (r *productionSourceResourceCatalogStub) ResolvePath(context.Context, string) (string, *types.StoredResource, error) {
	return "", nil, errors.New("unexpected resolve path")
}
func (r *productionSourceResourceCatalogStub) Bind(context.Context, string, string, string, string) error {
	return errors.New("unexpected bind")
}
func (r *productionSourceResourceCatalogStub) MarkDeleted(context.Context, string) error {
	return errors.New("unexpected delete")
}
func (r *productionSourceResourceCatalogStub) CreateAccessGrant(context.Context, string, time.Duration) (string, error) {
	return "", errors.New("unexpected grant")
}
func (r *productionSourceResourceCatalogStub) ResolveAccessGrant(context.Context, string) (*types.StoredResource, error) {
	return nil, errors.New("unexpected grant resolve")
}

func newProductionSourceServiceFixture(t *testing.T) (*productionSourceService, interfaces.ProductionSourceRepository, *gorm.DB, *productionSourceAuthorizerStub, *productionSourceResourceCatalogStub) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "service-source.db") + "?_foreign_keys=1&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	for _, name := range []string{"000001_knowledge_production_foundation.up.sql", "000002_knowledge_production_documents.up.sql", "000003_knowledge_production_runs.up.sql"} {
		migration, readErr := os.ReadFile(filepath.Join(filepath.Dir(filename), "../../../migrations/sqlite", name))
		require.NoError(t, readErr)
		require.NoError(t, db.Exec(string(migration)).Error)
	}
	require.NoError(t, db.Exec(`ALTER TABLE production_document_types ADD COLUMN template_key VARCHAR(255)`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE production_document_types ADD COLUMN origin VARCHAR(16) NOT NULL DEFAULT 'custom'`).Error)
	require.NoError(t, db.AutoMigrate(&types.AuditLog{}))
	require.NoError(t, db.Create(&types.ProductionProject{
		ID: serviceProjectID, TenantID: 7, Name: "Project", OwnerUserID: "owner", Status: types.ProductionProjectActive,
	}).Error)
	require.NoError(t, db.Create(&types.ProductionDocumentType{
		ID: serviceTypeID, TenantID: 7, Code: "type", Name: "Type", SchemaVersion: 1,
		BlockSchema: types.JSON(`{}`), SourceRequirements: types.JSON(`{}`), SkillBindings: types.JSON(`{}`),
		QualityRules: types.JSON(`{}`), ReviewPolicy: types.JSON(`{}`), PublicationPolicy: types.JSON(`{}`),
		Status: types.ProductionDocumentTypeActive, CreatedBy: "owner",
	}).Error)
	repo := apprepository.NewProductionSourceRepository(db)
	authorizer := &productionSourceAuthorizerStub{}
	resources := &productionSourceResourceCatalogStub{}
	return NewProductionSourceService(
		repo, authorizer, resources, &productionAuditServiceStub{}, apprepository.NewProductionUnitOfWork(db),
	), repo, db, authorizer, resources
}

func sourceServiceContext(tenantID uint64) context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenantID)
	return context.WithValue(ctx, types.UserIDContextKey, "author")
}

func createServiceSourceSet(t *testing.T, repo interfaces.ProductionSourceRepository, status types.ProductionSourceSetStatus) {
	t.Helper()
	require.NoError(t, repo.CreateSet(context.Background(), &types.ProductionSourceSet{
		ID: serviceSetID, TenantID: 7, ProjectID: serviceProjectID, DocumentTypeID: serviceTypeID,
		Status: status, CreatedBy: "author",
	}))
}

func createServiceSourceItem(t *testing.T, repo interfaces.ProductionSourceRepository, status types.ProductionSourceItemStatus) {
	t.Helper()
	require.NoError(t, repo.CreateItem(context.Background(), 7, serviceSetID, &types.ProductionSourceItem{
		ID: serviceItemID, SourceSetID: serviceSetID, SourceKind: types.ProductionSourceKindManual,
		Title: "Source", MimeType: "text/plain", ContentDigest: strings.Repeat("a", 64),
		CapturedAt: time.Now().UTC(), Metadata: types.JSON(`{}`), Status: status,
	}))
}

func TestProductionSourceServiceListsProjectSetsForAuthorizedReaders(t *testing.T) {
	svc, repo, _, authorizer, _ := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetReady)

	sets, err := svc.ListSets(sourceServiceContext(7), serviceProjectID)

	require.NoError(t, err)
	require.Len(t, sets, 1)
	require.Equal(t, serviceSetID, sets[0].ID)
	require.Equal(t, serviceProjectID, authorizer.project)
	require.Equal(t, allProductionProjectRoles, authorizer.roles)
}

func TestProductionSourceServiceDoesNotListBeforeAuthorization(t *testing.T) {
	svc, repo, _, authorizer, _ := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetReady)
	authorizer.err = types.ErrProductionForbidden

	sets, err := svc.ListSets(sourceServiceContext(7), serviceProjectID)

	require.Nil(t, sets)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionSourceWritesRequireProjectOwnerOrAuthor(t *testing.T) {
	svc, repo, _, authorizer, _ := newProductionSourceServiceFixture(t)
	authorizer.err = types.ErrProductionForbidden
	ctx := sourceServiceContext(7)

	created, err := svc.CreateSet(ctx, interfaces.CreateProductionSourceSetInput{
		ProjectID: serviceProjectID, DocumentTypeID: serviceTypeID,
	})
	require.Nil(t, created)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Equal(t, []types.ProductionRole{types.ProductionRoleProjectOwner, types.ProductionRoleAuthor}, authorizer.roles)

	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createServiceSourceItem(t, repo, types.ProductionSourceItemCandidate)
	_, err = svc.AddItem(ctx, serviceSetID, interfaces.CreateProductionSourceItemInput{
		SourceKind: types.ProductionSourceKindManual, Title: "Late", MimeType: "text/plain",
		ContentDigest: strings.Repeat("b", 64), CapturedAt: time.Now().UTC(), Metadata: types.JSON(`{}`),
	})
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.ErrorIs(t, svc.DecideItem(ctx, serviceItemID, types.ProductionSourceItemAccepted), types.ErrProductionForbidden)
	_, err = svc.AttachEvidence(ctx, serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		SnapshotType: types.ProductionEvidenceSnapshotText, InlineContent: types.JSON(`"evidence"`),
	})
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.ErrorIs(t, svc.Freeze(ctx, serviceSetID), types.ErrProductionForbidden)
	require.Equal(t, 5, authorizer.calls)
}

func TestProductionSourceServiceCreatesUUIDsAndDerivesTenantAndActor(t *testing.T) {
	svc, _, _, authorizer, _ := newProductionSourceServiceFixture(t)
	created, err := svc.CreateSet(sourceServiceContext(7), interfaces.CreateProductionSourceSetInput{
		ProjectID: serviceProjectID, DocumentTypeID: serviceTypeID,
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.Equal(t, uint64(7), created.TenantID)
	require.Equal(t, "author", created.CreatedBy)
	require.Equal(t, types.ProductionSourceSetCollecting, created.Status)
	require.Equal(t, serviceProjectID, authorizer.project)
}

func TestProductionSourceServiceRejectsNonCanonicalAggregateUUIDsBeforeRepositoryUse(t *testing.T) {
	canonical := serviceProjectID
	variants := map[string]string{
		"raw":       strings.ReplaceAll(canonical, "-", ""),
		"braced":    "{" + canonical + "}",
		"uppercase": strings.ToUpper(canonical),
		"urn":       "urn:uuid:" + canonical,
	}
	for name, value := range variants {
		t.Run(name, func(t *testing.T) {
			svc, _, db, authorizer, _ := newProductionSourceServiceFixture(t)
			created, err := svc.CreateSet(sourceServiceContext(7), interfaces.CreateProductionSourceSetInput{
				ProjectID: value, DocumentTypeID: serviceTypeID,
			})
			require.Nil(t, created)
			require.ErrorContains(t, err, "canonical UUID")
			require.Zero(t, authorizer.calls)
			var count int64
			require.NoError(t, db.Model(&types.ProductionSourceSet{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestProductionSourceServiceRejectsCrossTenantSourceIDsBeforeAuthorization(t *testing.T) {
	svc, repo, _, authorizer, _ := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)

	_, err := svc.AddItem(sourceServiceContext(8), serviceSetID, interfaces.CreateProductionSourceItemInput{
		SourceKind: types.ProductionSourceKindManual, Title: "Cross", MimeType: "text/plain",
		ContentDigest: strings.Repeat("a", 64), CapturedAt: time.Now().UTC(), Metadata: types.JSON(`{}`),
	})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Zero(t, authorizer.calls)
}

func TestProductionSourceServiceCanonicalizesInlineEvidenceAndComputesSHA256(t *testing.T) {
	svc, repo, db, _, resources := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)

	snapshot, err := svc.AttachEvidence(sourceServiceContext(7), serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		SnapshotType:      types.ProductionEvidenceSnapshotJSON,
		InlineContent:     types.JSON(`{ "b": 2, "a": 1 }`),
		RedactionMetadata: types.JSON(`{"redacted":false}`),
	})
	require.NoError(t, err)
	require.Equal(t, types.JSON(`{"a":1,"b":2}`), snapshot.InlineContent)
	want := sha256.Sum256([]byte(`{"a":1,"b":2}`))
	require.Equal(t, hex.EncodeToString(want[:]), snapshot.ContentDigest)
	require.Empty(t, resources.resolved)

	var persisted types.ProductionEvidenceSnapshot
	require.NoError(t, db.First(&persisted, "id = ?", snapshot.ID).Error)
	require.Equal(t, snapshot.ContentDigest, persisted.ContentDigest)
}

func TestProductionSourceServiceDeterministicEvidenceIsIdempotentAndConflictsOnMismatch(t *testing.T) {
	svc, repo, db, _, _ := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)
	evidenceID := "55555555-5555-4555-8555-555555555555"
	input := interfaces.CreateEvidenceSnapshotInput{
		EvidenceID: evidenceID, SnapshotType: types.ProductionEvidenceSnapshotToolResult,
		InlineContent:     types.JSON(`{"result":"stable"}`),
		RedactionMetadata: types.JSON(`{"provider_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		CapturedByRunID:   "66666666-6666-4666-8666-666666666666",
	}

	first, err := svc.AttachEvidence(sourceServiceContext(7), serviceItemID, input)
	require.NoError(t, err)
	second, err := svc.AttachEvidence(sourceServiceContext(7), serviceItemID, input)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, evidenceID, first.ID)

	var count int64
	require.NoError(t, db.Model(&types.ProductionEvidenceSnapshot{}).Where("id = ?", evidenceID).Count(&count).Error)
	require.Equal(t, int64(1), count)

	input.InlineContent = types.JSON(`{"result":"changed"}`)
	_, err = svc.AttachEvidence(sourceServiceContext(7), serviceItemID, input)
	require.ErrorIs(t, err, types.ErrProductionEvidenceConflict)
	var persisted types.ProductionEvidenceSnapshot
	require.NoError(t, db.First(&persisted, "id = ?", evidenceID).Error)
	require.Equal(t, first.InlineContent, persisted.InlineContent)
}

func TestProductionSourceServiceGetEvidenceReturnsTenantScopedContext(t *testing.T) {
	svc, repo, _, _, _ := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)
	evidenceID := "55555555-5555-4555-8555-555555555555"
	_, err := svc.AttachEvidence(sourceServiceContext(7), serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		EvidenceID: evidenceID, SnapshotType: types.ProductionEvidenceSnapshotJSON, InlineContent: types.JSON(`{"ok":true}`),
	})
	require.NoError(t, err)

	evidence, item, set, err := svc.GetEvidence(sourceServiceContext(7), evidenceID)
	require.NoError(t, err)
	require.Equal(t, evidenceID, evidence.ID)
	require.Equal(t, serviceItemID, item.ID)
	require.Equal(t, serviceSetID, set.ID)

	_, _, _, err = svc.GetEvidence(sourceServiceContext(8), evidenceID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestProductionSourceServiceCanonicalizesInlineEvidenceNumbersExactly(t *testing.T) {
	svc, repo, _, _, _ := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)

	snapshot, err := svc.AttachEvidence(sourceServiceContext(7), serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		SnapshotType:  types.ProductionEvidenceSnapshotJSON,
		InlineContent: types.JSON(`{"n":1.000e0,"large":123456789012345678901234567890}`),
	})
	require.NoError(t, err)
	require.Equal(t, types.JSON(`{"large":1.2345678901234567890123456789e29,"n":1}`), snapshot.InlineContent)

	canonical, err := types.CanonicalProductionJSON(snapshot.InlineContent)
	require.NoError(t, err)
	want := sha256.Sum256(canonical)
	require.Equal(t, hex.EncodeToString(want[:]), snapshot.ContentDigest)
}

func TestProductionSourceServiceRejectsMismatchedInlineDigest(t *testing.T) {
	svc, repo, _, _, _ := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)

	_, err := svc.AttachEvidence(sourceServiceContext(7), serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		SnapshotType:  types.ProductionEvidenceSnapshotText,
		InlineContent: types.JSON(`"evidence"`), ContentDigest: strings.Repeat("f", 64),
	})
	require.ErrorIs(t, err, types.ErrProductionEvidenceDigestMismatch)
}

func TestProductionSourceServiceRejectsNonUUIDRunReference(t *testing.T) {
	svc, repo, _, _, _ := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)

	_, err := svc.AttachEvidence(sourceServiceContext(7), serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		SnapshotType:  types.ProductionEvidenceSnapshotText,
		InlineContent: types.JSON(`"evidence"`), CapturedByRunID: "run-1",
	})
	require.ErrorContains(t, err, "run id must be a UUID")
}

func TestProductionSourceServiceNormalizesRunReferenceBeforePersistence(t *testing.T) {
	canonical := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	variants := map[string]string{
		"raw":       strings.ReplaceAll(canonical, "-", ""),
		"braced":    "{" + canonical + "}",
		"uppercase": strings.ToUpper(canonical),
		"urn":       "urn:uuid:" + canonical,
	}
	for name, value := range variants {
		t.Run(name, func(t *testing.T) {
			svc, repo, db, _, _ := newProductionSourceServiceFixture(t)
			createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
			createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)
			snapshot, err := svc.AttachEvidence(sourceServiceContext(7), serviceItemID, interfaces.CreateEvidenceSnapshotInput{
				SnapshotType:  types.ProductionEvidenceSnapshotText,
				InlineContent: types.JSON(`"evidence"`), CapturedByRunID: value,
			})
			require.NoError(t, err)
			require.Equal(t, canonical, snapshot.CapturedByRunID)
			require.Len(t, snapshot.CapturedByRunID, 36)

			var persisted types.ProductionEvidenceSnapshot
			require.NoError(t, db.First(&persisted, "id = ?", snapshot.ID).Error)
			require.Equal(t, canonical, persisted.CapturedByRunID)
		})
	}
}

func TestProductionSourceServiceResolvesTenantScopedResourceReferenceAndUsesRegistryDigest(t *testing.T) {
	svc, repo, _, _, resources := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)
	resources.resource = &types.StoredResource{
		ID: "resource-id", Handle: strings.Repeat("a", types.ResourceHandleLength), TenantID: 7,
		ContentHash: strings.Repeat("c", 64), State: types.ResourceStateActive,
		Lifecycle: types.ResourceLifecyclePersistent,
	}
	reference := types.BuildResourcePath(resources.resource.Handle)

	snapshot, err := svc.AttachEvidence(sourceServiceContext(7), serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		SnapshotType: types.ProductionEvidenceSnapshotFile, ResourceReference: reference,
	})
	require.NoError(t, err)
	require.Equal(t, reference, snapshot.StoragePath)
	require.Equal(t, strings.Repeat("c", 64), snapshot.ContentDigest)
	require.Equal(t, reference, resources.resolved)
	require.Equal(t, interfaces.ResourceBindingRequirement{
		TenantID: 7, OwnerType: types.ResourceOwnerTypeProductionProject,
		OwnerID: serviceProjectID,
	}, resources.bound)
}

func TestProductionSourceServiceRejectsSameTenantResourceBoundToAnotherProject(t *testing.T) {
	svc, repo, db, _, resources := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)
	resources.resource = &types.StoredResource{
		ID: "resource-id", Handle: strings.Repeat("a", types.ResourceHandleLength), TenantID: 7,
		ContentHash: strings.Repeat("c", 64), State: types.ResourceStateActive,
		Lifecycle: types.ResourceLifecyclePersistent,
	}
	resources.boundErr = types.ErrResourceBindingNotFound

	snapshot, err := svc.AttachEvidence(sourceServiceContext(7), serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		SnapshotType:      types.ProductionEvidenceSnapshotFile,
		ResourceReference: types.BuildResourcePath(resources.resource.Handle),
	})

	require.Nil(t, snapshot)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	var count int64
	require.NoError(t, db.Model(&types.ProductionEvidenceSnapshot{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProductionSourceServiceCanonicalizesResourceReferenceBeforeResolveAndPersistence(t *testing.T) {
	svc, repo, db, _, resources := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)
	resources.resource = &types.StoredResource{
		ID: "resource-id", Handle: strings.Repeat("a", types.ResourceHandleLength), TenantID: 7,
		ContentHash: strings.Repeat("c", 64), State: types.ResourceStateActive,
		Lifecycle: types.ResourceLifecyclePersistent,
	}
	canonical := types.BuildResourcePath(resources.resource.Handle)

	snapshot, err := svc.AttachEvidence(sourceServiceContext(7), serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		SnapshotType: types.ProductionEvidenceSnapshotFile, ResourceReference: "  " + canonical + "  ",
	})
	require.NoError(t, err)
	require.Equal(t, canonical, resources.resolved)
	require.Equal(t, canonical, snapshot.StoragePath)

	var persisted types.ProductionEvidenceSnapshot
	require.NoError(t, db.First(&persisted, "id = ?", snapshot.ID).Error)
	require.Equal(t, canonical, persisted.StoragePath)
}

func TestProductionSourceServiceRejectsPhysicalPathsMutableResourcesAndCrossTenantResources(t *testing.T) {
	tests := []struct {
		name      string
		reference string
		resource  *types.StoredResource
		want      error
	}{
		{name: "physical path", reference: "s3://bucket/key", want: types.ErrProductionEvidenceResourceInvalid},
		{name: "missing digest", reference: types.BuildResourcePath(strings.Repeat("a", types.ResourceHandleLength)), resource: &types.StoredResource{TenantID: 7}, want: types.ErrProductionEvidenceResourceInvalid},
		{name: "cross tenant", reference: types.BuildResourcePath(strings.Repeat("a", types.ResourceHandleLength)), resource: &types.StoredResource{TenantID: 8, ContentHash: strings.Repeat("d", 64)}, want: types.ErrProductionForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, repo, _, _, resources := newProductionSourceServiceFixture(t)
			createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
			createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)
			resources.resource = test.resource
			_, err := svc.AttachEvidence(sourceServiceContext(7), serviceItemID, interfaces.CreateEvidenceSnapshotInput{
				SnapshotType: types.ProductionEvidenceSnapshotFile, ResourceReference: test.reference,
			})
			require.ErrorIs(t, err, test.want)
		})
	}
}

func TestProductionSourceServiceFreezeRequiresAcceptedEvidenceAndRejectsFrozenMutation(t *testing.T) {
	svc, repo, _, _, _ := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)
	ctx := sourceServiceContext(7)

	require.ErrorIs(t, svc.Freeze(ctx, serviceSetID), types.ErrProductionEvidenceMissing)
	_, err := svc.AttachEvidence(ctx, serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		SnapshotType: types.ProductionEvidenceSnapshotText, InlineContent: types.JSON(`"evidence"`),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Freeze(ctx, serviceSetID))
	_, err = svc.AddItem(ctx, serviceSetID, interfaces.CreateProductionSourceItemInput{
		SourceKind: types.ProductionSourceKindManual, Title: "Late", MimeType: "text/plain",
		ContentDigest: strings.Repeat("e", 64), CapturedAt: time.Now().UTC(), Metadata: types.JSON(`{}`),
	})
	require.ErrorIs(t, err, types.ErrProductionSourceSetFrozen)
}

func TestProductionSourceServiceFrozenEvidenceRejectsAuthorWithGuessedRunAndCall(t *testing.T) {
	svc, repo, db, _, _ := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)
	ctx := sourceServiceContext(7)
	_, err := svc.AttachEvidence(ctx, serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		SnapshotType: types.ProductionEvidenceSnapshotText, InlineContent: types.JSON(`"seed"`),
	})
	require.NoError(t, err)
	documentID := "71000000-0000-4000-8000-000000000001"
	runID := "71000000-0000-4000-8000-000000000002"
	require.NoError(t, db.Create(&types.ProductionDocument{
		ID: documentID, TenantID: 7, ProjectID: serviceProjectID, DocumentTypeID: serviceTypeID,
		DocumentTypeSchemaVersion: 1, Title: "Document", CreatedBy: "author",
	}).Error)
	require.NoError(t, db.Create(&types.ProductionRun{
		ID: runID, TenantID: 7, ProjectID: serviceProjectID, DocumentID: types.ProductionDocumentID(documentID), SourceSetID: serviceSetID,
		RunType: types.ProductionRunWrite, Status: types.ProductionRunRunning, Attempt: 1, CurrentStep: 0,
		WakeupVersion: 1, StatePayload: types.JSON(`{}`), ModelID: "model", DocumentTypeSnapshot: types.JSON(`{}`),
		IdempotencyKey:       "run-evidence-test",
		WorkflowPlanSnapshot: types.JSON(`{"steps":[],"version":1}`),
		WorkflowPlanDigest:   "a5dd3ce7993c63ad01d8a9a45922bc5f17d2c41c5f21a10671ec8c05c5ffc4aa",
	}).Error)
	require.NoError(t, svc.Freeze(ctx, serviceSetID))

	input := interfaces.CreateEvidenceSnapshotInput{
		EvidenceID: "71000000-0000-4000-8000-000000000003", SnapshotType: types.ProductionEvidenceSnapshotToolResult,
		InlineContent: types.JSON(`{"result":"ok"}`), CapturedByRunID: runID,
		CapturedByToolCallID: "71000000-0000-4000-8000-000000000004",
	}
	_, err = svc.AttachEvidence(ctx, serviceItemID, input)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionSourceServiceAcceptsOnlyExactRunPrincipalForFrozenEvidence(t *testing.T) {
	svc, repo, db, authorizer, _ := newProductionSourceServiceFixture(t)
	authorizer.err = types.ErrProductionForbidden
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)
	ctx := sourceServiceContext(7)
	_, err := svc.AttachEvidence(ctx, serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		SnapshotType: types.ProductionEvidenceSnapshotText, InlineContent: types.JSON(`"seed"`),
	})
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	authorizer.err = nil
	_, err = svc.AttachEvidence(ctx, serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		SnapshotType: types.ProductionEvidenceSnapshotText, InlineContent: types.JSON(`"seed"`),
	})
	require.NoError(t, err)
	documentID := "72000000-0000-4000-8000-000000000001"
	runID := "72000000-0000-4000-8000-000000000002"
	require.NoError(t, db.Create(&types.ProductionDocument{
		ID: documentID, TenantID: 7, ProjectID: serviceProjectID, DocumentTypeID: serviceTypeID,
		DocumentTypeSchemaVersion: 1, Title: "Document", CreatedBy: "author",
	}).Error)
	require.NoError(t, db.Create(&types.ProductionRun{
		ID: runID, TenantID: 7, ProjectID: serviceProjectID, DocumentID: types.ProductionDocumentID(documentID), SourceSetID: serviceSetID,
		RunType: types.ProductionRunWrite, Status: types.ProductionRunRunning, Attempt: 1,
		WakeupVersion: 1, StatePayload: types.JSON(`{}`), ModelID: "model", DocumentTypeSnapshot: types.JSON(`{}`), IdempotencyKey: runID,
		WorkflowPlanSnapshot: types.JSON(`{"steps":[],"version":1}`),
		WorkflowPlanDigest:   "a5dd3ce7993c63ad01d8a9a45922bc5f17d2c41c5f21a10671ec8c05c5ffc4aa",
	}).Error)
	startedAt := time.Now().UTC()
	call := &types.ProductionToolCall{
		ID: "72000000-0000-4000-8000-000000000003", RunID: runID, TenantID: 7,
		ProjectID: serviceProjectID, DocumentID: types.ProductionDocumentID(documentID), SourceSetID: serviceSetID,
		Attempt: 1, CurrentStep: 0, IdempotencyKey: "principal-frozen-evidence",
		ProviderType: types.ProductionToolProviderMCP, ProviderID: "provider", ToolName: "lookup",
		RequestSnapshot: types.JSON(`{}`), RequestDigest: strings.Repeat("a", 64),
		Status: types.ProductionToolCallExecuting, ApprovalStatus: types.ProductionToolApprovalNotRequired, StartedAt: &startedAt,
	}
	require.NoError(t, db.Create(call).Error)
	require.NoError(t, svc.Freeze(ctx, serviceSetID))
	authorizer.err = types.ErrProductionForbidden
	principalCtx, err := types.WithProductionInternalPrincipal(context.Background(), types.ProductionInternalPrincipal{
		ActorID: types.ProductionSystemActorID, ActorKind: types.ProductionInternalActorWorker,
		TenantID: 7, ProjectID: serviceProjectID, RunID: runID,
	})
	require.NoError(t, err)
	evidenceID, err := types.ProductionToolEvidenceID(call)
	require.NoError(t, err)
	_, err = svc.AttachEvidence(principalCtx, serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		EvidenceID: evidenceID, SnapshotType: types.ProductionEvidenceSnapshotToolResult,
		InlineContent: types.JSON(`{"ok":true}`), CapturedByRunID: runID, CapturedByToolCallID: call.ID,
	})
	require.NoError(t, err)

	mismatchCtx, err := types.WithProductionInternalPrincipal(context.Background(), types.ProductionInternalPrincipal{
		ActorID: types.ProductionSystemActorID, ActorKind: types.ProductionInternalActorWorker,
		TenantID: 7, ProjectID: "72000000-0000-4000-8000-000000000099", RunID: runID,
	})
	require.NoError(t, err)
	_, err = svc.AttachEvidence(mismatchCtx, serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		EvidenceID: evidenceID, SnapshotType: types.ProductionEvidenceSnapshotToolResult,
		InlineContent: types.JSON(`{"ok":true}`), CapturedByRunID: runID, CapturedByToolCallID: call.ID,
	})
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionSourceServiceFrozenEvidenceRequiresExactInternalToolCallIdentity(t *testing.T) {
	svc, repo, db, _, _ := newProductionSourceServiceFixture(t)
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	createServiceSourceItem(t, repo, types.ProductionSourceItemAccepted)
	authorCtx := sourceServiceContext(7)
	_, err := svc.AttachEvidence(authorCtx, serviceItemID, interfaces.CreateEvidenceSnapshotInput{
		SnapshotType: types.ProductionEvidenceSnapshotText, InlineContent: types.JSON(`"seed"`),
	})
	require.NoError(t, err)
	documentID := "73000000-0000-4000-8000-000000000001"
	runID := "73000000-0000-4000-8000-000000000002"
	callID := "73000000-0000-4000-8000-000000000003"
	require.NoError(t, db.Create(&types.ProductionDocument{
		ID: documentID, TenantID: 7, ProjectID: serviceProjectID, DocumentTypeID: serviceTypeID,
		DocumentTypeSchemaVersion: 1, Title: "Document", CreatedBy: "author",
	}).Error)
	require.NoError(t, db.Create(&types.ProductionRun{
		ID: runID, TenantID: 7, ProjectID: serviceProjectID, DocumentID: types.ProductionDocumentID(documentID), SourceSetID: serviceSetID,
		RunType: types.ProductionRunWrite, Status: types.ProductionRunRunning, Attempt: 1,
		WakeupVersion: 1, StatePayload: types.JSON(`{}`), ModelID: "model", DocumentTypeSnapshot: types.JSON(`{}`), IdempotencyKey: runID,
		WorkflowPlanSnapshot: types.JSON(`{"steps":[],"version":1}`),
		WorkflowPlanDigest:   "a5dd3ce7993c63ad01d8a9a45922bc5f17d2c41c5f21a10671ec8c05c5ffc4aa",
	}).Error)
	startedAt := time.Now().UTC()
	call := &types.ProductionToolCall{
		ID: callID, RunID: runID, TenantID: 7, ProjectID: serviceProjectID,
		DocumentID: types.ProductionDocumentID(documentID), SourceSetID: serviceSetID,
		Attempt: 1, CurrentStep: 0, IdempotencyKey: callID,
		ProviderType: types.ProductionToolProviderMCP, ProviderID: "provider", ToolName: "lookup",
		RequestSnapshot: types.JSON(`{}`), RequestDigest: strings.Repeat("a", 64),
		Status: types.ProductionToolCallExecuting, ApprovalStatus: types.ProductionToolApprovalNotRequired, StartedAt: &startedAt,
	}
	require.NoError(t, db.Create(call).Error)
	require.NoError(t, svc.Freeze(authorCtx, serviceSetID))
	evidenceID, err := types.ProductionToolEvidenceID(call)
	require.NoError(t, err)
	input := interfaces.CreateEvidenceSnapshotInput{
		EvidenceID: evidenceID, SnapshotType: types.ProductionEvidenceSnapshotToolResult,
		InlineContent: types.JSON(`{"ok":true}`), CapturedByRunID: runID, CapturedByToolCallID: callID,
	}

	_, err = svc.AttachEvidence(authorCtx, serviceItemID, input)
	require.ErrorIs(t, err, types.ErrProductionForbidden)

	principalCtx, err := types.WithProductionInternalPrincipal(context.Background(), types.ProductionInternalPrincipal{
		ActorID: types.ProductionSystemActorID, ActorKind: types.ProductionInternalActorWorker,
		TenantID: 7, ProjectID: serviceProjectID, RunID: runID,
	})
	require.NoError(t, err)
	first, err := svc.AttachEvidence(principalCtx, serviceItemID, input)
	require.NoError(t, err)
	require.Equal(t, callID, first.CapturedByToolCallID)
	second, err := svc.AttachEvidence(principalCtx, serviceItemID, input)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)

	conflict := input
	conflict.InlineContent = types.JSON(`{"ok":false}`)
	_, err = svc.AttachEvidence(principalCtx, serviceItemID, conflict)
	require.ErrorIs(t, err, types.ErrProductionEvidenceConflict)

	for name, mutate := range map[string]func(*interfaces.CreateEvidenceSnapshotInput){
		"wrong call": func(in *interfaces.CreateEvidenceSnapshotInput) {
			in.CapturedByToolCallID = "73000000-0000-4000-8000-000000000099"
		},
		"wrong evidence": func(in *interfaces.CreateEvidenceSnapshotInput) {
			in.EvidenceID = "73000000-0000-4000-8000-000000000098"
		},
		"wrong type": func(in *interfaces.CreateEvidenceSnapshotInput) {
			in.SnapshotType = types.ProductionEvidenceSnapshotJSON
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := input
			mutate(&candidate)
			_, err := svc.AttachEvidence(principalCtx, serviceItemID, candidate)
			require.Error(t, err)
		})
	}

	wrongRunCtx, err := types.WithProductionInternalPrincipal(context.Background(), types.ProductionInternalPrincipal{
		ActorID: types.ProductionSystemActorID, ActorKind: types.ProductionInternalActorWorker,
		TenantID: 7, ProjectID: serviceProjectID, RunID: "73000000-0000-4000-8000-000000000097",
	})
	require.NoError(t, err)
	_, err = svc.AttachEvidence(wrongRunCtx, serviceItemID, input)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionSourceServiceReturnsRequiredFreezeAuditFailure(t *testing.T) {
	svc, repo, _, _, _ := newProductionSourceServiceFixture(t)
	audit := &productionAuditServiceStub{err: errors.New("governed audit unavailable")}
	svc.audit = audit
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)

	err := svc.Freeze(sourceServiceContext(7), serviceSetID)

	require.ErrorContains(t, err, "governed audit unavailable")
}

func TestProductionSourceDirectServiceAuditFailureRollsBackFreeze(t *testing.T) {
	svc, repo, db, _, _ := newProductionSourceServiceFixture(t)
	svc.audit = NewAuditLogService(apprepository.NewAuditLogRepository(db))
	createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
	require.NoError(t, installServiceAuditFailureTrigger(db))

	err := svc.Freeze(sourceServiceContext(7), serviceSetID)

	require.ErrorContains(t, err, "forced governed audit failure")
	set, getErr := repo.GetSet(context.Background(), 7, serviceSetID)
	require.NoError(t, getErr)
	require.Equal(t, types.ProductionSourceSetCollecting, set.Status)
	var auditCount int64
	require.NoError(t, db.Model(&types.AuditLog{}).Count(&auditCount).Error)
	require.Zero(t, auditCount)

	require.NoError(t, db.Exec(`DROP TRIGGER fail_governed_audit_insert`).Error)
	require.NoError(t, svc.Freeze(sourceServiceContext(7), serviceSetID))
	set, getErr = repo.GetSet(context.Background(), 7, serviceSetID)
	require.NoError(t, getErr)
	require.Equal(t, types.ProductionSourceSetFrozen, set.Status)
	require.NoError(t, db.Model(&types.AuditLog{}).Count(&auditCount).Error)
	require.Equal(t, int64(1), auditCount)
}

func TestProductionSourceNestedUnitOfWorkIsAtomicInsideOuterTransaction(t *testing.T) {
	t.Run("swallowed audit failure rolls back to savepoint", func(t *testing.T) {
		svc, repo, db, _, _ := newProductionSourceServiceFixture(t)
		svc.audit = NewAuditLogService(apprepository.NewAuditLogRepository(db))
		createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)
		require.NoError(t, installServiceAuditFailureTrigger(db))
		var serviceErr error

		outerErr := database.WithTransactionContext(sourceServiceContext(7), db, func(txCtx context.Context) error {
			serviceErr = svc.Freeze(txCtx, serviceSetID)
			return nil
		})

		require.NoError(t, outerErr)
		require.ErrorContains(t, serviceErr, "forced governed audit failure")
		set, err := repo.GetSet(context.Background(), 7, serviceSetID)
		require.NoError(t, err)
		require.Equal(t, types.ProductionSourceSetCollecting, set.Status)
		var auditCount int64
		require.NoError(t, db.Model(&types.AuditLog{}).Count(&auditCount).Error)
		require.Zero(t, auditCount)
	})

	t.Run("success commits", func(t *testing.T) {
		svc, repo, db, _, _ := newProductionSourceServiceFixture(t)
		svc.audit = NewAuditLogService(apprepository.NewAuditLogRepository(db))
		createServiceSourceSet(t, repo, types.ProductionSourceSetCollecting)

		err := database.WithTransactionContext(sourceServiceContext(7), db, func(txCtx context.Context) error {
			return svc.Freeze(txCtx, serviceSetID)
		})

		require.NoError(t, err)
		set, err := repo.GetSet(context.Background(), 7, serviceSetID)
		require.NoError(t, err)
		require.Equal(t, types.ProductionSourceSetFrozen, set.Status)
		var auditCount int64
		require.NoError(t, db.Model(&types.AuditLog{}).Count(&auditCount).Error)
		require.Equal(t, int64(1), auditCount)
	})
}
