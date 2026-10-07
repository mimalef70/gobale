//go:build !purego

package sqlite

import (
	"errors"
	"github.com/mattn/go-sqlite3"
)

// ErrorCategory deliberately projects no driver error text or SQL.
func ErrorCategory(err error) string {
	var e sqlite3.Error
	if errors.As(err, &e) {
		return errorCategory(int(e.Code))
	}
	return "other"
}
