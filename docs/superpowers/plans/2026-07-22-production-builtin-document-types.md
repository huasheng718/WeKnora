# Production Built-in Document Types Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every workspace five active, governed document types that can be used immediately, inspected, derived into custom versions, and enforced throughout source, writing, validation, review, and publication workflows.

**Architecture:** A typed Go catalog is the runtime source of truth for the five v1 templates. PostgreSQL and SQLite migrations persist immutable tenant-scoped copies for existing workspaces, while `TenantService` provisions the same catalog transactionally for new workspaces. Document-type snapshots, rather than global code switches, drive downstream governance; the existing management page gains origin, configuration inspection, and server-serialized draft derivation.

**Tech Stack:** Go 1.24, Gin, GORM, PostgreSQL, SQLite, Vue 3, TypeScript, TDesign Vue Next, Node test runner, Playwright.

## Global Constraints

- Preserve the user's unstaged `.gitignore` change; never stage or alter it.
- Built-ins are exactly `sop`, `policy_process`, `product_service_guide`, `faq`, and `incident_playbook`.
- Built-in v1 rows are `origin=builtin`, `schema_version=1`, `status=active`, and immutable.
- Derived rows retain `code` and `template_key`, use `origin=custom`, and receive the next version on the server.
- Existing same-code custom rows are preserved; their tenant skips only the colliding built-in.
- Default Skill bindings are `{"version":1,"skills":[]}` and workflow plans are `{"version":1,"steps":[]}`.
- Review defaults are business for SOP/product/FAQ, business then compliance for policy, engineering then business for incident.
- All tenant creation paths provision storage and five built-ins in one transaction.
- Model `fact` output is normalized before persistence to `paragraph` with `factual=true` and governed `needs_confirmation`; persisted `block_schema.allowed_block_types` remains the existing seven types and raw persisted `fact` remains invalid.
- PostgreSQL and SQLite behavior must remain equivalent.
- Four locales are required: `zh-CN`, `en-US`, `ko-KR`, and `ru-RU`.
- No document-type deletion, in-place active editing, visual rule builder, or automatic template upgrade.

---

## File Map

- `internal/types/production_document_type.go`: persisted origin and template lineage.
- `internal/types/production_document_type_config.go`: strict v1 JSON contracts and canonical validation.
- `internal/application/service/production_templates.go`: five immutable catalog definitions.
- `internal/application/repository/production_document_type.go`: seeding, derivation locks, version persistence.
- `internal/application/service/production_document_type.go`: authorization, validation, derivation, audit.
- `internal/application/service/tenant.go`: transactional workspace provisioning.
- `internal/application/service/production_source.go`: source requirement enforcement before freeze.
- `internal/application/service/production_writer.go`: prompt construction from the immutable snapshot.
- `internal/application/service/production_evidence_validator.go`: block and quality-rule enforcement.
- `internal/application/service/production_review.go`: policy validation and reviewer availability.
- `internal/application/service/production_release.go`: publication policy enforcement.
- `internal/handler/production_document_type.go` and `internal/router/router.go`: derivation HTTP command.
- `migrations/versioned/000076_production_builtin_document_types.*.sql`: PostgreSQL schema/backfill.
- `migrations/sqlite/000012_production_builtin_document_types.*.sql`: SQLite schema/backfill.
- `frontend/src/api/production/index.ts`: response and derivation command types.
- `frontend/src/views/production/models/documentTypeManagement.ts`: pure view/form transformations.
- `frontend/src/views/production/ProductionDocumentTypeManagement.vue`: badges, inspection, derivation UI.
- `frontend/src/i18n/locales/*.ts`: four-locale copy.
- `frontend/e2e/production-document-types.spec.ts`: live backend desktop/mobile acceptance.

---

### Task 1: Repair Baseline Contracts

**Files:**
- Modify: `internal/database/production_migration_test.go`
- Modify: `internal/application/service/production_evidence_validator_test.go`
- Modify: `internal/application/service/production_writer_test.go`

**Interfaces:**
- Consumes: consolidated publication migration `000074_knowledge_production_publication`.
- Produces: regression coverage for the established model-output normalization boundary and a migration test suite with no deleted file references.

- [ ] **Step 1: Write failing regression tests**

Add a writer regression case proving model `block_type=fact` is persisted as an evidence-governed paragraph:

```go
func TestProductionWriterNormalizesFactToGovernedParagraph(t *testing.T) {
    // Use the existing writer fixture with one model fact and accepted evidence.
    // Assert persisted BlockType is paragraph, attributes.factual is true,
    // needs_confirmation follows evidence availability, and content is a string.
}
```

