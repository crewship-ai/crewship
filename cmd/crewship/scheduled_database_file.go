//go:build !clionly

package main

import (
	"database/sql"
	"errors"
	"strings"
)

// mainDatabaseFile gets the resolved path from SQLite rather than parsing the
// operator's DSN (which may have URI parameters or a relative path).
func mainDatabaseFile(db *sql.DB) (string, error) {
	if db == nil {
		return "", errors.New("database is not configured")
	}
	rows, err := db.Query(`PRAGMA database_list`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var name, path sql.NullString
		if err := rows.Scan(&seq, &name, &path); err != nil {
			return "", err
		}
		if name.String == "main" && strings.TrimSpace(path.String) != "" {
			return strings.TrimSpace(path.String), nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", errors.New("main SQLite database has no file path")
}
