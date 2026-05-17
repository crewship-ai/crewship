package api

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"
)

// queueTestRig spins up the standard migrated test DB plus the
// fixture rows that every queue test needs:
//   - 1 workspace + OWNER user
//   - 1 crew (container_memory_mb defaulted to 4096 ⇒ budget 2)
//   - N agents in that crew (caller decides N)
//   - 1 chat to anchor assignments
//
// Returns the (db, crewID, agentIDs, chatID) tuple. Each test then
// inserts assignments directly via insertAssignment below — bypassing
// the AssignmentHandler so the tests focus on the queue primitives.
func queueTestRig(t *testing.T, numAgents int) (*sql.DB, string, []string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Crew with 4096 MiB → budget = 4096 / 2048 = 2 by default. Tests
	// that need a different budget set crews.max_concurrent_agents
	// explicitly via setCrewBudget.
	crewID := "crew_queue_test"
	if _, err := db.Exec(`
		INSERT INTO crews (id, workspace_id, name, slug, icon, color, container_memory_mb, container_cpus)
		VALUES (?, ?, 'Queue Test', 'queue-test', '🧪', '#000', 4096, 2)`,
		crewID, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	agentIDs := make([]string, numAgents)
	for i := 0; i < numAgents; i++ {
		id := "agent_queue_" + string(rune('a'+i))
		agentIDs[i] = id
		if _, err := db.Exec(`
			INSERT INTO agents (id, crew_id, workspace_id, name, slug, role_title, agent_role, cli_adapter, llm_provider, llm_model)
			VALUES (?, ?, ?, ?, ?, 'Tester', 'AGENT', 'CLAUDE_CODE', 'ANTHROPIC', 'claude-sonnet-4-6')`,
			id, crewID, wsID, "Agent "+string(rune('A'+i)), "agent-"+string(rune('a'+i))); err != nil {
			t.Fatalf("seed agent %d: %v", i, err)
		}
	}

	chatID := "chat_queue_test"
	if _, err := db.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'queue test', 'MISSION', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`,
		chatID, agentIDs[0], wsID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	return db, crewID, agentIDs, chatID
}

func setCrewBudget(t *testing.T, db *sql.DB, crewID string, budget int) {
	t.Helper()
	if _, err := db.Exec(`UPDATE crews SET max_concurrent_agents = ? WHERE id = ?`, budget, crewID); err != nil {
		t.Fatalf("setCrewBudget: %v", err)
	}
}

func insertAssignment(t *testing.T, db *sql.DB, id, wsID, chatID, byAgent, toAgent, status string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'test-task', ?, datetime('now'))`,
		id, wsID, chatID, byAgent, toAgent, status); err != nil {
		t.Fatalf("insert assignment %s: %v", id, err)
	}
}

func assignmentStatus(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM assignments WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatalf("read status %s: %v", id, err)
	}
	return s
}

// ── computeCrewBudget ─────────────────────────────────────────────────

func TestQueue_ComputeCrewBudget_OperatorOverrideWins(t *testing.T) {
	// max_concurrent_agents = 5 must beat the memory-derived default of
	// 2 (4096 / 2048). Otherwise the operator's explicit override is
	// silently ignored — which is exactly the kind of surprise that
	// would force people to read the source.
	db, crewID, _, _ := queueTestRig(t, 1)
	setCrewBudget(t, db, crewID, 5)
	got, err := computeCrewBudget(context.Background(), db, crewID)
	if err != nil {
		t.Fatalf("computeCrewBudget: %v", err)
	}
	if got != 5 {
		t.Errorf("budget = %d, want 5", got)
	}
}

func TestQueue_ComputeCrewBudget_DerivesFromMemoryWhenNoOverride(t *testing.T) {
	// 4096 MiB / 2048 MiB-per-agent = 2 slots. The fallback derivation
	// is the path 99% of operators hit; lock the math.
	db, crewID, _, _ := queueTestRig(t, 1)
	got, err := computeCrewBudget(context.Background(), db, crewID)
	if err != nil {
		t.Fatalf("computeCrewBudget: %v", err)
	}
	if got != 2 {
		t.Errorf("budget = %d, want 2 (4096 / %d)", got, defaultAgentMemoryEstimateMB)
	}
}