Add a validator regression proving a raw persisted `fact` still returns `unsupported_block_type`, while the normalized factual paragraph is accepted only with accepted evidence or `needs_confirmation=true`.

Replace the four stale `000075_knowledge_production_projection_integrity` / `000076_production_projection_failure_recovery` file assertions with checks against the consolidated `000074` up/down migrations. Add an assertion that no production migration test names a nonexistent file.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/database ./internal/application/service -run 'TestProduction.*Migration|TestProductionWriterNormalizesFact|TestValidateProductionVersion.*Fact' -count=1
```

Expected: migration assertions fail until stale paths are removed; any missing normalization/validator regression coverage fails before production code is changed.

- [ ] **Step 3: Implement the minimum baseline fix**

Replace the four stale migration-file references with the consolidated `000074` up/down files and add a filesystem-existence assertion for every migration path named by this test suite. Keep the established writer behavior unchanged: its model schema requests `block_type=fact`, then normalizes it before persistence to `block_type=paragraph`, string content, `factual=true`, and evidence-derived `needs_confirmation`. Do not add `fact` to `productionValidateBlockContent` or to persisted allowed block types.

- [ ] **Step 4: Run focused and package tests**

Run:

```bash
go test ./internal/database ./internal/application/service -run 'TestProduction.*Migration|TestValidateProductionVersion|TestProductionWriter' -count=1
```

Expected: PASS with no attempt to open deleted migration files.

- [ ] **Step 5: Commit**

```bash
git add internal/database/production_migration_test.go internal/application/service/production_evidence_validator_test.go internal/application/service/production_writer_test.go
git commit -m "fix(production): align migration and fact normalization contracts"
```

---

### Task 2: Define Strict Configuration Contracts and the Five-Type Catalog

**Files:**
- Create: `internal/types/production_document_type_config.go`
- Create: `internal/types/production_document_type_config_test.go`
- Modify: `internal/application/service/production_templates.go`
- Modify: `internal/application/service/production_templates_test.go`

**Interfaces:**
- Produces: `CanonicalProductionDocumentTypeConfig(input ProductionDocumentTypeConfigInput) (ProductionDocumentTypeConfig, error)`.
- Produces: `NewProductionBuiltinCatalog() (*ProductionBuiltinCatalog, error)`, `Definitions() []ProductionBuiltinTemplate`, `Rows(tenantID uint64, actor string) []types.ProductionDocumentType`, and `Lookup(code string) (ProductionBuiltinTemplate, bool)`.
- Consumed by: Tasks 3-6 for migrations, seeding, create/derive validation, and downstream execution.

- [ ] **Step 1: Write strict contract tests**

Cover valid defaults, unknown fields, wrong version, duplicate sections/gates, unknown block/source/reviewer values, empty review steps, invalid publication inheritance, and canonical byte equality. Use the exact input carrier:

```go
type ProductionDocumentTypeConfigInput struct {
    BlockSchema, SourceRequirements, SkillBindings, WorkflowPlan JSON
    QualityRules, ReviewPolicy, PublicationPolicy JSON
}
```

Assert these stable gate codes:

```go
var expectedGates = map[string][]string{
    "sop": {"section_completeness", "fact_evidence", "no_unconfirmed", "sop_exception_path"},
    "policy_process": {"section_completeness", "fact_evidence", "no_unconfirmed", "policy_approval_control"},
    "product_service_guide": {"section_completeness", "fact_evidence", "no_unconfirmed", "product_scope_boundary"},
    "faq": {"section_completeness", "fact_evidence", "no_unconfirmed", "faq_effective_date"},
    "incident_playbook": {"section_completeness", "fact_evidence", "no_unconfirmed", "incident_timeline"},
}
```

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./internal/types ./internal/application/service -run 'TestCanonicalProductionDocumentTypeConfig|TestProductionBuiltin' -count=1
```

Expected: FAIL because the strict config types and five catalog entries do not exist.

- [ ] **Step 3: Implement strict decoders and canonical outputs**

Define v1 structs with `json.Decoder.DisallowUnknownFields`, uniqueness checks, size/depth limits, and these exact shapes:

