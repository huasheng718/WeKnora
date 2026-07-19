# AI Orchestration Task 3 Report

## State Transitions

| From | To | Durable cause and guard |
|---|---|---|
| `queued` | `running` | Claim CAS on tenant, run, status, attempt and current step; attempt is preserved. |
| `running` | `running` | Expired-lease reclaim CAS; attempt increments and fences the stale worker. |
| `running` | `queued` | One step persisted successfully; state/raw output and next current step commit before enqueue. |
| `running` | `waiting_approval` | Pending tool call and run transition commit in one transaction; no enqueue or blocking wait. |
| `running` | `completed` | Final step output and terminal timestamp persist under the worker's exact attempt/step fence. |
| `running` | `failed` | Executor error and terminal error metadata persist under the exact worker fence. |
| `queued`, `running` | `cancelled` | Tenant-scoped cancellation CAS with terminal timestamp. |
| `waiting_approval` | `queued` | Matching approved tool call and waiting run transition commit atomically, then enqueue. |
| `waiting_approval` | `failed` | Rejected tool call and failed parent transition commit atomically. |
| `waiting_approval` | `cancelled` | Pending call rejection and parent cancellation commit atomically. |
| terminal | any | Rejected in the repository and by database terminal immutability guards. |

## RED / GREEN

- RED 1: focused repository/service tests failed on the absent run repository, full CAS/claim/tool-call surface, step result and orchestrator.
- GREEN 1: focused repository and service tests passed after the SQLite repository and one-step orchestrator were implemented.
- RED 2: the first race run found a duplicate approval loser could observe a pre-decision call and post-decision run and report an error.
- GREEN 2: the mismatch path now re-reads the durable call and treats an already-resolved decision as a no-op; focused race passed.
- RED 3: `CreateRun` persisted a noncanonical UUID before validating its worker wakeup contract.
- GREEN 3: the exact tenant/run/attempt payload validates before insert.

## Concurrency and Restart Safety

- Every read, claim, transition, list and decision includes tenant scope. Run writes compare exact status, attempt and current step; tool decisions compare exact status, attempt and step.
- SQLite concurrency tests use a real file-backed WAL database, multiple connections and channel barriers without sleeps. Two simultaneous claims produce one winner.
- `updated_at` is the schema-available lease boundary. An expired running claim increments attempt; stale attempt output cannot advance the reclaimed run.
- A handler executes one step and returns. Waiting approval stores the pending call and returns without enqueue. A fresh orchestrator receives approved calls and the persisted current step from the database.
- Wakeups contain only the approved transport fields. Deterministic tenant/run/attempt/step task IDs make duplicate resume enqueue idempotent. A queue failure after a queued step leaves a redeliverable durable row.
- Snapshot JSON is canonicalized before storage, SHA-256 digests are computed from canonical bytes, and credential-shaped fields are rejected.
- PostgreSQL claim and lock SQL contracts assert tenant, run, status, attempt and current-step predicates plus `RETURNING` / `FOR UPDATE`. No live PostgreSQL instance was available.

## Files

- `internal/types/interfaces/production_run.go`
- `internal/application/repository/production_run.go`
- `internal/application/repository/production_run_test.go`
- `internal/application/service/production_orchestrator.go`
- `internal/application/service/production_orchestrator_test.go`

## Verification and Concerns

- `go test -race ./internal/application/repository ./internal/application/service -run 'ProductionRunRepository|ProductionOrchestrator' -count=1` passed.
- Broader `Production` tests across types, interfaces, repository and service passed; targeted `go vet` and `git diff --check` passed.
- The lease is deliberately a fixed database timestamp for one bounded step; there is no in-memory heartbeat or worker-held lock. Concrete long-running Skill/MCP/writer executors must keep a step within the configured lease or add a separately governed durable renewal operation.
