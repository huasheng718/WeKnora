package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

func TestPostgreSQLMigrationVersionsAreUnique(t *testing.T) {
	entries, err := os.ReadDir("../../migrations/versioned")
	require.NoError(t, err)

	versions := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		version, _, found := strings.Cut(entry.Name(), "_")
		require.True(t, found, entry.Name())
		if previous, exists := versions[version]; exists {
			t.Fatalf("PostgreSQL migration version %s is used by both %s and %s", version, previous, entry.Name())
		}
		versions[version] = entry.Name()
	}
}

func TestSQLiteTemporaryDocumentsIncrementalMigrationUpAndDown(t *testing.T) {
	db, err := sql.Open("sqlite3", t.TempDir()+"/temporary-documents.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000011_temporary_documents.up.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, sqliteMasterSQL(t, db, "table", "temporary_documents"))
	require.NotEmpty(t, sqliteMasterSQL(t, db, "index", "idx_temporary_documents_scope"))

	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000011_temporary_documents.down.sql"))
	require.NoError(t, err)
	require.Empty(t, sqliteMasterSQL(t, db, "table", "temporary_documents"))
}

func TestProductionBuiltinDocumentTypeMigrationSQLiteBackfillsAndRollsBack(t *testing.T) {
	db := openSQLiteThroughProductionMigrationFive(t)
	for _, tenant := range []string{"Tenant A", "Tenant B"} {
		_, err := db.Exec(`INSERT INTO tenants (name, retriever_engines, business) VALUES (?, '[]', 'qa')`, tenant)
		require.NoError(t, err)
	}
	_, err := db.Exec(`INSERT INTO production_document_types
		(id, tenant_id, code, name, description, schema_version, status, created_by)
		VALUES ('custom-faq', 2, 'faq', 'Tenant FAQ', 'keep me', 1, 'active', 'owner-2')`)
	require.NoError(t, err)

	up := mustReadMigration(t, "../../migrations/sqlite/000012_production_builtin_document_types.up.sql")
	_, err = db.Exec(up)
	require.NoError(t, err)
	seedStart := strings.Index(up, "WITH builtin_definitions")
	require.GreaterOrEqual(t, seedStart, 0)
	_, err = db.Exec(up[seedStart:])
	require.NoError(t, err)

	for tenantID, builtinCount := range map[int]int{1: 5, 2: 4} {
		var got int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM production_document_types WHERE tenant_id = ? AND origin = 'builtin' AND deleted_at IS NULL`, tenantID).Scan(&got))
		require.Equal(t, builtinCount, got)
	}
	var total int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM production_document_types WHERE tenant_id = 2 AND deleted_at IS NULL`).Scan(&total))
	require.Equal(t, 5, total)
	var customName, customDescription, customOrigin string
	var customTemplate sql.NullString
	require.NoError(t, db.QueryRow(`SELECT name, description, origin, template_key FROM production_document_types WHERE id = 'custom-faq'`).Scan(&customName, &customDescription, &customOrigin, &customTemplate))
	require.Equal(t, "Tenant FAQ", customName)
	require.Equal(t, "keep me", customDescription)
	require.Equal(t, "custom", customOrigin)
	require.False(t, customTemplate.Valid)
	_, err = db.Exec(`INSERT INTO production_document_types
		(id, tenant_id, code, name, schema_version, status, origin, created_by)
		VALUES ('invalid-builtin', 1, 'invalid-builtin', 'Invalid', 1, 'draft', 'builtin', 'owner-1')`)
	require.Error(t, err, "built-in document types must require a non-null governed template key")

	_, err = db.Exec(`UPDATE production_document_types SET origin = 'custom' WHERE tenant_id = 1 AND code = 'sop'`)
	require.ErrorContains(t, err, "immutable")
	_, err = db.Exec(`UPDATE production_document_types SET template_key = 'changed' WHERE tenant_id = 1 AND code = 'sop'`)
	require.ErrorContains(t, err, "immutable")
	require.NotEmpty(t, sqliteMasterSQL(t, db, "index", "uq_production_document_types_live_template_version"))

	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000012_production_builtin_document_types.down.sql"))
	require.NoError(t, err)
	require.Empty(t, sqliteColumnTypeIfPresent(t, db, "production_document_types", "origin"))
	require.Empty(t, sqliteColumnTypeIfPresent(t, db, "production_document_types", "template_key"))
	require.Empty(t, sqliteMasterSQL(t, db, "index", "uq_production_document_types_live_template_version"))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM production_document_types`).Scan(&total))
	require.Equal(t, 1, total)
}

func TestProductionBuiltinDocumentTypeMigrationSQLiteReferencedDownAbortsWithoutForeignKeys(t *testing.T) {
	db := openSQLiteThroughProductionMigrationFive(t)
	_, err := db.Exec(`INSERT INTO tenants (name, retriever_engines, business) VALUES ('Tenant A', '[]', 'qa')`)
	require.NoError(t, err)
	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000012_production_builtin_document_types.up.sql"))
	require.NoError(t, err)

	var foreignKeys int
	require.NoError(t, db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys))
	require.Zero(t, foreignKeys, "regression must cover the production migration connection without FK enforcement")
	var sopID string
	require.NoError(t, db.QueryRow(`SELECT id FROM production_document_types WHERE tenant_id = 1 AND code = 'sop'`).Scan(&sopID))
	_, err = db.Exec(`INSERT INTO production_projects (id, tenant_id, name, owner_user_id) VALUES ('project-guard', 1, 'Guard', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_source_sets
		(id, tenant_id, project_id, document_type_id, created_by)
		VALUES ('source-set-guard', 1, 'project-guard', ?, 'owner-1')`, sopID)
	require.NoError(t, err)

	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000012_production_builtin_document_types.down.sql"))
	require.ErrorContains(t, err, "referenced built-in production document types prevent rollback")
	require.Equal(t, "varchar(16)", sqliteColumnType(t, db, "production_document_types", "origin"))
	var parentCount, childCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM production_document_types WHERE id = ?`, sopID).Scan(&parentCount))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM production_source_sets WHERE id = 'source-set-guard'`).Scan(&childCount))
	require.Equal(t, 1, parentCount)
	require.Equal(t, 1, childCount)
}

func TestProductionBuiltinDocumentTypeMigrationPostgreSQLParity(t *testing.T) {
	up := mustReadMigration(t, "../../migrations/versioned/000076_production_builtin_document_types.up.sql")
	down := mustReadMigration(t, "../../migrations/versioned/000076_production_builtin_document_types.down.sql")
	_, err := pg_query.Parse(up)
	require.NoError(t, err)
	_, err = pg_query.Parse(down)
	require.NoError(t, err)
	for _, fragment := range []string{
		"ADD COLUMN origin", "ADD COLUMN template_key", "chk_production_document_types_origin",
		"chk_production_document_types_builtin_template_key", "uq_production_document_types_live_template_version",
		"origin <> 'builtin' OR (template_key IS NOT NULL AND template_key IN (",
		"uuid_generate_v4()::varchar(36)", "system:builtin-document-types",
		"NEW.origin IS DISTINCT FROM OLD.origin", "NEW.template_key IS DISTINCT FROM OLD.template_key",
		"sop", "policy_process", "product_service_guide", "faq", "incident_playbook",
	} {
		require.Contains(t, up, fragment)
	}
	deleteAt := strings.Index(down, "DELETE FROM production_document_types")
	dropColumnsAt := strings.Index(down, "DROP COLUMN origin")
	require.GreaterOrEqual(t, deleteAt, 0)
	require.Greater(t, dropColumnsAt, deleteAt)
	require.Contains(t, down, "DROP INDEX IF EXISTS uq_production_document_types_live_template_version")
	require.Contains(t, down, "CREATE OR REPLACE FUNCTION prevent_active_production_document_type_definition_update")
}

var productionSQLiteFoundationMigrationNames = []string{
	"init",
	"knowledge_production_foundation",
	"knowledge_production_documents",
	"knowledge_production_runs",
	"knowledge_production_reviews",
	"knowledge_production_publication",
}

func productionSQLiteFoundationMigrationPaths() []string {
	paths := make([]string, 0, len(productionSQLiteFoundationMigrationNames))
	for version, name := range productionSQLiteFoundationMigrationNames {
		paths = append(paths, fmt.Sprintf("../../migrations/sqlite/%06d_%s.up.sql", version, name))
	}
	return paths
}

func openSQLiteThroughProductionMigrationFive(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", t.TempDir()+"/runtime-compat.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	for _, migration := range productionSQLiteFoundationMigrationPaths() {
		_, err = db.Exec(mustReadMigration(t, migration))
		require.NoError(t, err, migration)
	}
	return db
}

func openSQLiteThroughProductionMigrationEight(t *testing.T) *sql.DB {
	t.Helper()

	db := openSQLiteThroughProductionMigrationFive(t)
	for _, migration := range []string{
		"../../migrations/sqlite/000006_tenant_api_principal_config.up.sql",
		"../../migrations/sqlite/000007_system_admin_and_settings.up.sql",
		"../../migrations/sqlite/000008_knowledge_base_processing_config.up.sql",
	} {
		_, err := db.Exec(mustReadMigration(t, migration))
		require.NoError(t, err, migration)
	}
	return db
}

func TestSQLiteTenantAPIPrincipalConfigMigrationUpAndDown(t *testing.T) {
	db := openSQLiteThroughProductionMigrationFive(t)
	require.Empty(t, sqliteColumnTypeIfPresent(t, db, "tenants", "api_principal_config"))

	_, err := db.Exec(mustReadMigration(t, "../../migrations/sqlite/000006_tenant_api_principal_config.up.sql"))
	require.NoError(t, err)
	require.Equal(t, "text", sqliteColumnType(t, db, "tenants", "api_principal_config"))
	_, err = db.Exec(`INSERT INTO tenants (name, retriever_engines, business, api_principal_config) VALUES ('QA', '[]', '', '{}')`)
	require.NoError(t, err)

	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000006_tenant_api_principal_config.down.sql"))
	require.NoError(t, err)
	require.Empty(t, sqliteColumnTypeIfPresent(t, db, "tenants", "api_principal_config"))
}

func TestSQLiteSystemAdminAndSettingsMigrationUpAndDown(t *testing.T) {
	db := openSQLiteThroughProductionMigrationFive(t)
	require.Empty(t, sqliteColumnTypeIfPresent(t, db, "users", "is_system_admin"))
	require.Empty(t, sqliteMasterSQL(t, db, "table", "system_settings"))

	_, err := db.Exec(mustReadMigration(t, "../../migrations/sqlite/000007_system_admin_and_settings.up.sql"))
	require.NoError(t, err)
	require.Equal(t, "boolean", sqliteColumnType(t, db, "users", "is_system_admin"))
	require.NotEmpty(t, sqliteMasterSQL(t, db, "index", "idx_users_is_system_admin"))
	require.NotEmpty(t, sqliteMasterSQL(t, db, "table", "system_settings"))
	require.NotEmpty(t, sqliteMasterSQL(t, db, "index", "idx_system_settings_category"))
	_, err = db.Exec(`INSERT INTO users (id, username, email, password_hash, is_system_admin) VALUES ('user-1', 'qa', 'qa@example.test', 'hash', 0)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO system_settings (key, value, value_type, category) VALUES ('auth.registration_mode', '"self_serve"', 'string', 'auth')`)
	require.NoError(t, err)

	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000007_system_admin_and_settings.down.sql"))
	require.NoError(t, err)
	require.Empty(t, sqliteMasterSQL(t, db, "table", "system_settings"))
	require.Empty(t, sqliteColumnTypeIfPresent(t, db, "users", "is_system_admin"))
}

func TestSQLiteKnowledgeBaseProcessingConfigMigrationAndSchemaParity(t *testing.T) {
	db := openSQLiteThroughProductionMigrationFive(t)
	for _, migration := range []string{
		"../../migrations/sqlite/000006_tenant_api_principal_config.up.sql",
		"../../migrations/sqlite/000007_system_admin_and_settings.up.sql",
	} {
		_, err := db.Exec(mustReadMigration(t, migration))
		require.NoError(t, err, migration)
	}
	require.Empty(t, sqliteColumnTypeIfPresent(t, db, "knowledge_bases", "wiki_config"))
	require.Empty(t, sqliteColumnTypeIfPresent(t, db, "knowledge_bases", "indexing_strategy"))

	_, err := db.Exec(mustReadMigration(t, "../../migrations/sqlite/000008_knowledge_base_processing_config.up.sql"))
	require.NoError(t, err)
	for _, column := range []string{
		"id", "name", "type", "is_temporary", "description", "tenant_id", "creator_id",
		"chunking_config", "image_processing_config", "embedding_model_id", "summary_model_id",
		"vlm_config", "asr_config", "storage_provider_config", "storage_backend_id", "cos_config",
		"vector_store_id", "extract_config", "faq_config", "question_generation_config", "wiki_config",
		"indexing_strategy", "created_at", "updated_at", "deleted_at",
	} {
		require.NotEmpty(t, sqliteColumnTypeIfPresent(t, db, "knowledge_bases", column), column)
	}
	_, err = db.Exec(`INSERT INTO knowledge_bases (id, name, tenant_id, embedding_model_id, summary_model_id) VALUES ('kb-1', 'QA', 1, '', '')`)
	require.NoError(t, err)
	var strategy string
	require.NoError(t, db.QueryRow(`SELECT indexing_strategy FROM knowledge_bases WHERE id = 'kb-1'`).Scan(&strategy))
	require.JSONEq(t, `{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":false,"graph_enabled":false}`, strategy)

	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000008_knowledge_base_processing_config.down.sql"))
	require.NoError(t, err)
	require.Empty(t, sqliteColumnTypeIfPresent(t, db, "knowledge_bases", "wiki_config"))
	require.Empty(t, sqliteColumnTypeIfPresent(t, db, "knowledge_bases", "indexing_strategy"))
}