func TestQueue_ComputeCrewBudget_TinyMemoryFloorsToOne(t *testing.T) {
	// 512 / 2048 = 0 by integer math. A budget of 0 deadlocks the
	// queue (every claim fails), so the implementation must floor to
	// 1. The CHECK on max_concurrent_agents prevents operators from
	// explicitly setting 0; the floor here is the safety net for the
	// derived path.
	db, crewID, _, _ := queueTestRig(t, 1)
	if _, err := db.Exec(`UPDATE crews SET container_memory_mb = 512 WHERE id = ?`, crewID); err != nil {
		t.Fatalf("update memory: %v", err)
	}
	got, err := computeCrewBudget(context.Background(), db, crewID)
	if err != nil {
		t.Fatalf("computeCrewBudget: %v", err)
	}
	if got != 1 {
		t.Errorf("budget = %d, want 1 (floor)", got)
	}
}

func TestQueue_ComputeCrewBudget_UnknownCrew_ReturnsOne(t *testing.T) {
	// A missing crew row is an upstream bug (foreign-key violation
	// elsewhere), but the queue helper must not propagate it as an
	// error — the caller would then 500 a dispatch that's about to
	// fail at the next JOIN anyway. Return 1 so SQLite surfaces the
	// real FK error at INSERT/UPDATE time with a clearer message.
	db := setupTestDB(t)
	got, err := computeCrewBudget(context.Background(), db, "crew_nope")
	if err != nil {
		t.Fatalf("computeCrewBudget: %v", err)
	}
	if got != 1 {
		t.Errorf("budget = %d, want 1", got)
	}
}

func TestQueue_ComputeCrewBudget_ZeroBudgetGuard(t *testing.T) {
	// The CHECK on crews.max_concurrent_agents must reject 0. We
	// can't set 0 via UPDATE; verify by attempting it and asserting
	// the constraint fires. If a future migration drops the CHECK,
	// this test catches the regression before the queue deadlocks
	// silently in prod.
	db, crewID, _, _ := queueTestRig(t, 1)
	_, err := db.Exec(`UPDATE crews SET max_concurrent_agents = 0 WHERE id = ?`, crewID)
	if err == nil {
		t.Fatalf("CHECK constraint missing — UPDATE max_concurrent_agents=0 must fail")
	}
}

// ── claimCrewSlot ─────────────────────────────────────────────────────

func TestQueue_ClaimCrewSlot_HappyPath_FlipsPendingToRunning(t *testing.T) {
	// One agent, budget 2, one PENDING assignment. The CAS must
	// succeed and stamp running_at.
	db, crewID, agentIDs, chatID := queueTestRig(t, 1)
	insertAssignment(t, db, "a_1", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "PENDING")

	claimed, err := claimCrewSlot(context.Background(), db, "a_1", crewID, 2)
	if err != nil {
		t.Fatalf("claimCrewSlot: %v", err)
	}
	if !claimed {
		t.Fatalf("claimed = false, want true (budget had room)")
	}
	if got := assignmentStatus(t, db, "a_1"); got != "RUNNING" {
		t.Errorf("status = %q, want RUNNING", got)
	}
	var runningAt sql.NullString
	if err := db.QueryRow(`SELECT running_at FROM assignments WHERE id = 'a_1'`).Scan(&runningAt); err != nil {
		t.Fatalf("read running_at: %v", err)
	}
	if !runningAt.Valid || runningAt.String == "" {
		t.Errorf("running_at not stamped on RUNNING transition")
	}
}

