package api

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// stuckSweeperRig builds the standard test fixtures: workspace + crew
// + 3 agents + chat. orch=nil so any goroutine pumpAndDispatch spawns
// hits "orchestrator not available" and writes a terminal status —
// the sweeper's contract is "queue moved", not "agent actually ran",
// and the FAILED-on-orch-nil path lets us verify the queue
// transitions in isolation.
func stuckSweeperRig(t *testing.T) (*AssignmentHandler, *sql.DB, string, []string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewID := "crew_sweep"
	if _, err := db.Exec(`
		INSERT INTO crews (id, workspace_id, name, slug, icon, color, container_memory_mb, container_cpus)
		VALUES (?, ?, 'Sweep Test', 'sweep', '🧹', '#000', 4096, 2)`,
		crewID, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	agentIDs := make([]string, 3)
	for i := range agentIDs {
		id := "agent_sweep_" + string(rune('a'+i))
		agentIDs[i] = id
		if _, err := db.Exec(`
			INSERT INTO agents (id, crew_id, workspace_id, name, slug, role_title, agent_role,
			                    cli_adapter, llm_provider, llm_model, tool_profile, timeout_seconds, memory_enabled)
			VALUES (?, ?, ?, ?, ?, 'Worker', 'AGENT',
			        'CLAUDE_CODE', 'ANTHROPIC', 'claude-sonnet-4-6', 'standard', 60, 0)`,
			id, crewID, wsID, "Agent "+string(rune('A'+i)), "agent-sweep-"+string(rune('a'+i))); err != nil {
			t.Fatalf("seed agent %d: %v", i, err)
		}
	}

	chatID := "chat_sweep"
	if _, err := db.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'sweep test', 'MISSION', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`,
		chatID, agentIDs[0], wsID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	hub := ws.NewHub(logger, nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	h := NewAssignmentHandler(db, nil, hub, "internal-test", logger)
	return h, db, crewID, agentIDs, chatID
}

// seedQueuedAt inserts a QUEUED assignment with a synthetic queued_at
// stamp — letting the test backdate rows past the sweeper's stale
// threshold without sleeping.
func seedQueuedAt(t *testing.T, db *sql.DB, id, chatID, byAgent, toAgent, queuedAt string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, queued_at, created_at)
		VALUES (?, ?, ?, ?, ?, 'test-task', 'QUEUED', ?, datetime('now'))`,
		id, "test-workspace-id", chatID, byAgent, toAgent, queuedAt); err != nil {
		t.Fatalf("seed queued assignment %s: %v", id, err)
	}
}

// ── SweepStuckQueued ──────────────────────────────────────────────────

func TestSweepStuckQueued_OldQueuedRows_GetPumped(t *testing.T) {
	// 3 stale QUEUED rows in one crew, budget=2 (default), 0 RUNNING
	// → the sweeper should pump 2 (budget) and leave the third
	// QUEUED. Locks the "sweeper calls pumpAndDispatch which is
	// already idempotent" contract.
	h, db, crewID, agentIDs, chatID := stuckSweeperRig(t)
	old := "2026-01-01 00:00:00.000" // far past the 1m default stale threshold
	seedQueuedAt(t, db, "a_stuck_1", chatID, agentIDs[0], agentIDs[0], old)
	seedQueuedAt(t, db, "a_stuck_2", chatID, agentIDs[0], agentIDs[1], old)
	seedQueuedAt(t, db, "a_stuck_3", chatID, agentIDs[0], agentIDs[2], old)

	pumped, err := h.SweepStuckQueued(context.Background(), 1*time.Minute)
	if err != nil {
		t.Fatalf("SweepStuckQueued: %v", err)
	}
	if pumped != 2 {
		t.Errorf("pumped = %d, want 2 (budget)", pumped)
	}

	// Verify: 2 transitioned out of QUEUED, 1 still QUEUED.
	var queued, nonQueued int
	_ = db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE status = 'QUEUED'`).Scan(&queued)
	_ = db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE status != 'QUEUED'`).Scan(&nonQueued)
	_ = crewID // silence unused; we identify rows by chat instead
	if queued != 1 || nonQueued != 2 {
		t.Errorf("post-sweep: queued=%d (want 1), non-queued=%d (want 2)", queued, nonQueued)
	}
}

