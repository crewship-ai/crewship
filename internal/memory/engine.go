package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Config controls memory engine behavior.
type Config struct {
	MaxSizeMB int // max total memory size in MB (default: 10)
	// DailyMaxKB is informational only — the engine never enforces it. The
	// effective daily-log cap is capDailyBytes in tools.go (30 KB, PR-A F1),
	// mirrored by the legacy sidecar path's dailyCap (memory_write.go). Kept
	// aligned at 30 so nobody reads a stale 100 out of this struct.
	DailyMaxKB    int  // max daily log size in KB (default: 30)
	SearchEnabled bool // enable FTS5 search (default: true)
}

// DefaultConfig returns sensible defaults for agent memory.
func DefaultConfig() Config {
	return Config{
		MaxSizeMB:     10,
		DailyMaxKB:    30,
		SearchEnabled: true,
	}
}

// Status reports the current state of the memory engine.
type Status struct {
	TotalFiles  int       `json:"total_files"`
	TotalChunks int       `json:"total_chunks"`
	IndexedAt   time.Time `json:"indexed_at"`
	TotalSizeKB int64     `json:"total_size_kb"`
	SearchReady bool      `json:"search_ready"`
}

// Engine provides FTS5-backed search over agent memory files.
type Engine struct {
	basePath string
	db       *sql.DB
	mu       sync.RWMutex
	config   Config

	// fileHashes tracks the last-indexed content hash per file (keyed by the
	// file's path relative to basePath, exactly as stored in the memory_chunks
	// `file` column). It powers the incremental reindex fast path (ReindexPath):
	// a write whose content matches the recorded hash is a no-op, and a changed
	// file only ever re-chunks itself — never the whole corpus. ReindexContext
	// rebuilds this map wholesale. Guarded by e.mu. It is intentionally in
	// memory only: on process restart the sidecar runs a full Reindex at boot,
	// which reseeds it, so a cold map just means the first per-file write does
	// the (still O(file), not O(corpus)) re-chunk it would have done anyway.
	fileHashes map[string]string

	// writesSinceOptimize counts committed ReindexPath calls since the last
	// FTS5 `optimize`. Guarded by e.mu, like fileHashes, and in memory only:
	// losing the count on restart just means the next optimize is at most
	// optimizeEveryNWrites writes late, and the sidecar's boot Reindex
	// optimizes anyway. See optimizeEveryNWrites.
	writesSinceOptimize int
}

// New creates a memory engine for the given base path (e.g. /output/{agent}/.memory/).
// The FTS5 index is stored as index.sqlite inside the base path.
func New(basePath string, cfg Config) (*Engine, error) {
	if cfg.MaxSizeMB == 0 {
		cfg.MaxSizeMB = 10
	}
	if cfg.DailyMaxKB == 0 {
		cfg.DailyMaxKB = 30
	}

	dbPath := filepath.Join(basePath, "index.sqlite")
	// synchronous(NORMAL) + temp_store(MEMORY) mirror the main DB's rationale
	// (database.go): NORMAL drops an fsync per write and only risks losing the
	// last few transactions on an OS-level crash, never corruption. That is safe
	// here because this per-agent index is fully rebuilt from the memory files at
	// boot, so a lost tail is reconstructed on the next Reindex.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)")
	if err != nil {
		return nil, fmt.Errorf("open memory index: %w", err)
	}

	if err := initSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("init memory schema: %w", err)
	}

	return &Engine{
		basePath:   basePath,
		db:         db,
		config:     cfg,
		fileHashes: make(map[string]string),
	}, nil
}

