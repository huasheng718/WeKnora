package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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
	documentServiceProjectID  = "11111111-1111-4111-8111-111111111111"
	documentServiceTypeID     = "22222222-2222-4222-8222-222222222222"
	documentServiceSetID      = "33333333-3333-4333-8333-333333333333"
	documentServiceItemID     = "44444444-4444-4444-8444-444444444444"
	documentServiceEvidenceID = "55555555-5555-4555-8555-555555555555"
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
	return newProductionDocumentServiceFixtureWithEvidenceDigest(t, "")
}

func newProductionDocumentServiceFixtureWithEvidenceDigest(t *testing.T, evidenceDigest string) (*productionDocumentService, interfaces.ProductionDocumentRepository, *gorm.DB, *productionDocumentAuthorizerStub) {
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
	require.NoError(t, db.AutoMigrate(&types.AuditLog{}))
	require.NoError(t, db.Create(&types.ProductionProject{
		ID: documentServiceProjectID, TenantID: 7, Name: "Project", OwnerUserID: "owner", Status: types.ProductionProjectActive,
	}).Error)
	require.NoError(t, db.Create(&types.ProductionDocumentType{
		ID: documentServiceTypeID, TenantID: 7, Code: "software-development-baseline", Name: "Baseline", SchemaVersion: 3,
		BlockSchema: types.JSON(`{}`), SourceRequirements: types.JSON(`{}`), SkillBindings: types.JSON(`{}`),
		QualityRules: types.JSON(`{}`), ReviewPolicy: types.JSON(`{}`), PublicationPolicy: types.JSON(`{}`),
		Status: types.ProductionDocumentTypeActive, CreatedBy: "owner",
	}).Error)
	require.NoError(t, db.Create(&types.ProductionSourceSet{
		ID: documentServiceSetID, TenantID: 7, ProjectID: documentServiceProjectID, DocumentTypeID: documentServiceTypeID,
		Status: types.ProductionSourceSetCollecting, CreatedBy: "author",
	}).Error)
	require.NoError(t, db.Create(&types.ProductionSourceItem{
		ID: documentServiceItemID, SourceSetID: documentServiceSetID, SourceKind: types.ProductionSourceKindManual,
		Title: "Evidence", MimeType: "text/plain", ContentDigest: strings.Repeat("a", 64),
		CapturedAt: time.Now().UTC(), Metadata: types.JSON(`{}`), Status: types.ProductionSourceItemAccepted,
	}).Error)
	sources := apprepository.NewProductionSourceRepository(db)
	canonicalEvidence := types.JSON(`"evidence"`)
	if evidenceDigest == "" {
		sum := sha256.Sum256(canonicalEvidence)
		evidenceDigest = hex.EncodeToString(sum[:])
	}
	require.NoError(t, sources.CreateEvidence(context.Background(), 7, documentServiceItemID, &types.ProductionEvidenceSnapshot{
		ID: documentServiceEvidenceID, SourceItemID: documentServiceItemID,
		SnapshotType: types.ProductionEvidenceSnapshotText, InlineContent: canonicalEvidence,
		ContentDigest: evidenceDigest, RedactionMetadata: types.JSON(`{}`),
	}))
	frozenAt := time.Now().UTC()
	require.NoError(t, db.Model(&types.ProductionSourceSet{}).Where("id = ?", documentServiceSetID).
		Updates(map[string]any{"status": types.ProductionSourceSetFrozen, "frozen_at": frozenAt}).Error)
	documents := apprepository.NewProductionDocumentRepository(db)
	authorizer := &productionDocumentAuthorizerStub{}
	audit := &productionAuditServiceStub{}
	service := NewProductionDocumentService(
		documents,
		sources,
		apprepository.NewProductionDocumentTypeRepository(db),
		authorizer,
		nil,
		audit,
		apprepository.NewProductionUnitOfWork(db),
	)
	return service, documents, db, authorizer
}