```go
type ProductionBlockSchemaV1 struct {
    Version int `json:"version"`
    RequiredSections []string `json:"required_sections"`
    AllowedBlockTypes []string `json:"allowed_block_types"`
}
type ProductionSourceRequirementsV1 struct {
    Version int `json:"version"`
    MinAcceptedEvidence int `json:"min_accepted_evidence"`
    AllowedSourceKinds []ProductionSourceKind `json:"allowed_source_kinds"`
    RequireEvidenceSection bool `json:"require_evidence_section"`
    AllowUnsupportedFacts bool `json:"allow_unsupported_facts"`
}
type ProductionQualityRulesV1 struct {
    Version int `json:"version"`
    RequireEvidenceForFacts bool `json:"require_evidence_for_facts"`
    BlockNeedsConfirmation bool `json:"block_needs_confirmation"`
    Gates []string `json:"gates"`
}
type ProductionPublicationPolicyV1 struct {
    Version int `json:"version"`
    TargetType string `json:"target_type"`
    Chunking string `json:"chunking"`
    KnowledgeGraph string `json:"knowledge_graph"`
    RequireApprovedReview bool `json:"require_approved_review"`
}
```

Return both decoded structs and canonical JSON bytes so persistence and digest callers use the same representation.

- [ ] **Step 4: Replace the two-type switch with the five-type catalog**

Replace the global two-type switch with a validated `ProductionBuiltinCatalog`. `Definitions` returns only the five confirmed types; `Lookup` also contains explicit legacy adapters for existing software baseline and retrospective documents. Use these exact section arrays:

```go
var requiredSections = map[string][]string{
    "sop": {"目的与范围", "角色职责", "前置条件", "操作步骤", "异常处理", "风险控制", "验证记录", "证据清单"},
    "policy_process": {"制定依据", "适用范围", "术语定义", "职责权限", "制度要求", "业务流程", "审批控制", "例外处理", "监督机制", "证据清单"},
    "product_service_guide": {"产品定位", "核心能力", "适用场景", "使用前提", "配置与使用", "限制条件", "服务标准", "常见故障", "升级路径", "证据清单"},
    "faq": {"问题分类", "适用条件", "标准问题与回答", "例外情况", "处理建议", "升级路径", "来源与生效日期"},
    "incident_playbook": {"现象与影响", "事件等级", "止损措施", "诊断过程", "恢复步骤", "结果验证", "沟通升级", "根因分析", "改进行动", "证据清单"},
}
```

Each value combines its section array with the gate codes above, the confirmed review order, empty Skill/workflow defaults, and `knowledge_base` / `inherit_target` publication settings. `NewProductionBuiltinCatalog` canonicalizes all five, rejects duplicate code/template keys, and returns an error so the dependency container fails startup rather than serving invalid templates.

- [ ] **Step 5: Run tests**

```bash
go test ./internal/types ./internal/application/service -run 'TestCanonicalProductionDocumentTypeConfig|TestProductionBuiltin' -count=1
```

Expected: PASS; catalog length is exactly 5 and legacy lookup remains readable.

- [ ] **Step 6: Commit**

```bash
git add internal/types/production_document_type_config.go internal/types/production_document_type_config_test.go internal/application/service/production_templates.go internal/application/service/production_templates_test.go
git commit -m "feat(production): define governed built-in type catalog"
```

---

### Task 3: Add Origin, Template Lineage, and Existing-Tenant Backfill

**Files:**
- Create: `migrations/versioned/000076_production_builtin_document_types.up.sql`
- Create: `migrations/versioned/000076_production_builtin_document_types.down.sql`
- Create: `migrations/sqlite/000012_production_builtin_document_types.up.sql`
- Create: `migrations/sqlite/000012_production_builtin_document_types.down.sql`
- Modify: `internal/database/production_migration_test.go`
- Modify: `internal/types/production_document_type.go`
- Modify: `internal/types/production_project_test.go`
- Modify: `internal/application/repository/production_document_type.go`
- Modify: `internal/application/repository/production_document_type_test.go`

**Interfaces:**
- Produces: `ProductionDocumentTypeOrigin`, constants `builtin` / `custom`, nullable `TemplateKey`.
- Produces repository method `SeedBuiltins(ctx context.Context, tenantID uint64, actor string, definitions []types.ProductionDocumentType) error`.
- Consumed by: transactional tenant provisioning and frontend API responses.

- [ ] **Step 1: Write migration and model tests**

