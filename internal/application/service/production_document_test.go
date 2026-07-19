package service

import (
	"context"
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
	documentServiceProjectID = "11111111-1111-4111-8111-111111111111"
	documentServiceTypeID    = "22222222-2222-4222-8222-222222222222"
	documentServiceSetID     = "33333333-3333-4333-8333-333333333333"
)

type productionDocumentAuthorizerStub struct {
	err     error
	project string
	roles   []types.ProductionRole
}

func (a *productionDocumentAuthorizerStub) RequireProjectRole(_ context.Context, projectID string, roles ...types.ProductionRole) error {
	a.project = projectID
	a.roles = append([]types.ProductionRole(nil), roles...)
	return a.err
}

func newProductionDocumentServiceFixture(t *testing.T) (*productionDocumentService, interfaces.ProductionDocumentRepository, *gorm.DB, *productionDocumentAuthorizerStub) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "document-service.db") + "?_foreign_keys=1&_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	for _, name := range []string{"000001_knowledge_production_foundation.up.sql", "000002_knowledge_production_documents.up.sql"} {
		migration, readErr := os.ReadFile(filepath.Join(filepath.Dir(filename), "../../../migrations/sqlite", name))
		require.NoError(t, readErr)
		require.NoError(t, db.Exec(string(migration)).Error)
	}
	require.NoError(t, db.Create(&types.ProductionProject{
		ID: documentServiceProjectID, TenantID: 7, Name: "Project", OwnerUserID: "owner", Status: types.ProductionProjectActive,
	}).Error)
	require.NoError(t, db.Create(&types.ProductionDocumentType{
		ID: documentServiceTypeID, TenantID: 7, Code: "baseline", Name: "Baseline", SchemaVersion: 3,
		BlockSchema: types.JSON(`{}`), SourceRequirements: types.JSON(`{}`), SkillBindings: types.JSON(`{}`),
		QualityRules: types.JSON(`{}`), ReviewPolicy: types.JSON(`{}`), PublicationPolicy: types.JSON(`{}`),
		Status: types.ProductionDocumentTypeActive, CreatedBy: "owner",
	}).Error)
	frozenAt := time.Now().UTC()
	require.NoError(t, db.Create(&types.ProductionSourceSet{
		ID: documentServiceSetID, TenantID: 7, ProjectID: documentServiceProjectID, DocumentTypeID: documentServiceTypeID,
		Status: types.ProductionSourceSetFrozen, CreatedBy: "author", FrozenAt: &frozenAt,
	}).Error)
	documents := apprepository.NewProductionDocumentRepository(db)
	authorizer := &productionDocumentAuthorizerStub{}
	service := NewProductionDocumentService(
		documents,
		apprepository.NewProductionSourceRepository(db),
		apprepository.NewProductionDocumentTypeRepository(db),
		authorizer,
		nil,
	)
	return service, documents, db, authorizer
}

func productionDocumentContext(tenantID uint64) context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenantID)
	return context.WithValue(ctx, types.UserIDContextKey, "author")
}

func createServiceDocument(t *testing.T, svc *productionDocumentService) *types.ProductionDocument {
	t.Helper()
	document, err := svc.CreateDocument(productionDocumentContext(7), interfaces.CreateProductionDocumentInput{
		ProjectID: documentServiceProjectID, DocumentTypeID: documentServiceTypeID,
		SourceSetID: documentServiceSetID, Title: "Baseline",
	})
	require.NoError(t, err)
	return document
}

func serviceParagraph(logicalID, content string) types.ProductionDocumentBlockInput {
	return types.ProductionDocumentBlockInput{
		LogicalBlockID: logicalID, BlockType: "paragraph", Content: types.JSON(content),
		Attributes: types.JSON(`{}`), EvidenceRefs: types.JSON(`[]`), AIProvenance: types.JSON(`{}`),
	}
}

