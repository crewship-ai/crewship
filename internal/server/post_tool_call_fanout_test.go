package server

// post_tool_call_fanout_test.go — issue #2575.
//
// The sampled post_tool_call path discarded WARN and ESCALATE: the adapter
// only acted on a block-mode DENY, and nothing between
// behaviorhook.MaybeEvaluateEvery and the journal ever saw the verdict. The
// admin review-run endpoint (POST /api/v1/keeper/behavior) implemented the
// full fan-out, but live traffic never touched it.
//
// Every test here drives the REAL sampled entry — postToolCallObserver.Observe
// with a governance row switched on and behavior_sample_every=1, the real
// behaviorhook singleton, the real behavior evaluator — and asserts what the
// issue defines as correct: journal / inbox per the verdict's PolicyDecision
// (which encodes the workspace's behavior mode and the crew's autonomy),
// plus the keeper_requests audit row the endpoint path writes. The verdict
// comes from a canned llm.Provider; everything between it and the DB is
// production code.

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/behaviorhook"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/policy"
	"github.com/crewship-ai/crewship/internal/testutil"

	_ "modernc.org/sqlite"
)

// fanoutCannedProvider returns a fixed verdict from the judge seat.
type fanoutCannedProvider struct{ content string }

func (c *fanoutCannedProvider) Complete(_ context.Context, _ llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: c.content}, nil
}
func (c *fanoutCannedProvider) Stream(ctx context.Context, r llm.Request, h func(llm.StreamEvent) error) (*llm.Response, error) {
	resp, _ := c.Complete(ctx, r)
	_ = h(llm.StreamEvent{Type: "done", Response: resp})
	return resp, nil
}
func (c *fanoutCannedProvider) Name() string { return "fanout-canned" }

// allEntriesJournal captures every Emit — the sampled path can write more
// than one entry per observation (the verdict plus hook.blocked), so a
// last-entry recorder cannot assert the fan-out.
type allEntriesJournal struct {
	mu      sync.Mutex
	entries []journal.Entry
}

func (a *allEntriesJournal) Emit(_ context.Context, e journal.Entry) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return "je-" + string(e.Type), nil
}
func (a *allEntriesJournal) Flush(_ context.Context) error { return nil }

func (a *allEntriesJournal) of(t journal.EntryType) []journal.Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []journal.Entry
	for _, e := range a.entries {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// fanoutFixture wires the real sampled path — governance row (on, every
// call), the behaviorhook singleton, the real Phase-2 recorder, the observer
// — drives ONE tool-call observation through Observe, and hands back the
// journal and the DB to assert on.
func fanoutFixture(t *testing.T, autonomy, mode, judgeVerdict string) (*allEntriesJournal, *sql.DB) {
	t.Helper()

	d := testutil.MigratedDB(t)
	if _, err := d.DB.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, autonomy_level, behavior_mode)
		 VALUES ('cr1', 'ws1', 'Crew', 'cr1', ?, ?)`, autonomy, mode); err != nil {
		t.Fatal(err)
	}
	// The behavior audit row FKs requesting_agent_id → agents, and the
	// recorder's workspace resolution reads the agent — a real row, not a
	// free-form id.
	if _, err := d.DB.Exec(
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
		 VALUES ('agent-a', 'cr1', 'ws1', 'Agent A', 'agent-a')`); err != nil {
		t.Fatal(err)
	}
	if err := governance.Upsert(context.Background(), d.DB, "ws1", governance.Settings{
		Enabled:             true,
		DenyNotifyMinRisk:   governance.DefaultDenyNotifyMinRisk,
		BehaviorSampleEvery: 1, // sample EVERY call — the test wants a verdict per Observe
	}, ""); err != nil {
		t.Fatalf("governance upsert: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	provider := &fanoutCannedProvider{content: judgeVerdict}
	gk := gatekeeper.New(provider, "fanout-judge", logger)
	behaviorEval := gatekeeper.NewBehaviorEvaluator(gk, logger)

	// The REAL recorder — the same KeeperPhase2Handler the synchronous
	// POST /api/v1/keeper/behavior endpoint persists through.
	recorder := api.NewKeeperPhase2Handler(d.DB, "tok", policy.NewResolver(d.DB),
		nil, behaviorEval, nil, nil, logger)

	hook := behaviorhook.New(behaviorEval, policy.NewResolver(d.DB), logger)
	prev := behaviorhook.Get()
	behaviorhook.Set(hook)
	t.Cleanup(func() { behaviorhook.Set(prev) })

	jr := &allEntriesJournal{}
	obs := newPostToolCallObserver(logger, jr, d.DB).withRecorder(recorder)
	obs.Observe(orchestrator.ToolCallObservation{
		WorkspaceID: "ws1",
		CrewID:      "cr1",
		AgentID:     "agent-a",
		ToolName:    "shell_exec",
	})
	return jr, d.DB
}

func countInbox(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&n); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	return n
}

func inboxRow(t *testing.T, db *sql.DB) (blocking int, title string) {
	t.Helper()
	if err := db.QueryRow(`SELECT blocking, title FROM inbox_items LIMIT 1`).Scan(&blocking, &title); err != nil {
		t.Fatalf("read inbox row: %v", err)
	}
	return blocking, title
}

func behaviorDecision(t *testing.T, db *sql.DB) string {
	t.Helper()
	var decision string
	if err := db.QueryRow(`SELECT decision FROM keeper_requests WHERE request_type = 'behavior'`).Scan(&decision); err != nil {
		t.Fatalf("read behavior keeper_requests row: %v", err)
	}
	return decision
}

