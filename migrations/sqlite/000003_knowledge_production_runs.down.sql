DROP TRIGGER IF EXISTS trg_production_runs_fence_terminal_children;
DROP TRIGGER IF EXISTS trg_production_tool_calls_fence_parent_update;
DROP TRIGGER IF EXISTS trg_production_tool_calls_fence_parent_insert;
DROP TRIGGER IF EXISTS trg_production_tool_calls_guard_invocation_replace;
DROP TRIGGER IF EXISTS trg_production_tool_calls_guard_approval_decision;
DROP TRIGGER IF EXISTS trg_production_tool_calls_guard_invocation_identity;
DROP TRIGGER IF EXISTS trg_production_tool_calls_guard_terminal_delete;
DROP TRIGGER IF EXISTS trg_production_tool_calls_guard_terminal_replace;
DROP TRIGGER IF EXISTS trg_production_tool_calls_guard_terminal;
DROP TRIGGER IF EXISTS trg_production_runs_guard_terminal_delete;
DROP TRIGGER IF EXISTS trg_production_runs_guard_terminal_replace;
DROP TRIGGER IF EXISTS trg_production_runs_guard_terminal;
DROP TRIGGER IF EXISTS trg_production_evidence_snapshots_prevent_frozen_insert;

DROP TABLE IF EXISTS production_tool_calls;
DROP TABLE IF EXISTS production_runs;

CREATE TRIGGER trg_production_evidence_snapshots_prevent_frozen_insert
    BEFORE INSERT ON production_evidence_snapshots
    FOR EACH ROW
    WHEN EXISTS (
        SELECT 1 FROM production_source_items item
        JOIN production_source_sets source_set ON source_set.id = item.source_set_id
        WHERE item.id = NEW.source_item_id AND source_set.status = 'frozen'
    )
BEGIN
    SELECT RAISE(ABORT, 'evidence cannot be inserted into frozen source sets');
END;

DROP INDEX IF EXISTS uq_production_evidence_snapshots_item_context;
DROP INDEX IF EXISTS uq_production_source_items_set_context;
DROP INDEX IF EXISTS uq_production_document_versions_run_context;
