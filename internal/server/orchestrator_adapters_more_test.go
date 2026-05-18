package server

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// ---------------------------------------------------------------------------
// orchestrator_adapters.go — hooksAdapter.Dispatch and the two
// memoryMetricsAdapter aggregate queries (EntriesSinceLastMemoryUpdate,
// AgentSpendLast24h). The existing orchestrator_adapters_test.go covers
// the workspace-provider nil-typed-interface trap; this file fills in
// the adapter wrappers that read the journal + cost_ledger tables.
// ---------------------------------------------------------------------------

// noopJournalForAdapter satisfies journal.Emitter for the hooks
// dispatcher; we don't need to inspect emit calls here.
type noopJournalForAdapter struct{}

func (noopJournalForAdapter) Emit(_ context.Context, _ journal.Entry) (string, error) {
	return "", nil
}
func (noopJournalForAdapter) Flush(_ context.Context) error { return nil }

// ---- hooksAdapter.Dispatch ----

func TestHooksAdapter_Dispatch_RequiresWorkspaceID(t *testing.T) {
	// Source contract on hooks.Dispatch: empty workspace_id returns
	// "hooks: Dispatch requires workspace_id" — guarantee the adapter
	// forwards the EventContext faithfully so that validation fires.
	db := openTestDB(t)
	a := newHooksAdapter(db, noopJournalForAdapter{})
	err := a.Dispatch(context.Background(), "pre_agent", orchestrator.HookEventContext{
		// WorkspaceID intentionally empty
	})
	if err == nil {
		t.Fatal("expected error from hooks.Dispatch when WorkspaceID is empty")
	}
}

func TestHooksAdapter_Dispatch_NoMatchingHooks_ReturnsNil(t *testing.T) {
	// hooks.Dispatch returns nil when no hooks are registered for the
	// event. Pin that the adapter propagates that nil rather than
	// inventing an error.
	db := openTestDB(t)
	wsID := "ws_adapter_hooks"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', ?)`, wsID, "wsadap"); err != nil {
		t.Fatalf("seed ws: %v", err)
	}

	a := newHooksAdapter(db, noopJournalForAdapter{})
	err := a.Dispatch(context.Background(), "pre_agent", orchestrator.HookEventContext{
		WorkspaceID: wsID,
		AgentID:     "agent-1",
		ToolName:    "anything",
	})
	if err != nil {
		t.Errorf("Dispatch with no hooks should return nil; got %v", err)
	}
}

func TestHooksAdapter_Dispatch_ForwardsAllContextFields(t *testing.T) {
	// Sanity check the field-by-field mapping inside the adapter — a
	// regression that dropped, say, MissionID or ToolName would cause
	// hooks downstream to silently lose targeting context. With no
	// hooks registered the call no-ops; what we really verify is that
	// the adapter accepts a fully-populated struct without crashing.
	db := openTestDB(t)
	wsID := "ws_adapter_fields"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', ?)`, wsID, "wsadapf"); err != nil {
		t.Fatalf("seed ws: %v", err)
	}

	a := newHooksAdapter(db, noopJournalForAdapter{})
	in := orchestrator.HookEventContext{
		WorkspaceID: wsID,
		CrewID:      "crew-x",
		AgentID:     "agent-x",
		MissionID:   "mission-x",
		ToolName:    "shell",
		Severity:    "high",
		Payload:     map[string]any{"cmd": "ls"},
	}
	if err := a.Dispatch(context.Background(), "tool_call", in); err != nil {
		t.Errorf("Dispatch with full context returned %v", err)
	}
}

// ---- memoryMetricsAdapter ----

func seedJournalRowAtTime(t *testing.T, db *sql.DB, wsID, agentID, entryType, ts string) {
	t.Helper()
	// agentID is part of the id so two agents at the same ts don't collide.
	if _, err := db.Exec(`INSERT INTO journal_entries
		(id, workspace_id, agent_id, ts, entry_type, severity, actor_type, summary, payload)
		VALUES (?, ?, ?, ?, ?, 'info', 'orchestrator', ?, '{}')`,
		"je-"+agentID+"-"+ts+"-"+entryType, wsID, agentID, ts, entryType, entryType); err != nil {
		t.Fatalf("seed journal_entries: %v", err)
	}
}