func TestQueue_ClaimCrewSlot_BudgetFull_ReturnsFalse(t *testing.T) {
	// Two RUNNING assignments + budget=2 + one new PENDING → CAS
	// must refuse the third. Status stays PENDING; caller will then
	// markAssignmentQueued.
	db, crewID, agentIDs, chatID := queueTestRig(t, 1)
	insertAssignment(t, db, "a_run_1", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")
	insertAssignment(t, db, "a_run_2", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")
	insertAssignment(t, db, "a_pending", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "PENDING")

	claimed, err := claimCrewSlot(context.Background(), db, "a_pending", crewID, 2)
	if err != nil {
		t.Fatalf("claimCrewSlot: %v", err)
	}
	if claimed {
		t.Fatalf("claimed = true, want false (budget full)")
	}
	if got := assignmentStatus(t, db, "a_pending"); got != "PENDING" {
		t.Errorf("status = %q, want PENDING (unchanged)", got)
	}
}

func TestQueue_ClaimCrewSlot_TerminalStatusNotReclaimable(t *testing.T) {
	// A COMPLETED row must not be re-flipped to RUNNING by a stale
	// claim attempt. status IN ('PENDING','QUEUED') in the WHERE
	// guards this.
	db, crewID, agentIDs, chatID := queueTestRig(t, 1)
	insertAssignment(t, db, "a_done", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "COMPLETED")

	claimed, err := claimCrewSlot(context.Background(), db, "a_done", crewID, 5)
	if err != nil {
		t.Fatalf("claimCrewSlot: %v", err)
	}
	if claimed {
		t.Fatalf("claimed = true on COMPLETED row — terminal status must not be re-RUNNING'd")
	}
	if got := assignmentStatus(t, db, "a_done"); got != "COMPLETED" {
		t.Errorf("status = %q, want COMPLETED", got)
	}
}

func TestQueue_ClaimCrewSlot_QueuedRowReclaimable(t *testing.T) {
	// pumpCrewQueue depends on claimCrewSlot accepting QUEUED rows
	// too — otherwise no QUEUED row could ever transition to
	// RUNNING. Lock the predicate explicitly.
	db, crewID, agentIDs, chatID := queueTestRig(t, 1)
	insertAssignment(t, db, "a_queued", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "QUEUED")

	claimed, err := claimCrewSlot(context.Background(), db, "a_queued", crewID, 5)
	if err != nil {
		t.Fatalf("claimCrewSlot: %v", err)
	}
	if !claimed {
		t.Fatalf("claimed = false on QUEUED row — pump path depends on this transition")
	}
	if got := assignmentStatus(t, db, "a_queued"); got != "RUNNING" {
		t.Errorf("status = %q, want RUNNING", got)
	}
}

func TestQueue_ClaimCrewSlot_ZeroBudgetTreatedAsNoSlot(t *testing.T) {
	// Defence in depth: even though the CHECK constraint blocks
	// operator-set 0, a future caller computing budget incorrectly
	// could pass 0. The helper must treat that as "no slot" and not
	// run the CAS (which would always fail anyway, but the early
	// return is cleaner + matches the comment on the helper).
	db, crewID, agentIDs, chatID := queueTestRig(t, 1)
	insertAssignment(t, db, "a_p", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "PENDING")

	claimed, err := claimCrewSlot(context.Background(), db, "a_p", crewID, 0)
	if err != nil {
		t.Fatalf("claimCrewSlot: %v", err)
	}
	if claimed {
		t.Fatalf("claimed = true with budget=0")
	}
	if got := assignmentStatus(t, db, "a_p"); got != "PENDING" {
		t.Errorf("status = %q, want PENDING", got)
	}
}

func TestQueue_ClaimCrewSlot_ConcurrentClaims_HonourBudget(t *testing.T) {
	// THE motivating test: 8 goroutines each try to claim a slot for
	// a distinct PENDING assignment, budget = 3. Exactly 3 must end
	// up RUNNING; the other 5 must stay PENDING. The CAS atomicity
	// is the only mechanism that can deliver this guarantee — a
	// "read inflight, then UPDATE" sequence would oversubscribe.
	db, crewID, agentIDs, chatID := queueTestRig(t, 1)
	setCrewBudget(t, db, crewID, 3)

	const N = 8
	ids := make([]string, N)
	for i := 0; i < N; i++ {
		ids[i] = "a_race_" + string(rune('a'+i))
		insertAssignment(t, db, ids[i], "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "PENDING")
	}

	results := make([]bool, N)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			ok, err := claimCrewSlot(context.Background(), db, ids[idx], crewID, 3)
			if err != nil {
				t.Errorf("goroutine %d: %v", idx, err)
				return
			}
			results[idx] = ok
		}(i)
	}
	close(start)
	wg.Wait()

	wins := 0
	for _, ok := range results {
		if ok {
			wins++
		}
	}
	if wins != 3 {
		t.Fatalf("wins = %d, want exactly 3 (budget)", wins)
	}

	// Cross-check via the DB: exactly 3 RUNNING, 5 PENDING.
	var running, pending int
	_ = db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE status='RUNNING'`).Scan(&running)
	_ = db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE status='PENDING'`).Scan(&pending)
	if running != 3 || pending != 5 {
		t.Errorf("post-race counts: running=%d (want 3), pending=%d (want 5)", running, pending)
	}
}

// ── markAssignmentQueued ──────────────────────────────────────────────

func TestQueue_MarkQueued_FlipsPendingToQueued_WithTimestamp(t *testing.T) {
	db, _, agentIDs, chatID := queueTestRig(t, 1)
	insertAssignment(t, db, "a_q", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "PENDING")

	if err := markAssignmentQueued(context.Background(), db, "a_q"); err != nil {
		t.Fatalf("markAssignmentQueued: %v", err)
	}
	if got := assignmentStatus(t, db, "a_q"); got != "QUEUED" {
		t.Errorf("status = %q, want QUEUED", got)
	}
	var queuedAt sql.NullString
	_ = db.QueryRow(`SELECT queued_at FROM assignments WHERE id = 'a_q'`).Scan(&queuedAt)
	if !queuedAt.Valid || queuedAt.String == "" {
		t.Errorf("queued_at not stamped — pump FIFO ordering will be broken")
	}
}

