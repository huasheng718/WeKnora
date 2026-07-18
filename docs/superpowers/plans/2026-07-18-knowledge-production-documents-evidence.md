# Knowledge Production Documents and Evidence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add frozen source sets, immutable evidence snapshots, master documents, immutable versions, stable logical blocks, Markdown rendering and the two built-in document templates.

**Architecture:** Source collection and document authoring are separate aggregates linked by a frozen source-set ID. Evidence files reuse the existing resource registry; version content is normalized into block rows and never updated after creation.

**Tech Stack:** Go 1.26, GORM, PostgreSQL, SQLite, existing resource service, Gin, testify.

## Global Constraints

- Requires the Domain Foundation plan.
- PostgreSQL migration version is `000071`; SQLite migration version is `000002`.
- Accepted source items become immutable when their source set is frozen.
- Document versions and block rows are append-only.
- `(version_id, logical_block_id)` is unique.
- AI or human code cannot create a fact block with an invalid evidence reference.
- Evidence snapshots, blocks and versions store SHA-256 content digests; review and publication re-read and verify them.
- Every production write route uses the Domain Foundation `idempotency.Require()` middleware.
- Built-in type codes are `software-development-baseline` and `project-retrospective`.

---

### Task 1: Add Source and Document Migrations

**Files:**
- Create: `migrations/versioned/000071_knowledge_production_documents.up.sql`
- Create: `migrations/versioned/000071_knowledge_production_documents.down.sql`
- Create: `migrations/sqlite/000002_knowledge_production_documents.up.sql`
- Create: `migrations/sqlite/000002_knowledge_production_documents.down.sql`
- Modify: `internal/database/production_migration_test.go`

**Interfaces:**
- Consumes: production projects and document types from migration 000070/000001.
- Produces: source sets, source items, evidence snapshots, documents, versions and blocks.

- [ ] **Step 1: Extend the migration contract test**

```go
for _, table := range []string{
    "production_source_sets", "production_source_items", "production_evidence_snapshots",
    "production_documents", "production_document_versions", "production_document_blocks",
    "production_block_lineage",
} {
    requireMigrationTable(t, postgres, table)
    requireMigrationTable(t, sqlite, table)
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/database -run Production -v`
Expected: FAIL because migration 000071/000002 is absent.

- [ ] **Step 3: Add schemas and constraints**

Use separate row and logical block identifiers:

```sql
CREATE TABLE IF NOT EXISTS production_document_blocks (
    id VARCHAR(36) PRIMARY KEY,
    version_id VARCHAR(36) NOT NULL REFERENCES production_document_versions(id) ON DELETE CASCADE,
    logical_block_id VARCHAR(36) NOT NULL,
    block_type VARCHAR(24) NOT NULL,
    position INTEGER NOT NULL,
    content JSON NOT NULL,
    attributes JSON NOT NULL,
    evidence_refs JSON NOT NULL,
    ai_provenance JSON NOT NULL,
    content_digest VARCHAR(64) NOT NULL,
    UNIQUE(version_id, logical_block_id)
);

CREATE TABLE IF NOT EXISTS production_block_lineage (
    id VARCHAR(36) PRIMARY KEY,
    from_version_id VARCHAR(36) NOT NULL REFERENCES production_document_versions(id) ON DELETE CASCADE,
    from_logical_block_id VARCHAR(36) NOT NULL,
    to_version_id VARCHAR(36) NOT NULL REFERENCES production_document_versions(id) ON DELETE CASCADE,
    to_logical_block_id VARCHAR(36) NOT NULL,
    relation VARCHAR(24) NOT NULL,
    UNIQUE(from_version_id, from_logical_block_id, to_version_id, to_logical_block_id, relation)
);
```

Use `JSONB` for PostgreSQL and `TEXT` containing JSON for SQLite. Add `content_digest VARCHAR(64) NOT NULL` to evidence snapshots and document versions as well as blocks. Add indexes on tenant/project/document/source-set relationships.

- [ ] **Step 4: Run migration tests**

Run: `go test ./internal/database -run Production -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add migrations/versioned/000071_* migrations/sqlite/000002_* internal/database/production_migration_test.go
git commit -m "feat(production): add document and evidence schema"
```

