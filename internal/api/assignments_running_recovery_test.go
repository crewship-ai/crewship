package api

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// recoveryMissionCallback records OnAssignmentCompleted invocations so
// tests can assert recovery replays the normal completion signal into
// the mission engine (the same plumbing a real failure uses).
type recoveryMissionCallback struct {
	mu    sync.Mutex
	calls []struct {
		assignmentID, status, errMsg string
	}
}

func (m *recoveryMissionCallback) OnAssignmentCompleted(_ context.Context, assignmentID, status, _ string, errorMessage string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, struct{ assignmentID, status, errMsg string }{assignmentID, status, errorMessage})
	return nil
}

// recRecordingEmitter captures journal.Emit calls (thread-safe: the
// recovery path pumps freed slots, and pumped dispatch goroutines emit
// concurrently with the test's assertions).
type recRecordingEmitter struct {
	mu      sync.Mutex
	entries []journal.Entry
}

func (e *recRecordingEmitter) Emit(_ context.Context, entry journal.Entry) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.entries = append(e.entries, entry)
	return "rec_" + entry.Summary, nil
}
func (e *recRecordingEmitter) Flush(_ context.Context) error { return nil }

func (e *recRecordingEmitter) snapshot() []journal.Entry {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]journal.Entry, len(e.entries))
	copy(out, e.entries)
	return out
}

// seedRunningAt inserts a RUNNING assignment with an explicit
// running_at stamp (the dispatcher-side 'YYYY-MM-DD HH:MM:SS.SSS'
// shape claimCrewSlot writes).
func seedRunningAt(t *testing.T, db *sql.DB, id, chatID, byAgent, toAgent, runningAt string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, running_at, created_at)
		VALUES (?, ?, ?, ?, ?, 'test-task', 'RUNNING', ?, datetime('now'))`,
		id, "test-workspace-id", chatID, byAgent, toAgent, runningAt); err != nil {
		t.Fatalf("seed running assignment %s: %v", id, err)
	}
}

// seedRunningStartedRFC3339 inserts a RUNNING assignment whose only
// stamp is started_at in the RFC3339 'T'/'Z' shape runAssignment
// writes — the format-robustness case for the julianday comparison.
func seedRunningStartedRFC3339(t *testing.T, db *sql.DB, id, chatID, byAgent, toAgent string, startedAt time.Time) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, started_at, created_at)
		VALUES (?, ?, ?, ?, ?, 'test-task', 'RUNNING', ?, datetime('now'))`,
		id, "test-workspace-id", chatID, byAgent, toAgent, startedAt.UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed running assignment %s: %v", id, err)
	}
}

// ── RecoverInterruptedRunning ─────────────────────────────────────────