Use SQLite integration tests to create two tenants, add a pre-existing custom `faq` to one, apply `000012`, and assert built-in counts `5` and `4` respectively (the collision tenant still has five total live types including its custom FAQ), preserved custom content, `origin=custom` for history, immutable origin/template fields, and idempotent `INSERT ... WHERE NOT EXISTS` behavior. Apply down on an unreferenced fixture and assert custom rows survive while built-in rows, new indexes, and new columns are removed. Add PostgreSQL SQL-shape assertions for equivalent columns, constraints, indexes, backfill codes, trigger guards, and reverse order.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/database ./internal/types ./internal/application/repository -run 'TestProductionBuiltinDocumentTypeMigration|TestProductionDocumentType.*Origin|TestProductionDocumentTypeRepositorySeeds' -count=1
```

Expected: FAIL because migrations, fields, and seeding contract do not exist.

- [ ] **Step 3: Implement model and migrations**

Add:

```go
type ProductionDocumentTypeOrigin string
const (
    ProductionDocumentTypeOriginBuiltin ProductionDocumentTypeOrigin = "builtin"
    ProductionDocumentTypeOriginCustom ProductionDocumentTypeOrigin = "custom"
)
// On ProductionDocumentType:
Origin ProductionDocumentTypeOrigin `json:"origin" gorm:"type:varchar(16);not null;default:'custom'"`
TemplateKey *string `json:"template_key,omitempty" gorm:"type:varchar(255)"`
```

Both migrations must:

1. add `origin` with default `custom` and nullable `template_key`;
2. add checks for valid origin and built-in template keys;
3. add partial unique `(tenant_id, template_key, schema_version) WHERE template_key IS NOT NULL AND deleted_at IS NULL`;
4. rebuild/replace immutable triggers to guard both fields;
5. insert v1 rows for the five exact codes with valid UUIDs, `created_by='system:builtin-document-types'`, canonical JSON from Task 2, and `NOT EXISTS` checks for both code and template key;
6. skip only a tenant/code pair with a pre-existing live same-code row.

Use `uuid_generate_v4()::varchar(36)` in PostgreSQL and canonical randomblob hex groups in SQLite. PostgreSQL down deletes unreferenced built-ins, restores the previous immutable trigger, drops the lineage index/constraints, then drops the two columns. SQLite down rebuilds `production_document_types` with its original columns, copies only custom rows, and recreates the original indexes/triggers. A referenced built-in causes down to fail through existing foreign keys instead of orphaning production data.

- [ ] **Step 4: Make repositories transaction-context aware and add seeding**

Replace direct `r.db.WithContext(ctx)` writes in tenant, storage backend, and document-type repositories with:

```go
database.DBFromContext(ctx, r.db).WithContext(ctx)
```

`SeedBuiltins` inserts catalog-derived rows with `ON CONFLICT DO NOTHING` semantics translated through GORM and then verifies that every non-colliding template exists.

- [ ] **Step 5: Run migration and repository tests**

```bash
go test ./internal/database ./internal/types ./internal/application/repository -run 'TestProductionBuiltinDocumentTypeMigration|TestProductionDocumentType' -count=1
```

Expected: PASS for SQLite execution and PostgreSQL/SQLite parity assertions.

- [ ] **Step 6: Commit**

```bash
git add migrations/versioned/000076_production_builtin_document_types.* migrations/sqlite/000012_production_builtin_document_types.* internal/database/production_migration_test.go internal/types/production_document_type.go internal/types/production_project_test.go internal/application/repository/production_document_type.go internal/application/repository/production_document_type_test.go internal/application/repository/tenant.go internal/application/repository/storagebackend.go
git commit -m "feat(production): persist built-in document type lineage"
```

---

### Task 4: Provision New Workspaces Atomically

**Files:**
- Modify: `internal/types/interfaces/production_document_type.go`
- Modify: `internal/application/service/tenant.go`
- Modify: `internal/application/service/tenant_storagebackend_test.go`
- Modify: `internal/container/container.go`

**Interfaces:**
- Consumes: `ProductionUnitOfWork`, `ProductionDocumentTypeRepository.SeedBuiltins`, and `ProductionBuiltinCatalog.Rows`.
- Produces: `TenantService.CreateTenant` with tenant + storage + built-in atomicity for every caller.

- [ ] **Step 1: Write atomic provisioning tests**

Extend the real SQLite fixture to migrate `Tenant`, `StorageBackend`, and `ProductionDocumentType`. Assert one call creates exactly five active built-ins. Wrap the document-type repository with a failure injector and assert no tenant, backend, or type rows remain:

```go
type failingBuiltinSeeder struct{ interfaces.ProductionDocumentTypeRepository }
func (f failingBuiltinSeeder) SeedBuiltins(context.Context, uint64, string, []types.ProductionDocumentType) error {
    return errors.New("seed failure")
}
```

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/application/service -run 'TestCreateTenant.*Builtin|TestCreateTenantCreatesConcreteDefaultStorageBackend' -count=1
```

Expected: FAIL because tenant creation commits before storage/type provisioning.

- [ ] **Step 3: Implement one provisioning transaction**

Change the constructor to accept the UOW and document types:

