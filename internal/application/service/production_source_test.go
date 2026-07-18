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
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	serviceProjectID = "11111111-1111-4111-8111-111111111111"
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
}

func (r *productionSourceResourceCatalogStub) Register(context.Context, uint64, string, interfaces.ResourceRegistration) (string, error) {
	return "", errors.New("unexpected register")
}
func (r *productionSourceResourceCatalogStub) Resolve(_ context.Context, reference string) (*types.StoredResource, error) {
	r.resolved = reference
	return r.resource, r.err
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
	for _, name := range []string{"000001_knowledge_production_foundation.up.sql", "000002_knowledge_production_documents.up.sql"} {
		migration, readErr := os.ReadFile(filepath.Join(filepath.Dir(filename), "../../../migrations/sqlite", name))
		require.NoError(t, readErr)
		require.NoError(t, db.Exec(string(migration)).Error)
	}
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
	return NewProductionSourceService(repo, authorizer, resources), repo, db, authorizer, resources
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