### Task 2: Implement Frozen Source Sets and Evidence Snapshots

**Files:**
- Create: `internal/types/production_source.go`
- Create: `internal/types/interfaces/production_source.go`
- Create: `internal/application/repository/production_source.go`
- Create: `internal/application/repository/production_source_test.go`
- Create: `internal/application/service/production_source.go`
- Create: `internal/application/service/production_source_test.go`

**Interfaces:**
- Consumes: project-role authorization and existing `ResourceService`.
- Produces: `ProductionSourceService` with create, decide, attach evidence and freeze operations.

- [ ] **Step 1: Write failing freeze tests**

```go
func TestFrozenSourceSetRejectsNewItems(t *testing.T) {
    svc := newProductionSourceFixture(t, types.ProductionSourceSetFrozen)
    _, err := svc.AddItem(ctx, "source-set-1", types.CreateProductionSourceItemInput{Title: "late item"})
    require.ErrorIs(t, err, types.ErrProductionSourceSetFrozen)
}

func TestFreezeRequiresAcceptedEvidenceForEveryAcceptedItem(t *testing.T) {
    svc := newProductionSourceFixtureWithAcceptedItem(t, false)
    require.ErrorIs(t, svc.Freeze(ctx, "source-set-1"), types.ErrProductionEvidenceMissing)
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/application/repository ./internal/application/service -run ProductionSource -v`
Expected: FAIL with undefined source service.

- [ ] **Step 3: Implement repository transaction and service rules**

```go
type ProductionSourceService interface {
    CreateSet(ctx context.Context, input CreateProductionSourceSetInput) (*types.ProductionSourceSet, error)
    AddItem(ctx context.Context, sourceSetID string, input CreateProductionSourceItemInput) (*types.ProductionSourceItem, error)
    DecideItem(ctx context.Context, itemID string, decision types.ProductionSourceItemStatus) error
    AttachEvidence(ctx context.Context, itemID string, evidence CreateEvidenceSnapshotInput) (*types.ProductionEvidenceSnapshot, error)
    Freeze(ctx context.Context, sourceSetID string) error
}
```

Freeze locks the source-set row, validates accepted items, then updates `status=frozen` in the same transaction.

- [ ] **Step 4: Run source tests**

Run: `go test ./internal/application/repository ./internal/application/service -run ProductionSource -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/types/production_source.go internal/types/interfaces/production_source.go internal/application/repository/production_source* internal/application/service/production_source*
git commit -m "feat(production): add frozen evidence source sets"
```

### Task 3: Implement Immutable Documents, Versions and Blocks

**Files:**
- Create: `internal/types/production_document.go`
- Create: `internal/types/production_document_test.go`
- Create: `internal/types/interfaces/production_document.go`
- Create: `internal/application/repository/production_document.go`
- Create: `internal/application/repository/production_document_test.go`
- Create: `internal/application/service/production_document.go`
- Create: `internal/application/service/production_document_test.go`

**Interfaces:**
- Consumes: frozen source sets and active document types.
- Produces: document create, append-version, get-version and list-version operations.

- [ ] **Step 1: Write failing immutable-version tests**

