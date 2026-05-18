package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// auditTestRig stands up a temp dir with the {basePath}/crews/...
// shape the watcher expects, plus a minimal SQLite schema for
// memory_versions + workspaces + crews + journal_entries. Mirrors the
// pattern in retention_test.go so the two suites stay readable
// alongside each other.
func auditTestRig(t *testing.T) (string, *sql.DB, journal.Emitter, *scrubber.Scrubber) {
	t.Helper()
	base := t.TempDir()

	// WAL + a generous busy timeout: the audit_watcher path can
	// have two writers contending on the same DB at the same
	// instant — the journal.Writer goroutine flushing a queued
	// entry (FlushSize=1 below for test determinism) and the main
	// goroutine's RecordVersion INSERT. SQLite's default rollback
	// journal mode fails the second writer with SQLITE_BUSY; WAL
	// lets the second writer block briefly. busy_timeout=5000
	// gives the loser up to 5 s to retry before the test fails.
	// modernc.org/sqlite (our driver) reads pragmas via the
	// _pragma= URL parameter.
	dbPath := filepath.Join(t.TempDir(), "audit.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// Single connection avoids the second-connection-pool race that
	// would re-introduce SQLITE_BUSY under WAL when the pool grows.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	// Inline schema mirrors the production migrations' columns for
	// memory_versions exactly (column order + null/default
	// semantics, see versions_test.go's openVersionsDB) so a
	// divergence here would mask shape bugs. journal_entries
	// includes the columns the journal.Writer references; missing
	// columns produce silent "table has no column named X" errors
	// in the writer's batch path.
	if _, err := db.ExecContext(context.Background(), `
		CREATE TABLE workspaces (id TEXT PRIMARY KEY);
		CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL);
		CREATE TABLE memory_versions (
		    id           TEXT PRIMARY KEY,
		    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
		    path         TEXT NOT NULL,
		    tier         TEXT NOT NULL CHECK (tier IN ('agent','crew','workspace','pins','learned')),
		    sha256       TEXT NOT NULL,
		    bytes        INTEGER NOT NULL,
		    written_at   TEXT NOT NULL DEFAULT (datetime('now','subsec')),
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
		INSERT INTO workspaces (id) VALUES ('ws_audit');
		INSERT INTO crews (id, workspace_id) VALUES ('crew_audit', 'ws_audit');
	`); err != nil {
		t.Fatalf("seed schema: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	j := journal.NewWriter(db, logger, journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = j.Close() })

	return base, db, j, scrubber.New()
}

// writeMemoryFile creates a memory file at the audit-watcher's
// expected layout: {base}/crews/{crewID}/agents/{slug}/.memory/{rel}
func writeMemoryFile(t *testing.T, base, crewID, slug, rel string, content []byte) string {
	t.Helper()
	full := filepath.Join(base, "crews", crewID, "agents", slug, ".memory", rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	return full
}

// ── parseMemoryPath ───────────────────────────────────────────────────

func TestAuditWatcher_ParseMemoryPath_TablesAllShapes(t *testing.T) {
	// Locks the exact shape contract for every documented memory
	// filename. The matcher is the gate between "we audit this"
	// and "we ignore it"; a regression that silently skips e.g.
	// learned-*.md is a memory-audit hole nobody would notice
	// from a green test suite.
	base := "/tmp/cs-data"
	cases := []struct {
		name     string
		path     string
		wantOK   bool
		wantTier Tier
		wantRel  string // empty when wantOK is false
	}{
		{
			name:     "AGENT.md → TierAgent",
			path:     base + "/crews/crew_a/agents/martin/.memory/AGENT.md",
			wantOK:   true,
			wantTier: TierAgent,
			wantRel:  "agent:martin/AGENT.md",
		},
		{
			name:     "CREW.md → TierCrew",
			path:     base + "/crews/crew_a/agents/martin/.memory/CREW.md",
			wantOK:   true,
			wantTier: TierCrew,
			wantRel:  "agent:martin/CREW.md",
		},
		{
			name:     "pins.md → TierPins",
			path:     base + "/crews/crew_a/agents/martin/.memory/pins.md",
			wantOK:   true,
			wantTier: TierPins,
			wantRel:  "agent:martin/pins.md",
		},
		{
			name:     "learned-2026-05-17.md → TierLearned",
			path:     base + "/crews/crew_a/agents/martin/.memory/learned-2026-05-17.md",
			wantOK:   true,
			wantTier: TierLearned,
			wantRel:  "agent:martin/learned-2026-05-17.md",
		},
		{
			name:     "daily/2026-05-17.md → TierAgent",
			path:     base + "/crews/crew_a/agents/martin/.memory/daily/2026-05-17.md",
			wantOK:   true,
			wantTier: TierAgent,
			wantRel:  "agent:martin/daily/2026-05-17.md",
		},
		{
			name:   "non-memory file in agent dir is skipped",
			path:   base + "/crews/crew_a/agents/martin/scratch/notes.txt",
			wantOK: false,
		},
		{
			name:   "non-.md file in memory dir is skipped",
			path:   base + "/crews/crew_a/agents/martin/.memory/temp.log",
			wantOK: false,
		},
		{
			name:   "outside basePath returns false",
			path:   "/etc/passwd",
			wantOK: false,
		},
		{
			name:   "too-short crews subtree",
			path:   base + "/crews/crew_a/agents/martin/.memory/",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseMemoryPath(base, tc.path)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got.Tier != tc.wantTier {
				t.Errorf("tier = %q, want %q", got.Tier, tc.wantTier)
			}
			if got.RelPath != tc.wantRel {
				t.Errorf("rel = %q, want %q", got.RelPath, tc.wantRel)
			}
		})
	}
}

// ── auditOnePath ──────────────────────────────────────────────────────

func TestAuditWatcher_HappyPath_RecordsVersionAndEmitsUpdated(t *testing.T) {
	base, db, j, scr := auditTestRig(t)
	content := []byte("# AGENT memory\nfact: Crewship runs Linux containers per crew\n")
	full := writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md", content)

	cfg := AuditWatcherConfig{
		BasePath: base,
		BlobRoot: filepath.Join(base, "versions"),
		Scrubber: scr,
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := auditOnePath(context.Background(), db, j, cfg, full, logger); err != nil {
		t.Fatalf("auditOnePath: %v", err)
	}
	_ = j.(*journal.Writer).Flush(context.Background())

	// One memory_versions row with the right shape.
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&rows); err != nil {
		t.Fatalf("count memory_versions: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want 1", rows)
	}
	var path, tier, writtenBy, hash string
	if err := db.QueryRow(`SELECT path, tier, written_by, sha256 FROM memory_versions`).Scan(&path, &tier, &writtenBy, &hash); err != nil {
		t.Fatalf("scan memory_versions row: %v", err)
	}
	if path != "agent:martin/AGENT.md" {
		t.Errorf("path = %q", path)
	}
	if tier != "agent" {
		t.Errorf("tier = %q", tier)
	}
	if writtenBy != "audit-watcher" {
		t.Errorf("written_by = %q, want audit-watcher (audit trail honesty)", writtenBy)
	}
	sum := sha256.Sum256(content)
	if hash != hex.EncodeToString(sum[:]) {
		t.Errorf("sha mismatch")
	}

	// One memory.updated journal entry.
	var jRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type = 'memory.updated'`).Scan(&jRows); err != nil {
		t.Fatalf("count journal_entries: %v", err)
	}
	if jRows != 1 {
		t.Errorf("memory.updated entries = %d, want 1", jRows)
	}
}

func TestAuditWatcher_PathOutsideMemoryDir_Skipped(t *testing.T) {
	base, db, j, scr := auditTestRig(t)
	// Write a file outside the .memory/ shape.
	scratch := filepath.Join(base, "crews", "crew_audit", "agents", "martin", "scratch", "notes.txt")
	if err := os.MkdirAll(filepath.Dir(scratch), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(scratch, []byte("not memory"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := AuditWatcherConfig{BasePath: base, BlobRoot: filepath.Join(base, "versions"), Scrubber: scr}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := auditOnePath(context.Background(), db, j, cfg, scratch, logger); err != nil {
		t.Fatalf("auditOnePath: %v", err)
	}
	_ = j.(*journal.Writer).Flush(context.Background())
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&rows)
	if rows != 0 {
		t.Errorf("rows = %d, want 0 (path not a memory file)", rows)
	}
}

func TestAuditWatcher_RecentSidecarRecord_DedupedNoSecondRow(t *testing.T) {
	base, db, j, scr := auditTestRig(t)
	content := []byte("dedup test content")
	full := writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md", content)
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])

	// Pre-insert a row mimicking what the sidecar would have
	// recorded moments ago — same workspace, same path, same sha.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, payload_ref, written_at, written_by)
		VALUES ('mv_pre', 'ws_audit', 'agent:martin/AGENT.md', 'agent', ?, ?, '/dev/null', ?, 'sidecar')`,
		hash, len(content), now,
	); err != nil {
		t.Fatalf("seed sidecar row: %v", err)
	}

	cfg := AuditWatcherConfig{BasePath: base, BlobRoot: filepath.Join(base, "versions"), Scrubber: scr}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := auditOnePath(context.Background(), db, j, cfg, full, logger); err != nil {
		t.Fatalf("auditOnePath: %v", err)
	}
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&rows)
	if rows != 1 {
		t.Errorf("rows = %d, want 1 (audit dedup'd against recent sidecar row)", rows)
	}
}

func TestAuditWatcher_OldSidecarRecord_NotDeduped(t *testing.T) {
	// Past the dedup window — the watcher correctly records as a
	// new event. Prevents the dedup from being a perpetual mute
	// after a stale row sits at the same sha.
	base, db, j, scr := auditTestRig(t)
	content := []byte("rewrite after dedup window")
	full := writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md", content)
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])

	// Insert a row dated 10 minutes ago (well outside the 60s
	// dedup window).
	old := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, payload_ref, written_at, written_by)
		VALUES ('mv_old', 'ws_audit', 'agent:martin/AGENT.md', 'agent', ?, ?, '/dev/null', ?, 'sidecar')`,
		hash, len(content), old,
	); err != nil {
		t.Fatalf("seed stale row: %v", err)
	}

	cfg := AuditWatcherConfig{BasePath: base, BlobRoot: filepath.Join(base, "versions"), Scrubber: scr}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := auditOnePath(context.Background(), db, j, cfg, full, logger); err != nil {
		t.Fatalf("auditOnePath: %v", err)
	}
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&rows)
	if rows != 2 {
		t.Errorf("rows = %d, want 2 (stale row + new audit row)", rows)
	}
}

func TestAuditWatcher_PIIInContent_EmitsWriteRejectedWarn(t *testing.T) {
	// Direct write contained PII (a JWT-shaped bearer token in
	// this case — the only bearer pattern the production scrubber
	// catches per scrubber package). The audit watcher cannot
	// un-write the file from disk but MUST surface a journal
	// entry so the operator notices.
	base, db, j, scr := auditTestRig(t)
	content := []byte("note: leaked Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4iLCJpYXQiOjE1MTYyMzkwMjJ9.x in chat\n")
	full := writeMemoryFile(t, base, "crew_audit", "martin", "daily/2026-05-17.md", content)

	cfg := AuditWatcherConfig{BasePath: base, BlobRoot: filepath.Join(base, "versions"), Scrubber: scr}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := auditOnePath(context.Background(), db, j, cfg, full, logger); err != nil {
		t.Fatalf("auditOnePath: %v", err)
	}
	_ = j.(*journal.Writer).Flush(context.Background())

	var rejects int
	_ = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type = 'memory.write_rejected'`).Scan(&rejects)
	if rejects < 1 {
		t.Errorf("memory.write_rejected entries = %d, want >=1 (PII detected, must surface)", rejects)
	}
	// memory.updated still fires — we don't block, we annotate.
	var updates int
	_ = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type = 'memory.updated'`).Scan(&updates)
	if updates != 1 {
		t.Errorf("memory.updated entries = %d, want 1 (audit row still records)", updates)
	}
}

func TestAuditWatcher_UnknownCrew_SilentlySkipped(t *testing.T) {
	// fsnotify can fire after a crew was deleted — the .memory
	// files survive briefly on the bind-mount. Watcher must not
	// error on the unknown crew; the audit just has nowhere to
	// attribute the row.
	base, db, j, scr := auditTestRig(t)
	full := writeMemoryFile(t, base, "crew_ghost", "martin", "AGENT.md", []byte("ghost"))

	cfg := AuditWatcherConfig{BasePath: base, BlobRoot: filepath.Join(base, "versions"), Scrubber: scr}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := auditOnePath(context.Background(), db, j, cfg, full, logger); err != nil {
		t.Fatalf("unknown crew should not error: %v", err)
	}
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&rows)
	if rows != 0 {
		t.Errorf("rows = %d, want 0 (no crew → no attribution → no row)", rows)
	}
}

func TestAuditWatcher_HiddenAndTmpFiles_Skipped(t *testing.T) {
	// Atomic-rename writers (the sidecar itself uses this) drop a
	// foo.tmp + foo.lock + foo for one logical write. The watcher
	// must process only foo; auditing the .tmp would race the
	// rename and emit a phantom row whose content the operator
	// never sees on disk.
	base, db, j, scr := auditTestRig(t)
	// Set up a base layout so parseMemoryPath returns OK for the
	// .tmp + .lock + .hidden variants; they then fail the
	// hidden/tmp/lock prefix-suffix guard inside auditOnePath.
	tmp := writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md.tmp", []byte("staging"))
	lock := writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md.lock", []byte(""))
	hidden := writeMemoryFile(t, base, "crew_audit", "martin", ".secret", []byte("nope"))

	cfg := AuditWatcherConfig{BasePath: base, BlobRoot: filepath.Join(base, "versions"), Scrubber: scr}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	for _, p := range []string{tmp, lock, hidden} {
		if err := auditOnePath(context.Background(), db, j, cfg, p, logger); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&rows)
	if rows != 0 {
		t.Errorf("rows = %d, want 0 (staging files must not produce audit rows)", rows)
	}
}

// ── StartAuditWatcher boot path ───────────────────────────────────────

func TestStartAuditWatcher_EmptyBasePath_NoOp(t *testing.T) {
	// Tests we don't crash on the "no MemoryRoot configured" path —
	// the boot helper should log and return without starting a
	// goroutine.
	_, db, j, _ := auditTestRig(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	StartAuditWatcher(context.Background(), db, j, AuditWatcherConfig{}, logger)
	// Nothing to assert beyond "no panic" — Go zero-time wait is
	// fine for verifying the synchronous early-return.
}

func TestStartAuditWatcher_MissingRoot_DefersAndStartsOnCreate(t *testing.T) {
	// On a fresh install the crews dir doesn't exist yet. The
	// watcher must wait for it (deferred boot) rather than failing
	// outright. Verify by booting against an empty base, creating
	// the crews dir + a memory file, and observing that the audit
	// row + journal entry land within the deferred-boot window.
	//
	// We drive the test fast by overriding DeferredBootInterval to
	// 50 ms; production stays on the 30 s default. Skip in -short
	// to keep `go test -short` snappy.
	if testing.Short() {
		t.Skip("touches fsnotify + ticker; skip in -short")
	}

	base, db, j, scr := auditTestRig(t)
	// The rig creates an empty TempDir as base and seeds the crews
	// DB row but NOT the filesystem crews/ subtree (that's deferred
	// until writeMemoryFile fires). So we already meet the
	// precondition: {base}/crews/ does not exist when
	// StartAuditWatcher is called below.
	crewsRoot := filepath.Join(base, "crews")
	if _, err := os.Stat(crewsRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("precondition: crews root must not exist before StartAuditWatcher; stat err=%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	StartAuditWatcher(ctx, db, j, AuditWatcherConfig{
		BasePath:             base,
		BlobRoot:             filepath.Join(base, "versions"),
		Scrubber:             scr,
		DeferredBootInterval: 50 * time.Millisecond,
		// Fast poll fallback so the test doesn't depend on
		// fsnotify's recursive auto-add catching up to a deeply
		// nested mkdir+write that happens in the same instant.
		// Production uses the 30 s default; the test compresses
		// the loop so the polling sweep certainly fires within
		// the 3 s deadline.
		PollFallbackInterval: 100 * time.Millisecond,
		DebounceInterval:     20 * time.Millisecond,
	}, logger)

	// Create the crews dir first so the deferred poller can
	// detect it and boot StartWatcher. Then write the memory file
	// — the poll fallback in memory.Watcher scans the tree every
	// 100 ms, so the AGENT.md write will be picked up regardless
	// of fsnotify recursive-add timing.
	if err := os.MkdirAll(crewsRoot, 0o755); err != nil {
		t.Fatalf("step 1 mkdir crews root: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // let the 50 ms poller detect + boot

	_ = writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md",
		[]byte("# Martin\n\nNotes after deferred boot.\n"))

	// Poll up to 3 s for the audit row to land. The deferred ticker
	// fires every 50 ms; fsnotify + scrubber + record should be
	// sub-second on a quiet CI box.
	deadline := time.Now().Add(3 * time.Second)
	for {
		var rows int
		if err := db.QueryRow(`SELECT COUNT(*) FROM memory_versions WHERE workspace_id = ?`, "ws_audit").Scan(&rows); err != nil {
			t.Fatalf("count after deferred boot: %v", err)
		}
		if rows == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("deferred-boot watcher did not record the write within 3s (rows=%d)", rows)
		}
		time.Sleep(40 * time.Millisecond)
	}
}

// stringContains is a small helper so log-assertions stay readable.
// Not a critical helper — kept inline so changes don't ripple.
func stringContains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// Compile-time sanity: AuditWatcherConfig has the exported fields the
// server lifecycle wires (BasePath, BlobRoot, Scrubber). A future
// rename would break the server build, but this file would catch the
// regression faster.
var _ = AuditWatcherConfig{BasePath: "", BlobRoot: "", Scrubber: nil}
var _ = stringContains
