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
	// caps the pool at 5 (see below). The "~320 MiB worst case" this comment
	// used to quote was wrong in both directions, corrected under #1817 by
	// measurement: while the database fits inside mmap_size below, reads come
	// off the mapping and the cache stays nearly empty (5 connections, 15 MiB
	// physical); past the mmap window the cache does fill, and 64 MiB nominal
	// costs ~145 MiB resident, because modernc.org/memory rounds each ~4 KiB
	// cache page up to an 8 KiB slab. Five connections on a 548 MiB database
	// measured at 725 MiB. 64 MiB keeps the hot working set of
	// journal_entries + agents + missions resident through dashboard polls
	// instead of round-tripping page reads from disk on every refresh.
	// Bumping further (e.g. 256 MiB) buys diminishing returns unless the
	// DB grows past ~500 MiB.
	// busy_timeout(30000) bumped from 5s after the 2026-05-25 DR
	// validation surfaced a real "login fails with SQLITE_BUSY"
	// scenario: port_expose_registry purge (runs every 30s) holds
	// the writer lock long enough that a concurrent login lockout
	// check (which writes failed_login_count / locked_until on
	// users) blew past the 5s limit and surfaced as "Invalid email
	// or password" in the UI — a confusing message for what is
	// actually a transient lock contention. 30s matches the
	// background purge period so a worst-case login retry waits
	// out one full cycle instead of dying mid-cycle.
	dsn := path + sep + "_pragma=busy_timeout(30000)" +
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

	// Idle must match the open cap, or three of those five connections are
	// torn down the moment a burst drains and reopened on the next one.
	// database/sql defaults MaxIdleConns to 2 and this call was missing until
	// #1817, so that is exactly what production did.
	//
	// Measured on an M4, 235 MiB corpus, 5 concurrent readers (benchmarks in
	// pool_test.go, numbers in PR #1821):
	//
	//   - One reopen costs 386 µs: a fresh file open plus the seven DSN
	//     pragmas above, WAL handshake and mmap window included, against
	//     16 µs for the same statement on a warm connection.
	//   - Bursty load — the several-dashboard-tabs-polling pattern this pool
	//     is sized for — ran 2.6x faster warm: 435 µs vs 169 µs per 5-query
	//     burst, 11.5k vs 29.6k queries/s. Under *sustained* saturation the
	//     gap narrows to ~1.2x, because database/sql hands a released
	//     connection straight to a waiting goroutine and never consults the
	//     idle pool. The churn only bites when a burst drains.
	//
	// The obvious objection is memory: cache_size(-65536) is 64 MiB per
	// connection, so five warm connections look like 320 MiB on a self-hosted
	// box. Measured, they are not:
	//
	//   - While the database fits inside mmap_size (256 MiB), SQLite serves
	//     reads straight from the mapping and barely touches the pager cache.
	//     Five warm connections cost 15.3 MiB of physical footprint against
	//     13.2 MiB for two — 0.7 MiB per extra connection. RSS reads ~1.2 GiB
	//     because it counts the same file mapped five times; those pages are
	//     clean, file-backed, shared by the kernel and reclaimed under
	//     pressure. RSS is the wrong number here.
	//   - Past the mmap window the pager cache does fill: on a 548 MiB
	//     database five warm connections held 725 MiB. But that peak belongs
	//     to SetMaxOpenConns, not to this line — any five-way burst opens five
	//     connections and fills five caches whatever the idle cap says. When
	//     the burst drained at idle 2, closing three connections returned
	//     21 MiB of the 725 (704 MiB vs 724 MiB at idle 5): modernc.org/memory
	//     does not hand freed slabs back to the OS. The low idle cap buys ~3%
	//     of the memory it appears to and pays 2.6x on burst latency for it.
	//
	// SetConnMaxLifetime and SetConnMaxIdleTime stay unset, deliberately. The
	// usual reasons to cap connection lifetime — load balancers cutting idle
	// connections, server-side timeouts, rebalancing onto a new replica — all
	// describe a database reached over a network. This one is a file on the
	// local disk: nothing expires it, and recycling would only re-pay the
	// 386 µs open. ConnMaxIdleTime would be the one candidate, as a valve to
	// give the pager cache back on a quiet box, but the release measurement
	// above shows there is almost nothing to give back. If large-database
	// memory becomes a real problem, the lever is cache_size or mmap_size, not
	// connection expiry.
	db.SetMaxIdleConns(5)

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
