package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	appservice "github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	productionDocumentID = "66666666-6666-4666-8666-666666666666"
	productionVersionID  = "77777777-7777-4777-8777-777777777777"
	productionStaleID    = "88888888-8888-4888-8888-888888888888"
)

type productionDocumentServiceStub struct {
	listedProjectID string
	documents       []*types.ProductionDocument
	created         interfaces.CreateProductionDocumentInput
	append          struct {
		documentID string
		input      interfaces.AppendProductionVersionInput
	}
	listedID             string
	gotDocumentID        string
	gotVersionDocumentID string
	gotVersionID         string
	document             *types.ProductionDocument
	version              *types.ProductionDocumentVersion
	versions             []*types.ProductionDocumentVersion
	err                  error
}

func (s *productionDocumentServiceStub) ListDocuments(_ context.Context, projectID string) ([]*types.ProductionDocument, error) {
	s.listedProjectID = projectID
	return s.documents, s.err
}

func (s *productionDocumentServiceStub) CreateDocument(_ context.Context, input interfaces.CreateProductionDocumentInput) (*types.ProductionDocument, error) {
	s.created = input
	if s.err != nil {
		return nil, s.err
	}
	return &types.ProductionDocument{
		ID: productionDocumentID, TenantID: 7, ProjectID: input.ProjectID,
		DocumentTypeID: input.DocumentTypeID, Title: input.Title, Status: types.ProductionDocumentDraft,
	}, nil
}

func (s *productionDocumentServiceStub) AppendVersion(_ context.Context, documentID string, input interfaces.AppendProductionVersionInput) (*types.ProductionDocumentVersion, error) {
	s.append.documentID, s.append.input = documentID, input
	if s.err != nil {
		return nil, s.err
	}
	parent := input.ParentVersionID
	return &types.ProductionDocumentVersion{
		ID: productionVersionID, DocumentID: documentID, ParentVersionID: &parent,
		SourceSetID: input.SourceSetID, Origin: input.Origin,
	}, nil
}

func (*productionDocumentServiceStub) GetVersion(context.Context, string) (*types.ProductionDocumentVersion, error) {
	panic("unexpected GetVersion")
}

func (s *productionDocumentServiceStub) GetDocument(_ context.Context, documentID string) (*types.ProductionDocument, error) {
	s.gotDocumentID = documentID
	return s.document, s.err
}

func (s *productionDocumentServiceStub) GetVersionDetail(_ context.Context, documentID, versionID string) (*types.ProductionDocumentVersion, error) {
	s.gotVersionDocumentID = documentID
	s.gotVersionID = versionID
	return s.version, s.err
}

func (s *productionDocumentServiceStub) ListVersions(_ context.Context, documentID string) ([]*types.ProductionDocumentVersion, error) {
	s.listedID = documentID
	return s.versions, s.err
}

func validProductionVersionBody() string {
	return `{"source_set_id":"` + productionSourceSetID + `","origin":"human","change_summary":"Initial draft","blocks":[{"logical_block_id":"intro","block_type":"paragraph","content":{"text":"Hello"},"attributes":{},"evidence_refs":[],"ai_provenance":{}}],"lineage":[]}`
}

func TestProductionDocumentHandlerCreatesAppendsAndLists(t *testing.T) {
	service := &productionDocumentServiceStub{versions: []*types.ProductionDocumentVersion{{ID: productionVersionID}}}
	h := NewProductionDocumentHandler(service)

	create := performProductionHandlerRequest(
		http.MethodPost, "/production/projects/:id/documents",
		"/production/projects/"+productionProjectID+"/documents",
		`{"document_type_id":"`+productionDocumentTypeID+`","source_set_id":"`+productionSourceSetID+`","title":" Baseline "}`,
		h.Create,
	)
	appendResponse := performProductionDocumentHandlerRequest(
		http.MethodPost, "/production/documents/:id/versions",
		"/production/documents/"+productionDocumentID+"/versions",
		validProductionVersionBody(), productionVersionID, h.AppendVersion,
	)
	list := performProductionDocumentHandlerRequest(
		http.MethodGet, "/production/documents/:id/versions",
		"/production/documents/"+productionDocumentID+"/versions", "", "", h.ListVersions,
	)

	require.Equal(t, http.StatusCreated, create.Code)
	require.Equal(t, "Baseline", service.created.Title)
	require.Equal(t, productionProjectID, service.created.ProjectID)
	require.Equal(t, http.StatusCreated, appendResponse.Code)
	require.Equal(t, productionDocumentID, service.append.documentID)
	require.Equal(t, productionVersionID, service.append.input.ParentVersionID)
	require.Len(t, service.append.input.Blocks, 1)
	require.Equal(t, types.JSON(`{"text":"Hello"}`), service.append.input.Blocks[0].Content)
	require.Equal(t, http.StatusOK, list.Code)
	require.Equal(t, productionDocumentID, service.listedID)
}

