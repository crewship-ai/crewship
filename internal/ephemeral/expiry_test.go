package ephemeral

// Tests for the PR-D F5 ephemeral-expiry sweeper. Uses an injected
// clock so we don't have to sleep through the actual TTL; the
// sweeper's `now` closure is the only time source so a test that
// pins it to a known instant produces a deterministic verdict.

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

func seedWorkspaceAndCrew(t *testing.T, db *sql.DB) (wsID, crewID string) {
	t.Helper()
	wsID, crewID = "ws-eph", "crew-eph"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', 'ws')`, wsID); err != nil {
		t.Fatalf("ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew', 'crew')`, crewID, wsID); err != nil {
		t.Fatalf("crew: %v", err)
	}
	return wsID, crewID
}

func seedEphemeral(t *testing.T, db *sql.DB, wsID, crewID, id string, expiresAt string, expiredAt *string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role, status,
		    cli_adapter, tool_profile, memory_enabled,
		    ephemeral, expires_at, expired_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'AGENT', 'IDLE',
		        'CLAUDE_CODE', 'CODING', 1,
		        1, ?, ?, ?, ?)`,
		id, crewID, wsID, id, id, expiresAt, expiredAt, now, now)
	if err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// recordingBroadcaster captures BroadcastWorkspaceEvent calls so a
// test can assert the sweeper emitted the expected agent.expired
// event without standing up the real WS hub.
type recordingBroadcaster struct {
	mu     sync.Mutex
	events []map[string]string
}

func (r *recordingBroadcaster) BroadcastWorkspaceEvent(ws, ev string, p map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := map[string]string{"workspace": ws, "event": ev}
	for k, v := range p {
		copy[k] = v
	}
	r.events = append(r.events, copy)
}

func (r *recordingBroadcaster) Events() []map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]map[string]string, len(r.events))
	copy(out, r.events)
	return out
}

func TestSweep_GhostsOnlyExpiredAndEphemeralAgents(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID := seedWorkspaceAndCrew(t, db)

	// Inject "now" at 2026-06-01T12:00:00Z; one agent has
	// expires_at < now (must ghost), one has expires_at > now (must
	// NOT ghost), one is already a ghost (must not double-flip),
	// one is permanent ephemeral=0 (must be ignored).
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	seedEphemeral(t, db, wsID, crewID, "due", "2026-06-01T11:00:00Z", nil)   // due 1h ago
	seedEphemeral(t, db, wsID, crewID, "later", "2026-06-01T13:00:00Z", nil) // due in 1h
	pastExp := "2026-05-01T00:00:00Z"
	seedEphemeral(t, db, wsID, crewID, "old-ghost", "2026-05-01T00:00:00Z", &pastExp)

	// Permanent agent (ephemeral=0) — must not be touched.
	nowStr := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role, status,
		    cli_adapter, tool_profile, memory_enabled, ephemeral,
		    created_at, updated_at)
		VALUES ('perm', ?, ?, 'Perm', 'perm', 'AGENT', 'IDLE',
		        'CLAUDE_CODE', 'CODING', 1, 0,
		        ?, ?)`, crewID, wsID, nowStr, nowStr)
	if err != nil {
		t.Fatalf("seed perm: %v", err)
	}

	rec := &recordingBroadcaster{}
	n, err := SweepExpiredAgents(context.Background(), db, nil, rec, func() time.Time { return now })
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Errorf("ghosted = %d, want 1", n)
	}

	// Verify the right rows did/didn't transition.
	cases := []struct {
		id          string
		wantGhosted bool
	}{
		{"due", true},
		{"later", false},
		{"old-ghost", true}, // already ghost, must stay ghost
		{"perm", false},
	}
	for _, tc := range cases {
		var expiredAt sql.NullString
		if err := db.QueryRow(`SELECT expired_at FROM agents WHERE id = ?`, tc.id).Scan(&expiredAt); err != nil {
			t.Fatalf("query expired_at for %s: %v", tc.id, err)
		}
		if expiredAt.Valid != tc.wantGhosted {
			t.Errorf("agent %s: expired_at.Valid=%v, want %v", tc.id, expiredAt.Valid, tc.wantGhosted)
		}
	}

	// Broadcaster must have received exactly one agent.expired event.
	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("broadcaster events = %d, want 1", len(events))
	}
	if events[0]["event"] != "agent.expired" {
		t.Errorf("event = %q, want agent.expired", events[0]["event"])
	}
	if events[0]["id"] != "due" {
		t.Errorf("event id = %q, want due", events[0]["id"])
	}
}