func TestSQLiteTaskQueueAndDeadLettersMigrationUpAndDown(t *testing.T) {
	db := openSQLiteThroughProductionMigrationEight(t)
	require.Empty(t, sqliteMasterSQL(t, db, "table", "task_pending_ops"))
	require.Empty(t, sqliteMasterSQL(t, db, "table", "task_dead_letters"))

	_, err := db.Exec(mustReadMigration(t, "../../migrations/sqlite/000009_task_queue_and_dead_letters.up.sql"))
	require.NoError(t, err)
	for _, column := range []string{
		"id", "tenant_id", "task_type", "scope", "scope_id", "op", "dedup_key",
		"payload", "fail_count", "enqueued_at", "claimed_at",
	} {
		require.NotEmpty(t, sqliteColumnTypeIfPresent(t, db, "task_pending_ops", column), column)
	}
	for _, column := range []string{
		"id", "tenant_id", "task_type", "scope", "scope_id", "related_id",
		"payload", "last_error", "fail_count", "failed_at",
	} {
		require.NotEmpty(t, sqliteColumnTypeIfPresent(t, db, "task_dead_letters", column), column)
	}
	for _, index := range []string{
		"idx_task_pending_ops_scope",
		"idx_task_pending_ops_tenant",
		"idx_task_dead_letters_scope",
		"idx_task_dead_letters_tenant",
		"idx_task_dead_letters_task_type",
	} {
		require.NotEmpty(t, sqliteMasterSQL(t, db, "index", index), index)
	}

	_, err = db.Exec(`INSERT INTO task_pending_ops
		(tenant_id, task_type, scope, scope_id, op, dedup_key, payload)
		VALUES (1, 'wiki:ingest', 'knowledge_base', 'kb-1', 'ingest', 'doc-1', '{}')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO task_dead_letters
		(tenant_id, task_type, scope, scope_id, related_id, payload, fail_count)
		VALUES (1, 'wiki:ingest', 'knowledge_base', 'kb-1', 'doc-1', '{}', 3)`)
	require.NoError(t, err)

	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000009_task_queue_and_dead_letters.down.sql"))
	require.NoError(t, err)
	require.Empty(t, sqliteMasterSQL(t, db, "table", "task_pending_ops"))
	require.Empty(t, sqliteMasterSQL(t, db, "table", "task_dead_letters"))
}