func TestProductionDocumentHandlerListsProjectDocuments(t *testing.T) {
	service := &productionDocumentServiceStub{documents: []*types.ProductionDocument{{ID: productionDocumentID}}}
	h := NewProductionDocumentHandler(service)

	response := performProductionHandlerRequest(
		http.MethodGet,
		"/production/projects/:id/documents",
		"/production/projects/"+productionProjectID+"/documents",
		"",
		h.List,
	)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, productionProjectID, service.listedProjectID)
	require.Contains(t, response.Body.String(), productionDocumentID)
}

func TestProductionDocumentHandlerGetsDocumentAndImmutableVersionDetail(t *testing.T) {
	service := &productionDocumentServiceStub{
		document: &types.ProductionDocument{ID: productionDocumentID},
		version: &types.ProductionDocumentVersion{
			ID:         productionVersionID,
			DocumentID: productionDocumentID,
			Blocks:     []*types.ProductionDocumentBlock{{ID: "block-1", VersionID: productionVersionID}},
			Lineage:    []*types.ProductionBlockLineage{{ID: "lineage-1", ToVersionID: productionVersionID}},
		},
	}
	h := NewProductionDocumentHandler(service)

	documentResponse := performProductionDocumentHandlerRequest(
		http.MethodGet,
		"/production/documents/:id",
		"/production/documents/"+productionDocumentID,
		"", "", h.Get,
	)
	versionResponse := performProductionDocumentHandlerRequest(
		http.MethodGet,
		"/production/documents/:id/versions/:version_id",
		"/production/documents/"+productionDocumentID+"/versions/"+productionVersionID,
		"", "", h.GetVersion,
	)

	require.Equal(t, http.StatusOK, documentResponse.Code, documentResponse.Body.String())
	require.Equal(t, productionDocumentID, service.gotDocumentID)
	require.Equal(t, http.StatusOK, versionResponse.Code, versionResponse.Body.String())
	require.Equal(t, productionDocumentID, service.gotVersionDocumentID)
	require.Equal(t, productionVersionID, service.gotVersionID)
	require.Contains(t, versionResponse.Body.String(), `"blocks"`)
	require.Contains(t, versionResponse.Body.String(), `"lineage"`)
}

func performProductionDocumentHandlerRequest(
	method, pattern, path, body, ifMatch string,
	handler gin.HandlerFunc,
) *httptest.ResponseRecorder {
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Handle(method, pattern, func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "author-1")
		c.Request = c.Request.WithContext(ctx)
		handler(c)
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	engine.ServeHTTP(recorder, request)
	return recorder
}

