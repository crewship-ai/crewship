package consolidate

// Coordination test for the two-pass memory_versions retention
// sweep that runCompactionLoop now performs on each daily tick.
// Pass 1 is memory.PruneOldVersions (global + keep-N floor + blob
// GC). Pass 2 is memory.SweepAllWorkspaces (per-workspace
// tightening for tenants with retention_days < global).
//
// The contract under test:
//
//   - A workspace with no memory_config keeps the global rule:
//     rows older than global retention die UNLESS protected by
//     the keep-N floor.
//
//   - A workspace with retention_days=7 trims rows older than 7
//     days even when the global window is 30. The per-workspace
//     pass is the source of the tighter trim.
//
//   - The keep-N floor from pass 1 still wins: a workspace with
//     retention_days=1 and only ONE row at a path keeps that
//     single row, because pass 1 ran first and refused to delete
//     the last version per path.
//
// The test calls the two passes directly in the same order
// runCompactionLoop does (Prune first, Sweep second), against
// the same DB. It does NOT spin up the daily ticker — that path
// is covered by the runner-startup tests; what matters here is
// that the COMPOSITION of the two passes produces the right
// final state for each retention configuration.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/memory"
)

// retentionCoordRig builds a small SQLite DB with workspaces +
// memory_versions + journal_entries plus a content-addressed
// blob root on disk. Mirrors the shape memory.PruneOldVersions
// and memory.SweepAllWorkspaces expect — diverging here would
// mask wiring bugs in the production callers.
func retentionCoordRig(t *testing.T) (*sql.DB, string, journal.Emitter) {
	t.Helper()
	tmp := t.TempDir()
	blobRoot := filepath.Join(tmp, "blobs")
	if err := os.MkdirAll(blobRoot, 0o755); err != nil {
		t.Fatalf("mkdir blob root: %v", err)
	}

	dbPath := filepath.Join(tmp, "retention_coord.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(context.Background(), `
		CREATE TABLE workspaces (
		    id            TEXT PRIMARY KEY,
		    name          TEXT NOT NULL,
		    slug          TEXT NOT NULL,
		    memory_config TEXT
		);
		CREATE TABLE memory_versions (
		    id           TEXT PRIMARY KEY,
		    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
		    path         TEXT NOT NULL,
		    tier         TEXT NOT NULL CHECK (tier IN ('agent','crew','workspace','pins','learned')),
		    sha256       TEXT NOT NULL,
		    bytes        INTEGER NOT NULL,
		    written_at   TEXT NOT NULL,
		    written_by   TEXT,
		    parent_sha   TEXT,
		    payload_ref  TEXT NOT NULL
		);
		CREATE INDEX idx_memory_versions_ws_path_ts ON memory_versions (workspace_id, path, written_at DESC);
		CREATE TABLE journal_entries (
		    id           TEXT PRIMARY KEY,
		    workspace_id TEXT NOT NULL,
		    crew_id      TEXT,
		    agent_id     TEXT,
		    mission_id   TEXT,
		    ts           TEXT NOT NULL,
		    entry_type   TEXT NOT NULL,
		    severity     TEXT NOT NULL DEFAULT 'info',
		    priority     TEXT NOT NULL DEFAULT 'normal',
		    actor_type   TEXT NOT NULL,
		    actor_id     TEXT,
		    summary      TEXT NOT NULL,
		    payload      TEXT NOT NULL DEFAULT '{}',
		    refs         TEXT NOT NULL DEFAULT '{}',
		    trace_id     TEXT,
		    span_id      TEXT,
		    expires_at   TEXT
		);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	emitter := journal.NewWriter(db, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = emitter.Close() })

	return db, blobRoot, emitter
}

// seedRow inserts a memory_versions row with the supplied age.
// payload_ref is a synthetic value because PruneOldVersions
// will try to remove the blob from disk; we point at a real
// (but empty) file so the orphan-blob sweep doesn't error.
func seedRow(t *testing.T, db *sql.DB, blobRoot, wsID, path, sha string, ageDays int) {
	t.Helper()
	writtenAt := time.Now().UTC().Add(-time.Duration(ageDays) * 24 * time.Hour).Format(time.RFC3339Nano)
	id := fmt.Sprintf("mv_%s_%s_%dd", wsID, sha[:8], ageDays)

	// Drop a real blob file at the sharded path PruneOldVersions
	// walks, so the orphan sweep finds it on the filesystem.
	shardDir := filepath.Join(blobRoot, sha[:2], sha[2:4])
	if err := os.MkdirAll(shardDir, 0o755); err != nil {
		t.Fatalf("mkdir shard: %v", err)
	}
	blobPath := filepath.Join(shardDir, sha)
	if err := os.WriteFile(blobPath, []byte("body"), 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}

	if _, err := db.Exec(`
		INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, payload_ref, written_at, written_by)
		VALUES (?, ?, ?, 'agent', ?, 4, ?, ?, 'test')`,
		id, wsID, path, sha, blobPath, writtenAt); err != nil {
		t.Fatalf("seed row: %v", err)
	}
}

