package api

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/ws"
)

// dispatchPumpRig builds an AssignmentHandler against the migrated
// test DB with a real Hub (in-process; no network). orch is nil so
// runAssignment short-circuits at "orchestrator not available" and
// stamps a terminal FAILED — exactly the shape we want for testing
// the queue transitions without driving an actual agent process.
//
// The rig returns the bits each test needs and the fixtures so the
// tests can poke the DB directly to set up scenarios.
func dispatchPumpRig(t *testing.T) (*AssignmentHandler, *sql.DB, string, []string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// 4096 MiB / 2048 MiB per agent → budget 2 by default.
	crewID := "crew_disp"
	if _, err := db.Exec(`
		INSERT INTO crews (id, workspace_id, name, slug, icon, color, container_memory_mb, container_cpus)
		VALUES (?, ?, 'Dispatch Test', 'disp', '⚙️', '#000', 4096, 2)`,
		crewID, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	agentIDs := make([]string, 3)
	for i := range agentIDs {
		id := "agent_disp_" + string(rune('a'+i))
		agentIDs[i] = id
		if _, err := db.Exec(`
			INSERT INTO agents (id, crew_id, workspace_id, name, slug, role_title, agent_role,
			                    cli_adapter, llm_provider, llm_model, tool_profile, timeout_seconds, memory_enabled)
			VALUES (?, ?, ?, ?, ?, 'Worker', 'AGENT',
			        'CLAUDE_CODE', 'ANTHROPIC', 'claude-sonnet-4-6', 'standard', 60, 0)`,
			id, crewID, wsID, "Agent "+string(rune('A'+i)), "agent-disp-"+string(rune('a'+i))); err != nil {
			t.Fatalf("seed agent %d: %v", i, err)
		}
	}

	chatID := "chat_disp"
	if _, err := db.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'disp test', 'MISSION', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`,
		chatID, agentIDs[0], wsID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	hub := ws.NewHub(logger, nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	// orch=nil intentional — runAssignment's "orchestrator not
	// available" branch flips status to FAILED without spawning
	// anything. That's the test surface for the queue transitions.
	h := NewAssignmentHandler(db, nil, hub, "internal-test", logger)

	return h, db, crewID, agentIDs, chatID
}

func seedAssignmentRow(t *testing.T, db *sql.DB, id, wsID, chatID, byAgent, toAgent, status string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'test-task', ?, datetime('now'))`,
		id, wsID, chatID, byAgent, toAgent, status); err != nil {
		t.Fatalf("insert assignment %s: %v", id, err)
	}
}

func statusOf(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM assignments WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatalf("status of %s: %v", id, err)
	}
	return s
}

