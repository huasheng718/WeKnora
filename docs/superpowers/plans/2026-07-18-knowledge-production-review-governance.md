# Knowledge Production Review Governance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add block annotations, blocking quality gates, immutable review-policy snapshots and role-gated approval of one frozen document version.

**Architecture:** Annotations belong to a concrete version and block record. Review submission locks the document head, validates blocking annotations, freezes the version and materializes review steps from the document-type policy snapshot. Later edits create a new version and obsolete any unfinished review.

**Tech Stack:** Go 1.26, GORM, PostgreSQL, SQLite, Gin, existing tenant RBAC and audit service.

## Global Constraints

- Requires AI Orchestration plan.
- PostgreSQL migration version is `000073`; SQLite migration version is `000004`.
- Review decisions never mutate document content.
- A review request references exactly one immutable version.
- Open `blocking` annotations prevent submission.
- Tenant Admin/Owner may cancel or reject but may not impersonate a required professional role.
- All required steps must approve before the document becomes approved.
- Review submission re-verifies evidence, block and version content digests.
- Every production write route uses the Domain Foundation `idempotency.Require()` middleware.

---

### Task 1: Add Annotation and Review Schema

**Files:**
- Create: `migrations/versioned/000073_knowledge_production_reviews.up.sql`
- Create: `migrations/versioned/000073_knowledge_production_reviews.down.sql`
- Create: `migrations/sqlite/000004_knowledge_production_reviews.up.sql`
- Create: `migrations/sqlite/000004_knowledge_production_reviews.down.sql`
- Modify: `internal/database/production_migration_test.go`

**Interfaces:**
- Consumes: production documents, versions, blocks and project members.
- Produces: annotations, review requests and review steps.

- [ ] **Step 1: Add failing schema assertions**

```go
for _, table := range []string{"production_annotations", "production_review_requests", "production_review_steps"} {
    requireMigrationTable(t, postgres, table)
    requireMigrationTable(t, sqlite, table)
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/database -run Production -v`
Expected: FAIL.

- [ ] **Step 3: Add review tables and indexes**

Add indexes on `(document_id, version_id, status)`, `(review_request_id, sequence)` and `(review_request_id, required_role, decision)`. Store `policy_snapshot` as JSONB/PostgreSQL and JSON text/SQLite.

- [ ] **Step 4: Run migration tests**

Run: `go test ./internal/database -run Production -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add migrations/versioned/000073_* migrations/sqlite/000004_* internal/database/production_migration_test.go
git commit -m "feat(production): add annotation and review schema"
```

### Task 2: Define Review Types and Repositories

**Files:**
- Create: `internal/types/production_review.go`
- Create: `internal/types/production_review_test.go`
- Create: `internal/types/interfaces/production_review.go`
- Create: `internal/application/repository/production_review.go`
- Create: `internal/application/repository/production_review_test.go`

**Interfaces:**
- Consumes: document and project IDs.
- Produces: append annotation, resolve annotation, submit review transaction, decide step and obsolete pending review.

- [ ] **Step 1: Write failing transition tests**

```go
func TestReviewDecisionTransitions(t *testing.T) {
    require.True(t, CanTransitionReviewStep(ProductionReviewPending, ProductionReviewApproved))
    require.False(t, CanTransitionReviewStep(ProductionReviewApproved, ProductionReviewPending))
}

func TestReviewRepositoryDecisionIsCompareAndSwap(t *testing.T) {
    repo := newProductionReviewRepoFixture(t)
    ok, err := repo.DecideStep(ctx, 7, "step-1", types.ProductionReviewPending, types.ProductionReviewApproved, "reviewer", "ok")
    require.NoError(t, err)
    require.True(t, ok)
    ok, err = repo.DecideStep(ctx, 7, "step-1", types.ProductionReviewPending, types.ProductionReviewRejected, "reviewer", "again")
    require.NoError(t, err)
    require.False(t, ok)
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/types ./internal/application/repository -run ProductionReview -v`
Expected: FAIL.