func TestMemoryMetricsAdapter_EntriesSinceLastMemoryUpdate_EmptyWorkspace(t *testing.T) {
	db := openTestDB(t)
	wsID := "ws_metrics_empty"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', ?)`, wsID, "metempty"); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	a := newMemoryMetricsAdapter(db)
	n, err := a.EntriesSinceLastMemoryUpdate(context.Background(), wsID, "agent-1")
	if err != nil {
		t.Fatalf("EntriesSinceLastMemoryUpdate: %v", err)
	}
	if n != 0 {
		t.Errorf("got %d, want 0 (empty workspace)", n)
	}
}

func TestMemoryMetricsAdapter_EntriesSinceLastMemoryUpdate_CountsPostMemoryEntries(t *testing.T) {
	// Seed: 3 entries pre-memory.updated, then memory.updated, then 2
	// post entries. The window starts at the LAST memory.updated, so
	// the count should be exactly the 2 post entries (the memory.updated
	// row itself is excluded by `entry_type <> 'memory.updated'`).
	db := openTestDB(t)
	wsID := "ws_metrics_count"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', ?)`, wsID, "metcount"); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	now := time.Now().UTC()
	base := now.Add(-10 * time.Hour)
	seedJournalRowAtTime(t, db, wsID, "agent-1", "tool_call", base.Format(time.RFC3339))
	seedJournalRowAtTime(t, db, wsID, "agent-1", "llm_call", base.Add(time.Hour).Format(time.RFC3339))
	seedJournalRowAtTime(t, db, wsID, "agent-1", "agent.text", base.Add(2*time.Hour).Format(time.RFC3339))
	// The memory.updated marker — entries AFTER this define the count.
	seedJournalRowAtTime(t, db, wsID, "agent-1", "memory.updated", base.Add(3*time.Hour).Format(time.RFC3339))
	seedJournalRowAtTime(t, db, wsID, "agent-1", "tool_call", base.Add(4*time.Hour).Format(time.RFC3339))
	seedJournalRowAtTime(t, db, wsID, "agent-1", "agent.text", base.Add(5*time.Hour).Format(time.RFC3339))

	a := newMemoryMetricsAdapter(db)
	n, err := a.EntriesSinceLastMemoryUpdate(context.Background(), wsID, "agent-1")
	if err != nil {
		t.Fatalf("EntriesSinceLastMemoryUpdate: %v", err)
	}
	if n != 2 {
		t.Errorf("got %d, want 2 (only post-memory.updated entries; memory.updated row itself excluded)", n)
	}
}

func TestMemoryMetricsAdapter_EntriesSinceLastMemoryUpdate_NoMemoryUpdated_30DayWindow(t *testing.T) {
	// Source comment: "fallback to datetime('now','-30 days')" when no
	// memory.updated row exists. Entries within 30d count; entries
	// older than 30d don't.
	db := openTestDB(t)
	wsID := "ws_metrics_30d"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', ?)`, wsID, "met30d"); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	now := time.Now().UTC()
	// One inside the 30d window, one outside.
	seedJournalRowAtTime(t, db, wsID, "agent-2", "tool_call", now.Add(-1*time.Hour).Format(time.RFC3339))
	seedJournalRowAtTime(t, db, wsID, "agent-2", "tool_call", now.AddDate(0, 0, -45).Format(time.RFC3339))

	a := newMemoryMetricsAdapter(db)
	n, err := a.EntriesSinceLastMemoryUpdate(context.Background(), wsID, "agent-2")
	if err != nil {
		t.Fatalf("EntriesSinceLastMemoryUpdate: %v", err)
	}
	if n != 1 {
		t.Errorf("got %d, want 1 (the 45-day-old entry is outside the 30-day fallback window)", n)
	}
}

func TestMemoryMetricsAdapter_EntriesSinceLastMemoryUpdate_ScopedByAgent(t *testing.T) {
	// Two agents in the same workspace; the count must be agent-scoped.
	db := openTestDB(t)
	wsID := "ws_metrics_scope"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', ?)`, wsID, "metscope"); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	now := time.Now().UTC()
	seedJournalRowAtTime(t, db, wsID, "agent-A", "tool_call", now.Add(-1*time.Hour).Format(time.RFC3339))
	seedJournalRowAtTime(t, db, wsID, "agent-B", "tool_call", now.Add(-1*time.Hour).Format(time.RFC3339))
	seedJournalRowAtTime(t, db, wsID, "agent-B", "tool_call", now.Add(-2*time.Hour).Format(time.RFC3339))

	a := newMemoryMetricsAdapter(db)
	nA, _ := a.EntriesSinceLastMemoryUpdate(context.Background(), wsID, "agent-A")
	nB, _ := a.EntriesSinceLastMemoryUpdate(context.Background(), wsID, "agent-B")
	if nA != 1 {
		t.Errorf("agent-A count = %d, want 1", nA)
	}
	if nB != 2 {
		t.Errorf("agent-B count = %d, want 2", nB)
	}
}

