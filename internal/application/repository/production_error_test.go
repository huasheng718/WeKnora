package repository

import (
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTranslateProductionWriteErrorRecognizesStructuredDuplicateKeys(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "gorm", err: gorm.ErrDuplicatedKey},
		{name: "postgres", err: &pgconn.PgError{Code: "23505", Message: "duplicate key"}},
		{name: "sqlite unique", err: sqlite3.Error{Code: sqlite3.ErrConstraint, ExtendedCode: sqlite3.ErrConstraintUnique}},
		{name: "sqlite primary key", err: sqlite3.Error{Code: sqlite3.ErrConstraint, ExtendedCode: sqlite3.ErrConstraintPrimaryKey}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.ErrorIs(t, translateProductionWriteError(test.err), types.ErrProductionConflict)
		})
	}
}

func TestTranslateProductionWriteErrorPreservesNonUniqueFailure(t *testing.T) {
	original := errors.New("database unavailable")

	translated := translateProductionWriteError(original)

	require.ErrorIs(t, translated, original)
	require.NotErrorIs(t, translated, types.ErrProductionConflict)
}
