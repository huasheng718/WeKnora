DROP TRIGGER IF EXISTS trg_production_tool_calls_guard_terminal ON production_tool_calls;
DROP FUNCTION IF EXISTS guard_terminal_production_tool_call();
DROP TRIGGER IF EXISTS trg_production_runs_guard_terminal ON production_runs;
DROP FUNCTION IF EXISTS guard_terminal_production_run();

DROP TABLE IF EXISTS production_tool_calls;
DROP TABLE IF EXISTS production_runs;

DROP INDEX IF EXISTS uq_production_evidence_snapshots_item_context;
DROP INDEX IF EXISTS uq_production_source_items_set_context;
DROP INDEX IF EXISTS uq_production_document_versions_run_context;