```go
func TestAppendVersionPreservesLogicalBlockIDs(t *testing.T) {
    svc := newProductionDocumentFixture(t)
    v2, err := svc.AppendVersion(ctx, "document-1", AppendProductionVersionInput{
        ParentVersionID: "version-1",
        Blocks: []types.ProductionDocumentBlockInput{{LogicalBlockID: "block-a", BlockType: "paragraph"}},
    })
    require.NoError(t, err)
    require.Equal(t, 2, v2.VersionNumber)
    require.Equal(t, "block-a", v2.Blocks[0].LogicalBlockID)
}

func TestAppendVersionRecordsSplitBlockLineage(t *testing.T) {
    svc := newProductionDocumentFixture(t)
    v2, err := svc.AppendVersion(ctx, "document-1", AppendProductionVersionInput{
        ParentVersionID: "version-1",
        Blocks: []types.ProductionDocumentBlockInput{
            {LogicalBlockID: "block-a", BlockType: "paragraph"},
            {LogicalBlockID: "block-b", BlockType: "paragraph"},
        },
        Lineage: []types.ProductionBlockLineageInput{
            {FromLogicalBlockID: "block-a", ToLogicalBlockID: "block-a", Relation: types.ProductionBlockRelationSplit},
            {FromLogicalBlockID: "block-a", ToLogicalBlockID: "block-b", Relation: types.ProductionBlockRelationSplit},
        },
    })
    require.NoError(t, err)
    require.Len(t, v2.Lineage, 2)
    require.Equal(t, "version-1", v2.Lineage[0].FromVersionID)
    require.Equal(t, v2.ID, v2.Lineage[0].ToVersionID)
}

func TestRepositoryHasNoVersionUpdateMethod(t *testing.T) {
    var _ interfaces.ProductionDocumentRepository = repository.NewProductionDocumentRepository(nil)
}

func TestAppendVersionComputesBlockAndVersionDigests(t *testing.T) {
    version := appendVersionFixture(t, paragraphBlock("block-a", "baseline"))
    require.Len(t, version.Blocks[0].ContentDigest, 64)
    require.Len(t, version.ContentDigest, 64)
    require.Equal(t, version.ContentDigest, ComputeProductionVersionDigest(version))
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/types ./internal/application/repository ./internal/application/service -run ProductionDocument -v`
Expected: FAIL with undefined document contracts.

- [ ] **Step 3: Implement append-only repository and service**

```go
type ProductionDocumentRepository interface {
    CreateDocument(ctx context.Context, document *types.ProductionDocument) error
    GetDocument(ctx context.Context, tenantID uint64, id string) (*types.ProductionDocument, error)
    AppendVersion(
        ctx context.Context,
        version *types.ProductionDocumentVersion,
        blocks []*types.ProductionDocumentBlock,
        lineage []*types.ProductionBlockLineage,
    ) error
    GetVersion(ctx context.Context, tenantID uint64, versionID string) (*types.ProductionDocumentVersion, error)
    ListVersions(ctx context.Context, tenantID uint64, documentID string) ([]*types.ProductionDocumentVersion, error)
}
```

AppendVersion locks the document head, allocates `version_number=current+1`, inserts the version, blocks and validated lineage rows, then updates only `production_documents.current_version_id`. Lineage endpoints must identify blocks in the declared parent and new versions; allowed relations are `same`, `split` and `merged`.

- [ ] **Step 4: Run document tests**

Run: `go test ./internal/types ./internal/application/repository ./internal/application/service -run ProductionDocument -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/types/production_document* internal/types/interfaces/production_document.go internal/application/repository/production_document* internal/application/service/production_document*
git commit -m "feat(production): add immutable document versions"
```

### Task 4: Add Templates, Evidence Validation and Markdown Rendering

**Files:**
- Create: `internal/application/service/production_templates.go`
- Create: `internal/application/service/production_templates_test.go`
- Create: `internal/application/service/production_evidence_validator.go`
- Create: `internal/application/service/production_evidence_validator_test.go`
- Create: `internal/application/service/production_markdown_renderer.go`
- Create: `internal/application/service/production_markdown_renderer_test.go`

**Interfaces:**
- Consumes: document type block schema, accepted evidence IDs and immutable blocks.
- Produces: built-in definitions, `ValidateProductionVersion`, digest verification, and deterministic Markdown renderer.

- [ ] **Step 1: Write failing template and renderer tests**

```go
func TestSoftwareBaselineTemplateHasRequiredSections(t *testing.T) {
    got := BuiltinSoftwareDevelopmentBaseline()
    require.Equal(t, "software-development-baseline", got.Code)
    require.Contains(t, got.RequiredSections, "测试、质量和安全基线")
    require.Contains(t, got.RequiredSections, "证据清单")
}

func TestRendererIsDeterministic(t *testing.T) {
    first := RenderProductionMarkdown(versionFixture())
    second := RenderProductionMarkdown(versionFixture())
    require.Equal(t, first, second)
    require.Contains(t, first, "[^evidence:e-1]")
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/application/service -run 'Template|EvidenceValidator|Renderer' -v`
Expected: FAIL with missing functions.

