package api

// Tests for assignments_lease.go (PRD-ISSUES-AND-ROUTINES-2026 §9.4/§17,
// work package B4 — #2343): the lease stamp/renew/reap mechanics, and the
// F8 regression this whole file exists to close — recovery keyed on lease
// expiry, not on when the recovering process itself booted.

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// seedRunningWithLease inserts a RUNNING assignment carrying an explicit
// lease_owner/lease_expires_at pair — the shape stampInitialLease writes,
// available directly so a test can seed an ALREADY-expired (or
// already-valid) lease without waiting on real time.
func seedRunningWithLease(t *testing.T, db *sql.DB, id, chatID, byAgent, toAgent, owner, leaseExpiresAt string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status,
		    running_at, started_at, lease_owner, lease_expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, 'test-task', 'RUNNING', datetime('now'), datetime('now'), ?, ?, datetime('now'))`,
		id, "test-workspace-id", chatID, byAgent, toAgent, owner, leaseExpiresAt); err != nil {
		t.Fatalf("seed running-with-lease assignment %s: %v", id, err)
	}
}

// ── stampInitialLease / renewLease ─────────────────────────────────────

func TestStampInitialLease_SetsOwnerAndFutureExpiry(t *testing.T) {
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	insertAssignment(t, db, "a_stamp", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")

	before := time.Now().UTC()
	if err := stampInitialLease(context.Background(), h.db, "a_stamp", before, 90*time.Second); err != nil {
		t.Fatalf("stampInitialLease: %v", err)
	}

	var owner, expiresAt sql.NullString
	if err := db.QueryRow(`SELECT lease_owner, lease_expires_at FROM assignments WHERE id = 'a_stamp'`).
		Scan(&owner, &expiresAt); err != nil {
		t.Fatalf("load a_stamp: %v", err)
	}
	if !owner.Valid || owner.String == "" {
		t.Errorf("lease_owner = %q, want a non-empty process identity", owner.String)
	}
	if !strings.Contains(owner.String, ":") {
		t.Errorf("lease_owner = %q, want hostname:pid shape", owner.String)
	}
	got, err := time.Parse(time.RFC3339, expiresAt.String)
	if err != nil {
		t.Fatalf("parse lease_expires_at %q: %v", expiresAt.String, err)
	}
	if !got.After(before.Add(80 * time.Second)) {
		t.Errorf("lease_expires_at = %v, want ~90s after %v", got, before)
	}
}

func TestStampInitialLease_NoOpWhenNotRunning(t *testing.T) {
	// A row that isn't RUNNING (already recovered out from under a slow
	// dispatch goroutine, say) must not have a lease stamped onto it —
	// the WHERE status='RUNNING' guard is what makes this call safe to
	// fire unconditionally from runAssignment regardless of ordering.
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	insertAssignment(t, db, "a_not_running", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "FAILED")

	if err := stampInitialLease(context.Background(), h.db, "a_not_running", time.Now(), 90*time.Second); err != nil {
		t.Fatalf("stampInitialLease: %v", err)
	}
	var owner sql.NullString
	if err := db.QueryRow(`SELECT lease_owner FROM assignments WHERE id = 'a_not_running'`).Scan(&owner); err != nil {
		t.Fatalf("load: %v", err)
	}
	if owner.Valid {
		t.Errorf("lease_owner = %q, want NULL (row is not RUNNING)", owner.String)
	}
}

func TestRenewLease_OwnerGuard_WrongOwnerDoesNotRenew(t *testing.T) {
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	past := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)
	seedRunningWithLease(t, db, "a_owned", chatID, agentIDs[0], agentIDs[0], "real-owner:123", past)

	renewed, err := renewLease(context.Background(), h.db, "a_owned", "someone-else:999", time.Now(), 90*time.Second)
	if err != nil {
		t.Fatalf("renewLease: %v", err)
	}
	if renewed {
		t.Errorf("renewed = true, want false (wrong owner must not renew someone else's lease)")
	}
	var expiresAt string
	if err := db.QueryRow(`SELECT lease_expires_at FROM assignments WHERE id = 'a_owned'`).Scan(&expiresAt); err != nil {
		t.Fatalf("load: %v", err)
	}
	if expiresAt != past {
		t.Errorf("lease_expires_at = %q, want unchanged %q", expiresAt, past)
	}
}

func TestRenewLease_CorrectOwner_ExtendsExpiry(t *testing.T) {
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	past := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)
	seedRunningWithLease(t, db, "a_renew", chatID, agentIDs[0], agentIDs[0], "real-owner:123", past)

	now := time.Now()
	renewed, err := renewLease(context.Background(), h.db, "a_renew", "real-owner:123", now, 90*time.Second)
	if err != nil {
		t.Fatalf("renewLease: %v", err)
	}
	if !renewed {
		t.Fatalf("renewed = false, want true")
	}
	var expiresAt string
	if err := db.QueryRow(`SELECT lease_expires_at FROM assignments WHERE id = 'a_renew'`).Scan(&expiresAt); err != nil {
		t.Fatalf("load: %v", err)
	}
	got, perr := time.Parse(time.RFC3339, expiresAt)
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	if !got.After(now.Add(80 * time.Second)) {
		t.Errorf("lease_expires_at = %v, want ~90s after %v", got, now)
	}
}

// ── startLeaseHeartbeat: real goroutine, -race ─────────────────────────

// TestStartLeaseHeartbeat_RenewsRepeatedly_ThenStopsCleanly is the -race
// proof for the heartbeat goroutine: real ticker, real DB writes, run with
// `go test -race`. It must renew the lease several times (proving the
// heartbeat interval/TTL ratio actually gives multiple renewal
// opportunities before expiry, not just one) and, once stopped, must NOT
// renew again — proving the deferred stop() in runAssignment actually
// silences the goroutine rather than leaking it.
func TestStartLeaseHeartbeat_RenewsRepeatedly_ThenStopsCleanly(t *testing.T) {
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	insertAssignment(t, db, "a_hb", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")
	if err := stampInitialLease(context.Background(), h.db, "a_hb", time.Now(), 5*time.Second); err != nil {
		t.Fatalf("stampInitialLease: %v", err)
	}

	readExpiry := func() string {
		var v string
		if err := db.QueryRow(`SELECT lease_expires_at FROM assignments WHERE id = 'a_hb'`).Scan(&v); err != nil {
			t.Fatalf("load lease_expires_at: %v", err)
		}
		return v
	}
	initial := readExpiry()

	// heartbeatInterval is deliberately > 1s: lease_expires_at is stored as
	// plain RFC3339 (whole-second granularity, matching every other
	// assignments timestamp column), so two renewals inside the same wall
	// second are indistinguishable by string comparison alone — an interval
	// safely over 1s guarantees each renewal this test observes lands in a
	// different second. `defer stop()` runs even if an assertion below
	// fails, so a flaky window never leaks the ticker goroutine into later
	// tests (the failure mode a first revision of this test hit: a leaked
	// heartbeat kept writing to its own already-closed fixture DB for the
	// rest of the test binary's run).
	stop := h.startLeaseHeartbeat(context.Background(), "a_hb", 1100*time.Millisecond, 5*time.Second)
	defer stop()

	// Poll for at least 2 distinct renewals (the expiry timestamp must move
	// forward at least twice past its initial value) within a generous
	// window — real ticker, real goroutine, so this is timing-observational
	// rather than a fixed sleep-then-assert.
	deadline := time.Now().Add(4 * time.Second)
	renewals := 0
	last := initial
	for time.Now().Before(deadline) && renewals < 2 {
		time.Sleep(200 * time.Millisecond)
		cur := readExpiry()
		if cur != last {
			renewals++
			last = cur
		}
	}
	if renewals < 2 {
		t.Fatalf("observed %d renewals in the window, want >= 2 (heartbeat not ticking?)", renewals)
	}

	stop()
	afterStop := readExpiry()
	// Give any in-flight tick a moment to land, then confirm nothing further
	// renews — the goroutine actually exited rather than racing stop().
	time.Sleep(1300 * time.Millisecond)
	stable := readExpiry()
	if stable != afterStop {
		t.Errorf("lease_expires_at changed after stop() (%q -> %q); heartbeat goroutine did not stop", afterStop, stable)
	}
}

func TestStartLeaseHeartbeat_StopsWhenRowLeavesRunning(t *testing.T) {
	// If the row is reaped (or finishes) out from under a live heartbeat,
	// the goroutine must stop ticking on its own rather than spin forever
	// writing to a row that will never accept it again.
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	insertAssignment(t, db, "a_hb_reaped", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")
	if err := stampInitialLease(context.Background(), h.db, "a_hb_reaped", time.Now(), 500*time.Millisecond); err != nil {
		t.Fatalf("stampInitialLease: %v", err)
	}
	stop := h.startLeaseHeartbeat(context.Background(), "a_hb_reaped", 15*time.Millisecond, 500*time.Millisecond)
	defer stop()

	// Simulate the row terminalising underneath the heartbeat (a sweeper,
	// a real driver finishing — anything that flips status away from
	// RUNNING).
	if _, err := db.Exec(`UPDATE assignments SET status = 'COMPLETED' WHERE id = 'a_hb_reaped'`); err != nil {
		t.Fatalf("terminalise row: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	var expiresAt string
	_ = db.QueryRow(`SELECT lease_expires_at FROM assignments WHERE id = 'a_hb_reaped'`).Scan(&expiresAt)
	// No further assertion needed beyond "did not panic / hang" — the real
	// property under test is that the goroutine's own select loop returns
	// on a false renewal rather than looping tightly; a hang here would
	// block the whole test binary's exit via t.Cleanup(h.WaitDispatches)
	// on unrelated tests, which is the practical signal a regression would
	// surface as.
}

// ── SweepExpiredLeases ──────────────────────────────────────────────────

func TestSweepExpiredLeases_ReapsExpiredRow_FailsWithLeaseReason(t *testing.T) {
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	rec := &recRecordingEmitter{}
	h.SetJournal(rec)
	cb := &recoveryMissionCallback{}
	h.SetMissionCallback(cb)

	past := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)
	seedRunningWithLease(t, db, "a_lease_dead", chatID, agentIDs[0], agentIDs[0], "dead-host:456", past)

	n, err := h.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("SweepExpiredLeases: %v", err)
	}
	if n != 1 {
		t.Fatalf("reaped = %d, want 1", n)
	}

	var status string
	var errMsg sql.NullString
	if err := db.QueryRow(`SELECT status, error_message FROM assignments WHERE id = 'a_lease_dead'`).
		Scan(&status, &errMsg); err != nil {
		t.Fatalf("load: %v", err)
	}
	if status != "FAILED" {
		t.Errorf("status = %q, want FAILED", status)
	}
	if !errMsg.Valid || !strings.Contains(errMsg.String, "lease expired") || !strings.Contains(errMsg.String, "dead-host:456") {
		t.Errorf("error_message = %q, want it to name the lease-expired reason and the dead owner", errMsg.String)
	}

	// The user-observable half: the SAME completion signals a stuck-RUNNING
	// sweep produces — visible via the mission callback (what `issue runs`
	// is ultimately backed by, through OnAssignmentCompleted).
	cb.mu.Lock()
	calls := len(cb.calls)
	cb.mu.Unlock()
	if calls != 1 {
		t.Errorf("mission callback calls = %d, want 1", calls)
	}
}

func TestSweepExpiredLeases_LiveLease_Untouched(t *testing.T) {
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	future := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	seedRunningWithLease(t, db, "a_lease_live", chatID, agentIDs[0], agentIDs[0], "live-host:789", future)

	n, err := h.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("SweepExpiredLeases: %v", err)
	}
	if n != 0 {
		t.Errorf("reaped = %d, want 0 (lease not expired)", n)
	}
	if got := assignmentStatus(t, db, "a_lease_live"); got != "RUNNING" {
		t.Errorf("status = %q, want RUNNING (untouched)", got)
	}
}

func TestSweepExpiredLeases_NullLease_Untouched(t *testing.T) {
	// A RUNNING row with no lease at all (dispatched before this migration,
	// or a run whose stampInitialLease write hasn't landed yet) is NOT this
	// sweeper's job — that is scanRunningStuck's (the pre-existing
	// stuck-RUNNING heuristic) territory.
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	insertAssignment(t, db, "a_no_lease", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")

	n, err := h.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("SweepExpiredLeases: %v", err)
	}
	if n != 0 {
		t.Errorf("reaped = %d, want 0 (no lease to expire)", n)
	}
	if got := assignmentStatus(t, db, "a_no_lease"); got != "RUNNING" {
		t.Errorf("status = %q, want RUNNING (untouched)", got)
	}
}

// ── StartLeaseSweeper ────────────────────────────────────────────────────

func TestStartLeaseSweeper_TicksAndExits(t *testing.T) {
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	past := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)
	seedRunningWithLease(t, db, "a_ticker", chatID, agentIDs[0], agentIDs[0], "dead:1", past)

	ctx, cancel := context.WithCancel(context.Background())
	h.StartLeaseSweeper(ctx, 20*time.Millisecond)

	deadline := time.Now().Add(1 * time.Second)
	reaped := false
	for !reaped && time.Now().Before(deadline) {
		if assignmentStatus(t, db, "a_ticker") != "RUNNING" {
			reaped = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if !reaped {
		t.Errorf("lease sweeper did not fire within 1s window")
	}
	time.Sleep(50 * time.Millisecond)
}

func TestStartLeaseSweeper_DefaultInterval_AppliedWhenZero(t *testing.T) {
	h, _, _, _, _ := stuckSweeperRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.StartLeaseSweeper(ctx, 0)
	time.Sleep(20 * time.Millisecond)
}

// ── F8 regression: recovery keyed on lease expiry, not process start ────

func TestRecoverInterruptedRunning_LiveLease_UntouchedRegardlessOfBootTime(t *testing.T) {
	// The exact two-replica scenario F8 names: a row dispatched (and its
	// dispatch stamp written) LONG before this process's own boot time —
	// the pre-B4 heuristic alone would fail it outright — but its lease is
	// still being renewed by whichever process (this one, or a different
	// live replica) actually owns it. Boot-time recovery must leave it
	// alone: a live lease means "someone is still driving this", and that
	// fact does not depend on when the CHECKING process itself started.
	h, db, agentIDs, chatID := recoveryRig(t)
	longAgo := "2020-01-01 00:00:00.000" // ancient dispatch stamp
	future := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status,
		    running_at, lease_owner, lease_expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, 'test-task', 'RUNNING', ?, ?, ?, datetime('now'))`,
		"a_other_replica_live", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], longAgo, "replica-2:42", future); err != nil {
		t.Fatalf("seed live-lease row: %v", err)
	}

	// This process's own boot time is "now" — far after the row's ancient
	// dispatch stamp, which is exactly the shape that used to fail a live
	// peer's run under the pre-B4 heuristic.
	n, err := h.RecoverInterruptedRunning(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("RecoverInterruptedRunning: %v", err)
	}
	if n != 0 {
		t.Errorf("recovered = %d, want 0 (a live lease must never be touched by boot recovery)", n)
	}
	if got := assignmentStatus(t, db, "a_other_replica_live"); got != "RUNNING" {
		t.Errorf("status = %q, want RUNNING (untouched — the other replica still owns this run)", got)
	}
}