- [ ] **Step 3: Implement exact repository contract**

```go
type ProductionReviewRepository interface {
    CreateAnnotation(ctx context.Context, annotation *types.ProductionAnnotation) error
    ResolveAnnotation(ctx context.Context, tenantID uint64, annotationID, actorID string, resolution types.ProductionAnnotationStatus) (bool, error)
    CountOpenBlocking(ctx context.Context, tenantID uint64, versionID string) (int64, error)
    CreateReview(ctx context.Context, request *types.ProductionReviewRequest, steps []*types.ProductionReviewStep) error
    GetReview(ctx context.Context, tenantID uint64, reviewID string) (*types.ProductionReviewRequest, error)
    DecideStep(ctx context.Context, tenantID uint64, stepID string, from, to types.ProductionReviewDecision, actorID, comment string) (bool, error)
    ObsoletePendingByDocument(ctx context.Context, tenantID uint64, documentID, exceptVersionID string) error
}
```

- [ ] **Step 4: Run type and repository tests**

Run: `go test ./internal/types ./internal/application/repository -run ProductionReview -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/types/production_review* internal/types/interfaces/production_review.go internal/application/repository/production_review*
git commit -m "feat(production): add review state repository"
```

### Task 3: Enforce Annotation Quality Gates

**Files:**
- Create: `internal/application/service/production_annotation.go`
- Create: `internal/application/service/production_annotation_test.go`

**Interfaces:**
- Consumes: review repository, document repository and project-role helper.
- Produces: create, resolve and dismiss annotation operations.

- [ ] **Step 1: Write failing block-anchor tests**

```go
func TestAnnotationRejectsBlockFromAnotherVersion(t *testing.T) {
    svc := newProductionAnnotationFixture(t)
    _, err := svc.Create(ctx, CreateProductionAnnotationInput{
        DocumentID: "doc-1", VersionID: "version-2", BlockID: "version-1-block",
    })
    require.ErrorIs(t, err, types.ErrProductionAnnotationAnchorInvalid)
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/application/service -run ProductionAnnotation -v`
Expected: FAIL.

- [ ] **Step 3: Implement validation and role rules**

Authors and reviewers may create annotations. Only the annotation creator, an author or project owner may resolve; compliance blocking tags additionally require a compliance reviewer or project owner.

```go
func (s *productionAnnotationService) Create(ctx context.Context, input CreateProductionAnnotationInput) (*types.ProductionAnnotation, error)
func (s *productionAnnotationService) Resolve(ctx context.Context, annotationID string, status types.ProductionAnnotationStatus) error
```

- [ ] **Step 4: Run annotation tests**

Run: `go test ./internal/application/service -run ProductionAnnotation -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/application/service/production_annotation*
git commit -m "feat(production): enforce block annotation gates"
```

### Task 4: Implement Review Submission and Role-Gated Decisions

**Files:**
- Create: `internal/application/service/production_review.go`
- Create: `internal/application/service/production_review_test.go`
- Modify: `internal/application/service/production_document.go`
- Modify: `internal/application/service/production_document_test.go`
- Modify: `internal/types/audit_log.go`

**Interfaces:**
- Consumes: active document-type review policy, current version, annotations and project members.
- Produces: submit, approve, changes-requested, reject and cancel operations.

- [ ] **Step 1: Write failing governance tests**

