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
		// 0700: the data directory holds the SQLite file plus WAL/SHM sidecars,
		// which contain encrypted credentials and bcrypt hashes. No other local
		// user has business reading it.
		//
		// os.MkdirAll only applies its mode to directories it CREATES — if
		// the directory already exists (e.g. an upgrade from an earlier build
		// that created it at 0755), MkdirAll is a no-op and the loose perms
		// stick around. Follow up with an explicit Chmod so both fresh
		// installs and upgrades end up at 0700. Chmod is idempotent.
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return nil, fmt.Errorf("chmod database directory: %w", err)
		}
	}

	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	dsn := path + sep + "_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=cache_size(-20000)" +
		"&_pragma=temp_store(MEMORY)" +
		"&_pragma=mmap_size(268435456)" +
		"&_txlock=immediate"
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

	// Tighten permissions on the DB file itself (and its WAL/SHM sidecars if
	// present). Chmod is a no-op if the file already has the desired mode, so
	// this is safe to run on every boot. We only attempt it when the path
	// points to a real filesystem entry — in-memory and shared-cache paths
	// (":memory:", "file::memory:?cache=shared", etc.) are left alone.
	//
	// parseDSN only strips "file:"/"//" prefixes, so path may still carry a
	// query string (e.g. "./foo.db?cache=shared"). Strip it before stat/chmod
	// or we'd silently skip the chmod on any DSN with parameters.
	filePath := path
	if i := strings.IndexByte(filePath, '?'); i >= 0 {
		filePath = filePath[:i]
	}
	if _, statErr := os.Stat(filePath); statErr == nil {
		_ = os.Chmod(filePath, 0600)
		_ = os.Chmod(filePath+"-wal", 0600)
		_ = os.Chmod(filePath+"-shm", 0600)
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