func TestSweepStuckQueued_FreshRows_NotPumped(t *testing.T) {
	// Rows queued moments ago are not stale — sweeper must leave
	// them for the normal pump path. Pumping fresh rows would race
	// the completion-path pump and could double-claim during a real
	// completion (which would be safe because of the CAS, but a
	// wasted pump call is still noise).
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	fresh := time.Now().UTC().Format("2006-01-02 15:04:05.000")
	seedQueuedAt(t, db, "a_fresh_1", chatID, agentIDs[0], agentIDs[0], fresh)
	seedQueuedAt(t, db, "a_fresh_2", chatID, agentIDs[0], agentIDs[1], fresh)

	pumped, err := h.SweepStuckQueued(context.Background(), 5*time.Minute)
	if err != nil {
		t.Fatalf("SweepStuckQueued: %v", err)
	}
	if pumped != 0 {
		t.Errorf("pumped = %d, want 0 (rows are fresh)", pumped)
	}
	var queued int
	_ = db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE status = 'QUEUED'`).Scan(&queued)
	if queued != 2 {
		t.Errorf("queued = %d, want 2 (fresh rows untouched)", queued)
	}
}

func TestSweepStuckQueued_NoStaleCrews_ReturnsZero(t *testing.T) {
	// Empty queue: sweeper short-circuits without invoking pump.
	h, _, _, _, _ := stuckSweeperRig(t)
	pumped, err := h.SweepStuckQueued(context.Background(), 1*time.Minute)
	if err != nil {
		t.Fatalf("SweepStuckQueued: %v", err)
	}
	if pumped != 0 {
		t.Errorf("pumped = %d, want 0 on empty queue", pumped)
	}
}

func TestSweepStuckQueued_DefaultStaleAfter_AppliedWhenZero(t *testing.T) {
	// staleAfter <= 0 must fall back to the default (1 min). A row
	// queued 30 seconds ago alone in the crew must NOT trigger a
	// sweep: the staleness check finds no qualifying crew, the
	// sweeper short-circuits, the row stays QUEUED.
	//
	// (We deliberately don't co-seed a 2-min-old row here: once one
	// row in the crew is stale, pumpAndDispatch claims every QUEUED
	// row in that crew that fits the budget — fresh ones included.
	// That's the "the crew is unhealthy, drain it" recovery semantic,
	// not a bug. To exercise the staleness predicate in isolation,
	// the test needs exactly one fresh row in the crew.)
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	thirtySecAgo := time.Now().UTC().Add(-30 * time.Second).Format("2006-01-02 15:04:05.000")
	seedQueuedAt(t, db, "a_fresh_alone", chatID, agentIDs[0], agentIDs[0], thirtySecAgo)

	pumped, err := h.SweepStuckQueued(context.Background(), 0) // 0 → default 1min
	if err != nil {
		t.Fatalf("SweepStuckQueued: %v", err)
	}
	if pumped != 0 {
		t.Errorf("pumped = %d, want 0 (30s row alone, default stale is 60s)", pumped)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM assignments WHERE id = 'a_fresh_alone'`).Scan(&status)
	if status != "QUEUED" {
		t.Errorf("a_fresh_alone status = %q, want QUEUED", status)
	}
}

func TestSweepStuckQueued_CrewWithStaleRow_DrainsEverythingInThatCrew(t *testing.T) {
	// Documents the actual recovery semantic: once at least one
	// QUEUED row in a crew is older than staleAfter, the sweeper
	// considers the WHOLE crew unhealthy and asks pumpAndDispatch
	// to drain it (subject to budget). A row that's only 30s old
	// gets pumped along with the 2-min-old companion because the
	// crew's normal pump path is presumed broken.
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	thirtySecAgo := time.Now().UTC().Add(-30 * time.Second).Format("2006-01-02 15:04:05.000")
	twoMinAgo := time.Now().UTC().Add(-2 * time.Minute).Format("2006-01-02 15:04:05.000")
	seedQueuedAt(t, db, "a_companion", chatID, agentIDs[0], agentIDs[0], thirtySecAgo)
	seedQueuedAt(t, db, "a_truly_stale", chatID, agentIDs[0], agentIDs[1], twoMinAgo)

	pumped, err := h.SweepStuckQueued(context.Background(), 1*time.Minute)
	if err != nil {
		t.Fatalf("SweepStuckQueued: %v", err)
	}
	// Crew budget=2 (default), 0 RUNNING → both rows fit. Both get
	// pumped because the crew is flagged unhealthy.
	if pumped != 2 {
		t.Errorf("pumped = %d, want 2 (whole crew drained)", pumped)
	}
}