func TestQueue_MarkQueued_DoesNotRestampAlreadyQueued(t *testing.T) {
	// A subsequent markAssignmentQueued call (e.g. the dispatcher
	// retried on a row that's already QUEUED) must not reset
	// queued_at. Doing so would jump the row to the back of the FIFO
	// queue — the operator who waited longest would keep waiting.
	db, _, agentIDs, chatID := queueTestRig(t, 1)
	insertAssignment(t, db, "a_q", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "PENDING")
	if err := markAssignmentQueued(context.Background(), db, "a_q"); err != nil {
		t.Fatalf("first mark: %v", err)
	}
	var first string
	_ = db.QueryRow(`SELECT queued_at FROM assignments WHERE id = 'a_q'`).Scan(&first)
	// Force a measurable gap so the SQLite subsec timestamp would
	// definitely differ if the second mark mistakenly re-stamped.
	time.Sleep(20 * time.Millisecond)
	if err := markAssignmentQueued(context.Background(), db, "a_q"); err != nil {
		t.Fatalf("second mark: %v", err)
	}
	var second string
	_ = db.QueryRow(`SELECT queued_at FROM assignments WHERE id = 'a_q'`).Scan(&second)
	if first != second {
		t.Errorf("queued_at changed: %q → %q (FIFO fairness broken)", first, second)
	}
}

// ── pumpCrewQueue ─────────────────────────────────────────────────────

