# Enterprise Knowledge Production Center Delivery Roadmap

> **For agentic workers:** Execute the linked plans in order. Each plan is an independent review and test gate. Do not start a later plan while an earlier plan has failing tests or unresolved review findings.

**Goal:** Deliver the enterprise knowledge production center described in `docs/superpowers/specs/2026-07-18-enterprise-knowledge-production-center-design.md` through six independently testable implementation plans.

**Architecture:** A new `production` bounded context owns projects, evidence, immutable document versions, review and release state. Existing Knowledge, Chunk, retrieval, graph, Skill, MCP, RBAC, audit and Asynq modules are reused through explicit adapters rather than absorbing production workflow state.

**Tech Stack:** Go 1.26, Gin, GORM, PostgreSQL, SQLite, Asynq, Redis, Vue 3, Pinia, Vue Router, TDesign, TypeScript, Node test runner.

## Plan Order

1. [Domain Foundation](./2026-07-18-knowledge-production-domain-foundation.md)
   - Projects, project roles, document-type versions, migrations, repository, service, HTTP API, audit registration.
   - Exit gate: tenant-isolated project and document-type APIs pass Go tests on repository and route layers.

2. [Documents and Evidence](./2026-07-18-knowledge-production-documents-evidence.md)
   - Source sets, source items, evidence snapshots, immutable document versions, stable block identity and split/merge lineage, SHA-256 digests, Markdown renderer, two built-in templates.
   - Exit gate: a frozen source set can produce and retrieve an immutable version whose evidence references validate.

3. [AI Orchestration](./2026-07-18-knowledge-production-ai-orchestration.md)
   - Persistent runs, durable tool calls, Skill/MCP/data-source adapters, model-backed structured writer, dedicated queue, resume after approval.
   - Exit gate: a writing run can pause for MCP approval, resume on another worker, and create a validated version.

4. [Review Governance](./2026-07-18-knowledge-production-review-governance.md)
   - Annotations, blocking quality tags, policy snapshots, frozen review versions, role-gated decisions and obsolete reviews.
   - Exit gate: blocking annotations prevent submission and all required role steps must approve the same immutable version.

5. [Publication Projection](./2026-07-18-knowledge-production-publication-projection.md)
   - Releases, per-KB targets, Knowledge projection adapter, active projection resolver, list filtering, atomic head switch, graph scope, post-activation Wiki, cleanup and rollback.
   - Exit gate: the old projection remains searchable during build; one database head switch makes only the new Knowledge ID visible.

6. [Frontend Workbench](./2026-07-18-knowledge-production-frontend-workbench.md)
   - First-level navigation, project workbench, structured editor, evidence/annotation panels, review and publication screens.
   - Exit gate: the complete approved workflow passes frontend unit tests, type checking, production build and Playwright desktop/mobile QA.

## Cross-Plan Contracts

The following names are stable across every plan:

```go
type ProductionRole string
type ProductionProject struct{ /* tenant-scoped aggregate root */ }
type ProductionDocumentType struct{ /* immutable schema version row */ }
type ProductionDocument struct{ /* mutable head metadata */ }
type ProductionDocumentVersion struct{ /* immutable version */ }
type ProductionRun struct{ /* persistent orchestrator state */ }
type ProductionReviewRequest struct{ /* one frozen version */ }
type ProductionRelease struct{ /* one approved version */ }
type ProductionReleaseTarget struct{ /* one target KB */ }
type ProductionProjectionHead struct{ /* active target per document and KB */ }
```

HTTP resources remain under `/api/v1/production`. Database table names remain prefixed with `production_`. Queue task names remain prefixed with `production:`.

## Global Release Gates

- No production publish UI is visible before Plan 5 active-projection tests pass.
- No production worker waits in memory for human approval.
- No approved or published document version is updated in place.
- Every write path enforces tenant isolation and emits the required audit event.
- Every production write route is protected by the shared `Idempotency-Key` middleware.
- Review and publication re-verify evidence, block and version content digests.
- Inactive production projections are absent from retrieval, graph queries and ordinary KB document lists.
- Wiki ingest/retract begins only after the active projection head switches.
- PostgreSQL migrations and SQLite/Lite migrations land together.
- Existing `docker-compose.override.yml` remains outside feature commits.
- Each plan uses TDD and ends with its own commit before the next plan starts.

## Final Verification

After all six plans complete, run:

```bash
go test ./internal/types/... ./internal/application/repository/... ./internal/application/service/... ./internal/handler/... ./internal/router/...
go test -race ./internal/application/service/... ./internal/application/repository/...
cd frontend && npm test
cd frontend && npm run type-check
cd frontend && npm run build
docker compose up -d
curl -fsS http://localhost:8080/health
curl -I http://localhost
```

Expected results:

- All Go and frontend tests pass.
- Type checking and production build complete without errors.
- API health returns `{"status":"ok"}`.
- Web root returns HTTP 200.
- Existing knowledge upload, chat, Agent, Wiki and integrations smoke tests remain green.
