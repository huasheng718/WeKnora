# Knowledge Production Publication Projection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish an approved document version into existing knowledge bases through isolated Knowledge projections, atomically switch search visibility per document and target KB, and support retry, cleanup and rollback.

**Architecture:** Every release target owns a new Knowledge ID. All production Knowledge IDs are excluded from ordinary full-KB retrieval; an active projection resolver adds only the projection-head target. Activation is one guarded database head switch, so external vector enable/disable cleanup can be eventually consistent without exposing two versions.

**Tech Stack:** Go 1.26, GORM, Asynq, existing Knowledge manual ingestion, RetrieveParams filters, Neo4j graph namespace, Gin, PostgreSQL, SQLite.

## Global Constraints

- Requires Review Governance plan.
- PostgreSQL migration version is `000074`; SQLite migration version is `000005`.
- Atomicity boundary is `(production_document_id, target_knowledge_base_id)`.
- A release may be partially published across targets.
- The target relation is persisted before Knowledge ingestion starts.
- Full-KB retrieval excludes every inactive production Knowledge ID even if its external index remains enabled.
- Wiki rebuild is post-activation derived work and is not an activation prerequisite.
- Default projection retention is 30 days; active projections are never cleaned.
- Publication re-verifies evidence, block and approved-version content digests before rendering.
- Every production write route uses the Domain Foundation `idempotency.Require()` middleware.

---

### Task 1: Add Release and Projection-Head Schema

**Files:**
- Create: `migrations/versioned/000074_knowledge_production_publication.up.sql`
- Create: `migrations/versioned/000074_knowledge_production_publication.down.sql`
- Create: `migrations/sqlite/000005_knowledge_production_publication.up.sql`
- Create: `migrations/sqlite/000005_knowledge_production_publication.down.sql`
- Modify: `internal/database/production_migration_test.go`

**Interfaces:**
- Consumes: approved document versions and target knowledge bases.
- Produces: releases, release targets and projection heads.

- [ ] **Step 1: Add failing schema assertions**

```go
for _, table := range []string{"production_releases", "production_release_targets", "production_projection_heads"} {
    requireMigrationTable(t, postgres, table)
    requireMigrationTable(t, sqlite, table)
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/database -run Production -v`
Expected: FAIL.

- [ ] **Step 3: Add unique activation constraints**

```sql
CREATE TABLE IF NOT EXISTS production_projection_heads (
    tenant_id BIGINT NOT NULL,
    document_id VARCHAR(36) NOT NULL,
    target_knowledge_base_id VARCHAR(36) NOT NULL,
    active_release_target_id VARCHAR(36) NOT NULL,
    lock_version INTEGER NOT NULL DEFAULT 1,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, document_id, target_knowledge_base_id)
);
```

Add a unique non-null relation from `production_release_targets.knowledge_id` to one target.

- [ ] **Step 4: Run migration tests**

Run: `go test ./internal/database -run Production -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add migrations/versioned/000074_* migrations/sqlite/000005_* internal/database/production_migration_test.go
git commit -m "feat(production): add publication projection schema"
```

### Task 2: Implement Release Repository and State Machine

**Files:**
- Create: `internal/types/production_release.go`
- Create: `internal/types/production_release_test.go`
- Create: `internal/types/interfaces/production_release.go`
- Create: `internal/application/repository/production_release.go`
- Create: `internal/application/repository/production_release_test.go`

**Interfaces:**
- Consumes: approved version IDs and KB IDs.
- Produces: release/target creation, state transition, head compare-and-swap and history listing.

- [ ] **Step 1: Write failing head-switch tests**

```go
func TestSwitchProjectionHeadRejectsStaleLock(t *testing.T) {
    repo := newProductionReleaseRepoFixture(t, activeHead("target-old", 3))
    _, err := repo.SwitchHead(ctx, 7, "doc-1", "kb-1", "target-new", 2)
    require.ErrorIs(t, err, types.ErrProductionProjectionConflict)
}

func TestTargetTransitionsDoNotSkipReady(t *testing.T) {
    require.False(t, CanTransitionReleaseTarget(types.ReleaseTargetBuilding, types.ReleaseTargetActive))
    require.True(t, CanTransitionReleaseTarget(types.ReleaseTargetBuilding, types.ReleaseTargetReady))
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/types ./internal/application/repository -run 'ProductionRelease|ProjectionHead' -v`
Expected: FAIL.

- [ ] **Step 3: Implement repository contract**

