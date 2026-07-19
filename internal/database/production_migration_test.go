package database

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	pg_query "github.com/pganalyze/pg_query_go/v6"
	"github.com/stretchr/testify/require"
)

func mustReadMigration(t *testing.T, path string) string {
	t.Helper()

	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(contents)
}

func TestProductionFoundationMigrationsDeclareRequiredTables(t *testing.T) {
	postgres := mustReadMigration(t, "../../migrations/versioned/000070_knowledge_production_foundation.up.sql")
	sqlite := mustReadMigration(t, "../../migrations/sqlite/000001_knowledge_production_foundation.up.sql")

	for _, table := range []string{
		"production_projects",
		"production_project_members",
		"production_document_types",
		"production_idempotency_keys",
	} {
		require.Contains(t, postgres, "CREATE TABLE IF NOT EXISTS "+table)
		require.Contains(t, sqlite, "CREATE TABLE IF NOT EXISTS "+table)
	}
}

func TestProductionDocumentsMigrationsDeclareRequiredTables(t *testing.T) {
	postgres := mustReadMigration(t, "../../migrations/versioned/000071_knowledge_production_documents.up.sql")
	sqlite := mustReadMigration(t, "../../migrations/sqlite/000002_knowledge_production_documents.up.sql")

	for _, table := range []string{
		"production_source_sets",
		"production_source_items",
		"production_evidence_snapshots",
		"production_documents",
		"production_document_versions",
		"production_document_blocks",
		"production_block_lineage",
	} {
		require.Contains(t, postgres, "CREATE TABLE IF NOT EXISTS "+table)
		require.Contains(t, sqlite, "CREATE TABLE IF NOT EXISTS "+table)
	}
}

func TestProductionRunsMigrationsDeclareRequiredTables(t *testing.T) {
	postgres := mustReadMigration(t, "../../migrations/versioned/000072_knowledge_production_runs.up.sql")
	sqlite := mustReadMigration(t, "../../migrations/sqlite/000003_knowledge_production_runs.up.sql")

	for _, table := range []string{"production_runs", "production_tool_calls"} {
		require.Contains(t, postgres, "CREATE TABLE IF NOT EXISTS "+table)
		require.Contains(t, sqlite, "CREATE TABLE IF NOT EXISTS "+table)
	}
}

func TestProductionRunsPostgreSQLMigrationDeclaresEquivalentStructure(t *testing.T) {
	up := mustReadMigration(t, "../../migrations/versioned/000072_knowledge_production_runs.up.sql")
	_, err := pg_query.Parse(up)
	require.NoError(t, err)

	for _, declaration := range []string{
		"state_payload JSONB NOT NULL DEFAULT '{}'::jsonb",
		"document_type_snapshot JSONB NOT NULL",
		"raw_model_response JSONB NULL",
		"request_snapshot JSONB NOT NULL",
		"response_snapshot JSONB NULL",
		"CHECK (run_type IN ('collect', 'write', 'rewrite', 'validate'))",
		"CHECK (status IN ('queued', 'running', 'waiting_approval', 'completed', 'failed', 'cancelled'))",
		"CHECK (provider_type IN ('skill', 'mcp', 'datasource'))",
		"CHECK (status IN ('planned', 'pending_approval', 'approved', 'rejected', 'executing', 'completed', 'failed'))",
		"FOREIGN KEY (document_id, tenant_id, project_id) REFERENCES production_documents(id, tenant_id, project_id)",
		"FOREIGN KEY (source_set_id, tenant_id, project_id) REFERENCES production_source_sets(id, tenant_id, project_id)",
		"FOREIGN KEY (input_version_id, document_id, tenant_id, project_id, source_set_id)",
		"FOREIGN KEY (output_version_id, document_id, tenant_id, project_id, source_set_id)",
		"FOREIGN KEY (run_id, tenant_id, project_id, document_id, source_set_id)",
		"FOREIGN KEY (response_evidence_id, response_evidence_source_item_id)",
		"FOREIGN KEY (response_evidence_source_item_id, source_set_id)",
		"CREATE TRIGGER trg_production_runs_guard_terminal",
		"CREATE TRIGGER trg_production_tool_calls_guard_terminal",
		"BEFORE UPDATE OR DELETE ON production_runs",
		"BEFORE UPDATE OR DELETE ON production_tool_calls",
		"CREATE OR REPLACE FUNCTION guard_production_tool_call_invocation_identity()",
		"CREATE TRIGGER trg_production_tool_calls_guard_invocation_identity",
		"OLD.status <> 'planned'",
		"CREATE OR REPLACE FUNCTION fence_production_tool_call_parent()",
		"CREATE TRIGGER trg_production_tool_calls_fence_parent",
		"FOR UPDATE",
		"CREATE OR REPLACE FUNCTION fence_terminal_production_run_children()",
		"CREATE TRIGGER trg_production_runs_fence_terminal_children",
		"call.status IN ('planned', 'pending_approval', 'approved', 'executing')",
		"raw_model_response IS NOT NULL AND raw_model_response_digest IS NOT NULL",
		"response_snapshot IS NOT NULL AND response_digest IS NOT NULL",
		"NEW.run_id IS DISTINCT FROM OLD.run_id",
		"NEW.tenant_id IS DISTINCT FROM OLD.tenant_id",
		"NEW.project_id IS DISTINCT FROM OLD.project_id",
		"NEW.document_id IS DISTINCT FROM OLD.document_id",
		"NEW.source_set_id IS DISTINCT FROM OLD.source_set_id",
		"NEW.provider_type IS DISTINCT FROM OLD.provider_type",
		"NEW.provider_id IS DISTINCT FROM OLD.provider_id",
		"NEW.tool_name IS DISTINCT FROM OLD.tool_name",
		"NEW.request_snapshot IS DISTINCT FROM OLD.request_snapshot",
		"NEW.request_digest IS DISTINCT FROM OLD.request_digest",
		"NEW.attempt IS DISTINCT FROM OLD.attempt",
		"NEW.current_step IS DISTINCT FROM OLD.current_step",
		"NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key",
	} {
		require.Contains(t, up, declaration)
	}
	for _, secret := range []string{"api_key", "access_token", "refresh_token", "credential", "secret"} {
		require.NotContains(t, strings.ToLower(up), secret)
	}

	down := mustReadMigration(t, "../../migrations/versioned/000072_knowledge_production_runs.down.sql")
	_, err = pg_query.Parse(down)
	require.NoError(t, err)
	for _, declaration := range []string{
		"DROP TRIGGER IF EXISTS trg_production_runs_fence_terminal_children ON production_runs",
		"DROP FUNCTION IF EXISTS fence_terminal_production_run_children()",
		"DROP TRIGGER IF EXISTS trg_production_tool_calls_fence_parent ON production_tool_calls",
		"DROP FUNCTION IF EXISTS fence_production_tool_call_parent()",
		"DROP TRIGGER IF EXISTS trg_production_tool_calls_guard_invocation_identity ON production_tool_calls",
		"DROP FUNCTION IF EXISTS guard_production_tool_call_invocation_identity()",
	} {
		require.Contains(t, down, declaration)
	}
	require.Less(t, strings.Index(down, "DROP TABLE IF EXISTS production_tool_calls"), strings.Index(down, "DROP TABLE IF EXISTS production_runs"))
	require.Less(t, strings.Index(down, "DROP TABLE IF EXISTS production_runs"), strings.Index(down, "DROP INDEX IF EXISTS uq_production_document_versions_run_context"))
}

