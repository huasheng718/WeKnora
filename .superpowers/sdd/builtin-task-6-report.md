# Builtin Task 6 Report: Enforce Snapshotted Governance End to End

## Status

Complete. Task 6 governance is enforced across source freeze, writer/validator, review submission, release, and retired exact-bound run completion. No frontend work was added.

## Inherited Work

The takeover started from an intentional uncommitted diff spanning the production source, writer/validator, review, release, project authorization, run snapshot, and their tests. The inherited implementation already contained most Task 6 behavior and focused test coverage. The pre-existing `.gitignore` change was preserved and excluded from Task 6 staging.

## Completed Boundaries

1. Source freeze loads the bound active document type, enforces minimum accepted evidence, accepted source kinds, evidence-section policy, and unsupported-fact policy, and returns stable reason codes. Known empty legacy source configs use an explicit built-in adapter.
2. Run snapshots include all seven governance configs. Writer prompts and validator checks consume the exact run snapshot, not a global type-code lookup. Legacy baseline/retrospective configs are adapted only when the governance fields are empty.
3. Writer `fact` output is persisted as a governed factual `paragraph`; validation deterministically enforces required sections, fact evidence, blocked confirmation, allowed block types, and type-specific gates.
4. Review submission requires a live tenant member assigned to every materialized reviewer role. Exact bound active or retired type policy is used, with no current-active fallback.
5. Release preflight/prepare loads the exact document-bound type ID/schema version, accepts only active or retired immutable rows, and requires knowledge-base target, approved review, and inherited chunking/graph processing.
6. A run created before type retirement can finish its exact internal AI append. Human/create paths remain active-only, and the repository transaction independently requires a matching internal principal, AI origin, and system actor before allowing retired status.

## Takeover Fixes

- Updated the remaining handler source-service constructor fixture.
- Fixed legacy source freeze without weakening isolated source-policy validation.
- Added a RED/GREEN regression for retired exact-bound internal append and retained active-only human behavior.
- Preserved the PostgreSQL active-only lock query shape while adding the narrowly scoped active-or-retired internal query.

## Verification Evidence

- `go test ./internal/application/service -run 'SourceRequirements' -count=1`: PASS, exit 0.
- `go test ./internal/application/service -run 'ProductionWriter.*(SnapshotGovernance|LegacyAdapter|InvalidGovernance)' -count=1`: PASS, exit 0.
- `go test ./internal/application/service -run 'ValidateProductionVersion.*(SnapshotGovernance|QualityRules|TypeSpecific)' -count=1`: PASS, exit 0.
- `go test ./internal/application/service -run 'ReviewerAvailability|UsesExactTypeRetiredAfterInitialRead' -count=1`: PASS, exit 0.
- `go test ./internal/application/service -run 'PublicationPolicy|RetiredExactBoundPublicationPolicy' -count=1`: PASS, exit 0.
- `go test ./internal/handler -run 'Production' -count=1`: PASS, exit 0.
- Retired append RED: `TestProductionDocumentServiceInternalAppendUsesRetiredExactBoundType` failed with `production document type is not active`, exit 1.
- Retired append GREEN plus PostgreSQL lock tests: PASS, exit 0.
- Brief command: `go test ./internal/application/repository ./internal/application/service ./internal/container -run 'Production|SourceRequirements|SnapshotGovernance|ReviewerAvailability|PublicationPolicy|QualityRules' -count=1`: PASS, exit 0.
- Full affected packages outside the filesystem/network sandbox: `go test ./internal/application/repository ./internal/application/service ./internal/container ./internal/handler -count=1`: PASS, exit 0.
- `git diff --check f44469e`: PASS, exit 0.

All Go commands used `GOCACHE=/tmp/weknora-task6-gocache`. The initial default-cache run was blocked by sandbox permissions, and the first full-package sandbox run could not bind `httptest` loopback ports; the approved outside-sandbox rerun passed.

## Files

- Interfaces/repositories: `internal/types/interfaces/production_source.go`, `internal/types/interfaces/production_project.go`, `internal/application/repository/production_source.go`, `internal/application/repository/production_project.go`, `internal/application/repository/production_document.go`, and focused repository tests.
- Services: production source, writer, validator/evidence validator, document, project, review, release, and run services, plus focused tests and fixture adaptations.
- Handlers: test-only interface/constructor adaptations in production document/project handler tests.

## Concerns

No open architecture blocker. The retired append exception is deliberately limited to authenticated internal AI-run persistence; type creation, human append, and source freeze still require active types. Linker/compiler warnings from existing native dependencies remain unchanged.