func TestRecoverInterruptedRunning_FailsOrphan_FreesSlot_EmitsSignals(t *testing.T) {
	// The core leak scenario: budget-1 crew, one pre-boot RUNNING orphan.
	// Before recovery the orphan blocks claimCrewSlot; after recovery the
	// row is FAILED with a restart reason, the slot claims again, the
	// mission callback saw the failure, and the journal recorded it.
	h, db, crewID, agentIDs, chatID := stuckSweeperRig(t)
	setCrewBudget(t, db, crewID, 1)
	rec := &recRecordingEmitter{}
	h.SetJournal(rec)
	cb := &recoveryMissionCallback{}
	h.SetMissionCallback(cb)

	past := time.Now().UTC().Add(-1 * time.Hour).Format("2006-01-02 15:04:05.000")
	seedRunningAt(t, db, "a_orphan", chatID, agentIDs[0], agentIDs[0], past)
	insertAssignment(t, db, "a_wants_slot", "test-workspace-id", chatID, agentIDs[0], agentIDs[1], "PENDING")

	// Precondition — the leak: the orphaned RUNNING row consumes the
	// crew's only slot, so a fresh dispatch cannot claim.
	claimed, err := claimCrewSlot(context.Background(), db, "a_wants_slot", crewID, 1)
	if err != nil {
		t.Fatalf("claimCrewSlot precondition: %v", err)
	}
	if claimed {
		t.Fatalf("precondition broken: claim succeeded while orphan holds the slot")
	}

	n, err := h.RecoverInterruptedRunning(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("RecoverInterruptedRunning: %v", err)
	}
	if n != 1 {
		t.Errorf("recovered = %d, want 1", n)
	}

	var status string
	var errMsg, finishedAt sql.NullString
	if err := db.QueryRow(`SELECT status, error_message, finished_at FROM assignments WHERE id = 'a_orphan'`).Scan(&status, &errMsg, &finishedAt); err != nil {
		t.Fatalf("load orphan: %v", err)
	}
	if status != "FAILED" {
		t.Errorf("orphan status = %q, want FAILED", status)
	}
	if !errMsg.Valid || !strings.Contains(errMsg.String, "interrupted by server restart") {
		t.Errorf("orphan error_message = %q, want restart-recovery reason", errMsg.String)
	}
	if !finishedAt.Valid || finishedAt.String == "" {
		t.Errorf("orphan finished_at not set")
	}

	// Slot freed: the pending dispatch can claim now.
	claimed, err = claimCrewSlot(context.Background(), db, "a_wants_slot", crewID, 1)
	if err != nil {
		t.Fatalf("claimCrewSlot after recovery: %v", err)
	}
	if !claimed {
		t.Errorf("claimCrewSlot still blocked after recovery — slot not freed")
	}

	// Completion signals: mission callback saw the FAILED transition
	// with the recovery reason (same plumbing that fires the WS
	// assignment_failed broadcast in finishAssignment).
	cb.mu.Lock()
	calls := len(cb.calls)
	var gotCall struct{ assignmentID, status, errMsg string }
	if calls > 0 {
		gotCall = cb.calls[0]
	}
	cb.mu.Unlock()
	if calls != 1 {
		t.Fatalf("mission callback calls = %d, want 1", calls)
	}
	if gotCall.assignmentID != "a_orphan" || gotCall.status != "FAILED" || !strings.Contains(gotCall.errMsg, "interrupted by server restart") {
		t.Errorf("mission callback got (%q, %q, %q), want a_orphan/FAILED/restart reason", gotCall.assignmentID, gotCall.status, gotCall.errMsg)
	}

	// Journal: an assignment.failed entry attributed to the recovery.
	found := false
	for _, e := range rec.snapshot() {
		if e.Type == journal.EntryAssignmentFail && e.ActorID == "assignment_recovery" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no assignment.failed journal entry from recovery")
	}
}

func TestRecoverInterruptedRunning_PumpsQueuedRowIntoFreedSlot(t *testing.T) {
	// The full boot story at unit grain: an orphaned RUNNING row +
	// a QUEUED row stranded behind it on a budget-1 crew. Recovery must
	// not just fail the orphan — the finishAssignment pump must promote
	// the queued row so the queue drains without waiting for a sweeper.
	h, db, crewID, agentIDs, chatID := stuckSweeperRig(t)
	setCrewBudget(t, db, crewID, 1)

	past := time.Now().UTC().Add(-1 * time.Hour).Format("2006-01-02 15:04:05.000")
	seedRunningAt(t, db, "a_orphan_pump", chatID, agentIDs[0], agentIDs[0], past)
	seedQueuedAt(t, db, "a_stranded", chatID, agentIDs[0], agentIDs[1], past)

	if _, err := h.RecoverInterruptedRunning(context.Background(), time.Now()); err != nil {
		t.Fatalf("RecoverInterruptedRunning: %v", err)
	}
	h.WaitDispatches()

	var strandedStatus string
	if err := db.QueryRow(`SELECT status FROM assignments WHERE id = 'a_stranded'`).Scan(&strandedStatus); err != nil {
		t.Fatalf("load stranded: %v", err)
	}
	if strandedStatus == "QUEUED" {
		t.Errorf("stranded row still QUEUED after recovery — freed slot was not pumped")
	}
}