func TestProductionDocumentHandlerRequiresCanonicalIfMatchAndValidBody(t *testing.T) {
	for _, header := range []string{"", "not-a-uuid", strings.ToUpper("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), `"` + productionVersionID + `"`} {
		t.Run("if-match "+header, func(t *testing.T) {
			service := &productionDocumentServiceStub{}
			h := NewProductionDocumentHandler(service)
			response := performProductionDocumentHandlerRequest(
				http.MethodPost, "/production/documents/:id/versions",
				"/production/documents/"+productionDocumentID+"/versions",
				validProductionVersionBody(), header, h.AppendVersion,
			)
			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Empty(t, service.append.documentID)
		})
	}

	tests := []struct{ name, body string }{
		{name: "invalid source set", body: `{"source_set_id":"bad","origin":"human","blocks":[{"block_type":"paragraph","content":{}}]}`},
		{name: "invalid origin", body: `{"source_set_id":"` + productionSourceSetID + `","origin":"machine","blocks":[{"block_type":"paragraph","content":{}}]}`},
		{name: "empty blocks", body: `{"source_set_id":"` + productionSourceSetID + `","origin":"human","blocks":[]}`},
		{name: "long block type", body: `{"source_set_id":"` + productionSourceSetID + `","origin":"human","blocks":[{"block_type":"` + strings.Repeat("b", 25) + `","content":{}}]}`},
		{name: "invalid lineage", body: `{"source_set_id":"` + productionSourceSetID + `","origin":"human","blocks":[{"logical_block_id":"intro","block_type":"paragraph","content":{}}],"lineage":[{"from_logical_block_id":"intro","to_logical_block_id":"intro","relation":"copied"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &productionDocumentServiceStub{}
			h := NewProductionDocumentHandler(service)
			response := performProductionDocumentHandlerRequest(
				http.MethodPost, "/production/documents/:id/versions",
				"/production/documents/"+productionDocumentID+"/versions",
				test.body, productionVersionID, h.AppendVersion,
			)
			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Empty(t, service.append.documentID)
		})
	}
}

func TestProductionDocumentHandlerRejectsCreatePathAndBodyBounds(t *testing.T) {
	tests := []struct{ name, path, body string }{
		{
			name: "noncanonical project", path: "/production/projects/AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA/documents",
			body: `{"document_type_id":"` + productionDocumentTypeID + `","source_set_id":"` + productionSourceSetID + `","title":"Baseline"}`,
		},
		{
			name: "invalid document type", path: "/production/projects/" + productionProjectID + "/documents",
			body: `{"document_type_id":"bad","source_set_id":"` + productionSourceSetID + `","title":"Baseline"}`,
		},
		{
			name: "invalid source set", path: "/production/projects/" + productionProjectID + "/documents",
			body: `{"document_type_id":"` + productionDocumentTypeID + `","source_set_id":"bad","title":"Baseline"}`,
		},
		{
			name: "title too long", path: "/production/projects/" + productionProjectID + "/documents",
			body: `{"document_type_id":"` + productionDocumentTypeID + `","source_set_id":"` + productionSourceSetID + `","title":"` + strings.Repeat("t", 256) + `"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &productionDocumentServiceStub{}
			response := performProductionDocumentHandlerRequest(
				http.MethodPost, "/production/projects/:id/documents", test.path, test.body, "",
				NewProductionDocumentHandler(service).Create,
			)
			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Empty(t, service.created.ProjectID)
		})
	}
}

func TestProductionDocumentHandlerMapsTypedErrorsWithoutLeakage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "stale", err: types.ErrProductionDocumentStaleParent, want: http.StatusConflict},
		{name: "invalid source lifecycle", err: types.ErrProductionDocumentSourceSetInvalid, want: http.StatusConflict},
		{name: "invalid lineage", err: types.ErrProductionBlockLineageInvalid, want: http.StatusBadRequest},
		{name: "forbidden", err: types.ErrProductionForbidden, want: http.StatusForbidden},
		{name: "not found", err: gorm.ErrRecordNotFound, want: http.StatusNotFound},
		{name: "unexpected", err: errors.New("database password secret"), want: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := NewProductionDocumentHandler(&productionDocumentServiceStub{err: test.err})
			response := performProductionDocumentHandlerRequest(
				http.MethodPost, "/production/documents/:id/versions",
				"/production/documents/"+productionDocumentID+"/versions",
				validProductionVersionBody(), productionVersionID, h.AppendVersion,
			)
			require.Equal(t, test.want, response.Code)
			require.NotContains(t, response.Body.String(), "database password secret")
		})
	}
}

type productionHTTPAuthorizer struct{ err error }

func (a *productionHTTPAuthorizer) RequireProjectRole(context.Context, string, ...types.ProductionRole) error {
	return a.err
}

type productionGovernedAuditService struct {
	interfaces.AuditLogService
	mu       sync.Mutex
	failNext bool
}

func (s *productionGovernedAuditService) Log(ctx context.Context, entry *types.AuditLog) error {
	s.mu.Lock()
	if s.failNext {
		s.failNext = false
		s.mu.Unlock()
		return errors.New("forced governed audit failure")
	}
	s.mu.Unlock()
	return s.AuditLogService.Log(ctx, entry)
}

