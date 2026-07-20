DROP TRIGGER IF EXISTS trg_production_runs_fence_terminal_children ON production_runs;
DROP FUNCTION IF EXISTS fence_terminal_production_run_children();
DROP TRIGGER IF EXISTS trg_production_runs_guard_workflow_identity ON production_runs;
DROP FUNCTION IF EXISTS guard_production_run_workflow_identity();
DROP TRIGGER IF EXISTS trg_production_tool_calls_fence_parent ON production_tool_calls;
DROP FUNCTION IF EXISTS fence_production_tool_call_parent();
DROP TRIGGER IF EXISTS trg_production_tool_calls_guard_invocation_identity ON production_tool_calls;
DROP FUNCTION IF EXISTS guard_production_tool_call_invocation_identity();
DROP TRIGGER IF EXISTS trg_production_tool_calls_guard_terminal ON production_tool_calls;
DROP FUNCTION IF EXISTS guard_terminal_production_tool_call();
DROP TRIGGER IF EXISTS trg_production_runs_guard_terminal ON production_runs;
DROP FUNCTION IF EXISTS guard_terminal_production_run();
DROP TRIGGER IF EXISTS trg_production_evidence_snapshots_prevent_frozen_insert ON production_evidence_snapshots;
DROP FUNCTION IF EXISTS allow_only_frozen_tool_call_evidence_insert();

ALTER TABLE production_evidence_snapshots
    DROP CONSTRAINT IF EXISTS fk_production_evidence_snapshots_captured_tool_call;

DROP TABLE IF EXISTS production_tool_calls;
DROP TABLE IF EXISTS production_runs;

CREATE OR REPLACE FUNCTION prevent_frozen_production_evidence_insert()
RETURNS TRIGGER AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM production_source_items item
        JOIN production_source_sets source_set ON source_set.id = item.source_set_id
        WHERE item.id = NEW.source_item_id AND source_set.status = 'frozen'
    ) THEN
        RAISE EXCEPTION 'evidence cannot be inserted into frozen source sets';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_evidence_snapshots_prevent_frozen_insert
    BEFORE INSERT ON production_evidence_snapshots
    FOR EACH ROW
    EXECUTE FUNCTION prevent_frozen_production_evidence_insert();

DROP INDEX IF EXISTS uq_production_evidence_snapshots_item_context;
DROP INDEX IF EXISTS uq_production_source_items_set_context;
DROP INDEX IF EXISTS uq_production_document_versions_run_context;
