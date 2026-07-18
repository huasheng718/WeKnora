package repository

import (
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
)

type sqlStateError interface {
	SQLState() string
}

func translateProductionWriteError(err error) error {
	if err == nil || !isProductionDuplicateKey(err) {
		return err
	}
	return errors.Join(types.ErrProductionConflict, err)
}

func isProductionDuplicateKey(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var state sqlStateError
	if errors.As(err, &state) && state.SQLState() == "23505" {
		return true
	}
	var sqliteError sqlite3.Error
	if errors.As(err, &sqliteError) {
		return sqliteError.ExtendedCode == sqlite3.ErrConstraintUnique ||
			sqliteError.ExtendedCode == sqlite3.ErrConstraintPrimaryKey
	}
	var sqliteErrorPointer *sqlite3.Error
	return errors.As(err, &sqliteErrorPointer) && sqliteErrorPointer != nil &&
		(sqliteErrorPointer.ExtendedCode == sqlite3.ErrConstraintUnique ||
			sqliteErrorPointer.ExtendedCode == sqlite3.ErrConstraintPrimaryKey)
}