func governedServiceBlocks(extra ...types.ProductionDocumentBlockInput) []types.ProductionDocumentBlockInput {
	template := BuiltinSoftwareDevelopmentBaseline()
	blocks := make([]types.ProductionDocumentBlockInput, 0, len(template.RequiredSections)+len(extra))
	for index, section := range template.RequiredSections {
		blocks = append(blocks, types.ProductionDocumentBlockInput{
			LogicalBlockID: fmt.Sprintf("section-%02d", index), BlockType: "heading",
			Content: types.JSON(strconv.Quote(section)), Attributes: types.JSON(`{}`),
			EvidenceRefs: types.JSON(`[]`), AIProvenance: types.JSON(`{}`),
		})
	}
	return append(blocks, extra...)
}

func governedServiceParagraph(logicalID, content string) types.ProductionDocumentBlockInput {
	return types.ProductionDocumentBlockInput{
		LogicalBlockID: logicalID, BlockType: "paragraph", Content: types.JSON(content),
		Attributes: types.JSON(`{}`), EvidenceRefs: types.JSON(`[` + strconv.Quote(documentServiceEvidenceID) + `]`),
		AIProvenance: types.JSON(`{}`),
	}
}

func TestProductionDocumentServiceCreatesBootstrapVersionAndStrictlyAuditsEveryVersion(t *testing.T) {
	svc, repo, _, _ := newProductionDocumentServiceFixture(t)
	audit := svc.audit.(*productionAuditServiceStub)
	ctx := context.WithValue(productionDocumentContext(7), types.TenantRoleContextKey, types.TenantRoleContributor)

	document, err := svc.CreateDocument(ctx, interfaces.CreateProductionDocumentInput{
		ProjectID: documentServiceProjectID, DocumentTypeID: documentServiceTypeID,
		SourceSetID: documentServiceSetID, Title: "Baseline",
	})

	require.NoError(t, err)
	require.NotNil(t, document.CurrentVersionID)
	versions, err := repo.ListVersions(context.Background(), 7, document.ID)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Equal(t, 1, versions[0].VersionNumber)
	require.Equal(t, *document.CurrentVersionID, versions[0].ID)
	require.Equal(t, types.ComputeProductionVersionDigest(&types.ProductionDocumentVersion{}), versions[0].ContentDigest)
	require.Len(t, audit.entries, 1)
	require.Equal(t, types.AuditActionProductionVersionCreated, audit.entries[0].Action)
	require.Equal(t, versions[0].ID, audit.entries[0].TargetID)

	appended, err := svc.AppendVersion(ctx, document.ID, interfaces.AppendProductionVersionInput{
		ParentVersionID: *document.CurrentVersionID, SourceSetID: documentServiceSetID,
		Origin: types.ProductionDocumentOriginHuman,
		Blocks: governedServiceBlocks(serviceParagraph("block-a", `"a"`)),
	})
	require.NoError(t, err)
	require.Equal(t, 2, appended.VersionNumber)
	require.Len(t, audit.entries, 2)
	require.Equal(t, appended.ID, audit.entries[1].TargetID)
}

func TestProductionDocumentServiceReturnsRequiredAuditFailures(t *testing.T) {
	svc, _, _, _ := newProductionDocumentServiceFixture(t)
	audit := svc.audit.(*productionAuditServiceStub)
	audit.err = errors.New("governed audit unavailable")

	document, err := svc.CreateDocument(productionDocumentContext(7), interfaces.CreateProductionDocumentInput{
		ProjectID: documentServiceProjectID, DocumentTypeID: documentServiceTypeID,
		SourceSetID: documentServiceSetID, Title: "Baseline",
	})

	require.Nil(t, document)
	require.ErrorContains(t, err, "governed audit unavailable")
}

