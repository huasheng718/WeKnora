# Knowledge Production Domain Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add tenant-isolated production projects, project-role assignments and versioned document-type definitions with repository, service, API, RBAC and audit coverage.

**Architecture:** Introduce the first slice of the `production` bounded context without touching Knowledge ingestion or retrieval. PostgreSQL and SQLite schemas define the same logical constraints; GORM repositories implement storage, services enforce project-role rules, and Gin routes expose `/api/v1/production`.

**Tech Stack:** Go 1.26, Gin, GORM, PostgreSQL migrations, SQLite migrations, testify, dig.

## Global Constraints

- Keep existing Owner/Admin/Contributor/Viewer semantics unchanged.
- Production project roles are additional tenant-scoped assignments, not new TenantRole values.
- PostgreSQL migration version is `000070`; SQLite migration version is `000001`.
- All production IDs are UUID strings and all reads include `tenant_id`.
- Every production write route accepts `Idempotency-Key`; the same actor, route, key and request digest replays the stored response, while a different digest returns HTTP 409.
- Document-type versions are immutable after activation.
- Do not add frontend files in this plan.
- Do not stage or commit `docker-compose.override.yml`.

---

### Task 1: Add Foundation Migrations

**Files:**
- Create: `migrations/versioned/000070_knowledge_production_foundation.up.sql`
- Create: `migrations/versioned/000070_knowledge_production_foundation.down.sql`
- Create: `migrations/sqlite/000001_knowledge_production_foundation.up.sql`
- Create: `migrations/sqlite/000001_knowledge_production_foundation.down.sql`
- Create: `internal/database/production_migration_test.go`

**Interfaces:**
- Consumes: existing `RunMigrationsWithOptions` in `internal/database/migration.go`.
- Produces: `production_projects`, `production_project_members`, `production_document_types`, `production_idempotency_keys` with matching PostgreSQL and SQLite columns.

- [ ] **Step 1: Write the failing migration contract test**

```go
func TestProductionFoundationMigrationsDeclareRequiredTables(t *testing.T) {
    postgres := mustReadMigration(t, "../../migrations/versioned/000070_knowledge_production_foundation.up.sql")
    sqlite := mustReadMigration(t, "../../migrations/sqlite/000001_knowledge_production_foundation.up.sql")
    for _, table := range []string{"production_projects", "production_project_members", "production_document_types", "production_idempotency_keys"} {
        require.Contains(t, postgres, "CREATE TABLE IF NOT EXISTS "+table)
        require.Contains(t, sqlite, "CREATE TABLE IF NOT EXISTS "+table)
    }
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `go test ./internal/database -run TestProductionFoundationMigrationsDeclareRequiredTables -v`
Expected: FAIL because migration files do not exist.

- [ ] **Step 3: Add equivalent PostgreSQL and SQLite schemas**

Use these required keys and constraints in both dialects:

```sql
CREATE TABLE IF NOT EXISTS production_projects (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    name VARCHAR(255) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    owner_user_id VARCHAR(36) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP NULL
);

CREATE TABLE IF NOT EXISTS production_idempotency_keys (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    actor_user_id VARCHAR(36) NOT NULL,
    route VARCHAR(255) NOT NULL,
    idempotency_key VARCHAR(255) NOT NULL,
    request_digest VARCHAR(64) NOT NULL,
    status_code INTEGER NULL,
    response_body JSON NULL,
    completed_at TIMESTAMP NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(tenant_id, actor_user_id, route, idempotency_key)
);
```

Add project-member uniqueness on live `(project_id, user_id, role)`. Add document-type uniqueness on live `(tenant_id, code, schema_version)` and a partial unique index allowing only one active version per `(tenant_id, code)`.
Use `JSONB` for PostgreSQL `response_body` and `TEXT` containing JSON for SQLite. `Reserve` must rely on the unique key rather than an in-memory lock so retries are safe across API replicas.

- [ ] **Step 4: Run migration tests**

Run: `go test ./internal/database -run ProductionFoundation -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add migrations/versioned/000070_knowledge_production_foundation.* migrations/sqlite/000001_knowledge_production_foundation.* internal/database/production_migration_test.go
git commit -m "feat(production): add foundation schema"
```

### Task 2: Define Foundation Types and Interfaces

**Files:**
- Create: `internal/types/production_project.go`
- Create: `internal/types/production_document_type.go`
- Create: `internal/types/production_idempotency.go`
- Create: `internal/types/production_project_test.go`
- Create: `internal/types/interfaces/production_project.go`
- Create: `internal/types/interfaces/production_document_type.go`
- Create: `internal/types/interfaces/production_idempotency.go`

**Interfaces:**
- Consumes: `types.JSON`, context tenant helpers and GORM conventions.
- Produces: `ProductionProjectRepository`, `ProductionProjectService`, `ProductionDocumentTypeRepository`, `ProductionDocumentTypeService`, `ProductionIdempotencyRepository`.

- [ ] **Step 1: Write failing role and state tests**

```go
func TestProductionRoleValidation(t *testing.T) {
    require.True(t, ProductionRoleAuthor.IsValid())
    require.True(t, ProductionRolePublisher.IsValid())
    require.False(t, ProductionRole("owner").IsValid())
}

