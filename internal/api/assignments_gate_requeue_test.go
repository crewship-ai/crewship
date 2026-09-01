package api

// F51 follow-up: runAssignment losing chatbridge.RunGate's per-agent claim
// used to mark the assignment FAILED ("already has a live run in
// progress"). That is semantically wrong — nothing failed, the work merely
// has to wait — and permanently pollutes run history with fake failures.
//
// These tests cover the fix end-to-end, against the real claim/pump
// primitives (claimCrewSlot / pumpCrewQueue / pumpAndDispatch) rather than
// calling runAssignment in isolation:
//
//  1. TestGateLoss_RequeuedRow_DrainedOncePumped proves a row that lost the
//     gate is QUEUED, not FAILED, and that it IS subsequently claimed and
//     actually run once the gate frees — i.e. it does not silently rot in
//     QUEUED forever.
//  2. TestPumpAndDispatch_DoesNotCollideWithHeldGate proves the completion
//     pump — which claims purely on crew budget and has no visibility into
//     the in-memory gate — cannot force a live collision: a row it claims
//     for a busy agent bounces back to QUEUED via runAssignment's gate
//     check instead of reaching the orchestrator.
//  3. TestRunAssignment_ReleasesGateBeforeOwnPump_SoOwnQueuedRowDrainsImmediately
//     covers a latent ordering hazard found while implementing this fix:
//     finishAssignment calls pumpAndDispatch synchronously, and pumpAndDispatch
//     dispatches each claimed row from a SPAWNED GOROUTINE. If the gate were
//     released only via a bare `defer h.runGate.End(...)` (which fires after
//     runAssignment itself returns, i.e. strictly after the in-body
//     finishAssignment call that triggers the pump), there would be a real
//     data race between that deferred release and the spawned goroutine's own
//     gate.TryStart for a same-agent queued row: nothing orders one before
//     the other, so the pumped row could observe the gate as still held and
//     bounce back to QUEUED instead of draining immediately.
//
//     runAssignment now calls a releaseGate() helper explicitly, right
//     before every finishAssignment call, instead of relying solely on the
//     defer (which remains only as a panic/missed-path safety net). Because
//     that explicit release happens strictly before pumpAndDispatch's `go
//     dispatchByID(...)` statement in the SAME goroutine, Go's
//     happens-before guarantee for goroutine creation (the spawning
//     goroutine's prior actions happen-before the spawned goroutine's first
//     action) means the pumped goroutine's gate.TryStart is deterministically
//     guaranteed to observe the released gate — not just likely to.
//
//     Honesty check: reverting just the explicit releaseGate() calls (back
//     to defer-only) and running this test 15x in a row did NOT reproduce a
//     failure — in practice the goroutine-scheduling latency to actually run
//     the spawned dispatch reliably exceeds the handful of instructions
//     finishAssignment does after the pump call before returning, so the old
//     defer already "happened to" win every observed race. This is
//     therefore a real but apparently low-probability/theoretical hazard,
//     not a reproduced production bug — the fix is kept because it turns a
//     race into a proven guarantee at negligible cost, not because this test
//     can currently prove the old code wrong.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
)