func (s *productionGovernedAuditService) FailNext() {
	s.mu.Lock()
	s.failNext = true
	s.mu.Unlock()
}

type productionDocumentsHTTPFixture struct {
	db          *gorm.DB
	engine      *gin.Engine
	sources     interfaces.ProductionSourceRepository
	documents   interfaces.ProductionDocumentRepository
	completion  *productionCompletionFailingRepo
	audit       *productionGovernedAuditService
	projectID   string
	typeID      string
	sourceSetID string
	documentID  string
}

func newProductionDocumentsHTTPFixture(t *testing.T) *productionDocumentsHTTPFixture {
	t.Helper()
	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared&_foreign_keys=1&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, name := range []string{
		"../../migrations/sqlite/000001_knowledge_production_foundation.up.sql",
		"../../migrations/sqlite/000002_knowledge_production_documents.up.sql",
		"../../migrations/sqlite/000004_knowledge_production_reviews.up.sql",
	} {
		migration, readErr := os.ReadFile(name)
		require.NoError(t, readErr)
		require.NoError(t, db.Exec(string(migration)).Error)
	}
	require.NoError(t, db.AutoMigrate(&types.AuditLog{}))

	fixture := &productionDocumentsHTTPFixture{
		db: db, projectID: productionProjectID, typeID: productionDocumentTypeID,
		sourceSetID: productionSourceSetID, documentID: productionDocumentID,
	}
	require.NoError(t, db.Create(&types.ProductionProject{
		ID: fixture.projectID, TenantID: 7, Name: "Project", OwnerUserID: "author-1", Status: types.ProductionProjectActive,
	}).Error)
	require.NoError(t, db.Create(&types.ProductionDocumentType{
		ID: fixture.typeID, TenantID: 7, Code: "software-development-baseline", Name: "Baseline", SchemaVersion: 1,
		BlockSchema: types.JSON(`{}`), SourceRequirements: types.JSON(`{}`), SkillBindings: types.JSON(`{}`),
		QualityRules: types.JSON(`{}`), ReviewPolicy: types.JSON(`{}`), PublicationPolicy: types.JSON(`{}`),
		Status: types.ProductionDocumentTypeActive, CreatedBy: "author-1",
	}).Error)
	fixture.sources = apprepository.NewProductionSourceRepository(db)
	fixture.documents = apprepository.NewProductionDocumentRepository(db)
	require.NoError(t, fixture.sources.CreateSet(context.Background(), &types.ProductionSourceSet{
		ID: fixture.sourceSetID, TenantID: 7, ProjectID: fixture.projectID, DocumentTypeID: fixture.typeID,
		Status: types.ProductionSourceSetCollecting, CreatedBy: "author-1",
	}))
	require.NoError(t, fixture.sources.CreateItem(context.Background(), 7, fixture.sourceSetID, &types.ProductionSourceItem{
		ID: productionSourceItemID, SourceSetID: fixture.sourceSetID, SourceKind: types.ProductionSourceKindManual,
		Title: "Evidence", MimeType: "text/plain", ContentDigest: strings.Repeat("a", 64),
		CapturedAt: time.Now().UTC(), Metadata: types.JSON(`{}`), Status: types.ProductionSourceItemAccepted,
	}))

	fixture.audit = &productionGovernedAuditService{
		AuditLogService: appservice.NewAuditLogService(apprepository.NewAuditLogRepository(db)),
	}
	authorizer := &productionHTTPAuthorizer{}
	uow := apprepository.NewProductionUnitOfWork(db)
	sourceService := appservice.NewProductionSourceService(fixture.sources, authorizer, nil, fixture.audit, uow)
	documentService := appservice.NewProductionDocumentService(
		fixture.documents, fixture.sources, apprepository.NewProductionDocumentTypeRepository(db), authorizer, nil, fixture.audit, uow,
		apprepository.NewProductionReviewRepository(db),
	)
	idempotency := apprepository.NewProductionIdempotencyRepository(db)
	fixture.completion = &productionCompletionFailingRepo{ProductionIdempotencyRepository: idempotency}

	gin.SetMode(gin.TestMode)
	fixture.engine = gin.New()
	fixture.engine.Use(middleware.ErrorHandler())
	fixture.engine.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "author-1")
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleContributor)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	idempotencyMiddleware := middleware.NewProductionIdempotencyMiddleware(fixture.completion)
	sourceHandler := NewProductionSourceHandler(sourceService)
	documentHandler := NewProductionDocumentHandler(documentService)
	fixture.engine.POST("/production/source-sets/:id/freeze", idempotencyMiddleware.Require(), sourceHandler.Freeze)
	fixture.engine.POST("/production/projects/:id/documents", idempotencyMiddleware.Require(), documentHandler.Create)
	fixture.engine.POST("/production/documents/:id/versions", idempotencyMiddleware.Require(), documentHandler.AppendVersion)
	sqlDB.SetMaxOpenConns(1)
	return fixture
}