func TestProductionDocumentServiceRejectsUngovernedAppendsWithoutPersistence(t *testing.T) {
	tests := []struct {
		name     string
		fixture  func(*testing.T) (*productionDocumentService, interfaces.ProductionDocumentRepository, *gorm.DB, *productionDocumentAuthorizerStub)
		blocks   []types.ProductionDocumentBlockInput
		wantCode string
	}{
		{
			name: "unknown evidence", fixture: newProductionDocumentServiceFixture,
			blocks: governedServiceBlocks(types.ProductionDocumentBlockInput{
				LogicalBlockID: "claim", BlockType: "paragraph", Content: types.JSON(`"claim"`), Attributes: types.JSON(`{}`),
				EvidenceRefs: types.JSON(`["unknown-evidence"]`), AIProvenance: types.JSON(`{}`),
			}),
			wantCode: "unknown_evidence_id",
		},
		{
			name: "missing evidence", fixture: newProductionDocumentServiceFixture,
			blocks: governedServiceBlocks(types.ProductionDocumentBlockInput{
				LogicalBlockID: "claim", BlockType: "paragraph", Content: types.JSON(`"claim"`), Attributes: types.JSON(`{}`),
				EvidenceRefs: types.JSON(`[]`), AIProvenance: types.JSON(`{}`),
			}),
			wantCode: "factual_evidence_required",
		},
		{
			name: "unsupported block", fixture: newProductionDocumentServiceFixture,
			blocks: governedServiceBlocks(types.ProductionDocumentBlockInput{
				LogicalBlockID: "unsupported", BlockType: "quote", Content: types.JSON(`"quote"`),
				Attributes: types.JSON(`{"needs_confirmation":true}`), EvidenceRefs: types.JSON(`[]`), AIProvenance: types.JSON(`{}`),
			}),
			wantCode: "unsupported_block_type",
		},
		{
			name: "missing required sections", fixture: newProductionDocumentServiceFixture,
			blocks: []types.ProductionDocumentBlockInput{{
				LogicalBlockID: "only-one", BlockType: "heading", Content: types.JSON(`"基线范围与目标"`),
				Attributes: types.JSON(`{}`), EvidenceRefs: types.JSON(`[]`), AIProvenance: types.JSON(`{}`),
			}},
			wantCode: "required_section_missing",
		},
		{
			name: "mismatched evidence digest",
			fixture: func(t *testing.T) (*productionDocumentService, interfaces.ProductionDocumentRepository, *gorm.DB, *productionDocumentAuthorizerStub) {
				return newProductionDocumentServiceFixtureWithEvidenceDigest(t, strings.Repeat("f", 64))
			},
			blocks:   governedServiceBlocks(governedServiceParagraph("claim", `"claim"`)),
			wantCode: "digest",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, _, db, _ := test.fixture(t)
			document := createServiceDocument(t, svc)
			bootstrapID := *document.CurrentVersionID
			audit := svc.audit.(*productionAuditServiceStub)

			version, err := svc.AppendVersion(productionDocumentContext(7), document.ID, interfaces.AppendProductionVersionInput{
				ParentVersionID: bootstrapID, SourceSetID: documentServiceSetID,
				Origin: types.ProductionDocumentOriginHuman, Blocks: test.blocks,
			})

			require.Nil(t, version)
			require.ErrorIs(t, err, types.ErrProductionDocumentValidation)
			require.ErrorContains(t, err, test.wantCode)
			var versionCount, blockCount, lineageCount int64
			require.NoError(t, db.Model(&types.ProductionDocumentVersion{}).Count(&versionCount).Error)
			require.NoError(t, db.Model(&types.ProductionDocumentBlock{}).Count(&blockCount).Error)
			require.NoError(t, db.Model(&types.ProductionBlockLineage{}).Count(&lineageCount).Error)
			require.Equal(t, int64(1), versionCount)
			require.Zero(t, blockCount)
			require.Zero(t, lineageCount)
			var persisted types.ProductionDocument
			require.NoError(t, db.First(&persisted, "id = ?", document.ID).Error)
			require.Equal(t, bootstrapID, *persisted.CurrentVersionID)
			require.Len(t, audit.entries, 1)
		})
	}
}

func TestProductionDocumentServiceGovernedAppendSucceeds(t *testing.T) {
	svc, _, db, _ := newProductionDocumentServiceFixture(t)
	document := createServiceDocument(t, svc)
	version, err := svc.AppendVersion(productionDocumentContext(7), document.ID, interfaces.AppendProductionVersionInput{
		ParentVersionID: *document.CurrentVersionID, SourceSetID: documentServiceSetID,
		Origin: types.ProductionDocumentOriginHuman,
		Blocks: governedServiceBlocks(governedServiceParagraph("claim", `"governed claim"`)),
	})
	require.NoError(t, err)
	require.NotNil(t, version)
	require.Equal(t, int64(2), countServiceRows(t, db, &types.ProductionDocumentVersion{}))
	require.Equal(t, int64(len(version.Blocks)), countServiceRows(t, db, &types.ProductionDocumentBlock{}))
}

