package paymaster

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"

	_ "modernc.org/sqlite"
)

// schemaSQL mirrors the cost_ledger / budget_limits tables. Kept inline so the
// test stays decoupled from the migrate package — same approach the journal
// package's tests use. The columns added in migration v60 (billing_mode,
// quota_*, rate_*, cost_confidence, subscription_plan) are reflected here.
const schemaSQL = `
CREATE TABLE cost_ledger (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT,
    agent_id TEXT,
    mission_id TEXT,
    ts TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cached_input_tokens INTEGER NOT NULL DEFAULT 0,
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    tags TEXT NOT NULL DEFAULT '{}',
    billing_mode TEXT NOT NULL DEFAULT 'metered' CHECK(billing_mode IN ('metered','flat_rate')),
    quota_remaining_pct REAL,
    quota_window TEXT,
    subscription_plan TEXT,
    rate_input_per_m REAL,
    rate_output_per_m REAL,
    rate_cached_in_per_m REAL,
    rate_cache_write_per_m REAL,
    cost_confidence TEXT NOT NULL DEFAULT 'estimate' CHECK(cost_confidence IN ('precise','estimate','unknown'))
);
CREATE INDEX idx_cost_ws_ts ON cost_ledger(workspace_id, ts DESC);
CREATE INDEX idx_cost_crew_ts ON cost_ledger(crew_id, ts DESC);
CREATE INDEX idx_cost_agent_ts ON cost_ledger(agent_id, ts DESC);
CREATE INDEX idx_cost_billing_mode ON cost_ledger(workspace_id, billing_mode, ts DESC) WHERE billing_mode = 'flat_rate';

CREATE TABLE budget_limits (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    scope_kind TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    window TEXT NOT NULL,
    limit_usd REAL NOT NULL,
    mode TEXT NOT NULL DEFAULT 'tiered',
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(workspace_id, scope_kind, scope_id, window)
);
CREATE INDEX idx_budget_scope ON budget_limits(scope_kind, scope_id, enabled);
`

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), schemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// fakeEmitter is the test stand-in for journal.Emitter. Records every entry
// in-memory so assertions can read them back without paying for the writer
// goroutine + flush ticker. Mutex because middleware tests run multiple
// goroutines through the same emitter (none of the existing tests do, but
// future ones almost certainly will).
type fakeEmitter struct {
	mu      sync.Mutex
	entries []journal.Entry
}

func (f *fakeEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e.ID == "" {
		e.ID = "j_test_fake"
	}
	f.entries = append(f.entries, e)
	return e.ID, nil
}

func (f *fakeEmitter) Flush(_ context.Context) error { return nil }