## Fix After Review

### Inherited findings

1. Source-freeze validation was outside the authoritative freeze locks.
2. Legacy governance adaptation was not shared and fail closed across all seven config fields.
3. A retired-type internal append trusted only the exported principal and did not require a persisted run.
4. Reviewer availability was read without locks held through review materialization.
5. Missing-section validation could emit duplicate issues across quality gates.

### Fixes

- Source freeze now locks the source set (`FOR UPDATE`) and exact document type (`FOR SHARE`) in the same unit of work, then revalidates status, canonical config, accepted evidence, and accepted source kinds before freezing. SQLite uses the existing writer reservation. Deterministic no-sleep races cover a newly accepted forbidden kind, rejected evidence, and type retirement winning before the authoritative lock.
- The shared legacy adapter now runs only when `block_schema`, `source_requirements`, `skill_bindings`, `workflow_plan`, `quality_rules`, `review_policy`, and `publication_policy` are all empty for a known legacy code. Source, writer/validator, review, and release consume the same canonical config path. Partial legacy governance fails closed.
- Internal append now requires a persisted `running` `write` or `rewrite` run bound to the exact tenant, project, document, source set, input parent, deterministic output version, and complete document-type snapshot. The service prevalidates the run, while the document repository independently reserves/locks the run before document/type/source locks and rechecks the snapshot. Human/create paths remain active-only.
- Reviewer availability now locks live `production_project_members` and `tenant_members` rows with `FOR UPDATE OF member, tenant_member` on PostgreSQL. SQLite performs a no-op update over the same live candidates to reserve the writer. These locks remain in the submission unit of work through `CreateCurrentReview`. No-sleep races cover project-role removal and tenant-member suspension winning before the transaction starts; both return `reviewer_role_unavailable` and persist no request.
- Missing required sections are tracked per validation pass, so overlapping gates emit one `required_section_missing` issue per section.

### RED and GREEN evidence

- Missing-section RED: `TestValidateProductionVersionDeduplicatesMissingSectionAcrossQualityGates` observed duplicate `required_section_missing`; focused GREEN passed after per-section tracking.
- Legacy adapter RED covered all-empty success versus partial-governance rejection in writer, review, and release; focused GREEN passed with the shared seven-field predicate.
- Source retirement race RED: with the locked active-status check removed, `TestProductionSourceServiceFreezeRevalidatesGovernanceAfterWinningLock/document_type_retirement_commits_before_lock` froze successfully and failed the test; restoring the lock-time status check made the full source race group pass.
- Persisted-run RED: service and repository nonexistent-run tests both observed successful appends, and the PostgreSQL contract observed document locking before any run lock. GREEN passed after the run authority and lock order were added.
- Reviewer-lock RED: the PostgreSQL contract observed `SELECT count(*)` with no row lock. GREEN passed after the dual-table locking query and SQLite reservation were added.
- Focused source command: `go test ./internal/application/repository ./internal/application/service -run 'LocksFreezeGovernanceInOrder|FreezeRevalidatesGovernanceAfterWinningLock|SourceRequirements' -count=1`: PASS, exit 0.
- Focused document command: `go test ./internal/application/repository ./internal/application/service -run 'ProductionDocument' -count=1`: PASS, exit 0.
- Focused reviewer command: `go test ./internal/application/repository ./internal/application/service -run 'PostgresLocksLiveReviewerRows|RequiresEveryConfiguredReviewerAvailability|RevalidatesReviewerAfterConcurrentRevocationWins' -count=1`: PASS, exit 0.
- Combined command: `go test ./internal/application/repository ./internal/application/service ./internal/container -run 'Production|SourceRequirements|SnapshotGovernance|ReviewerAvailability|PublicationPolicy|QualityRules' -count=1`: PASS, exit 0.
- Full affected packages outside the loopback sandbox: `go test ./internal/application/repository ./internal/application/service ./internal/container ./internal/handler -count=1`: PASS, exit 0.
- `git diff --check 0ae7206`: PASS, exit 0.

All Go commands used `GOCACHE=/tmp/weknora-task6-gocache`.

### Files and concerns

The review fix changes production source, document, project, review, release, writer/run, validator, their interfaces, and focused repository/service/handler tests. Existing partial test fixtures were upgraded to canonical seven-field governance without weakening production validation. No migration or external API shape changed. The document-service constructor gains the already-registered run repository dependency. The pre-existing unstaged `.gitignore` remains excluded from staging. No open architecture blocker remains; the only observed warning is the existing duplicate `-lc++` linker warning in container tests.