```go
func NewTenantService(
    repo interfaces.TenantRepository,
    storageRepo interfaces.StorageBackendRepository,
    documentTypes interfaces.ProductionDocumentTypeRepository,
    uow interfaces.ProductionUnitOfWork,
    catalog *ProductionBuiltinCatalog,
) interfaces.TenantService
```

After validation, execute `CreateTenant`, `createDefaultStorageBackend`, and `SeedBuiltins(txCtx, tenant.ID, systemActor, catalog.Rows(tenant.ID, systemActor))` inside one `uow.WithinTransaction`. Remove compensating soft-delete rollback from this path because the database transaction is authoritative.

- [ ] **Step 4: Run tenant, registration, and handler tests**

```bash
go test ./internal/application/service ./internal/handler -run 'Tenant|Register|Provision' -count=1
```

Expected: PASS; existing registration and self-service callers need no changes because they already call `TenantService.CreateTenant`.

- [ ] **Step 5: Commit**

```bash
git add internal/types/interfaces/production_document_type.go internal/application/service/tenant.go internal/application/service/tenant_storagebackend_test.go internal/container/container.go
git commit -m "feat(tenant): provision built-in document types atomically"
```

---

### Task 5: Add Validated Draft Derivation API

**Files:**
- Modify: `internal/types/interfaces/production_document_type.go`
- Modify: `internal/types/audit_log.go`
- Modify: `internal/application/repository/production_document_type.go`
- Modify: `internal/application/repository/production_document_type_test.go`
- Modify: `internal/application/service/production_document_type.go`
- Modify: `internal/application/service/production_document_type_test.go`
- Modify: `internal/handler/production_document_type.go`
- Modify: `internal/handler/production_document_type_test.go`
- Modify: `internal/router/router.go`
- Modify: `internal/router/router_production_test.go`

**Interfaces:**
- Produces: `DeriveProductionDocumentTypeInput` containing editable name, description, and seven JSON configs.
- Produces: `DeriveDraft(ctx, tenantID, baseID string, input DeriveProductionDocumentTypeInput) (*types.ProductionDocumentType, error)`.
- Produces HTTP: `POST /api/v1/production/document-types/:id/drafts` with required `Idempotency-Key`.

- [ ] **Step 1: Write service/repository/HTTP failures first**