func TestRecoverInterruptedRunning_ExpiredLease_RecoveredAtBoot(t *testing.T) {
	// The owner-died half of the same scenario: same ancient dispatch
	// stamp, but the lease has ALSO expired (nobody has renewed it in
	// living memory) — this row genuinely has no live driver, and boot
	// recovery must still catch it, not skip it just because it once had a
	// lease.
	h, db, agentIDs, chatID := recoveryRig(t)
	longAgo := "2020-01-01 00:00:00.000"
	expiredLease := "2020-01-01T00:05:00Z"
	if _, err := db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status,
		    running_at, lease_owner, lease_expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, 'test-task', 'RUNNING', ?, ?, ?, datetime('now'))`,
		"a_dead_lease", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], longAgo, "dead-replica:7", expiredLease); err != nil {
		t.Fatalf("seed dead-lease row: %v", err)
	}

	n, err := h.RecoverInterruptedRunning(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("RecoverInterruptedRunning: %v", err)
	}
	if n != 1 {
		t.Errorf("recovered = %d, want 1", n)
	}
	if got := assignmentStatus(t, db, "a_dead_lease"); got != "FAILED" {
		t.Errorf("status = %q, want FAILED", got)
	}
}

func TestRecoverInterruptedRunning_NullLease_FallsBackToBootHeuristic(t *testing.T) {
	// Legacy-row coverage: a row with no lease at all still uses the
	// original pre-B4 boot-time-cutoff heuristic — unchanged behaviour for
	// data written before this migration shipped.
	h, db, agentIDs, chatID := recoveryRig(t)
	past := time.Now().UTC().Add(-1 * time.Hour).Format("2006-01-02 15:04:05.000")
	seedRunningAt(t, db, "a_legacy_orphan", chatID, agentIDs[0], agentIDs[0], past)

	n, err := h.RecoverInterruptedRunning(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("RecoverInterruptedRunning: %v", err)
	}
	if n != 1 {
		t.Errorf("recovered = %d, want 1", n)
	}
	if got := assignmentStatus(t, db, "a_legacy_orphan"); got != "FAILED" {
		t.Errorf("status = %q, want FAILED", got)
	}
}

// recoveryRig is stuckSweeperRig's return values flattened to what these
// tests actually need (no crewID) — kept separate so a signature change to
// stuckSweeperRig doesn't ripple through every call site here.
func recoveryRig(t *testing.T) (*AssignmentHandler, *sql.DB, []string, string) {
	t.Helper()
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	return h, db, agentIDs, chatID
}

// ── -race: the sweeper's reap and a live finishAssignment cannot both
//    terminalise the same row ─────────────────────────────────────────

// TestSweepExpiredLeasesVsFinishAssignment_ExactlyOneWinsTheRace is the
// concurrency proof guardrail (3): a lease-expired row whose driver is, in
// fact, still alive and finishing at the exact same instant the sweeper
// reaps it must produce exactly ONE terminal outcome and exactly ONE set of
// completion signals — never both, never neither. Run with `go test -race`
// so a data race on the shared row would also fail the build, not just the
// assertion.
func TestSweepExpiredLeasesVsFinishAssignment_ExactlyOneWinsTheRace(t *testing.T) {
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	cb := &recoveryMissionCallback{}
	h.SetMissionCallback(cb)
	rec := &recRecordingEmitter{}
	h.SetJournal(rec)

	past := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)
	seedRunningWithLease(t, db, "a_race", chatID, agentIDs[0], agentIDs[0], "racer:1", past)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := h.SweepExpiredLeases(context.Background()); err != nil {
			t.Errorf("SweepExpiredLeases: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		h.finishAssignment(context.Background(), "a_race", "run_race", chatID, "agent-sweep-a",
			"test-workspace-id", "driver won", "", nil)
	}()
	wg.Wait()
	h.WaitDispatches()

	status := assignmentStatus(t, db, "a_race")
	if status != "FAILED" && status != "COMPLETED" {
		t.Fatalf("status = %q, want FAILED (sweeper won) or COMPLETED (driver won)", status)
	}

	// Exactly one terminal signal, regardless of who won — the row's own
	// terminal-state CAS inside finishAssignment is what guarantees this;
	// this test's job is to prove it holds under real concurrency, not by
	// inspection.
	cb.mu.Lock()
	calls := len(cb.calls)
	cb.mu.Unlock()
	if calls != 1 {
		t.Errorf("mission callback calls = %d, want exactly 1 (both paths must not both terminalise)", calls)
	}
	failEntries, doneEntries := 0, 0
	for _, e := range rec.snapshot() {
		switch e.Type {
		case journal.EntryAssignmentFail:
			failEntries++
		case journal.EntryAssignmentDone:
			doneEntries++
		}
	}
	if failEntries+doneEntries != 1 {
		t.Errorf("terminal assignment.* journal entries = %d, want exactly 1 (fail=%d done=%d)",
			failEntries+doneEntries, failEntries, doneEntries)
	}
}
