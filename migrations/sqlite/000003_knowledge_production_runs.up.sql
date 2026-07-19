CREATE UNIQUE INDEX IF NOT EXISTS uq_production_document_versions_run_context
    ON production_document_versions (id, document_id, tenant_id, project_id, source_set_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_production_source_items_set_context
    ON production_source_items (id, source_set_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_production_evidence_snapshots_item_context
    ON production_evidence_snapshots (id, source_item_id);

CREATE TABLE IF NOT EXISTS production_runs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_id VARCHAR(36) NOT NULL,
    source_set_id VARCHAR(36) NOT NULL,
    run_type VARCHAR(20) NOT NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'queued',
    attempt INTEGER NOT NULL DEFAULT 0,
    current_step INTEGER NOT NULL DEFAULT 0,
    state_payload TEXT NOT NULL DEFAULT '{}',
    model_id VARCHAR(64) NOT NULL,
    document_type_snapshot TEXT NOT NULL,
    input_version_id VARCHAR(36) NULL,
    output_version_id VARCHAR(36) NULL,
    idempotency_key VARCHAR(255) NOT NULL,
    raw_model_response TEXT NULL,
    raw_model_response_digest VARCHAR(64) NULL,
    error_code VARCHAR(64) NULL,
    error_message TEXT NULL,
    started_at DATETIME NULL,
    completed_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_runs_type CHECK (run_type IN ('collect', 'write', 'rewrite', 'validate')),
    CONSTRAINT chk_production_runs_status CHECK (status IN ('queued', 'running', 'waiting_approval', 'completed', 'failed', 'cancelled')),
    CONSTRAINT chk_production_runs_resume CHECK (attempt >= 0 AND current_step >= 0),
    CONSTRAINT chk_production_runs_state_payload CHECK (json_valid(state_payload)),
    CONSTRAINT chk_production_runs_document_type_snapshot CHECK (json_valid(document_type_snapshot)),
    CONSTRAINT chk_production_runs_raw_response CHECK (
        (raw_model_response IS NULL AND raw_model_response_digest IS NULL) OR
        (raw_model_response IS NOT NULL AND raw_model_response_digest IS NOT NULL AND json_valid(raw_model_response) AND length(raw_model_response_digest) = 64 AND raw_model_response_digest NOT GLOB '*[^0-9a-f]*')
    ),
    CONSTRAINT chk_production_runs_terminal_timestamp CHECK (
        (status IN ('completed', 'failed', 'cancelled')) = (completed_at IS NOT NULL)
    ),
    UNIQUE(id, tenant_id, project_id, document_id, source_set_id),
    UNIQUE(tenant_id, idempotency_key),
    FOREIGN KEY (document_id, tenant_id, project_id) REFERENCES production_documents(id, tenant_id, project_id) ON DELETE RESTRICT,
    FOREIGN KEY (source_set_id, tenant_id, project_id) REFERENCES production_source_sets(id, tenant_id, project_id) ON DELETE RESTRICT,
    FOREIGN KEY (input_version_id, document_id, tenant_id, project_id, source_set_id) REFERENCES production_document_versions(id, document_id, tenant_id, project_id, source_set_id) ON DELETE RESTRICT,
    FOREIGN KEY (output_version_id, document_id, tenant_id, project_id, source_set_id) REFERENCES production_document_versions(id, document_id, tenant_id, project_id, source_set_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_production_runs_document_status
    ON production_runs (tenant_id, project_id, document_id, status);
CREATE INDEX IF NOT EXISTS idx_production_runs_source_set
    ON production_runs (tenant_id, project_id, source_set_id);

CREATE TABLE IF NOT EXISTS production_tool_calls (
    id VARCHAR(36) PRIMARY KEY,
    run_id VARCHAR(36) NOT NULL,
    tenant_id INTEGER NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_id VARCHAR(36) NOT NULL,
    source_set_id VARCHAR(36) NOT NULL,
    attempt INTEGER NOT NULL,
    current_step INTEGER NOT NULL,
    idempotency_key VARCHAR(255) NOT NULL,
    provider_type VARCHAR(20) NOT NULL,
    provider_id VARCHAR(255) NOT NULL,
    tool_name VARCHAR(255) NOT NULL,
    request_snapshot TEXT NOT NULL,
    request_digest VARCHAR(64) NOT NULL,
    response_snapshot TEXT NULL,
    response_digest VARCHAR(64) NULL,
    response_evidence_id VARCHAR(36) NULL,
    response_evidence_source_item_id VARCHAR(36) NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'planned',
    approval_status VARCHAR(20) NOT NULL DEFAULT 'not_required',
    approval_requested_at DATETIME NULL,
    approved_by VARCHAR(36) NULL,
    approved_at DATETIME NULL,
    rejected_by VARCHAR(36) NULL,
    rejected_at DATETIME NULL,
    error_code VARCHAR(64) NULL,
    error_message TEXT NULL,
    started_at DATETIME NULL,
    completed_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_tool_calls_provider CHECK (provider_type IN ('skill', 'mcp', 'datasource')),
    CONSTRAINT chk_production_tool_calls_status CHECK (status IN ('planned', 'pending_approval', 'approved', 'rejected', 'executing', 'completed', 'failed')),
    CONSTRAINT chk_production_tool_calls_approval_status CHECK (approval_status IN ('not_required', 'pending', 'approved', 'rejected')),
    CONSTRAINT chk_production_tool_calls_resume CHECK (attempt >= 0 AND current_step >= 0),
    CONSTRAINT chk_production_tool_calls_request_snapshot CHECK (json_valid(request_snapshot)),
    CONSTRAINT chk_production_tool_calls_request_digest CHECK (length(request_digest) = 64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
    CONSTRAINT chk_production_tool_calls_response CHECK (
        (response_snapshot IS NULL AND response_digest IS NULL) OR
        (response_snapshot IS NOT NULL AND response_digest IS NOT NULL AND json_valid(response_snapshot) AND length(response_digest) = 64 AND response_digest NOT GLOB '*[^0-9a-f]*')
    ),
    CONSTRAINT chk_production_tool_calls_evidence_pair CHECK (
        (response_evidence_id IS NULL) = (response_evidence_source_item_id IS NULL)
    ),
    CONSTRAINT chk_production_tool_calls_completed_output CHECK (
        status <> 'completed' OR (
            response_snapshot IS NOT NULL AND response_digest IS NOT NULL AND
            response_evidence_id IS NOT NULL AND response_evidence_source_item_id IS NOT NULL
        )
    ),
    CONSTRAINT chk_production_tool_calls_terminal_timestamp CHECK (
        (status IN ('completed', 'failed', 'rejected')) = (completed_at IS NOT NULL)
    ),
    CONSTRAINT chk_production_tool_calls_approval CHECK (
        (approval_status = 'not_required' AND status NOT IN ('pending_approval', 'approved', 'rejected') AND
            approval_requested_at IS NULL AND approved_by IS NULL AND approved_at IS NULL AND rejected_by IS NULL AND rejected_at IS NULL) OR
        (approval_status = 'pending' AND status = 'pending_approval' AND approval_requested_at IS NOT NULL AND
            approved_by IS NULL AND approved_at IS NULL AND rejected_by IS NULL AND rejected_at IS NULL) OR
        (approval_status = 'approved' AND status IN ('approved', 'executing', 'completed', 'failed') AND approval_requested_at IS NOT NULL AND
            approved_by IS NOT NULL AND approved_at IS NOT NULL AND rejected_by IS NULL AND rejected_at IS NULL) OR
        (approval_status = 'rejected' AND status = 'rejected' AND approval_requested_at IS NOT NULL AND
            approved_by IS NULL AND approved_at IS NULL AND rejected_by IS NOT NULL AND rejected_at IS NOT NULL)
    ),
    UNIQUE(run_id, idempotency_key),
    UNIQUE(run_id, attempt, current_step),
    FOREIGN KEY (run_id, tenant_id, project_id, document_id, source_set_id) REFERENCES production_runs(id, tenant_id, project_id, document_id, source_set_id) ON DELETE RESTRICT,
    FOREIGN KEY (response_evidence_id, response_evidence_source_item_id) REFERENCES production_evidence_snapshots(id, source_item_id) ON DELETE RESTRICT,
    FOREIGN KEY (response_evidence_source_item_id, source_set_id) REFERENCES production_source_items(id, source_set_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_production_tool_calls_run_status
    ON production_tool_calls (run_id, status, attempt, current_step);
CREATE INDEX IF NOT EXISTS idx_production_tool_calls_approval
    ON production_tool_calls (tenant_id, status, approval_status);

CREATE TRIGGER IF NOT EXISTS trg_production_tool_calls_guard_invocation_identity
    BEFORE UPDATE ON production_tool_calls
    FOR EACH ROW
    WHEN NEW.id IS NOT OLD.id OR (
        OLD.status <> 'planned' AND (
            NEW.run_id IS NOT OLD.run_id OR
            NEW.tenant_id IS NOT OLD.tenant_id OR
            NEW.project_id IS NOT OLD.project_id OR
            NEW.document_id IS NOT OLD.document_id OR
            NEW.source_set_id IS NOT OLD.source_set_id OR
            NEW.provider_type IS NOT OLD.provider_type OR
            NEW.provider_id IS NOT OLD.provider_id OR
            NEW.tool_name IS NOT OLD.tool_name OR
            NEW.request_snapshot IS NOT OLD.request_snapshot OR
            NEW.request_digest IS NOT OLD.request_digest OR
            NEW.attempt IS NOT OLD.attempt OR
            NEW.current_step IS NOT OLD.current_step OR
            NEW.idempotency_key IS NOT OLD.idempotency_key
        )
    )
BEGIN
    SELECT RAISE(ABORT, 'production tool call invocation identity is immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_tool_calls_guard_approval_decision
    BEFORE UPDATE ON production_tool_calls
    FOR EACH ROW
    WHEN OLD.approval_status IN ('approved', 'rejected') AND (
        NEW.approval_status IS NOT OLD.approval_status OR
        NEW.approval_requested_at IS NOT OLD.approval_requested_at OR
        NEW.approved_by IS NOT OLD.approved_by OR
        NEW.approved_at IS NOT OLD.approved_at OR
        NEW.rejected_by IS NOT OLD.rejected_by OR
        NEW.rejected_at IS NOT OLD.rejected_at
    )
BEGIN
    SELECT RAISE(ABORT, 'production tool call approval decision is immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_tool_calls_guard_invocation_replace
    BEFORE INSERT ON production_tool_calls
    FOR EACH ROW
    WHEN EXISTS (
        SELECT 1 FROM production_tool_calls
        WHERE status IN ('pending_approval', 'approved', 'executing')
          AND (id = NEW.id OR (run_id = NEW.run_id AND idempotency_key = NEW.idempotency_key) OR
               (run_id = NEW.run_id AND attempt = NEW.attempt AND current_step = NEW.current_step))
    )
BEGIN
    SELECT RAISE(ABORT, 'production tool call invocation identity is immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_tool_calls_fence_parent_insert
    BEFORE INSERT ON production_tool_calls
    FOR EACH ROW
    WHEN EXISTS (
        SELECT 1 FROM production_runs
        WHERE id = NEW.run_id AND status IN ('completed', 'failed', 'cancelled')
    )
BEGIN
    SELECT RAISE(ABORT, 'terminal production runs reject tool calls');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_tool_calls_fence_parent_update
    BEFORE UPDATE ON production_tool_calls
    FOR EACH ROW
    WHEN EXISTS (
        SELECT 1 FROM production_runs
        WHERE id IN (OLD.run_id, NEW.run_id) AND status IN ('completed', 'failed', 'cancelled')
    )
BEGIN
    SELECT RAISE(ABORT, 'terminal production runs reject tool calls');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_runs_fence_terminal_children
    BEFORE UPDATE OF status ON production_runs
    FOR EACH ROW
    WHEN OLD.status NOT IN ('completed', 'failed', 'cancelled')
      AND NEW.status IN ('completed', 'failed', 'cancelled')
      AND EXISTS (
          SELECT 1 FROM production_tool_calls call
          WHERE call.run_id = OLD.id
            AND call.status IN ('planned', 'pending_approval', 'approved', 'executing')
      )
BEGIN
    SELECT RAISE(ABORT, 'active production tool calls prevent terminal run');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_runs_guard_terminal
    BEFORE UPDATE ON production_runs
    FOR EACH ROW
    WHEN OLD.status IN ('completed', 'failed', 'cancelled')
BEGIN
    SELECT RAISE(ABORT, 'terminal production runs are immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_runs_guard_terminal_replace
    BEFORE INSERT ON production_runs
    FOR EACH ROW
    WHEN EXISTS (
        SELECT 1 FROM production_runs
        WHERE status IN ('completed', 'failed', 'cancelled')
          AND (id = NEW.id OR (tenant_id = NEW.tenant_id AND idempotency_key = NEW.idempotency_key))
    )
BEGIN
    SELECT RAISE(ABORT, 'terminal production runs cannot be replaced');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_runs_guard_terminal_delete
    BEFORE DELETE ON production_runs
    FOR EACH ROW
    WHEN OLD.status IN ('completed', 'failed', 'cancelled')
BEGIN
    SELECT RAISE(ABORT, 'terminal production runs are immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_tool_calls_guard_terminal
    BEFORE UPDATE ON production_tool_calls
    FOR EACH ROW
    WHEN OLD.status IN ('completed', 'failed', 'rejected') AND NOT EXISTS (
        SELECT 1 FROM production_runs
        WHERE id = OLD.run_id AND status IN ('completed', 'failed', 'cancelled')
    )
BEGIN
    SELECT RAISE(ABORT, 'terminal production tool calls are immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_tool_calls_guard_terminal_replace
    BEFORE INSERT ON production_tool_calls
    FOR EACH ROW
    WHEN EXISTS (
        SELECT 1 FROM production_tool_calls
        WHERE status IN ('completed', 'failed', 'rejected')
          AND (id = NEW.id OR (run_id = NEW.run_id AND idempotency_key = NEW.idempotency_key) OR
               (run_id = NEW.run_id AND attempt = NEW.attempt AND current_step = NEW.current_step))
    )
BEGIN
    SELECT RAISE(ABORT, 'terminal production tool calls cannot be replaced');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_tool_calls_guard_terminal_delete
    BEFORE DELETE ON production_tool_calls
    FOR EACH ROW
    WHEN OLD.status IN ('completed', 'failed', 'rejected')
BEGIN
    SELECT RAISE(ABORT, 'terminal production tool calls are immutable');
END;
