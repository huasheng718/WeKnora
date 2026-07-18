package database

import (
	"os"
	"testing"

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