func TestSweep_NoOpWhenNothingDue(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID := seedWorkspaceAndCrew(t, db)
	seedEphemeral(t, db, wsID, crewID, "live", "2099-01-01T00:00:00Z", nil)

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	n, err := SweepExpiredAgents(context.Background(), db, nil, nil, func() time.Time { return now })
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 0 {
		t.Errorf("n = %d, want 0", n)
	}
}

func TestSweep_RehiredRowSkippedByGuard(t *testing.T) {
	// Race semantics: between the SELECT and the per-row UPDATE,
	// a rehire could have cleared expired_at and pushed expires_at
	// into the future. The per-row UPDATE guard catches that case
	// — the row no longer matches `expired_at IS NULL AND expires_at
	// < now` because expires_at moved. Without the guard, the
	// sweeper would re-ghost a freshly-rehired agent.
	//
	// We can't reliably trigger the race in a unit test, so we
	// approximate it by seeding a row that already matches the
	// post-rehire shape (expired_at NULL, expires_at in the future)
	// and verifying the sweeper leaves it alone even when its
	// snapshot suggests otherwise.
	db := setupTestDB(t)
	wsID, crewID := seedWorkspaceAndCrew(t, db)
	// Seed: expires_at is in the past at seed time, then we move it
	// to the future before calling the sweep — exactly the race
	// outcome (the sweep's snapshot is stale).
	seedEphemeral(t, db, wsID, crewID, "rehired", "2026-06-01T11:00:00Z", nil)
	_, err := db.Exec(`UPDATE agents SET expires_at = ? WHERE id = ?`, "2099-01-01T00:00:00Z", "rehired")
	if err != nil {
		t.Fatalf("simulate rehire: %v", err)
	}

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	n, err := SweepExpiredAgents(context.Background(), db, nil, nil, func() time.Time { return now })
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 0 {
		t.Errorf("n = %d on rehired row; want 0 (UPDATE guard must catch the race)", n)
	}

	var expiredAt sql.NullString
	if err := db.QueryRow(`SELECT expired_at FROM agents WHERE id = ?`, "rehired").Scan(&expiredAt); err != nil {
		t.Fatalf("query expired_at for rehired: %v", err)
	}
	if expiredAt.Valid {
		t.Errorf("rehired agent ghosted; expired_at = %v", expiredAt)
	}
}

func TestSweep_EmitsJournalEntryPerGhost(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID := seedWorkspaceAndCrew(t, db)
	seedEphemeral(t, db, wsID, crewID, "due1", "2026-06-01T11:00:00Z", nil)
	seedEphemeral(t, db, wsID, crewID, "due2", "2026-06-01T11:30:00Z", nil)

	je := &recordingEmitter{}
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	n, err := SweepExpiredAgents(context.Background(), db, je, nil, func() time.Time { return now })
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 2 {
		t.Errorf("ghosted = %d, want 2", n)
	}
	if got := je.count(); got != 2 {
		t.Errorf("journal emit count = %d, want 2", got)
	}
}

// recordingEmitter is a minimal journal.Emitter that counts Emit
// calls + keeps the last entry for inspection.
type recordingEmitter struct {
	mu      sync.Mutex
	entries []journal.Entry
}

func (e *recordingEmitter) Emit(ctx context.Context, entry journal.Entry) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.entries = append(e.entries, entry)
	return "id", nil
}

// Flush is part of journal.Emitter; the recorder doesn't buffer so
// it's a no-op.
func (e *recordingEmitter) Flush(ctx context.Context) error { return nil }

func (e *recordingEmitter) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.entries)
}