// ---- AgentSpendLast24h ----

func seedCostLedgerRow(t *testing.T, db *sql.DB, id, wsID, agentID string, cost float64, inTok, outTok int64, ts time.Time) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO cost_ledger
		(id, workspace_id, agent_id, ts, provider, model, input_tokens, output_tokens, cost_usd)
		VALUES (?, ?, NULLIF(?, ''), ?, 'anthropic', 'claude', ?, ?, ?)`,
		id, wsID, agentID, ts.UTC().Format(time.RFC3339), inTok, outTok, cost); err != nil {
		t.Fatalf("seed cost_ledger: %v", err)
	}
}

func TestMemoryMetricsAdapter_AgentSpendLast24h_EmptyWorkspace(t *testing.T) {
	db := openTestDB(t)
	wsID := "ws_spend_empty"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', ?)`, wsID, "spend-e"); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	a := newMemoryMetricsAdapter(db)
	usd, tokens, calls, err := a.AgentSpendLast24h(context.Background(), wsID, "agent-1")
	if err != nil {
		t.Fatalf("AgentSpendLast24h: %v", err)
	}
	if usd != 0 || tokens != 0 || calls != 0 {
		t.Errorf("empty workspace = (%v, %d, %d), want all zero", usd, tokens, calls)
	}
}

func TestMemoryMetricsAdapter_AgentSpendLast24h_AggregatesAndWindowsCorrectly(t *testing.T) {
	db := openTestDB(t)
	wsID := "ws_spend_agg"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', ?)`, wsID, "spend-a"); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	now := time.Now().UTC()
	// In-window rows for agent-X
	seedCostLedgerRow(t, db, "c1", wsID, "agent-X", 1.25, 100, 50, now.Add(-1*time.Hour))
	seedCostLedgerRow(t, db, "c2", wsID, "agent-X", 2.50, 200, 100, now.Add(-12*time.Hour))
	// Out-of-window (>24h ago) — must NOT be counted
	seedCostLedgerRow(t, db, "c3", wsID, "agent-X", 99.99, 9999, 9999, now.Add(-48*time.Hour))
	// In-window but different agent — must NOT be counted
	seedCostLedgerRow(t, db, "c4", wsID, "agent-Y", 5.00, 500, 250, now.Add(-2*time.Hour))

	a := newMemoryMetricsAdapter(db)
	usd, tokens, calls, err := a.AgentSpendLast24h(context.Background(), wsID, "agent-X")
	if err != nil {
		t.Fatalf("AgentSpendLast24h: %v", err)
	}
	// Expected: c1 + c2 = $3.75; (100+50) + (200+100) = 450 tokens; 2 calls.
	if usd != 3.75 {
		t.Errorf("usd = %v, want 3.75", usd)
	}
	if tokens != 450 {
		t.Errorf("tokens = %d, want 450 (input+output summed)", tokens)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (out-of-window + foreign-agent excluded)", calls)
	}
}

func TestMemoryMetricsAdapter_AgentSpendLast24h_ScopedByWorkspace(t *testing.T) {
	// Two workspaces, both have an agent of the same id. Each workspace
	// must see only its own rows.
	db := openTestDB(t)
	for _, wsID := range []string{"ws_spend_iso_a", "ws_spend_iso_b"} {
		if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', ?)`, wsID, wsID); err != nil {
			t.Fatalf("seed %s: %v", wsID, err)
		}
	}
	now := time.Now().UTC()
	seedCostLedgerRow(t, db, "iso-1", "ws_spend_iso_a", "agent-shared", 1.0, 10, 5, now.Add(-1*time.Hour))
	seedCostLedgerRow(t, db, "iso-2", "ws_spend_iso_b", "agent-shared", 999.0, 9999, 9999, now.Add(-1*time.Hour))

	a := newMemoryMetricsAdapter(db)
	usdA, _, callsA, _ := a.AgentSpendLast24h(context.Background(), "ws_spend_iso_a", "agent-shared")
	if usdA != 1.0 || callsA != 1 {
		t.Errorf("workspace_a got (%v, %d), want (1.0, 1) — foreign workspace row leaked", usdA, callsA)
	}
	usdB, _, callsB, _ := a.AgentSpendLast24h(context.Background(), "ws_spend_iso_b", "agent-shared")
	if usdB != 999.0 || callsB != 1 {
		t.Errorf("workspace_b got (%v, %d), want (999.0, 1)", usdB, callsB)
	}
}