```go
func TestBlockingAnnotationPreventsReviewSubmission(t *testing.T) {
    svc := newProductionReviewFixture(t, withOpenBlockingAnnotation())
    _, err := svc.Submit(ctx, "document-1", "version-3")
    require.ErrorIs(t, err, types.ErrProductionBlockingAnnotations)
}

func TestDigestMismatchPreventsReviewSubmission(t *testing.T) {
    svc := newProductionReviewFixture(t, withTamperedBlockContent())
    _, err := svc.Submit(ctx, "document-1", "version-3")
    require.ErrorIs(t, err, types.ErrProductionContentDigestMismatch)
}

func TestWrongProjectRoleCannotApproveStep(t *testing.T) {
    svc := newProductionReviewFixture(t, requiredRole(types.ProductionRoleEngineeringReviewer))
    err := svc.Decide(ctxForProjectRole(types.ProductionRoleBusinessReviewer), "step-1", types.ProductionReviewApproved, "ok")
    require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestNewVersionObsoletesPendingReview(t *testing.T) {
    svc := newProductionDocumentFixtureWithPendingReview(t)
    _, err := svc.AppendVersion(ctx, "document-1", validVersionInput())
    require.NoError(t, err)
    require.Equal(t, types.ProductionReviewObsolete, loadReview(t, "review-1").Status)
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/application/service -run 'ProductionReview|ObsoletesPendingReview' -v`
Expected: FAIL.

- [ ] **Step 3: Implement frozen policy snapshot and aggregate completion**

```go
type ProductionReviewService interface {
    Submit(ctx context.Context, documentID, versionID string) (*types.ProductionReviewRequest, error)
    Decide(ctx context.Context, stepID string, decision types.ProductionReviewDecision, comment string) error
    Cancel(ctx context.Context, reviewID, reason string) error
}
```

Before freezing the version, load all referenced evidence and call `VerifyProductionVersionDigests`; a mismatch blocks submission and emits a security audit event. When the final required step approves, atomically set review `approved`, document `approved` and `latest_approved_version_id`. Add review-submitted and review-decided audit actions.

- [ ] **Step 4: Run service tests with race detector**

Run: `go test -race ./internal/application/service -run ProductionReview -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/application/service/production_review* internal/application/service/production_document* internal/types/audit_log.go
git commit -m "feat(production): add version-bound role cosign"
```

### Task 5: Expose Annotation and Review APIs

**Files:**
- Create: `internal/handler/production_review.go`
- Create: `internal/handler/production_review_test.go`
- Modify: `internal/router/router.go`
- Modify: `internal/router/router_production_test.go`
- Modify: `internal/container/container.go`

**Interfaces:**
- Consumes: annotation and review services.
- Produces: annotation CRUD, review submit/get and step-decision endpoints.

- [ ] **Step 1: Write failing route tests**

```go
assertRoute(t, engine, http.MethodPost, "/api/v1/production/documents/:id/annotations")
assertRoute(t, engine, http.MethodPost, "/api/v1/production/documents/:id/reviews")
assertRoute(t, engine, http.MethodPost, "/api/v1/production/reviews/:id/steps/:step_id/decision")
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/handler ./internal/router -run ProductionReview -v`
Expected: FAIL.

- [ ] **Step 3: Implement handlers and conflict responses**

Return HTTP 409 for stale version, duplicate decision or an obsolete review. Return HTTP 422 for blocking annotations or invalid anchors.

```go
production.POST("/documents/:id/annotations", g.Contributor(), idempotency.Require(), reviewHandler.CreateAnnotation)
production.PUT("/annotations/:id/status", g.Contributor(), idempotency.Require(), reviewHandler.UpdateAnnotationStatus)
production.POST("/documents/:id/reviews", g.Contributor(), idempotency.Require(), reviewHandler.Submit)
production.POST("/reviews/:id/steps/:step_id/decision", g.Contributor(), idempotency.Require(), reviewHandler.Decide)
```

- [ ] **Step 4: Run governance packages**

Run: `go test ./internal/types ./internal/application/repository ./internal/application/service ./internal/handler ./internal/router -run Production -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handler/production_review* internal/router/router.go internal/router/router_production_test.go internal/container/container.go
git commit -m "feat(production): expose annotation and review APIs"
```

## Plan Verification

Run: `go test -race ./internal/types ./internal/application/repository ./internal/application/service ./internal/handler ./internal/router -run Production`
Expected: PASS with no review double-decision or version-mixing race.
