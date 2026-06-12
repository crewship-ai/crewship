package memory

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// failEmitter always errors on Emit so the watcher's emit-failure warn
// branches can be exercised without a broken DB.
type failEmitter struct{}

func (failEmitter) Emit(ctx context.Context, e journal.Entry) (string, error) {
	return "", errors.New("emitter down")
}
func (failEmitter) Flush(ctx context.Context) error { return nil }

// syncBuffer is a goroutine-safe bytes.Buffer for capturing slog output
// from watcher goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// ── StartAuditWatcher wiring contracts ───────────────────────────────

func TestStartAuditWatcher_NilDependencies_Panic(t *testing.T) {
	_, db, j, _ := auditTestRig(t)
	cases := []struct {
		name string
		fn   func()
	}{
		{name: "nil logger", fn: func() {
			StartAuditWatcher(context.Background(), db, j, AuditWatcherConfig{BasePath: "/x"}, nil)
		}},
		{name: "nil db", fn: func() {
			StartAuditWatcher(context.Background(), nil, j, AuditWatcherConfig{BasePath: "/x"}, quietLogger())
		}},
		{name: "nil journal", fn: func() {
			StartAuditWatcher(context.Background(), db, nil, AuditWatcherConfig{BasePath: "/x"}, quietLogger())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("expected panic — a nil dependency is a wiring bug that must be loud at boot")
				}
			}()
			tc.fn()
		})
	}
}

func TestWaitForRootThenWatch_CancelledContext_ReturnsPromptly(t *testing.T) {
	_, db, j, _ := auditTestRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		waitForRootThenWatch(ctx, db, j, AuditWatcherConfig{DeferredBootInterval: 5 * time.Millisecond},
			filepath.Join(t.TempDir(), "never"), quietLogger())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("waitForRootThenWatch did not exit on cancelled context")
	}
}

func TestWaitForRootThenWatch_ZeroIntervalDefaults_CancelledExit(t *testing.T) {
	_, db, j, _ := auditTestRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		// Zero interval → 30s default; the pre-cancelled ctx exits the
		// select immediately so the default never actually ticks.
		waitForRootThenWatch(ctx, db, j, AuditWatcherConfig{}, filepath.Join(t.TempDir(), "never"), quietLogger())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("waitForRootThenWatch did not exit with default interval + cancelled ctx")
	}
}

func TestAuditOnePath_LstatFailure_Propagated(t *testing.T) {
	base, db, j, _ := auditTestRig(t)
	full := writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md", []byte("x"))
	memDir := filepath.Dir(full)
	if err := os.Chmod(memDir, 0o000); err != nil { // parent unsearchable → Lstat EACCES
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(memDir, 0o755) })
	err := auditOnePath(context.Background(), db, j, AuditWatcherConfig{BasePath: base}, full, quietLogger())
	if err == nil || !strings.Contains(err.Error(), "lstat") {
		t.Fatalf("err = %v, want lstat error", err)
	}
}

func TestAuditOnePath_HiddenDailyFile_Skipped(t *testing.T) {
	base, db, j, _ := auditTestRig(t)
	// daily/.draft.md parses as a TierAgent daily file but the hidden
	// basename must be skipped as a staging artefact.
	full := writeMemoryFile(t, base, "crew_audit", "martin", "daily/.draft.md", []byte("wip"))
	if err := auditOnePath(context.Background(), db, j, AuditWatcherConfig{BasePath: base}, full, quietLogger()); err != nil {
		t.Fatalf("hidden file must be skipped silently: %v", err)
	}
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&rows)
	if rows != 0 {
		t.Errorf("hidden daily file produced %d version rows", rows)
	}
}

func TestRunAuditWatcher_MissingRoot_DisablesGracefully(t *testing.T) {
	_, db, j, _ := auditTestRig(t)
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// Root does not exist → StartWatcher fails → warn + return (no hang).
	runAuditWatcher(context.Background(), db, j, AuditWatcherConfig{}, filepath.Join(t.TempDir(), "ghost"), logger)
	if !strings.Contains(buf.String(), "audit disabled this boot") {
		t.Errorf("expected disable warn, log = %q", buf.String())
	}
}

