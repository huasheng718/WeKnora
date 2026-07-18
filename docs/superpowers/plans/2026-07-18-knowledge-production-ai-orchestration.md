# Knowledge Production AI Orchestration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add persistent AI runs and durable Skill, MCP and data-source tool calls that can pause for approval, resume on another worker and create a validated document version.

**Architecture:** Database rows are the source of truth for orchestration state. Asynq tasks are short-lived wakeups; workers execute one resumable step and never block waiting for a human. Existing Skill loader/sandbox, MCP policy checker/client and data-source connector registry are wrapped behind production adapters.

**Tech Stack:** Go 1.26, GORM, Asynq, Redis, existing Skill manager, MCP client, data-source registry, model service, Langfuse tracing.

## Global Constraints

- Requires Documents and Evidence plan.
- PostgreSQL migration version is `000072`; SQLite migration version is `000003`.
- Queue name is `production`; task types are `production:collect`, `production:write`, `production:validate`.
- Workers never wait in memory for tool approval.
- Tool credentials and raw secrets are never copied into snapshots.
- Only preloaded Skills are available in the first release.
- Every completed tool call creates an immutable evidence snapshot before model use.
- Every production write route uses the Domain Foundation `idempotency.Require()` middleware.

---

### Task 1: Add Persistent Run and Tool-Call Schema

**Files:**
- Create: `migrations/versioned/000072_knowledge_production_runs.up.sql`
- Create: `migrations/versioned/000072_knowledge_production_runs.down.sql`
- Create: `migrations/sqlite/000003_knowledge_production_runs.up.sql`
- Create: `migrations/sqlite/000003_knowledge_production_runs.down.sql`
- Modify: `internal/database/production_migration_test.go`

**Interfaces:**
- Consumes: production documents, versions and evidence snapshots.
- Produces: `production_runs` and `production_tool_calls`.

- [ ] **Step 1: Add failing schema assertions**

```go
for _, table := range []string{"production_runs", "production_tool_calls"} {
    requireMigrationTable(t, postgres, table)
    requireMigrationTable(t, sqlite, table)
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/database -run Production -v`
Expected: FAIL.

- [ ] **Step 3: Add schemas with resume fields**

Include `attempt`, `current_step`, `state_payload`, `model_id`, `input_version_id`, `output_version_id`, timestamps and terminal error fields on runs. Include unique `idempotency_key` per run and request/response snapshot fields on tool calls.

- [ ] **Step 4: Run migration tests**

Run: `go test ./internal/database -run Production -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add migrations/versioned/000072_* migrations/sqlite/000003_* internal/database/production_migration_test.go
git commit -m "feat(production): add persistent AI run schema"
```

### Task 2: Register Production Queue and Payloads

**Files:**
- Create: `internal/types/production_run.go`
- Create: `internal/types/interfaces/production_run.go`
- Modify: `internal/types/task.go`
- Modify: `internal/types/task_queue_test.go`

**Interfaces:**
- Consumes: central queue topology in `internal/types/task.go`.
- Produces: `QueueProduction`, production task constants and `ProductionRunPayload`.

- [ ] **Step 1: Write failing topology test**

```go
func TestProductionTaskTypesUseProductionQueue(t *testing.T) {
    for _, taskType := range []string{TypeProductionCollect, TypeProductionWrite, TypeProductionValidate} {
        queue, ok := QueueForTaskType(taskType)
        require.True(t, ok)
        require.Equal(t, QueueProduction, queue)
    }
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/types -run ProductionTaskTypes -v`
Expected: FAIL with undefined constants.

- [ ] **Step 3: Add queue definitions and payload**

```go
const QueueProduction = "production"
const TypeProductionCollect = "production:collect"
const TypeProductionWrite = "production:write"
const TypeProductionValidate = "production:validate"

type ProductionRunPayload struct {
    TracingContext
    TenantID uint64 `json:"tenant_id"`
    RunID    string `json:"run_id"`
    Attempt  int    `json:"attempt"`
}
```

Add production queue concurrency to `WorkerPoolConcurrency` as a separately configurable pool.

- [ ] **Step 4: Run topology tests**

Run: `go test ./internal/types -run 'Queue|Production' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/types/production_run.go internal/types/interfaces/production_run.go internal/types/task.go internal/types/task_queue_test.go
git commit -m "feat(production): register AI orchestration queue"
```

### Task 3: Implement Persistent Run State Machine

**Files:**
- Create: `internal/application/repository/production_run.go`
- Create: `internal/application/repository/production_run_test.go`
- Create: `internal/application/service/production_orchestrator.go`
- Create: `internal/application/service/production_orchestrator_test.go`

**Interfaces:**
- Consumes: run repository, document/source services and task enqueuer.
- Produces: create, claim, advance, wait, complete, fail, cancel and resume operations.

- [ ] **Step 1: Write failing restart-safe tests**

