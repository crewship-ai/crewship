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
	// cache_size(-65536) = 64 MiB per-connection page cache. SetMaxOpenConns
	// caps the pool at 5 (see below), so worst case is ~320 MiB resident —
	// trivial against the VMs we target. 64 MiB keeps the hot working set of
	// journal_entries + agents + missions resident through dashboard polls
	// instead of round-tripping page reads from disk on every refresh.
	// Bumping further (e.g. 256 MiB) buys diminishing returns unless the
	// DB grows past ~500 MiB.
	dsn := path + sep + "_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=cache_size(-65536)" +
		"&_pragma=temp_store(MEMORY)" +
		"&_pragma=mmap_size(268435456)" +
		"&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// SQLite supports one concurrent writer, but WAL mode (enabled
	// above) lets readers run alongside the writer without blocking.
	// The previous cap of 2 connections forced even read-only API
	// hits to serialize against any in-flight write, which manifested
	// as request stalls under modest concurrent dashboard load
	// (4–5 simultaneous tabs polling /api/v1/missions etc).
	//
	// 5 connections gives ~4 concurrent readers + 1 writer, which is
	// what WAL is designed for. Going higher buys no extra writer
	// throughput (writers still serialize via busy_timeout) and just
	// pads memory; lower than 5 reintroduces the dashboard-tab stall.
	// busy_timeout(5000ms) applies per-connection via the DSN pragma
	// above, so it stays in effect at any pool size.
	db.SetMaxOpenConns(5)

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