func (f *fakeEmitter) byType(t journal.EntryType) []journal.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []journal.Entry
	for _, e := range f.entries {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// TestEstimate covers the canonical pricing rows and the fallback paths. The
// numbers are computed from the rate card so that tightening prices in
// pricing.go produces a clean test diff (rather than the test failing in a
// way that hides the intent).
func TestEstimate(t *testing.T) {
	tests := []struct {
		name                           string
		provider, model                string
		in, out, cachedIn, cacheCreate int64
		want                           float64
	}{
		{
			name:     "opus 1k in / 1k out",
			provider: "anthropic", model: "claude-opus-4-7",
			in: 1000, out: 1000,
			// 2026-04 reprice: $5 input / $25 output per 1M.
			// 1000 * 5/1e6 + 1000 * 25/1e6 = 0.005 + 0.025
			want: 0.005 + 0.025,
		},
		{
			name:     "sonnet with cached input",
			provider: "anthropic", model: "claude-sonnet-4-6",
			in: 1000, out: 500, cachedIn: 10000,
			// 1000 * 3/1e6 + 500 * 15/1e6 + 10000 * 0.30/1e6
			want: 0.003 + 0.0075 + 0.003,
		},
		{
			name:     "haiku tiny call",
			provider: "anthropic", model: "claude-haiku-4-5",
			in: 100, out: 50,
			// 2026-04 reprice: $1 input / $5 output per 1M.
			// 100 * 1/1e6 + 50 * 5/1e6
			want: 0.0001 + 0.00025,
		},
		{
			name:     "ollama free",
			provider: "ollama", model: "llama3:70b",
			in: 1_000_000, out: 1_000_000,
			want: 0,
		},
		{
			name:     "openai gpt-5 alias maps to 5.5 rate",
			provider: "openai", model: "gpt-5",
			in: 1000, out: 500,
			// gpt-5 alias resolves to gpt-5.5 rate: $4 in / $24 out per 1M.
			// 1000 * 4/1e6 + 500 * 24/1e6
			want: 0.004 + 0.012,
		},
		{
			name:     "openai gpt-5.5 flagship",
			provider: "openai", model: "gpt-5.5",
			in: 1000, out: 500,
			want: 0.004 + 0.012,
		},
		{
			name:     "gemini 2.5 pro",
			provider: "google", model: "gemini-2.5-pro",
			in: 1000, out: 500,
			// 1000 * 2.50/1e6 + 500 * 15/1e6 (upper tier)
			want: 0.0025 + 0.0075,
		},
		{
			name:     "grok 4.20",
			provider: "xai", model: "grok-4.20",
			in: 1000, out: 500,
			// 1000 * 2/1e6 + 500 * 6/1e6
			want: 0.002 + 0.003,
		},
		{
			name:     "deepseek chat",
			provider: "deepseek", model: "deepseek-chat",
			in: 1_000_000, out: 1_000_000,
			// $0.252 in + $0.378 out
			want: 0.252 + 0.378,
		},
		{
			name:     "unknown provider returns zero",
			provider: "wholly-unknown-vendor", model: "x",
			in: 1000, out: 1000,
			want: 0,
		},
		{
			name:     "anthropic fallback for unknown model",
			provider: "anthropic", model: "claude-future-99",
			in: 1000, out: 500,
			// Conservative fallback: Opus-tier ($5/$25 per 1M).
			// 1000 * 5/1e6 + 500 * 25/1e6 = 0.005 + 0.0125
			want: 0.005 + 0.0125,
		},
		{
			name:     "google fallback for unknown model",
			provider: "google", model: "gemini-future-99",
			in: 1000, out: 500,
			// fallback equals gemini-2.5-pro rate (upper tier)
			want: 0.0025 + 0.0075,
		},
		{
			name:     "openai fallback for unknown model",
			provider: "openai", model: "gpt-future-99",
			in: 1000, out: 500,
			// Conservative fallback: o3-pro tier ($20/$80 per 1M).
			want: 0.02 + 0.04,
		},
		{
			name:     "deepseek fallback for unknown model",
			provider: "deepseek", model: "deepseek-future-99",
			in: 1000, out: 500,
			// Conservative fallback: reasoner tier ($0.70/$2.50 per 1M).
			want: 0.0007 + 0.00125,
		},
		{
			name:     "negative tokens treated as zero",
			provider: "anthropic", model: "claude-opus-4-7",
			in: -50, out: 100,
			// only output tokens count, $25/M
			want: 100 * 25 / 1_000_000.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Estimate(tc.provider, tc.model, tc.in, tc.out, tc.cachedIn, tc.cacheCreate)
			if !nearly(got, tc.want, 1e-9) {
				t.Errorf("Estimate=%.10f want=%.10f", got, tc.want)
			}
		})
	}
}