func TestProductionRunsSQLiteMigrationFreezesInvocationIdentityAfterApprovalRequested(t *testing.T) {
	for _, mutation := range productionToolCallIdentityMutations() {
		t.Run(mutation.name, func(t *testing.T) {
			db := openProductionRunsSQLite(t)
			seedProductionRunScopes(t, db)
			insertProductionRun(t, db, "run-1", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-1")
			insertProductionRun(t, db, "run-other", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-other")
			insertProductionToolCall(t, db, "call-1", "run-1", "call-key-1", 0, 0)
			_, err := db.Exec(`UPDATE production_tool_calls SET status = 'pending_approval', approval_status = 'pending', approval_requested_at = CURRENT_TIMESTAMP WHERE id = 'call-1'`)
			require.NoError(t, err)

			_, err = db.Exec(`UPDATE production_tool_calls SET ` + mutation.assignment + ` WHERE id = 'call-1'`)
			require.ErrorContains(t, err, "production tool call invocation identity is immutable")
		})
	}
}

func TestProductionRunsSQLiteMigrationFreezesInvocationIdentityThroughApprovedCompletion(t *testing.T) {
	for _, mutation := range productionToolCallIdentityMutations() {
		t.Run(mutation.name, func(t *testing.T) {
			db := openProductionRunsSQLite(t)
			seedProductionRunScopes(t, db)
			insertProductionRun(t, db, "run-1", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-1")
			insertProductionRun(t, db, "run-other", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-other")
			insertProductionToolCall(t, db, "call-1", "run-1", "call-key-1", 0, 0)
			_, err := db.Exec(`UPDATE production_tool_calls SET status = 'pending_approval', approval_status = 'pending', approval_requested_at = CURRENT_TIMESTAMP WHERE id = 'call-1'`)
			require.NoError(t, err)
			_, err = db.Exec(`UPDATE production_tool_calls SET status = 'approved', approval_status = 'approved', approved_by = 'reviewer-1', approved_at = CURRENT_TIMESTAMP WHERE id = 'call-1'`)
			require.NoError(t, err)

			_, err = db.Exec(`UPDATE production_tool_calls SET status = 'completed', response_snapshot = '{"ok":true}', response_digest = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', response_evidence_id = 'evidence-1', response_evidence_source_item_id = 'source-item-1', completed_at = CURRENT_TIMESTAMP, ` + mutation.assignment + ` WHERE id = 'call-1'`)
			require.ErrorContains(t, err, "production tool call invocation identity is immutable")
		})
	}
}

func TestProductionRunsSQLiteMigrationRejectsFrozenInvocationReplace(t *testing.T) {
	db := openProductionRunsSQLite(t)
	seedProductionRunScopes(t, db)
	insertProductionRun(t, db, "run-1", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-1")
	insertProductionToolCall(t, db, "call-1", "run-1", "call-key-1", 0, 0)
	_, err := db.Exec(`UPDATE production_tool_calls SET status = 'pending_approval', approval_status = 'pending', approval_requested_at = CURRENT_TIMESTAMP WHERE id = 'call-1'`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT OR REPLACE INTO production_tool_calls (id, run_id, tenant_id, project_id, document_id, source_set_id, attempt, current_step, idempotency_key, provider_type, provider_id, tool_name, request_snapshot, request_digest) VALUES ('call-replaced', 'run-1', 1, 'project-1', 'document-1', 'source-set-1', 0, 1, 'call-key-1', 'mcp', 'changed', 'changed', '{"changed":true}', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb')`)
	require.ErrorContains(t, err, "production tool call invocation identity is immutable")
}

func TestProductionRunsSQLiteMigrationRequiresExactPayloadDigestPairs(t *testing.T) {
	db := openProductionRunsSQLite(t)
	seedProductionRunScopes(t, db)

	_, err := db.Exec(`INSERT INTO production_runs (id, tenant_id, project_id, document_id, source_set_id, run_type, model_id, document_type_snapshot, idempotency_key, raw_model_response) VALUES ('run-payload-only', 1, 'project-1', 'document-1', 'source-set-1', 'write', 'model-1', '{}', 'run-payload-only', '{"text":"value"}')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_runs (id, tenant_id, project_id, document_id, source_set_id, run_type, model_id, document_type_snapshot, idempotency_key, raw_model_response_digest) VALUES ('run-digest-only', 1, 'project-1', 'document-1', 'source-set-1', 'write', 'model-1', '{}', 'run-digest-only', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_runs (id, tenant_id, project_id, document_id, source_set_id, run_type, model_id, document_type_snapshot, idempotency_key, raw_model_response, raw_model_response_digest) VALUES ('run-valid-pair', 1, 'project-1', 'document-1', 'source-set-1', 'write', 'model-1', '{}', 'run-valid-pair', '{"text":"value"}', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`)
	require.NoError(t, err)

	insertProductionRun(t, db, "run-1", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-1")
	insertProductionToolCall(t, db, "call-payload-only", "run-1", "call-payload-only", 0, 0)
	_, err = db.Exec(`UPDATE production_tool_calls SET response_snapshot = '{"ok":true}' WHERE id = 'call-payload-only'`)
	require.Error(t, err)
	insertProductionToolCall(t, db, "call-digest-only", "run-1", "call-digest-only", 0, 1)
	_, err = db.Exec(`UPDATE production_tool_calls SET response_digest = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' WHERE id = 'call-digest-only'`)
	require.Error(t, err)
	insertProductionToolCall(t, db, "call-valid-pair", "run-1", "call-valid-pair", 0, 2)
	_, err = db.Exec(`UPDATE production_tool_calls SET response_snapshot = '{"ok":true}', response_digest = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' WHERE id = 'call-valid-pair'`)
	require.NoError(t, err)
}

func TestProductionRunsSQLiteMigrationFencesTerminalParentAndActiveChildren(t *testing.T) {
	t.Run("terminal parent rejects insert", func(t *testing.T) {
		db := openProductionRunsSQLite(t)
		seedProductionRunScopes(t, db)
		insertProductionRun(t, db, "run-1", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-1")
		_, err := db.Exec(`UPDATE production_runs SET status = 'failed', completed_at = CURRENT_TIMESTAMP WHERE id = 'run-1'`)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO production_tool_calls (id, run_id, tenant_id, project_id, document_id, source_set_id, attempt, current_step, idempotency_key, provider_type, provider_id, tool_name, request_snapshot, request_digest) VALUES ('call-late', 'run-1', 1, 'project-1', 'document-1', 'source-set-1', 0, 0, 'call-late', 'skill', 'writer', 'collect', '{}', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`)
		require.ErrorContains(t, err, "terminal production runs reject tool calls")
	})

	t.Run("call cannot advance after parent terminal", func(t *testing.T) {
		db := openProductionRunsSQLite(t)
		seedProductionRunScopes(t, db)
		insertProductionRun(t, db, "run-1", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-1")
		insertProductionToolCall(t, db, "call-1", "run-1", "call-key-1", 0, 0)
		_, err := db.Exec(`UPDATE production_tool_calls SET status = 'completed', response_snapshot = '{"ok":true}', response_digest = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', response_evidence_id = 'evidence-1', response_evidence_source_item_id = 'source-item-1', completed_at = CURRENT_TIMESTAMP WHERE id = 'call-1'`)
		require.NoError(t, err)
		_, err = db.Exec(`UPDATE production_runs SET status = 'completed', output_version_id = 'version-1', completed_at = CURRENT_TIMESTAMP WHERE id = 'run-1'`)
		require.NoError(t, err)
		_, err = db.Exec(`UPDATE production_tool_calls SET updated_at = CURRENT_TIMESTAMP WHERE id = 'call-1'`)
		require.ErrorContains(t, err, "terminal production runs reject tool calls")
	})

	for _, childStatus := range []string{"planned", "pending_approval", "approved", "executing"} {
		t.Run("active child "+childStatus, func(t *testing.T) {
			db := openProductionRunsSQLite(t)
			seedProductionRunScopes(t, db)
			insertProductionRun(t, db, "run-1", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-1")
			insertProductionToolCall(t, db, "call-1", "run-1", "call-key-1", 0, 0)
			setProductionToolCallStatus(t, db, "call-1", childStatus)
			_, err := db.Exec(`UPDATE production_runs SET status = 'failed', completed_at = CURRENT_TIMESTAMP WHERE id = 'run-1'`)
			require.ErrorContains(t, err, "active production tool calls prevent terminal run")
		})
	}

	t.Run("all terminal children allow terminal run", func(t *testing.T) {
		db := openProductionRunsSQLite(t)
		seedProductionRunScopes(t, db)
		insertProductionRun(t, db, "run-1", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-1")
		insertProductionToolCall(t, db, "call-1", "run-1", "call-key-1", 0, 0)
		setProductionToolCallStatus(t, db, "call-1", "completed")
		_, err := db.Exec(`UPDATE production_runs SET status = 'failed', completed_at = CURRENT_TIMESTAMP WHERE id = 'run-1'`)
		require.NoError(t, err)
	})
}

func TestProductionRunsSQLiteMigrationEnforcesScopeAndIdempotency(t *testing.T) {
	db := openProductionRunsSQLite(t)
	seedProductionRunScopes(t, db)

	insertProductionRun(t, db, "run-1", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-1")
	_, err := db.Exec(`INSERT INTO production_runs (id, tenant_id, project_id, document_id, source_set_id, run_type, model_id, document_type_snapshot, input_version_id, idempotency_key) VALUES ('run-duplicate-key', 1, 'project-1', 'document-1', 'source-set-1', 'write', 'model-1', '{}', 'version-1', 'run-key-1')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_runs (id, tenant_id, project_id, document_id, source_set_id, run_type, model_id, document_type_snapshot, input_version_id, idempotency_key) VALUES ('run-cross-project', 1, 'project-1', 'document-1', 'source-set-2', 'write', 'model-1', '{}', 'version-2', 'run-key-2')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_runs (id, tenant_id, project_id, document_id, source_set_id, run_type, model_id, document_type_snapshot, input_version_id, idempotency_key) VALUES ('run-cross-version', 1, 'project-1', 'document-1', 'source-set-1', 'write', 'model-1', '{}', 'version-2', 'run-key-3')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_runs (id, tenant_id, project_id, document_id, source_set_id, run_type, model_id, document_type_snapshot, input_version_id, idempotency_key) VALUES ('run-cross-tenant', 2, 'project-1', 'document-1', 'source-set-1', 'write', 'model-1', '{}', 'version-1', 'run-key-4')`)
	require.Error(t, err)
	insertProductionRun(t, db, "run-output", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-output")
	_, err = db.Exec(`UPDATE production_runs SET output_version_id = 'version-2' WHERE id = 'run-output'`)
	require.Error(t, err)

	insertProductionToolCall(t, db, "call-1", "run-1", "call-key-1", 0, 0)
	_, err = db.Exec(`INSERT INTO production_tool_calls (id, run_id, tenant_id, project_id, document_id, source_set_id, attempt, current_step, idempotency_key, provider_type, provider_id, tool_name, request_snapshot, request_digest) VALUES ('call-duplicate-key', 'run-1', 1, 'project-1', 'document-1', 'source-set-1', 0, 1, 'call-key-1', 'skill', 'writer', 'collect', '{}', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_tool_calls (id, run_id, tenant_id, project_id, document_id, source_set_id, attempt, current_step, idempotency_key, provider_type, provider_id, tool_name, request_snapshot, request_digest) VALUES ('call-duplicate-step', 'run-1', 1, 'project-1', 'document-1', 'source-set-1', 0, 0, 'call-key-2', 'skill', 'writer', 'collect', '{}', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_tool_calls (id, run_id, tenant_id, project_id, document_id, source_set_id, attempt, current_step, idempotency_key, provider_type, provider_id, tool_name, request_snapshot, request_digest) VALUES ('call-cross-run-scope', 'run-1', 1, 'project-2', 'document-2', 'source-set-2', 0, 1, 'call-key-3', 'skill', 'writer', 'collect', '{}', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`)
	require.Error(t, err)
}

func TestProductionRunsSQLiteMigrationLinksEvidenceWithinSourceSet(t *testing.T) {
	db := openProductionRunsSQLite(t)
	seedProductionRunScopes(t, db)
	insertProductionRun(t, db, "run-1", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-1")
	insertProductionToolCall(t, db, "call-1", "run-1", "call-key-1", 0, 0)

	_, err := db.Exec(`UPDATE production_tool_calls SET status = 'completed', response_snapshot = '{"ok":true}', response_digest = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', response_evidence_id = 'evidence-1', response_evidence_source_item_id = 'source-item-1', completed_at = CURRENT_TIMESTAMP WHERE id = 'call-1'`)
	require.NoError(t, err)

	insertProductionToolCall(t, db, "call-2", "run-1", "call-key-2", 0, 1)
	_, err = db.Exec(`UPDATE production_tool_calls SET status = 'completed', response_snapshot = '{"ok":true}', response_digest = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', response_evidence_id = 'evidence-2', response_evidence_source_item_id = 'source-item-2', completed_at = CURRENT_TIMESTAMP WHERE id = 'call-2'`)
	require.Error(t, err)
}

func TestProductionRunsSQLiteMigrationGuardsTerminalRowsAndReplacements(t *testing.T) {
	db := openProductionRunsSQLite(t)
	seedProductionRunScopes(t, db)
	insertProductionRun(t, db, "run-1", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-1")
	_, err := db.Exec(`UPDATE production_runs SET status = 'completed', output_version_id = 'version-1', raw_model_response = '{"text":"done"}', raw_model_response_digest = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', completed_at = CURRENT_TIMESTAMP WHERE id = 'run-1'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_runs SET status = 'running' WHERE id = 'run-1'`)
	require.ErrorContains(t, err, "terminal production runs are immutable")
	_, err = db.Exec(`UPDATE production_runs SET raw_model_response = '{"text":"changed"}' WHERE id = 'run-1'`)
	require.ErrorContains(t, err, "terminal production runs are immutable")
	_, err = db.Exec(`DELETE FROM production_runs WHERE id = 'run-1'`)
	require.ErrorContains(t, err, "terminal production runs are immutable")
	_, err = db.Exec(`INSERT OR REPLACE INTO production_runs (id, tenant_id, project_id, document_id, source_set_id, run_type, model_id, document_type_snapshot, input_version_id, idempotency_key) VALUES ('run-replaced', 1, 'project-1', 'document-1', 'source-set-1', 'write', 'model-1', '{}', 'version-1', 'run-key-1')`)
	require.ErrorContains(t, err, "terminal production runs cannot be replaced")

	insertProductionRun(t, db, "run-2", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-2")
	insertProductionToolCall(t, db, "call-1", "run-2", "call-key-1", 0, 0)
	_, err = db.Exec(`UPDATE production_tool_calls SET status = 'completed', response_snapshot = '{"ok":true}', response_digest = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', response_evidence_id = 'evidence-1', response_evidence_source_item_id = 'source-item-1', completed_at = CURRENT_TIMESTAMP WHERE id = 'call-1'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_tool_calls SET status = 'executing' WHERE id = 'call-1'`)
	require.ErrorContains(t, err, "terminal production tool calls are immutable")
	_, err = db.Exec(`UPDATE production_tool_calls SET response_snapshot = '{"ok":false}' WHERE id = 'call-1'`)
	require.ErrorContains(t, err, "terminal production tool calls are immutable")
	_, err = db.Exec(`DELETE FROM production_tool_calls WHERE id = 'call-1'`)
	require.ErrorContains(t, err, "terminal production tool calls are immutable")
	_, err = db.Exec(`INSERT OR REPLACE INTO production_tool_calls (id, run_id, tenant_id, project_id, document_id, source_set_id, attempt, current_step, idempotency_key, provider_type, provider_id, tool_name, request_snapshot, request_digest) VALUES ('call-replaced', 'run-2', 1, 'project-1', 'document-1', 'source-set-1', 0, 1, 'call-key-1', 'skill', 'writer', 'collect', '{}', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`)
	require.ErrorContains(t, err, "terminal production tool calls cannot be replaced")
}

func TestProductionRunsSQLiteMigrationRejectsInvalidContracts(t *testing.T) {
	db := openProductionRunsSQLite(t)
	seedProductionRunScopes(t, db)

	_, err := db.Exec(`INSERT INTO production_runs (id, tenant_id, project_id, document_id, source_set_id, run_type, status, model_id, document_type_snapshot, idempotency_key) VALUES ('bad-run-status', 1, 'project-1', 'document-1', 'source-set-1', 'write', 'paused', 'model-1', '{}', 'run-key-1')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_runs (id, tenant_id, project_id, document_id, source_set_id, run_type, model_id, document_type_snapshot, idempotency_key, raw_model_response_digest) VALUES ('bad-run-digest', 1, 'project-1', 'document-1', 'source-set-1', 'write', 'model-1', '{}', 'run-key-2', 'short')`)
	require.Error(t, err)
	insertProductionRun(t, db, "run-1", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-3")
	_, err = db.Exec(`INSERT INTO production_tool_calls (id, run_id, tenant_id, project_id, document_id, source_set_id, attempt, current_step, idempotency_key, provider_type, provider_id, tool_name, request_snapshot, request_digest) VALUES ('bad-provider', 'run-1', 1, 'project-1', 'document-1', 'source-set-1', 0, 0, 'call-key-1', 'http', 'writer', 'collect', '{}', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_tool_calls (id, run_id, tenant_id, project_id, document_id, source_set_id, attempt, current_step, idempotency_key, provider_type, provider_id, tool_name, request_snapshot, request_digest) VALUES ('bad-digest', 'run-1', 1, 'project-1', 'document-1', 'source-set-1', 0, 0, 'call-key-2', 'skill', 'writer', 'collect', '{}', 'not-a-sha256')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_tool_calls (id, run_id, tenant_id, project_id, document_id, source_set_id, attempt, current_step, idempotency_key, provider_type, provider_id, tool_name, request_snapshot, request_digest, status, approval_status) VALUES ('bad-approval', 'run-1', 1, 'project-1', 'document-1', 'source-set-1', 0, 0, 'call-key-3', 'skill', 'writer', 'collect', '{}', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'pending_approval', 'pending')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_tool_calls (id, run_id, tenant_id, project_id, document_id, source_set_id, attempt, current_step, idempotency_key, provider_type, provider_id, tool_name, request_snapshot, request_digest, status, approval_status, approval_requested_at) VALUES ('approval-call', 'run-1', 1, 'project-1', 'document-1', 'source-set-1', 0, 0, 'call-key-4', 'skill', 'writer', 'collect', '{}', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'pending_approval', 'pending', CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_tool_calls SET status = 'approved', approval_status = 'approved', approved_by = 'reviewer-1', approved_at = CURRENT_TIMESTAMP WHERE id = 'approval-call'`)
	require.NoError(t, err)
}

func TestProductionRunsSQLiteMigrationRollsBackPopulatedSchema(t *testing.T) {
	db := openProductionRunsSQLite(t)
	seedProductionRunScopes(t, db)
	insertProductionRun(t, db, "run-1", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-1")
	insertProductionToolCall(t, db, "call-1", "run-1", "call-key-1", 0, 0)
	_, err := db.Exec(`UPDATE production_tool_calls SET status = 'completed', response_snapshot = '{"ok":true}', response_digest = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', response_evidence_id = 'evidence-1', response_evidence_source_item_id = 'source-item-1', completed_at = CURRENT_TIMESTAMP WHERE id = 'call-1'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_runs SET status = 'failed', error_code = 'MODEL_ERROR', error_message = 'failed', completed_at = CURRENT_TIMESTAMP WHERE id = 'run-1'`)
	require.NoError(t, err)

	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000003_knowledge_production_runs.down.sql"))
	require.NoError(t, err)
	for _, table := range []string{"production_tool_calls", "production_runs"} {
		require.Empty(t, sqliteMasterSQL(t, db, "table", table))
	}
	for _, index := range []string{
		"uq_production_document_versions_run_context",
		"uq_production_source_items_set_context",
		"uq_production_evidence_snapshots_item_context",
	} {
		require.Empty(t, sqliteMasterSQL(t, db, "index", index))
	}
}

func TestProductionFoundationSQLiteMigrationPreventsActiveDefinitionUpdates(t *testing.T) {
	db := openProductionFoundationSQLite(t)
	insertProductionDocumentType(t, db, "type-1", "baseline", 1, "draft")

	_, err := db.Exec("UPDATE production_document_types SET status = 'active' WHERE id = 'type-1'")
	require.NoError(t, err)

	for column, value := range map[string]string{
		"name":               "'Changed'",
		"block_schema":       "'{\"required\":true}'",
		"skill_bindings":     "'{\"skill\":\"writer\"}'",
		"review_policy":      "'{\"role\":\"reviewer\"}'",
		"publication_policy": "'{\"target\":\"knowledge-base\"}'",
	} {
		_, err = db.Exec("UPDATE production_document_types SET " + column + " = " + value + " WHERE id = 'type-1'")
		require.ErrorContains(t, err, "active production document type definitions are immutable")
	}

	_, err = db.Exec("UPDATE production_document_types SET status = 'retired' WHERE id = 'type-1'")
	require.NoError(t, err)
}

func TestProductionFoundationSQLiteMigrationFreezesRetiredDocumentTypes(t *testing.T) {
	db := openProductionFoundationSQLite(t)
	insertProductionDocumentType(t, db, "type-retired", "baseline", 1, "draft")

	_, err := db.Exec("UPDATE production_document_types SET status = 'active' WHERE id = 'type-retired'")
	require.NoError(t, err)
	_, err = db.Exec("UPDATE production_document_types SET status = 'retired' WHERE id = 'type-retired'")
	require.NoError(t, err)

	_, err = db.Exec("UPDATE production_document_types SET name = 'Changed' WHERE id = 'type-retired'")
	require.ErrorContains(t, err, "immutable")
	_, err = db.Exec("UPDATE production_document_types SET status = 'draft' WHERE id = 'type-retired'")
	require.ErrorContains(t, err, "invalid production document type status transition")
}

func TestProductionFoundationSQLiteMigrationRejectsInvalidStatuses(t *testing.T) {
	db := openProductionFoundationSQLite(t)

	_, err := db.Exec(`INSERT INTO production_projects (id, tenant_id, name, owner_user_id, status) VALUES ('project-invalid', 1, 'Project', 'owner-1', 'draft')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_document_types (id, tenant_id, code, name, schema_version, status, created_by) VALUES ('type-invalid', 1, 'baseline', 'Baseline', 1, 'unknown', 'owner-1')`)
	require.Error(t, err)
}

func TestProductionFoundationSQLiteMigrationRejectsActivatedStatusEscapes(t *testing.T) {
	tests := []struct {
		name, oldStatus, newStatus string
	}{
		{name: "active to draft", oldStatus: "active", newStatus: "draft"},
		{name: "active to unknown", oldStatus: "active", newStatus: "unknown"},
		{name: "retired to draft", oldStatus: "retired", newStatus: "draft"},
		{name: "retired to active", oldStatus: "retired", newStatus: "active"},
		{name: "retired to unknown", oldStatus: "retired", newStatus: "unknown"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openProductionFoundationSQLite(t)
			insertProductionDocumentType(t, db, "type-1", "baseline", 1, test.oldStatus)

			_, transitionErr := db.Exec(
				"UPDATE production_document_types SET status = ? WHERE id = 'type-1'", test.newStatus,
			)
			require.Error(t, transitionErr)
			_, definitionErr := db.Exec("UPDATE production_document_types SET name = 'Bypassed' WHERE id = 'type-1'")
			require.ErrorContains(t, definitionErr, "immutable")
		})
	}
}

func TestProductionFoundationPostgreSQLMigrationDeclaresEquivalentStructure(t *testing.T) {
	postgres := mustReadMigration(t, "../../migrations/versioned/000070_knowledge_production_foundation.up.sql")
	_, err := pg_query.Parse(postgres)
	require.NoError(t, err)

	for _, declaration := range []string{
		"response_body JSONB NULL",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_production_project_members_live_role",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_production_document_types_live_version",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_production_document_types_active_code",
		"CREATE OR REPLACE FUNCTION prevent_active_production_document_type_definition_update()",
		"CREATE TRIGGER trg_production_document_types_prevent_active_definition_update",
		"OLD.status IN ('active', 'retired')",
		"CONSTRAINT chk_production_projects_status CHECK (status IN ('active', 'archived'))",
		"CONSTRAINT chk_production_document_types_status CHECK (status IN ('draft', 'active', 'retired'))",
		"OLD.status = 'active' AND NEW.status NOT IN ('active', 'retired')",
		"OLD.status = 'retired' AND NEW.status <> 'retired'",
	} {
		require.Contains(t, postgres, declaration)
	}

	postgresDown := mustReadMigration(t, "../../migrations/versioned/000070_knowledge_production_foundation.down.sql")
	_, err = pg_query.Parse(postgresDown)
	require.NoError(t, err)
	require.Contains(t, postgresDown, "DROP TRIGGER IF EXISTS trg_production_document_types_prevent_active_definition_update ON production_document_types")
	require.Contains(t, postgresDown, "DROP FUNCTION IF EXISTS prevent_active_production_document_type_definition_update()")
}

func TestProductionDocumentsPostgreSQLMigrationDeclaresEquivalentStructure(t *testing.T) {
	postgres := mustReadMigration(t, "../../migrations/versioned/000071_knowledge_production_documents.up.sql")
	_, err := pg_query.Parse(postgres)
	require.NoError(t, err)

	for _, declaration := range []string{
		"metadata JSONB NOT NULL DEFAULT '{}'::jsonb",
		"inline_content JSONB NULL",
		"content JSONB NOT NULL",
		"attributes JSONB NOT NULL",
		"evidence_refs JSONB NOT NULL",
		"ai_provenance JSONB NOT NULL",
		"content_digest VARCHAR(64) NOT NULL",
		"UNIQUE(version_id, logical_block_id)",
		"ADD CONSTRAINT uq_production_projects_id_tenant UNIQUE (id, tenant_id)",
		"ADD CONSTRAINT uq_production_document_types_id_tenant UNIQUE (id, tenant_id)",
		"UNIQUE(id, tenant_id, project_id)",
		"UNIQUE(id, document_id)",
		"FOREIGN KEY (project_id, tenant_id) REFERENCES production_projects(id, tenant_id)",
		"FOREIGN KEY (document_type_id, tenant_id) REFERENCES production_document_types(id, tenant_id)",
		"FOREIGN KEY (current_version_id, id) REFERENCES production_document_versions(id, document_id)",
		"FOREIGN KEY (latest_approved_version_id, id) REFERENCES production_document_versions(id, document_id)",
		"FOREIGN KEY (parent_version_id, document_id) REFERENCES production_document_versions(id, document_id)",
		"FOREIGN KEY (document_id, tenant_id, project_id) REFERENCES production_documents(id, tenant_id, project_id)",
		"FOREIGN KEY (source_set_id, tenant_id, project_id) REFERENCES production_source_sets(id, tenant_id, project_id)",
		"CREATE INDEX IF NOT EXISTS idx_production_source_sets_project",
		"CREATE INDEX IF NOT EXISTS idx_production_source_sets_document_type",
		"CREATE INDEX IF NOT EXISTS idx_production_documents_tenant",
		"CREATE INDEX IF NOT EXISTS idx_production_document_versions_source_set",
		"CREATE INDEX IF NOT EXISTS idx_production_block_lineage_from_version",
		"CREATE INDEX IF NOT EXISTS idx_production_block_lineage_to_version",
		"CREATE TRIGGER trg_production_source_sets_prevent_frozen_delete",
		"CREATE TRIGGER trg_production_evidence_snapshots_prevent_replace",
		"CREATE TRIGGER trg_production_document_versions_prevent_replace",
		"CREATE TRIGGER trg_production_document_blocks_prevent_replace",
		"CREATE TRIGGER trg_production_document_versions_prevent_mutation",
		"CREATE TRIGGER trg_production_document_blocks_prevent_mutation",
	} {
		require.Contains(t, postgres, declaration)
	}
	require.NotContains(t, postgres, "trg_production_document_versions_validate_source_set")
	require.NotContains(t, postgres, "trg_production_documents_validate_version_source_sets")
	require.NotContains(t, postgres, "trg_production_source_sets_validate_document_versions")
	require.Equal(t, 4, strings.Count(postgres, "content_digest VARCHAR(64) NOT NULL"))

	postgresDown := mustReadMigration(t, "../../migrations/versioned/000071_knowledge_production_documents.down.sql")
	_, err = pg_query.Parse(postgresDown)
	require.NoError(t, err)
	for _, table := range []string{
		"production_block_lineage",
		"production_document_blocks",
		"production_document_versions",
		"production_documents",
		"production_evidence_snapshots",
		"production_source_items",
		"production_source_sets",
	} {
		require.Contains(t, postgresDown, "DROP TABLE IF EXISTS "+table)
	}
}

func TestProductionDocumentsPostgreSQLMigrationDeclaresFinalIntegrityGuards(t *testing.T) {
	up := mustReadMigration(t, "../../migrations/versioned/000071_knowledge_production_documents.up.sql")
	down := mustReadMigration(t, "../../migrations/versioned/000071_knowledge_production_documents.down.sql")

	for _, fragment := range []string{
		"CHECK ((storage_path IS NOT NULL)::integer + (inline_content IS NOT NULL)::integer = 1)",
		"FOREIGN KEY (from_version_id, from_logical_block_id) REFERENCES production_document_blocks(version_id, logical_block_id)",
		"FOREIGN KEY (to_version_id, to_logical_block_id) REFERENCES production_document_blocks(version_id, logical_block_id)",
		"CHECK (relation IN ('same', 'split', 'merged'))",
		"CREATE TRIGGER trg_production_block_lineage_prevent_mutation",
		"CREATE TRIGGER trg_production_block_lineage_prevent_replace",
		"NEW.source_set_id",
		"production_evidence_snapshots",
		"source items cannot be inserted into frozen source sets",
		"source items in frozen source sets are immutable",
		"evidence cannot be inserted into frozen source sets",
	} {
		require.Contains(t, up, fragment)
	}
	for _, fragment := range []string{
		"DROP TRIGGER IF EXISTS trg_production_block_lineage_prevent_replace ON production_block_lineage",
		"DROP TRIGGER IF EXISTS trg_production_block_lineage_prevent_mutation ON production_block_lineage",
		"DROP FUNCTION IF EXISTS prevent_production_block_lineage_replace()",
		"DROP FUNCTION IF EXISTS prevent_production_block_lineage_mutation()",
	} {
		require.Contains(t, down, fragment)
	}
}

func TestProductionFoundationMigrationsApplySQLiteConstraintsAndRollback(t *testing.T) {
	db := openProductionFoundationSQLite(t)
	_, err := db.Exec(`INSERT INTO production_projects (id, tenant_id, name, owner_user_id) VALUES ('project-1', 1, 'Project', 'owner-1')`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO production_project_members (project_id, user_id, role, assigned_by) VALUES ('project-1', 'user-1', 'author', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_project_members (project_id, user_id, role, assigned_by) VALUES ('project-1', 'user-1', 'author', 'owner-1')`)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE production_project_members SET deleted_at = CURRENT_TIMESTAMP WHERE project_id = 'project-1' AND user_id = 'user-1' AND role = 'author'`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_project_members (project_id, user_id, role, assigned_by) VALUES ('project-1', 'user-1', 'author', 'owner-1')`)
	require.NoError(t, err)

	insertProductionDocumentType(t, db, "type-1", "baseline", 1, "draft")
	_, err = db.Exec(`INSERT INTO production_document_types (id, tenant_id, code, name, schema_version, status, created_by) VALUES ('type-duplicate', 1, 'baseline', 'Baseline', 1, 'draft', 'owner-1')`)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE production_document_types SET deleted_at = CURRENT_TIMESTAMP WHERE id = 'type-1'`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_document_types (id, tenant_id, code, name, schema_version, status, created_by) VALUES ('type-duplicate', 1, 'baseline', 'Baseline', 1, 'draft', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_document_types SET status = 'active' WHERE id = 'type-duplicate'`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_document_types (id, tenant_id, code, name, schema_version, status, created_by) VALUES ('type-active', 1, 'baseline', 'Baseline v2', 2, 'active', 'owner-1')`)
	require.Error(t, err)

	_, err = db.Exec(`INSERT INTO production_idempotency_keys (id, tenant_id, actor_user_id, route, idempotency_key, request_digest, response_body) VALUES ('key-1', 1, 'owner-1', '/projects', 'request-1', 'digest', '{"ok":true}')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_idempotency_keys (id, tenant_id, actor_user_id, route, idempotency_key, request_digest) VALUES ('key-2', 1, 'owner-1', '/projects', 'request-1', 'digest')`)
	require.Error(t, err)

	require.Equal(t, "text", sqliteColumnType(t, db, "production_idempotency_keys", "response_body"))
	for _, index := range []string{
		"idx_production_projects_tenant",
		"uq_production_project_members_live_role",
		"uq_production_document_types_live_version",
		"uq_production_document_types_active_code",
	} {
		require.NotEmpty(t, sqliteMasterSQL(t, db, "index", index))
	}
	require.Contains(t, sqliteMasterSQL(t, db, "trigger", "trg_production_document_types_prevent_active_definition_update"), "BEFORE UPDATE")
	require.Contains(t, sqliteMasterSQL(t, db, "table", "production_projects"), "chk_production_projects_status")
	require.Contains(t, sqliteMasterSQL(t, db, "table", "production_document_types"), "chk_production_document_types_status")

	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000001_knowledge_production_foundation.down.sql"))
	require.NoError(t, err)
	for _, table := range []string{"production_projects", "production_project_members", "production_document_types", "production_idempotency_keys"} {
		require.Empty(t, sqliteMasterSQL(t, db, "table", table))
	}
}

func TestProductionDocumentsSQLiteMigrationEnforcesIntegrityAndRollback(t *testing.T) {
	db := openProductionDocumentsSQLite(t)
	insertProductionDocumentType(t, db, "type-1", "baseline", 1, "active")
	_, err := db.Exec(`INSERT INTO production_projects (id, tenant_id, name, owner_user_id) VALUES ('project-1', 1, 'Project', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_source_sets (id, tenant_id, project_id, document_type_id, created_by) VALUES ('source-set-1', 1, 'project-1', 'type-1', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_source_items (id, source_set_id, source_kind, title, mime_type, content_digest, captured_at, metadata, status) VALUES ('source-item-1', 'source-set-1', 'manual', 'Source', 'text/plain', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', CURRENT_TIMESTAMP, '{}', 'accepted')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_evidence_snapshots (id, source_item_id, snapshot_type, inline_content, content_digest, redaction_metadata) VALUES ('evidence-1', 'source-item-1', 'text', '{"text":"evidence"}', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', '{}')`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_source_sets SET status = 'frozen', frozen_at = CURRENT_TIMESTAMP WHERE id = 'source-set-1'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_source_sets SET status = 'ready' WHERE id = 'source-set-1'`)
	require.ErrorContains(t, err, "frozen source sets cannot be reopened")

	_, err = db.Exec(`INSERT INTO production_documents (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, created_by) VALUES ('document-1', 1, 'project-1', 'type-1', 1, 'Document', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_document_versions (id, document_id, tenant_id, project_id, version_number, source_set_id, origin, content_digest, created_by) VALUES ('version-1', 'document-1', 1, 'project-1', 1, 'source-set-1', 'human', 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'owner-1')`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO production_source_items (id, source_set_id, source_kind, title, mime_type, content_digest, captured_at, metadata) VALUES ('missing-set', 'missing', 'manual', 'Missing', 'text/plain', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', CURRENT_TIMESTAMP, '{}')`)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE production_source_items SET title = 'Changed' WHERE id = 'source-item-1'`)
	require.ErrorContains(t, err, "source items in frozen source sets are immutable")
	_, err = db.Exec(`DELETE FROM production_source_items WHERE id = 'source-item-1'`)
	require.ErrorContains(t, err, "source items in frozen source sets are immutable")
	_, err = db.Exec(`UPDATE production_evidence_snapshots SET content_digest = 'changed' WHERE id = 'evidence-1'`)
	require.ErrorContains(t, err, "evidence snapshots are immutable")

	_, err = db.Exec(`INSERT INTO production_document_blocks (id, version_id, logical_block_id, block_type, position, content, attributes, evidence_refs, ai_provenance, content_digest) VALUES ('block-1', 'version-1', 'block-a', 'paragraph', 1, '{"text":"supported"}', '{}', '["evidence-1"]', '{}', 'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_document_blocks (id, version_id, logical_block_id, block_type, position, content, attributes, evidence_refs, ai_provenance, content_digest) VALUES ('block-duplicate', 'version-1', 'block-a', 'paragraph', 2, '{"text":"duplicate"}', '{}', '[]', '{}', 'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee')`)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE production_document_versions SET change_summary = 'Changed' WHERE id = 'version-1'`)
	require.ErrorContains(t, err, "document versions are append-only")
	_, err = db.Exec(`DELETE FROM production_document_versions WHERE id = 'version-1'`)
	require.ErrorContains(t, err, "document versions are append-only")
	_, err = db.Exec(`UPDATE production_document_blocks SET position = 9 WHERE id = 'block-1'`)
	require.ErrorContains(t, err, "document blocks are append-only")
	_, err = db.Exec(`DELETE FROM production_document_blocks WHERE id = 'block-1'`)
	require.ErrorContains(t, err, "document blocks are append-only")

	for _, jsonColumn := range []struct{ table, column string }{
		{"production_source_items", "metadata"},
		{"production_evidence_snapshots", "inline_content"},
		{"production_evidence_snapshots", "redaction_metadata"},
		{"production_document_blocks", "content"},
		{"production_document_blocks", "attributes"},
		{"production_document_blocks", "evidence_refs"},
		{"production_document_blocks", "ai_provenance"},
	} {
		require.Equal(t, "text", sqliteColumnType(t, db, jsonColumn.table, jsonColumn.column))
	}
	for _, index := range []string{
		"idx_production_source_sets_project",
		"idx_production_source_sets_document_type",
		"idx_production_source_items_source_set",
		"idx_production_evidence_snapshots_source_item",
		"idx_production_documents_tenant",
		"idx_production_documents_project",
		"idx_production_document_versions_document",
		"idx_production_document_versions_source_set",
		"idx_production_document_blocks_version",
		"idx_production_block_lineage_from_version",
		"idx_production_block_lineage_to_version",
	} {
		require.NotEmpty(t, sqliteMasterSQL(t, db, "index", index))
	}
	for _, digestColumn := range []struct{ table, column string }{
		{"production_evidence_snapshots", "content_digest"},
		{"production_document_versions", "content_digest"},
		{"production_document_blocks", "content_digest"},
	} {
		require.Equal(t, "varchar(64)", sqliteColumnType(t, db, digestColumn.table, digestColumn.column))
	}

	rollbackDB := openProductionDocumentsSQLite(t)
	_, err = rollbackDB.Exec(mustReadMigration(t, "../../migrations/sqlite/000002_knowledge_production_documents.down.sql"))
	require.NoError(t, err)
	for _, table := range []string{
		"production_source_sets",
		"production_source_items",
		"production_evidence_snapshots",
		"production_documents",
		"production_document_versions",
		"production_document_blocks",
		"production_block_lineage",
	} {
		require.Empty(t, sqliteMasterSQL(t, rollbackDB, "table", table))
	}
}

func TestProductionDocumentsSQLiteMigrationGuardsRelationshipsAndReplacements(t *testing.T) {
	db := openProductionDocumentsSQLite(t)
	insertProductionDocumentType(t, db, "type-1", "baseline", 1, "active")
	_, err := db.Exec(`INSERT INTO production_document_types (id, tenant_id, code, name, schema_version, status, created_by) VALUES ('type-2', 2, 'baseline', 'Baseline', 1, 'active', 'owner-2')`)
	require.NoError(t, err)
	for _, project := range []struct {
		id       string
		tenantID int
	}{
		{"project-1", 1},
		{"project-2", 1},
		{"project-3", 2},
	} {
		_, err = db.Exec(`INSERT INTO production_projects (id, tenant_id, name, owner_user_id) VALUES (?, ?, 'Project', 'owner-1')`, project.id, project.tenantID)
		require.NoError(t, err)
	}

	_, err = db.Exec(`INSERT INTO production_source_sets (id, tenant_id, project_id, document_type_id, created_by) VALUES ('source-set-1', 1, 'project-1', 'type-1', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_source_sets (id, tenant_id, project_id, document_type_id, created_by) VALUES ('source-set-tenant-mismatch', 1, 'project-3', 'type-1', 'owner-1')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_source_sets (id, tenant_id, project_id, document_type_id, created_by) VALUES ('source-set-type-mismatch', 1, 'project-1', 'type-2', 'owner-1')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_source_sets (id, tenant_id, project_id, document_type_id, created_by) VALUES ('source-set-2', 1, 'project-2', 'type-1', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_source_sets (id, tenant_id, project_id, document_type_id, created_by) VALUES ('source-set-3', 2, 'project-3', 'type-2', 'owner-2')`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO production_documents (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, created_by) VALUES ('document-project-mismatch', 1, 'project-3', 'type-1', 1, 'Document', 'owner-1')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_documents (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, created_by) VALUES ('document-type-mismatch', 1, 'project-1', 'type-2', 1, 'Document', 'owner-1')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_documents (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, created_by) VALUES ('document-1', 1, 'project-1', 'type-1', 1, 'Document', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_documents (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, created_by) VALUES ('document-2', 1, 'project-2', 'type-1', 1, 'Document', 'owner-1')`)
	require.NoError(t, err)

	insertProductionVersion(t, db, "version-1", "document-1", 1, "project-1", 1, "source-set-1", "")
	insertProductionVersion(t, db, "version-2", "document-2", 1, "project-2", 1, "source-set-2", "")
	_, err = db.Exec(`INSERT INTO production_document_versions (id, document_id, tenant_id, project_id, version_number, source_set_id, origin, content_digest, created_by) VALUES ('version-cross-project', 'document-1', 1, 'project-1', 2, 'source-set-2', 'human', 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'owner-1')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_document_versions (id, document_id, tenant_id, project_id, version_number, source_set_id, origin, content_digest, created_by) VALUES ('version-cross-tenant', 'document-1', 1, 'project-1', 2, 'source-set-3', 'human', 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'owner-1')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_document_versions (id, document_id, tenant_id, project_id, version_number, source_set_id, origin, content_digest, created_by) VALUES ('version-document-context-mismatch', 'document-1', 1, 'project-2', 3, 'source-set-2', 'human', 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'owner-1')`)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE production_documents SET current_version_id = 'version-2' WHERE id = 'document-1'`)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE production_documents SET latest_approved_version_id = 'version-2' WHERE id = 'document-1'`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_document_versions (id, document_id, tenant_id, project_id, version_number, parent_version_id, source_set_id, origin, content_digest, created_by) VALUES ('version-cross-document-parent', 'document-1', 1, 'project-1', 2, 'version-2', 'source-set-1', 'human', 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'owner-1')`)
	require.Error(t, err)

	insertProductionBlock(t, db, "block-1", "version-1", "block-a")
	insertProductionBlock(t, db, "block-2", "version-2", "block-b")
	_, err = db.Exec(`INSERT INTO production_block_lineage (id, from_version_id, from_logical_block_id, to_version_id, to_logical_block_id, relation) VALUES ('lineage-1', 'version-1', 'block-a', 'version-2', 'block-b', 'same')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_block_lineage (id, from_version_id, from_logical_block_id, to_version_id, to_logical_block_id, relation) VALUES ('lineage-duplicate', 'version-1', 'block-a', 'version-2', 'block-b', 'same')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_block_lineage (id, from_version_id, from_logical_block_id, to_version_id, to_logical_block_id, relation) VALUES ('lineage-missing', 'missing', 'block-a', 'version-2', 'block-b', 'same')`)
	require.Error(t, err)

	_, err = db.Exec(`INSERT INTO production_source_items (id, source_set_id, source_kind, title, mime_type, content_digest, captured_at, metadata, status) VALUES ('source-item-1', 'source-set-1', 'manual', 'Source', 'text/plain', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', CURRENT_TIMESTAMP, '{}', 'accepted')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_evidence_snapshots (id, source_item_id, snapshot_type, inline_content, content_digest, redaction_metadata) VALUES ('evidence-1', 'source-item-1', 'text', '{"text":"evidence"}', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', '{}')`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_source_sets SET status = 'frozen' WHERE id = 'source-set-1'`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT OR REPLACE INTO production_source_sets (id, tenant_id, project_id, document_type_id, status, created_by) VALUES ('source-set-1', 1, 'project-1', 'type-1', 'collecting', 'owner-1')`)
	require.ErrorContains(t, err, "frozen source sets cannot be replaced")
	_, err = db.Exec(`DELETE FROM production_source_sets WHERE id = 'source-set-1'`)
	require.ErrorContains(t, err, "frozen source sets cannot be deleted")
	_, err = db.Exec(`DELETE FROM production_projects WHERE id = 'project-1'`)
	require.Error(t, err)

	_, err = db.Exec(`INSERT OR REPLACE INTO production_source_items (id, source_set_id, source_kind, title, mime_type, content_digest, captured_at, metadata, status) VALUES ('source-item-1', 'source-set-1', 'manual', 'Replaced', 'text/plain', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', CURRENT_TIMESTAMP, '{}', 'accepted')`)
	require.ErrorContains(t, err, "source items cannot be inserted into frozen source sets")
	_, err = db.Exec(`INSERT OR REPLACE INTO production_evidence_snapshots (id, source_item_id, snapshot_type, inline_content, content_digest, redaction_metadata) VALUES ('evidence-1', 'source-item-1', 'text', '{"text":"replacement"}', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', '{}')`)
	require.ErrorContains(t, err, "evidence snapshots are immutable")
	_, err = db.Exec(`INSERT OR REPLACE INTO production_document_versions (id, document_id, tenant_id, project_id, version_number, source_set_id, origin, content_digest, created_by) VALUES ('version-1', 'document-1', 1, 'project-1', 1, 'source-set-1', 'human', 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'owner-1')`)
	require.ErrorContains(t, err, "document versions are append-only")
	_, err = db.Exec(`INSERT OR REPLACE INTO production_document_versions (id, document_id, tenant_id, project_id, version_number, source_set_id, origin, content_digest, created_by) VALUES ('version-replacement', 'document-1', 1, 'project-1', 1, 'source-set-1', 'human', 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'owner-1')`)
	require.ErrorContains(t, err, "document versions are append-only")
	_, err = db.Exec(`INSERT OR REPLACE INTO production_document_blocks (id, version_id, logical_block_id, block_type, position, content, attributes, evidence_refs, ai_provenance, content_digest) VALUES ('block-1', 'version-1', 'block-a', 'paragraph', 1, '{"text":"replacement"}', '{}', '[]', '{}', 'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd')`)
	require.ErrorContains(t, err, "document blocks are append-only")
	_, err = db.Exec(`INSERT OR REPLACE INTO production_document_blocks (id, version_id, logical_block_id, block_type, position, content, attributes, evidence_refs, ai_provenance, content_digest) VALUES ('block-replacement', 'version-1', 'block-a', 'paragraph', 1, '{"text":"replacement"}', '{}', '[]', '{}', 'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd')`)
	require.ErrorContains(t, err, "document blocks are append-only")

	_, err = db.Exec(`INSERT INTO production_source_sets (id, tenant_id, project_id, document_type_id, created_by) VALUES ('source-set-cleanup', 1, 'project-2', 'type-1', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM production_source_sets WHERE id = 'source-set-cleanup'`)
	require.NoError(t, err)
}

func TestProductionDocumentsSQLiteEvidenceContentIsExclusive(t *testing.T) {
	db := openProductionDocumentsSQLite(t)
	insertProductionDocumentType(t, db, "type-1", "baseline", 1, "active")
	_, err := db.Exec(`INSERT INTO production_projects (id, tenant_id, name, owner_user_id) VALUES ('project-1', 1, 'Project', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_source_sets (id, tenant_id, project_id, document_type_id, created_by) VALUES ('source-set-1', 1, 'project-1', 'type-1', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_source_items (id, source_set_id, source_kind, title, mime_type, content_digest, captured_at, metadata) VALUES ('source-item-1', 'source-set-1', 'manual', 'Source', 'text/plain', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', CURRENT_TIMESTAMP, '{}')`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO production_evidence_snapshots (id, source_item_id, snapshot_type, content_digest, redaction_metadata) VALUES ('evidence-neither', 'source-item-1', 'text', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', '{}')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_evidence_snapshots (id, source_item_id, snapshot_type, storage_path, inline_content, content_digest, redaction_metadata) VALUES ('evidence-both', 'source-item-1', 'text', 'resource://aaaaaaaaaaaaaaaaaaaaaa', '"inline"', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', '{}')`)
	require.Error(t, err)
}

func TestProductionDocumentsSQLiteFrozenSetsRejectAllItemAndEvidenceMutations(t *testing.T) {
	db := openProductionDocumentsSQLite(t)
	insertProductionDocumentType(t, db, "type-1", "baseline", 1, "active")
	_, err := db.Exec(`INSERT INTO production_projects (id, tenant_id, name, owner_user_id) VALUES ('project-1', 1, 'Project', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_source_sets (id, tenant_id, project_id, document_type_id, created_by) VALUES ('frozen-set', 1, 'project-1', 'type-1', 'owner-1'), ('collecting-set', 1, 'project-1', 'type-1', 'owner-1')`)
	require.NoError(t, err)
	for _, row := range []struct{ id, setID, status string }{
		{"frozen-candidate", "frozen-set", "candidate"},
		{"frozen-rejected", "frozen-set", "rejected"},
		{"frozen-accepted", "frozen-set", "accepted"},
		{"collecting-candidate", "collecting-set", "candidate"},
	} {
		_, err = db.Exec(`INSERT INTO production_source_items (id, source_set_id, source_kind, title, mime_type, content_digest, captured_at, metadata, status) VALUES (?, ?, 'manual', 'Source', 'text/plain', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', CURRENT_TIMESTAMP, '{}', ?)`, row.id, row.setID, row.status)
		require.NoError(t, err)
	}
	_, err = db.Exec(`UPDATE production_source_sets SET status = 'frozen', frozen_at = CURRENT_TIMESTAMP WHERE id = 'frozen-set'`)
	require.NoError(t, err)

	for _, status := range []string{"candidate", "rejected", "accepted"} {
		_, err = db.Exec(`INSERT INTO production_source_items (id, source_set_id, source_kind, title, mime_type, content_digest, captured_at, metadata, status) VALUES (?, 'frozen-set', 'manual', 'Late', 'text/plain', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', CURRENT_TIMESTAMP, '{}', ?)`, "late-"+status, status)
		require.ErrorContains(t, err, "source items cannot be inserted into frozen source sets")
	}
	for _, itemID := range []string{"frozen-candidate", "frozen-rejected", "frozen-accepted"} {
		_, err = db.Exec(`UPDATE production_source_items SET title = 'Changed' WHERE id = ?`, itemID)
		require.ErrorContains(t, err, "source items in frozen source sets are immutable")
		_, err = db.Exec(`DELETE FROM production_source_items WHERE id = ?`, itemID)
		require.ErrorContains(t, err, "source items in frozen source sets are immutable")
	}
	_, err = db.Exec(`UPDATE production_source_items SET source_set_id = 'collecting-set' WHERE id = 'frozen-candidate'`)
	require.ErrorContains(t, err, "source items in frozen source sets are immutable")
	_, err = db.Exec(`UPDATE production_source_items SET source_set_id = 'frozen-set' WHERE id = 'collecting-candidate'`)
	require.ErrorContains(t, err, "source items in frozen source sets are immutable")
	_, err = db.Exec(`INSERT INTO production_evidence_snapshots (id, source_item_id, snapshot_type, inline_content, content_digest, redaction_metadata) VALUES ('late-evidence', 'frozen-candidate', 'text', '"late"', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', '{}')`)
	require.ErrorContains(t, err, "evidence cannot be inserted into frozen source sets")
}

func TestProductionDocumentsSQLiteLineageRequiresRealImmutableEndpoints(t *testing.T) {
	db := openProductionDocumentsSQLite(t)
	insertProductionDocumentType(t, db, "type-1", "baseline", 1, "active")
	_, err := db.Exec(`INSERT INTO production_projects (id, tenant_id, name, owner_user_id) VALUES ('project-1', 1, 'Project', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_source_sets (id, tenant_id, project_id, document_type_id, created_by) VALUES ('source-set-1', 1, 'project-1', 'type-1', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_documents (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, created_by) VALUES ('document-1', 1, 'project-1', 'type-1', 1, 'Document', 'owner-1')`)
	require.NoError(t, err)
	insertProductionVersion(t, db, "version-1", "document-1", 1, "project-1", 1, "source-set-1", "")
	insertProductionVersion(t, db, "version-2", "document-1", 1, "project-1", 2, "source-set-1", "version-1")
	insertProductionBlock(t, db, "block-1", "version-1", "block-a")
	insertProductionBlock(t, db, "block-2", "version-2", "block-b")

	_, err = db.Exec(`INSERT INTO production_block_lineage (id, from_version_id, from_logical_block_id, to_version_id, to_logical_block_id, relation) VALUES ('missing-from', 'version-1', 'missing', 'version-2', 'block-b', 'same')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_block_lineage (id, from_version_id, from_logical_block_id, to_version_id, to_logical_block_id, relation) VALUES ('missing-to', 'version-1', 'block-a', 'version-2', 'missing', 'same')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_block_lineage (id, from_version_id, from_logical_block_id, to_version_id, to_logical_block_id, relation) VALUES ('bad-relation', 'version-1', 'block-a', 'version-2', 'block-b', 'copied')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_block_lineage (id, from_version_id, from_logical_block_id, to_version_id, to_logical_block_id, relation) VALUES ('lineage-1', 'version-1', 'block-a', 'version-2', 'block-b', 'same')`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_block_lineage SET relation = 'split' WHERE id = 'lineage-1'`)
	require.ErrorContains(t, err, "block lineage is append-only")
	_, err = db.Exec(`DELETE FROM production_block_lineage WHERE id = 'lineage-1'`)
	require.ErrorContains(t, err, "block lineage is append-only")
	_, err = db.Exec(`INSERT OR REPLACE INTO production_block_lineage (id, from_version_id, from_logical_block_id, to_version_id, to_logical_block_id, relation) VALUES ('lineage-1', 'version-1', 'block-a', 'version-2', 'block-b', 'same')`)
	require.ErrorContains(t, err, "block lineage is append-only")
}

func TestProductionDocumentsSQLiteMigrationRollsBackPopulatedSchema(t *testing.T) {
	db := openProductionDocumentsSQLite(t)
	insertProductionDocumentType(t, db, "type-1", "baseline", 1, "active")
	_, err := db.Exec(`INSERT INTO production_projects (id, tenant_id, name, owner_user_id) VALUES ('project-1', 1, 'Project', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_source_sets (id, tenant_id, project_id, document_type_id, created_by) VALUES ('source-set-1', 1, 'project-1', 'type-1', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_source_items (id, source_set_id, source_kind, title, mime_type, content_digest, captured_at, metadata) VALUES ('source-item-1', 'source-set-1', 'manual', 'Source', 'text/plain', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', CURRENT_TIMESTAMP, '{}')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_evidence_snapshots (id, source_item_id, snapshot_type, inline_content, content_digest, redaction_metadata) VALUES ('evidence-1', 'source-item-1', 'text', '{"text":"evidence"}', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', '{}')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_documents (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, created_by) VALUES ('document-1', 1, 'project-1', 'type-1', 1, 'Document', 'owner-1')`)
	require.NoError(t, err)
	insertProductionVersion(t, db, "version-1", "document-1", 1, "project-1", 1, "source-set-1", "")
	insertProductionVersion(t, db, "version-2", "document-1", 1, "project-1", 2, "source-set-1", "version-1")
	insertProductionBlock(t, db, "block-1", "version-1", "block-a")
	insertProductionBlock(t, db, "block-2", "version-2", "block-b")
	_, err = db.Exec(`INSERT INTO production_block_lineage (id, from_version_id, from_logical_block_id, to_version_id, to_logical_block_id, relation) VALUES ('lineage-1', 'version-1', 'block-a', 'version-2', 'block-b', 'same')`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_documents SET current_version_id = 'version-2', latest_approved_version_id = 'version-2' WHERE id = 'document-1'`)
	require.NoError(t, err)

	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000002_knowledge_production_documents.down.sql"))
	require.NoError(t, err)
	for _, table := range []string{
		"production_source_sets",
		"production_source_items",
		"production_evidence_snapshots",
		"production_documents",
		"production_document_versions",
		"production_document_blocks",
		"production_block_lineage",
	} {
		require.Empty(t, sqliteMasterSQL(t, db, "table", table))
	}
}

func openProductionFoundationSQLite(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000001_knowledge_production_foundation.up.sql"))
	require.NoError(t, err)
	return db
}

func openProductionDocumentsSQLite(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000001_knowledge_production_foundation.up.sql"))
	require.NoError(t, err)
	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000002_knowledge_production_documents.up.sql"))
	require.NoError(t, err)
	return db
}

func openProductionRunsSQLite(t *testing.T) *sql.DB {
	t.Helper()

	db := openProductionDocumentsSQLite(t)
	_, err := db.Exec(mustReadMigration(t, "../../migrations/sqlite/000003_knowledge_production_runs.up.sql"))
	require.NoError(t, err)
	return db
}

func seedProductionRunScopes(t *testing.T, db *sql.DB) {
	t.Helper()

	insertProductionDocumentType(t, db, "type-1", "baseline", 1, "active")
	_, err := db.Exec(`INSERT INTO production_projects (id, tenant_id, name, owner_user_id) VALUES ('project-1', 1, 'Project 1', 'owner-1'), ('project-2', 1, 'Project 2', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_source_sets (id, tenant_id, project_id, document_type_id, created_by) VALUES ('source-set-1', 1, 'project-1', 'type-1', 'owner-1'), ('source-set-2', 1, 'project-2', 'type-1', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_documents (id, tenant_id, project_id, document_type_id, document_type_schema_version, title, created_by) VALUES ('document-1', 1, 'project-1', 'type-1', 1, 'Document 1', 'owner-1'), ('document-2', 1, 'project-2', 'type-1', 1, 'Document 2', 'owner-1')`)
	require.NoError(t, err)
	insertProductionVersion(t, db, "version-1", "document-1", 1, "project-1", 1, "source-set-1", "")
	insertProductionVersion(t, db, "version-2", "document-2", 1, "project-2", 1, "source-set-2", "")
	_, err = db.Exec(`INSERT INTO production_source_items (id, source_set_id, source_kind, title, mime_type, content_digest, captured_at, metadata, status) VALUES ('source-item-1', 'source-set-1', 'manual', 'Source 1', 'application/json', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', CURRENT_TIMESTAMP, '{}', 'accepted'), ('source-item-2', 'source-set-2', 'manual', 'Source 2', 'application/json', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', CURRENT_TIMESTAMP, '{}', 'accepted')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_evidence_snapshots (id, source_item_id, snapshot_type, inline_content, content_digest, redaction_metadata) VALUES ('evidence-1', 'source-item-1', 'tool_result', '{"ok":true}', 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', '{}'), ('evidence-2', 'source-item-2', 'tool_result', '{"ok":true}', 'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd', '{}')`)
	require.NoError(t, err)
}

type productionToolCallIdentityMutation struct {
	name       string
	assignment string
}

func productionToolCallIdentityMutations() []productionToolCallIdentityMutation {
	return []productionToolCallIdentityMutation{
		{name: "run id", assignment: "run_id = 'run-other'"},
		{name: "tenant id", assignment: "tenant_id = 2"},
		{name: "project id", assignment: "project_id = 'project-2'"},
		{name: "document id", assignment: "document_id = 'document-2'"},
		{name: "source set id", assignment: "source_set_id = 'source-set-2'"},
		{name: "provider type", assignment: "provider_type = 'mcp'"},
		{name: "provider id", assignment: "provider_id = 'changed-provider'"},
		{name: "tool name", assignment: "tool_name = 'changed-tool'"},
		{name: "request snapshot", assignment: `request_snapshot = '{"changed":true}'`},
		{name: "request digest", assignment: "request_digest = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'"},
		{name: "attempt", assignment: "attempt = 1"},
		{name: "current step", assignment: "current_step = 1"},
		{name: "idempotency key", assignment: "idempotency_key = 'changed-key'"},
	}
}

func setProductionToolCallStatus(t *testing.T, db *sql.DB, callID, status string) {
	t.Helper()

	var statements []string
	switch status {
	case "planned":
		return
	case "pending_approval":
		statements = []string{`UPDATE production_tool_calls SET status = 'pending_approval', approval_status = 'pending', approval_requested_at = CURRENT_TIMESTAMP WHERE id = ?`}
	case "approved":
		statements = []string{
			`UPDATE production_tool_calls SET status = 'pending_approval', approval_status = 'pending', approval_requested_at = CURRENT_TIMESTAMP WHERE id = ?`,
			`UPDATE production_tool_calls SET status = 'approved', approval_status = 'approved', approved_by = 'reviewer-1', approved_at = CURRENT_TIMESTAMP WHERE id = ?`,
		}
	case "executing":
		statements = []string{`UPDATE production_tool_calls SET status = 'executing', started_at = CURRENT_TIMESTAMP WHERE id = ?`}
	case "completed":
		statements = []string{`UPDATE production_tool_calls SET status = 'completed', response_snapshot = '{"ok":true}', response_digest = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', response_evidence_id = 'evidence-1', response_evidence_source_item_id = 'source-item-1', completed_at = CURRENT_TIMESTAMP WHERE id = ?`}
	default:
		t.Fatalf("unsupported production tool call status %s", status)
	}
	for _, statement := range statements {
		_, err := db.Exec(statement, callID)
		require.NoError(t, err)
	}
}

func insertProductionRun(t *testing.T, db *sql.DB, id string, tenantID int, projectID, documentID, sourceSetID, inputVersionID, idempotencyKey string) {
	t.Helper()

	_, err := db.Exec(
		`INSERT INTO production_runs (id, tenant_id, project_id, document_id, source_set_id, run_type, model_id, document_type_snapshot, input_version_id, idempotency_key) VALUES (?, ?, ?, ?, ?, 'write', 'model-1', '{}', NULLIF(?, ''), ?)`,
		id, tenantID, projectID, documentID, sourceSetID, inputVersionID, idempotencyKey,
	)
	require.NoError(t, err)
}

func insertProductionToolCall(t *testing.T, db *sql.DB, id, runID, idempotencyKey string, attempt, currentStep int) {
	t.Helper()

	_, err := db.Exec(
		`INSERT INTO production_tool_calls (id, run_id, tenant_id, project_id, document_id, source_set_id, attempt, current_step, idempotency_key, provider_type, provider_id, tool_name, request_snapshot, request_digest) VALUES (?, ?, 1, 'project-1', 'document-1', 'source-set-1', ?, ?, ?, 'skill', 'writer', 'collect', '{}', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`,
		id, runID, attempt, currentStep, idempotencyKey,
	)
	require.NoError(t, err)
}

func insertProductionDocumentType(t *testing.T, db *sql.DB, id, code string, schemaVersion int, status string) {
	t.Helper()

	_, err := db.Exec(
		`INSERT INTO production_document_types (id, tenant_id, code, name, schema_version, status, created_by) VALUES (?, 1, ?, 'Baseline', ?, ?, 'owner-1')`,
		id,
		code,
		schemaVersion,
		status,
	)
	require.NoError(t, err)
}

func insertProductionVersion(t *testing.T, db *sql.DB, id, documentID string, tenantID int, projectID string, versionNumber int, sourceSetID, parentVersionID string) {
	t.Helper()

	_, err := db.Exec(
		`INSERT INTO production_document_versions (id, document_id, tenant_id, project_id, version_number, parent_version_id, source_set_id, origin, content_digest, created_by) VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, 'human', 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'owner-1')`,
		id,
		documentID,
		tenantID,
		projectID,
		versionNumber,
		parentVersionID,
		sourceSetID,
	)
	require.NoError(t, err)
}

func insertProductionBlock(t *testing.T, db *sql.DB, id, versionID, logicalBlockID string) {
	t.Helper()

	_, err := db.Exec(
		`INSERT INTO production_document_blocks (id, version_id, logical_block_id, block_type, position, content, attributes, evidence_refs, ai_provenance, content_digest) VALUES (?, ?, ?, 'paragraph', 1, '{"text":"block"}', '{}', '[]', '{}', 'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd')`,
		id,
		versionID,
		logicalBlockID,
	)
	require.NoError(t, err)
}

func sqliteColumnType(t *testing.T, db *sql.DB, table, column string) string {
	t.Helper()

	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		require.NoError(t, rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey))
		if name == column {
			return strings.ToLower(columnType)
		}
	}
	require.NoError(t, rows.Err())
	t.Fatalf("column %s not found on table %s", column, table)
	return ""
}

func sqliteMasterSQL(t *testing.T, db *sql.DB, objectType, name string) string {
	t.Helper()

	var statement sql.NullString
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = ? AND name = ?`, objectType, name).Scan(&statement)
	if err == sql.ErrNoRows {
		return ""
	}
	require.NoError(t, err)
	return statement.String
}