```go
func TestRunWaitingApprovalDoesNotHoldWorker(t *testing.T) {
    orchestrator := newProductionOrchestratorFixture(t, approvalRequiredTool())
    err := orchestrator.HandleRun(ctx, "run-1")
    require.NoError(t, err)
    run := loadRun(t, "run-1")
    require.Equal(t, types.ProductionRunWaitingApproval, run.Status)
    require.Empty(t, fakeTaskEnqueuer(t).BlockingWaits)
}

func TestResumeUsesPersistedCurrentStep(t *testing.T) {
    orchestrator := newProductionOrchestratorFixture(t, completedApprovalAtStep(2))
    require.NoError(t, orchestrator.HandleRun(ctx, "run-1"))
    require.Equal(t, 3, loadRun(t, "run-1").CurrentStep)
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/application/repository ./internal/application/service -run ProductionOrchestrator -v`
Expected: FAIL.

- [ ] **Step 3: Implement compare-and-swap transitions**

```go
type ProductionRunRepository interface {
    Create(ctx context.Context, run *types.ProductionRun) error
    Get(ctx context.Context, tenantID uint64, runID string) (*types.ProductionRun, error)
    Transition(ctx context.Context, runID string, from, to types.ProductionRunStatus, patch types.JSONMap) (bool, error)
    CreateToolCall(ctx context.Context, call *types.ProductionToolCall) error
    ResolveToolCall(ctx context.Context, callID string, decision types.ProductionToolCallStatus, actor string) (bool, error)
}
```

Every handler executes one state transition, persists output, and returns. Resume enqueues a new task carrying only run ID and attempt.

- [ ] **Step 4: Run state-machine tests including race**

Run: `go test -race ./internal/application/repository ./internal/application/service -run ProductionOrchestrator -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/application/repository/production_run* internal/application/service/production_orchestrator*
git commit -m "feat(production): add restart-safe AI orchestration"
```

### Task 4: Add Skill and Data-Source Production Adapters

**Files:**
- Create: `internal/application/service/production_skill_adapter.go`
- Create: `internal/application/service/production_skill_adapter_test.go`
- Create: `internal/application/service/production_datasource_adapter.go`
- Create: `internal/application/service/production_datasource_adapter_test.go`

**Interfaces:**
- Consumes: `skills.Manager`, `interfaces.SkillService`, connector registry and evidence service.
- Produces: sanitized `ProductionToolAdapter` results stored as evidence.

- [ ] **Step 1: Write failing adapter tests**

```go
func TestSkillAdapterPinsInstructionDigest(t *testing.T) {
    adapter := newSkillAdapterFixture(t, "---\nname: baseline\ndescription: x\n---\nRules")
    result, err := adapter.Execute(ctx, callForSkill("baseline"))
    require.NoError(t, err)
    require.NotEmpty(t, result.ProviderDigest)
    require.Equal(t, result.ProviderDigest, result.Evidence.Metadata["skill_digest"])
}

func TestDataSourceAdapterRedactsCredentials(t *testing.T) {
    result := executeDataSourceFixture(t, map[string]any{"token": "secret", "title": "Doc"})
    require.NotContains(t, string(result.Evidence.Content), "secret")
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/application/service -run 'ProductionSkillAdapter|ProductionDataSourceAdapter' -v`
Expected: FAIL.

- [ ] **Step 3: Implement shared adapter contract**

```go
type ProductionToolAdapter interface {
    Plan(ctx context.Context, call *types.ProductionToolCall) (*ProductionToolPlan, error)
    Execute(ctx context.Context, call *types.ProductionToolCall) (*ProductionToolResult, error)
}
```

Reject Skills not present in the document-type snapshot. Store only normalized output, provider digest and redaction metadata.

- [ ] **Step 4: Run adapter tests**

Run: `go test ./internal/application/service -run 'Production.*Adapter' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/application/service/production_skill_adapter* internal/application/service/production_datasource_adapter*
git commit -m "feat(production): add skill and datasource adapters"
```

### Task 5: Generate Evidence-Grounded Structured Document Versions

**Files:**
- Create: `internal/application/service/production_writer.go`
- Create: `internal/application/service/production_writer_test.go`

**Interfaces:**
- Consumes: `interfaces.ModelService.GetChatModel`, frozen evidence snapshots, the document-type block template and `ProductionEvidenceValidator`.
- Produces: `ProductionWriter.Write(ctx context.Context, run *types.ProductionRun) (*types.ProductionDocumentVersion, error)` and a server-validated block payload for `ProductionDocumentService.AppendVersion`.

- [ ] **Step 1: Write failing schema and grounding tests**

```go
func TestProductionWriterRejectsUnknownEvidenceReference(t *testing.T) {
    writer := newProductionWriterFixture(t, modelJSON(`{
        "blocks":[{"logical_block_id":"block-a","block_type":"fact","content":{"text":"已上线"},"evidence_refs":["evidence-missing"],"needs_confirmation":false}]
    }`))
    _, err := writer.Write(ctx, productionRun("run-1"))
    require.ErrorIs(t, err, types.ErrProductionEvidenceReferenceInvalid)
}

func TestProductionWriterMarksUnsupportedFactsForConfirmation(t *testing.T) {
    writer := newProductionWriterFixture(t, modelJSON(`{
        "blocks":[{"logical_block_id":"block-a","block_type":"fact","content":{"text":"转化率提升 30%"},"evidence_refs":[],"needs_confirmation":false}]
    }`))
    version, err := writer.Write(ctx, productionRun("run-1"))
    require.NoError(t, err)
    require.True(t, version.Blocks[0].NeedsConfirmation)
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/application/service -run ProductionWriter -v`
Expected: FAIL because the writer and structured output contract do not exist.