func TestProductionDocumentServiceBindsInternalPrincipalToAIProvenance(t *testing.T) {
	svc, _, db, authorizer := newProductionDocumentServiceFixture(t)
	document := createServiceDocument(t, svc)
	authorizer.err = types.ErrProductionForbidden
	runID := "77000000-0000-4000-8000-000000000001"
	blocks := governedServiceBlocks(governedServiceParagraph("claim", `"governed claim"`))
	for index := range blocks {
		blocks[index].AIProvenance = types.JSON(`{"run_id":"` + runID + `"}`)
	}
	input := interfaces.AppendProductionVersionInput{
		ParentVersionID: *document.CurrentVersionID, SourceSetID: documentServiceSetID,
		Origin: types.ProductionDocumentOriginAI, Blocks: blocks,
	}
	mismatchCtx, err := types.WithProductionInternalPrincipal(context.Background(), types.ProductionInternalPrincipal{
		ActorID: types.ProductionSystemActorID, ActorKind: types.ProductionInternalActorWorker,
		TenantID: 7, ProjectID: documentServiceProjectID, RunID: "77000000-0000-4000-8000-000000000099",
	})
	require.NoError(t, err)
	version, err := svc.AppendVersion(mismatchCtx, document.ID, input)
	require.Nil(t, version)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Equal(t, int64(1), countServiceRows(t, db, &types.ProductionDocumentVersion{}))

	exactCtx, err := types.WithProductionInternalPrincipal(context.Background(), types.ProductionInternalPrincipal{
		ActorID: types.ProductionSystemActorID, ActorKind: types.ProductionInternalActorWorker,
		TenantID: 7, ProjectID: documentServiceProjectID, RunID: runID,
	})
	require.NoError(t, err)
	version, err = svc.AppendVersion(exactCtx, document.ID, input)
	require.NoError(t, err)
	require.NotNil(t, version)
	require.Equal(t, int64(2), countServiceRows(t, db, &types.ProductionDocumentVersion{}))
}

func TestProductionDocumentDirectServiceAuditFailureRollsBackBootstrapAndAppend(t *testing.T) {
	t.Run("bootstrap", func(t *testing.T) {
		svc, _, db, _ := newProductionDocumentServiceFixture(t)
		svc.audit = NewAuditLogService(apprepository.NewAuditLogRepository(db))
		require.NoError(t, installServiceAuditFailureTrigger(db))

		document, err := svc.CreateDocument(productionDocumentContext(7), interfaces.CreateProductionDocumentInput{
			ProjectID: documentServiceProjectID, DocumentTypeID: documentServiceTypeID,
			SourceSetID: documentServiceSetID, Title: "Baseline",
		})

		require.Nil(t, document)
		require.ErrorContains(t, err, "forced governed audit failure")
		require.Zero(t, countServiceRows(t, db, &types.ProductionDocument{}))
		require.Zero(t, countServiceRows(t, db, &types.ProductionDocumentVersion{}))
		require.Zero(t, countServiceRows(t, db, &types.AuditLog{}))
		require.NoError(t, db.Exec(`DROP TRIGGER fail_governed_audit_insert`).Error)

		document, err = svc.CreateDocument(productionDocumentContext(7), interfaces.CreateProductionDocumentInput{
			ProjectID: documentServiceProjectID, DocumentTypeID: documentServiceTypeID,
			SourceSetID: documentServiceSetID, Title: "Baseline",
		})
		require.NoError(t, err)
		require.NotNil(t, document)
		require.Equal(t, int64(1), countServiceRows(t, db, &types.ProductionDocument{}))
		require.Equal(t, int64(1), countServiceRows(t, db, &types.ProductionDocumentVersion{}))
		require.Equal(t, int64(1), countServiceRows(t, db, &types.AuditLog{}))
	})

	t.Run("append", func(t *testing.T) {
		svc, _, db, _ := newProductionDocumentServiceFixture(t)
		svc.audit = NewAuditLogService(apprepository.NewAuditLogRepository(db))
		document := createServiceDocument(t, svc)
		bootstrapID := *document.CurrentVersionID
		require.NoError(t, installServiceAuditFailureTrigger(db))

		version, err := svc.AppendVersion(productionDocumentContext(7), document.ID, interfaces.AppendProductionVersionInput{
			ParentVersionID: bootstrapID, SourceSetID: documentServiceSetID,
			Origin: types.ProductionDocumentOriginHuman,
			Blocks: governedServiceBlocks(governedServiceParagraph("claim", `"governed claim"`)),
		})

		require.Nil(t, version)
		require.ErrorContains(t, err, "forced governed audit failure")
		require.Equal(t, int64(1), countServiceRows(t, db, &types.ProductionDocumentVersion{}))
		require.Zero(t, countServiceRows(t, db, &types.ProductionDocumentBlock{}))
		require.Equal(t, int64(1), countServiceRows(t, db, &types.AuditLog{}))
		var persisted types.ProductionDocument
		require.NoError(t, db.First(&persisted, "id = ?", document.ID).Error)
		require.Equal(t, bootstrapID, *persisted.CurrentVersionID)
	})
}