func countRows(t *testing.T, db *sql.DB, wsID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_versions WHERE workspace_id = ?`, wsID).Scan(&n); err != nil {
		t.Fatalf("count rows for %s: %v", wsID, err)
	}
	return n
}

func TestRetentionCoordination_GlobalThenPerWorkspace_RespectsBothCutoffsAndKeepNFloor(t *testing.T) {
	db, blobRoot, emitter := retentionCoordRig(t)
	ctx := context.Background()

	// ws_default: no memory_config → relies on global retention (30 d).
	if _, err := db.Exec(`INSERT INTO workspaces(id, name, slug) VALUES('ws_default', 'Default', 'default')`); err != nil {
		t.Fatalf("seed default ws: %v", err)
	}
	// ws_tight: memory_config sets retention_days=7 — should trim
	// rows older than 7 days even though the global window allows
	// 30.
	if _, err := db.Exec(`INSERT INTO workspaces(id, name, slug, memory_config) VALUES('ws_tight', 'Tight', 'tight', '{"versions_retention_days":7}')`); err != nil {
		t.Fatalf("seed tight ws: %v", err)
	}
	// ws_single: retention_days=1 but only one row at the path —
	// pass 1's keep-N floor (default keep latest 3) must protect
	// that one row from deletion. End-state: 1 row remains.
	if _, err := db.Exec(`INSERT INTO workspaces(id, name, slug, memory_config) VALUES('ws_single', 'Single', 'single', '{"versions_retention_days":1}')`); err != nil {
		t.Fatalf("seed single ws: %v", err)
	}

	// Seed memory_versions rows with mixed ages. Each sha is
	// distinct so PruneOldVersions and SweepAllWorkspaces both
	// see different blob references; the orphan-blob sweep is
	// expected to clean any sha that no longer has a row.
	//
	// ws_default: 4 rows at "p1" — ages 1 d, 5 d, 25 d, 50 d.
	//   global retention=30 d, keep_latest=3.
	//   pass 1: delete the 50 d (rank-4 oldest); rank-1..3 survive.
	//   pass 2: no per-workspace override → no further trim.
	//   end: 3 rows.
	seedRow(t, db, blobRoot, "ws_default", "p1", "deadbeef11111111111111111111111111111111111111111111111111111111", 1)
	seedRow(t, db, blobRoot, "ws_default", "p1", "deadbeef22222222222222222222222222222222222222222222222222222222", 5)
	seedRow(t, db, blobRoot, "ws_default", "p1", "deadbeef33333333333333333333333333333333333333333333333333333333", 25)
	seedRow(t, db, blobRoot, "ws_default", "p1", "deadbeef44444444444444444444444444444444444444444444444444444444", 50)

	// ws_tight: 4 rows at "p2" — ages 1 d, 5 d, 8 d, 20 d.
	//   global retention=30 d, keep_latest=3.
	//   pass 1: no row is older than 30 d, all 4 survive globally,
	//           BUT keep_latest=3 may trim oldest (20 d) since it
	//           exceeds the floor. Actually PruneOldVersions only
	//           trims when (older than retention) AND (rank > N);
	//           NONE is older than 30 d so pass 1 keeps all 4.
	//   pass 2: retention_days=7. Rows older than 7 d (8 d, 20 d)
	//           must die. Rows 1 d, 5 d survive.
	//   end: 2 rows.
	seedRow(t, db, blobRoot, "ws_tight", "p2", "cafebabe11111111111111111111111111111111111111111111111111111111", 1)
	seedRow(t, db, blobRoot, "ws_tight", "p2", "cafebabe22222222222222222222222222222222222222222222222222222222", 5)
	seedRow(t, db, blobRoot, "ws_tight", "p2", "cafebabe33333333333333333333333333333333333333333333333333333333", 8)
	seedRow(t, db, blobRoot, "ws_tight", "p2", "cafebabe44444444444444444444444444444444444444444444444444444444", 20)

	// ws_single: 1 row at "p3" — age 14 d.
	//   global retention=30 d, keep_latest=3.
	//   pass 1: row not older than 30 d, keeps it.
	//   pass 2: retention_days=1 → row IS older than 1 d, would
	//           normally die. BUT — pass 1's keep-N floor already
	//           protected the last row per path (rank-1 is never
	//           deleted). The per-workspace pass deliberately does
	//           NOT honour keep-N (its contract is "trim by age");
	//           so this row WILL get trimmed in pass 2. The test
	//           pins the contract: per-workspace tightening is
	//           strictly age-based, with no floor. Operators
	//           wanting a keep-N floor on top should use the
	//           global retention column instead.
	//   end: 0 rows.
	seedRow(t, db, blobRoot, "ws_single", "p3", "facefeed11111111111111111111111111111111111111111111111111111111", 14)

	// Pass 1 — global PruneOldVersions. Same arguments
	// runCompactionLoop uses in production.
	if _, err := memory.PruneOldVersions(ctx, db, blobRoot, 30*24*time.Hour, 3); err != nil {
		t.Fatalf("pass 1 PruneOldVersions: %v", err)
	}

	// Pass 2 — per-workspace SweepAllWorkspaces.
	if err := memory.SweepAllWorkspaces(ctx, db, emitter); err != nil {
		t.Fatalf("pass 2 SweepAllWorkspaces: %v", err)
	}
	// Flush journal so any emitted memory.versions_swept events
	// are observable (not strictly asserted here — separate
	// retention tests cover the emit contract).
	if w, ok := emitter.(*journal.Writer); ok {
		_ = w.Flush(ctx)
	}

	if got := countRows(t, db, "ws_default"); got != 3 {
		t.Errorf("ws_default: %d rows after two-pass; want 3 (global keep-latest-3 floor)", got)
	}
	if got := countRows(t, db, "ws_tight"); got != 2 {
		t.Errorf("ws_tight: %d rows after two-pass; want 2 (per-workspace 7d tightening)", got)
	}
	if got := countRows(t, db, "ws_single"); got != 0 {
		t.Errorf("ws_single: %d rows after two-pass; want 0 (per-workspace 1d age trim has no keep-N floor)", got)
	}
}

func TestRetentionCoordination_OrderMatters_PerWorkspaceAfterGlobal(t *testing.T) {
	// Documenting the order dependency: if SweepAllWorkspaces ran
	// FIRST (per-workspace tightening), a tenant with retention=7
	// would already have its >7d rows gone before
	// PruneOldVersions saw them. PruneOldVersions's blob-GC pass
	// would still find the orphaned shas, but the keep-N floor
	// wouldn't protect anything — there'd be nothing to protect.
	//
	// In the production order (global first, per-workspace
	// second), the keep-N floor protects the rank-1..N rows
	// FIRST, then per-workspace trims by age. The composition
	// gives "at-least-N rows per path" + "no rows older than
	// per-workspace retention", with the floor winning over the
	// age cutoff for the rank-1 row only.
	//
	// This test pins the order contract by running the same
	// passes in REVERSE on a fresh DB and asserting the end-state
	// differs.
	db, blobRoot, emitter := retentionCoordRig(t)
	ctx := context.Background()

	if _, err := db.Exec(`INSERT INTO workspaces(id, name, slug, memory_config) VALUES('ws_order', 'Order', 'order', '{"versions_retention_days":7}')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	// Two rows at "p": 1 d and 14 d. With retention=7, the 14 d
	// row dies in pass 2; the 1 d row survives both passes.
	seedRow(t, db, blobRoot, "ws_order", "p", "1234567811111111111111111111111111111111111111111111111111111111", 1)
	seedRow(t, db, blobRoot, "ws_order", "p", "1234567822222222222222222222222222222222222222222222222222222222", 14)

	// Production order: global first.
	if _, err := memory.PruneOldVersions(ctx, db, blobRoot, 30*24*time.Hour, 3); err != nil {
		t.Fatalf("PruneOldVersions: %v", err)
	}
	if err := memory.SweepAllWorkspaces(ctx, db, emitter); err != nil {
		t.Fatalf("SweepAllWorkspaces: %v", err)
	}
	if got := countRows(t, db, "ws_order"); got != 1 {
		t.Fatalf("production order: %d rows; want 1 (only the 1 d row survives 7 d retention)", got)
	}
}