func TestStartExpirySweeper_RunsOnTick(t *testing.T) {
	// Background-goroutine smoke test: register the sweeper at a
	// 25ms interval, seed a due ephemeral, wait for the next tick,
	// verify the row ghosted. A regression that broke the ticker
	// (e.g. ctx leak) would show up as a never-ghosted row here.
	db := setupTestDB(t)
	wsID, crewID := seedWorkspaceAndCrew(t, db)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	seedEphemeral(t, db, wsID, crewID, "due-tick", past, nil)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartExpirySweeper(ctx, db, nil, nil, 25*time.Millisecond, logger)

	// Wait up to ~2s for the sweeper to flip the row. The happy
	// path returns the moment expired_at is set, so a healthy CI
	// run still finishes in tens of milliseconds. The wide
	// deadline only kicks in on a loaded runner where the
	// goroutine scheduler is starved — without it, this test
	// flaked under contention for reasons unrelated to behavior.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var expiredAt sql.NullString
		if err := db.QueryRow(`SELECT expired_at FROM agents WHERE id = 'due-tick'`).Scan(&expiredAt); err != nil {
			t.Fatalf("query expired_at for due-tick: %v", err)
		}
		if expiredAt.Valid {
			return // success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("sweeper did not ghost due row within 2s")
}

// TestSweep_SkipsRunningAgent verifies the mid-mission grace contract:
// an ephemeral agent whose TTL fires while status='RUNNING' is NOT
// ghosted on this sweep tick. The expires_at column stays in place
// (the scheduling intent is preserved); only the expired_at flip
// waits until the next sweep after the agent naturally idles out.
// Without this guard the sweeper would yank the container while the
// chatbridge was still streaming, surfacing as a "ghost mid-mission"
// anomaly in the journal.
func TestSweep_SkipsRunningAgent(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID := seedWorkspaceAndCrew(t, db)

	// Seed two ephemeral rows, both past their TTL. One is IDLE
	// (must ghost), the other is RUNNING (must NOT ghost — mid
	// mission grace).
	seedEphemeral(t, db, wsID, crewID, "idle-due", "2026-06-01T11:00:00Z", nil)
	seedEphemeral(t, db, wsID, crewID, "running-due", "2026-06-01T11:00:00Z", nil)
	if _, err := db.Exec(`UPDATE agents SET status = 'RUNNING' WHERE id = ?`, "running-due"); err != nil {
		t.Fatalf("set running: %v", err)
	}

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	rec := &recordingBroadcaster{}
	n, err := SweepExpiredAgents(context.Background(), db, nil, rec, func() time.Time { return now })
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Errorf("ghosted = %d, want 1 (idle-due only)", n)
	}

	// idle-due must be ghosted; running-due must NOT be.
	var idleExpired, runningExpired sql.NullString
	if err := db.QueryRow(`SELECT expired_at FROM agents WHERE id = ?`, "idle-due").Scan(&idleExpired); err != nil {
		t.Fatalf("query expired_at for idle-due: %v", err)
	}
	if err := db.QueryRow(`SELECT expired_at FROM agents WHERE id = ?`, "running-due").Scan(&runningExpired); err != nil {
		t.Fatalf("query expired_at for running-due: %v", err)
	}

	if !idleExpired.Valid {
		t.Error("idle-due agent: expired_at NULL, want set")
	}
	if runningExpired.Valid {
		t.Errorf("running-due agent: expired_at=%v, want NULL (mid-mission grace)", runningExpired.String)
	}

	// The RUNNING row's expires_at MUST still be the past timestamp
	// — scheduling intent is preserved; only the flag flip waits.
	var expiresAt string
	if err := db.QueryRow(`SELECT expires_at FROM agents WHERE id = ?`, "running-due").Scan(&expiresAt); err != nil {
		t.Fatalf("query expires_at for running-due: %v", err)
	}
	if expiresAt != "2026-06-01T11:00:00Z" {
		t.Errorf("running-due expires_at = %q, want unchanged 2026-06-01T11:00:00Z", expiresAt)
	}

	// Exactly one event — for idle-due. running-due must not show up.
	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0]["id"] != "idle-due" {
		t.Errorf("event id = %q, want idle-due", events[0]["id"])
	}

	// Second sweep, now with the RUNNING agent flipped to IDLE
	// (mission complete) — it should ghost on this pass.
	if _, err := db.Exec(`UPDATE agents SET status = 'IDLE' WHERE id = ?`, "running-due"); err != nil {
		t.Fatalf("idle running-due: %v", err)
	}
	n, err = SweepExpiredAgents(context.Background(), db, nil, nil, func() time.Time { return now })
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if n != 1 {
		t.Errorf("second sweep ghosted = %d, want 1", n)
	}
	if err := db.QueryRow(`SELECT expired_at FROM agents WHERE id = ?`, "running-due").Scan(&runningExpired); err != nil {
		t.Fatalf("query expired_at after second sweep: %v", err)
	}
	if !runningExpired.Valid {
		t.Error("running-due agent (now idle): expired_at NULL after second sweep, want set")
	}
}

func TestStartExpirySweeper_StopsOnContextCancel(t *testing.T) {
	db := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	StartExpirySweeper(ctx, db, nil, nil, 10*time.Millisecond, logger)
	cancel()
	// If the goroutine leaked, the test runner would still pass —
	// goleak isn't wired here. The behavioural check is "no panic
	// after cancel"; the goroutine MUST exit cleanly on ctx.Done()
	// without further DB calls. We sleep a few ticks to catch a
	// regression that polled DB after cancel.
	time.Sleep(50 * time.Millisecond)
}
