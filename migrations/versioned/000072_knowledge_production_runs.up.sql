CREATE UNIQUE INDEX IF NOT EXISTS uq_production_document_versions_run_context
    ON production_document_versions (id, document_id, tenant_id, project_id, source_set_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_production_source_items_set_context
    ON production_source_items (id, source_set_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_production_evidence_snapshots_item_context
    ON production_evidence_snapshots (id, source_item_id);

CREATE TABLE IF NOT EXISTS production_runs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_id VARCHAR(36) NULL,
    source_set_id VARCHAR(36) NOT NULL,
    run_type VARCHAR(20) NOT NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'queued',
    attempt INTEGER NOT NULL DEFAULT 0,
    current_step INTEGER NOT NULL DEFAULT 0,
    wakeup_version INTEGER NOT NULL DEFAULT 0,
    wakeup_enqueued_version INTEGER NOT NULL DEFAULT 0,
    state_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    model_id VARCHAR(64) NOT NULL,
    document_type_snapshot JSONB NOT NULL,
    workflow_plan_snapshot JSONB NOT NULL DEFAULT '{"steps":[],"version":1}'::jsonb,
    input_version_id VARCHAR(36) NULL,
    output_version_id VARCHAR(36) NULL,
    idempotency_key VARCHAR(255) NOT NULL,
    raw_model_response JSONB NULL,
    raw_model_response_digest VARCHAR(64) NULL,
    error_code VARCHAR(64) NULL,
    error_message TEXT NULL,
    started_at TIMESTAMP NULL,
    completed_at TIMESTAMP NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_runs_type CHECK (run_type IN ('collect', 'write', 'rewrite', 'validate')),
    CONSTRAINT chk_production_runs_document_scope CHECK (
        (run_type = 'collect' AND document_id IS NULL AND input_version_id IS NULL AND output_version_id IS NULL) OR
        (run_type IN ('write', 'rewrite', 'validate') AND document_id IS NOT NULL)
    ),
    CONSTRAINT chk_production_runs_status CHECK (status IN ('queued', 'running', 'waiting_approval', 'completed', 'failed', 'cancelled')),
    CONSTRAINT chk_production_runs_resume CHECK (attempt >= 0 AND current_step >= 0),
    CONSTRAINT chk_production_runs_wakeup CHECK (
        wakeup_version >= 0 AND wakeup_enqueued_version >= 0 AND
        wakeup_enqueued_version <= wakeup_version
    ),
    CONSTRAINT chk_production_runs_raw_response CHECK (
        (raw_model_response IS NULL AND raw_model_response_digest IS NULL) OR
        (raw_model_response IS NOT NULL AND raw_model_response_digest IS NOT NULL AND raw_model_response_digest ~ '^[0-9a-f]{64}$')
    ),
    CONSTRAINT chk_production_runs_terminal_timestamp CHECK (
        (status IN ('completed', 'failed', 'cancelled')) = (completed_at IS NOT NULL)
    ),
    UNIQUE(id, tenant_id, project_id, document_id, source_set_id),
    UNIQUE(tenant_id, idempotency_key),
    CONSTRAINT fk_production_runs_document_context FOREIGN KEY (document_id, tenant_id, project_id) REFERENCES production_documents(id, tenant_id, project_id) ON DELETE RESTRICT,
    CONSTRAINT fk_production_runs_source_set_context FOREIGN KEY (source_set_id, tenant_id, project_id) REFERENCES production_source_sets(id, tenant_id, project_id) ON DELETE RESTRICT,
    CONSTRAINT fk_production_runs_input_version_context FOREIGN KEY (input_version_id, document_id, tenant_id, project_id, source_set_id) REFERENCES production_document_versions(id, document_id, tenant_id, project_id, source_set_id) ON DELETE RESTRICT,
    CONSTRAINT fk_production_runs_output_version_context FOREIGN KEY (output_version_id, document_id, tenant_id, project_id, source_set_id) REFERENCES production_document_versions(id, document_id, tenant_id, project_id, source_set_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_production_runs_document_status
    ON production_runs (tenant_id, project_id, document_id, status);
CREATE INDEX IF NOT EXISTS idx_production_runs_source_set
    ON production_runs (tenant_id, project_id, source_set_id);

CREATE TABLE IF NOT EXISTS production_tool_calls (
    id VARCHAR(36) PRIMARY KEY,
    run_id VARCHAR(36) NOT NULL,
    tenant_id BIGINT NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_id VARCHAR(36) NULL,
    source_set_id VARCHAR(36) NOT NULL,
    attempt INTEGER NOT NULL,
    current_step INTEGER NOT NULL,
    idempotency_key VARCHAR(255) NOT NULL,
    provider_type VARCHAR(20) NOT NULL,
    provider_id VARCHAR(255) NOT NULL,
    tool_name VARCHAR(255) NOT NULL,
    request_snapshot JSONB NOT NULL,
    request_digest VARCHAR(64) NOT NULL,
    response_snapshot JSONB NULL,
    response_digest VARCHAR(64) NULL,
    response_evidence_id VARCHAR(36) NULL,
    response_evidence_source_item_id VARCHAR(36) NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'planned',
    approval_status VARCHAR(20) NOT NULL DEFAULT 'not_required',
    approval_requested_at TIMESTAMP NULL,
    approved_by VARCHAR(36) NULL,
    approved_at TIMESTAMP NULL,
    rejected_by VARCHAR(36) NULL,
    rejected_at TIMESTAMP NULL,
    error_code VARCHAR(64) NULL,
    error_message TEXT NULL,
    started_at TIMESTAMP NULL,
    completed_at TIMESTAMP NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_tool_calls_provider CHECK (provider_type IN ('skill', 'mcp', 'datasource')),
    CONSTRAINT chk_production_tool_calls_status CHECK (status IN ('planned', 'pending_approval', 'approved', 'rejected', 'executing', 'completed', 'failed')),
    CONSTRAINT chk_production_tool_calls_approval_status CHECK (approval_status IN ('not_required', 'pending', 'approved', 'rejected')),
    CONSTRAINT chk_production_tool_calls_resume CHECK (attempt >= 0 AND current_step >= 0),
    CONSTRAINT chk_production_tool_calls_request_digest CHECK (request_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT chk_production_tool_calls_response CHECK (
        (response_snapshot IS NULL AND response_digest IS NULL) OR
        (response_snapshot IS NOT NULL AND response_digest IS NOT NULL AND response_digest ~ '^[0-9a-f]{64}$')
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
    CONSTRAINT fk_production_tool_calls_run_context FOREIGN KEY (run_id, tenant_id, project_id, document_id, source_set_id) REFERENCES production_runs(id, tenant_id, project_id, document_id, source_set_id) ON DELETE RESTRICT,
    CONSTRAINT fk_production_tool_calls_evidence_context FOREIGN KEY (response_evidence_id, response_evidence_source_item_id) REFERENCES production_evidence_snapshots(id, source_item_id) ON DELETE RESTRICT,
    CONSTRAINT fk_production_tool_calls_source_item_context FOREIGN KEY (response_evidence_source_item_id, source_set_id) REFERENCES production_source_items(id, source_set_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_production_tool_calls_run_status
    ON production_tool_calls (run_id, status, attempt, current_step);
CREATE INDEX IF NOT EXISTS idx_production_tool_calls_approval
    ON production_tool_calls (tenant_id, status, approval_status);

DROP TRIGGER IF EXISTS trg_production_evidence_snapshots_prevent_frozen_insert ON production_evidence_snapshots;
DROP FUNCTION IF EXISTS prevent_frozen_production_evidence_insert();
CREATE OR REPLACE FUNCTION allow_only_frozen_run_evidence_insert()
RETURNS TRIGGER AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM production_source_items item
        JOIN production_source_sets source_set ON source_set.id = item.source_set_id
        WHERE item.id = NEW.source_item_id AND source_set.status = 'frozen'
    ) AND NOT EXISTS (
        SELECT 1
        FROM production_source_items item
        JOIN production_source_sets source_set ON source_set.id = item.source_set_id
        JOIN production_runs run
          ON run.id = NEW.captured_by_run_id
         AND run.tenant_id = source_set.tenant_id
         AND run.project_id = source_set.project_id
         AND run.source_set_id = source_set.id
        WHERE item.id = NEW.source_item_id
          AND item.status = 'accepted'
          AND NEW.id <> ''
    ) THEN
        RAISE EXCEPTION 'frozen source set requires accepted run-captured evidence';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_evidence_snapshots_prevent_frozen_insert
    BEFORE INSERT ON production_evidence_snapshots
    FOR EACH ROW
    EXECUTE FUNCTION allow_only_frozen_run_evidence_insert();

CREATE OR REPLACE FUNCTION guard_production_tool_call_invocation_identity()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id THEN
        RAISE EXCEPTION 'production tool call invocation identity is immutable';
    END IF;
    IF OLD.status <> 'planned' AND (
        NEW.run_id IS DISTINCT FROM OLD.run_id OR
        NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR
        NEW.project_id IS DISTINCT FROM OLD.project_id OR
        NEW.document_id IS DISTINCT FROM OLD.document_id OR
        NEW.source_set_id IS DISTINCT FROM OLD.source_set_id OR
        NEW.provider_type IS DISTINCT FROM OLD.provider_type OR
        NEW.provider_id IS DISTINCT FROM OLD.provider_id OR
        NEW.tool_name IS DISTINCT FROM OLD.tool_name OR
        NEW.request_snapshot IS DISTINCT FROM OLD.request_snapshot OR
        NEW.request_digest IS DISTINCT FROM OLD.request_digest OR
        NEW.attempt IS DISTINCT FROM OLD.attempt OR
        NEW.current_step IS DISTINCT FROM OLD.current_step OR
        NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
    ) THEN
        RAISE EXCEPTION 'production tool call invocation identity is immutable';
    END IF;
    IF OLD.approval_status IN ('approved', 'rejected') AND (
        NEW.approval_status IS DISTINCT FROM OLD.approval_status OR
        NEW.approval_requested_at IS DISTINCT FROM OLD.approval_requested_at OR
        NEW.approved_by IS DISTINCT FROM OLD.approved_by OR
        NEW.approved_at IS DISTINCT FROM OLD.approved_at OR
        NEW.rejected_by IS DISTINCT FROM OLD.rejected_by OR
        NEW.rejected_at IS DISTINCT FROM OLD.rejected_at
    ) THEN
        RAISE EXCEPTION 'production tool call approval decision is immutable';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_tool_calls_guard_invocation_identity
    BEFORE UPDATE ON production_tool_calls
    FOR EACH ROW
    EXECUTE FUNCTION guard_production_tool_call_invocation_identity();

CREATE OR REPLACE FUNCTION fence_production_tool_call_parent()
RETURNS TRIGGER AS $$
DECLARE
    parent_status VARCHAR(24);
BEGIN
    IF TG_OP = 'INSERT' THEN
        SELECT status INTO parent_status
        FROM production_runs
        WHERE id = NEW.run_id
          AND tenant_id = NEW.tenant_id
          AND project_id = NEW.project_id
          AND document_id IS NOT DISTINCT FROM NEW.document_id
          AND source_set_id = NEW.source_set_id
        FOR UPDATE;
        IF parent_status IS NULL OR parent_status IN ('completed', 'failed', 'cancelled') THEN
            RAISE EXCEPTION 'terminal production runs reject tool calls';
        END IF;
        RETURN NEW;
    END IF;

    FOR parent_status IN
        SELECT status
        FROM production_runs
        WHERE id IN (OLD.run_id, NEW.run_id)
        ORDER BY id
        FOR UPDATE
    LOOP
        IF parent_status IN ('completed', 'failed', 'cancelled') THEN
            RAISE EXCEPTION 'terminal production runs reject tool calls';
        END IF;
    END LOOP;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_tool_calls_fence_parent
    BEFORE INSERT OR UPDATE ON production_tool_calls
    FOR EACH ROW
    EXECUTE FUNCTION fence_production_tool_call_parent();

CREATE OR REPLACE FUNCTION fence_terminal_production_run_children()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status NOT IN ('completed', 'failed', 'cancelled')
       AND NEW.status IN ('completed', 'failed', 'cancelled')
       AND EXISTS (
           SELECT 1
           FROM production_tool_calls call
           WHERE call.run_id = OLD.id
             AND call.status IN ('planned', 'pending_approval', 'approved', 'executing')
       ) THEN
        RAISE EXCEPTION 'active production tool calls prevent terminal run';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_runs_fence_terminal_children
    BEFORE UPDATE OF status ON production_runs
    FOR EACH ROW
    EXECUTE FUNCTION fence_terminal_production_run_children();

CREATE OR REPLACE FUNCTION guard_terminal_production_run()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status IN ('completed', 'failed', 'cancelled') AND (TG_OP = 'DELETE' OR NEW IS DISTINCT FROM OLD) THEN
        RAISE EXCEPTION 'terminal production runs are immutable';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_runs_guard_terminal
    BEFORE UPDATE OR DELETE ON production_runs
    FOR EACH ROW
    EXECUTE FUNCTION guard_terminal_production_run();

CREATE OR REPLACE FUNCTION guard_terminal_production_tool_call()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status IN ('completed', 'failed', 'rejected') AND (TG_OP = 'DELETE' OR NEW IS DISTINCT FROM OLD) THEN
        RAISE EXCEPTION 'terminal production tool calls are immutable';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_tool_calls_guard_terminal
    BEFORE UPDATE OR DELETE ON production_tool_calls
    FOR EACH ROW
    EXECUTE FUNCTION guard_terminal_production_tool_call();