func TestProductionDocumentServiceRequiresFrozenSourceSetAndActiveType(t *testing.T) {
	t.Run("source set", func(t *testing.T) {
		svc, _, db, _ := newProductionDocumentServiceFixture(t)
		collectingSetID := "44444444-4444-4444-8444-444444444444"
		require.NoError(t, db.Create(&types.ProductionSourceSet{
			ID: collectingSetID, TenantID: 7, ProjectID: documentServiceProjectID,
			DocumentTypeID: documentServiceTypeID, Status: types.ProductionSourceSetCollecting, CreatedBy: "author",
		}).Error)
		document, err := svc.CreateDocument(productionDocumentContext(7), interfaces.CreateProductionDocumentInput{
			ProjectID: documentServiceProjectID, DocumentTypeID: documentServiceTypeID,
			SourceSetID: collectingSetID, Title: "Baseline",
		})
		require.Nil(t, document)
		require.ErrorIs(t, err, types.ErrProductionDocumentSourceSetInvalid)
	})

	t.Run("document type", func(t *testing.T) {
		svc, _, db, _ := newProductionDocumentServiceFixture(t)
		require.NoError(t, db.Model(&types.ProductionDocumentType{}).Where("id = ?", documentServiceTypeID).
			Update("status", types.ProductionDocumentTypeRetired).Error)
		document, err := svc.CreateDocument(productionDocumentContext(7), interfaces.CreateProductionDocumentInput{
			ProjectID: documentServiceProjectID, DocumentTypeID: documentServiceTypeID,
			SourceSetID: documentServiceSetID, Title: "Baseline",
		})
		require.Nil(t, document)
		require.ErrorIs(t, err, types.ErrProductionDocumentTypeInactive)
	})
}

func TestProductionDocumentServiceDerivesTenantSchemaAndActor(t *testing.T) {
	svc, _, _, authorizer := newProductionDocumentServiceFixture(t)

	document := createServiceDocument(t, svc)

	require.Equal(t, uint64(7), document.TenantID)
	require.Equal(t, 3, document.DocumentTypeSchemaVersion)
	require.Equal(t, "author", document.CreatedBy)
	require.Equal(t, documentServiceProjectID, authorizer.project)
	require.Equal(t, []types.ProductionRole{types.ProductionRoleProjectOwner, types.ProductionRoleAuthor}, authorizer.roles)
}

func TestProductionDocumentServiceTrimsAndBoundsTitle(t *testing.T) {
	t.Run("accepts exactly 255 characters", func(t *testing.T) {
		svc, _, _, _ := newProductionDocumentServiceFixture(t)
		title := strings.Repeat("a", 255)
		document, err := svc.CreateDocument(productionDocumentContext(7), interfaces.CreateProductionDocumentInput{
			ProjectID: documentServiceProjectID, DocumentTypeID: documentServiceTypeID,
			SourceSetID: documentServiceSetID, Title: "  " + title + "  ",
		})
		require.NoError(t, err)
		require.Equal(t, title, document.Title)
	})

	t.Run("rejects 256 characters", func(t *testing.T) {
		svc, _, db, _ := newProductionDocumentServiceFixture(t)
		document, err := svc.CreateDocument(productionDocumentContext(7), interfaces.CreateProductionDocumentInput{
			ProjectID: documentServiceProjectID, DocumentTypeID: documentServiceTypeID,
			SourceSetID: documentServiceSetID, Title: strings.Repeat("a", 256),
		})
		require.Nil(t, document)
		require.ErrorContains(t, err, "255")
		var count int64
		require.NoError(t, db.Model(&types.ProductionDocument{}).Count(&count).Error)
		require.Zero(t, count)
	})
}

