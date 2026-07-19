# Documents and Evidence Final Fixes Report

Date: 2026-07-19
Base: `41ff3bc`

## Outcome

The final-review findings were implemented without changing the bootstrap-v1 contract. Empty v1 remains a non-content initialization; first content v2 and every later append are governed before persistence.

## RED Evidence

1. Schema guards:
   - Command: `GOCACHE=/private/tmp/weknora-go-build-cache go test ./internal/database -run 'ProductionDocuments.*(FinalIntegrity|EvidenceContent|FrozenSets|Lineage)' -count=1 -v`
   - Exit: 1.
   - Expected failures: PostgreSQL contract lacked XOR/composite lineage guards; SQLite accepted both evidence content forms, a frozen-set item insert, and a lineage edge with a nonexistent logical endpoint.
2. Service/UoW/resource contracts:
   - Command: `GOCACHE=/private/tmp/weknora-go-build-cache go test ./internal/application/service ./internal/handler -run 'Production(DocumentHTTPRejectsUngoverned|DocumentServiceRejectsUngoverned|DocumentDirectServiceAudit|SourceDirectServiceAudit|SourceServiceRejectsSameTenant)|ResourceCatalogResolveBound' -count=1`
   - Exit: 1.
   - Expected compile failures: missing `ProductionUnitOfWork`, `ResolveBound`, `ResourceBindingRequirement`, production-project owner constant, and typed document-validation error.

## GREEN Evidence

1. Full Documents Production gate:
   - `GOCACHE=/private/tmp/weknora-go-build-cache go test ./internal/database ./internal/types/... ./internal/application/repository ./internal/application/service ./internal/middleware ./internal/handler ./internal/router -run Production -count=1`
   - All eight package groups passed.
2. ResourceCatalog affected gate:
   - `GOCACHE=/private/tmp/weknora-go-build-cache go test ./internal/application/service ./internal/application/service/file ./internal/router -run ResourceCatalog -count=1`
   - All passed.
3. Focused race gate:
   - `GOCACHE=/private/tmp/weknora-go-build-cache go test -race ./internal/application/repository ./internal/application/service ./internal/handler -run 'Production(Document|Source)|ResourceCatalog' -count=1`
   - All passed.
4. Container compile: `go test ./internal/container -run '^$' -count=1` passed.
5. Vet: targeted database/types/repository/service/middleware/handler/router/container packages passed.
6. `git diff --check` passed; `docker-compose.override.yml` has no diff.

## Architecture Decisions

- Added semantic `ProductionUnitOfWork`; it owns direct-service transactions and reuses a transaction already bound by HTTP idempotency. Idempotency reservations remain a separate concern.
- Freeze, bootstrap Create, and Append now run mutation plus required audit in the same service UoW. The audit repository's savepoint behavior remains unchanged for legacy best-effort callers; these governed callers propagate audit errors and roll back.
- Append loads accepted evidence only through exact frozen `(tenant, project, source_set)` membership, hydrates the immutable document type code, resolves registry evidence through the trusted catalog, then calls both production validation and digest verification before the UoW.
- `ResourceBindingRequirement` uses authoritative tenant, constant owner type `production_project`, and source-set-derived project ID. Relation is intentionally not authorization input, preserving compatibility with resources bound through existing `Bind` relations.
- Lineage now has composite block-endpoint FKs, a relation CHECK, and append-only mutation/replace guards in both dialects.
- Frozen source sets reject every item insert, every update/delete of existing items, moves into or out of the set, and evidence inserts. Evidence content now enforces exact inline/storage XOR.

## Coverage and Files

Behavioral coverage includes unknown evidence, factual content without evidence or confirmation, unsupported blocks, missing required sections, corrupt evidence digest, valid governed append, same-tenant other-project resources, and direct SQLite audit rollback/success for freeze/bootstrap/append. Raw SQLite tests cover all schema bypasses; PostgreSQL is parser/contract-verified.

Primary implementation files are the production document/source services and repositories, resource catalog/repository interfaces, the new UoW files, both Documents migrations and down migrations, container wiring, handler error mapping, and their focused tests.

## Tradeoffs and Residual Notes

- Append does not accept caller-supplied block or version digests; it computes them canonically before verification. Therefore end-to-end HTTP tests can inject an evidence mismatch but not a block/version mismatch. Existing `VerifyProductionVersionDigests` tests explicitly cover tampered block and version digests.
- PostgreSQL validation is parser and migration-contract based; no live PostgreSQL instance was used in this wave.