Tests must cover admin/owner success, contributor denial, cross-tenant base ID, immutable lineage, strict config error, retired-base derivation, two concurrent requests producing consecutive versions, same-idempotency replay, and route guard registration.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/application/repository ./internal/application/service ./internal/handler ./internal/router -run 'DocumentType.*Deriv|ProductionRoutes' -count=1
```

Expected: FAIL because the derivation interfaces and route do not exist.

- [ ] **Step 3: Canonicalize all create and derive inputs**

In `CreateDocumentType`, call Task 2 validation before persistence and force `OriginCustom`, `TemplateKey=nil`. In `DeriveDraft`, authorize, canonicalize, and pass a fully validated input to the repository. Add audit action:

```go
AuditActionProductionDocumentTypeDerived AuditAction = "production.document_type_derived"
```

Audit details include `base_document_type_id`, `base_schema_version`, and `derived_schema_version`.

- [ ] **Step 4: Serialize version allocation in the repository**

Within one transaction, load the base tenant-scoped row, acquire a PostgreSQL advisory/row lock or SQLite writer reservation for `(tenant_id, code)`, calculate `COALESCE(MAX(schema_version),0)+1`, and insert the custom draft. Retry unique conflicts only inside the bounded repository operation; never trust a client version.

- [ ] **Step 5: Add handler and route**

Bind the editable request, validate the path UUID, call `DeriveDraft`, and return `201`. Register:

```go
production.POST("/document-types/:id/drafts", g.Admin(), idempotency.Require(), documentTypeHandler.DeriveDraft)
```

- [ ] **Step 6: Run focused tests**

```bash
go test ./internal/application/repository ./internal/application/service ./internal/handler ./internal/router -run 'DocumentType|ProductionRoutes' -count=1
```

Expected: PASS with race-safe versions and no success audit on failed writes.

- [ ] **Step 7: Commit**

```bash
git add internal/types/interfaces/production_document_type.go internal/types/audit_log.go internal/application/repository/production_document_type.go internal/application/repository/production_document_type_test.go internal/application/service/production_document_type.go internal/application/service/production_document_type_test.go internal/handler/production_document_type.go internal/handler/production_document_type_test.go internal/router/router.go internal/router/router_production_test.go
git commit -m "feat(production): derive governed document type drafts"
```

---

### Task 6: Enforce Snapshotted Governance End to End

**Files:**
- Modify: `internal/types/interfaces/production_source.go`
- Modify: `internal/application/repository/production_source.go`
- Modify: `internal/application/repository/production_source_test.go`
- Modify: `internal/application/service/production_source.go`
- Modify: `internal/application/service/production_source_test.go`
- Modify: `internal/application/service/production_writer.go`
- Modify: `internal/application/service/production_writer_test.go`
- Modify: `internal/application/service/production_validator.go`
- Modify: `internal/application/service/production_validator_test.go`
- Modify: `internal/application/service/production_evidence_validator.go`
- Modify: `internal/application/service/production_evidence_validator_test.go`
- Modify: `internal/application/service/production_document.go`
- Modify: `internal/application/service/production_document_test.go`
- Modify: `internal/application/service/production_review.go`
- Modify: `internal/application/service/production_review_test.go`
- Modify: `internal/types/interfaces/production_project.go`
- Modify: `internal/application/repository/production_project.go`
- Modify: `internal/application/repository/production_project_test.go`
- Modify: `internal/application/service/production_release.go`
- Modify: `internal/application/service/production_release_test.go`
- Modify: `internal/application/service/production_run.go`
- Modify: `internal/container/container.go`

**Interfaces:**
- Consumes: canonical config structs from Task 2 and immutable document-type snapshots.
- Produces: `ListAcceptedSourceKinds(ctx, tenantID, projectID, sourceSetID) ([]types.ProductionSourceKind, error)`.
- Produces: `HasLiveRoleAssignee(ctx context.Context, tenantID uint64, projectID string, role types.ProductionRole) (bool, error)`.
- Produces stable validation errors for source requirements, reviewer availability, quality gates, and publication policy.

- [ ] **Step 1: Write one failing integration test per governance boundary**

Add tests proving:

1. freeze fails below `min_accepted_evidence` or on a forbidden source kind;
2. writer prompt contains snapshot sections/gates for `sop`, not a global lookup;
3. validator rejects a missing required section, an evidence-free factual paragraph, and a `needs_confirmation` block;
4. review submission rejects policies whose required project role has no assignee;
5. release rejects non-knowledge-base target policy, disabled approved-review requirement, or non-inherit processing values;
6. retiring the type after run creation does not alter the run snapshot outcome.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/application/repository ./internal/application/service -run 'SourceRequirements|SnapshotGovernance|ReviewerAvailability|PublicationPolicy|QualityRules' -count=1
```

Expected: FAIL because downstream services currently ignore most configuration fields and writer/validator use the code switch.

- [ ] **Step 3: Enforce source requirements before freeze**

Inject `ProductionDocumentTypeRepository` into `ProductionSourceService`. Load the source set's active type, decode `SourceRequirements`, query accepted evidence and distinct source kinds, then validate before calling repository `Freeze`. Return `ErrProductionDocumentSourceSetInvalid` joined with stable reason codes.

- [ ] **Step 4: Make writer and validator snapshot-driven**

Expand `productionWriterDocumentTypeSnapshot` to decode all seven configs. Replace `BuiltinProductionTemplate(documentType.Code)` calls with decoded `BlockSchema` / `QualityRules`. Update `ValidateProductionVersion` to accept the decoded config explicitly:

```go
func ValidateProductionVersion(
    version *types.ProductionDocumentVersion,
    acceptedEvidence map[string]struct{},
    blockSchema types.ProductionBlockSchemaV1,
    qualityRules types.ProductionQualityRulesV1,
) ProductionValidationResult
```

Implement the stable gates as deterministic checks: the three generic gates validate section/evidence/confirmation rules; each type-specific gate requires its named required heading (`异常处理`, `审批控制`, `限制条件`, `来源与生效日期`, or `诊断过程`).

- [ ] **Step 5: Enforce reviewer availability and publication policy**

Extend the project repository/authorizer with a role-assignee query and check every materialized review step before creating the request. Inject document types into `ProductionReleaseService`, load the exact document-bound type ID/schema version even if retired, decode `PublicationPolicy`, and require `target_type=knowledge_base`, both processing modes `inherit_target`, and approved review.

- [ ] **Step 6: Run focused and production package tests**

```bash
go test ./internal/application/repository ./internal/application/service ./internal/container -run 'Production|SourceRequirements|SnapshotGovernance|ReviewerAvailability|PublicationPolicy|QualityRules' -count=1
```

Expected: PASS; legacy software baseline and retrospective fixtures remain compatible through explicit legacy config adapters.

- [ ] **Step 7: Commit**