func TestRecoverInterruptedRunning_SkipsRowsStampedAfterCutoff(t *testing.T) {
	// A row that flipped RUNNING after process start has a live driver
	// (it was dispatched by THIS process) and must not be touched, even
	// though its status matches the scan.
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	fresh := time.Now().UTC().Format("2006-01-02 15:04:05.000")
	seedRunningAt(t, db, "a_live", chatID, agentIDs[0], agentIDs[0], fresh)

	bootCutoff := time.Now().Add(-10 * time.Minute) // "process started 10 min before the row was stamped"
	n, err := h.RecoverInterruptedRunning(context.Background(), bootCutoff)
	if err != nil {
		t.Fatalf("RecoverInterruptedRunning: %v", err)
	}
	if n != 0 {
		t.Errorf("recovered = %d, want 0 (row is post-boot)", n)
	}
	if got := assignmentStatus(t, db, "a_live"); got != "RUNNING" {
		t.Errorf("a_live status = %q, want RUNNING (untouched)", got)
	}
}

func TestRecoverInterruptedRunning_NoRunningRows_NoOp(t *testing.T) {
	h, _, _, _, _ := stuckSweeperRig(t)
	n, err := h.RecoverInterruptedRunning(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("RecoverInterruptedRunning: %v", err)
	}
	if n != 0 {
		t.Errorf("recovered = %d, want 0 on clean table", n)
	}
}

// ── SweepStuckRunning ─────────────────────────────────────────────────

func TestSweepStuckRunning_FailsStaleRows_BothTimestampFormats(t *testing.T) {
	// Two stale RUNNING rows — one stamped via running_at in the
	// dispatcher's space-separated shape, one via started_at in
	// runAssignment's RFC3339 'T'/'Z' shape — plus one fresh row. The
	// sweep must fail exactly the two stale rows; a lexicographic
	// comparison instead of julianday() would misjudge the RFC3339 one.
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	threeHoursAgo := time.Now().UTC().Add(-3 * time.Hour)
	seedRunningAt(t, db, "a_stale_space", chatID, agentIDs[0], agentIDs[0], threeHoursAgo.Format("2006-01-02 15:04:05.000"))
	seedRunningStartedRFC3339(t, db, "a_stale_rfc", chatID, agentIDs[0], agentIDs[1], threeHoursAgo)
	seedRunningAt(t, db, "a_fresh_run", chatID, agentIDs[0], agentIDs[2], time.Now().UTC().Format("2006-01-02 15:04:05.000"))

	swept, err := h.SweepStuckRunning(context.Background(), 2*time.Hour)
	if err != nil {
		t.Fatalf("SweepStuckRunning: %v", err)
	}
	if swept != 2 {
		t.Errorf("swept = %d, want 2", swept)
	}
	for _, id := range []string{"a_stale_space", "a_stale_rfc"} {
		if got := assignmentStatus(t, db, id); got != "FAILED" {
			t.Errorf("%s status = %q, want FAILED", id, got)
		}
	}
	if got := assignmentStatus(t, db, "a_fresh_run"); got != "RUNNING" {
		t.Errorf("a_fresh_run status = %q, want RUNNING (within staleness bound)", got)
	}
	var errMsg sql.NullString
	_ = db.QueryRow(`SELECT error_message FROM assignments WHERE id = 'a_stale_space'`).Scan(&errMsg)
	if !errMsg.Valid || !strings.Contains(errMsg.String, "stuck in RUNNING") {
		t.Errorf("swept row error_message = %q, want stuck-RUNNING reason", errMsg.String)
	}
}

func TestSweepStuckRunning_DefaultStaleAfter_AppliedWhenZero(t *testing.T) {
	// staleAfter <= 0 falls back to the 2h default: a row running for
	// 30 minutes must survive a sweep with staleAfter=0.
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	halfHourAgo := time.Now().UTC().Add(-30 * time.Minute).Format("2006-01-02 15:04:05.000")
	seedRunningAt(t, db, "a_halfhour", chatID, agentIDs[0], agentIDs[0], halfHourAgo)

	swept, err := h.SweepStuckRunning(context.Background(), 0)
	if err != nil {
		t.Fatalf("SweepStuckRunning: %v", err)
	}
	if swept != 0 {
		t.Errorf("swept = %d, want 0 (30min row, default bound is 2h)", swept)
	}
	if got := assignmentStatus(t, db, "a_halfhour"); got != "RUNNING" {
		t.Errorf("a_halfhour status = %q, want RUNNING", got)
	}
}