func TestSQLiteKnowledgePendingSubtasksMigrationUpAndDown(t *testing.T) {
	db := openSQLiteThroughProductionMigrationEight(t)
	_, err := db.Exec(mustReadMigration(t, "../../migrations/sqlite/000009_task_queue_and_dead_letters.up.sql"))
	require.NoError(t, err)
	require.Empty(t, sqliteColumnTypeIfPresent(t, db, "knowledges", "pending_subtasks_count"))

	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000010_knowledge_pending_subtasks.up.sql"))
	require.NoError(t, err)
	require.Equal(t, "integer", sqliteColumnType(t, db, "knowledges", "pending_subtasks_count"))
	_, err = db.Exec(`INSERT INTO knowledges
		(id, tenant_id, knowledge_base_id, type, title, source)
		VALUES ('knowledge-1', 1, 'kb-1', 'file', 'QA', '/tmp/qa')`)
	require.NoError(t, err)
	var pendingSubtasks int
	require.NoError(t, db.QueryRow(`SELECT pending_subtasks_count FROM knowledges WHERE id = 'knowledge-1'`).Scan(&pendingSubtasks))
	require.Zero(t, pendingSubtasks)

	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000010_knowledge_pending_subtasks.down.sql"))
	require.NoError(t, err)
	require.Empty(t, sqliteColumnTypeIfPresent(t, db, "knowledges", "pending_subtasks_count"))
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

func TestProductionReviewMigrationsDeclareRequiredTables(t *testing.T) {
	postgres := mustReadMigration(t, "../../migrations/versioned/000073_knowledge_production_reviews.up.sql")
	sqlite := mustReadMigration(t, "../../migrations/sqlite/000004_knowledge_production_reviews.up.sql")

	for _, table := range []string{
		"production_annotations",
		"production_review_requests",
		"production_review_steps",
	} {
		require.Contains(t, postgres, "CREATE TABLE IF NOT EXISTS "+table)
		require.Contains(t, sqlite, "CREATE TABLE IF NOT EXISTS "+table)
	}
}

func TestProductionReviewMigrationsProvideBlockingCountIndex(t *testing.T) {
	const indexName = "idx_production_annotations_blocking_count"
	const declaration = "CREATE INDEX IF NOT EXISTS " + indexName + "\n    ON production_annotations (tenant_id, version_id, severity, status);"
	for _, path := range []string{
		"../../migrations/versioned/000073_knowledge_production_reviews.up.sql",
		"../../migrations/sqlite/000004_knowledge_production_reviews.up.sql",
	} {
		require.Contains(t, mustReadMigration(t, path), declaration)
	}
	for _, path := range []string{
		"../../migrations/versioned/000073_knowledge_production_reviews.down.sql",
		"../../migrations/sqlite/000004_knowledge_production_reviews.down.sql",
	} {
		require.Contains(t, mustReadMigration(t, path), "DROP INDEX IF EXISTS "+indexName+";")
	}

	db := openProductionReviewSQLite(t)
	indexSQL := sqliteMasterSQL(t, db, "index", indexName)
	require.Contains(t, indexSQL, "tenant_id, version_id, severity, status")
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT count(*) FROM production_annotations
		WHERE tenant_id = ? AND version_id = ? AND severity = ? AND status = ?`,
		1, "version-1", "blocking", "open")
	require.NoError(t, err)
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
	}
	require.NoError(t, rows.Err())
	require.Contains(t, strings.Join(plan, "\n"), indexName)
}

func TestProductionReviewPostgreSQLMigrationDeclaresIntegrityGuards(t *testing.T) {
	up := mustReadMigration(t, "../../migrations/versioned/000073_knowledge_production_reviews.up.sql")
	_, err := pg_query.Parse(up)
	require.NoError(t, err)

	for _, declaration := range []string{
		"policy_snapshot JSONB NOT NULL",
		"policy_digest VARCHAR(64) NOT NULL",
		"status IN ('pending', 'approved', 'rejected', 'obsolete', 'cancelled', 'changes_requested')",
		"decision IN ('pending', 'approved', 'changes_requested', 'rejected', 'cancelled')",
		"FOREIGN KEY (document_id, tenant_id, project_id)\n        REFERENCES production_documents(id, tenant_id, project_id)",
		"FOREIGN KEY (version_id, document_id, tenant_id, project_id)\n        REFERENCES production_document_versions(id, document_id, tenant_id, project_id)",
		"FOREIGN KEY (block_id, version_id)\n        REFERENCES production_document_blocks(id, version_id)",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_production_review_requests_version",
		"CREATE INDEX IF NOT EXISTS idx_production_review_requests_document_version_status",
		"CREATE INDEX IF NOT EXISTS idx_production_review_steps_request_sequence",
		"CREATE INDEX IF NOT EXISTS idx_production_review_steps_request_role_decision",
		"CREATE TRIGGER trg_production_review_requests_guard_identity",
		"CREATE TRIGGER trg_production_review_steps_guard_decision",
		"CREATE TRIGGER trg_production_review_requests_fence_terminal_children",
		"production review requests must be submitted pending",
		"production review steps must be inserted pending",
		"NEW.status IN ('approved', 'rejected', 'cancelled', 'obsolete', 'changes_requested')",
		"decision = 'cancelled'",
		"review_request_id = NEW.review_request_id AND sequence = NEW.sequence",
		"review_request_id = NEW.review_request_id AND required_role = NEW.required_role",
	} {
		require.Contains(t, up, declaration)
	}
	require.Contains(t, up, "IF NEW.status <> 'pending' OR NEW.terminal_by IS NOT NULL")
	require.Contains(t, up, "IF NOT EXISTS (SELECT 1 FROM production_review_steps WHERE review_request_id = OLD.id)")
	guardStart := strings.Index(up, "CREATE OR REPLACE FUNCTION guard_production_review_request_identity()")
	guardEnd := strings.Index(up[guardStart:], "$$ LANGUAGE plpgsql;")
	require.NotEqual(t, -1, guardStart)
	require.NotEqual(t, -1, guardEnd)
	require.Contains(t, up[guardStart:guardStart+guardEnd], "NEW.status NOT IN ('pending', 'approved', 'rejected', 'obsolete', 'cancelled', 'changes_requested')")

	down := mustReadMigration(t, "../../migrations/versioned/000073_knowledge_production_reviews.down.sql")
	_, err = pg_query.Parse(down)
	require.NoError(t, err)
	require.Less(t, strings.Index(down, "DROP TABLE IF EXISTS production_review_steps"), strings.Index(down, "DROP TABLE IF EXISTS production_review_requests"))
	require.Less(t, strings.Index(down, "DROP TABLE IF EXISTS production_review_requests"), strings.Index(down, "DROP TABLE IF EXISTS production_annotations"))
	require.Less(t, strings.Index(down, "DROP TRIGGER IF EXISTS trg_production_review_requests_validate_submission ON production_review_requests"), strings.Index(down, "DROP FUNCTION IF EXISTS validate_production_review_request_submission()"))
}

func TestProductionReviewSQLiteMigrationEnforcesAnchorsAndTerminalReviewGuards(t *testing.T) {
	db := openProductionReviewSQLite(t)
	seedProductionRunScopes(t, db)
	insertProductionVersion(t, db, "version-review", "document-1", 1, "project-1", 2, "source-set-1", "")
	_, err := db.Exec(`INSERT INTO production_document_versions (id, document_id, tenant_id, project_id, version_number, source_set_id, origin, content_digest, created_by, frozen_at) VALUES ('version-frozen', 'document-1', 1, 'project-1', 3, 'source-set-1', 'human', 'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee', 'owner-1', CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	insertProductionBlock(t, db, "block-review", "version-review", "block-review")
	insertProductionBlock(t, db, "block-frozen", "version-frozen", "block-frozen")
	insertProductionBlock(t, db, "block-other", "version-2", "block-other")
	_, err = db.Exec(`INSERT INTO production_project_members (project_id, user_id, role, assigned_by) VALUES ('project-1', 'compliance-1', 'compliance_reviewer', 'owner-1')`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO production_annotations (id, tenant_id, project_id, document_id, version_id, block_id, annotation_type, severity, anchor, body, created_by) VALUES ('bad-anchor', 1, 'project-1', 'document-1', 'version-review', 'block-other', 'comment', 'info', '{}', 'bad', 'author-1')`)
	require.Error(t, err, "a cross-version block must not be a valid annotation anchor")
	_, err = db.Exec(`INSERT INTO production_annotations (id, tenant_id, project_id, document_id, version_id, block_id, annotation_type, severity, anchor, body, created_by) VALUES ('bad-quality-tag', 1, 'project-1', 'document-1', 'version-frozen', 'block-frozen', 'quality_tag', 'warning', '{}', 'missing category', 'author-1')`)
	require.Error(t, err, "quality tags require an explicit constrained category")
	_, err = db.Exec(`INSERT INTO production_annotations (id, tenant_id, project_id, document_id, version_id, block_id, annotation_type, quality_tag, severity, anchor, body, created_by) VALUES ('annotation-1', 1, 'project-1', 'document-1', 'version-frozen', 'block-frozen', 'quality_tag', 'missing_evidence', 'blocking', '{"path":"/title"}', 'needs evidence', 'author-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_review_requests (id, tenant_id, project_id, document_id, version_id, policy_snapshot, policy_digest, submitted_by) VALUES ('review-blocked', 1, 'project-1', 'document-1', 'version-frozen', '{"steps":["compliance_reviewer"]}', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'author-1')`)
	require.ErrorContains(t, err, "open blocking annotations prevent review submission")
	_, err = db.Exec(`UPDATE production_annotations SET status = 'resolved', resolved_by = 'author-1', resolved_at = CURRENT_TIMESTAMP WHERE id = 'annotation-1'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_annotations SET block_id = 'block-review' WHERE id = 'annotation-1'`)
	require.ErrorContains(t, err, "production annotation anchor is immutable")
	_, err = db.Exec(`UPDATE production_annotations SET status = 'open', resolved_by = NULL, resolved_at = NULL WHERE id = 'annotation-1'`)
	require.ErrorContains(t, err, "terminal production annotations are immutable")

	_, err = db.Exec(`INSERT INTO production_review_requests (id, tenant_id, project_id, document_id, version_id, policy_snapshot, policy_digest, submitted_by) VALUES ('review-unfrozen-version', 1, 'project-1', 'document-1', 'version-review', '{}', '9999999999999999999999999999999999999999999999999999999999999999', 'author-1')`)
	require.NoError(t, err, "append-only document versions are immutable review targets without a mutable freeze update")
	_, err = db.Exec(`INSERT INTO production_review_requests (id, tenant_id, project_id, document_id, version_id, policy_snapshot, policy_digest, submitted_by) VALUES ('review-1', 1, 'project-1', 'document-1', 'version-frozen', '{"steps":["compliance_reviewer"]}', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'author-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_review_requests SET status = 'rejected', terminal_by = 'owner-1', completed_at = CURRENT_TIMESTAMP WHERE id = 'review-1'`)
	require.Error(t, err, "terminal rejection requires a reason")
	_, err = db.Exec(`INSERT INTO production_review_steps (id, review_request_id, tenant_id, project_id, document_id, version_id, required_role, sequence) VALUES ('step-1', 'review-1', 1, 'project-1', 'document-1', 'version-frozen', 'compliance_reviewer', 1)`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_review_steps SET decision = 'approved', reviewer_user_id = 'owner-1', comment = 'ok', decided_at = CURRENT_TIMESTAMP WHERE id = 'step-1'`)
	require.ErrorContains(t, err, "required review role")
	_, err = db.Exec(`UPDATE production_review_steps SET decision = 'approved', reviewer_user_id = 'compliance-1', comment = 'ok', decided_at = CURRENT_TIMESTAMP WHERE id = 'step-1'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_review_requests SET status = 'approved', terminal_by = 'owner-1', completed_at = CURRENT_TIMESTAMP WHERE id = 'review-1'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_review_steps SET comment = 'changed' WHERE id = 'step-1'`)
	require.ErrorContains(t, err, "terminal production review steps are immutable")
	_, err = db.Exec(`INSERT OR REPLACE INTO production_review_requests (id, tenant_id, project_id, document_id, version_id, policy_snapshot, policy_digest, submitted_by) VALUES ('review-1', 1, 'project-1', 'document-1', 'version-frozen', '{}', 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'author-1')`)
	require.ErrorContains(t, err, "production review requests cannot be replaced")
}

func TestProductionReviewSQLiteMigrationRollsBackPopulatedSchema(t *testing.T) {
	db := openProductionReviewSQLite(t)
	seedProductionRunScopes(t, db)
	_, err := db.Exec(`INSERT INTO production_document_versions (id, document_id, tenant_id, project_id, version_number, source_set_id, origin, content_digest, created_by, frozen_at) VALUES ('version-rollback-review', 'document-1', 1, 'project-1', 3, 'source-set-1', 'human', 'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee', 'owner-1', CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	insertProductionBlock(t, db, "block-rollback-review", "version-rollback-review", "block-rollback-review")
	_, err = db.Exec(`INSERT INTO production_annotations (id, tenant_id, project_id, document_id, version_id, block_id, annotation_type, severity, anchor, body, created_by) VALUES ('annotation-rollback', 1, 'project-1', 'document-1', 'version-rollback-review', 'block-rollback-review', 'comment', 'info', '{}', 'note', 'author-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_review_requests (id, tenant_id, project_id, document_id, version_id, policy_snapshot, policy_digest, submitted_by) VALUES ('review-rollback', 1, 'project-1', 'document-1', 'version-rollback-review', '{"steps":["business_reviewer"]}', 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff', 'author-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_review_steps (id, review_request_id, tenant_id, project_id, document_id, version_id, required_role, sequence) VALUES ('step-rollback', 'review-rollback', 1, 'project-1', 'document-1', 'version-rollback-review', 'business_reviewer', 1)`)
	require.NoError(t, err)
	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000004_knowledge_production_reviews.down.sql"))
	require.NoError(t, err)
	for _, table := range []string{"production_review_steps", "production_review_requests", "production_annotations"} {
		require.Empty(t, sqliteMasterSQL(t, db, "table", table))
	}
	require.Empty(t, sqliteMasterSQL(t, db, "index", "idx_production_annotations_blocking_count"))
}

func TestProductionReviewSQLiteMigrationRejectsTerminalRequestAndStepInserts(t *testing.T) {
	db := openProductionReviewSQLite(t)
	seedProductionReviewFixture(t, db)

	for _, status := range []string{"approved", "rejected", "cancelled", "obsolete", "changes_requested"} {
		_, err := db.Exec(`INSERT INTO production_review_requests (id, tenant_id, project_id, document_id, version_id, policy_snapshot, policy_digest, status, submitted_by, terminal_by, terminal_reason, completed_at) VALUES (?, 1, 'project-1', 'document-1', 'version-review-fixture', '{}', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', ?, 'author-1', 'owner-1', 'terminal', CURRENT_TIMESTAMP)`, "review-terminal-"+status, status)
		require.Error(t, err, "terminal review request %s must not be inserted", status)
	}
	insertProductionReviewRequest(t, db, "review-insert-guard", "version-review-fixture", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "{}")
	_, err := db.Exec(`UPDATE production_review_requests SET status = 'approved', terminal_by = 'owner-1', completed_at = CURRENT_TIMESTAMP WHERE id = 'review-insert-guard'`)
	require.Error(t, err, "zero-step approval must not bypass materialized review steps")
	for _, decision := range []string{"approved", "rejected", "changes_requested", "cancelled"} {
		_, err := db.Exec(`INSERT INTO production_review_steps (id, review_request_id, tenant_id, project_id, document_id, version_id, required_role, sequence, reviewer_user_id, decision, comment, decided_at) VALUES (?, 'review-insert-guard', 1, 'project-1', 'document-1', 'version-review-fixture', 'business_reviewer', ?, 'owner-1', ?, 'bypass', CURRENT_TIMESTAMP)`, "step-terminal-"+decision, len(decision), decision)
		require.Error(t, err, "review step decision %s must only arise through a guarded update", decision)
	}
	_, err = db.Exec(`INSERT INTO production_review_steps (id, review_request_id, tenant_id, project_id, document_id, version_id, required_role, sequence, decision, comment, decided_at) VALUES ('step-terminal-no-role', 'review-insert-guard', 1, 'project-1', 'document-1', 'version-review-fixture', 'business_reviewer', 99, 'approved', 'bypass', CURRENT_TIMESTAMP)`)
	require.Error(t, err, "review step decisions cannot be inserted without the required role")
}

func TestProductionReviewSQLiteMigrationGuardsReplaceAndTerminalizesNonApproval(t *testing.T) {
	for _, status := range []string{"rejected", "cancelled", "obsolete", "changes_requested"} {
		t.Run(status, func(t *testing.T) {
			db := openProductionReviewSQLite(t)
			seedProductionReviewFixture(t, db)
			insertProductionReviewRequest(t, db, "review-"+status, "version-review-fixture", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "{\"a\":1,\"b\":2}")
			insertProductionReviewStep(t, db, "step-approved-"+status, "review-"+status, "business_reviewer", 1)
			insertProductionReviewStep(t, db, "step-pending-"+status, "review-"+status, "engineering_reviewer", 2)
			_, err := db.Exec(`UPDATE production_review_steps SET decision = 'approved', reviewer_user_id = 'business-1', comment = 'ok', decided_at = CURRENT_TIMESTAMP WHERE id = ?`, "step-approved-"+status)
			require.NoError(t, err)
			_, err = db.Exec(`UPDATE production_review_requests SET status = ?, terminal_by = 'owner-1', terminal_reason = 'closed', completed_at = CURRENT_TIMESTAMP WHERE id = ?`, status, "review-"+status)
			require.NoError(t, err)

			var pending, cancelled int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM production_review_steps WHERE review_request_id = ? AND decision = 'pending'`, "review-"+status).Scan(&pending))
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM production_review_steps WHERE review_request_id = ? AND decision = 'cancelled'`, "review-"+status).Scan(&cancelled))
			require.Zero(t, pending)
			require.Equal(t, 1, cancelled)
			_, err = db.Exec(`UPDATE production_review_steps SET comment = 'mutated' WHERE id = ?`, "step-pending-"+status)
			require.ErrorContains(t, err, "terminal production review steps are immutable")
		})
	}

	db := openProductionReviewSQLite(t)
	seedProductionReviewFixture(t, db)
	insertProductionReviewRequest(t, db, "review-replace", "version-review-fixture", "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", "{}")
	insertProductionReviewStep(t, db, "step-replace", "review-replace", "business_reviewer", 1)
	_, err := db.Exec(`UPDATE production_review_steps SET decision = 'approved', reviewer_user_id = 'business-1', comment = 'ok', decided_at = CURRENT_TIMESTAMP WHERE id = 'step-replace'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_review_requests SET status = 'approved', terminal_by = 'owner-1', completed_at = CURRENT_TIMESTAMP WHERE id = 'review-replace'`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT OR REPLACE INTO production_review_requests (id, tenant_id, project_id, document_id, version_id, policy_snapshot, policy_digest, submitted_by) VALUES ('review-replace-new-id', 1, 'project-1', 'document-1', 'version-review-fixture', '{}', 'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee', 'author-1')`)
	require.ErrorContains(t, err, "production review requests cannot be replaced")
	_, err = db.Exec(`INSERT OR REPLACE INTO production_review_steps (id, review_request_id, tenant_id, project_id, document_id, version_id, required_role, sequence) VALUES ('step-replace-new-id', 'review-replace', 1, 'project-1', 'document-1', 'version-review-fixture', 'business_reviewer', 1)`)
	require.ErrorContains(t, err, "production review steps cannot be replaced")
	var requestStatus, stepDecision string
	require.NoError(t, db.QueryRow(`SELECT status FROM production_review_requests WHERE id = 'review-replace'`).Scan(&requestStatus))
	require.NoError(t, db.QueryRow(`SELECT decision FROM production_review_steps WHERE id = 'step-replace'`).Scan(&stepDecision))
	require.Equal(t, "approved", requestStatus)
	require.Equal(t, "approved", stepDecision)

	t.Run("child terminalization rolls back with parent", func(t *testing.T) {
		db := openProductionReviewSQLite(t)
		seedProductionReviewFixture(t, db)
		insertProductionReviewRequest(t, db, "review-rollback-terminalization", "version-review-fixture", "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "{}")
		insertProductionReviewStep(t, db, "step-rollback-terminalization", "review-rollback-terminalization", "business_reviewer", 1)
		_, err := db.Exec(`CREATE TRIGGER test_production_review_step_cancel_failure BEFORE UPDATE ON production_review_steps FOR EACH ROW WHEN NEW.decision = 'cancelled' BEGIN SELECT RAISE(ABORT, 'forced child terminalization failure'); END`)
		require.NoError(t, err)
		_, err = db.Exec(`UPDATE production_review_requests SET status = 'cancelled', terminal_by = 'owner-1', terminal_reason = 'closed', completed_at = CURRENT_TIMESTAMP WHERE id = 'review-rollback-terminalization'`)
		require.ErrorContains(t, err, "forced child terminalization failure")
		var status, decision string
		require.NoError(t, db.QueryRow(`SELECT status FROM production_review_requests WHERE id = 'review-rollback-terminalization'`).Scan(&status))
		require.NoError(t, db.QueryRow(`SELECT decision FROM production_review_steps WHERE id = 'step-rollback-terminalization'`).Scan(&decision))
		require.Equal(t, "pending", status)
		require.Equal(t, "pending", decision)
	})
}

func TestProductionReviewSQLiteMigrationEnforcesOneImmutablePolicyPerVersion(t *testing.T) {
	db := openProductionReviewSQLite(t)
	seedProductionReviewFixture(t, db)
	insertProductionReviewRequest(t, db, "review-policy", "version-review-fixture", "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "{\"a\":1,\"b\":2}")
	_, err := db.Exec(`INSERT INTO production_review_requests (id, tenant_id, project_id, document_id, version_id, policy_snapshot, policy_digest, submitted_by) VALUES ('review-policy-equivalent', 1, 'project-1', 'document-1', 'version-review-fixture', '{ "b": 2, "a": 1 }', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'author-1')`)
	require.Error(t, err, "equivalent policy JSON cannot create a second review for one immutable version")
	_, err = db.Exec(`UPDATE production_review_requests SET policy_snapshot = '{"a":2}', policy_digest = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE id = 'review-policy'`)
	require.ErrorContains(t, err, "production review request identity is immutable")
	insertProductionVersion(t, db, "version-review-policy-whitespace", "document-1", 1, "project-1", 4, "source-set-1", "")
	_, err = db.Exec(`INSERT INTO production_review_requests (id, tenant_id, project_id, document_id, version_id, policy_snapshot, policy_digest, submitted_by) VALUES ('review-policy-whitespace', 1, 'project-1', 'document-1', 'version-review-policy-whitespace', '{ "a": 1 }', '1111111111111111111111111111111111111111111111111111111111111111', 'author-1')`)
	require.Error(t, err, "SQLite submission policy text must be normalized JSON")
	insertProductionVersion(t, db, "version-review-policy-invented", "document-1", 1, "project-1", 5, "source-set-1", "")
	_, err = db.Exec(`INSERT INTO production_review_requests (id, tenant_id, project_id, document_id, version_id, policy_snapshot, policy_digest, submitted_by) VALUES ('review-policy-invented', 1, 'project-1', 'document-1', 'version-review-policy-invented', '{}', '1111111111111111111111111111111111111111111111111111111111111111', 'author-1')`)
	require.NoError(t, err, "portable SQL can validate digest shape but cannot recompute SHA-256 without a nonstandard extension")
	_, err = db.Exec(`INSERT INTO production_review_requests (id, tenant_id, project_id, document_id, version_id, policy_snapshot, policy_digest, submitted_by) VALUES ('review-policy-invented-duplicate', 1, 'project-1', 'document-1', 'version-review-policy-invented', '{"different":true}', '2222222222222222222222222222222222222222222222222222222222222222', 'author-1')`)
	require.Error(t, err, "an invented digest cannot create a duplicate review or rebind an immutable version")
}

func TestProductionRunsPostgreSQLMigrationDeclaresEquivalentStructure(t *testing.T) {
	up := mustReadMigration(t, "../../migrations/versioned/000072_knowledge_production_runs.up.sql")
	_, err := pg_query.Parse(up)
	require.NoError(t, err)

	for _, declaration := range []string{
		"state_payload JSONB NOT NULL DEFAULT '{}'::jsonb",
		"wakeup_version INTEGER NOT NULL DEFAULT 0",
		"wakeup_enqueued_version INTEGER NOT NULL DEFAULT 0",
		"wakeup_enqueued_version <= wakeup_version",
		"document_type_snapshot JSONB NOT NULL",
		"workflow_plan_snapshot JSONB NOT NULL",
		"workflow_plan_digest VARCHAR(64) NOT NULL",
		"raw_model_response JSONB NULL",
		"request_snapshot JSONB NOT NULL",
		"response_snapshot JSONB NULL",
		"ADD CONSTRAINT fk_production_evidence_snapshots_captured_tool_call",
		"FOREIGN KEY (captured_by_tool_call_id) REFERENCES production_tool_calls(id)",
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
		"CREATE TRIGGER trg_production_runs_guard_workflow_identity",
		"CREATE TRIGGER trg_production_tool_calls_guard_terminal",
		"BEFORE UPDATE OR DELETE ON production_runs",
		"BEFORE UPDATE OR DELETE ON production_tool_calls",
		"CREATE OR REPLACE FUNCTION guard_production_tool_call_invocation_identity()",
		"CREATE TRIGGER trg_production_tool_calls_guard_invocation_identity",
		"CREATE OR REPLACE FUNCTION fence_production_tool_call_parent()",
		"CREATE TRIGGER trg_production_tool_calls_fence_parent",
		"FOR UPDATE",
		"CREATE OR REPLACE FUNCTION fence_terminal_production_run_children()",
		"CREATE TRIGGER trg_production_runs_fence_terminal_children",
		"call.status IN ('planned', 'pending_approval', 'approved', 'executing')",
		"raw_model_response IS NOT NULL AND raw_model_response_digest IS NOT NULL",
		"response_snapshot IS NOT NULL AND response_digest IS NOT NULL",
		"NEW.id IS DISTINCT FROM OLD.id",
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
	require.NotContains(t, up, "OLD.status <> 'planned'")
	for _, secret := range []string{"api_key", "access_token", "refresh_token", "credential", "secret"} {
		require.NotContains(t, strings.ToLower(up), secret)
	}

	down := mustReadMigration(t, "../../migrations/versioned/000072_knowledge_production_runs.down.sql")
	_, err = pg_query.Parse(down)
	require.NoError(t, err)
	require.Contains(t, down, "DROP CONSTRAINT IF EXISTS fk_production_evidence_snapshots_captured_tool_call")
	require.Less(t,
		strings.Index(down, "DROP CONSTRAINT IF EXISTS fk_production_evidence_snapshots_captured_tool_call"),
		strings.Index(down, "DROP TABLE IF EXISTS production_tool_calls"),
	)
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

func TestProductionRunsSQLiteWorkflowSnapshotAndDigestAreImmutable(t *testing.T) {
	db := openProductionRunsSQLite(t)
	seedProductionRunScopes(t, db)
	insertProductionRun(t, db, "run-workflow-immutable", 1, "project-1", "document-1", "source-set-1", "version-1", "run-workflow-immutable")

	_, err := db.Exec(`UPDATE production_runs SET workflow_plan_snapshot = '{"steps":[{"provider_id":"baseline","provider_type":"skill","request":{},"tool_name":"load"}],"version":1}' WHERE id = 'run-workflow-immutable'`)
	require.ErrorContains(t, err, "production workflow identity is immutable")
	_, err = db.Exec(`UPDATE production_runs SET workflow_plan_digest = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' WHERE id = 'run-workflow-immutable'`)
	require.ErrorContains(t, err, "production workflow identity is immutable")
	_, err = db.Exec(`UPDATE production_runs SET status = 'running' WHERE id = 'run-workflow-immutable'`)
	require.NoError(t, err, "ordinary run state must remain mutable")
}

func TestProductionRunsWorkflowIdentityDownMigrationsRemoveGuards(t *testing.T) {
	sqliteDown := mustReadMigration(t, "../../migrations/sqlite/000003_knowledge_production_runs.down.sql")
	postgresDown := mustReadMigration(t, "../../migrations/versioned/000072_knowledge_production_runs.down.sql")
	require.Contains(t, sqliteDown, "DROP TRIGGER IF EXISTS trg_production_runs_guard_workflow_identity")
	require.Contains(t, postgresDown, "DROP TRIGGER IF EXISTS trg_production_runs_guard_workflow_identity ON production_runs")
	require.Contains(t, postgresDown, "DROP FUNCTION IF EXISTS guard_production_run_workflow_identity()")
}

func TestProductionWorkflowPlanColumnsAreImmutableAndJSONConstrained(t *testing.T) {
	postgresFoundation := mustReadMigration(t, "../../migrations/versioned/000070_knowledge_production_foundation.up.sql")
	postgresRuns := mustReadMigration(t, "../../migrations/versioned/000072_knowledge_production_runs.up.sql")
	sqliteFoundation := mustReadMigration(t, "../../migrations/sqlite/000001_knowledge_production_foundation.up.sql")
	sqliteRuns := mustReadMigration(t, "../../migrations/sqlite/000003_knowledge_production_runs.up.sql")

	for _, migration := range []string{postgresFoundation, sqliteFoundation} {
		require.Contains(t, migration, "workflow_plan")
		require.Contains(t, migration, `'{"steps":[],"version":1}'`)
		require.Contains(t, migration, "active production document type definitions are immutable")
	}
	for _, migration := range []string{postgresRuns, sqliteRuns} {
		require.Contains(t, migration, "workflow_plan_snapshot")
		require.Contains(t, migration, `'{"steps":[],"version":1}'`)
	}
}

func TestProductionRunsSQLiteMigrationConstrainsWakeupGenerations(t *testing.T) {
	db := openProductionRunsSQLite(t)
	seedProductionRunScopes(t, db)

	_, err := db.Exec(`INSERT INTO production_runs
        (id, tenant_id, project_id, document_id, source_set_id, run_type, model_id,
         document_type_snapshot, idempotency_key, wakeup_version, wakeup_enqueued_version)
        VALUES ('run-invalid-wakeup', 1, 'project-1', 'document-1', 'source-set-1',
                'write', 'model-1', '{}', 'run-invalid-wakeup', 1, 2)`)
	require.Error(t, err)

	insertProductionRun(t, db, "run-valid-wakeup", 1, "project-1", "document-1", "source-set-1", "version-1", "run-valid-wakeup")
	_, err = db.Exec(`UPDATE production_runs SET wakeup_version = 3, wakeup_enqueued_version = 2 WHERE id = 'run-valid-wakeup'`)
	require.NoError(t, err)
}

func TestProductionRunsSQLiteCollectIsDocumentIndependentAndOtherRunsAreNot(t *testing.T) {
	db := openProductionRunsSQLite(t)
	seedProductionRunScopes(t, db)

	_, err := db.Exec(`INSERT INTO production_runs
        (id, tenant_id, project_id, document_id, source_set_id, run_type, model_id, document_type_snapshot, idempotency_key)
        VALUES ('collect-independent', 1, 'project-1', NULL, 'source-set-1', 'collect', 'model-1', '{}', 'collect-independent')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_runs
        (id, tenant_id, project_id, document_id, source_set_id, run_type, model_id, document_type_snapshot, idempotency_key)
        VALUES ('collect-with-document', 1, 'project-1', 'document-1', 'source-set-1', 'collect', 'model-1', '{}', 'collect-with-document')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_runs
        (id, tenant_id, project_id, document_id, source_set_id, run_type, model_id, document_type_snapshot, idempotency_key)
        VALUES ('write-without-document', 1, 'project-1', NULL, 'source-set-1', 'write', 'model-1', '{}', 'write-without-document')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO production_tool_calls
        (id, run_id, tenant_id, project_id, document_id, source_set_id, attempt, current_step, idempotency_key, provider_type, provider_id, tool_name, request_snapshot, request_digest)
        VALUES ('collect-call', 'collect-independent', 1, 'project-1', NULL, 'source-set-1', 0, 0, 'collect-call', 'skill', 'baseline', 'load', '{}', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`)
	require.NoError(t, err)
}

func TestProductionRunsSQLiteMigrationFreezesToolCallPrimaryKeyWhilePlanned(t *testing.T) {
	db := openProductionRunsSQLite(t)
	seedProductionRunScopes(t, db)
	insertProductionRun(t, db, "run-1", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-1")
	insertProductionToolCall(t, db, "call-1", "run-1", "call-key-1", 0, 0)

	_, err := db.Exec(`UPDATE production_tool_calls SET id = 'call-changed' WHERE id = 'call-1'`)
	require.ErrorContains(t, err, "production tool call invocation identity is immutable")
}

func TestProductionRunsSQLiteMigrationFreezesInvocationIdentityImmediatelyAfterInsert(t *testing.T) {
	for _, mutation := range productionToolCallIdentityMutations() {
		t.Run(mutation.name, func(t *testing.T) {
			db := openProductionRunsSQLite(t)
			seedProductionRunScopes(t, db)
			insertProductionRun(t, db, "run-1", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-1")
			insertProductionRun(t, db, "run-other", 1, "project-1", "document-1", "source-set-1", "version-1", "run-key-other")
			insertProductionToolCall(t, db, "call-1", "run-1", "call-key-1", 0, 0)

			_, err := db.Exec(`UPDATE production_tool_calls SET ` + mutation.assignment + ` WHERE id = 'call-1'`)
			require.ErrorContains(t, err, "production tool call invocation identity is immutable")
		})
	}
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
		"captured_by_tool_call_id VARCHAR(36) NULL",
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

func TestProductionRunsSQLiteFrozenAcceptedItemRequiresExactExecutingToolCallEvidence(t *testing.T) {
	db := openProductionRunsSQLite(t)
	seedProductionRunScopes(t, db)
	insertProductionRun(t, db, "run-frozen-evidence", 1, "project-1", "document-1", "source-set-1", "version-1", "run-frozen-evidence")
	insertProductionToolCall(t, db, "call-frozen-evidence", "run-frozen-evidence", "call-frozen-evidence", 1, 0)
	setProductionToolCallStatus(t, db, "call-frozen-evidence", "executing")
	insertProductionToolCall(t, db, "call-planned-evidence", "run-frozen-evidence", "call-planned-evidence", 1, 1)
	_, err := db.Exec(`UPDATE production_source_sets SET status = 'frozen', frozen_at = CURRENT_TIMESTAMP WHERE id = 'source-set-1'`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO production_evidence_snapshots (id, source_item_id, snapshot_type, inline_content, content_digest, redaction_metadata, captured_by_run_id, captured_by_tool_call_id) VALUES ('run-evidence', 'source-item-1', 'tool_result', '{"ok":true}', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', '{}', 'run-frozen-evidence', 'call-frozen-evidence')`)
	require.NoError(t, err)

	for _, test := range []struct {
		name, id, snapshotType, runID, callID string
	}{
		{name: "missing call", id: "missing-call-evidence", snapshotType: "tool_result", runID: "run-frozen-evidence", callID: ""},
		{name: "wrong call", id: "wrong-call-evidence", snapshotType: "tool_result", runID: "run-frozen-evidence", callID: "call-missing"},
		{name: "planned call", id: "planned-call-evidence", snapshotType: "tool_result", runID: "run-frozen-evidence", callID: "call-planned-evidence"},
		{name: "wrong run", id: "wrong-run-evidence", snapshotType: "tool_result", runID: "run-missing", callID: "call-frozen-evidence"},
		{name: "wrong type", id: "wrong-type-evidence", snapshotType: "json", runID: "run-frozen-evidence", callID: "call-frozen-evidence"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, insertErr := db.Exec(`INSERT INTO production_evidence_snapshots (id, source_item_id, snapshot_type, inline_content, content_digest, redaction_metadata, captured_by_run_id, captured_by_tool_call_id) VALUES (?, 'source-item-1', ?, '{"ok":true}', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', '{}', NULLIF(?, ''), NULLIF(?, ''))`, test.id, test.snapshotType, test.runID, test.callID)
			require.ErrorContains(t, insertErr, "frozen source set requires accepted executing tool-call evidence")
		})
	}

	_, err = db.Exec(`INSERT INTO production_evidence_snapshots (id, source_item_id, snapshot_type, inline_content, content_digest, redaction_metadata) VALUES ('ordinary-evidence', 'source-item-1', 'text', '"late"', 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', '{}')`)
	require.ErrorContains(t, err, "frozen source set requires accepted executing tool-call evidence")
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

func TestProductionPublicationMigrationsDeclareRequiredTables(t *testing.T) {
	postgres := mustReadMigration(t, "../../migrations/versioned/000074_knowledge_production_publication.up.sql")
	sqlite := mustReadMigration(t, "../../migrations/sqlite/000005_knowledge_production_publication.up.sql")

	for _, table := range []string{
		"production_releases",
		"production_release_targets",
		"production_projection_heads",
	} {
		require.Contains(t, postgres, "CREATE TABLE IF NOT EXISTS "+table)
		require.Contains(t, sqlite, "CREATE TABLE IF NOT EXISTS "+table)
	}
}

func TestProductionPublicationBindingVersionsContainAllProjectionSchema(t *testing.T) {
	postgresUp := mustReadMigration(t, "../../migrations/versioned/000074_knowledge_production_publication.up.sql")
	sqliteUp := mustReadMigration(t, "../../migrations/sqlite/000005_knowledge_production_publication.up.sql")
	for _, migration := range []string{postgresUp, sqliteUp} {
		for _, required := range []string{
			"release_digest_version", "failure_code", "failure_reason", "recovery_attempted_at",
			"idx_production_release_targets_failure_recovery", "supersedes_release_id",
			"uq_production_releases_version_root", "uq_production_releases_successor",
		} {
			require.Contains(t, migration, required)
		}
	}
	for _, path := range []string{
		"../../migrations/versioned/000074_knowledge_production_publication.up.sql",
		"../../migrations/versioned/000074_knowledge_production_publication.down.sql",
		"../../migrations/sqlite/000005_knowledge_production_publication.up.sql",
		"../../migrations/sqlite/000005_knowledge_production_publication.down.sql",
	} {
		_, err := os.Stat(path)
		require.NoError(t, err, path)
	}
}

func TestProductionMigrationTestPathsExist(t *testing.T) {
	contents, err := os.ReadFile("production_migration_test.go")
	require.NoError(t, err)

	paths := regexp.MustCompile(`\.\./\.\./migrations/(?:versioned|sqlite)/[0-9]{6}_[a-z0-9_]+\.(?:up|down)\.sql`).FindAllString(string(contents), -1)
	paths = append(paths, productionSQLiteFoundationMigrationPaths()...)
	require.NotEmpty(t, paths)
	for _, path := range paths {
		_, err := os.Stat(path)
		require.NoError(t, err, path)
	}
}

func TestProductionPublicationReprepareLineageConstraints(t *testing.T) {
	postgres := mustReadMigration(t, "../../migrations/versioned/000074_knowledge_production_publication.up.sql")
	_, err := pg_query.Parse(postgres)
	require.NoError(t, err)
	for _, declaration := range []string{
		"supersedes_release_id VARCHAR(36) NULL",
		"FOREIGN KEY (supersedes_release_id, tenant_id, project_id, document_id, version_id)",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_production_releases_version_root",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_production_releases_successor",
	} {
		require.Contains(t, postgres, declaration)
	}
	require.NotContains(t, postgres, "UNIQUE(tenant_id, project_id, document_id, version_id)")

	db := openProductionPublicationSQLite(t)
	seedProductionReleaseScope(t, db)
	insertProductionPublicationRelease(t, db, "release-lineage-root", "version-1", "review-pub-1", strings.Repeat("a", 64))
	_, err = db.Exec(`INSERT INTO production_releases
		(id, tenant_id, project_id, document_id, version_id, review_request_id, release_digest, created_by)
		VALUES ('release-lineage-root-2', 1, 'project-1', 'document-1', 'version-1', 'review-pub-1', ?, 'owner-1')`, strings.Repeat("a", 64))
	require.Error(t, err, "one approved version can have only one root release")

	_, err = db.Exec(`INSERT INTO production_releases
		(id, tenant_id, project_id, document_id, version_id, review_request_id, release_digest, supersedes_release_id, created_by)
		VALUES ('release-lineage-next', 1, 'project-1', 'document-1', 'version-1', 'review-pub-1', ?, 'release-lineage-root', 'owner-1')`, strings.Repeat("a", 64))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_releases
		(id, tenant_id, project_id, document_id, version_id, review_request_id, release_digest, supersedes_release_id, created_by)
		VALUES ('release-lineage-fork', 1, 'project-1', 'document-1', 'version-1', 'review-pub-1', ?, 'release-lineage-root', 'owner-1')`, strings.Repeat("a", 64))
	require.Error(t, err, "one release can have only one direct successor")
	_, err = db.Exec(`UPDATE production_releases SET supersedes_release_id = NULL WHERE id = 'release-lineage-next'`)
	require.ErrorContains(t, err, "production release identity is immutable")
}

func TestProductionLifecycleDerivationTimestampsAreWriteOnceInSQLite(t *testing.T) {
	db := openProductionProjectionIntegritySQLite(t)
	seedProductionReleaseScope(t, db)
	insertProductionPublicationRelease(t, db, "release-frozen-lifecycle", "version-1", "review-pub-1", strings.Repeat("a", 64))
	insertProductionPublicationTarget(t, db, "target-frozen-lifecycle", "release-frozen-lifecycle", "version-1", "kb-1", "knowledge-frozen-lifecycle", strings.Repeat("a", 64))

	_, err := db.Exec(`UPDATE production_release_targets
		SET status = 'failed', failure_code = 'PROJECTION_BUILD_FAILED', failure_reason = 'projection build failed',
		    failed_at = '2020-01-01 00:00:00', retention_until = '2020-01-31 00:00:00'
		WHERE id = 'target-frozen-lifecycle'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_release_targets
		SET failed_at = '2020-02-01 00:00:00', retention_until = '2020-03-02 00:00:00'
		WHERE id = 'target-frozen-lifecycle'`)
	require.ErrorContains(t, err, "retention_until is immutable")

	_, err = db.Exec(`UPDATE production_release_targets
		SET status = 'cleanup_pending', cleanup_requested_at = '2020-02-01 00:00:00'
		WHERE id = 'target-frozen-lifecycle'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_release_targets
		SET cleanup_requested_at = '2020-02-02 00:00:00'
		WHERE id = 'target-frozen-lifecycle'`)
	require.ErrorContains(t, err, "cleanup_requested_at is immutable")

	_, err = db.Exec(`UPDATE production_release_targets
		SET status = 'cleaned', cleaned_at = '2020-02-03 00:00:00'
		WHERE id = 'target-frozen-lifecycle'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_release_targets
		SET cleaned_at = '2020-02-04 00:00:00'
		WHERE id = 'target-frozen-lifecycle'`)
	require.ErrorContains(t, err, "cleaned_at is immutable")
}

func TestProductionLifecycleRetentionAllowsRetryAndIsPreservedThroughCleanup(t *testing.T) {
	postgres := mustReadMigration(t, "../../migrations/versioned/000074_knowledge_production_publication.up.sql")
	for _, declaration := range []string{
		"OLD.status IN ('failed', 'rolled_back', 'cleanup_pending', 'cleaned') AND\n       NEW.status = OLD.status AND",
		"OLD.status IN ('failed', 'rolled_back') AND NEW.status = 'cleanup_pending'",
		"OLD.status = 'cleanup_pending' AND NEW.status = 'cleaned'",
	} {
		require.Contains(t, postgres, declaration)
	}

	db := openProductionProjectionIntegritySQLite(t)
	seedProductionReleaseScope(t, db)
	insertProductionPublicationRelease(t, db, "release-retention-retry", "version-1", "review-pub-1", strings.Repeat("a", 64))
	insertProductionPublicationTarget(t, db, "target-retention-retry", "release-retention-retry", "version-1", "kb-1", "knowledge-retention-retry", strings.Repeat("a", 64))

	_, err := db.Exec(`UPDATE production_release_targets
		SET status = 'failed', failure_code = 'PROJECTION_BUILD_FAILED', failure_reason = 'projection build failed',
		    failed_at = '2020-01-01 00:00:00', retention_until = '2020-01-31 00:00:00'
		WHERE id = 'target-retention-retry'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_release_targets
		SET status = 'building', failure_code = '', failure_reason = '', failed_at = NULL, retention_until = NULL,
		    updated_at = '2020-02-01 00:00:00'
		WHERE id = 'target-retention-retry'`)
	require.NoError(t, err, "failed targets must be retryable after clearing lifecycle fields")

	insertProductionPublicationTarget(t, db, "target-retention-cleanup", "release-retention-retry", "version-1", "kb-2", "knowledge-retention-cleanup", strings.Repeat("a", 64))
	_, err = db.Exec(`UPDATE production_release_targets
		SET status = 'failed', failure_code = 'PROJECTION_BUILD_FAILED', failure_reason = 'projection build failed',
		    failed_at = '2020-01-01 00:00:00', retention_until = '2020-01-31 00:00:00'
		WHERE id = 'target-retention-cleanup'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_release_targets
		SET status = 'cleanup_pending', cleanup_requested_at = '2020-02-01 00:00:00', retention_until = '2020-02-01 00:00:00'
		WHERE id = 'target-retention-cleanup'`)
	require.ErrorContains(t, err, "retention_until is immutable",
		"cleanup entry must preserve the eligibility deadline")
	_, err = db.Exec(`UPDATE production_release_targets
		SET status = 'cleanup_pending', cleanup_requested_at = '2020-02-01 00:00:00'
		WHERE id = 'target-retention-cleanup'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_release_targets
		SET status = 'cleaned', cleaned_at = '2020-02-02 00:00:00', retention_until = '2020-02-01 00:00:00'
		WHERE id = 'target-retention-cleanup'`)
	require.ErrorContains(t, err, "retention_until is immutable",
		"cleanup completion must preserve the eligibility deadline")
}

func TestProductionProjectionIntegrityIsFoldedAndParses(t *testing.T) {
	postgresUp := mustReadMigration(t, "../../migrations/versioned/000074_knowledge_production_publication.up.sql")
	postgresDown := mustReadMigration(t, "../../migrations/versioned/000074_knowledge_production_publication.down.sql")
	_, err := pg_query.Parse(postgresUp)
	require.NoError(t, err)
	_, err = pg_query.Parse(postgresDown)
	require.NoError(t, err)

	for _, migration := range []string{
		postgresUp,
		mustReadMigration(t, "../../migrations/sqlite/000005_knowledge_production_publication.up.sql"),
	} {
		require.Contains(t, migration, "release_digest_version")
		require.Contains(t, migration, "failure_code")
		require.Contains(t, migration, "failure_reason")
	}
}

func TestProductionFailureRecoveryIsFoldedAndParses(t *testing.T) {
	postgresUp := mustReadMigration(t, "../../migrations/versioned/000074_knowledge_production_publication.up.sql")
	postgresDown := mustReadMigration(t, "../../migrations/versioned/000074_knowledge_production_publication.down.sql")
	_, err := pg_query.Parse(postgresUp)
	require.NoError(t, err)
	_, err = pg_query.Parse(postgresDown)
	require.NoError(t, err)

	for _, migration := range []string{
		postgresUp,
		mustReadMigration(t, "../../migrations/sqlite/000005_knowledge_production_publication.up.sql"),
	} {
		require.Contains(t, migration, "recovery_attempted_at")
		require.Contains(t, migration, "idx_production_release_targets_failure_recovery")
		require.Contains(t, migration, "tenant_id")
		require.Contains(t, migration, "status")
	}
}

func TestProductionFailureRecoverySQLiteBindingVersionSupportsPopulatedTargets(t *testing.T) {
	db := openProductionPublicationSQLite(t)
	seedProductionReleaseScope(t, db)
	insertProductionPublicationRelease(t, db, "release-recovery", "version-1", "review-pub-1", strings.Repeat("a", 64))
	insertProductionPublicationTarget(t, db, "target-recovery", "release-recovery", "version-1", "kb-1", "knowledge-recovery", strings.Repeat("a", 64))
	var attemptedAt sql.NullTime
	require.NoError(t, db.QueryRow(`SELECT recovery_attempted_at FROM production_release_targets WHERE id = 'target-recovery'`).Scan(&attemptedAt))
	require.False(t, attemptedAt.Valid)
	_, err := db.Exec(`UPDATE production_release_targets SET recovery_attempted_at = CURRENT_TIMESTAMP WHERE id = 'target-recovery'`)
	require.NoError(t, err)

	var targetCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM production_release_targets WHERE id = 'target-recovery'`).Scan(&targetCount))
	require.Equal(t, 1, targetCount)
}

func TestProductionProjectionIntegrityPostgreSQLBackfillsLegacyFailedTargetsBeforeConstraint(t *testing.T) {
	postgresUp := mustReadMigration(t, "../../migrations/versioned/000074_knowledge_production_publication.up.sql")
	require.Contains(t, postgresUp, "ADD CONSTRAINT chk_production_release_targets_failure")
	require.NotContains(t, postgresUp, "LEGACY_PROJECTION_FAILURE",
		"the unreleased binding migration has no legacy publication rows to backfill")
}

func TestProductionProjectionIntegrityPostgreSQLUpgradesPopulatedFailedTarget(t *testing.T) {
	t.Skip("publication binding migration is unreleased; incremental upgrade is intentionally removed")
	dsn := os.Getenv("WEKNORA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WEKNORA_TEST_POSTGRES_DSN is not configured")
	}

	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, config)
	require.NoError(t, err)
	defer conn.Close(context.Background())

	schema := "production_projection_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = conn.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA "%s"`, schema))
	require.NoError(t, err)
	defer func() {
		_, _ = conn.Exec(context.Background(), fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schema))
	}()
	_, err = conn.Exec(ctx, fmt.Sprintf(`SET search_path TO "%s"`, schema))
	require.NoError(t, err)

	_, err = conn.Exec(ctx, `
CREATE TABLE production_releases (
    id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL, project_id VARCHAR(36) NOT NULL,
    document_id VARCHAR(36) NOT NULL, version_id VARCHAR(36) NOT NULL, review_request_id VARCHAR(36) NOT NULL,
    release_digest VARCHAR(64) NOT NULL, status VARCHAR(20) NOT NULL DEFAULT 'building',
    retention_days INTEGER NOT NULL DEFAULT 30, created_by VARCHAR(36) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE production_release_targets (
    id VARCHAR(36) PRIMARY KEY, release_id VARCHAR(36) NOT NULL, tenant_id BIGINT NOT NULL,
    project_id VARCHAR(36) NOT NULL, document_id VARCHAR(36) NOT NULL, version_id VARCHAR(36) NOT NULL,
    target_knowledge_base_id VARCHAR(36) NOT NULL, knowledge_id VARCHAR(36) NOT NULL,
    release_digest VARCHAR(64) NOT NULL, config_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    config_digest VARCHAR(64) NOT NULL, status VARCHAR(20) NOT NULL DEFAULT 'building',
    retention_days INTEGER NOT NULL DEFAULT 30, retention_until TIMESTAMP NULL,
    activated_at TIMESTAMP NULL, failed_at TIMESTAMP NULL, rolled_back_at TIMESTAMP NULL,
    cleanup_requested_at TIMESTAMP NULL, cleaned_at TIMESTAMP NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE production_projection_heads (
    tenant_id BIGINT NOT NULL, document_id VARCHAR(36) NOT NULL,
    target_knowledge_base_id VARCHAR(36) NOT NULL, active_release_target_id VARCHAR(36) NOT NULL,
    lock_version INTEGER NOT NULL DEFAULT 1, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO production_releases
    (id, tenant_id, project_id, document_id, version_id, review_request_id, release_digest, created_by)
VALUES
    ('release-legacy', 1, 'project-1', 'document-1', 'version-1', 'review-1', repeat('a', 64), 'owner-1');
INSERT INTO production_release_targets
    (id, release_id, tenant_id, project_id, document_id, version_id, target_knowledge_base_id,
     knowledge_id, release_digest, config_digest, status, failed_at, retention_until)
VALUES
    ('target-legacy-failed', 'release-legacy', 1, 'project-1', 'document-1', 'version-1', 'kb-1',
     'knowledge-1', repeat('a', 64), repeat('b', 64), 'failed',
     TIMESTAMP '2026-01-01 00:00:00', TIMESTAMP '2026-01-31 00:00:00');
`)
	require.NoError(t, err)

	_, err = conn.Exec(ctx, mustReadMigration(t, "../../migrations/versioned/000074_knowledge_production_publication.up.sql"))
	require.NoError(t, err)
	var failureCode, failureReason string
	err = conn.QueryRow(ctx, `SELECT failure_code, failure_reason FROM production_release_targets WHERE id = 'target-legacy-failed'`).Scan(&failureCode, &failureReason)
	require.NoError(t, err)
	require.Equal(t, "LEGACY_PROJECTION_FAILURE", failureCode)
	require.Equal(t, "legacy failed target migrated without recorded failure details", failureReason)
}

func TestProductionProjectionIntegritySQLiteUpgradesAndDowngradesPopulatedPublication(t *testing.T) {
	db := openProductionPublicationSQLite(t)
	seedProductionReleaseScope(t, db)
	insertProductionPublicationRelease(t, db, "release-upgrade", "version-1", "review-pub-1", strings.Repeat("a", 64))
	insertProductionPublicationTarget(t, db, "target-upgrade", "release-upgrade", "version-1", "kb-1", "knowledge-upgrade", strings.Repeat("a", 64))
	insertProductionPublicationTarget(t, db, "target-upgrade-failed", "release-upgrade", "version-1", "kb-2", "knowledge-upgrade-failed", strings.Repeat("a", 64))
	_, err := db.Exec(`UPDATE production_release_targets
		SET status = 'failed', failure_code = 'PROJECTION_BUILD_FAILED', failure_reason = 'projection build failed',
		    failed_at = '2026-01-01 00:00:00', retention_until = '2026-01-31 00:00:00'
		WHERE id = 'target-upgrade-failed'`)
	require.NoError(t, err)

	var digestVersion int
	var failureCode, failureReason string
	require.NoError(t, db.QueryRow(`SELECT release_digest_version FROM production_releases WHERE id = 'release-upgrade'`).Scan(&digestVersion))
	require.Zero(t, digestVersion, "legacy digests must remain explicitly unverifiable")
	require.NoError(t, db.QueryRow(`SELECT failure_code, failure_reason FROM production_release_targets WHERE id = 'target-upgrade'`).Scan(&failureCode, &failureReason))
	require.Empty(t, failureCode)
	require.Empty(t, failureReason)
	require.NoError(t, db.QueryRow(`SELECT failure_code, failure_reason FROM production_release_targets WHERE id = 'target-upgrade-failed'`).Scan(&failureCode, &failureReason))
	require.Equal(t, "PROJECTION_BUILD_FAILED", failureCode)
	require.Equal(t, "projection build failed", failureReason)
	_, err = db.Exec(`UPDATE production_releases SET release_digest_version = 1 WHERE id = 'release-upgrade'`)
	require.ErrorContains(t, err, "release identity is immutable")
	_, err = db.Exec(`UPDATE production_release_targets
		SET status = 'failed', failed_at = CURRENT_TIMESTAMP, retention_until = datetime(CURRENT_TIMESTAMP, '+30 days')
		WHERE id = 'target-upgrade'`)
	require.Error(t, err, "failed targets require bounded failure metadata")

	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000005_knowledge_production_publication.down.sql"))
	require.NoError(t, err)
	require.Empty(t, sqliteColumnTypeIfPresent(t, db, "production_releases", "release_digest_version"))
	require.Empty(t, sqliteColumnTypeIfPresent(t, db, "production_release_targets", "failure_code"))
	var knowledgeBaseCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM knowledge_bases`).Scan(&knowledgeBaseCount))
	require.Positive(t, knowledgeBaseCount, "binding down must preserve populated dependencies")
}

func TestProductionPublicationPostgreSQLMigrationDeclaresReleaseAndProjectionIntegrity(t *testing.T) {
	up := mustReadMigration(t, "../../migrations/versioned/000074_knowledge_production_publication.up.sql")
	_, err := pg_query.Parse(up)
	require.NoError(t, err)

	for _, declaration := range []string{
		"release_digest VARCHAR(64) NOT NULL",
		"retention_days INTEGER NOT NULL DEFAULT 30",
		"knowledge_id VARCHAR(36) NOT NULL UNIQUE",
		"status IN ('building', 'ready', 'active', 'failed', 'rolled_back', 'cleanup_pending', 'cleaned')",
		"PRIMARY KEY (tenant_id, document_id, target_knowledge_base_id)",
		"FOREIGN KEY (active_release_target_id, tenant_id, document_id, target_knowledge_base_id)",
		"REFERENCES production_release_targets(id, tenant_id, document_id, target_knowledge_base_id)",
		"CREATE TRIGGER trg_production_releases_validate_approved_review",
		"CREATE TRIGGER trg_production_releases_guard_status",
		"CREATE TRIGGER trg_production_release_targets_guard",
		"CREATE TRIGGER trg_production_projection_heads_guard",
		"CREATE TRIGGER trg_production_projection_heads_activate_target",
		"active production release targets require projection head activation",
		"production projection heads must currently reference active targets",
		"NEW.failed_at IS NOT NULL OR NEW.rolled_back_at IS NOT NULL",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_production_release_targets_active_projection",
		"CREATE INDEX IF NOT EXISTS idx_production_release_targets_scope_status",
		"CREATE INDEX IF NOT EXISTS idx_production_release_targets_cleanup_eligibility",
		"retention_until TIMESTAMP NULL",
		"FOREIGN KEY (release_id, tenant_id, project_id, document_id, version_id, release_digest)",
		"production projection heads require ready or active targets",
		"production projection head updates require CAS lock versions",
		"config_snapshot JSONB NOT NULL",
		"config_digest VARCHAR(64) NOT NULL",
		"chk_production_release_targets_config_digest",
		"octet_length(config_snapshot::text)",
	} {
		require.Contains(t, up, declaration)
	}
	for _, declaration := range []string{
		"release_digest_version INTEGER NOT NULL DEFAULT 0",
		"failure_code VARCHAR(64) NOT NULL DEFAULT ''",
		"failure_reason VARCHAR(256) NOT NULL DEFAULT ''",
		"production release target failure metadata is lifecycle-owned",
	} {
		require.Contains(t, up, declaration)
	}

	down := mustReadMigration(t, "../../migrations/versioned/000074_knowledge_production_publication.down.sql")
	_, err = pg_query.Parse(down)
	require.NoError(t, err)
	require.Less(t, strings.Index(down, "DROP TABLE IF EXISTS production_projection_heads"), strings.Index(down, "DROP TABLE IF EXISTS production_release_targets"))
	require.Less(t, strings.Index(down, "DROP TABLE IF EXISTS production_release_targets"), strings.Index(down, "DROP TABLE IF EXISTS production_releases"))
	for _, index := range []string{
		"uq_production_release_targets_active_projection",
		"idx_production_release_targets_scope_status",
		"idx_production_release_targets_cleanup_eligibility",
	} {
		require.Contains(t, down, "DROP INDEX IF EXISTS "+index)
	}
	require.Contains(t, down, "DROP TRIGGER IF EXISTS trg_production_projection_heads_activate_target ON production_projection_heads")
}

func TestProductionPublicationSQLiteMigrationGuardsCanonicalTargetConfigIdentity(t *testing.T) {
	db := openProductionPublicationSQLite(t)
	seedProductionReleaseScope(t, db)
	insertProductionPublicationRelease(t, db, "release-config", "version-1", "review-pub-1", strings.Repeat("a", 64))

	_, err := db.Exec(`INSERT INTO production_release_targets
		(id, release_id, tenant_id, project_id, document_id, version_id, target_knowledge_base_id, knowledge_id, release_digest, config_snapshot, config_digest)
		VALUES ('target-config', 'release-config', 1, 'project-1', 'document-1', 'version-1', 'kb-1', 'knowledge-config', ?, '{"chunking":{"size":512}}', ?)`,
		strings.Repeat("a", 64), strings.Repeat("b", 64))
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_release_targets SET config_snapshot = '{}' WHERE id = 'target-config'`)
	require.ErrorContains(t, err, "target identity is immutable")
	_, err = db.Exec(`UPDATE production_release_targets SET config_digest = ? WHERE id = 'target-config'`, strings.Repeat("c", 64))
	require.ErrorContains(t, err, "target identity is immutable")

	for _, tc := range []struct {
		name, snapshot, digest string
	}{
		{name: "non-object", snapshot: `[]`, digest: strings.Repeat("b", 64)},
		{name: "non-canonical", snapshot: `{ "chunking": {"size":512} }`, digest: strings.Repeat("b", 64)},
		{name: "bad digest", snapshot: `{}`, digest: "ABC"},
	} {
		_, err = db.Exec(`INSERT INTO production_release_targets
			(id, release_id, tenant_id, project_id, document_id, version_id, target_knowledge_base_id, knowledge_id, release_digest, config_snapshot, config_digest)
			VALUES (?, 'release-config', 1, 'project-1', 'document-1', 'version-1', 'kb-2', ?, ?, ?, ?)`,
			"target-config-"+tc.name, "knowledge-config-"+tc.name, strings.Repeat("a", 64), tc.snapshot, tc.digest)
		require.Errorf(t, err, "case %s", tc.name)
	}
}

func TestProductionPublicationSQLiteMigrationEnforcesScopedTargetsAndCASHeads(t *testing.T) {
	db := openProductionPublicationSQLite(t)
	seedProductionReleaseScope(t, db)

	_, err := db.Exec(`INSERT INTO production_releases (id, tenant_id, project_id, document_id, version_id, review_request_id, release_digest, created_by) VALUES ('release-1', 1, 'project-1', 'document-1', 'version-1', 'review-pub-1', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_releases (id, tenant_id, project_id, document_id, version_id, review_request_id, release_digest, created_by) VALUES ('release-unapproved', 1, 'project-1', 'document-1', 'version-2', 'review-pub-2', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'owner-1')`)
	require.ErrorContains(t, err, "approved review")

	_, err = db.Exec(`INSERT INTO production_release_targets (id, release_id, tenant_id, project_id, document_id, version_id, target_knowledge_base_id, knowledge_id, release_digest) VALUES ('target-wrong-kb', 'release-1', 1, 'project-1', 'document-1', 'version-1', 'kb-other-tenant', 'knowledge-wrong-kb', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`)
	require.Error(t, err, "target knowledge bases cannot cross tenant boundaries")
	_, err = db.Exec(`INSERT INTO production_release_targets (id, release_id, tenant_id, project_id, document_id, version_id, target_knowledge_base_id, knowledge_id, release_digest) VALUES ('target-1', 'release-1', 1, 'project-1', 'document-1', 'version-1', 'kb-1', 'knowledge-1', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_release_targets (id, release_id, tenant_id, project_id, document_id, version_id, target_knowledge_base_id, knowledge_id, release_digest) VALUES ('target-duplicate-knowledge', 'release-1', 1, 'project-1', 'document-1', 'version-1', 'kb-1', 'knowledge-1', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`)
	require.Error(t, err, "a preassigned Knowledge ID belongs to only one target")
	_, err = db.Exec(`UPDATE production_release_targets SET knowledge_id = 'knowledge-changed' WHERE id = 'target-1'`)
	require.ErrorContains(t, err, "target identity is immutable")

	_, err = db.Exec(`INSERT INTO production_projection_heads (tenant_id, document_id, target_knowledge_base_id, active_release_target_id) VALUES (1, 'document-1', 'kb-1', 'target-1')`)
	require.ErrorContains(t, err, "require ready or active targets")
	_, err = db.Exec(`UPDATE production_release_targets SET status = 'ready' WHERE id = 'target-1'`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_projection_heads (tenant_id, document_id, target_knowledge_base_id, active_release_target_id) VALUES (1, 'document-1', 'kb-1', 'target-1')`)
	require.NoError(t, err)
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM production_release_targets WHERE id = 'target-1'`).Scan(&status))
	require.Equal(t, "active", status)

	_, err = db.Exec(`UPDATE production_projection_heads SET lock_version = 3 WHERE tenant_id = 1 AND document_id = 'document-1' AND target_knowledge_base_id = 'kb-1'`)
	require.ErrorContains(t, err, "require CAS lock versions")
}

func TestProductionPublicationSQLiteMigrationRollsBackPopulatedSchema(t *testing.T) {
	db := openProductionPublicationSQLite(t)
	seedProductionReleaseScope(t, db)
	_, err := db.Exec(`INSERT INTO production_releases (id, tenant_id, project_id, document_id, version_id, review_request_id, release_digest, created_by) VALUES ('release-rollback', 1, 'project-1', 'document-1', 'version-1', 'review-pub-1', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'owner-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_release_targets (id, release_id, tenant_id, project_id, document_id, version_id, target_knowledge_base_id, knowledge_id, release_digest) VALUES ('target-rollback', 'release-rollback', 1, 'project-1', 'document-1', 'version-1', 'kb-1', 'knowledge-rollback', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`)
	require.NoError(t, err)
	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000005_knowledge_production_publication.down.sql"))
	require.NoError(t, err)
	for _, table := range []string{"production_projection_heads", "production_release_targets", "production_releases"} {
		require.Empty(t, sqliteMasterSQL(t, db, "table", table))
	}
}

func TestProductionPublicationSQLiteMigrationGuardsAggregateLifecycleTargetsRetentionAndIndexes(t *testing.T) {
	db := openProductionProjectionIntegritySQLite(t)
	seedProductionReleaseScope(t, db)
	insertProductionPublicationRelease(t, db, "release-1", "version-1", "review-pub-1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	for _, status := range []string{"active", "cleaned"} {
		_, err := db.Exec(`UPDATE production_releases SET status = ? WHERE id = 'release-1'`, status)
		require.ErrorContains(t, err, "invalid production release status transition")
	}
	for _, status := range []string{"ready", "active", "failed", "building", "rolled_back", "building"} {
		_, err := db.Exec(`UPDATE production_releases SET status = ? WHERE id = 'release-1'`, status)
		require.NoErrorf(t, err, "release transition to %s", status)
	}
	_, err := db.Exec(`UPDATE production_releases SET status = 'active' WHERE id = 'release-1'`)
	require.ErrorContains(t, err, "invalid production release status transition")

	insertProductionPublicationTarget(t, db, "target-1", "release-1", "version-1", "kb-1", "knowledge-1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, err = db.Exec(`INSERT INTO production_release_targets (id, release_id, tenant_id, project_id, document_id, version_id, target_knowledge_base_id, knowledge_id, release_digest) VALUES ('target-digest-mismatch', 'release-1', 1, 'project-1', 'document-1', 'version-1', 'kb-2', 'knowledge-digest-mismatch', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb')`)
	require.Error(t, err, "target release digests are bound to their parent release")
	_, err = db.Exec(`UPDATE production_release_targets SET status = 'ready' WHERE id = 'target-1'`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_projection_heads (tenant_id, document_id, target_knowledge_base_id, active_release_target_id) VALUES (1, 'document-1', 'kb-1', 'target-1')`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_release_targets SET cleanup_requested_at = CURRENT_TIMESTAMP WHERE id = 'target-1'`)
	require.ErrorContains(t, err, "active production release targets cannot be cleaned")
	approveProductionPublicationReview(t, db, "review-pub-2", "version-pub-2", "step-pub-2")
	insertProductionPublicationRelease(t, db, "release-2", "version-pub-2", "review-pub-2", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	insertProductionPublicationTarget(t, db, "target-2", "release-2", "version-pub-2", "kb-1", "knowledge-2", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	_, err = db.Exec(`UPDATE production_release_targets SET status = 'ready' WHERE id = 'target-2'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_projection_heads SET active_release_target_id = 'target-2', lock_version = 2 WHERE tenant_id = 1 AND document_id = 'document-1' AND target_knowledge_base_id = 'kb-1'`)
	require.NoError(t, err)
	var oldTargetStatus, newTargetStatus string
	var lockVersion int
	require.NoError(t, db.QueryRow(`SELECT status FROM production_release_targets WHERE id = 'target-1'`).Scan(&oldTargetStatus))
	require.NoError(t, db.QueryRow(`SELECT status FROM production_release_targets WHERE id = 'target-2'`).Scan(&newTargetStatus))
	require.NoError(t, db.QueryRow(`SELECT lock_version FROM production_projection_heads WHERE tenant_id = 1 AND document_id = 'document-1' AND target_knowledge_base_id = 'kb-1'`).Scan(&lockVersion))
	require.Equal(t, "rolled_back", oldTargetStatus)
	require.Equal(t, "active", newTargetStatus)
	require.Equal(t, 2, lockVersion)
	_, err = db.Exec(`UPDATE production_release_targets SET status = 'rolled_back', activated_at = NULL, rolled_back_at = '2020-01-01 00:00:00', retention_until = '2020-01-31 00:00:00' WHERE id = 'target-2'`)
	require.ErrorContains(t, err, "active projection heads")
	var activeTargets int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM production_release_targets WHERE tenant_id = 1 AND document_id = 'document-1' AND target_knowledge_base_id = 'kb-1' AND status = 'active'`).Scan(&activeTargets))
	require.Equal(t, 1, activeTargets)

	_, err = db.Exec(`UPDATE production_release_targets SET status = 'building', activated_at = NULL, rolled_back_at = NULL, retention_until = NULL WHERE id = 'target-1'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_release_targets SET status = 'ready' WHERE id = 'target-1'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_release_targets SET status = 'active', activated_at = CURRENT_TIMESTAMP WHERE id = 'target-1'`)
	require.ErrorContains(t, err, "require projection head activation")
	for _, assignment := range []string{
		"failed_at = CURRENT_TIMESTAMP",
		"rolled_back_at = CURRENT_TIMESTAMP",
		"cleanup_requested_at = CURRENT_TIMESTAMP",
		"cleaned_at = CURRENT_TIMESTAMP",
		"retention_until = CURRENT_TIMESTAMP",
	} {
		_, err = db.Exec(`UPDATE production_release_targets SET ` + assignment + ` WHERE id = 'target-2'`)
		require.ErrorContainsf(t, err, "active production release targets cannot be cleaned", assignment)
	}

	insertProductionPublicationTarget(t, db, "target-trigger-failure", "release-1", "version-1", "kb-3", "knowledge-trigger-failure", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, err = db.Exec(`UPDATE production_projection_heads SET active_release_target_id = 'target-trigger-failure', lock_version = 3 WHERE tenant_id = 1 AND document_id = 'document-1' AND target_knowledge_base_id = 'kb-1'`)
	require.ErrorContains(t, err, "require ready or active targets")
	var activeHead string
	require.NoError(t, db.QueryRow(`SELECT active_release_target_id FROM production_projection_heads WHERE tenant_id = 1 AND document_id = 'document-1' AND target_knowledge_base_id = 'kb-1'`).Scan(&activeHead))
	require.Equal(t, "target-2", activeHead)

	insertProductionPublicationTarget(t, db, "target-retention", "release-1", "version-1", "kb-2", "knowledge-retention", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, err = db.Exec(`UPDATE production_release_targets
		SET status = 'failed', failure_code = 'PROJECTION_BUILD_FAILED', failure_reason = 'projection build failed',
		    failed_at = '2099-01-01 00:00:00', retention_until = '2099-01-31 00:00:00'
		WHERE id = 'target-retention'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_release_targets SET status = 'cleanup_pending', cleanup_requested_at = CURRENT_TIMESTAMP WHERE id = 'target-retention'`)
	require.ErrorContains(t, err, "retention has not expired")
	_, err = db.Exec(`UPDATE production_release_targets SET failed_at = '2020-01-01 00:00:00', retention_until = '2020-01-31 00:00:00' WHERE id = 'target-retention'`)
	require.ErrorContains(t, err, "retention_until is immutable")

	insertProductionPublicationTarget(t, db, "target-retention-expired", "release-2", "version-pub-2", "kb-3", "knowledge-retention-expired", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	_, err = db.Exec(`UPDATE production_release_targets
		SET status = 'failed', failure_code = 'PROJECTION_BUILD_FAILED', failure_reason = 'projection build failed',
		    failed_at = '2020-01-01 00:00:00', retention_until = '2020-01-31 00:00:00'
		WHERE id = 'target-retention-expired'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_release_targets SET status = 'cleanup_pending', cleanup_requested_at = CURRENT_TIMESTAMP WHERE id = 'target-retention-expired'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_release_targets SET status = 'cleaned', cleaned_at = CURRENT_TIMESTAMP WHERE id = 'target-retention-expired'`)
	require.NoError(t, err)

	for _, query := range []string{
		`EXPLAIN QUERY PLAN SELECT id FROM production_release_targets WHERE tenant_id = 1 AND target_knowledge_base_id = 'kb-1' AND status = 'active'`,
		`EXPLAIN QUERY PLAN SELECT id FROM production_release_targets WHERE status = 'failed' AND retention_until <= CURRENT_TIMESTAMP`,
	} {
		rows, err := db.Query(query)
		require.NoError(t, err)
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
			plan = append(plan, detail)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		require.Contains(t, strings.Join(plan, "\n"), map[string]string{
			`EXPLAIN QUERY PLAN SELECT id FROM production_release_targets WHERE tenant_id = 1 AND target_knowledge_base_id = 'kb-1' AND status = 'active'`: "idx_production_release_targets_scope_status",
			`EXPLAIN QUERY PLAN SELECT id FROM production_release_targets WHERE status = 'failed' AND retention_until <= CURRENT_TIMESTAMP`:                "idx_production_release_targets_cleanup_eligibility",
		}[query])
	}
}

func TestProductionPublicationSQLiteMigrationEnumeratesReleaseStatusEdges(t *testing.T) {
	db := openProductionPublicationSQLite(t)
	seedProductionReleaseScope(t, db)

	accepted := []struct{ from, to string }{
		{"building", "ready"},
		{"building", "failed"},
		{"building", "rolled_back"},
		{"ready", "active"},
		{"ready", "failed"},
		{"ready", "rolled_back"},
		{"active", "failed"},
		{"active", "rolled_back"},
		{"failed", "building"},
		{"failed", "rolled_back"},
		{"failed", "cleanup_pending"},
		{"rolled_back", "building"},
		{"rolled_back", "cleanup_pending"},
		{"cleanup_pending", "cleaned"},
	}
	for index, edge := range accepted {
		releaseID := insertProductionPublicationTransitionRelease(t, db, index)
		setProductionReleaseStatus(t, db, releaseID, edge.from)
		_, err := db.Exec(`UPDATE production_releases SET status = ? WHERE id = ?`, edge.to, releaseID)
		require.NoErrorf(t, err, "accepted release edge %s -> %s", edge.from, edge.to)
	}

	rejected := []struct{ from, to string }{
		{"building", "active"},
		{"ready", "building"},
		{"active", "ready"},
		{"failed", "active"},
		{"rolled_back", "active"},
		{"cleanup_pending", "building"},
		{"cleaned", "building"},
	}
	for index, edge := range rejected {
		releaseID := insertProductionPublicationTransitionRelease(t, db, index+len(accepted))
		setProductionReleaseStatus(t, db, releaseID, edge.from)
		_, err := db.Exec(`UPDATE production_releases SET status = ? WHERE id = ?`, edge.to, releaseID)
		require.ErrorContainsf(t, err, "invalid production release status transition", "rejected release edge %s -> %s", edge.from, edge.to)
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

func openProductionReviewSQLite(t *testing.T) *sql.DB {
	t.Helper()

	db := openProductionRunsSQLite(t)
	_, err := db.Exec(mustReadMigration(t, "../../migrations/sqlite/000004_knowledge_production_reviews.up.sql"))
	require.NoError(t, err)
	return db
}

func openProductionPublicationSQLite(t *testing.T) *sql.DB {
	t.Helper()

	db := openProductionReviewSQLite(t)
	_, err := db.Exec(`CREATE TABLE knowledge_bases (
        id VARCHAR(36) PRIMARY KEY,
        tenant_id INTEGER NOT NULL,
        UNIQUE(id, tenant_id)
    )`)
	require.NoError(t, err)
	_, err = db.Exec(mustReadMigration(t, "../../migrations/sqlite/000005_knowledge_production_publication.up.sql"))
	require.NoError(t, err)
	return db
}

func openProductionProjectionIntegritySQLite(t *testing.T) *sql.DB {
	t.Helper()
	return openProductionPublicationSQLite(t)
}

func insertProductionPublicationRelease(t *testing.T, db *sql.DB, id, versionID, reviewID, digest string) {
	t.Helper()

	_, err := db.Exec(`INSERT INTO production_releases (id, tenant_id, project_id, document_id, version_id, review_request_id, release_digest, created_by) VALUES (?, 1, 'project-1', 'document-1', ?, ?, ?, 'owner-1')`, id, versionID, reviewID, digest)
	require.NoError(t, err)
}

func insertProductionPublicationTransitionRelease(t *testing.T, db *sql.DB, sequence int) string {
	t.Helper()

	versionID := fmt.Sprintf("version-release-edge-%d", sequence)
	reviewID := fmt.Sprintf("review-release-edge-%d", sequence)
	releaseID := fmt.Sprintf("release-edge-%d", sequence)
	insertProductionVersion(t, db, versionID, "document-1", 1, "project-1", 100+sequence, "source-set-1", "")
	insertProductionReviewRequest(t, db, reviewID, versionID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "{}")
	approveProductionPublicationReview(t, db, reviewID, versionID, fmt.Sprintf("step-release-edge-%d", sequence))
	insertProductionPublicationRelease(t, db, releaseID, versionID, reviewID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	return releaseID
}

func setProductionReleaseStatus(t *testing.T, db *sql.DB, releaseID, status string) {
	t.Helper()

	for _, next := range map[string][]string{
		"building":        nil,
		"ready":           {"ready"},
		"active":          {"ready", "active"},
		"failed":          {"failed"},
		"rolled_back":     {"rolled_back"},
		"cleanup_pending": {"failed", "cleanup_pending"},
		"cleaned":         {"failed", "cleanup_pending", "cleaned"},
	}[status] {
		_, err := db.Exec(`UPDATE production_releases SET status = ? WHERE id = ?`, next, releaseID)
		require.NoError(t, err)
	}
}

func insertProductionPublicationTarget(t *testing.T, db *sql.DB, id, releaseID, versionID, knowledgeBaseID, knowledgeID, digest string) {
	t.Helper()

	_, err := db.Exec(`INSERT INTO production_release_targets (id, release_id, tenant_id, project_id, document_id, version_id, target_knowledge_base_id, knowledge_id, release_digest) VALUES (?, ?, 1, 'project-1', 'document-1', ?, ?, ?, ?)`, id, releaseID, versionID, knowledgeBaseID, knowledgeID, digest)
	require.NoError(t, err)
}

func approveProductionPublicationReview(t *testing.T, db *sql.DB, reviewID, versionID, stepID string) {
	t.Helper()

	_, err := db.Exec(`INSERT INTO production_review_steps (id, review_request_id, tenant_id, project_id, document_id, version_id, required_role, sequence) VALUES (?, ?, 1, 'project-1', 'document-1', ?, 'business_reviewer', 1)`, stepID, reviewID, versionID)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_review_steps SET decision = 'approved', reviewer_user_id = 'business-1', comment = 'approved', decided_at = CURRENT_TIMESTAMP WHERE id = ?`, stepID)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_review_requests SET status = 'approved', terminal_by = 'owner-1', completed_at = CURRENT_TIMESTAMP WHERE id = ?`, reviewID)
	require.NoError(t, err)
}

func seedProductionReleaseScope(t *testing.T, db *sql.DB) {
	t.Helper()

	seedProductionRunScopes(t, db)
	insertProductionVersion(t, db, "version-pub-2", "document-1", 1, "project-1", 2, "source-set-1", "version-1")
	_, err := db.Exec(`INSERT INTO knowledge_bases (id, tenant_id) VALUES ('kb-1', 1), ('kb-2', 1), ('kb-3', 1), ('kb-other-tenant', 2)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO production_project_members (project_id, user_id, role, assigned_by) VALUES ('project-1', 'business-1', 'business_reviewer', 'owner-1')`)
	require.NoError(t, err)
	insertProductionReviewRequest(t, db, "review-pub-1", "version-1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "{}")
	_, err = db.Exec(`INSERT INTO production_review_steps (id, review_request_id, tenant_id, project_id, document_id, version_id, required_role, sequence) VALUES ('step-pub-1', 'review-pub-1', 1, 'project-1', 'document-1', 'version-1', 'business_reviewer', 1)`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_review_steps SET decision = 'approved', reviewer_user_id = 'business-1', comment = 'approved', decided_at = CURRENT_TIMESTAMP WHERE id = 'step-pub-1'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE production_review_requests SET status = 'approved', terminal_by = 'owner-1', completed_at = CURRENT_TIMESTAMP WHERE id = 'review-pub-1'`)
	require.NoError(t, err)
	insertProductionReviewRequest(t, db, "review-pub-2", "version-pub-2", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "{}")
}

func seedProductionReviewFixture(t *testing.T, db *sql.DB) {
	t.Helper()

	seedProductionRunScopes(t, db)
	insertProductionVersion(t, db, "version-review-fixture", "document-1", 1, "project-1", 3, "source-set-1", "")
	insertProductionBlock(t, db, "block-review-fixture", "version-review-fixture", "block-review-fixture")
	_, err := db.Exec(`INSERT INTO production_project_members (project_id, user_id, role, assigned_by) VALUES ('project-1', 'business-1', 'business_reviewer', 'owner-1'), ('project-1', 'engineering-1', 'engineering_reviewer', 'owner-1')`)
	require.NoError(t, err)
}

func insertProductionReviewRequest(t *testing.T, db *sql.DB, id, versionID, digest, snapshot string) {
	t.Helper()

	_, err := db.Exec(`INSERT INTO production_review_requests (id, tenant_id, project_id, document_id, version_id, policy_snapshot, policy_digest, submitted_by) VALUES (?, 1, 'project-1', 'document-1', ?, ?, ?, 'author-1')`, id, versionID, snapshot, digest)
	require.NoError(t, err)
}

func insertProductionReviewStep(t *testing.T, db *sql.DB, id, reviewID, role string, sequence int) {
	t.Helper()

	_, err := db.Exec(`INSERT INTO production_review_steps (id, review_request_id, tenant_id, project_id, document_id, version_id, required_role, sequence) VALUES (?, ?, 1, 'project-1', 'document-1', 'version-review-fixture', ?, ?)`, id, reviewID, role, sequence)
	require.NoError(t, err)
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
		{name: "id", assignment: "id = 'call-changed'"},
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

func sqliteColumnTypeIfPresent(t *testing.T, db *sql.DB, table, column string) string {
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