```bash
git add internal/types/interfaces/production_source.go internal/application/repository/production_source.go internal/application/repository/production_source_test.go internal/application/service/production_source.go internal/application/service/production_source_test.go internal/application/service/production_writer.go internal/application/service/production_writer_test.go internal/application/service/production_validator.go internal/application/service/production_validator_test.go internal/application/service/production_evidence_validator.go internal/application/service/production_evidence_validator_test.go internal/application/service/production_document.go internal/application/service/production_document_test.go internal/application/service/production_review.go internal/application/service/production_review_test.go internal/types/interfaces/production_project.go internal/application/repository/production_project.go internal/application/repository/production_project_test.go internal/application/service/production_release.go internal/application/service/production_release_test.go internal/application/service/production_run.go internal/container/container.go
git commit -m "feat(production): enforce document type governance snapshots"
```

---

### Task 7: Add Built-in Inspection and Draft Derivation UI

**Files:**
- Modify: `frontend/src/api/production/index.ts`
- Modify: `frontend/src/views/production/models/documentTypeManagement.ts`
- Modify: `frontend/src/views/production/models/documentTypeManagement.test.ts`
- Modify: `frontend/src/views/production/ProductionDocumentTypeManagement.vue`
- Modify: `frontend/src/views/production/productionDocumentTypeManagement.test.ts`
- Modify: `frontend/src/i18n/locales/zh-CN.ts`
- Modify: `frontend/src/i18n/locales/en-US.ts`
- Modify: `frontend/src/i18n/locales/ko-KR.ts`
- Modify: `frontend/src/i18n/locales/ru-RU.ts`
- Modify: `frontend/src/i18n/locales/workspaceTerminology.test.ts`

**Interfaces:**
- Consumes: API fields `origin`, `template_key` and `POST /document-types/:id/drafts`.
- Produces: `deriveProductionDocumentType(id, command)` and pure inspection/derivation view models.

- [ ] **Step 1: Write frontend model and source-contract tests**

Assert origin labels, readable summaries, locked lineage fields, prefilled JSON, server-generated version display, admin/owner derivation visibility, and contributor/viewer read-only behavior. Extend the Vue source contract test to require config and derive controls.

- [ ] **Step 2: Verify RED**

```bash
cd frontend && npm test -- --test-name-pattern='document type|workspace terminology'
```

Expected: FAIL because API fields, derivation transformation, and translation keys are missing.

- [ ] **Step 3: Extend API types and pure helpers**

Add:

```ts
export type ProductionDocumentTypeOrigin = 'builtin' | 'custom'
export interface DeriveProductionDocumentTypeInput {
  name: string
  description?: string
  block_schema: ProductionJSON
  source_requirements: ProductionJSON
  skill_bindings: ProductionJSON
  workflow_plan: ProductionJSON
  quality_rules: ProductionJSON
  review_policy: ProductionJSON
  publication_policy: ProductionJSON
}
export function deriveProductionDocumentType(id: string, command: ProductionCommand<DeriveProductionDocumentTypeInput>) {
  return post<ProductionResponse<ProductionDocumentType>>(`/api/v1/production/document-types/${id}/drafts`, command.payload, productionCommandConfig(command))
}
```

Add pure functions for source badge theme/text key, readable config summary, and prefilled derive form payload.

- [ ] **Step 4: Implement table, inspection drawer, and derivation mode**

Add the origin column. Use one read-only drawer with concise section/source/review/publication summaries followed by seven raw JSON panels. Reuse the create drawer for `mode='create' | 'derive'`; in derive mode lock code/template/version, preserve edits after failure, and create/reuse an idempotent command by payload signature.

- [ ] **Step 5: Add all four locale keys and responsive CSS**

Add exact keys for built-in/custom, view configuration, derive draft, generated version, lineage fields, summaries, and failure/retry text. Keep the existing local table scroller; ensure drawers use responsive width and long code/JSON wraps without page overflow.

- [ ] **Step 6: Run frontend tests, type-check, and build**

```bash
cd frontend && npm test
cd frontend && npm run type-check
cd frontend && npm run build
```