```go
type ProductionKnowledgeScope struct {
    ActiveKnowledgeIDs        []string
    InactiveKnowledgeIDs      []string
    AllProductionKnowledgeIDs []string
}

type ProductionReleaseRepository interface {
    CreateRelease(ctx context.Context, release *types.ProductionRelease, targets []*types.ProductionReleaseTarget) error
    GetTarget(ctx context.Context, tenantID uint64, targetID string) (*types.ProductionReleaseTarget, error)
    TransitionTarget(ctx context.Context, targetID string, from, to types.ProductionReleaseTargetStatus, patch types.JSONMap) (bool, error)
    ResolveScopes(ctx context.Context, tenantID uint64, kbIDs []string) (map[string]types.ProductionKnowledgeScope, error)
    SwitchHead(ctx context.Context, tenantID uint64, documentID, kbID, targetID string, expectedLock int) (*types.ProductionProjectionHead, error)
    ListProjectionHistory(ctx context.Context, tenantID uint64, documentID, kbID string) ([]*types.ProductionReleaseTarget, error)
}
```

- [ ] **Step 4: Run repository race tests**

Run: `go test -race ./internal/application/repository -run 'ProductionRelease|ProjectionHead' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/types/production_release* internal/types/interfaces/production_release.go internal/application/repository/production_release*
git commit -m "feat(production): add release projection state machine"
```

### Task 3: Build Isolated Knowledge Projections

**Files:**
- Create: `internal/application/service/production_projection_builder.go`
- Create: `internal/application/service/production_projection_builder_test.go`
- Modify: `internal/types/knowledge.go`
- Modify: `internal/application/service/knowledge_create.go`
- Modify: `internal/application/service/knowledge_create_test.go`
- Modify: `internal/application/service/knowledge_post_process.go`
- Create: `internal/application/service/knowledge_post_process_projection_test.go`

**Interfaces:**
- Consumes: approved version renderer, release repository, KnowledgeService and KB configuration.
- Produces: new manual Knowledge per target with immutable production metadata.

- [ ] **Step 1: Write failing hidden-build tests**

```go
func TestProjectionTargetExistsBeforeKnowledgeIsQueued(t *testing.T) {
    builder := newProjectionBuilderFixture(t)
    _, err := builder.Build(ctx, "target-1")
    require.NoError(t, err)
    require.Less(t, eventIndex("target.persisted"), eventIndex("knowledge.enqueued"))
}

func TestProjectionRejectsTamperedApprovedVersion(t *testing.T) {
    builder := newProjectionBuilderFixture(t, withTamperedApprovedBlock())
    _, err := builder.Build(ctx, "target-1")
    require.ErrorIs(t, err, types.ErrProductionContentDigestMismatch)
    require.Equal(t, 0, eventCount("knowledge.enqueued"))
}

func TestProjectionMetadataRoundTrips(t *testing.T) {
    meta := types.NewManualKnowledgeMetadata("# Baseline", types.ManualKnowledgeStatusPublish, 1)
    meta.ProductionProjection = &types.ProductionProjectionMetadata{DocumentID: "doc-1", VersionID: "v3", ReleaseTargetID: "target-1"}
    got := roundTripManualMetadata(t, meta)
    require.Equal(t, "target-1", got.ProductionProjection.ReleaseTargetID)
}

func TestProductionProjectionBuildDoesNotEnqueueWikiBeforeActivation(t *testing.T) {
    postProcess, enqueuer := newKnowledgePostProcessFixture(t, productionProjectionKnowledge("knowledge-building"))
    require.NoError(t, postProcess.Handle(ctx, completedKnowledgeTask("knowledge-building")))
    require.NotContains(t, enqueuer.TaskTypes(), types.TypeWikiIngest)
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/types ./internal/application/service -run Projection -v`
Expected: FAIL.

- [ ] **Step 3: Extend manual metadata and implement builder**

```go
type ProductionProjectionMetadata struct {
    DocumentID     string `json:"document_id"`
    VersionID      string `json:"version_id"`
    ReleaseTargetID string `json:"release_target_id"`
    ContentDigest  string `json:"content_digest"`
}
```

Builder validates target KB readiness, re-verifies all approved-version digests, renders Markdown, persists target relation, creates manual Knowledge and transitions target to `building`. A digest mismatch fails the target and emits a security audit event before any Knowledge row is created. `knowledge_post_process.go` detects `ProductionProjectionMetadata` and suppresses its normal Wiki enqueue while the release target is not active; vector and graph build work still completes. Existing non-production manual creation behavior must remain unchanged.

- [ ] **Step 4: Run Knowledge and projection tests**

