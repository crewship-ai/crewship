package api

// F51: chatbridge.tryMarkRunStart enforced "at most one live RunAgent exec
// per chat" but had exactly one caller (chatbridge.Bridge.HandleChatMessage).
// runAssignment — the door /assign, DispatchMention (@mention), and the
// mission engine's DispatchAssignment all funnel through — never consulted
// it, so two concurrent assignment runs (or an assignment racing a chat
// send) for the SAME agent could both reach orchestrator.RunAgent and race
// the identical tmux session name + /tmp scratch files
// (orchestrator.TmuxSessionName is keyed by agent slug alone). These tests
// prove runAssignment now shares chatbridge.RunGate — keyed by target
// AgentID — with the chat-send path, and that a losing call is refused (not
// silently dropped, not silently run anyway) with a recorded reason.
//
// These tests fail to compile against the pre-fix tree: h.SetRunGate and
// chatbridge.RunGate did not exist, and runAssignment had no exclusivity
// check to prove — see the PR description for the captured failure with
// the guard clause in runAssignment temporarily removed instead (same
// scenario, "agent busy" never fires and the row fails for a DIFFERENT,
// orchestrator-unavailable reason instead).

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
)

// TestRunAssignment_ConcurrentSameAgent_SecondIsRefused simulates the real
// production race deterministically: goroutine A's TryStart on the shared
// per-agent gate always wins some microseconds before goroutine B's, so by
// the time B's runAssignment call evaluates the gate, A's claim is already
// held. Pre-claiming the gate directly reproduces exactly that ordering
// without depending on goroutine-scheduling luck (which would make the test
// flaky in either direction). h.orch is left nil, so if the guard did NOT
// fire first, the row would fail with "orchestrator not available" instead
// — a different, distinguishable reason — proving the busy check runs
// before any orchestrator work is attempted, not after it fails for some
// unrelated cause.
func TestRunAssignment_ConcurrentSameAgent_SecondIsRefused(t *testing.T) {
	t.Parallel()
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	gate := chatbridge.NewRunGate()
	h.SetRunGate(gate)

	insertAssignment(t, h.db, "asg-gate-1", wsID, chatID, leadID, workerID, "PENDING")
	body := createAssignmentBody{
		TargetSlug: "asg-worker", Task: "t", CrewID: crewID, WorkspaceID: wsID, ChatID: chatID,
	}
	target := targetAgentInfo{ID: workerID, Slug: "asg-worker", Name: "Worker", CrewSlug: "asg"}

	// Simulate an already-live run for this agent (a chat send, or an
	// earlier assignment, currently holding the same slot runAssignment is
	// about to try to claim).
	if !gate.TryStart(workerID) {
		t.Fatal("setup: gate should be free before the test claims it")
	}
	t.Cleanup(func() { gate.End(workerID) })

	h.runAssignment(context.Background(), "asg-gate-1", body, target)

	var status, errMsg string
	if err := h.db.QueryRow(
		`SELECT status, COALESCE(error_message,'') FROM assignments WHERE id = 'asg-gate-1'`,
	).Scan(&status, &errMsg); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "FAILED" {
		t.Fatalf("status = %q, want FAILED (assignment must be refused, not silently dropped or run anyway)", status)
	}
	if !strings.Contains(errMsg, "already has a live run") {
		t.Errorf("error_message = %q, want the agent-busy reason (got a different failure — the exclusivity"+
			" check did not run first)", errMsg)
	}
	if gate.InFlight("asg-run-should-not-have-claimed") {
		t.Error("runAssignment must not have claimed any OTHER key")
	}
}

// TestRunAssignment_GateFreedAfterRun_NextCallProceeds is the control: once
// the holder releases its claim, a subsequent runAssignment for the same
// agent must proceed normally (reach the orchestrator-availability check)
// rather than being permanently wedged "busy". Guards against an
// implementation that claims but never releases.
func TestRunAssignment_GateFreedAfterRun_NextCallProceeds(t *testing.T) {
	t.Parallel()
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	gate := chatbridge.NewRunGate()
	h.SetRunGate(gate)

	insertAssignment(t, h.db, "asg-gate-2", wsID, chatID, leadID, workerID, "PENDING")
	body := createAssignmentBody{
		TargetSlug: "asg-worker", Task: "t", CrewID: crewID, WorkspaceID: wsID, ChatID: chatID,
	}
	target := targetAgentInfo{ID: workerID, Slug: "asg-worker", Name: "Worker", CrewSlug: "asg"}

	// No pre-held claim this time — the gate is free.
	h.runAssignment(context.Background(), "asg-gate-2", body, target)

	var status, errMsg string
	if err := h.db.QueryRow(
		`SELECT status, COALESCE(error_message,'') FROM assignments WHERE id = 'asg-gate-2'`,
	).Scan(&status, &errMsg); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "FAILED" || errMsg != "orchestrator not available" {
		t.Errorf("status=%q err=%q, want the normal (nil-orchestrator) failure — the gate must release "+
			"after the run finishes, not stay claimed", status, errMsg)
	}
	if gate.InFlight(workerID) {
		t.Error("gate still reports the agent in-flight after runAssignment returned")
	}
}

// TestRunAssignment_TwoRealGoroutines_MutualExclusion launches two genuine
// concurrent runAssignment calls for the SAME agent (two different
// assignment rows, as two independent dispatch doors would produce) and
// asserts the outcomes are mutually exclusive: exactly one reaches the
// orchestrator-availability failure and exactly one is refused as busy —
// never both succeeding past the gate, never both refused. Run under
// -race to catch any unsynchronized access the wiring might introduce.
func TestRunAssignment_TwoRealGoroutines_MutualExclusion(t *testing.T) {
	t.Parallel()
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	gate := chatbridge.NewRunGate()
	h.SetRunGate(gate)

	insertAssignment(t, h.db, "asg-gate-3a", wsID, chatID, leadID, workerID, "PENDING")
	insertAssignment(t, h.db, "asg-gate-3b", wsID, chatID, leadID, workerID, "PENDING")
	target := targetAgentInfo{ID: workerID, Slug: "asg-worker", Name: "Worker", CrewSlug: "asg"}
	mkBody := func() createAssignmentBody {
		return createAssignmentBody{TargetSlug: "asg-worker", Task: "t", CrewID: crewID, WorkspaceID: wsID, ChatID: chatID}
	}

	// Hold the gate for the duration of both calls so their entry into
	// runAssignment is forced to overlap the same live claim, deterministically
	// reproducing the collision window instead of hoping two fast goroutines
	// happen to race within it.
	if !gate.TryStart(workerID) {
		t.Fatal("setup: gate should be free")
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); h.runAssignment(context.Background(), "asg-gate-3a", mkBody(), target) }()
	go func() { defer wg.Done(); h.runAssignment(context.Background(), "asg-gate-3b", mkBody(), target) }()
	wg.Wait()
	gate.End(workerID)

	for _, id := range []string{"asg-gate-3a", "asg-gate-3b"} {
		var status, errMsg string
		if err := h.db.QueryRow(
			`SELECT status, COALESCE(error_message,'') FROM assignments WHERE id = ?`, id,
		).Scan(&status, &errMsg); err != nil {
			t.Fatalf("query %s: %v", id, err)
		}
		if status != "FAILED" || !strings.Contains(errMsg, "already has a live run") {
			t.Errorf("%s: status=%q err=%q, want FAILED with the busy reason — both calls raced a held claim "+
				"and neither should have reached the orchestrator", id, status, errMsg)
		}
	}
	if gate.InFlight(workerID) {
		t.Error("gate must be free once every holder (test + both goroutines) released")
	}
}