// End-to-end: existing root at boot → fsnotify event → version row +
// memory.updated journal entry, all via the real event loop.
func TestStartAuditWatcher_ExistingRoot_AuditsDirectWrite(t *testing.T) {
	base, db, j, scr := auditTestRig(t)
	if err := os.MkdirAll(filepath.Join(base, "crews"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := AuditWatcherConfig{
		BasePath:         base,
		BlobRoot:         filepath.Join(base, "versions"),
		Scrubber:         scr,
		DebounceInterval: 20 * time.Millisecond,
		// The deep dir chain is created AFTER the recursive watch was
		// registered, so fsnotify alone may miss the write — the poll
		// fallback is the deterministic detection path here.
		PollFallbackInterval: 50 * time.Millisecond,
	}
	StartAuditWatcher(ctx, db, j, cfg, quietLogger())

	full := writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md", []byte("direct write body\n"))

	// The write above races the watcher's initial poll snapshot: if the
	// snapshot already contains the file, only a LATER mtime change is
	// reported. Re-touch the file on a slow cadence (well above the
	// debounce window) until the audit lands.
	deadline := time.Now().Add(8 * time.Second)
	lastTouch := time.Now()
	var rows int
	for time.Now().Before(deadline) {
		_ = db.QueryRow(`SELECT COUNT(*) FROM memory_versions WHERE path = 'agent:martin/AGENT.md'`).Scan(&rows)
		if rows > 0 {
			break
		}
		if time.Since(lastTouch) > 300*time.Millisecond {
			now := time.Now()
			_ = os.Chtimes(full, now, now)
			lastTouch = now
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rows == 0 {
		t.Fatalf("no memory_versions row recorded for direct write at %s", full)
	}
	var writtenBy string
	if err := db.QueryRow(`SELECT written_by FROM memory_versions WHERE path = 'agent:martin/AGENT.md'`).Scan(&writtenBy); err != nil {
		t.Fatal(err)
	}
	if writtenBy != "audit-watcher" {
		t.Errorf("written_by = %q, want audit-watcher", writtenBy)
	}
}

// A per-path failure inside the loop is warn-level: the watcher must
// log and stay alive, never crash the loop.
func TestStartAuditWatcher_PathFailureInLoop_WarnsAndContinues(t *testing.T) {
	base, db, j, _ := auditTestRig(t)
	if err := os.MkdirAll(filepath.Join(base, "crews"), 0o755); err != nil {
		t.Fatal(err)
	}
	// BlobRoot routed under a regular file → RecordVersion fails for
	// every audited path.
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := AuditWatcherConfig{
		BasePath:             base,
		BlobRoot:             filepath.Join(blocker, "versions"),
		DebounceInterval:     20 * time.Millisecond,
		PollFallbackInterval: 50 * time.Millisecond,
	}
	StartAuditWatcher(ctx, db, j, cfg, logger)

	full := writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md", []byte("doomed\n"))

	deadline := time.Now().Add(8 * time.Second)
	lastTouch := time.Now()
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "path audit failed") {
			break
		}
		if time.Since(lastTouch) > 300*time.Millisecond {
			now := time.Now()
			_ = os.Chtimes(full, now, now)
			lastTouch = now
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(buf.String(), "path audit failed") {
		t.Fatalf("expected 'path audit failed' warn, log = %q", buf.String())
	}
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&rows)
	if rows != 0 {
		t.Errorf("rows = %d, want 0 when blob store is broken", rows)
	}
}

// ── auditOnePath edge branches ───────────────────────────────────────

func TestAuditOnePath_DeletedFile_StaleEventSkipped(t *testing.T) {
	base, db, j, _ := auditTestRig(t)
	full := writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md", []byte("x"))
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if err := auditOnePath(context.Background(), db, j, AuditWatcherConfig{BasePath: base}, full, quietLogger()); err != nil {
		t.Fatalf("stale event must be a silent skip, got %v", err)
	}
}

func TestAuditOnePath_SymlinkRefused(t *testing.T) {
	base, db, j, _ := auditTestRig(t)
	target := filepath.Join(t.TempDir(), "host-secret")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "crews", "crew_audit", "agents", "martin", ".memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "AGENT.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := auditOnePath(context.Background(), db, j, AuditWatcherConfig{BasePath: base}, link, logger); err != nil {
		t.Fatalf("symlink refusal is a skip, not an error: %v", err)
	}
	if !strings.Contains(buf.String(), "refusing to follow symlink") {
		t.Errorf("expected symlink warn, log = %q", buf.String())
	}
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&rows)
	if rows != 0 {
		t.Errorf("symlinked path must never produce a version row, got %d", rows)
	}
}

func TestAuditOnePath_DirectoryShapedLikeMemoryFile_Skipped(t *testing.T) {
	base, db, j, _ := auditTestRig(t)
	dir := filepath.Join(base, "crews", "crew_audit", "agents", "martin", ".memory", "AGENT.md")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := auditOnePath(context.Background(), db, j, AuditWatcherConfig{BasePath: base}, dir, quietLogger()); err != nil {
		t.Fatalf("directory must be skipped silently: %v", err)
	}
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&rows)
	if rows != 0 {
		t.Errorf("directory produced %d version rows", rows)
	}
}

func TestAuditOnePath_LockFileSkipped(t *testing.T) {
	base, db, j, _ := auditTestRig(t)
	full := writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md.lock", []byte("flock sentinel"))
	if err := auditOnePath(context.Background(), db, j, AuditWatcherConfig{BasePath: base}, full, quietLogger()); err != nil {
		t.Fatalf(".lock staging artefact must be skipped: %v", err)
	}
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&rows)
	if rows != 0 {
		t.Errorf(".lock file produced %d version rows", rows)
	}
}