func performProductionDocumentsHTTPRequest(
	fixture *productionDocumentsHTTPFixture,
	path, key, body, ifMatch string,
) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	fixture.engine.ServeHTTP(recorder, request)
	return recorder
}

func countProductionRows(t *testing.T, db *gorm.DB, model any) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(model).Count(&count).Error)
	return count
}

func attachProductionFixtureEvidence(t *testing.T, fixture *productionDocumentsHTTPFixture) {
	attachProductionFixtureEvidenceWithDigest(t, fixture, "")
}

func attachProductionFixtureEvidenceWithDigest(t *testing.T, fixture *productionDocumentsHTTPFixture, digest string) {
	t.Helper()
	inline := types.JSON(`"evidence"`)
	if digest == "" {
		sum := sha256.Sum256(inline)
		digest = hex.EncodeToString(sum[:])
	}
	require.NoError(t, fixture.sources.CreateEvidence(context.Background(), 7, productionSourceItemID, &types.ProductionEvidenceSnapshot{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SourceItemID: productionSourceItemID,
		SnapshotType: types.ProductionEvidenceSnapshotText, InlineContent: inline,
		ContentDigest: digest, RedactionMetadata: types.JSON(`{}`),
	}))
}

func governedProductionVersionBody(extra ...map[string]any) string {
	blocks := make([]map[string]any, 0, len(appservice.BuiltinSoftwareDevelopmentBaseline().RequiredSections)+len(extra))
	for index, section := range appservice.BuiltinSoftwareDevelopmentBaseline().RequiredSections {
		blocks = append(blocks, map[string]any{
			"logical_block_id": fmt.Sprintf("section-%02d", index), "block_type": "heading", "content": section,
			"attributes": map[string]any{}, "evidence_refs": []string{}, "ai_provenance": map[string]any{},
		})
	}
	blocks = append(blocks, extra...)
	payload, err := json.Marshal(map[string]any{
		"source_set_id": productionSourceSetID, "origin": "human", "change_summary": "Governed draft",
		"blocks": blocks, "lineage": []any{},
	})
	if err != nil {
		panic(err)
	}
	return string(payload)
}

func validGovernedProductionVersionBody() string {
	return governedProductionVersionBody(map[string]any{
		"logical_block_id": "claim", "block_type": "paragraph", "content": "governed claim",
		"attributes": map[string]any{}, "evidence_refs": []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		"ai_provenance": map[string]any{},
	})
}

func createProductionDocumentRequestBody(fixture *productionDocumentsHTTPFixture) string {
	return `{"document_type_id":"` + fixture.typeID + `","source_set_id":"` + fixture.sourceSetID + `","title":"Baseline"}`
}