Run: `go test ./internal/types ./internal/application/service -run 'Manual|Projection' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/application/service/production_projection_builder* internal/types/knowledge.go internal/application/service/knowledge_create* internal/application/service/knowledge_post_process*
git commit -m "feat(production): build isolated knowledge projections"
```

### Task 4: Exclude Inactive Projections From Retrieval

**Files:**
- Create: `internal/application/service/production_projection_resolver.go`
- Create: `internal/application/service/production_projection_resolver_test.go`
- Modify: `internal/types/search.go`
- Modify: `internal/types/chat_manage.go`
- Modify: `internal/types/knowledge.go`
- Modify: `internal/types/interfaces/knowledge.go`
- Modify: `internal/application/repository/knowledge.go`
- Create: `internal/application/repository/knowledge_projection_list_test.go`
- Modify: `internal/application/service/knowledge.go`
- Create: `internal/application/service/knowledge_projection_list_test.go`
- Modify: `internal/application/service/session_knowledge_qa.go`
- Modify: `internal/application/service/session_knowledge_qa_test.go`
- Modify: `internal/application/service/knowledgebase_search.go`
- Modify: `internal/application/service/knowledgebase_search_fanout.go`
- Modify: `internal/application/service/knowledgebase_search_fanout_test.go`

**Interfaces:**
- Consumes: `ResolveScopes` from Task 2 and existing `RetrieveParams.ExcludeKnowledgeIDs`.
- Produces: active/inactive scope injection for every full-KB search path.

- [ ] **Step 1: Write failing visibility tests**

```go
func TestResolverExcludesInactiveProductionKnowledge(t *testing.T) {
    resolver := newProjectionResolverFixture(t, scope("kb-1", "knowledge-active", "knowledge-old", "knowledge-building"))
    target := &types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-1"}
    require.NoError(t, resolver.Apply(ctx, 7, []*types.SearchTarget{target}))
    require.ElementsMatch(t, []string{"knowledge-old", "knowledge-building"}, target.ExcludeKnowledgeIDs)
}

func TestExplicitOldProjectionCannotBypassResolver(t *testing.T) {
    resolver := newProjectionResolverFixture(t, inactive("knowledge-old"))
    err := resolver.AuthorizeExplicitKnowledgeIDs(ctx, 7, []string{"knowledge-old"})
    require.ErrorIs(t, err, types.ErrProductionProjectionInactive)
}

func TestOrdinaryKnowledgeListOmitsInactiveAndMarksActiveProjection(t *testing.T) {
    svc := newKnowledgeListFixture(t, scope("kb-1", "knowledge-active", "knowledge-old", "knowledge-building"))
    result, err := svc.ListPagedKnowledgeByKnowledgeBaseID(ctx, "kb-1", &types.Pagination{Page: 1, PageSize: 20}, types.KnowledgeListFilter{})
    require.NoError(t, err)
    rows := result.Data.([]*types.Knowledge)
    require.Equal(t, []string{"knowledge-active"}, knowledgeIDs(rows))
    active := rows[0]
    require.Equal(t, "production", active.Source)
    require.True(t, active.ReadOnly)
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/application/service -run ProjectionResolver -v`
Expected: FAIL.

- [ ] **Step 3: Add exclusion fields and propagate them**

```go
// Add to SearchTarget.
ExcludeKnowledgeIDs []string `json:"exclude_knowledge_ids,omitempty"`

// Add to KnowledgeListFilter.
ExcludeKnowledgeIDs []string

// Add to Knowledge as a response-only field.
ReadOnly bool `json:"read_only" gorm:"-"`
```

Every conversion from SearchTarget/SearchParams to RetrieveParams must copy `ExcludeKnowledgeIDs`. Full KB targets receive all inactive production IDs. Explicit Knowledge ID requests reject inactive production IDs instead of silently exposing history. Before the ordinary paged KB document query, resolve the KB scope and pass `InactiveKnowledgeIDs` into `KnowledgeListFilter`; apply the exclusion before both `COUNT` and `SELECT`. Mark rows in `ActiveKnowledgeIDs` as response-only `source=production` and `read_only=true` without changing their stored manual source.

- [ ] **Step 4: Run search tests and driver filter suites**

Run: `go test ./internal/application/service ./internal/application/repository/retriever/... -run 'Projection|ExcludeKnowledgeIDs' -v`
Expected: PASS for PostgreSQL, SQLite, Elasticsearch, OpenSearch, Qdrant, Milvus, Weaviate, Doris and Tencent VectorDB filter tests.

- [ ] **Step 5: Commit**