- [ ] **Step 3: Implement the server-owned output schema and model call**

```go
type ProductionWriterBlock struct {
    LogicalBlockID   string          `json:"logical_block_id" jsonschema:"required"`
    BlockType        string          `json:"block_type" jsonschema:"required"`
    Content          json.RawMessage `json:"content" jsonschema:"required"`
    EvidenceRefs     []string        `json:"evidence_refs" jsonschema:"required"`
    NeedsConfirmation bool           `json:"needs_confirmation" jsonschema:"required"`
}

type ProductionWriterOutput struct {
    Blocks []ProductionWriterBlock `json:"blocks" jsonschema:"required,minItems=1"`
}

chatModel, err := w.modelService.GetChatModel(ctx, run.ModelID)
if err != nil {
    return nil, err
}
response, err := chatModel.Chat(ctx, messages, &chat.ChatOptions{
    Format: utils.GenerateSchema[ProductionWriterOutput](),
})
```

The server constructs `messages` from the pinned template, Skill digest, accepted evidence snapshots and current immutable version; model-supplied instructions cannot replace the schema. Persist the raw model response and its SHA-256 digest on the run before decoding. Decode with `json.Decoder.DisallowUnknownFields`, validate every evidence ID against the run's frozen source set, force `needs_confirmation=true` on factual blocks without supporting evidence, and append the resulting version only after the complete payload passes validation. Invalid output keeps the auditable raw response but leaves `output_version_id` empty.

- [ ] **Step 4: Run writer and orchestration tests**

Run: `go test ./internal/application/service -run 'ProductionWriter|ProductionOrchestrator|ProductionEvidence' -v`
Expected: PASS, including invalid JSON, unknown fields, unknown evidence IDs and unsupported facts.

- [ ] **Step 5: Commit**

```bash
git add internal/application/service/production_writer*
git commit -m "feat(production): generate grounded document versions"
```

### Task 6: Add Durable MCP Approval and Resume API

**Files:**
- Create: `internal/application/service/production_mcp_adapter.go`
- Create: `internal/application/service/production_mcp_adapter_test.go`
- Create: `internal/handler/production_run.go`
- Create: `internal/handler/production_run_test.go`
- Modify: `internal/router/router.go`
- Modify: `internal/router/task.go`
- Modify: `internal/router/sync_task.go`
- Modify: `internal/container/container.go`
- Modify: `internal/types/audit_log.go`

**Interfaces:**
- Consumes: `MCPToolApprovalService.IsRequired`, MCP manager/client and task enqueuer.
- Produces: production run start/status, tool-call decision and worker handlers.

- [ ] **Step 1: Write failing durable-approval tests**

```go
func TestApproveToolCallRequeuesRunOnce(t *testing.T) {
    h := newProductionRunHandlerFixture(t, pendingToolCall("call-1", "run-1"))
    rec := postJSON(t, h, "/api/v1/production/tool-calls/call-1/decision", `{"decision":"approve"}`)
    require.Equal(t, http.StatusOK, rec.Code)
    require.Equal(t, 1, enqueuedRunCount(t, "run-1"))
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/application/service ./internal/handler ./internal/router -run 'ProductionMCP|ProductionRun' -v`
Expected: FAIL.

- [ ] **Step 3: Implement policy check, decision endpoint and task wiring**

```go
production.POST("/documents/:id/runs", g.Contributor(), idempotency.Require(), runHandler.Start)
production.POST("/source-sets/:id/collect", g.Contributor(), idempotency.Require(), runHandler.StartCollection)
production.GET("/runs/:id", g.Viewer(), runHandler.Get)
production.POST("/tool-calls/:id/decision", g.Contributor(), idempotency.Require(), runHandler.DecideToolCall)
```

On approval, atomically change tool call from `pending_approval` to `approved`, emit `production.tool_call_approved`, then enqueue the run. A duplicate decision returns HTTP 409 and does not enqueue again.

- [ ] **Step 4: Run orchestration tests**

Run: `go test -race ./internal/types ./internal/application/repository ./internal/application/service ./internal/handler ./internal/router -run Production -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/application/service/production_mcp_adapter* internal/handler/production_run* internal/router/router.go internal/router/task.go internal/router/sync_task.go internal/container/container.go internal/types/audit_log.go
git commit -m "feat(production): resume AI runs after durable tool approval"
```

## Plan Verification

Run:

```bash
go test ./internal/types ./internal/application/repository ./internal/application/service ./internal/handler ./internal/router
go test -race ./internal/application/repository ./internal/application/service -run Production
```

Expected: PASS, including cross-worker resume and duplicate-approval protection.