// memoryChunksDDL is the ONE definition of the FTS5 index. It is written
// verbatim on a fresh database and compared verbatim against
// sqlite_master.sql on an existing one (see chunksSchemaIsCurrent), so
// changing this constant is what triggers a rebuild of every already-built
// index — there is no second place to remember to bump.
//
// `file UNINDEXED` is load-bearing and not cosmetic (#1678): with `file`
// searchable, the token "md" matched every chunk of every markdown file and
// "daily" matched every daily note. Under the old implicit-AND query builder
// that stayed invisible (an extra required term only makes a query stricter);
// under the OR builder in search.go it would return the whole tier for any
// question containing a path word. Path-scoped search is the `tier` argument
// on memory.search, not a silent term in the free-text match.
//
// No "IF NOT EXISTS": initSchema decides whether to create, and SQLite
// strips the clause from sqlite_master.sql anyway, which would break the
// verbatim comparison.
const memoryChunksDDL = `CREATE VIRTUAL TABLE memory_chunks USING fts5(
	file UNINDEXED,
	content,
	tokenize='unicode61'
)`

// chunksSchemaIsCurrent reports whether a memory_chunks definition read out
// of sqlite_master matches memoryChunksDDL. The comparison is on normalised
// whitespace only: SQLite stores the CREATE statement's text verbatim apart
// from an `IF NOT EXISTS` clause, so this recognises exactly what this code
// writes and nothing else.
//
// Comparing against the DDL rather than against a version counter in
// memory_meta is deliberate. A counter can disagree with the table it
// claims to describe — a database restored from a backup, or written by an
// older binary that never had the row — and the failure is silent: the
// index keeps the old column definition while claiming to be current.
func chunksSchemaIsCurrent(storedSQL string) bool {
	return normaliseDDL(storedSQL) == normaliseDDL(memoryChunksDDL)
}

func normaliseDDL(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// optimizeEveryNWrites is how many ReindexPath calls pass between FTS5
// `optimize` runs (#1678 §7.3).
//
// FTS5 never rewrites its index in place: every ReindexPath is a DELETE
// (which appends tombstones) plus N INSERTs (which append segments), so a
// long-lived agent's index fragments monotonically and queries slow down
// against data that never changed. Measured on one daily note rewritten
// 3000 times — the exact shape memory.write produces — the index went from
// 197µs to 36µs per query for 3ms of total optimize work.
//
// The cadence is a cost/benefit pick, not a tuned constant. `optimize` is
// O(index), so running it per write would dominate an 86µs write; running
// it never is what we have today. 200 writes is ~1% overhead on the write
// path at the measured cost, and bounds fragmentation to roughly one
// merge's worth. It is also well inside a single agent's daily write
// volume, so an index that is quiet overnight is never left fragmented for
// long. Nothing depends on the exact value: it changes when maintenance
// happens, never what the index contains.
const optimizeEveryNWrites = 200

// initSchema brings an index.sqlite up to memoryChunksDDL, creating it if
// it does not exist and rebuilding it in place if it was created by an
// older definition.
//
// The rebuild is the part that is easy to get wrong, and both wrong answers
// are worse than the bug they fix:
//
//   - `CREATE VIRTUAL TABLE IF NOT EXISTS` — what this used to do — leaves
//     an existing table alone. Every index.sqlite written before #1678
//     would keep `file` as a SEARCHABLE column forever, so the OR query
//     builder would ship without its prerequisite and hand back a whole
//     tier for any question containing a path word.
//   - DROP + CREATE without carrying the rows over would empty the index
//     until something ran a full Reindex. The sidecar does run one at boot
//     (sidecar/server.go), but it is not the only caller of New — the CLI's
//     `crewship memory` path and the workspace tier construct engines too —
//     so "the caller will reindex" is not a property this function may
//     assume.
//
// So the rows are copied through a plain staging table and re-inserted into
// the new virtual table, all inside one transaction: a crash mid-migration
// leaves either the old index or the new one, never an empty one. The
// content is identical either way — (file, content) is all memory_chunks
// has ever stored — so nothing is lost and no reindex is required.
func initSchema(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS memory_meta (
			key TEXT PRIMARY KEY,
			value TEXT
		);
	`); err != nil {
		return err
	}

	stored, err := storedChunksDDL(db)
	if err != nil {
		return err
	}
	if stored == "" {
		_, err := db.Exec(memoryChunksDDL)
		return err
	}
	if chunksSchemaIsCurrent(stored) {
		return nil
	}
	return rebuildChunksSchema(db)
}

// storedChunksDDL returns the CREATE statement SQLite recorded for
// memory_chunks, or "" if the table does not exist yet.
func storedChunksDDL(db *sql.DB) (string, error) {
	var sqlText sql.NullString
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'memory_chunks'`,
	).Scan(&sqlText)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read memory_chunks schema: %w", err)
	}
	return sqlText.String, nil
}

