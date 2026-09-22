package scheduler

// Occurrence audit ahead of the #2643 scheduler migration. These tests do not
// re-cover what scheduler_test.go already proves (same-occurrence dedup across
// skewed clocks, distinct occurrences both firing, reserve-error fail-closed
// producing zero runs). They pin down the OCCURRENCE STATE after each
// early-exit branch, and the identity occurrenceBucket derives when the read
// of schedule_next_run does not succeed.
//
// All of it runs against real temporary SQLite (in-memory for whole-path
// fires, file-backed for the cross-connection lock test). Nothing here
// executes a real CLI adapter or a real server restart; the orchestrator is
// the same in-memory harness the #816 tests use, so evidence is about the
// scheduler's dedup logic, not about live runtimes.

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// occurrenceAgent is the scheduledAgent every audit fire uses.
func occurrenceAgent() scheduledAgent {
	return scheduledAgent{ID: "a1", Slug: "bob", Name: "Bob", CrewID: "crew1", CrewSlug: "alpha",
		Cron: "* * * * *", Prompt: "do work", Workspace: "ws1"}
}

// idemRowCount counts reservation rows left for the workspace.
func idemRowCount(t *testing.T, db *sql.DB, workspace string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pipeline_run_idempotency WHERE workspace_id = ?`, workspace).Scan(&n); err != nil {
		t.Fatalf("count idem rows: %v", err)
	}
	return n
}

// agentSchedule reads schedule_last_run / schedule_next_run for the agent.
func agentSchedule(t *testing.T, db *sql.DB, agentID string) (lastRun, nextRun sql.NullString) {
	t.Helper()
	if err := db.QueryRowContext(t.Context(), `SELECT schedule_last_run, schedule_next_run FROM agents WHERE id = ?`, agentID).
		Scan(&lastRun, &nextRun); err != nil {
		t.Fatalf("read agent schedule: %v", err)
	}
	return lastRun, nextRun
}

// nextMinuteBoundary mirrors updateTimestamps' next-slot computation for a
// every-minute cron: the first minute boundary strictly after t.
func nextMinuteBoundary(t time.Time) string {
	return t.Truncate(time.Minute).Add(time.Minute).UTC().Format(time.RFC3339)
}

// ---------------------------------------------------------------------------
// Point 2 — busy agent: what the skip leaves behind.
// ---------------------------------------------------------------------------

// TestOccurrenceAudit_BusyAgentSkipsOccurrence pins the exact state after the
// agentRunLock bounce (scheduler.go busy branch): zero runs, the idempotency
// reservation is DELETED (Forget), schedule_next_run advances to the next slot
// and schedule_last_run stays NULL. Nothing anywhere defers the work — no
// queue row, no surviving reservation — so this is a SKIP of the occurrence,
// not a deferral. The migration to the durable dispatcher changes exactly
// this: the occurrence becomes queued work that waits for capacity instead of
// being consumed by the skip.
func TestOccurrenceAudit_BusyAgentSkipsOccurrence(t *testing.T) {
	db := testDB(t)
	seedCrew(t, db, "crew1", "ws1", "Alpha", "alpha")
	seedAgent(t, db, "a1", "bob", "Bob", "crew1", "ws1", "* * * * *", "do work", true)

	fixed := time.Date(2026, 7, 6, 8, 0, 30, 0, time.UTC)
	s, resolver := newIdemFireScheduler(t, db, func() time.Time { return fixed })

	lock := chatbridge.NewAgentRunLock()
	s.SetAgentRunLock(lock)
	if !lock.TryStart("a1") { // the agent is busy with a chat run
		t.Fatal("test could not hold the agent lock")
	}
	t.Cleanup(func() { lock.End("a1") })

	setNextRun(t, db, "a1", "2026-07-06T08:00:00Z")
	before := time.Now()
	s.triggerAgent(occurrenceAgent())

	resolver.mu.Lock()
	if n := len(resolver.createdRuns); n != 0 {
		resolver.mu.Unlock()
		t.Fatalf("busy skip produced %d runs, want 0", n)
	}
	resolver.mu.Unlock()

	if n := idemRowCount(t, db, "ws1"); n != 0 {
		t.Errorf("reservation survived the busy skip: %d rows in pipeline_run_idempotency, want 0 (Forget must free it)", n)
	}

	lastRun, nextRun := agentSchedule(t, db, "a1")
	if lastRun.Valid && lastRun.String != "" {
		t.Errorf("schedule_last_run = %q after a busy skip, want unset (the occurrence never ran)", lastRun.String)
	}
	if !nextRun.Valid || nextRun.String == "" {
		t.Fatalf("schedule_next_run unset after busy skip, want advanced to the next slot")
	}
	// updateTimestamps computes the next slot from the REAL clock, so accept
	// the boundary computed just before or just after the fire.
	want1, want2 := nextMinuteBoundary(before), nextMinuteBoundary(time.Now())
	if nextRun.String != want1 && nextRun.String != want2 {
		t.Errorf("schedule_next_run = %q, want %q (or %q across a minute rollover): the skip must consume the occurrence by advancing the schedule", nextRun.String, want1, want2)
	}
	if nextRun.String == "2026-07-06T08:00:00Z" {
		t.Errorf("schedule_next_run still the skipped occurrence 08:00 — the skip did not consume it")
	}
}

// ---------------------------------------------------------------------------
// Point 3 — occurrenceBucket identity when the read does not succeed.
// ---------------------------------------------------------------------------

// TestOccurrenceAudit_BucketIdentityMatrix pins what identity each read
// outcome produces. A present, parseable value is normalized to UTC; a MISSING
// value (NULL or empty) falls back to the wall-clock minute — the documented
// force-fire case; a present but unparsable value is returned raw, which still
// yields a stable key.
func TestOccurrenceAudit_BucketIdentityMatrix(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "* * * * *", "do work", true)
	s, _ := newIdemFireScheduler(t, db, func() time.Time { return time.Date(2026, 7, 6, 8, 7, 30, 0, time.UTC) })
	ctx := context.Background()

	cases := []struct {
		name   string
		stored sql.NullString
		want   string
	}{
		{"valid UTC", sql.NullString{String: "2026-07-06T08:00:00Z", Valid: true}, "2026-07-06T08:00:00Z"},
		{"valid offset normalizes to UTC", sql.NullString{String: "2026-07-06T10:00:00+02:00", Valid: true}, "2026-07-06T08:00:00Z"},
		{"NULL falls back to wall minute", sql.NullString{Valid: false}, "2026-07-06T08:07:00Z"},
		{"empty falls back to wall minute", sql.NullString{String: "", Valid: true}, "2026-07-06T08:07:00Z"},
		{"unparsable returned raw", sql.NullString{String: "not-a-timestamp", Valid: true}, "not-a-timestamp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var v interface{}
			if tc.stored.Valid {
				v = tc.stored.String
			}
			if _, err := db.ExecContext(t.Context(), `UPDATE agents SET schedule_next_run = ? WHERE id = 'a1'`, v); err != nil {
				t.Fatalf("seed schedule_next_run: %v", err)
			}
			if got, err := s.occurrenceBucket(ctx, "a1"); err != nil || got != tc.want {
				t.Errorf("occurrenceBucket = %q, err=%v, want %q", got, err, tc.want)
			}
		})
	}
}

// fileDB opens a file-backed SQLite with the test schema in rollback-journal
// mode and busy_timeout(0), so a BEGIN EXCLUSIVE on a second connection makes
// reads on this one fail immediately with SQLITE_BUSY — a real read error,
// not a mock. Returns the handle and the file's path (for the second handle).
func fileDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "occurrence.db")
	dsn := "file:" + path + "?_pragma=journal_mode(delete)&_pragma=busy_timeout(0)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open file db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE agents (
		id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, name TEXT, slug TEXT,
		agent_role TEXT DEFAULT 'AGENT', deleted_at TEXT, schedule_cron TEXT,
		schedule_prompt TEXT, schedule_enabled INTEGER DEFAULT 0,
		schedule_last_run TEXT, schedule_next_run TEXT)`); err != nil {
		t.Fatalf("create agents: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE pipeline_run_idempotency (
		workspace_id TEXT NOT NULL, idempotency_key TEXT NOT NULL, run_id TEXT NOT NULL,
		pipeline_id TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT (datetime('now','subsec')),
		expires_at TEXT NOT NULL, PRIMARY KEY (workspace_id, idempotency_key))`); err != nil {
		t.Fatalf("create idem table: %v", err)
	}
	return db, path
}

// A real read lock failure must not create a substitute identity. This uses
// rollback-journal SQLite deliberately to force a deterministic read error;
// it does not claim an EXCLUSIVE lock has identical effects under production WAL.
func TestOccurrenceAudit_ReadError_SQLITE_BUSY_RefusesNewIdentity(t *testing.T) {
	db, path := fileDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "crew1", "ws1", "* * * * *", "do work", true)
	const due = "2026-07-06T08:00:00Z"
	setNextRun(t, db, "a1", due)
	s, resolver := newIdemFireScheduler(t, db, func() time.Time {
		return time.Date(2026, 7, 6, 8, 7, 30, 0, time.UTC)
	})
	ctx := context.Background()
	key := pipeline.ScheduledFireIdempotencyKey("agent-sched", "a1", due)
	if _, fresh, err := s.idem.LookupOrReserve(ctx, "ws1", key, "run-1", "a1", pipeline.DefaultIdempotencyTTL); err != nil || !fresh {
		t.Fatalf("reserve true occurrence: new=%v err=%v", fresh, err)
	}
	lockDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer lockDB.Close()
	conn, err := lockDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, "ROLLBACK")

	var value sql.NullString
	err = db.QueryRowContext(t.Context(), `SELECT schedule_next_run FROM agents WHERE id = 'a1'`).Scan(&value)
	var coded interface{ Code() int }
	if !errors.As(err, &coded) || coded.Code() != 5 {
		t.Fatalf("expected SQLITE_BUSY, got %v", err)
	}
	got, err := s.occurrenceBucket(ctx, "a1")
	if got != "" || !errors.As(err, &coded) || coded.Code() != 5 {
		t.Fatalf("locked occurrence must fail without an identity: got=%q err=%v", got, err)
	}
	s.triggerAgent(occurrenceAgent())
	if len(resolver.createdChats) != 0 || len(resolver.createdRuns) != 0 {
		t.Fatal("read failure reached chat/run creation")
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	// After the lock is gone, retry must use the original identity and dedupe.
	s.triggerAgent(occurrenceAgent())
	if len(resolver.createdRuns) != 0 || idemRowCount(t, db, "ws1") != 1 {
		t.Fatal("read failure followed by retry created a second occurrence")
	}
	last, next := agentSchedule(t, db, "a1")
	if last.Valid || next.String != due {
		t.Fatalf("read failure consumed occurrence: last=%v next=%v", last, next)
	}
}

// A stale tick must stop if its agent row disappeared. The resolver intentionally
// remains a stub returning the old agent: this proves fail-closed scheduler
// behavior, not that production authorization would accept a deleted agent.
// Together with the real SQLITE_BUSY test this covers two read-error paths;
// it is not an end-to-end transient-lock reproduction against production WAL.
func TestOccurrenceAudit_MissingAgentDoesNotManufactureSecondRun(t *testing.T) {
	db := testDB(t)
	seedCrew(t, db, "crew1", "ws1", "Alpha", "alpha")
	seedAgent(t, db, "a1", "bob", "Bob", "crew1", "ws1", "* * * * *", "do work", true)

	s, resolver := newIdemFireScheduler(t, db, func() time.Time { return time.Date(2026, 7, 6, 8, 0, 30, 0, time.UTC) })
	ag := occurrenceAgent()

	// Fire #1: occurrence 08:00, healthy read, reserves and runs.
	setNextRun(t, db, "a1", "2026-07-06T08:00:00Z")
	s.triggerAgent(ag)

	// A duplicate tick for the SAME occurrence, whose bucket read errors.
	setNextRun(t, db, "a1", "2026-07-06T08:00:00Z") // replica/restart still holds 08:00
	s.nowFn = func() time.Time { return time.Date(2026, 7, 6, 8, 7, 30, 0, time.UTC) }
	if _, err := db.ExecContext(t.Context(), `DELETE FROM agents WHERE id = 'a1'`); err != nil {
		t.Fatalf("delete agent row (read-error stand-in): %v", err)
	}
	s.triggerAgent(ag)

	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if n := len(resolver.createdRuns); n != 1 {
		t.Fatalf("missing agent must stop the stale tick: got %d CreateRun calls, want only the original one", n)
	}
}

// ---------------------------------------------------------------------------
// Point 3c — reserve error keeps the occurrence unconsumed.
// ---------------------------------------------------------------------------

// TestOccurrenceAudit_ReserveErrorKeepsOccurrenceUnconsumed extends
// TestScheduledFire_ReserveErrorFailsClosed (which proves zero runs) with the
// occurrence-state contract: after a failed reservation the occurrence is
// not consumed by this failed attempt — schedule_next_run stays at the due
// occurrence, schedule_last_run stays unset, and no reservation row exists.
func TestOccurrenceAudit_ReserveErrorKeepsOccurrenceUnconsumed(t *testing.T) {
	db := testDB(t)
	seedCrew(t, db, "crew1", "ws1", "Alpha", "alpha")
	seedAgent(t, db, "a1", "bob", "Bob", "crew1", "ws1", "* * * * *", "do work", true)

	fixed := time.Date(2026, 7, 6, 8, 0, 30, 0, time.UTC)
	s, resolver := newIdemFireScheduler(t, db, func() time.Time { return fixed })
	setNextRun(t, db, "a1", "2026-07-06T08:00:00Z")

	// The idem store is wired (New saw the table) but the table is gone:
	// occurrenceBucket reads fine, LookupOrReserve errors.
	if _, err := db.ExecContext(t.Context(), `DROP TABLE pipeline_run_idempotency`); err != nil {
		t.Fatalf("drop idem table: %v", err)
	}

	s.triggerAgent(occurrenceAgent())

	resolver.mu.Lock()
	if n := len(resolver.createdRuns); n != 0 {
		resolver.mu.Unlock()
		t.Fatalf("reserve error produced %d runs, want 0 (fail closed)", n)
	}
	resolver.mu.Unlock()

	lastRun, nextRun := agentSchedule(t, db, "a1")
	if lastRun.Valid && lastRun.String != "" {
		t.Errorf("schedule_last_run = %q after reserve error, want unset", lastRun.String)
	}
	if !nextRun.Valid || nextRun.String != "2026-07-06T08:00:00Z" {
		t.Errorf("schedule_next_run = %+v after reserve error, want the due occurrence 2026-07-06T08:00:00Z untouched — the next tick must retry it", nextRun)
	}
}
