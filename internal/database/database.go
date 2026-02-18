package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
	path string
}

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

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	if err := applyPragmas(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply pragmas: %w", err)
	}

	return &DB{DB: db, path: path}, nil
}

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

func applyPragmas(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA cache_size=-20000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}