func TestSweepStuckRunning_FreesSlotForQueuedRow(t *testing.T) {
	// Belt-and-braces path end-to-end: stale RUNNING on a budget-1 crew
	// with a QUEUED row behind it. Sweeping must free the slot AND pump
	// the queued row (via the shared finishAssignment path).
	h, db, crewID, agentIDs, chatID := stuckSweeperRig(t)
	setCrewBudget(t, db, crewID, 1)
	past := time.Now().UTC().Add(-3 * time.Hour).Format("2006-01-02 15:04:05.000")
	seedRunningAt(t, db, "a_leak", chatID, agentIDs[0], agentIDs[0], past)
	seedQueuedAt(t, db, "a_behind_leak", chatID, agentIDs[0], agentIDs[1], past)

	swept, err := h.SweepStuckRunning(context.Background(), 2*time.Hour)
	if err != nil {
		t.Fatalf("SweepStuckRunning: %v", err)
	}
	if swept != 1 {
		t.Errorf("swept = %d, want 1", swept)
	}
	h.WaitDispatches()

	if got := assignmentStatus(t, db, "a_behind_leak"); got == "QUEUED" {
		t.Errorf("a_behind_leak still QUEUED after sweep — freed slot was not pumped")
	}
}

func TestSweepStuckRunning_RespectsConfiguredAgentTimeout(t *testing.T) {
	// An agent may legitimately be configured with timeout_seconds well
	// above the sweeper's floor. A RUNNING row older than the floor but
	// younger than its own configured timeout (+ grace) still has a live
	// driver by construction — sweeping it would double-book the crew
	// slot and set up a FAILED-vs-COMPLETED terminal collision when the
	// driver finishes.
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	if _, err := db.Exec(`UPDATE agents SET timeout_seconds = ? WHERE id = ?`,
		int((3 * time.Hour).Seconds()), agentIDs[0]); err != nil {
		t.Fatalf("raise agent timeout: %v", err)
	}
	// 2.5h old: past the 2h floor, but within the agent's 3h timeout.
	twoAndAHalfAgo := time.Now().UTC().Add(-150 * time.Minute).Format("2006-01-02 15:04:05.000")
	seedRunningAt(t, db, "a_long_run", chatID, agentIDs[0], agentIDs[0], twoAndAHalfAgo)

	swept, err := h.SweepStuckRunning(context.Background(), 2*time.Hour)
	if err != nil {
		t.Fatalf("SweepStuckRunning: %v", err)
	}
	if swept != 0 {
		t.Errorf("swept = %d, want 0 (row within its agent's 3h timeout)", swept)
	}
	if got := assignmentStatus(t, db, "a_long_run"); got != "RUNNING" {
		t.Errorf("a_long_run status = %q, want RUNNING (driver still live)", got)
	}

	// Once the row outlives timeout + grace, the sweeper must reclaim it.
	fourHoursAgo := time.Now().UTC().Add(-4 * time.Hour).Format("2006-01-02 15:04:05.000")
	if _, err := db.Exec(`UPDATE assignments SET running_at = ? WHERE id = 'a_long_run'`, fourHoursAgo); err != nil {
		t.Fatalf("backdate running_at: %v", err)
	}
	swept, err = h.SweepStuckRunning(context.Background(), 2*time.Hour)
	if err != nil {
		t.Fatalf("SweepStuckRunning (past timeout): %v", err)
	}
	if swept != 1 {
		t.Errorf("swept = %d, want 1 (row past configured timeout + grace)", swept)
	}
	if got := assignmentStatus(t, db, "a_long_run"); got != "FAILED" {
		t.Errorf("a_long_run status = %q, want FAILED", got)
	}
}