// TestRecordRoundtrip writes a Call through Record and then queries the row
// back to verify every column persists, plus checks the journal emitter saw
// llm.call + cost.incurred. Zero-cost calls should NOT produce cost.incurred.
func TestRecordRoundtrip(t *testing.T) {
	db := openTestDB(t)
	em := &fakeEmitter{}
	ctx := context.Background()

	rec, err := Record(ctx, db, em, Call{
		Scope: Scope{
			WorkspaceID: "ws1",
			CrewID:      "crew1",
			AgentID:     "agent1",
			MissionID:   "mission1",
		},
		Provider:            "anthropic",
		Model:               "claude-sonnet-4-6",
		InputTokens:         1000,
		OutputTokens:        500,
		CachedInputTokens:   200,
		CacheCreationTokens: 50,
		CostUSD:             0.025,
		Tags:                map[string]any{"feature": "summary"},
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if rec.ID == "" {
		t.Fatal("expected ledger id")
	}

	// SQL roundtrip.
	var (
		gotProvider, gotModel  string
		gotIn, gotOut          int64
		gotCachedIn, gotCacheC int64
		gotCost                float64
		gotTags                string
	)
	err = db.QueryRowContext(ctx,
		`SELECT provider, model, input_tokens, output_tokens, cached_input_tokens,
		        cache_creation_tokens, cost_usd, tags FROM cost_ledger WHERE id = ?`, rec.ID).
		Scan(&gotProvider, &gotModel, &gotIn, &gotOut, &gotCachedIn, &gotCacheC, &gotCost, &gotTags)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if gotProvider != "anthropic" || gotModel != "claude-sonnet-4-6" {
		t.Errorf("provider/model roundtrip wrong: %s/%s", gotProvider, gotModel)
	}
	if gotIn != 1000 || gotOut != 500 || gotCachedIn != 200 || gotCacheC != 50 {
		t.Errorf("token counts wrong: in=%d out=%d cached=%d cacheC=%d", gotIn, gotOut, gotCachedIn, gotCacheC)
	}
	if !nearly(gotCost, 0.025, 1e-9) {
		t.Errorf("cost roundtrip: %v", gotCost)
	}
	if gotTags != `{"feature":"summary"}` {
		t.Errorf("tags roundtrip: %q", gotTags)
	}

	// Journal: one llm.call + one cost.incurred (cost > 0).
	if got := len(em.byType(journal.EntryLLMCall)); got != 1 {
		t.Errorf("expected 1 llm.call entry, got %d", got)
	}
	if got := len(em.byType(journal.EntryCostIncurred)); got != 1 {
		t.Errorf("expected 1 cost.incurred entry, got %d", got)
	}
}

func TestRecordZeroCostSkipsIncurred(t *testing.T) {
	db := openTestDB(t)
	em := &fakeEmitter{}
	ctx := context.Background()

	_, err := Record(ctx, db, em, Call{
		Scope:    Scope{WorkspaceID: "ws1", CrewID: "crew1"},
		Provider: "ollama",
		Model:    "llama3:70b",
		// CostUSD: 0 (omitted)
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got := len(em.byType(journal.EntryLLMCall)); got != 1 {
		t.Errorf("expected 1 llm.call entry, got %d", got)
	}
	if got := len(em.byType(journal.EntryCostIncurred)); got != 0 {
		t.Errorf("expected 0 cost.incurred for free call, got %d", got)
	}
}

// TestEnforceHardModeBlocks pre-loads spend that exceeds a hard-mode budget
// then asserts Enforce returns a *BudgetExceededError and emits the
// budget.exceeded journal entry. The agent is expected to NOT make any call
// when this error fires.
func TestEnforceHardModeBlocks(t *testing.T) {
	db := openTestDB(t)
	em := &fakeEmitter{}
	ctx := context.Background()

	// Workspace budget: $1.00 per day, hard mode.
	mustExec(t, db, `INSERT INTO budget_limits (id, workspace_id, scope_kind, scope_id, window, limit_usd, mode)
	                 VALUES ('b1', 'ws1', 'workspace', 'ws1', 'day', 1.0, 'hard')`)

	// Pre-load $1.50 of spend in the current day window.
	now := time.Now().UTC().Format(tsLayout)
	mustExec(t, db, `INSERT INTO cost_ledger (id, workspace_id, ts, provider, model, cost_usd)
	                 VALUES ('c1', 'ws1', ?, 'anthropic', 'claude-opus-4-7', 1.50)`, now)

	err := Enforce(ctx, db, em, Scope{WorkspaceID: "ws1"})
	if err == nil {
		t.Fatal("expected BudgetExceededError, got nil")
	}
	var bx *BudgetExceededError
	if !errors.As(err, &bx) {
		t.Fatalf("expected *BudgetExceededError, got %T: %v", err, err)
	}
	if len(bx.Statuses) != 1 || bx.Statuses[0].State != StateExceeded {
		t.Fatalf("unexpected statuses: %+v", bx.Statuses)
	}

	if got := len(em.byType(journal.EntryBudgetExceed)); got != 1 {
		t.Errorf("expected 1 budget.exceeded entry, got %d", got)
	}
}

// TestEnforceSoftModeWarns confirms soft budgets never block — they only
// emit a budget.warning entry — even when spend is over the limit.
func TestEnforceSoftModeWarns(t *testing.T) {
	db := openTestDB(t)
	em := &fakeEmitter{}
	ctx := context.Background()

	mustExec(t, db, `INSERT INTO budget_limits (id, workspace_id, scope_kind, scope_id, window, limit_usd, mode)
	                 VALUES ('b1', 'ws1', 'workspace', 'ws1', 'day', 1.0, 'soft')`)
	now := time.Now().UTC().Format(tsLayout)
	mustExec(t, db, `INSERT INTO cost_ledger (id, workspace_id, ts, provider, model, cost_usd)
	                 VALUES ('c1', 'ws1', ?, 'anthropic', 'claude-opus-4-7', 2.0)`, now)

	if err := Enforce(ctx, db, em, Scope{WorkspaceID: "ws1"}); err != nil {
		t.Fatalf("soft mode should not block: %v", err)
	}
	if got := len(em.byType(journal.EntryBudgetWarning)); got != 1 {
		t.Errorf("expected 1 warning entry for soft over-limit, got %d", got)
	}
	if got := len(em.byType(journal.EntryBudgetExceed)); got != 0 {
		t.Errorf("soft mode must not emit budget.exceeded, got %d", got)
	}
}

// TestEnforceTieredWarnAt80 confirms tiered budgets emit budget.warning
// (not budget.exceeded) when spend crosses 80% but stays under 100%.
func TestEnforceTieredWarnAt80(t *testing.T) {
	db := openTestDB(t)
	em := &fakeEmitter{}
	ctx := context.Background()

	mustExec(t, db, `INSERT INTO budget_limits (id, workspace_id, scope_kind, scope_id, window, limit_usd, mode)
	                 VALUES ('b1', 'ws1', 'workspace', 'ws1', 'day', 1.0, 'tiered')`)
	now := time.Now().UTC().Format(tsLayout)
	// 85% of $1.00.
	mustExec(t, db, `INSERT INTO cost_ledger (id, workspace_id, ts, provider, model, cost_usd)
	                 VALUES ('c1', 'ws1', ?, 'anthropic', 'claude-opus-4-7', 0.85)`, now)

	if err := Enforce(ctx, db, em, Scope{WorkspaceID: "ws1"}); err != nil {
		t.Fatalf("tiered at 85%% should not block: %v", err)
	}
	if got := len(em.byType(journal.EntryBudgetWarning)); got != 1 {
		t.Errorf("expected 1 warning, got %d", got)
	}
	if got := len(em.byType(journal.EntryBudgetExceed)); got != 0 {
		t.Errorf("expected 0 exceeded entries at 85%%, got %d", got)
	}
}

// TestCheckHierarchy loads budgets at workspace + crew + agent levels and
// asserts Check returns all three with correct utilization. This exercises
// the loadApplicableBudgets union query — easy to break that one with a
// wrong placeholder count.
func TestCheckHierarchy(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Three budgets at three scopes.
	mustExec(t, db, `INSERT INTO budget_limits (id, workspace_id, scope_kind, scope_id, window, limit_usd, mode) VALUES
	                ('bw', 'ws1', 'workspace', 'ws1',    'day', 10.0, 'tiered'),
	                ('bc', 'ws1', 'crew',      'crew1',  'day',  5.0, 'tiered'),
	                ('ba', 'ws1', 'agent',     'agent1', 'day',  2.0, 'tiered')`)

	now := time.Now().UTC().Format(tsLayout)
	mustExec(t, db, `INSERT INTO cost_ledger (id, workspace_id, crew_id, agent_id, ts, provider, model, cost_usd) VALUES
	                ('c1', 'ws1', 'crew1', 'agent1', ?, 'anthropic', 'claude-opus-4-7', 1.0),
	                ('c2', 'ws1', 'crew1', 'agent2', ?, 'anthropic', 'claude-opus-4-7', 0.5)`, now, now)

	statuses, err := Check(ctx, db, Scope{
		WorkspaceID: "ws1", CrewID: "crew1", AgentID: "agent1",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(statuses) != 3 {
		t.Fatalf("expected 3 budgets, got %d", len(statuses))
	}

	// workspace sees all spend across the workspace = 1.5
	// crew sees the crew's spend = 1.5
	// agent sees only agent1's spend = 1.0
	gotByScope := map[ScopeKind]float64{}
	for _, s := range statuses {
		gotByScope[s.Budget.ScopeKind] = s.SpentUSD
	}
	if !nearly(gotByScope[ScopeWorkspace], 1.5, 1e-9) {
		t.Errorf("workspace spent: got %v want 1.5", gotByScope[ScopeWorkspace])
	}
	if !nearly(gotByScope[ScopeCrew], 1.5, 1e-9) {
		t.Errorf("crew spent: got %v want 1.5", gotByScope[ScopeCrew])
	}
	if !nearly(gotByScope[ScopeAgent], 1.0, 1e-9) {
		t.Errorf("agent spent: got %v want 1.0", gotByScope[ScopeAgent])
	}
}

// TestRollupAggregation seeds rows for two crews, two agents, one mission,
// then asserts each rollup helper returns the right totals.
func TestRollupAggregation(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Format(tsLayout)
	mustExec(t, db, `INSERT INTO cost_ledger
	                (id, workspace_id, crew_id, agent_id, mission_id, ts, provider, model, input_tokens, output_tokens, cost_usd) VALUES
	                ('r1', 'ws1', 'crewA', 'agentA1', 'm1', ?, 'anthropic', 'claude-opus-4-7', 100, 50, 1.0),
	                ('r2', 'ws1', 'crewA', 'agentA2', 'm1', ?, 'anthropic', 'claude-opus-4-7', 200, 75, 2.0),
	                ('r3', 'ws1', 'crewB', 'agentB1', 'm2', ?, 'anthropic', 'claude-opus-4-7', 300, 100, 0.5),
	                ('r4', 'ws1', 'crewB', 'agentB1', 'm2', ?, 'anthropic', 'claude-opus-4-7', 400, 200, 1.5)`,
		now, now, now, now)

	// SpendByCrew: crewA=$3, crewB=$2; ordered by cost DESC.
	crewSpend, err := SpendByCrew(ctx, db, "ws1", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("SpendByCrew: %v", err)
	}
	if len(crewSpend) != 2 {
		t.Fatalf("expected 2 crews, got %d", len(crewSpend))
	}
	if crewSpend[0].CrewID != "crewA" || !nearly(crewSpend[0].CostUSD, 3.0, 1e-9) {
		t.Errorf("crewA wrong: %+v", crewSpend[0])
	}
	if crewSpend[1].CrewID != "crewB" || !nearly(crewSpend[1].CostUSD, 2.0, 1e-9) {
		t.Errorf("crewB wrong: %+v", crewSpend[1])
	}

	// SpendByAgent for crewB: agentB1 contributed both rows = $2 total.
	agentSpend, err := SpendByAgent(ctx, db, "crewB", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("SpendByAgent: %v", err)
	}
	if len(agentSpend) != 1 || agentSpend[0].AgentID != "agentB1" || !nearly(agentSpend[0].CostUSD, 2.0, 1e-9) {
		t.Errorf("agentB1 wrong: %+v", agentSpend)
	}
	if agentSpend[0].CallCount != 2 {
		t.Errorf("agentB1 call count: got %d want 2", agentSpend[0].CallCount)
	}

	// SpendByMission m1 = $3 total.
	missionSpend, err := SpendByMission(ctx, db, "m1")
	if err != nil {
		t.Fatalf("SpendByMission: %v", err)
	}
	if !nearly(missionSpend.CostUSD, 3.0, 1e-9) || missionSpend.CallCount != 2 {
		t.Errorf("m1 spend wrong: %+v", missionSpend)
	}

	// TopSpenders: agentA2 is highest at $2, then agentB1 at $2 (tied), then agentA1 at $1.
	top, err := TopSpenders(ctx, db, "ws1", 5, time.Time{})
	if err != nil {
		t.Fatalf("TopSpenders: %v", err)
	}
	if len(top) != 3 {
		t.Fatalf("expected 3 spenders, got %d", len(top))
	}
	if top[0].CostUSD < top[1].CostUSD || top[1].CostUSD < top[2].CostUSD {
		t.Errorf("TopSpenders not in DESC order: %+v", top)
	}
}

// TestMiddlewareSuccess wires up Middleware around a fake LLMCaller and
// verifies a successful call writes a ledger row and emits journal entries.
// Token counts come from CallResponse; cost is filled by Estimate because
// the response leaves it zero.
func TestMiddlewareSuccess(t *testing.T) {
	db := openTestDB(t)
	em := &fakeEmitter{}
	ctx := context.Background()

	inner := CallerFunc(func(_ context.Context, req CallRequest) (CallResponse, error) {
		return CallResponse{
			InputTokens:  1000,
			OutputTokens: 500,
		}, nil
	})

	mw := Middleware(inner, em, db)
	resp, err := mw.Call(ctx, CallRequest{
		Scope:    Scope{WorkspaceID: "ws1", CrewID: "crew1"},
		Provider: "anthropic",
		Model:    "claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.InputTokens != 1000 {
		t.Errorf("response not passed through: %+v", resp)
	}

	// Ledger row written?
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cost_ledger`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 ledger row, got %d", n)
	}
}

// TestMiddlewareBlocksOnHardBudget exercises the pre-call enforcement: if a
// hard budget is breached, the inner caller must NOT be invoked.
func TestMiddlewareBlocksOnHardBudget(t *testing.T) {
	db := openTestDB(t)
	em := &fakeEmitter{}
	ctx := context.Background()

	mustExec(t, db, `INSERT INTO budget_limits (id, workspace_id, scope_kind, scope_id, window, limit_usd, mode)
	                 VALUES ('b1', 'ws1', 'workspace', 'ws1', 'day', 1.0, 'hard')`)
	now := time.Now().UTC().Format(tsLayout)
	mustExec(t, db, `INSERT INTO cost_ledger (id, workspace_id, ts, provider, model, cost_usd)
	                 VALUES ('c1', 'ws1', ?, 'anthropic', 'claude-opus-4-7', 1.50)`, now)

	called := false
	inner := CallerFunc(func(_ context.Context, _ CallRequest) (CallResponse, error) {
		called = true
		return CallResponse{}, nil
	})

	mw := Middleware(inner, em, db)
	_, err := mw.Call(ctx, CallRequest{
		Scope:    Scope{WorkspaceID: "ws1"},
		Provider: "anthropic",
		Model:    "claude-opus-4-7",
	})
	if err == nil {
		t.Fatal("expected enforcement error")
	}
	var bx *BudgetExceededError
	if !errors.As(err, &bx) {
		t.Errorf("want BudgetExceededError, got %T", err)
	}
	if called {
		t.Error("inner caller was invoked despite hard budget block")
	}
}

// TestRecordFlatRateNullsCost asserts the v60 flat-rate invariant: even if
// the caller hands in a non-zero CostUSD, the row lands with cost=0 and
// confidence=unknown. Pricing for subscription calls is structurally
// nonsense, so we never persist a number that would imply otherwise.
func TestRecordFlatRateNullsCost(t *testing.T) {
	db := openTestDB(t)
	em := &fakeEmitter{}
	ctx := context.Background()

	rec, err := Record(ctx, db, em, Call{
		Scope:            Scope{WorkspaceID: "ws1", CrewID: "crew1"},
		Provider:         "anthropic",
		Model:            "claude-opus-4-7",
		InputTokens:      1000,
		OutputTokens:     500,
		CostUSD:          99.99, // would be ignored
		BillingMode:      BillingFlatRate,
		SubscriptionPlan: "Anthropic Max",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	var (
		cost       float64
		mode       string
		confidence string
		plan       sql.NullString
	)
	err = db.QueryRowContext(ctx,
		`SELECT cost_usd, billing_mode, cost_confidence, subscription_plan
		 FROM cost_ledger WHERE id = ?`, rec.ID).
		Scan(&cost, &mode, &confidence, &plan)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if cost != 0 {
		t.Errorf("flat-rate cost should be 0, got %v", cost)
	}
	if mode != "flat_rate" {
		t.Errorf("billing_mode: %q want flat_rate", mode)
	}
	if confidence != "unknown" {
		t.Errorf("confidence: %q want unknown", confidence)
	}
	if !plan.Valid || plan.String != "Anthropic Max" {
		t.Errorf("plan: %+v want Anthropic Max", plan)
	}

	// Flat-rate must NOT fire cost.incurred — no $ was incurred.
	if got := len(em.byType(journal.EntryCostIncurred)); got != 0 {
		t.Errorf("flat-rate should not emit cost.incurred, got %d", got)
	}
	if got := len(em.byType(journal.EntryLLMCall)); got != 1 {
		t.Errorf("expected 1 llm.call entry, got %d", got)
	}
}

// TestRecordMeteredSnapshotsRateCard is the Langfuse-pattern check: the
// v60 ratecard columns are populated at write time so a future pricing.go
// change can't retroactively rewrite history.
func TestRecordMeteredSnapshotsRateCard(t *testing.T) {
	db := openTestDB(t)
	em := &fakeEmitter{}
	ctx := context.Background()

	rec, err := Record(ctx, db, em, Call{
		Scope:        Scope{WorkspaceID: "ws1"},
		Provider:     "anthropic",
		Model:        "claude-sonnet-4-6",
		InputTokens:  100,
		OutputTokens: 200,
		CostUSD:      0.01,
		BillingMode:  BillingMetered,
		Confidence:   ConfidencePrecise,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	var inRate, outRate sql.NullFloat64
	err = db.QueryRowContext(ctx,
		`SELECT rate_input_per_m, rate_output_per_m FROM cost_ledger WHERE id = ?`, rec.ID).
		Scan(&inRate, &outRate)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !inRate.Valid || !nearly(inRate.Float64, 3.00, 1e-9) {
		t.Errorf("rate_input_per_m: %+v want 3.00", inRate)
	}
	if !outRate.Valid || !nearly(outRate.Float64, 15.00, 1e-9) {
		t.Errorf("rate_output_per_m: %+v want 15.00", outRate)
	}
}

// TestEnforceQuotaWarnAtLowRemaining asserts a budget.warning fires when
// remaining quota drops below 20% — without blocking. Mirrors the soft /
// tiered $-budget warn behaviour for the rate-limit-header signal.
func TestEnforceQuotaWarnAtLowRemaining(t *testing.T) {
	em := &fakeEmitter{}
	ctx := context.Background()

	if err := EnforceQuota(ctx, em, Scope{WorkspaceID: "ws1"}, 0.10, QuotaTokensPerMin, false); err != nil {
		t.Fatalf("low remaining should not block: %v", err)
	}
	if got := len(em.byType(journal.EntryBudgetWarning)); got != 1 {
		t.Errorf("expected 1 warning, got %d", got)
	}
}

// TestEnforceQuota429Blocks asserts the upstream's authoritative "you're
// out" signal returns BudgetExceededError so the caller can surface a
// quota_exhausted error consistently with the $-budget enforcement path.
func TestEnforceQuota429Blocks(t *testing.T) {
	em := &fakeEmitter{}
	ctx := context.Background()

	err := EnforceQuota(ctx, em, Scope{WorkspaceID: "ws1"}, 0, QuotaTokensPerMin, true)
	if err == nil {
		t.Fatal("expected BudgetExceededError on 429")
	}
	var bx *BudgetExceededError
	if !errors.As(err, &bx) {
		t.Errorf("want BudgetExceededError, got %T", err)
	}
	if got := len(em.byType(journal.EntryBudgetExceed)); got != 1 {
		t.Errorf("expected 1 budget.exceeded entry, got %d", got)
	}
}

// TestEnforceQuotaPlentyOfRoom is the no-op path: above 20% remaining, no
// signal, no error. The journal stays clean and the proxy hot path
// doesn't pay for an unnecessary entry.
func TestEnforceQuotaPlentyOfRoom(t *testing.T) {
	em := &fakeEmitter{}
	ctx := context.Background()
	if err := EnforceQuota(ctx, em, Scope{WorkspaceID: "ws1"}, 0.85, QuotaTokensPerMin, false); err != nil {
		t.Fatalf("85%% remaining should be no-op: %v", err)
	}
	if got := len(em.byType(journal.EntryBudgetWarning)) + len(em.byType(journal.EntryBudgetExceed)); got != 0 {
		t.Errorf("plenty-of-room path should not emit, got %d entries", got)
	}
}

// TestSubscriptionUsageByPlan exercises the new flat-rate rollup helper.
// Mixed metered + flat-rate ledger rows are seeded; the rollup must return
// only flat-rate aggregates grouped by (plan, provider).
func TestSubscriptionUsageByPlan(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(tsLayout)

	// 2 flat-rate Anthropic Max rows + 1 metered row + 1 different plan row.
	mustExec(t, db, `INSERT INTO cost_ledger
	    (id, workspace_id, ts, provider, model, input_tokens, output_tokens,
	     cost_usd, billing_mode, subscription_plan, cost_confidence) VALUES
	    ('s1', 'ws1', ?, 'anthropic', 'claude-opus-4-7', 100, 200, 0, 'flat_rate', 'Anthropic Max', 'unknown'),
	    ('s2', 'ws1', ?, 'anthropic', 'claude-opus-4-7', 50,  100, 0, 'flat_rate', 'Anthropic Max', 'unknown'),
	    ('s3', 'ws1', ?, 'anthropic', 'claude-opus-4-7', 10,  20,  0.05, 'metered',  NULL, 'precise'),
	    ('s4', 'ws1', ?, 'openai',    'gpt-5.5',         30,  40,  0, 'flat_rate', 'Cursor Ultra', 'unknown')`,
		now, now, now, now)

	rows, err := SubscriptionUsageByPlan(ctx, db, "ws1", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("SubscriptionUsageByPlan: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 plan-provider groups, got %d: %+v", len(rows), rows)
	}

	gotByPlan := map[string]SubscriptionUsage{}
	for _, r := range rows {
		gotByPlan[r.SubscriptionPlan] = r
	}
	max := gotByPlan["Anthropic Max"]
	if max.CallCount != 2 || max.InTokens != 150 || max.OutTokens != 300 {
		t.Errorf("Anthropic Max rollup wrong: %+v", max)
	}
	cursor := gotByPlan["Cursor Ultra"]
	if cursor.CallCount != 1 || cursor.Provider != "openai" {
		t.Errorf("Cursor Ultra row wrong: %+v", cursor)
	}
}

// TestDeriveState locks the state-machine boundaries so any future change to
// the warn threshold triggers an explicit test update.
func TestDeriveState(t *testing.T) {
	tests := []struct {
		spent, limit float64
		mode         EnforcementMode
		want         BudgetState
	}{
		{0.5, 1.0, ModeTiered, StateOK},
		{0.79, 1.0, ModeTiered, StateOK},
		{0.80, 1.0, ModeTiered, StateWarn},
		{0.99, 1.0, ModeTiered, StateWarn},
		{1.00, 1.0, ModeTiered, StateExceeded},
		{1.50, 1.0, ModeTiered, StateExceeded},
		// Hard mode skips the warn band.
		{0.95, 1.0, ModeHard, StateOK},
		{1.00, 1.0, ModeHard, StateExceeded},
		// Soft mode warns + reports exceeded.
		{0.85, 1.0, ModeSoft, StateWarn},
		{1.00, 1.0, ModeSoft, StateExceeded},
		// Zero limit short-circuits to ok.
		{5.0, 0, ModeHard, StateOK},
	}
	for _, tc := range tests {
		got := deriveState(tc.spent, tc.limit, tc.mode)
		if got != tc.want {
			t.Errorf("deriveState(%v, %v, %s)=%s want %s", tc.spent, tc.limit, tc.mode, got, tc.want)
		}
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// nearly is a small float comparator for the cost math. Direct == on float64
// is fine for most of these because the math is small and exact, but a few
// cases (cache pricing) do enough divisions to accumulate ULP-level noise.
func nearly(a, b, eps float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= eps
}
