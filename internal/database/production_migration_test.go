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
