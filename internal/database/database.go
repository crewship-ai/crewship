package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// DB wraps sql.DB with the resolved database file path. It uses the "sqlite"
// driver from modernc.org/sqlite with WAL mode and foreign keys enabled.
type DB struct {
	*sql.DB
	path string
}

// Open parses the given database URL (e.g. "file:/path/to/db"), creates the
// parent directory if needed, and opens an SQLite connection with WAL mode,
// foreign keys, and a 5-second busy timeout.
func Open(databaseURL string) (*DB, error) {
	path, err := parseDSN(databaseURL)
	if err != nil {
		return nil, err
	}

	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	dsn := path + sep + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=cache_size(-20000)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// SQLite supports only one concurrent writer. Limiting open connections
	// ensures busy_timeout and other pragmas apply to every connection in the pool.
	db.SetMaxOpenConns(2)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	return &DB{DB: db, path: path}, nil
}

// Path returns the resolved filesystem path of the SQLite database file.
func (d *DB) Path() string {
	return d.path
}

func parseDSN(dsn string) (string, error) {
	if dsn == "" {
		return "", fmt.Errorf("DATABASE_URL is empty")
	}
	dsn = strings.TrimPrefix(dsn, "file:")
	dsn = strings.TrimPrefix(dsn, "//")
	if dsn == "" {
		return "", fmt.Errorf("DATABASE_URL has no path after 'file:'")
	}
	return dsn, nil
}