- [ ] **Step 3: Implement exact validation contract**

```go
type ProductionValidationResult struct {
    Errors   []ProductionValidationIssue
    Warnings []ProductionValidationIssue
}

func ValidateProductionVersion(version *types.ProductionDocumentVersion, acceptedEvidence map[string]struct{}) ProductionValidationResult
func VerifyProductionVersionDigests(version *types.ProductionDocumentVersion, evidenceByID map[string]*types.ProductionEvidenceSnapshot) error
func RenderProductionMarkdown(version *types.ProductionDocumentVersion) (string, error)
```

Canonicalize JSON before hashing. Reject missing required sections, duplicate logical block IDs, unknown evidence IDs, factual blocks without evidence or an explicit `needs_confirmation=true` attribute, and any evidence/block/version digest mismatch.

- [ ] **Step 4: Run service tests**

Run: `go test ./internal/application/service -run 'Production.*(Template|Evidence|Markdown)' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/application/service/production_templates* internal/application/service/production_evidence_validator* internal/application/service/production_markdown_renderer*
git commit -m "feat(production): validate and render governed documents"
```

### Task 5: Expose Source and Document APIs

**Files:**
- Create: `internal/handler/production_source.go`
- Create: `internal/handler/production_source_test.go`
- Create: `internal/handler/production_document.go`
- Create: `internal/handler/production_document_test.go`
- Modify: `internal/router/router.go`
- Modify: `internal/router/router_production_test.go`
- Modify: `internal/container/container.go`
- Modify: `internal/types/audit_log.go`

**Interfaces:**
- Consumes: source/document services and project-role authorization.
- Produces: source-set, source-item, evidence, document and version HTTP endpoints.

- [ ] **Step 1: Add failing route and handler tests**

```go
assertRoute(t, engine, http.MethodPost, "/api/v1/production/projects/:id/source-sets")
assertRoute(t, engine, http.MethodPost, "/api/v1/production/source-sets/:id/freeze")
assertRoute(t, engine, http.MethodPost, "/api/v1/production/projects/:id/documents")
assertRoute(t, engine, http.MethodGet, "/api/v1/production/documents/:id/versions")

func TestAppendVersionRejectsStaleIfMatch(t *testing.T) {
    rec := appendVersionRequest(t, engine, "document-1", "version-stale", validVersionBody())
    require.Equal(t, http.StatusConflict, rec.Code)
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/handler ./internal/router -run Production -v`
Expected: FAIL because routes are missing.

- [ ] **Step 3: Register APIs and audit actions**

Add `production.source_frozen` and `production.version_created`. Route reads through `Viewer()` and mutations through `Contributor()`, then enforce project roles inside services. `AppendVersion` requires `If-Match: <current-version-id>` and returns HTTP 409 without inserting rows when the document head has changed.

```go
production.POST("/projects/:id/source-sets", g.Contributor(), idempotency.Require(), sourceHandler.CreateSet)
production.PUT("/source-items/:id/decision", g.Contributor(), idempotency.Require(), sourceHandler.DecideItem)
production.POST("/source-sets/:id/freeze", g.Contributor(), idempotency.Require(), sourceHandler.Freeze)
production.POST("/projects/:id/documents", g.Contributor(), idempotency.Require(), documentHandler.Create)
production.POST("/documents/:id/versions", g.Contributor(), idempotency.Require(), documentHandler.AppendVersion)
```

- [ ] **Step 4: Run focused package tests**

Run: `go test ./internal/types/... ./internal/application/repository ./internal/application/service ./internal/handler ./internal/router -run Production -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handler/production_* internal/router/router.go internal/router/router_production_test.go internal/container/container.go internal/types/audit_log.go
git commit -m "feat(production): expose document and evidence APIs"
```

## Plan Verification

Run: `go test ./internal/database ./internal/types/... ./internal/application/repository ./internal/application/service ./internal/handler ./internal/router`
Expected: PASS, with immutable version and evidence validation tests included.
