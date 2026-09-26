package database

import (
	"database/sql/driver"
	"strings"

	"modernc.org/sqlite"
)

// SQLite's built-in lower() folds ASCII only. Chat names and titles use the
// same Unicode lowercasing as the UI; register before any pooled connection opens.
func init() {
	if err := sqlite.RegisterDeterministicScalarFunction("crewship_casefold", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if args[0] == nil {
			return "", nil
		}
		value, _ := args[0].(string)
		return strings.ToLower(value), nil
	}); err != nil {
		panic(err)
	}
}