Expected: all tests PASS, Vue TypeScript exits 0, and Vite build exits 0.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/api/production/index.ts frontend/src/views/production/models/documentTypeManagement.ts frontend/src/views/production/models/documentTypeManagement.test.ts frontend/src/views/production/ProductionDocumentTypeManagement.vue frontend/src/views/production/productionDocumentTypeManagement.test.ts frontend/src/i18n/locales/zh-CN.ts frontend/src/i18n/locales/en-US.ts frontend/src/i18n/locales/ko-KR.ts frontend/src/i18n/locales/ru-RU.ts frontend/src/i18n/locales/workspaceTerminology.test.ts
git commit -m "feat(production-ui): use and derive built-in document types"
```

---

### Task 8: Run Live End-to-End Acceptance and Update Progress

**Files:**
- Create: `frontend/e2e/production-document-types.spec.ts`
- Modify: `frontend/e2e/fixtures/production-api.ts`
- Modify: `.superpowers/sdd/progress.md`

**Interfaces:**
- Consumes: live backend and frontend with migrations applied.
- Produces: desktop/mobile screenshots and a source-controlled progress record with exact verification evidence.

- [ ] **Step 1: Write the live Playwright scenario**

Register a new identity, assert the new workspace has exactly five active built-ins, open `/platform/knowledge-production/document-types`, inspect SOP configuration, derive a custom SOP draft, activate it, and assert builtin v1 becomes retired while custom v2 becomes active. Repeat layout assertions at `1440x900` and `390x844`; capture `production-document-types-desktop.png` and `production-document-types-mobile.png`.

- [ ] **Step 2: Start the feature branch runtime**

Use the repository's isolated SQLite QA environment. Playwright starts both backend and frontend and applies migrations through normal server startup; it must not touch the developer database.

```bash
cd frontend && \
PLAYWRIGHT_PRODUCTION_QA=1 \
PLAYWRIGHT_QA_BACKEND_MODE=ephemeral-sqlite \
PLAYWRIGHT_QA_DATA_DIR="${TMPDIR%/}/weknora-production-types-qa" \
PLAYWRIGHT_BACKEND_URL=http://127.0.0.1:15178 \
PLAYWRIGHT_PORT=15177 \
npx playwright test e2e/production-document-types.spec.ts --project=chromium
```

Expected: initial failure only if the live fixture or selector is incomplete; fix the fixture/test, not production behavior already covered by lower-level tests.

- [ ] **Step 3: Run complete backend verification**

```bash
go test ./internal/database ./internal/types ./internal/application/repository ./internal/application/service ./internal/handler ./internal/router ./internal/container -count=1
go test -race ./internal/application/repository ./internal/application/service -run 'DocumentType|Tenant|SourceRequirements|PublicationPolicy' -count=1
go build ./...
```

Expected: every command exits 0; race tests report no data race.

Run the existing PostgreSQL migration integration coverage against a real disposable PostgreSQL database:

```bash
WEKNORA_TEST_POSTGRES_DSN="$WEKNORA_TEST_POSTGRES_DSN" \
go test ./internal/database -run 'TestProduction.*PostgreSQL.*Migration|TestProductionBuiltin.*PostgreSQL' -count=1
```

Expected: with `WEKNORA_TEST_POSTGRES_DSN` configured, the built-in migration executes up/down and PostgreSQL/SQLite parity assertions pass. If the DSN is unavailable, record that exact environment limitation in progress and do not claim live PostgreSQL execution passed.

- [ ] **Step 4: Run complete frontend and browser verification**

```bash
cd frontend && npm test
cd frontend && npm run type-check
cd frontend && npm run build
cd frontend && PLAYWRIGHT_PRODUCTION_QA=1 PLAYWRIGHT_QA_BACKEND_MODE=ephemeral-sqlite PLAYWRIGHT_QA_DATA_DIR="${TMPDIR%/}/weknora-production-types-qa" PLAYWRIGHT_BACKEND_URL=http://127.0.0.1:15178 PLAYWRIGHT_PORT=15177 npx playwright test e2e/production-document-types.spec.ts --project=chromium
```

Expected: all commands exit 0; screenshots show no page overflow, clipped controls, overlap, untranslated keys, or blank content.

- [ ] **Step 5: Update progress with evidence**

Record commit IDs, exact commands/counts, runtime URL, migration versions, screenshot paths, and any external model limitation in `.superpowers/sdd/progress.md`. Do not mark live AI writing verified unless an actual configured model completed it.

- [ ] **Step 6: Commit acceptance artifacts and progress**

```bash
git add frontend/e2e/production-document-types.spec.ts frontend/e2e/fixtures/production-api.ts .superpowers/sdd/progress.md
git commit -m "test(production): verify built-in document type workflow"
```

---

## Final Review Gate

Before integration, inspect `git diff huasheng/main...HEAD`, verify the only remaining unstaged change is the user's `.gitignore`, run the complete commands from Task 8 again from a clean process, and perform an independent code review focused on migration reversibility, tenant transaction propagation, derivation races, snapshot immutability, cross-tenant authorization, and mobile overflow. Only then proceed with the existing branch-completion workflow for merge, push, and release.