```bash
git add internal/application/service/production_projection_resolver* internal/types/search.go internal/types/chat_manage.go internal/types/knowledge.go internal/types/interfaces/knowledge.go internal/application/repository/knowledge* internal/application/service/knowledge.go internal/application/service/knowledge_projection_list_test.go internal/application/service/session_knowledge_qa* internal/application/service/knowledgebase_search*
git commit -m "feat(production): hide inactive projections from retrieval"
```

### Task 5: Scope Knowledge Graph Queries to Active Projections

**Files:**
- Create: `internal/application/service/production_graph_scope.go`
- Create: `internal/application/service/production_graph_scope_test.go`
- Modify: `internal/agent/tools/query_knowledge_graph.go`
- Modify: `internal/agent/tools/query_knowledge_graph_test.go`
- Modify: `internal/application/service/chat_pipeline/search_parallel.go`

**Interfaces:**
- Consumes: active projection resolver and `RetrieveGraphRepository.SearchNode`.
- Produces: graph namespace list containing ordinary Knowledge plus active production Knowledge only.

- [ ] **Step 1: Write failing graph-scope test**

```go
func TestGraphScopeOmitsInactiveProjectionNamespace(t *testing.T) {
    scope := BuildActiveGraphNamespaces("kb-1", []string{"ordinary"}, []string{"prod-active"}, []string{"prod-old"})
    require.Contains(t, scope, types.NameSpace{KnowledgeBase: "kb-1", Knowledge: "prod-active"})
    require.NotContains(t, scope, types.NameSpace{KnowledgeBase: "kb-1", Knowledge: "prod-old"})
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/application/service ./internal/agent/tools -run GraphScope -v`
Expected: FAIL.

- [ ] **Step 3: Query and merge only allowed namespaces**

Add a service helper that searches each allowed Knowledge namespace and deterministically deduplicates nodes and relations. Do not use an empty Knowledge namespace for a KB containing production projections.

- [ ] **Step 4: Run graph tests**

Run: `go test ./internal/application/service ./internal/agent/tools -run 'Graph|Projection' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/application/service/production_graph_scope* internal/agent/tools/query_knowledge_graph* internal/application/service/chat_pipeline/search_parallel.go
git commit -m "feat(production): scope graph queries to active projections"
```

### Task 6: Activate, Clean Up and Roll Back Targets

**Files:**
- Create: `internal/application/service/production_release.go`
- Create: `internal/application/service/production_release_test.go`
- Create: `internal/application/service/production_projection_cleanup.go`
- Create: `internal/application/service/production_projection_cleanup_test.go`
- Modify: `internal/types/task.go`
- Modify: `internal/router/task.go`
- Modify: `internal/router/sync_task.go`
- Modify: `internal/types/audit_log.go`

**Interfaces:**
- Consumes: release repository, projection builder, Knowledge/Chunk services and retrieve engine status updater.
- Produces: prepare, activate, retry, rollback and retention cleanup operations.

- [ ] **Step 1: Write failing atomic-visibility and rollback tests**

```go
func TestBuildFailureLeavesOldHeadActive(t *testing.T) {
    svc := newProductionReleaseFixture(t, activeHead("target-old"), failedBuild("target-new"))
    require.Error(t, svc.Activate(ctx, "target-new", 1))
    require.Equal(t, "target-old", loadHead(t).ActiveReleaseTargetID)
}

func TestPrepareReleaseSnapshotsTargetProcessingConfig(t *testing.T) {
    svc := newProductionReleaseFixture(t, targetKBWithWriteAccess())
    release, err := svc.Prepare(ctx, "document-1", "version-3", []string{"kb-1"})
    require.NoError(t, err)
    snapshot := release.Targets[0].ConfigSnapshot
    require.NotEmpty(t, snapshot.Chunking.Method)
    require.NotEmpty(t, snapshot.EmbeddingModelID)
    require.Equal(t, true, snapshot.Graph.Enabled)
    require.NotEmpty(t, snapshot.Graph.ModelID)
}

func TestRollbackIsAnotherHeadSwitch(t *testing.T) {
    svc := newProductionReleaseFixture(t, activeHead("target-new"), retainedTarget("target-old"))
    require.NoError(t, svc.Rollback(ctx, "target-old", loadHead(t).LockVersion))
    require.Equal(t, "target-old", loadHead(t).ActiveReleaseTargetID)
}

func TestActivationEnqueuesWikiAfterHeadSwitch(t *testing.T) {
    svc, events := newProductionReleaseFixtureWithEvents(t, activeHead("target-old"), readyTarget("target-new"))
    require.NoError(t, svc.Activate(ctx, "target-new", loadHead(t).LockVersion))
    require.Less(t, events.Index("projection.head_switched"), events.Index("wiki.ingest:knowledge-new"))
    require.Less(t, events.Index("projection.head_switched"), events.Index("wiki.retract:knowledge-old"))
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/application/service -run 'ProductionRelease|Rollback' -v`
Expected: FAIL.