func TestProductionDocumentNestedUnitOfWorkRollsBackWhenOuterTransactionSwallowsAuditFailure(t *testing.T) {
	t.Run("bootstrap", func(t *testing.T) {
		svc, _, db, _ := newProductionDocumentServiceFixture(t)
		svc.audit = NewAuditLogService(apprepository.NewAuditLogRepository(db))
		require.NoError(t, installServiceAuditFailureTrigger(db))
		var serviceErr error

		outerErr := database.WithTransactionContext(productionDocumentContext(7), db, func(txCtx context.Context) error {
			_, serviceErr = svc.CreateDocument(txCtx, interfaces.CreateProductionDocumentInput{
				ProjectID: documentServiceProjectID, DocumentTypeID: documentServiceTypeID,
				SourceSetID: documentServiceSetID, Title: "Baseline",
			})
			return nil
		})

		require.NoError(t, outerErr)
		require.ErrorContains(t, serviceErr, "forced governed audit failure")
		require.Zero(t, countServiceRows(t, db, &types.ProductionDocument{}))
		require.Zero(t, countServiceRows(t, db, &types.ProductionDocumentVersion{}))
		require.Zero(t, countServiceRows(t, db, &types.AuditLog{}))
	})

	t.Run("append", func(t *testing.T) {
		svc, _, db, _ := newProductionDocumentServiceFixture(t)
		svc.audit = NewAuditLogService(apprepository.NewAuditLogRepository(db))
		document := createServiceDocument(t, svc)
		bootstrapID := *document.CurrentVersionID
		require.NoError(t, installServiceAuditFailureTrigger(db))
		var serviceErr error

		outerErr := database.WithTransactionContext(productionDocumentContext(7), db, func(txCtx context.Context) error {
			_, serviceErr = svc.AppendVersion(txCtx, document.ID, interfaces.AppendProductionVersionInput{
				ParentVersionID: bootstrapID, SourceSetID: documentServiceSetID,
				Origin: types.ProductionDocumentOriginHuman,
				Blocks: governedServiceBlocks(governedServiceParagraph("claim", `"governed claim"`)),
			})
			return nil
		})

		require.NoError(t, outerErr)
		require.ErrorContains(t, serviceErr, "forced governed audit failure")
		require.Equal(t, int64(1), countServiceRows(t, db, &types.ProductionDocumentVersion{}))
		require.Zero(t, countServiceRows(t, db, &types.ProductionDocumentBlock{}))
		require.Equal(t, int64(1), countServiceRows(t, db, &types.AuditLog{}))
		var persisted types.ProductionDocument
		require.NoError(t, db.First(&persisted, "id = ?", document.ID).Error)
		require.Equal(t, bootstrapID, *persisted.CurrentVersionID)
	})
}