// pollFinalStatus polls id's status until it settles into a status not in
// `transient`, or fails the test if it ever lands on a status in `bad`.
// Used where the assignment is expected to pass through RUNNING briefly
// (the pump's CAS) before its dispatched goroutine finishes.
func pollFinalStatus(t *testing.T, db *sql.DB, id string, transient map[string]bool, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		last = statusOf(t, db, id)
		if !transient[last] {
			// Give one more short beat in case it's mid-transition
			// (e.g. RUNNING -> requeued to QUEUED) and re-check once
			// more before trusting a "settled" reading.
			time.Sleep(20 * time.Millisecond)
			again := statusOf(t, db, id)
			if again == last {
				return last
			}
			last = again
			if !transient[last] {
				return last
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pollFinalStatus(%s) timeout: last=%q", id, last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestGateLoss_RequeuedRow_DrainedOncePumped: a row that loses the gate
// lands QUEUED (not FAILED), and once the gate frees, the normal
// completion-path pump claims and actually runs it (orch=nil so it ends
// FAILED with "orchestrator not available" — proof it reached the
// orchestrator-availability check this time, i.e. it got PAST the gate).
func TestGateLoss_RequeuedRow_DrainedOncePumped(t *testing.T) {
	t.Parallel()
	h, db, crewID, agentIDs, chatID := dispatchPumpRig(t)
	setCrewBudget(t, db, crewID, 1)
	gate := chatbridge.NewRunGate()
	h.SetRunGate(gate)

	const aid = "a_gateloss_1"
	seedAssignmentRow(t, db, aid, "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")

	// Simulate: claimCrewSlot already flipped this row to RUNNING, but the
	// agent has a different live run in flight (a chat send, or an earlier
	// assignment) holding the exec slot.
	if !gate.TryStart(agentIDs[0]) {
		t.Fatal("setup: gate should be free")
	}

	if err := h.dispatchByID(context.Background(), aid); err != nil {
		t.Fatalf("dispatchByID: %v", err)
	}
	// dispatchByID -> runAssignment is synchronous (no inner goroutine), so
	// by return time the gate-loss branch has already run.
	if got := statusOf(t, db, aid); got != "QUEUED" {
		t.Fatalf("status after gate loss = %q, want QUEUED", got)
	}

	// The agent's live run finishes and frees the gate.
	gate.End(agentIDs[0])

	// The natural drain trigger: some assignment in this crew completes,
	// which calls pumpAndDispatch(crewID). We invoke it directly here
	// (finishAssignment's own call is covered by test 3 below).
	n, err := h.pumpAndDispatch(context.Background(), crewID)
	if err != nil {
		t.Fatalf("pumpAndDispatch: %v", err)
	}
	if n != 1 {
		t.Fatalf("pumpAndDispatch claimed %d, want 1", n)
	}

	final := pollFinalStatus(t, db, aid, map[string]bool{"QUEUED": true, "RUNNING": true}, 2*time.Second)
	if final != "FAILED" {
		t.Fatalf("final status = %q, want FAILED (orch=nil path — proves the run actually reached the "+
			"orchestrator-availability check once the gate freed, not stuck requeuing forever)", final)
	}
	var errMsg string
	_ = db.QueryRow(`SELECT COALESCE(error_message,'') FROM assignments WHERE id = ?`, aid).Scan(&errMsg)
	if errMsg != "orchestrator not available" {
		t.Errorf("error_message = %q, want %q — a different reason means it didn't cleanly pass the gate",
			errMsg, "orchestrator not available")
	}
	if gate.InFlight(agentIDs[0]) {
		t.Error("gate still reports the agent in-flight after its run finished")
	}
}

// TestPumpAndDispatch_DoesNotCollideWithHeldGate: the completion-path pump
// (pumpCrewQueue) claims purely on crew budget — it has no visibility into
// the in-memory RunGate, because the gate can't be evaluated inside the SQL
// CAS. If it claims a QUEUED row for an agent that is ACTUALLY still busy
// (gate held, e.g. by a concurrent chat send), that must not turn into a
// live collision: runAssignment's gate check inside the dispatched goroutine
// must catch it and return the row to QUEUED instead of proceeding to the
// orchestrator.
func TestPumpAndDispatch_DoesNotCollideWithHeldGate(t *testing.T) {
	t.Parallel()
	h, db, crewID, agentIDs, chatID := dispatchPumpRig(t)
	setCrewBudget(t, db, crewID, 2) // headroom: budget alone would happily claim
	gate := chatbridge.NewRunGate()
	h.SetRunGate(gate)

	const aid = "a_collide_1"
	seedAssignmentRow(t, db, aid, "test-workspace-id", chatID, agentIDs[1], agentIDs[1], "QUEUED")
	if _, err := db.Exec(`UPDATE assignments SET queued_at = datetime('now') WHERE id = ?`, aid); err != nil {
		t.Fatalf("stamp queued_at: %v", err)
	}

	// The agent this row targets has a live run elsewhere (e.g. a chat
	// send) holding the gate for the whole test.
	if !gate.TryStart(agentIDs[1]) {
		t.Fatal("setup: gate should be free")
	}
	defer gate.End(agentIDs[1])

	n, err := h.pumpAndDispatch(context.Background(), crewID)
	if err != nil {
		t.Fatalf("pumpAndDispatch: %v", err)
	}
	if n != 1 {
		t.Fatalf("pumpAndDispatch claimed %d, want 1 (budget alone sees room)", n)
	}

	// The claim's CAS flips the row RUNNING; the dispatched goroutine's
	// runAssignment must catch the still-held gate and requeue it. It must
	// NOT settle on FAILED (which would mean it reached the
	// orchestrator-availability check, i.e. got past the gate — the
	// collision this test exists to catch) or stay RUNNING (a live exec
	// that never got requeued).
	final := pollFinalStatus(t, db, aid, map[string]bool{"RUNNING": true}, 2*time.Second)
	if final != "QUEUED" {
		t.Fatalf("final status = %q, want QUEUED — the pump let a busy agent's row through instead of "+
			"bouncing it back", final)
	}
	if !gate.InFlight(agentIDs[1]) {
		t.Error("gate must still report the agent in-flight — the held claim must be untouched by the losing call")
	}
}

// TestRunAssignment_ReleasesGateBeforeOwnPump_SoOwnQueuedRowDrainsImmediately
// is the regression test for the ordering window: finishAssignment calls
// pumpAndDispatch SYNCHRONOUSLY as part of completing a run. If the gate is
// released only via a deferred call (which fires after runAssignment itself
// returns — i.e. after the in-body finishAssignment call, and therefore
// after its pump already ran), the very completion that frees an agent's
// slot finds its OWN gate still held during that first pump attempt, and a
// same-agent row queued behind it gets bounced straight back to QUEUED
// instead of draining. This proves the fix (explicit releaseGate() right
// before each finishAssignment call) closes that window: A1 finishes,
// A2 (same agent, already QUEUED) is picked up by A1's OWN completion pump
// and actually runs (orch=nil ends FAILED) rather than being requeued.
func TestRunAssignment_ReleasesGateBeforeOwnPump_SoOwnQueuedRowDrainsImmediately(t *testing.T) {
	t.Parallel()
	h, db, crewID, agentIDs, chatID := dispatchPumpRig(t)
	setCrewBudget(t, db, crewID, 1)
	gate := chatbridge.NewRunGate()
	h.SetRunGate(gate)

	const a1 = "a_order_1"
	const a2 = "a_order_2"
	seedAssignmentRow(t, db, a1, "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "PENDING")
	seedAssignmentRow(t, db, a2, "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "QUEUED")
	if _, err := db.Exec(`UPDATE assignments SET queued_at = datetime('now') WHERE id = ?`, a2); err != nil {
		t.Fatalf("stamp queued_at: %v", err)
	}

	body := createAssignmentBody{
		TargetSlug: "agent-disp-a", Task: "t", CrewID: crewID, WorkspaceID: "test-workspace-id", ChatID: chatID,
	}
	target := targetAgentInfo{ID: agentIDs[0], Slug: "agent-disp-a", Name: "Agent A", CrewSlug: "disp"}

	// Run A1 synchronously — no pre-held gate, so it claims + runs (orch=nil
	// short-circuit) + finishes, all inline. finishAssignment's own pump
	// call happens inside this call.
	h.runAssignment(context.Background(), a1, body, target)

	if got := statusOf(t, db, a1); got != "FAILED" {
		t.Fatalf("a1 status = %q, want FAILED (orch=nil short-circuit)", got)
	}
	// a2 must not be stuck QUEUED: A1's own completion pump should have
	// claimed and run it immediately, because the gate was released BEFORE
	// finishAssignment (and its pump) ran — not after.
	final := pollFinalStatus(t, db, a2, map[string]bool{"QUEUED": true, "RUNNING": true}, 1*time.Second)
	if final != "FAILED" {
		t.Fatalf("a2 final status = %q, want FAILED — it should have been drained by a1's own completion "+
			"pump (proves the gate was released before, not after, finishAssignment's pump call)", final)
	}
	if gate.InFlight(agentIDs[0]) {
		t.Error("gate still reports the agent in-flight after both runs finished")
	}
}