func TestSweepStuckQueued_MultiCrew_PumpsEach(t *testing.T) {
	// Two crews each with a stuck QUEUED row + budget room. Sweeper
	// must invoke pump for BOTH — a bug that grouped per-workspace
	// instead of per-crew would only drain one crew per sweep.
	h, db, crewA, agentIDs, chatID := stuckSweeperRig(t)
	// Seed a second crew + agent so we have two distinct crews.
	if _, err := db.Exec(`
		INSERT INTO crews (id, workspace_id, name, slug, icon, color, container_memory_mb, container_cpus)
		VALUES ('crew_sweep_b', 'test-workspace-id', 'Sweep B', 'sweep-b', '🧹', '#000', 4096, 2)`); err != nil {
		t.Fatalf("seed crew b: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO agents (id, crew_id, workspace_id, name, slug, role_title, agent_role,
		                    cli_adapter, llm_provider, llm_model, tool_profile, timeout_seconds, memory_enabled)
		VALUES ('agent_sweep_b_crewb', 'crew_sweep_b', 'test-workspace-id', 'Bee', 'agent-multicrew-b', 'Tester', 'AGENT',
		        'CLAUDE_CODE', 'ANTHROPIC', 'claude-sonnet-4-6', 'standard', 60, 0)`); err != nil {
		t.Fatalf("seed agent b: %v", err)
	}
	old := "2026-01-01 00:00:00.000"
	seedQueuedAt(t, db, "a_crew_a", chatID, agentIDs[0], agentIDs[0], old)
	seedQueuedAt(t, db, "a_crew_b", chatID, agentIDs[0], "agent_sweep_b_crewb", old)

	pumped, err := h.SweepStuckQueued(context.Background(), 1*time.Minute)
	if err != nil {
		t.Fatalf("SweepStuckQueued: %v", err)
	}
	if pumped != 2 {
		t.Errorf("pumped = %d, want 2 (one per crew)", pumped)
	}
	_ = crewA
}

func TestSweepStuckQueued_RespectsBudgetCeiling(t *testing.T) {
	// Stale crew has 2 RUNNING already (budget=2) + 3 stale QUEUED.
	// Sweep pumps 0 — budget is saturated. The RUNNING completion
	// path will drain via the normal pump; the sweeper just adds a
	// safety net for the crashed-pump case, not a budget bypass.
	h, db, crewID, agentIDs, chatID := stuckSweeperRig(t)
	setCrewBudget(t, db, crewID, 2)
	insertAssignment(t, db, "a_run_a", "test-workspace-id", chatID, agentIDs[0], agentIDs[0], "RUNNING")
	insertAssignment(t, db, "a_run_b", "test-workspace-id", chatID, agentIDs[0], agentIDs[1], "RUNNING")
	old := "2026-01-01 00:00:00.000"
	seedQueuedAt(t, db, "a_stuck_1", chatID, agentIDs[0], agentIDs[2], old)
	seedQueuedAt(t, db, "a_stuck_2", chatID, agentIDs[0], agentIDs[2], old)
	seedQueuedAt(t, db, "a_stuck_3", chatID, agentIDs[0], agentIDs[2], old)

	pumped, err := h.SweepStuckQueued(context.Background(), 1*time.Minute)
	if err != nil {
		t.Fatalf("SweepStuckQueued: %v", err)
	}
	if pumped != 0 {
		t.Errorf("pumped = %d, want 0 (budget saturated)", pumped)
	}
}

// ── StartStuckQueueSweeper ────────────────────────────────────────────

func TestStartStuckQueueSweeper_TicksAndExits(t *testing.T) {
	// Verify the goroutine fires SweepStuckQueued at least once
	// within the test window and exits cleanly on ctx cancel.
	// Uses a very short interval (50ms) so the test finishes fast;
	// 1s safety window so a slow CI machine doesn't false-fail.
	h, db, _, agentIDs, chatID := stuckSweeperRig(t)
	old := "2026-01-01 00:00:00.000"
	seedQueuedAt(t, db, "a_tick_1", chatID, agentIDs[0], agentIDs[0], old)

	ctx, cancel := context.WithCancel(context.Background())
	h.StartStuckQueueSweeper(ctx, 50*time.Millisecond, 1*time.Millisecond)

	// Poll for the QUEUED row to transition out — the sweeper's
	// observable side effect.
	deadline := time.Now().Add(1 * time.Second)
	pumped := false
	for !pumped && time.Now().Before(deadline) {
		var status string
		_ = db.QueryRow(`SELECT status FROM assignments WHERE id = 'a_tick_1'`).Scan(&status)
		if status != "QUEUED" {
			pumped = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	if !pumped {
		t.Errorf("sweeper did not pump within 1s window")
	}
	// Give the goroutine a tick to notice ctx cancel before the
	// test exits and closes the DB. Without this the goroutine may
	// race the t.Cleanup that closes the connection.
	time.Sleep(100 * time.Millisecond)
}

func TestStartStuckQueueSweeper_DefaultInterval_AppliedWhenZero(t *testing.T) {
	// interval <= 0 must fall back to defaultSweeperInterval (5min).
	// We can't wait 5 min in a unit test, so the assertion is "the
	// goroutine started and is still alive after a brief moment" —
	// i.e. it didn't deadlock or panic on a zero ticker. ctx cancel
	// exits the goroutine.
	h, _, _, _, _ := stuckSweeperRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.StartStuckQueueSweeper(ctx, 0, 0) // both default
	// Cancel after a short delay; if the goroutine hadn't started
	// the cancel would be a no-op. We're really testing that no
	// panic fired in the goroutine setup.
	time.Sleep(50 * time.Millisecond)
}