func TestProductionDocumentNestedUnitOfWorkSuccessCommits(t *testing.T) {
	svc, _, db, _ := newProductionDocumentServiceFixture(t)
	svc.audit = NewAuditLogService(apprepository.NewAuditLogRepository(db))
	var document *types.ProductionDocument
	var version *types.ProductionDocumentVersion

	err := database.WithTransactionContext(productionDocumentContext(7), db, func(txCtx context.Context) error {
		var createErr error
		document, createErr = svc.CreateDocument(txCtx, interfaces.CreateProductionDocumentInput{
			ProjectID: documentServiceProjectID, DocumentTypeID: documentServiceTypeID,
			SourceSetID: documentServiceSetID, Title: "Baseline",
		})
		if createErr != nil {
			return createErr
		}
		var appendErr error
		version, appendErr = svc.AppendVersion(txCtx, document.ID, interfaces.AppendProductionVersionInput{
			ParentVersionID: *document.CurrentVersionID, SourceSetID: documentServiceSetID,
			Origin: types.ProductionDocumentOriginHuman,
			Blocks: governedServiceBlocks(governedServiceParagraph("claim", `"governed claim"`)),
		})
		return appendErr
	})

	require.NoError(t, err)
	require.NotNil(t, document)
	require.NotNil(t, version)
	require.Equal(t, int64(1), countServiceRows(t, db, &types.ProductionDocument{}))
	require.Equal(t, int64(2), countServiceRows(t, db, &types.ProductionDocumentVersion{}))
	require.Equal(t, int64(len(version.Blocks)), countServiceRows(t, db, &types.ProductionDocumentBlock{}))
	require.Equal(t, int64(2), countServiceRows(t, db, &types.AuditLog{}))
}

func installServiceAuditFailureTrigger(db *gorm.DB) error {
	return db.Exec(`
CREATE TRIGGER fail_governed_audit_insert
BEFORE INSERT ON audit_logs
BEGIN
	SELECT RAISE(ABORT, 'forced governed audit failure');
END`).Error
}

func countServiceRows(t *testing.T, db *gorm.DB, model any) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(model).Count(&count).Error)
	return count
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
		Attributes: types.JSON(`{}`), EvidenceRefs: types.JSON(`[` + strconv.Quote(documentServiceEvidenceID) + `]`), AIProvenance: types.JSON(`{}`),
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
		ParentVersionID: *document.CurrentVersionID, SourceSetID: documentServiceSetID,
		Origin: types.ProductionDocumentOriginHuman,
		Blocks: governedServiceBlocks(types.ProductionDocumentBlockInput{
			LogicalBlockID: "block-a", BlockType: "paragraph", Content: types.JSON(`"value"`),
			Attributes:   types.JSON(`{ "z": false, "a": true }`),
			EvidenceRefs: types.JSON(`[` + strconv.Quote(documentServiceEvidenceID) + `]`), AIProvenance: types.JSON(`{}`),
		}),
	})

	require.NoError(t, err)
	require.Equal(t, 2, version.VersionNumber)
	block := version.Blocks[len(version.Blocks)-1]
	require.Equal(t, "block-a", block.LogicalBlockID)
	require.Len(t, block.ContentDigest, 64)
	require.Len(t, version.ContentDigest, 64)
	require.Equal(t, block.ContentDigest, types.ComputeProductionBlockDigest(block))
	require.Equal(t, version.ContentDigest, types.ComputeProductionVersionDigest(version))
	require.Equal(t, types.JSON(`{"a":true,"z":false}`), block.Attributes)
}

func TestProductionDocumentServiceRecordsSplitAndMergeLineage(t *testing.T) {
	svc, _, _, _ := newProductionDocumentServiceFixture(t)
	document := createServiceDocument(t, svc)
	first, err := svc.AppendVersion(productionDocumentContext(7), document.ID, interfaces.AppendProductionVersionInput{
		ParentVersionID: *document.CurrentVersionID, SourceSetID: documentServiceSetID,
		Origin: types.ProductionDocumentOriginHuman,
		Blocks: governedServiceBlocks(
			serviceParagraph("block-a", `"a"`), serviceParagraph("block-b", `"b"`), serviceParagraph("block-c", `"c"`),
		),
	})
	require.NoError(t, err)

	second, err := svc.AppendVersion(productionDocumentContext(7), document.ID, interfaces.AppendProductionVersionInput{
		ParentVersionID: first.ID, SourceSetID: documentServiceSetID, Origin: types.ProductionDocumentOriginMixed,
		Blocks: governedServiceBlocks(
			serviceParagraph("block-a-1", `"a1"`), serviceParagraph("block-a-2", `"a2"`), serviceParagraph("block-bc", `"bc"`),
		),
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
		ParentVersionID: *document.CurrentVersionID, SourceSetID: documentServiceSetID,
		Origin: types.ProductionDocumentOriginHuman,
		Blocks: governedServiceBlocks(serviceParagraph("block-a", `"a"`)),
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