func TestFinishAssignment_LateDriver_CannotOverwriteSweptRow(t *testing.T) {
	// Terminal-transition guard: after the sweeper (or any recovery path)
	// failed a RUNNING row, a late driver's finishAssignment must lose the
	// CAS — the row stays FAILED and the driver's duplicate terminal
	// signals (mission callback, terminal run.* journal entry) are
	// suppressed.
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	rec := &recRecordingEmitter{}
	h.SetJournal(rec)
	cb := &recoveryMissionCallback{}
	h.SetMissionCallback(cb)

	past := time.Now().UTC().Add(-3 * time.Hour).Format("2006-01-02 15:04:05.000")
	seedRunningAt(t, db, "a_collide", chatID, agentIDs[0], agentIDs[0], past)

	swept, err := h.SweepStuckRunning(context.Background(), 2*time.Hour)
	if err != nil {
		t.Fatalf("SweepStuckRunning: %v", err)
	}
	if swept != 1 {
		t.Fatalf("swept = %d, want 1", swept)
	}
	h.WaitDispatches()

	cb.mu.Lock()
	callsAfterSweep := len(cb.calls)
	cb.mu.Unlock()
	entriesAfterSweep := len(rec.snapshot())

	// The still-live driver finishes late with a COMPLETED result.
	h.finishAssignment(context.Background(), "a_collide", "run_late", chatID, "agent-sweep-a", "test-workspace-id", "late result", "")

	var status string
	var result, errMsg sql.NullString
	if err := db.QueryRow(`SELECT status, result_summary, error_message FROM assignments WHERE id = 'a_collide'`).
		Scan(&status, &result, &errMsg); err != nil {
		t.Fatalf("load a_collide: %v", err)
	}
	if status != "FAILED" {
		t.Errorf("status = %q, want FAILED (late driver must not overwrite the swept row)", status)
	}
	if result.Valid && result.String != "" {
		t.Errorf("result_summary = %q, want empty (late result must be discarded)", result.String)
	}
	if !errMsg.Valid || !strings.Contains(errMsg.String, "stuck in RUNNING") {
		t.Errorf("error_message = %q, want the sweeper's reason preserved", errMsg.String)
	}

	// No duplicate terminal signals from the losing driver.
	cb.mu.Lock()
	callsAfterLate := len(cb.calls)
	cb.mu.Unlock()
	if callsAfterLate != callsAfterSweep {
		t.Errorf("mission callback calls = %d after late finish, want %d (no duplicate terminal callback)", callsAfterLate, callsAfterSweep)
	}
	for _, e := range rec.snapshot()[entriesAfterSweep:] {
		if e.Type == journal.EntryRunCompleted {
			t.Errorf("late driver emitted %s journal entry despite losing the terminal CAS", e.Type)
		}
	}
}

// ── StartStuckRunningSweeper ──────────────────────────────────────────

func TestStartStuckRunningSweeper_TicksAndExits(t *testing.T) {
	// Short interval; the stale RUNNING row must transition within the
	// test window, and the goroutine must exit on ctx cancel.
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	past := time.Now().UTC().Add(-3 * time.Hour).Format("2006-01-02 15:04:05.000")
	seedRunningAt(t, db, "a_tick_run", chatID, agentIDs[0], agentIDs[0], past)

	ctx, cancel := context.WithCancel(context.Background())
	h.StartStuckRunningSweeper(ctx, 50*time.Millisecond, 1*time.Millisecond)

	deadline := time.Now().Add(1 * time.Second)
	swept := false
	for !swept && time.Now().Before(deadline) {
		if assignmentStatus(t, db, "a_tick_run") != "RUNNING" {
			swept = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	if !swept {
		t.Errorf("stuck-RUNNING sweeper did not fire within 1s window")
	}
	// Let the goroutine observe the cancel before the fixture DB closes.
	time.Sleep(100 * time.Millisecond)
}

func TestStartStuckRunningSweeper_DefaultInterval_AppliedWhenZero(t *testing.T) {
	// interval <= 0 must fall back to the default without panicking on
	// a zero ticker.
	h, _, _, _, _ := stuckSweeperRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.StartStuckRunningSweeper(ctx, 0, 0)
	time.Sleep(50 * time.Millisecond)
}