- [ ] **Step 3: Implement guarded activation and eventual cleanup**

`Prepare` validates publisher write access plus target storage, chunking, embedding/vector, graph and model readiness, then snapshots those settings per target without starting a build. Add `TypeProductionBuild`, `TypeProductionActivate`, `TypeProductionCleanup` to QueueProduction. Activation requires target `ready`, completed Knowledge parse and successful graph subtasks when graph is enabled. After the guarded head switch succeeds, call the existing `EnqueueWikiIngest` for the new Knowledge ID and `EnqueueWikiRetract` for the previous active Knowledge ID, then enqueue projection cleanup. Wiki is derived post-activation work: enqueue failures are retryable activation follow-up failures and never roll back the visible head. Cleanup disables old chunks/indexes but never changes the active head.

- [ ] **Step 4: Run release tests with race detector**

Run: `go test -race ./internal/application/service ./internal/types -run 'ProductionRelease|Projection' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/application/service/production_release* internal/application/service/production_projection_cleanup* internal/types/task.go internal/router/task.go internal/router/sync_task.go internal/types/audit_log.go
git commit -m "feat(production): atomically activate and roll back projections"
```

### Task 7: Expose Publication APIs and Protect Knowledge Mutations

**Files:**
- Create: `internal/handler/production_release.go`
- Create: `internal/handler/production_release_test.go`
- Modify: `internal/handler/knowledge.go`
- Modify: `internal/handler/knowledge_move_gate_test.go`
- Modify: `internal/router/router.go`
- Modify: `internal/router/router_production_test.go`
- Modify: `internal/container/container.go`

**Interfaces:**
- Consumes: production release service.
- Produces: create release, target status, activate, retry and rollback endpoints; active-projection mutation guard.

- [ ] **Step 1: Write failing API and mutation-guard tests**

```go
assertRoute(t, engine, http.MethodPost, "/api/v1/production/documents/:id/releases")
assertRoute(t, engine, http.MethodPost, "/api/v1/production/release-targets/:id/activate")
assertRoute(t, engine, http.MethodPost, "/api/v1/production/release-targets/:id/rollback")

func TestDeleteKnowledgeRejectsActiveProductionProjection(t *testing.T) {
    rec := deleteKnowledgeFixture(t, activeProductionKnowledge("knowledge-1"))
    require.Equal(t, http.StatusConflict, rec.Code)
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/handler ./internal/router -run 'ProductionRelease|ActiveProductionProjection' -v`
Expected: FAIL.

- [ ] **Step 3: Register publisher-gated APIs**

Route through `Contributor()` then require project role `publisher` in the service. Active production projections reject direct update, move, reparse and delete; callers must create a release or rollback.

```go
production.POST("/documents/:id/releases", g.Contributor(), idempotency.Require(), releaseHandler.Create)
production.POST("/release-targets/:id/activate", g.Contributor(), idempotency.Require(), releaseHandler.Activate)
production.POST("/release-targets/:id/retry", g.Contributor(), idempotency.Require(), releaseHandler.Retry)
production.POST("/release-targets/:id/rollback", g.Contributor(), idempotency.Require(), releaseHandler.Rollback)
```

- [ ] **Step 4: Run publication and regression tests**

Run: `go test ./internal/types/... ./internal/application/repository/... ./internal/application/service/... ./internal/handler/... ./internal/router/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handler/production_release* internal/handler/knowledge* internal/router/router.go internal/router/router_production_test.go internal/container/container.go
git commit -m "feat(production): expose governed publication APIs"
```

## Plan Verification

Run:

```bash
go test ./internal/application/repository/retriever/... ./internal/application/service/... ./internal/agent/tools ./internal/handler ./internal/router
go test -race ./internal/application/repository ./internal/application/service -run 'Production|Projection'
```

Expected:

- Old projection remains visible while new target is building.
- Failed builds never change the head.
- One head switch changes retrieval and graph visibility.
- Projection build never triggers Wiki; activation enqueues new ingest and old retract only after the head switch.
- Ordinary KB lists omit inactive projections and expose the active projection as `source=production`, read-only.
- Direct Knowledge mutation cannot bypass governance.
- Rollback restores a retained target without rewriting release history.