func TestProductionDocumentServiceRequiresOwnerOrAuthorForMutations(t *testing.T) {
	svc, _, _, authorizer := newProductionDocumentServiceFixture(t)
	authorizer.err = types.ErrProductionForbidden

	document, err := svc.CreateDocument(productionDocumentContext(7), interfaces.CreateProductionDocumentInput{
		ProjectID: documentServiceProjectID, DocumentTypeID: documentServiceTypeID,
		SourceSetID: documentServiceSetID, Title: "Baseline",
	})
	require.Nil(t, document)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionDocumentServicePreservesLogicalIDsAndComputesCanonicalDigests(t *testing.T) {
	svc, _, _, _ := newProductionDocumentServiceFixture(t)
	document := createServiceDocument(t, svc)

	version, err := svc.AppendVersion(productionDocumentContext(7), document.ID, interfaces.AppendProductionVersionInput{
		SourceSetID: documentServiceSetID, Origin: types.ProductionDocumentOriginHuman,
		Blocks: []types.ProductionDocumentBlockInput{{
			LogicalBlockID: "block-a", BlockType: "paragraph", Content: types.JSON(`{ "b": 2, "a": 1 }`),
			Attributes: types.JSON(`{ "z": false, "a": true }`), EvidenceRefs: types.JSON(`[]`), AIProvenance: types.JSON(`{}`),
		}},
	})

	require.NoError(t, err)
	require.Equal(t, 1, version.VersionNumber)
	require.Equal(t, "block-a", version.Blocks[0].LogicalBlockID)
	require.Len(t, version.Blocks[0].ContentDigest, 64)
	require.Len(t, version.ContentDigest, 64)
	require.Equal(t, version.Blocks[0].ContentDigest, types.ComputeProductionBlockDigest(version.Blocks[0]))
	require.Equal(t, version.ContentDigest, types.ComputeProductionVersionDigest(version))
	require.Equal(t, types.JSON(`{"a":1,"b":2}`), version.Blocks[0].Content)
}

func TestProductionDocumentServiceRecordsSplitAndMergeLineage(t *testing.T) {
	svc, _, _, _ := newProductionDocumentServiceFixture(t)
	document := createServiceDocument(t, svc)
	first, err := svc.AppendVersion(productionDocumentContext(7), document.ID, interfaces.AppendProductionVersionInput{
		SourceSetID: documentServiceSetID, Origin: types.ProductionDocumentOriginHuman,
		Blocks: []types.ProductionDocumentBlockInput{
			serviceParagraph("block-a", `"a"`), serviceParagraph("block-b", `"b"`), serviceParagraph("block-c", `"c"`),
		},
	})
	require.NoError(t, err)

	second, err := svc.AppendVersion(productionDocumentContext(7), document.ID, interfaces.AppendProductionVersionInput{
		ParentVersionID: first.ID, SourceSetID: documentServiceSetID, Origin: types.ProductionDocumentOriginMixed,
		Blocks: []types.ProductionDocumentBlockInput{
			serviceParagraph("block-a-1", `"a1"`), serviceParagraph("block-a-2", `"a2"`), serviceParagraph("block-bc", `"bc"`),
		},
		Lineage: []types.ProductionBlockLineageInput{
			{FromLogicalBlockID: "block-a", ToLogicalBlockID: "block-a-1", Relation: types.ProductionBlockRelationSplit},
			{FromLogicalBlockID: "block-a", ToLogicalBlockID: "block-a-2", Relation: types.ProductionBlockRelationSplit},
			{FromLogicalBlockID: "block-b", ToLogicalBlockID: "block-bc", Relation: types.ProductionBlockRelationMerged},
			{FromLogicalBlockID: "block-c", ToLogicalBlockID: "block-bc", Relation: types.ProductionBlockRelationMerged},
		},
	})

	require.NoError(t, err)
	require.Len(t, second.Lineage, 4)
	require.Equal(t, first.ID, second.Lineage[0].FromVersionID)
	require.Equal(t, second.ID, second.Lineage[0].ToVersionID)
}

func TestProductionDocumentServiceRejectsCrossProjectSourceSet(t *testing.T) {
	svc, _, db, _ := newProductionDocumentServiceFixture(t)
	otherProject := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	otherSet := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	require.NoError(t, db.Create(&types.ProductionProject{
		ID: otherProject, TenantID: 7, Name: "Other", OwnerUserID: "owner", Status: types.ProductionProjectActive,
	}).Error)
	frozenAt := time.Now().UTC()
	require.NoError(t, db.Create(&types.ProductionSourceSet{
		ID: otherSet, TenantID: 7, ProjectID: otherProject, DocumentTypeID: documentServiceTypeID,
		Status: types.ProductionSourceSetFrozen, CreatedBy: "author", FrozenAt: &frozenAt,
	}).Error)

	document, err := svc.CreateDocument(productionDocumentContext(7), interfaces.CreateProductionDocumentInput{
		ProjectID: documentServiceProjectID, DocumentTypeID: documentServiceTypeID, SourceSetID: otherSet, Title: "Cross",
	})
	require.Nil(t, document)
	require.ErrorIs(t, err, types.ErrProductionDocumentSourceSetInvalid)
}

func TestProductionDocumentServiceScopesReadsByTenantAndProjectAuthorization(t *testing.T) {
	svc, _, _, authorizer := newProductionDocumentServiceFixture(t)
	document := createServiceDocument(t, svc)
	version, err := svc.AppendVersion(productionDocumentContext(7), document.ID, interfaces.AppendProductionVersionInput{
		SourceSetID: documentServiceSetID, Origin: types.ProductionDocumentOriginHuman,
		Blocks: []types.ProductionDocumentBlockInput{serviceParagraph("block-a", `"a"`)},
	})
	require.NoError(t, err)

	got, err := svc.GetVersion(productionDocumentContext(7), version.ID)
	require.NoError(t, err)
	require.Equal(t, version.ID, got.ID)
	authorizer.err = types.ErrProductionForbidden
	got, err = svc.GetVersion(productionDocumentContext(7), version.ID)
	require.Nil(t, got)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	got, err = svc.GetVersion(productionDocumentContext(8), version.ID)
	require.Nil(t, got)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
