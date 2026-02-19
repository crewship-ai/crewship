package memory

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Config controls memory engine behavior.
type Config struct {
	MaxSizeMB     int  // max total memory size in MB (default: 10)
	DailyMaxKB    int  // max daily log size in KB (default: 100)
	SearchEnabled bool // enable FTS5 search (default: true)
}

// DefaultConfig returns sensible defaults for agent memory.
func DefaultConfig() Config {
	return Config{
		MaxSizeMB:     10,
		DailyMaxKB:    100,
		SearchEnabled: true,
	}
}

// Status reports the current state of the memory engine.
type Status struct {
	TotalFiles   int       `json:"total_files"`
	TotalChunks  int       `json:"total_chunks"`
	IndexedAt    time.Time `json:"indexed_at"`
	TotalSizeKB  int64     `json:"total_size_kb"`
	SearchReady  bool      `json:"search_ready"`
}

// Engine provides FTS5-backed search over agent memory files.
type Engine struct {
	basePath string
	db       *sql.DB
	mu       sync.RWMutex
	config   Config
}

// New creates a memory engine for the given base path (e.g. /output/{agent}/.memory/).
// The FTS5 index is stored as index.sqlite inside the base path.
func New(basePath string, cfg Config) (*Engine, error) {
	if cfg.MaxSizeMB == 0 {
		cfg.MaxSizeMB = 10
	}
	if cfg.DailyMaxKB == 0 {
		cfg.DailyMaxKB = 100
	}

	dbPath := filepath.Join(basePath, "index.sqlite")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open memory index: %w", err)
	}

	if err := initSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("init memory schema: %w", err)
	}

	return &Engine{
		basePath: basePath,
		db:       db,
		config:   cfg,
	}, nil
}

func initSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS memory_chunks USING fts5(
			file,
			content,
			tokenize='unicode61'
		);

		CREATE TABLE IF NOT EXISTS memory_meta (
			key TEXT PRIMARY KEY,
			value TEXT
		);
	`)
	return err
}

// Status returns information about the memory index state.
func (e *Engine) Status() (*Status, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var totalChunks int
	if err := e.db.QueryRow("SELECT count(*) FROM memory_chunks").Scan(&totalChunks); err != nil {
		return nil, fmt.Errorf("count chunks: %w", err)
	}

	var totalFiles int
	if err := e.db.QueryRow("SELECT count(DISTINCT file) FROM memory_chunks").Scan(&totalFiles); err != nil {
		return nil, fmt.Errorf("count files: %w", err)
	}

	var indexedAtStr sql.NullString
	_ = e.db.QueryRow("SELECT value FROM memory_meta WHERE key = 'last_indexed'").Scan(&indexedAtStr)
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