func TestActivatedDocumentTypeIsImmutable(t *testing.T) {
    dt := &ProductionDocumentType{Status: ProductionDocumentTypeActive}
    require.ErrorIs(t, dt.CanMutateDefinition(), ErrProductionDocumentTypeImmutable)
}
```

- [ ] **Step 2: Run the tests and verify failure**

Run: `go test ./internal/types -run 'ProductionRole|ActivatedDocumentType' -v`
Expected: FAIL with undefined production types.

- [ ] **Step 3: Add exact enums and service contracts**

```go
type ProductionRole string

const (
    ProductionRoleProjectOwner       ProductionRole = "project_owner"
    ProductionRoleAuthor             ProductionRole = "author"
    ProductionRoleBusinessReviewer   ProductionRole = "business_reviewer"
    ProductionRoleEngineeringReviewer ProductionRole = "engineering_reviewer"
    ProductionRoleKnowledgeAdmin     ProductionRole = "knowledge_admin"
    ProductionRoleComplianceReviewer ProductionRole = "compliance_reviewer"
    ProductionRolePublisher          ProductionRole = "publisher"
    ProductionRoleObserver           ProductionRole = "observer"
)

type ProductionProjectService interface {
    CreateProject(ctx context.Context, input CreateProductionProjectInput) (*types.ProductionProject, error)
    GetProject(ctx context.Context, tenantID uint64, projectID string) (*types.ProductionProject, error)
    ListProjects(ctx context.Context, tenantID uint64, userID string) ([]*types.ProductionProject, error)
    AssignRole(ctx context.Context, projectID, userID string, role types.ProductionRole) error
    RemoveRole(ctx context.Context, projectID, userID string, role types.ProductionRole) error
}

type ProductionIdempotencyRepository interface {
    Reserve(ctx context.Context, record *types.ProductionIdempotencyKey) (existing *types.ProductionIdempotencyKey, created bool, err error)
    Complete(ctx context.Context, id string, statusCode int, responseBody types.JSON) error
}
```

Define document-type create, activate, get and list contracts with explicit `tenantID` arguments.

- [ ] **Step 4: Run type tests**

Run: `go test ./internal/types/... -run Production -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/types/production_* internal/types/interfaces/production_*
git commit -m "feat(production): define project and document type contracts"
```

### Task 3: Implement Foundation Repositories

**Files:**
- Create: `internal/application/repository/production_project.go`
- Create: `internal/application/repository/production_project_test.go`
- Create: `internal/application/repository/production_document_type.go`
- Create: `internal/application/repository/production_document_type_test.go`
- Create: `internal/application/repository/production_idempotency.go`
- Create: `internal/application/repository/production_idempotency_test.go`

**Interfaces:**
- Consumes: repository interfaces from Task 2 and `*gorm.DB`.
- Produces: `NewProductionProjectRepository(db *gorm.DB)`, `NewProductionDocumentTypeRepository(db *gorm.DB)` and transactional idempotency reserve/complete operations.

- [ ] **Step 1: Write tenant-isolation and immutable-activation tests**

```go
func TestProductionProjectRepositoryRejectsCrossTenantRead(t *testing.T) {
    repo, db := newProductionRepoTestDB(t)
    seedProductionProject(t, db, "project-1", 7)
    got, err := repo.GetByID(context.Background(), 8, "project-1")
    require.ErrorIs(t, err, gorm.ErrRecordNotFound)
    require.Nil(t, got)
}