func decodeProductionDocumentResponse(t *testing.T, response *httptest.ResponseRecorder) *types.ProductionDocument {
	t.Helper()
	var payload struct {
		Data *types.ProductionDocument `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.NotNil(t, payload.Data)
	return payload.Data
}

func TestProductionSourceAndDocumentHTTPTransactions(t *testing.T) {
	fixture := newProductionDocumentsHTTPFixture(t)
	freezePath := "/production/source-sets/" + fixture.sourceSetID + "/freeze"

	missing := performProductionDocumentsHTTPRequest(fixture, freezePath, "freeze-missing", "", "")
	require.Equal(t, http.StatusConflict, missing.Code)
	require.Equal(t, types.ProductionSourceSetCollecting, mustProductionSourceSet(t, fixture).Status)
	require.Zero(t, countProductionRows(t, fixture.db, &types.AuditLog{}))

	attachProductionFixtureEvidence(t, fixture)
	freeze := performProductionDocumentsHTTPRequest(fixture, freezePath, "freeze-success", "", "")
	replayFreeze := performProductionDocumentsHTTPRequest(fixture, freezePath, "freeze-success", "", "")
	require.Equal(t, http.StatusOK, freeze.Code)
	require.Equal(t, freeze.Body.String(), replayFreeze.Body.String())
	require.Equal(t, types.ProductionSourceSetFrozen, mustProductionSourceSet(t, fixture).Status)
	require.Equal(t, int64(1), countProductionRows(t, fixture.db, &types.AuditLog{}))
	audits := productionAudits(t, fixture.db)
	require.Equal(t, types.AuditActionProductionSourceFrozen, audits[0].Action)
	require.Equal(t, fixture.sourceSetID, audits[0].TargetID)
	alreadyFrozen := performProductionDocumentsHTTPRequest(fixture, freezePath, "freeze-again", "", "")
	require.Equal(t, http.StatusConflict, alreadyFrozen.Code)

	createPath := "/production/projects/" + fixture.projectID + "/documents"
	create := performProductionDocumentsHTTPRequest(
		fixture, createPath, "document-create", createProductionDocumentRequestBody(fixture), "",
	)
	require.Equal(t, http.StatusCreated, create.Code)
	document := decodeProductionDocumentResponse(t, create)
	require.NotNil(t, document.CurrentVersionID)
	fixture.documentID = document.ID
	headID := *document.CurrentVersionID
	bootstrap, err := fixture.documents.GetVersion(context.Background(), 7, headID)
	require.NoError(t, err)
	require.Equal(t, 1, bootstrap.VersionNumber)
	require.Empty(t, bootstrap.Blocks)
	require.Empty(t, bootstrap.Lineage)
	require.Equal(t, types.ComputeProductionVersionDigest(&types.ProductionDocumentVersion{}), bootstrap.ContentDigest)

	appendPath := "/production/documents/" + fixture.documentID + "/versions"
	beforeVersions := countProductionRows(t, fixture.db, &types.ProductionDocumentVersion{})
	beforeBlocks := countProductionRows(t, fixture.db, &types.ProductionDocumentBlock{})
	stale := performProductionDocumentsHTTPRequest(fixture, appendPath, "append-stale", validGovernedProductionVersionBody(), productionStaleID)
	require.Equal(t, http.StatusConflict, stale.Code)
	require.Equal(t, beforeVersions, countProductionRows(t, fixture.db, &types.ProductionDocumentVersion{}))
	require.Equal(t, beforeBlocks, countProductionRows(t, fixture.db, &types.ProductionDocumentBlock{}))

	created := performProductionDocumentsHTTPRequest(fixture, appendPath, "append-success", validGovernedProductionVersionBody(), headID)
	replay := performProductionDocumentsHTTPRequest(fixture, appendPath, "append-success", validGovernedProductionVersionBody(), headID)
	require.Equal(t, http.StatusCreated, created.Code)
	require.Equal(t, created.Body.String(), replay.Body.String())
	conflictingReplay := performProductionDocumentsHTTPRequest(
		fixture, appendPath, "append-success", validGovernedProductionVersionBody(), productionStaleID,
	)
	require.Equal(t, http.StatusConflict, conflictingReplay.Code)
	require.Contains(t, conflictingReplay.Body.String(), "PRODUCTION_IDEMPOTENCY_KEY_CONFLICT")
	require.Equal(t, beforeVersions+1, countProductionRows(t, fixture.db, &types.ProductionDocumentVersion{}))
	require.Equal(t, int64(3), countProductionRows(t, fixture.db, &types.AuditLog{}))
	audits = productionAudits(t, fixture.db)
	require.Equal(t, types.AuditActionProductionVersionCreated, audits[1].Action)
	require.Equal(t, headID, audits[1].TargetID)
	require.Equal(t, types.AuditActionProductionVersionCreated, audits[2].Action)
	require.Equal(t, string(types.TenantRoleContributor), audits[2].ActorRole)

	var current types.ProductionDocument
	require.NoError(t, fixture.db.First(&current, "id = ?", fixture.documentID).Error)
	require.NotNil(t, current.CurrentVersionID)
	fixture.completion.failNext = true
	failed := performProductionDocumentsHTTPRequest(fixture, appendPath, "append-completion-fails", validGovernedProductionVersionBody(), *current.CurrentVersionID)
	require.Equal(t, http.StatusInternalServerError, failed.Code)
	require.Equal(t, beforeVersions+1, countProductionRows(t, fixture.db, &types.ProductionDocumentVersion{}))
	require.Equal(t, int64(3), countProductionRows(t, fixture.db, &types.AuditLog{}))
	require.Equal(t, int64(3), countProductionRows(t, fixture.db, &types.ProductionIdempotencyKey{}))
}

func TestProductionDocumentHTTPRejectsUngovernedAppendsWithoutRowsHeadOrAudit(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		evidenceDigest string
	}{
		{
			name: "unknown evidence",
			body: governedProductionVersionBody(map[string]any{
				"logical_block_id": "claim", "block_type": "paragraph", "content": "claim",
				"attributes": map[string]any{}, "evidence_refs": []string{"unknown-evidence"}, "ai_provenance": map[string]any{},
			}),
		},
		{
			name: "missing evidence",
			body: governedProductionVersionBody(map[string]any{
				"logical_block_id": "claim", "block_type": "paragraph", "content": "claim",
				"attributes": map[string]any{}, "evidence_refs": []string{}, "ai_provenance": map[string]any{},
			}),
		},
		{
			name: "unsupported block",
			body: governedProductionVersionBody(map[string]any{
				"logical_block_id": "quote", "block_type": "quote", "content": "claim",
				"attributes": map[string]any{"needs_confirmation": true}, "evidence_refs": []string{}, "ai_provenance": map[string]any{},
			}),
		},
		{
			name: "missing required sections",
			body: `{"source_set_id":"` + productionSourceSetID + `","origin":"human","blocks":[{"logical_block_id":"only","block_type":"heading","content":"基线范围与目标","attributes":{},"evidence_refs":[],"ai_provenance":{}}]}`,
		},
		{
			name: "mismatched evidence digest", body: validGovernedProductionVersionBody(),
			evidenceDigest: strings.Repeat("f", 64),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProductionDocumentsHTTPFixture(t)
			attachProductionFixtureEvidenceWithDigest(t, fixture, test.evidenceDigest)
			freeze := performProductionDocumentsHTTPRequest(
				fixture, "/production/source-sets/"+fixture.sourceSetID+"/freeze", "freeze", "", "",
			)
			require.Equal(t, http.StatusOK, freeze.Code)
			created := performProductionDocumentsHTTPRequest(
				fixture, "/production/projects/"+fixture.projectID+"/documents", "create",
				createProductionDocumentRequestBody(fixture), "",
			)
			require.Equal(t, http.StatusCreated, created.Code)
			document := decodeProductionDocumentResponse(t, created)
			bootstrapID := *document.CurrentVersionID
			beforeAudits := countProductionRows(t, fixture.db, &types.AuditLog{})

			response := performProductionDocumentsHTTPRequest(
				fixture, "/production/documents/"+document.ID+"/versions", "invalid-append", test.body, bootstrapID,
			)

			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Equal(t, int64(1), countProductionRows(t, fixture.db, &types.ProductionDocumentVersion{}))
			require.Zero(t, countProductionRows(t, fixture.db, &types.ProductionDocumentBlock{}))
			require.Zero(t, countProductionRows(t, fixture.db, &types.ProductionBlockLineage{}))
			require.Equal(t, beforeAudits, countProductionRows(t, fixture.db, &types.AuditLog{}))
			var persisted types.ProductionDocument
			require.NoError(t, fixture.db.First(&persisted, "id = ?", document.ID).Error)
			require.Equal(t, bootstrapID, *persisted.CurrentVersionID)
		})
	}
}

func TestProductionGovernedAuditFailuresRollBackHTTPMutations(t *testing.T) {
	t.Run("freeze", func(t *testing.T) {
		fixture := newProductionDocumentsHTTPFixture(t)
		attachProductionFixtureEvidence(t, fixture)
		fixture.audit.FailNext()

		response := performProductionDocumentsHTTPRequest(
			fixture, "/production/source-sets/"+fixture.sourceSetID+"/freeze", "freeze-audit-fails", "", "",
		)

		require.Equal(t, http.StatusInternalServerError, response.Code)
		require.NotContains(t, response.Body.String(), "forced governed audit failure")
		require.Equal(t, types.ProductionSourceSetCollecting, mustProductionSourceSet(t, fixture).Status)
		require.Zero(t, countProductionRows(t, fixture.db, &types.AuditLog{}))
		require.Zero(t, countProductionRows(t, fixture.db, &types.ProductionIdempotencyKey{}))
	})

	t.Run("bootstrap", func(t *testing.T) {
		fixture := newProductionDocumentsHTTPFixture(t)
		attachProductionFixtureEvidence(t, fixture)
		freeze := performProductionDocumentsHTTPRequest(
			fixture, "/production/source-sets/"+fixture.sourceSetID+"/freeze", "freeze-success", "", "",
		)
		require.Equal(t, http.StatusOK, freeze.Code)
		fixture.audit.FailNext()

		response := performProductionDocumentsHTTPRequest(
			fixture, "/production/projects/"+fixture.projectID+"/documents", "bootstrap-audit-fails",
			createProductionDocumentRequestBody(fixture), "",
		)

		require.Equal(t, http.StatusInternalServerError, response.Code)
		require.Zero(t, countProductionRows(t, fixture.db, &types.ProductionDocument{}))
		require.Zero(t, countProductionRows(t, fixture.db, &types.ProductionDocumentVersion{}))
		require.Equal(t, int64(1), countProductionRows(t, fixture.db, &types.AuditLog{}))
		require.Equal(t, int64(1), countProductionRows(t, fixture.db, &types.ProductionIdempotencyKey{}))
	})

	t.Run("append", func(t *testing.T) {
		fixture := newProductionDocumentsHTTPFixture(t)
		attachProductionFixtureEvidence(t, fixture)
		freeze := performProductionDocumentsHTTPRequest(
			fixture, "/production/source-sets/"+fixture.sourceSetID+"/freeze", "freeze-success", "", "",
		)
		require.Equal(t, http.StatusOK, freeze.Code)
		created := performProductionDocumentsHTTPRequest(
			fixture, "/production/projects/"+fixture.projectID+"/documents", "document-create",
			createProductionDocumentRequestBody(fixture), "",
		)
		require.Equal(t, http.StatusCreated, created.Code)
		document := decodeProductionDocumentResponse(t, created)
		require.NotNil(t, document.CurrentVersionID)
		bootstrapID := *document.CurrentVersionID
		fixture.audit.FailNext()

		response := performProductionDocumentsHTTPRequest(
			fixture, "/production/documents/"+document.ID+"/versions", "append-audit-fails",
			validGovernedProductionVersionBody(), bootstrapID,
		)

		require.Equal(t, http.StatusInternalServerError, response.Code)
		require.Equal(t, int64(1), countProductionRows(t, fixture.db, &types.ProductionDocumentVersion{}))
		require.Zero(t, countProductionRows(t, fixture.db, &types.ProductionDocumentBlock{}))
		var persisted types.ProductionDocument
		require.NoError(t, fixture.db.First(&persisted, "id = ?", document.ID).Error)
		require.Equal(t, bootstrapID, *persisted.CurrentVersionID)
		require.Equal(t, int64(2), countProductionRows(t, fixture.db, &types.AuditLog{}))
		require.Equal(t, int64(2), countProductionRows(t, fixture.db, &types.ProductionIdempotencyKey{}))
	})
}

func productionAudits(t *testing.T, db *gorm.DB) []*types.AuditLog {
	t.Helper()
	var audits []*types.AuditLog
	require.NoError(t, db.Order("id ASC").Find(&audits).Error)
	return audits
}

func mustProductionSourceSet(t *testing.T, fixture *productionDocumentsHTTPFixture) *types.ProductionSourceSet {
	t.Helper()
	set, err := fixture.sources.GetSet(context.Background(), 7, fixture.sourceSetID)
	require.NoError(t, err)
	return set
}

var _ interfaces.ProductionDocumentService = (*productionDocumentServiceStub)(nil)
var _ interfaces.ProductionProjectAuthorizer = (*productionHTTPAuthorizer)(nil)