func TestAuditOnePath_UnreadableFile_SurfacesReadError(t *testing.T) {
	base, db, j, _ := auditTestRig(t)
	full := writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md", []byte("x"))
	if err := os.Chmod(full, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(full, 0o644) })
	err := auditOnePath(context.Background(), db, j, AuditWatcherConfig{BasePath: base}, full, quietLogger())
	if err == nil || !strings.Contains(err.Error(), "read:") {
		t.Fatalf("err = %v, want read error", err)
	}
}

func TestAuditOnePath_WorkspaceLookupFailure_Propagated(t *testing.T) {
	base, db, j, _ := auditTestRig(t)
	full := writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md", []byte("x"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := auditOnePath(ctx, db, j, AuditWatcherConfig{BasePath: base}, full, quietLogger())
	if err == nil || !strings.Contains(err.Error(), "lookup workspace") {
		t.Fatalf("err = %v, want workspace-lookup error", err)
	}
}

func TestAuditOnePath_DedupLookupFailure_Propagated(t *testing.T) {
	base, db, j, _ := auditTestRig(t)
	full := writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md", []byte("x"))
	// Break ONLY the dedup query: the crews lookup still works.
	if _, err := db.Exec(`DROP TABLE memory_versions`); err != nil {
		t.Fatal(err)
	}
	err := auditOnePath(context.Background(), db, j, AuditWatcherConfig{BasePath: base}, full, quietLogger())
	if err == nil || !strings.Contains(err.Error(), "dedup lookup") {
		t.Fatalf("err = %v, want dedup-lookup error (DB hiccups must never pass as 'not deduped')", err)
	}
}

func TestAuditOnePath_RecordVersionFailure_Propagated(t *testing.T) {
	base, db, j, _ := auditTestRig(t)
	full := writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md", []byte("x"))
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := AuditWatcherConfig{BasePath: base, BlobRoot: filepath.Join(blocker, "versions")}
	err := auditOnePath(context.Background(), db, j, cfg, full, quietLogger())
	if err == nil || !strings.Contains(err.Error(), "record version") {
		t.Fatalf("err = %v, want record-version error", err)
	}
}

func TestAuditOnePath_EmitterFailures_WarnButStillRecord(t *testing.T) {
	base, db, _, scr := auditTestRig(t)
	// JWT-shaped bearer token: the one PII pattern the production
	// scrubber flags (same sample the existing PII test uses).
	content := []byte("leak: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4iLCJpYXQiOjE1MTYyMzkwMjJ9.x\n")
	full := writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md", content)

	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := AuditWatcherConfig{BasePath: base, BlobRoot: filepath.Join(base, "versions"), Scrubber: scr}
	if err := auditOnePath(context.Background(), db, failEmitter{}, cfg, full, logger); err != nil {
		t.Fatalf("emit failures are warn-only, audit must succeed: %v", err)
	}
	log := buf.String()
	if !strings.Contains(log, "journal emit (scrubber) failed") {
		t.Errorf("missing scrubber-emit warn, log = %q", log)
	}
	if !strings.Contains(log, "journal emit (updated) failed") {
		t.Errorf("missing updated-emit warn, log = %q", log)
	}
	// The version row still lands — journal is best-effort.
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&rows)
	if rows != 1 {
		t.Errorf("rows = %d, want 1 despite emitter failures", rows)
	}
}

// Compile-time guard: failEmitter implements journal.Emitter.
var _ journal.Emitter = failEmitter{}