func TestDocumentTypeRepositoryActivatesOneVersion(t *testing.T) {
    repo, _ := newProductionRepoTestDB(t)
    require.NoError(t, repo.Activate(context.Background(), 7, "baseline", 2))
    active, err := repo.GetActiveByCode(context.Background(), 7, "baseline")
    require.NoError(t, err)
    require.Equal(t, 2, active.SchemaVersion)
}

func TestProductionIdempotencyReserveReturnsExistingRecord(t *testing.T) {
    repo, _ := newProductionIdempotencyRepoTestDB(t)
    first := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-a")
    _, created, err := repo.Reserve(ctx, first)
    require.NoError(t, err)
    require.True(t, created)
    existing, created, err := repo.Reserve(ctx, first)
    require.NoError(t, err)
    require.False(t, created)
    require.Equal(t, first.ID, existing.ID)
}
```

- [ ] **Step 2: Run repository tests and verify failure**

Run: `go test ./internal/application/repository -run 'ProductionProject|DocumentType' -v`
Expected: FAIL because constructors are undefined.

- [ ] **Step 3: Implement scoped GORM repositories**

Every lookup must start with tenant scope:

```go
func (r *productionProjectRepository) GetByID(ctx context.Context, tenantID uint64, id string) (*types.ProductionProject, error) {
    var project types.ProductionProject
    err := r.db.WithContext(ctx).
        Where("tenant_id = ? AND id = ? AND deleted_at IS NULL", tenantID, id).
        First(&project).Error
    return &project, err
}
```

Implement document-type activation in one transaction: retire the previous active row, then activate the requested draft version.

- [ ] **Step 4: Run repository tests**

Run: `go test ./internal/application/repository -run 'ProductionProject|DocumentType' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/application/repository/production_*
git commit -m "feat(production): add foundation repositories"
```

### Task 4: Implement Services, Project Roles and Audit Actions

**Files:**
- Create: `internal/application/service/production_project.go`
- Create: `internal/application/service/production_project_test.go`
- Create: `internal/application/service/production_document_type.go`
- Create: `internal/application/service/production_document_type_test.go`
- Modify: `internal/types/audit_log.go`

**Interfaces:**
- Consumes: foundation repositories, TenantMemberService and AuditLogService.
- Produces: service implementations with `RequireProjectRole(ctx, projectID, roles...)` authorization helper.

- [ ] **Step 1: Write failing authorization tests**

```go
func TestAssignProjectRoleRequiresTenantAdminOrProjectOwner(t *testing.T) {
    svc := newProductionProjectServiceFixture(t, types.TenantRoleContributor, nil)
    err := svc.AssignRole(ctxForUser(7, "author-user"), "project-1", "reviewer", types.ProductionRoleBusinessReviewer)
    require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProjectOwnerCanAssignRole(t *testing.T) {
    svc := newProductionProjectServiceFixture(t, types.TenantRoleContributor, []types.ProductionRole{types.ProductionRoleProjectOwner})
    require.NoError(t, svc.AssignRole(ctxForUser(7, "owner-user"), "project-1", "reviewer", types.ProductionRoleBusinessReviewer))
}
```

- [ ] **Step 2: Run service tests and verify failure**

Run: `go test ./internal/application/service -run 'ProductionProject|ProductionDocumentType' -v`
Expected: FAIL with missing services.

- [ ] **Step 3: Implement service rules and audit constants**

Add these constants:

```go
AuditActionProductionProjectCreated    AuditAction = "production.project_created"
AuditActionProductionProjectRoleSet    AuditAction = "production.project_role_set"
AuditActionProductionDocumentTypeCreated   AuditAction = "production.document_type_created"
AuditActionProductionDocumentTypeActivated AuditAction = "production.document_type_activated"
```

CreateProject assigns the caller `project_owner` in the same transaction boundary exposed by the repository. Document-type create and activate require TenantRoleAdmin or higher.

- [ ] **Step 4: Run service and audit tests**

Run: `go test ./internal/application/service ./internal/types -run Production -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/application/service/production_* internal/types/audit_log.go
git commit -m "feat(production): enforce project roles and document type lifecycle"
```

### Task 5: Wire Foundation HTTP APIs

**Files:**
- Create: `internal/handler/production_project.go`
- Create: `internal/handler/production_project_test.go`
- Create: `internal/handler/production_document_type.go`
- Create: `internal/handler/production_document_type_test.go`
- Create: `internal/middleware/production_idempotency.go`
- Create: `internal/middleware/production_idempotency_test.go`
- Create: `internal/router/router_production_test.go`
- Modify: `internal/router/router.go`
- Modify: `internal/container/container.go`

**Interfaces:**
- Consumes: foundation services from Task 4, `ProductionIdempotencyRepository` and existing `rbacGuards`.
- Produces: `RegisterProductionRoutes(r, projectHandler, documentTypeHandler, guards)` plus shared idempotency middleware used by every later production write route.

- [ ] **Step 1: Write failing route tests**

```go
func TestProductionFoundationRoutesAreRegistered(t *testing.T) {
    engine := gin.New()
    v1 := engine.Group("/api/v1")
    RegisterProductionRoutes(v1, &handler.ProductionProjectHandler{}, &handler.ProductionDocumentTypeHandler{}, &rbacGuards{})
    assertRoute(t, engine, http.MethodGet, "/api/v1/production/projects")
    assertRoute(t, engine, http.MethodPost, "/api/v1/production/projects")
    assertRoute(t, engine, http.MethodPut, "/api/v1/production/document-types/:id/activate")
}

func TestProductionWriteReplaysSameIdempotencyKey(t *testing.T) {
    engine, repo := newProductionHTTPFixture(t)
    first := postWithIdempotencyKey(t, engine, "/api/v1/production/projects", "request-1", `{"name":"Baseline"}`)
    replay := postWithIdempotencyKey(t, engine, "/api/v1/production/projects", "request-1", `{"name":"Baseline"}`)
    require.Equal(t, http.StatusCreated, first.Code)
    require.Equal(t, first.Body.String(), replay.Body.String())
    require.Equal(t, int64(1), repo.ProjectCount(t))
}

func TestProductionWriteRejectsKeyReusedWithDifferentBody(t *testing.T) {
    engine, _ := newProductionHTTPFixture(t)
    postWithIdempotencyKey(t, engine, "/api/v1/production/projects", "request-1", `{"name":"Baseline"}`)
    conflict := postWithIdempotencyKey(t, engine, "/api/v1/production/projects", "request-1", `{"name":"Retro"}`)
    require.Equal(t, http.StatusConflict, conflict.Code)
}
```

- [ ] **Step 2: Run handler and router tests and verify failure**

Run: `go test ./internal/handler ./internal/router -run Production -v`
Expected: FAIL with undefined handlers and route registrar.

- [ ] **Step 3: Implement handlers and route gates**

Register project reads at `Viewer()`, project creation at `Contributor()`, and document-type writes at `Admin()`:

```go
production := r.Group("/production")
production.GET("/projects", g.Viewer(), projectHandler.List)
production.POST("/projects", g.Contributor(), idempotency.Require(), projectHandler.Create)
production.POST("/projects/:id/members", g.Contributor(), idempotency.Require(), projectHandler.AssignRole)
production.DELETE("/projects/:id/members/:user_id/:role", g.Contributor(), idempotency.Require(), projectHandler.RemoveRole)
production.GET("/document-types", g.Viewer(), documentTypeHandler.List)
production.POST("/document-types", g.Admin(), idempotency.Require(), documentTypeHandler.Create)
production.PUT("/document-types/:id/activate", g.Admin(), idempotency.Require(), documentTypeHandler.Activate)
```

`Require()` hashes the canonical request body, reserves `(tenant, actor, route, key)`, captures the successful JSON response and replays it for an identical retry. Missing keys return HTTP 400, digest conflicts return HTTP 409, and an in-progress duplicate returns HTTP 409 with a retryable error code. Service authorization remains authoritative after the coarse route gate. All write routes added by later plans must include this middleware.

- [ ] **Step 4: Run focused and package tests**

Run: `go test ./internal/types/... ./internal/application/repository ./internal/application/service ./internal/handler ./internal/router -run Production -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handler/production_* internal/middleware/production_idempotency* internal/router/router.go internal/router/router_production_test.go internal/container/container.go
git commit -m "feat(production): expose project foundation APIs"
```

## Plan Verification

Run:

```bash
go test ./internal/database ./internal/types/... ./internal/application/repository ./internal/application/service ./internal/handler ./internal/router
git status --short
```

Expected:

- All listed packages pass.
- Production project reads are tenant-isolated.
- Document-type activation leaves exactly one active schema version.
- Project role assignment follows tenant and project authority.
- The worktree is clean except for the pre-existing untracked `docker-compose.override.yml`.
