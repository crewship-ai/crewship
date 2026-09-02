package api

// #2269 follow-up: three reviewers independently found that the F51
// exclusivity work stopped short of actually draining the queue.
//
//   - TestQueue_PumpCrewQueue_SkipsAgentWithRunningAssignment covers defect
//     4 (pumpCrewQueue is agent-blind): the picker used to claim the oldest
//     QUEUED row by crew budget alone, so a second row for an agent that
//     ALREADY has a RUNNING assignment would be claimed and immediately
//     bounce off runAssignment's AgentRunLock check — burning the crew's
//     one free slot on a doomed dispatch while a DIFFERENT, idle agent's
//     row sat untried.
//   - TestLockLoss_PumpsAfterRequeue_SoOtherAgentsQueuedRowDrains covers
//     defect 1 (lock-loss branch does not pump): runAssignment's own
//     TryStart check used to `return` after a successful requeue without
//     ever calling pumpAndDispatch, so a DIFFERENT crew member's queued row
//     — which the requeue just freed a slot for — sat QUEUED until an
//     unrelated completion or the stuck sweeper got to it.
//   - TestLockLoss_DoesNotPumpWhenOnlyBusyAgentIsQueued proves the fix
//     above did not trade that bug for a worse one: requeueLockLossAndMaybeDrain
//     only pumps when SOME OTHER agent has queued work. If the busy agent's
//     own row is the ONLY thing queued, pumping would just reclaim it,
//     lose the lock again, requeue again, and pump again — a zero-delay
//     retry storm for as long as the busy period lasts.

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
)

