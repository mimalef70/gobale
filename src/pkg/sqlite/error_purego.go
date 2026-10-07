//go:build purego

package sqlite

import (
	"errors"
	"modernc.org/sqlite"
)

// ErrorCategory deliberately projects no driver error text or SQL.
func ErrorCategory(err error) string {
	var e *sqlite.Error
	if errors.As(err, &e) {
		return errorCategory(e.Code() & 255)
	}
	return "other"
}