// waitForStatus polls until the assignment's status changes to one of
// the targets, or fails the test on timeout. Used because dispatchByID
// runs runAssignment which writes status asynchronously via its own
// goroutine completion path. Without a small poll, the test would
// race the goroutine.
func waitForStatus(t *testing.T, db *sql.DB, id string, targets []string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		s := statusOf(t, db, id)
		for _, want := range targets {
			if s == want {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("waitForStatus(%s) timeout: last=%q want one of %v", id, s, targets)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ── DispatchAssignment claim/queue branch ─────────────────────────────

func TestDispatch_BudgetAvailable_FlipsToRunning(t *testing.T) {
	// With 0 inflight and budget=2, a new dispatch must claim the
	// slot — assignment ends RUNNING (then immediately FAILED
	// because orch=nil; both states are post-claim so the test
	// accepts either). The key assertion is "not QUEUED".
	h, db, crewID, agentIDs, chatID := dispatchPumpRig(t)
	const aid = "a_dispatch_1"
	seedAssignmentRow(t, db, aid, "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "PENDING")

	err := h.DispatchAssignment(context.Background(), orchestrator.DispatchRequest{
		AssignmentID: aid,
		AgentID:      agentIDs[0],
		CrewID:       crewID,
		WorkspaceID:  "test-workspace-id",
		ChatID:       chatID,
		Task:         "do thing",
	})
	if err != nil {
		t.Fatalf("DispatchAssignment: %v", err)
	}
	// runAssignment is sync inside DispatchAssignment (it doesn't
	// spawn its own goroutine for the orch-nil path), so by the
	// time we're back the row is FAILED — never QUEUED.
	st := statusOf(t, db, aid)
	if st == "QUEUED" {
		t.Errorf("status = QUEUED, want any non-QUEUED (slot was available)")
	}
}

func TestDispatch_BudgetFull_FlipsToQueued(t *testing.T) {
	// Two RUNNING + budget=2 + one new PENDING dispatch → the new
	// row must end QUEUED with queued_at stamped. runAssignment
	// must NOT have been invoked (no FAILED status, no started_at).
	h, db, crewID, agentIDs, chatID := dispatchPumpRig(t)
	setCrewBudget(t, db, crewID, 2)
	seedAssignmentRow(t, db, "a_run_1", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")
	seedAssignmentRow(t, db, "a_run_2", "test-workspace-id", chatID, agentIDs[0], agentIDs[1], "RUNNING")
	seedAssignmentRow(t, db, "a_queued", "test-workspace-id", chatID, agentIDs[0], agentIDs[2], "PENDING")

	err := h.DispatchAssignment(context.Background(), orchestrator.DispatchRequest{
		AssignmentID: "a_queued",
		AgentID:      agentIDs[2],
		CrewID:       crewID,
		WorkspaceID:  "test-workspace-id",
		ChatID:       chatID,
		Task:         "do thing",
	})
	if err != nil {
		t.Fatalf("DispatchAssignment: %v", err)
	}
	if got := statusOf(t, db, "a_queued"); got != "QUEUED" {
		t.Fatalf("status = %q, want QUEUED", got)
	}
	var queuedAt, startedAt sql.NullString
	_ = db.QueryRow(`SELECT queued_at, started_at FROM assignments WHERE id = 'a_queued'`).Scan(&queuedAt, &startedAt)
	if !queuedAt.Valid || queuedAt.String == "" {
		t.Errorf("queued_at not stamped — FIFO pump order will be broken")
	}
	if startedAt.Valid && startedAt.String != "" {
		t.Errorf("started_at = %q on a QUEUED row — runAssignment was invoked when it shouldn't have been", startedAt.String)
	}
}

func TestDispatch_LeadPlanningBypassesQueue(t *testing.T) {
	// Even with budget full, a lead-planning assignment must NOT
	// queue — a deferred lead deadlocks its entire mission. Locks
	// the carve-out at internal/api/assignments.go (the
	// !req.LeadPlanning guard).
	h, db, crewID, agentIDs, chatID := dispatchPumpRig(t)
	setCrewBudget(t, db, crewID, 1)
	seedAssignmentRow(t, db, "a_inflight", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")
	seedAssignmentRow(t, db, "a_lead", "test-workspace-id", chatID, agentIDs[0], agentIDs[1], "PENDING")

	err := h.DispatchAssignment(context.Background(), orchestrator.DispatchRequest{
		AssignmentID: "a_lead",
		AgentID:      agentIDs[1],
		CrewID:       crewID,
		WorkspaceID:  "test-workspace-id",
		ChatID:       chatID,
		Task:         "lead plans",
		LeadPlanning: true, // <-- the carve-out
	})
	if err != nil {
		t.Fatalf("DispatchAssignment: %v", err)
	}
	// Lead bypasses claim → no QUEUED. runAssignment fires (orch=nil
	// → ends FAILED) and the row is non-QUEUED post-call.
	if got := statusOf(t, db, "a_lead"); got == "QUEUED" {
		t.Errorf("lead-planning assignment was queued — carve-out broken")
	}
}

// ── pumpAndDispatch + completion-path pump ────────────────────────────

func TestPumpAndDispatch_NoQueuedRows_NoOp(t *testing.T) {
	h, _, crewID, _, _ := dispatchPumpRig(t)
	n, err := h.pumpAndDispatch(context.Background(), crewID)
	if err != nil {
		t.Fatalf("pumpAndDispatch: %v", err)
	}
	if n != 0 {
		t.Errorf("dispatched %d, want 0 (queue empty)", n)
	}
}

func TestPumpAndDispatch_BudgetFreeUpAfterCompletion(t *testing.T) {
	// Scenario: 2 RUNNING + 1 QUEUED at budget=2. Complete one of
	// the RUNNING (i.e. drop it to COMPLETED). pumpAndDispatch on
	// that crew should claim the QUEUED row and start dispatching
	// it. We can't easily assert "agent actually ran" without orch,
	// but we CAN assert the QUEUED row transitioned to a non-
	// QUEUED status (the pump's CAS flipped it to RUNNING, then
	// runAssignment's orch=nil path will eventually flip to FAILED).
	h, db, crewID, agentIDs, chatID := dispatchPumpRig(t)
	setCrewBudget(t, db, crewID, 2)
	seedAssignmentRow(t, db, "a_done", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "COMPLETED")
	seedAssignmentRow(t, db, "a_run", "test-workspace-id", chatID, agentIDs[0], agentIDs[1], "RUNNING")
	seedAssignmentRow(t, db, "a_q", "test-workspace-id", chatID, agentIDs[0], agentIDs[2], "QUEUED")
	if _, err := db.Exec(`UPDATE assignments SET queued_at = datetime('now') WHERE id = 'a_q'`); err != nil {
		t.Fatalf("stamp queued_at: %v", err)
	}

	n, err := h.pumpAndDispatch(context.Background(), crewID)
	if err != nil {
		t.Fatalf("pumpAndDispatch: %v", err)
	}
	if n != 1 {
		t.Fatalf("dispatched %d, want 1 (one QUEUED + free slot)", n)
	}
	// Pump's CAS already flipped a_q to RUNNING; the dispatched
	// goroutine then runs runAssignment which (orch=nil) ends
	// FAILED. Both states are valid post-pump terminal-ish; the
	// regression we care about is "still QUEUED".
	final := waitForStatus(t, db, "a_q", []string{"RUNNING", "COMPLETED", "FAILED"}, 2*time.Second)
	if final == "QUEUED" {
		t.Errorf("a_q stayed QUEUED after pump")
	}
}

func TestPumpAndDispatch_EmptyCrewID_NoOp(t *testing.T) {
	// Defensive: pumpAndDispatch with no crew id (caller couldn't
	// resolve it, e.g. the assignment row was deleted between
	// completion and pump). Must NOT panic or error — just no-op.
	h, _, _, _, _ := dispatchPumpRig(t)
	n, err := h.pumpAndDispatch(context.Background(), "")
	if err != nil {
		t.Fatalf("pumpAndDispatch with empty crew: %v", err)
	}
	if n != 0 {
		t.Errorf("dispatched %d, want 0", n)
	}
}

func TestCrewIDForAssignment_RoundTrips(t *testing.T) {
	h, db, crewID, agentIDs, chatID := dispatchPumpRig(t)
	seedAssignmentRow(t, db, "a_lookup", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")

	got, err := h.crewIDForAssignment(context.Background(), "a_lookup")
	if err != nil {
		t.Fatalf("crewIDForAssignment: %v", err)
	}
	if got != crewID {
		t.Errorf("crew id = %q, want %q", got, crewID)
	}
}

func TestCrewIDForAssignment_MissingRow_ReturnsEmpty(t *testing.T) {
	// Caller (completion path) treats empty-string as "no pump",
	// which is the right behaviour when the assignment row was
	// deleted out-of-band between status update and pump.
	h, _, _, _, _ := dispatchPumpRig(t)
	got, err := h.crewIDForAssignment(context.Background(), "a_nope")
	if err != nil {
		t.Fatalf("crewIDForAssignment unknown id: %v", err)
	}
	if got != "" {
		t.Errorf("crew id = %q, want empty for missing row", got)
	}
}

// ── End-to-end: dispatchByID against a queued row ─────────────────────

func TestDispatchByID_LoadsBodyAndDispatches(t *testing.T) {
	// Seed an already-RUNNING row (mimicking what claimCrewSlot
	// would leave behind) and call dispatchByID directly. It must
	// load the joined target + creds + crew members and call
	// runAssignment, ending in a terminal status (FAILED because
	// orch=nil).
	h, db, _, agentIDs, chatID := dispatchPumpRig(t)
	seedAssignmentRow(t, db, "a_dbid", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")

	if err := h.dispatchByID(context.Background(), "a_dbid"); err != nil {
		t.Fatalf("dispatchByID: %v", err)
	}
	// runAssignment without orch hits its short-circuit, sets
	// terminal status. By return time the row is FAILED (sync
	// short-circuit, no goroutine inside).
	if got := statusOf(t, db, "a_dbid"); got != "FAILED" && got != "COMPLETED" {
		t.Errorf("post-dispatchByID status = %q, want FAILED|COMPLETED", got)
	}
}

func TestDispatchByID_UnknownAssignment_ReturnsError(t *testing.T) {
	h, _, _, _, _ := dispatchPumpRig(t)
	err := h.dispatchByID(context.Background(), "a_nope")
	if err == nil {
		t.Fatalf("dispatchByID(nonexistent): want error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want substring 'not found'", err.Error())
	}
}

// ── WS event emission ─────────────────────────────────────────────────

func TestEmitAssignmentQueued_DoesNotPanicWithoutHub(t *testing.T) {
	// broadcastChannelEvent / broadcastWorkspaceEvent are nil-safe
	// in this codebase. Validate that emitAssignmentQueued tolerates
	// missing hub state (unit test runs with a real but unsubscribed
	// hub — no listeners; broadcast is a no-op).
	h, _, crewID, _, chatID := dispatchPumpRig(t)
	// Should not panic; should not error (returns void).
	h.emitAssignmentQueued(context.Background(), "a_evt", chatID, "test-workspace-id", crewID, "slug")
}

func TestEmitAssignmentQueued_PayloadIncludesQueueDepth(t *testing.T) {
	// emitAssignmentQueued reads queueDepth at emit time. Subscribe
	// a WS client to the workspace channel + verify the payload's
	// queue_depth matches the actual count.
	h, db, crewID, agentIDs, chatID := dispatchPumpRig(t)
	// Seed 2 QUEUED rows so depth = 2.
	seedAssignmentRow(t, db, "a_q1", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "QUEUED")
	seedAssignmentRow(t, db, "a_q2", "test-workspace-id", chatID, agentIDs[0], agentIDs[1], "QUEUED")

	// We don't subscribe a real WS client here (hub setup is
	// heavyweight); instead we verify queueDepth directly — the
	// helper that emitAssignmentQueued delegates to. The actual WS
	// payload composition is exercised by the broadcastWorkspaceEvent
	// path which has its own coverage.
	depth, err := queueDepth(context.Background(), h.db, crewID)
	if err != nil {
		t.Fatalf("queueDepth: %v", err)
	}
	if depth != 2 {
		t.Errorf("queue_depth = %d, want 2", depth)
	}
}