// The pin the issue asks for: "a test that a sampled ESCALATE produces an
// inbox item". Guided + warn mode: non-blocking operator review.
func TestSampledEscalate_ProducesInboxItem(t *testing.T) {
	jr, db := fanoutFixture(t, "guided", "warn",
		`{"decision":"ESCALATE","reason":"ambiguous tool sequence","risk":6}`)

	if n := countInbox(t, db); n != 1 {
		t.Fatalf("sampled ESCALATE produced %d inbox items, want 1 — the verdict was computed and dropped (#2575)", n)
	}
	blocking, _ := inboxRow(t, db)
	if blocking != 0 {
		t.Errorf("guided/warn ESCALATE inbox item is blocking=%d, want 0 (non-blocking review)", blocking)
	}
	if decision := behaviorDecision(t, db); decision != "ESCALATE" {
		t.Errorf("keeper_requests decision = %q, want ESCALATE", decision)
	}
	if entries := jr.of(journal.EntryKeeperDecision); len(entries) != 1 {
		t.Errorf("keeper.decision journal entries = %d, want 1", len(entries))
	}
}

// WARN is the verdict the old path dropped most silently: telemetry value,
// operator should see it, agent keeps running.
func TestSampledWarn_ProducesNonBlockingInboxItem(t *testing.T) {
	jr, db := fanoutFixture(t, "guided", "warn",
		`{"decision":"WARN","reason":"tight loop suspicion","risk":4}`)

	if n := countInbox(t, db); n != 1 {
		t.Fatalf("sampled WARN produced %d inbox items, want 1", n)
	}
	blocking, _ := inboxRow(t, db)
	if blocking != 0 {
		t.Errorf("WARN inbox item is blocking=%d, want 0 — WARN never interrupts", blocking)
	}
	if decision := behaviorDecision(t, db); decision != "WARN" {
		t.Errorf("keeper_requests decision = %q, want WARN", decision)
	}
	if entries := jr.of(journal.EntryKeeperDecision); len(entries) != 1 {
		t.Errorf("keeper.decision journal entries = %d, want 1", len(entries))
	}
}

// Block mode × strict × DENY: the one case the OLD path handled — it must
// keep its journal entry, and now also lands the inbox item the endpoint
// path always wrote for it.
func TestSampledDeny_BlockModeStrict_BlocksAndSurfaces(t *testing.T) {
	jr, db := fanoutFixture(t, "strict", "block",
		`{"decision":"DENY","reason":"destructive sequence","risk":9}`)

	if n := countInbox(t, db); n != 1 {
		t.Fatalf("sampled block-mode DENY produced %d inbox items, want 1", n)
	}
	blocking, _ := inboxRow(t, db)
	if blocking != 1 {
		t.Errorf("block-mode DENY inbox item is blocking=%d, want 1", blocking)
	}
	if entries := jr.of(journal.EntryHookBlocked); len(entries) != 1 {
		t.Errorf("hook.blocked journal entries = %d, want 1 — the pre-#2575 behaviour must survive", len(entries))
	}
	if decision := behaviorDecision(t, db); decision != "DENY" {
		t.Errorf("keeper_requests decision = %q, want DENY", decision)
	}
}

// ESCALATE in block mode × strict is the sharpest governance setting: the
// operator review must BLOCK the crew until resolved, same as the endpoint.
func TestSampledEscalate_BlockModeStrict_IsBlocking(t *testing.T) {
	jr, db := fanoutFixture(t, "strict", "block",
		`{"decision":"ESCALATE","reason":"ambiguous tool sequence","risk":6}`)

	if n := countInbox(t, db); n != 1 {
		t.Fatalf("sampled block-mode ESCALATE produced %d inbox items, want 1", n)
	}
	blocking, _ := inboxRow(t, db)
	if blocking != 1 {
		t.Errorf("block-mode strict ESCALATE inbox item is blocking=%d, want 1", blocking)
	}
	if entries := jr.of(journal.EntryHookBlocked); len(entries) != 1 {
		t.Errorf("hook.blocked journal entries = %d, want 1 — ESCALATE blocks here too", len(entries))
	}
}

// ALLOW: telemetry only. No inbox noise for a clean sample.
func TestSampledAllow_JournalOnly_NoInbox(t *testing.T) {
	jr, db := fanoutFixture(t, "guided", "warn",
		`{"decision":"ALLOW","reason":"looks normal","risk":1}`)

	if n := countInbox(t, db); n != 0 {
		t.Fatalf("a clean ALLOW sample produced %d inbox items, want 0", n)
	}
	if entries := jr.of(journal.EntryKeeperDecision); len(entries) != 1 {
		t.Errorf("keeper.decision journal entries = %d, want 1 (telemetry)", len(entries))
	}
	if decision := behaviorDecision(t, db); decision != "ALLOW" {
		t.Errorf("keeper_requests decision = %q, want ALLOW", decision)
	}
}

// Full autonomy: the evaluator's matrix degrades every non-blocking verdict
// to journal-only — the governance config, honoured on the sampled path too.
func TestSampledWarn_FullAutonomy_JournalOnly(t *testing.T) {
	jr, db := fanoutFixture(t, "full", "warn",
		`{"decision":"WARN","reason":"tight loop suspicion","risk":4}`)

	if n := countInbox(t, db); n != 0 {
		t.Fatalf("full-autonomy WARN produced %d inbox items, want 0 — the autonomy level says journal-only", n)
	}
	if entries := jr.of(journal.EntryKeeperDecision); len(entries) != 1 {
		t.Errorf("keeper.decision journal entries = %d, want 1", len(entries))
	}
}