// rebuildChunksSchema recreates memory_chunks with the current definition,
// carrying every row across. See initSchema for why it is shaped this way.
func rebuildChunksSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin memory_chunks rebuild: %w", err)
	}
	defer tx.Rollback()

	// Re-read inside the transaction: two processes opening the same index
	// concurrently would otherwise both act on a stale reading and the
	// second would redo the work.
	var stored sql.NullString
	err = tx.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'memory_chunks'`,
	).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.Exec(memoryChunksDDL); err != nil {
			return fmt.Errorf("create memory_chunks: %w", err)
		}
		return tx.Commit()
	}
	if err != nil {
		return fmt.Errorf("read memory_chunks schema: %w", err)
	}
	if chunksSchemaIsCurrent(stored.String) {
		return tx.Commit()
	}

	// A plain table, not a rename: ALTER TABLE ... RENAME rewrites the
	// recorded CREATE statement (it quotes the new name), which would stop
	// chunksSchemaIsCurrent from recognising its own output and re-migrate
	// the index on every open.
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS memory_chunks_rebuild`,
		`CREATE TABLE memory_chunks_rebuild (file TEXT, content TEXT)`,
		`INSERT INTO memory_chunks_rebuild (file, content) SELECT file, content FROM memory_chunks`,
		`DROP TABLE memory_chunks`,
		memoryChunksDDL,
		`INSERT INTO memory_chunks (file, content) SELECT file, content FROM memory_chunks_rebuild`,
		`DROP TABLE memory_chunks_rebuild`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("rebuild memory_chunks: %w", err)
		}
	}
	return tx.Commit()
}

// Status returns information about the memory index state.
//
// No e.mu RLock is held over the DB reads: SQLite is opened in WAL mode
// and Reindex writes inside a single transaction, so the SELECTs below
// see a consistent snapshot of the index. The filesystem walk for the
// directory size happens after the DB reads — it has nothing to do with
// the index and adding it under any kind of lock would just extend the
// window that delays Reindex acquisition for no benefit.
func (e *Engine) Status(ctx context.Context) (*Status, error) {
	var totalChunks int
	if err := e.db.QueryRowContext(ctx, "SELECT count(*) FROM memory_chunks").Scan(&totalChunks); err != nil {
		return nil, fmt.Errorf("count chunks: %w", err)
	}

	var totalFiles int
	if err := e.db.QueryRowContext(ctx, "SELECT count(DISTINCT file) FROM memory_chunks").Scan(&totalFiles); err != nil {
		return nil, fmt.Errorf("count files: %w", err)
	}

	var indexedAtStr sql.NullString
	if err := e.db.QueryRowContext(ctx, "SELECT value FROM memory_meta WHERE key = 'last_indexed'").Scan(&indexedAtStr); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read last_indexed: %w", err)
	}
	var indexedAt time.Time
	if indexedAtStr.Valid {
		indexedAt, _ = time.Parse(time.RFC3339, indexedAtStr.String)
	}

	totalSize := computeDirSize(e.basePath)

	return &Status{
		TotalFiles:  totalFiles,
		TotalChunks: totalChunks,
		IndexedAt:   indexedAt,
		TotalSizeKB: totalSize / 1024,
		SearchReady: e.config.SearchEnabled,
	}, nil
}

// Close shuts down the engine and releases the SQLite connection.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.db.Close()
}

// computeDirSize walks a directory and returns total size in bytes.
// Note: Direct filesystem access is intentional here — the memory engine runs
// inside the sidecar container process, not on the host. Provider interfaces
// are for host-level abstraction (Docker/K8s/S3), not container-internal ops.
func computeDirSize(dir string) int64 {
	var total int64
	filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}