func TestQueue_PumpCrewQueue_BudgetSaturated_NoClaims(t *testing.T) {
	// Crew already at budget (2 RUNNING, budget=2) + 1 QUEUED waiting.
	// Pump must take nothing.
	db, crewID, agentIDs, chatID := queueTestRig(t, 1)
	insertAssignment(t, db, "a_r1", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")
	insertAssignment(t, db, "a_r2", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")
	insertAssignment(t, db, "a_q1", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "QUEUED")
	if _, err := db.Exec(`UPDATE assignments SET queued_at = datetime('now') WHERE id = 'a_q1'`); err != nil {
		t.Fatalf("stamp queued_at: %v", err)
	}

	claimed, err := pumpCrewQueue(context.Background(), db, crewID, 2)
	if err != nil {
		t.Fatalf("pumpCrewQueue: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("claimed %d rows, want 0 (budget saturated)", len(claimed))
	}
	if got := assignmentStatus(t, db, "a_q1"); got != "QUEUED" {
		t.Errorf("a_q1 status = %q, want QUEUED", got)
	}
}

func TestQueue_PumpCrewQueue_FIFOByQueuedAt(t *testing.T) {
	// 3 QUEUED rows with distinct queued_at, budget=2 with 0 inflight.
	// Pump must claim the two oldest (a_first, a_second) and leave the
	// newest (a_third) QUEUED.
	db, crewID, agentIDs, chatID := queueTestRig(t, 1)
	setCrewBudget(t, db, crewID, 2)
	insertAssignment(t, db, "a_third", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "QUEUED")
	insertAssignment(t, db, "a_first", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "QUEUED")
	insertAssignment(t, db, "a_second", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "QUEUED")
	// Force the timestamps; can't rely on insert order matching
	// queued_at order. SQLite's datetime is second-precision unless
	// we ask for subsec — use distinct seconds to be portable.
	_, _ = db.Exec(`UPDATE assignments SET queued_at = '2026-05-17T10:00:00Z' WHERE id = 'a_first'`)
	_, _ = db.Exec(`UPDATE assignments SET queued_at = '2026-05-17T10:00:01Z' WHERE id = 'a_second'`)
	_, _ = db.Exec(`UPDATE assignments SET queued_at = '2026-05-17T10:00:02Z' WHERE id = 'a_third'`)

	claimed, err := pumpCrewQueue(context.Background(), db, crewID, 2)
	if err != nil {
		t.Fatalf("pumpCrewQueue: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed = %v (n=%d), want exactly 2", claimed, len(claimed))
	}
	if claimed[0] != "a_first" || claimed[1] != "a_second" {
		t.Errorf("FIFO order violated: got %v, want [a_first a_second]", claimed)
	}
	if got := assignmentStatus(t, db, "a_third"); got != "QUEUED" {
		t.Errorf("a_third status = %q, want QUEUED (still waiting)", got)
	}
}

func TestQueue_PumpCrewQueue_EmptyQueue_ReturnsEmpty(t *testing.T) {
	// No QUEUED rows; pump must terminate without spinning or erroring.
	db, crewID, _, _ := queueTestRig(t, 1)
	claimed, err := pumpCrewQueue(context.Background(), db, crewID, 5)
	if err != nil {
		t.Fatalf("pumpCrewQueue: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("claimed %d, want 0", len(claimed))
	}
}

func TestQueue_PumpCrewQueue_RespectsBudgetCeiling(t *testing.T) {
	// 5 QUEUED rows, 0 inflight, budget=3 → pump claims exactly 3.
	db, crewID, agentIDs, chatID := queueTestRig(t, 1)
	setCrewBudget(t, db, crewID, 3)
	for i := 0; i < 5; i++ {
		id := "a_pump_" + string(rune('a'+i))
		insertAssignment(t, db, id, "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "QUEUED")
	}
	// Force monotonic queued_at — pump's FIFO must serialise on this.
	if _, err := db.Exec(`
		UPDATE assignments SET queued_at = datetime(strftime('%s', 'now') + (CAST(substr(id, length(id)) AS INTEGER) - 97), 'unixepoch')
		WHERE id LIKE 'a_pump_%'`); err != nil {
		t.Fatalf("stamp queued_at: %v", err)
	}

	claimed, err := pumpCrewQueue(context.Background(), db, crewID, 3)
	if err != nil {
		t.Fatalf("pumpCrewQueue: %v", err)
	}
	if len(claimed) != 3 {
		t.Fatalf("claimed = %d, want 3 (budget)", len(claimed))
	}

	var running, queued int
	_ = db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE status='RUNNING'`).Scan(&running)
	_ = db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE status='QUEUED'`).Scan(&queued)
	if running != 3 || queued != 2 {
		t.Errorf("post-pump: running=%d (want 3), queued=%d (want 2)", running, queued)
	}
}

// ── queueDepth ────────────────────────────────────────────────────────

func TestQueue_QueueDepth_CountsQueuedOnly(t *testing.T) {
	db, crewID, agentIDs, chatID := queueTestRig(t, 1)
	insertAssignment(t, db, "a_q1", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "QUEUED")
	insertAssignment(t, db, "a_q2", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "QUEUED")
	insertAssignment(t, db, "a_r", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")
	insertAssignment(t, db, "a_p", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "PENDING")

	got, err := queueDepth(context.Background(), db, crewID)
	if err != nil {
		t.Fatalf("queueDepth: %v", err)
	}
	if got != 2 {
		t.Errorf("depth = %d, want 2 (only QUEUED counted)", got)
	}
}

func TestQueue_QueueDepth_ScopedPerCrew(t *testing.T) {
	// QUEUED rows in another crew must not bleed into this crew's
	// depth — the count is a per-crew metric, used in WS payloads and
	// the operator-visible "X ahead of you" hint. Bleed would mis-
	// advertise wait times and confuse operators across crews.
	db, crewID, agentIDs, chatID := queueTestRig(t, 1)
	// Create a second crew + agent + assignment in QUEUED status.
	if _, err := db.Exec(`
		INSERT INTO crews (id, workspace_id, name, slug, icon, color, container_memory_mb, container_cpus)
		VALUES ('crew_other', 'test-workspace-id', 'Other', 'other', '🔵', '#abc', 4096, 2)`); err != nil {
		t.Fatalf("seed other crew: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO agents (id, crew_id, workspace_id, name, slug, role_title, agent_role, cli_adapter, llm_provider, llm_model)
		VALUES ('agent_other', 'crew_other', 'test-workspace-id', 'Other Agent', 'agent-other', 'Tester', 'AGENT', 'CLAUDE_CODE', 'ANTHROPIC', 'claude-sonnet-4-6')`); err != nil {
		t.Fatalf("seed other agent: %v", err)
	}
	insertAssignment(t, db, "a_other_q", "test-workspace-id", chatID, agentIDs[0], "agent_other", "QUEUED")
	insertAssignment(t, db, "a_self_q", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "QUEUED")

	got, err := queueDepth(context.Background(), db, crewID)
	if err != nil {
		t.Fatalf("queueDepth: %v", err)
	}
	if got != 1 {
		t.Errorf("depth = %d, want 1 (only this crew's QUEUED)", got)
	}
}