func TestQueue_PumpCrewQueue_SkipsAgentWithRunningAssignment(t *testing.T) {
	t.Parallel()
	db, crewID, agentIDs, chatID := queueTestRig(t, 2)
	setCrewBudget(t, db, crewID, 2) // headroom: budget alone would happily claim a second row for X

	// X already has a RUNNING assignment.
	insertAssignment(t, db, "a_running", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")

	// A second, QUEUED row also targets X — this is the doomed claim the
	// old agent-blind picker would take (it's the oldest QUEUED row and
	// budget has room).
	insertAssignment(t, db, "a_x2", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "QUEUED")
	// Y is idle and has a QUEUED row too, stamped NEWER than a_x2's so a
	// naive FIFO-only picker would still prefer a_x2.
	insertAssignment(t, db, "a_y", "test-workspace-id", chatID, agentIDs[1], agentIDs[1], "QUEUED")
	if _, err := db.Exec(`UPDATE assignments SET queued_at = '2026-05-17T10:00:00Z' WHERE id = 'a_x2'`); err != nil {
		t.Fatalf("stamp a_x2: %v", err)
	}
	if _, err := db.Exec(`UPDATE assignments SET queued_at = '2026-05-17T10:00:01Z' WHERE id = 'a_y'`); err != nil {
		t.Fatalf("stamp a_y: %v", err)
	}

	claimed, err := pumpCrewQueue(context.Background(), db, crewID, 2)
	if err != nil {
		t.Fatalf("pumpCrewQueue: %v", err)
	}
	if len(claimed) != 1 || claimed[0] != "a_y" {
		t.Fatalf("claimed = %v, want exactly [a_y] — the picker must skip a_x2 (X already has a RUNNING "+
			"row) and claim Y's row instead, not burn the free slot on a row that will only bounce", claimed)
	}
	if got := assignmentStatus(t, db, "a_x2"); got != "QUEUED" {
		t.Errorf("a_x2 status = %q, want QUEUED (skipped, not claimed)", got)
	}
	if got := assignmentStatus(t, db, "a_y"); got != "RUNNING" {
		t.Errorf("a_y status = %q, want RUNNING (claimed)", got)
	}
}

// TestLockLoss_PumpsAfterRequeue_SoOtherAgentsQueuedRowDrains reproduces
// defect 1's scenario exactly: budget=1, agent X busy (AgentRunLock held,
// simulating a live chat turn), queue = [B->X oldest, C->Y idle]. A
// completion pumps; the pump claims B (oldest), B bounces off the held
// lock and gets requeued — and, with the fix, that requeue itself triggers
// a further pump that claims C, because requeueing B freed the crew's one
// slot and Y is idle.
func TestLockLoss_PumpsAfterRequeue_SoOtherAgentsQueuedRowDrains(t *testing.T) {
	t.Parallel()
	h, db, crewID, agentIDs, chatID := dispatchPumpRig(t)
	setCrewBudget(t, db, crewID, 1)
	lock := chatbridge.NewAgentRunLock()
	h.SetAgentRunLock(lock)

	const b = "a_ll_b"
	const c = "a_ll_c"
	seedAssignmentRow(t, db, b, "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "QUEUED")
	seedAssignmentRow(t, db, c, "test-workspace-id", chatID, agentIDs[1], agentIDs[1], "QUEUED")
	if _, err := db.Exec(`UPDATE assignments SET queued_at = '2026-05-17T10:00:00Z' WHERE id = ?`, b); err != nil {
		t.Fatalf("stamp b: %v", err)
	}
	if _, err := db.Exec(`UPDATE assignments SET queued_at = '2026-05-17T10:00:01Z' WHERE id = ?`, c); err != nil {
		t.Fatalf("stamp c: %v", err)
	}

	// Agent X is busy on a live chat turn for the whole test.
	if !lock.TryStart(agentIDs[0]) {
		t.Fatal("setup: lock should be free")
	}
	defer lock.End(agentIDs[0])

	// The natural drain trigger: some assignment elsewhere completes and
	// calls pumpAndDispatch(crewID). Budget=1 means it claims exactly B
	// (the oldest QUEUED row) first.
	n, err := h.pumpAndDispatch(context.Background(), crewID)
	if err != nil {
		t.Fatalf("pumpAndDispatch: %v", err)
	}
	if n != 1 {
		t.Fatalf("pumpAndDispatch claimed %d, want 1", n)
	}

	// C must be drained WITHOUT any further external trigger — proving the
	// lock-loss branch pumped on its own after requeuing B.
	cFinal := pollFinalStatus(t, db, c, map[string]bool{"PENDING": true, "QUEUED": true, "RUNNING": true}, 2*time.Second)
	if cFinal != "FAILED" {
		t.Fatalf("c final status = %q, want FAILED (orch=nil path — proves it reached the "+
			"orchestrator-availability check, i.e. it was actually dispatched, not left QUEUED)", cFinal)
	}

	// B stays behind X's still-held lock: C's own completion pump (inside
	// finishAssignment) reclaims B again (it's the only QUEUED row left),
	// bounces off the still-held lock, and requeues it — asynchronously, in
	// a spawned goroutine, so poll rather than assert immediately.
	bFinal := pollFinalStatus(t, db, b, map[string]bool{"PENDING": true, "RUNNING": true}, 2*time.Second)
	if bFinal != "QUEUED" {
		t.Errorf("b final status = %q, want QUEUED (agent still busy)", bFinal)
	}
}

// TestLockLoss_DoesNotPumpWhenOnlyBusyAgentIsQueued is the livelock guard:
// with only ONE queued row, targeting the SAME agent that just lost the
// lock, requeueLockLossAndMaybeDrain must NOT trigger a further pump — it
// would just reclaim the same row, lose the lock again, and requeue again,
// forever, for as long as the agent stays busy. This test can't directly
// observe "no infinite loop" other than by finishing promptly; it asserts
// the row settles into QUEUED and stays there across a short observation
// window, rather than cycling through RUNNING repeatedly.
func TestLockLoss_DoesNotPumpWhenOnlyBusyAgentIsQueued(t *testing.T) {
	t.Parallel()
	h, db, crewID, agentIDs, chatID := dispatchPumpRig(t)
	setCrewBudget(t, db, crewID, 1)
	lock := chatbridge.NewAgentRunLock()
	h.SetAgentRunLock(lock)

	const b = "a_solo_b"
	seedAssignmentRow(t, db, b, "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "QUEUED")
	if _, err := db.Exec(`UPDATE assignments SET queued_at = datetime('now') WHERE id = ?`, b); err != nil {
		t.Fatalf("stamp b: %v", err)
	}

	if !lock.TryStart(agentIDs[0]) {
		t.Fatal("setup: lock should be free")
	}
	defer lock.End(agentIDs[0])

	n, err := h.pumpAndDispatch(context.Background(), crewID)
	if err != nil {
		t.Fatalf("pumpAndDispatch: %v", err)
	}
	if n != 1 {
		t.Fatalf("pumpAndDispatch claimed %d, want 1", n)
	}

	// Give any (wrongly) self-triggered cascade a moment to run.
	time.Sleep(200 * time.Millisecond)

	if got := assignmentStatus(t, db, b); got != "QUEUED" {
		t.Errorf("b status = %q, want QUEUED (no other agent has queued work, so no re-pump should have fired)", got)
	}
}
