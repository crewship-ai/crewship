package paymaster

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// TestDeriveState_Boundaries fills in the cracks the existing
// TestDeriveState left: exact-100% with each mode, just-below-warn,
// negative spend (clamped to OK by definition since limit<=0 is the
// only OK shortcut), exact warn boundary.
func TestDeriveState_Boundaries(t *testing.T) {
	tests := []struct {
		name         string
		spent, limit float64
		mode         EnforcementMode
		want         BudgetState
	}{
		{"exact 80% tiered → warn", 0.80, 1.0, ModeTiered, StateWarn},
		{"just below 80% tiered → ok", 0.7999, 1.0, ModeTiered, StateOK},
		{"exact 100.0001% hard → exceeded", 1.000_001, 1.0, ModeHard, StateExceeded},
		{"floating-point 99.999...% hard → ok", 0.999_999, 1.0, ModeHard, StateOK},
		{"zero spent any mode → ok", 0, 1.0, ModeHard, StateOK},
		{"negative limit → ok shortcut", 5.0, -1.0, ModeHard, StateOK},
		{"hard mode below warn", 0.5, 1.0, ModeHard, StateOK},
		{"hard mode at 100% → exceeded", 1.0, 1.0, ModeHard, StateExceeded},
		{"tiered mode 80% exact → warn", 0.80, 1.0, ModeTiered, StateWarn},
		{"soft mode at 80% → warn", 0.80, 1.0, ModeSoft, StateWarn},
		{"soft mode under 80% → ok", 0.50, 1.0, ModeSoft, StateOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveState(tt.spent, tt.limit, tt.mode); got != tt.want {
				t.Errorf("deriveState(%v, %v, %s) = %s, want %s",
					tt.spent, tt.limit, tt.mode, got, tt.want)
			}
		})
	}
}

// TestLookupPrice_Direct exercises the resolution chain explicitly:
// 1) exact key, 2) provider/* wildcard, 3) provider fallback, 4) zero.
func TestLookupPrice_Direct(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		model          string
		wantInputPerM  float64
		wantOutputPerM float64
	}{
		{"exact match opus", "anthropic", "claude-opus-4-7", 5.0, 25.0},
		{"case-insensitive provider", "Anthropic", "claude-opus-4-7", 5.0, 25.0},
		{"case-insensitive model", "anthropic", "Claude-Opus-4-7", 5.0, 25.0},
		{"trim whitespace", "  anthropic  ", "  claude-opus-4-7  ", 5.0, 25.0},
		{"ollama wildcard", "ollama", "llama3:70b", 0, 0},
		{"local wildcard", "local", "anything", 0, 0},
		{"anthropic fallback", "anthropic", "totally-new-model", 5.0, 25.0},
		{"openai fallback", "openai", "totally-new-model", 20.0, 80.0},
		{"google fallback", "google", "totally-new-model", 2.5, 15.0},
		{"unknown provider → zeros", "unknown-vendor", "some-model", 0, 0},
		{"empty provider → zeros", "", "claude-opus-4-7", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lookupPrice(tt.provider, tt.model)
			if got.InputPerM != tt.wantInputPerM {
				t.Errorf("InputPerM = %v, want %v", got.InputPerM, tt.wantInputPerM)
			}
			if got.OutputPerM != tt.wantOutputPerM {
				t.Errorf("OutputPerM = %v, want %v", got.OutputPerM, tt.wantOutputPerM)
			}
		})
	}
}

// TestRateCard_IsLookupPrice locks the contract: RateCard is the public
// face of lookupPrice. A regression that diverged would silently corrupt
// the snapshotted rate columns on cost_ledger rows.
func TestRateCard_IsLookupPrice(t *testing.T) {
	rc := RateCard("anthropic", "claude-sonnet-4-6")
	if rc.InputPerM != 3.0 || rc.OutputPerM != 15.0 {
		t.Errorf("RateCard sonnet: %+v", rc)
	}
	rc = RateCard("ollama", "llama3:70b")
	if rc.InputPerM != 0 {
		t.Errorf("RateCard ollama wildcard not zero: %+v", rc)
	}
}

// TestEstimate_NegativeAllChannels — paranoia coverage that every channel
// individually clamps. A regression in any one would let a glitched usage
// block produce a credit on the ledger.
func TestEstimate_NegativeAllChannels(t *testing.T) {
	got := Estimate("anthropic", "claude-opus-4-7", -1, -1, -1, -1)
	if got != 0 {
		t.Errorf("all-negative tokens should produce 0, got %v", got)
	}
}

// TestEstimate_OnlyCachedInput verifies cached input is priced at the
// CachedInputPerM rate (~10x cheaper than fresh).
func TestEstimate_OnlyCachedInput(t *testing.T) {
	// claude-sonnet-4-6: CachedInputPerM=0.30
	got := Estimate("anthropic", "claude-sonnet-4-6", 0, 0, 1_000_000, 0)
	if !nearly(got, 0.30, 1e-9) {
		t.Errorf("cached-only sonnet: got %v want 0.30", got)
	}
}

// TestEstimate_OnlyCacheCreate verifies cache creation is priced at
// CacheWritePerM (slightly more than fresh input).
func TestEstimate_OnlyCacheCreate(t *testing.T) {
	// claude-opus-4-7: CacheWritePerM=6.25
	got := Estimate("anthropic", "claude-opus-4-7", 0, 0, 0, 1_000_000)
	if !nearly(got, 6.25, 1e-9) {
		t.Errorf("cache-create-only opus: got %v want 6.25", got)
	}
}

// TestWindowStart covers all five window kinds + "unknown" rejection.
// Calendar alignment is the contract operators rely on for "spent today".
func TestWindowStart(t *testing.T) {
	// Wed 2026-04-29 14:30:45 UTC — a fixed date so every assertion is
	// stable. April 29 2026 is a Wednesday.
	now := time.Date(2026, 4, 29, 14, 30, 45, 678_000_000, time.UTC)

	tests := []struct {
		name string
		w    BudgetWindow
		want time.Time
		ok   bool
	}{
		{"hour truncates to top of hour", WindowHour,
			time.Date(2026, 4, 29, 14, 0, 0, 0, time.UTC), true},
		{"day truncates to midnight UTC", WindowDay,
			time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC), true},
		{"week starts Monday", WindowWeek,
			// Monday before Wed 2026-04-29 = 2026-04-27
			time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC), true},
		{"month starts on the 1st", WindowMonth,
			time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), true},
		{"mission window returns zero time", WindowMission,
			time.Time{}, true},
		{"unknown window not ok", BudgetWindow("never"), time.Time{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := windowStart(tt.w, now)
			if ok != tt.ok {
				t.Errorf("ok = %v, want %v", ok, tt.ok)
			}
			if !got.Equal(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWindowStart_WeekSundayBoundary — Sunday's offset arithmetic is the
// historical bugbear. Verify Sunday rolls to the previous Monday rather
// than next-day's.
func TestWindowStart_WeekSundayBoundary(t *testing.T) {
	// Sunday 2026-05-03 10:00 UTC.
	sun := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	got, ok := windowStart(WindowWeek, sun)
	if !ok {
		t.Fatal("ok=false")
	}
	wantMonday := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC) // 6 days back
	if !got.Equal(wantMonday) {
		t.Errorf("Sunday week start: got %v want %v", got, wantMonday)
	}
}

// TestWindowOrUnknown verifies the empty-string substitution for display.
func TestWindowOrUnknown(t *testing.T) {
	if got := windowOrUnknown(""); got != "unknown" {
		t.Errorf("empty: %q", got)
	}
	if got := windowOrUnknown(QuotaTokensPerMin); got != "tokens_per_min" {
		t.Errorf("tokens: %q", got)
	}
}

// TestBudgetPayload_StableKeys pins the JSON-keys produced for budget
// journal entries. Frontend and downstream tools key on these strings,
// so an unintended rename here would silently break dashboards.
func TestBudgetPayload_StableKeys(t *testing.T) {
	s := BudgetStatus{
		Budget: Budget{
			ID:        "b1",
			ScopeKind: ScopeAgent,
			ScopeID:   "agent_x",
			Window:    WindowDay,
			Mode:      ModeTiered,
			LimitUSD:  100.0,
		},
		SpentUSD: 87.34,
		LimitUSD: 100.0,
		UtilPct:  87.34,
		State:    StateWarn,
	}
	got := budgetPayload(s)

	wantKeys := []string{
		"budget_id", "scope_kind", "scope_id", "window", "mode",
		"spent_usd", "limit_usd", "util_pct", "state",
	}
	for _, k := range wantKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("missing key %q in payload: %+v", k, got)
		}
	}
	if got["budget_id"] != "b1" {
		t.Errorf("budget_id: %v", got["budget_id"])
	}
	if got["state"] != "warn" {
		t.Errorf("state: %v", got["state"])
	}
}

// TestBudgetExceededError_Format pins the error string. Callers may
// surface this directly to operators, so the format is part of the API.
func TestBudgetExceededError_Format(t *testing.T) {
	tests := []struct {
		name string
		err  *BudgetExceededError
		want string
	}{
		{
			name: "empty falls back to generic",
			err:  &BudgetExceededError{},
			want: "paymaster: budget exceeded",
		},
		{
			name: "single status formats first",
			err: &BudgetExceededError{Statuses: []BudgetStatus{{
				Budget:   Budget{Mode: ModeHard, ScopeKind: ScopeWorkspace, Window: WindowDay, LimitUSD: 1.0},
				SpentUSD: 1.5,
			}}},
			want: "paymaster: budget exceeded (hard scope=workspace window=day spent=$1.5000 limit=$1.0000)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() =\n%q want\n%q", got, tt.want)
			}
		})
	}
}

// TestCheck_RequiresWorkspaceID surfaces the cross-tenant guard.
func TestCheck_RequiresWorkspaceID(t *testing.T) {
	db := openTestDB(t)
	_, err := Check(context.Background(), db, Scope{})
	if err == nil {
		t.Fatal("want error for empty workspace_id")
	}
	if !strings.Contains(err.Error(), "workspace_id") {
		t.Errorf("want workspace_id message, got %v", err)
	}
}

// TestCheck_NilDB returns a clear error rather than panicking.
func TestCheck_NilDB(t *testing.T) {
	_, err := Check(context.Background(), nil, Scope{WorkspaceID: "ws"})
	if err == nil {
		t.Fatal("want error for nil db")
	}
}

// TestEnforce_NoBudgetsConfigured is the happy-path no-op: a workspace
// without any budget rows lets all spend through, no journal entries.
func TestEnforce_NoBudgetsConfigured(t *testing.T) {
	db := openTestDB(t)
	em := &fakeEmitter{}
	if err := Enforce(context.Background(), db, em, Scope{WorkspaceID: "ws_unbudgeted"}); err != nil {
		t.Fatalf("Enforce empty: %v", err)
	}
	if got := len(em.entries); got != 0 {
		t.Errorf("no budgets → no entries, got %d", got)
	}
}

// TestEnforce_NilEmitter exercises the doc'd contract that emitter is
// optional. Hard budget should still block; entries are simply dropped.
func TestEnforce_NilEmitter(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, `INSERT INTO budget_limits (id, workspace_id, scope_kind, scope_id, window, limit_usd, mode)
	                 VALUES ('b1', 'ws1', 'workspace', 'ws1', 'day', 1.0, 'hard')`)
	now := time.Now().UTC().Format(tsLayout)
	mustExec(t, db, `INSERT INTO cost_ledger (id, workspace_id, ts, provider, model, cost_usd)
	                 VALUES ('c1', 'ws1', ?, 'anthropic', 'claude-opus-4-7', 1.50)`, now)

	err := Enforce(context.Background(), db, nil, Scope{WorkspaceID: "ws1"})
	var bx *BudgetExceededError
	if !errors.As(err, &bx) {
		t.Fatalf("want BudgetExceededError, got %T (%v)", err, err)
	}
}

// TestEnforceQuota_NoSignalIsNoop covers the "rate-limit headers absent"
// path. Zero or one remaining percent both mean "no signal" and produce
// neither warn nor exceeded.
func TestEnforceQuota_NoSignalIsNoop(t *testing.T) {
	tests := []struct {
		name         string
		remainingPct float64
	}{
		{"zero (no header)", 0.0},
		{"one (full quota)", 1.0},
		{"negative (garbage)", -0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			em := &fakeEmitter{}
			err := EnforceQuota(context.Background(), em, Scope{WorkspaceID: "ws1"},
				tt.remainingPct, QuotaTokensPerMin, false)
			if err != nil {
				t.Errorf("want nil, got %v", err)
			}
			if len(em.entries) != 0 {
				t.Errorf("want no entries, got %d", len(em.entries))
			}
		})
	}
}

// TestEnforceQuota_WarnBoundary covers the exact 20% threshold — a
// regression that flipped < to <= would cause noisy double-warnings.
func TestEnforceQuota_WarnBoundary(t *testing.T) {
	tests := []struct {
		name     string
		pct      float64
		wantWarn bool
	}{
		{"just under 20%", 0.199, true},
		{"exactly 20%", 0.20, false}, // the predicate is `>=` warn-threshold ⇒ no-op
		{"just over 20%", 0.21, false},
		{"5%", 0.05, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			em := &fakeEmitter{}
			_ = EnforceQuota(context.Background(), em, Scope{WorkspaceID: "ws1"},
				tt.pct, QuotaTokensPerMin, false)
			gotWarn := len(em.byType(journal.EntryBudgetWarning)) == 1
			if gotWarn != tt.wantWarn {
				t.Errorf("warn=%v at pct=%v, want %v", gotWarn, tt.pct, tt.wantWarn)
			}
		})
	}
}

// TestSpendByCrew_RequiresWorkspaceID + crew/agent/mission validators.
func TestRollupHelpers_RequireScopeIDs(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := SpendByCrew(ctx, db, "", time.Time{}, time.Time{}); err == nil {
		t.Error("SpendByCrew with empty workspace_id should error")
	}
	if _, err := SpendByAgent(ctx, db, "", time.Time{}, time.Time{}); err == nil {
		t.Error("SpendByAgent with empty crew_id should error")
	}
	if _, err := SpendByMission(ctx, db, ""); err == nil {
		t.Error("SpendByMission with empty mission_id should error")
	}
	if _, err := TopSpenders(ctx, db, "", 10, time.Time{}); err == nil {
		t.Error("TopSpenders with empty workspace_id should error")
	}
	if _, err := SubscriptionUsageByPlan(ctx, db, "", time.Time{}, time.Time{}); err == nil {
		t.Error("SubscriptionUsageByPlan with empty workspace_id should error")
	}
}

// TestTopSpenders_LimitClamping verifies <=0 → 10 and >100 → 100.
func TestTopSpenders_LimitClamping(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Seed 130 agents with one row each so we'd see all of them with no clamp.
	now := time.Now().UTC().Format(tsLayout)
	for i := 0; i < 130; i++ {
		mustExec(t, db,
			`INSERT INTO cost_ledger (id, workspace_id, agent_id, ts, provider, model, cost_usd)
			 VALUES (?, 'ws1', ?, ?, 'anthropic', 'claude-opus-4-7', ?)`,
			"r"+itoa(i), "agent_"+itoa(i), now, float64(i+1)*0.01)
	}

	tests := []struct {
		name        string
		limit       int
		wantMaxRows int
	}{
		{"zero clamps to 10", 0, 10},
		{"negative clamps to 10", -5, 10},
		{"under cap respected", 25, 25},
		{"over cap clamps to 100", 500, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TopSpenders(ctx, db, "ws1", tt.limit, time.Time{})
			if err != nil {
				t.Fatalf("TopSpenders: %v", err)
			}
			if len(got) != tt.wantMaxRows {
				t.Errorf("got %d rows, want %d", len(got), tt.wantMaxRows)
			}
		})
	}
}

// TestTopSpenders_NullAgentExcluded — workspace-level rows (NULL agent_id)
// must not appear in the leaderboard.
func TestTopSpenders_NullAgentExcluded(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(tsLayout)

	mustExec(t, db, `INSERT INTO cost_ledger
		(id, workspace_id, agent_id, ts, provider, model, cost_usd) VALUES
		('r1', 'ws1', 'agent_a',  ?, 'anthropic', 'claude-opus-4-7', 0.10),
		('r2', 'ws1', NULL,        ?, 'anthropic', 'claude-opus-4-7', 9.99),
		('r3', 'ws1', 'agent_b',  ?, 'anthropic', 'claude-opus-4-7', 0.05)`,
		now, now, now)

	got, err := TopSpenders(ctx, db, "ws1", 10, time.Time{})
	if err != nil {
		t.Fatalf("TopSpenders: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries (NULL excluded), got %d: %+v", len(got), got)
	}
	for _, s := range got {
		if s.ID == "" {
			t.Errorf("NULL agent leaked into leaderboard: %+v", s)
		}
	}
}

// TestSpendByCrew_NullCrewIDsAsEmptyString — workspace-level (no crew)
// rows surface with CrewID="" rather than being silently dropped.
func TestSpendByCrew_NullCrewIDsAsEmptyString(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(tsLayout)

	mustExec(t, db, `INSERT INTO cost_ledger
		(id, workspace_id, crew_id, ts, provider, model, cost_usd) VALUES
		('r1', 'ws1', 'crew_a', ?, 'anthropic', 'claude-opus-4-7', 1.0),
		('r2', 'ws1', NULL,      ?, 'anthropic', 'claude-opus-4-7', 0.5)`,
		now, now)

	got, err := SpendByCrew(ctx, db, "ws1", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("SpendByCrew: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows (NULL → empty), got %d", len(got))
	}
	var sawEmpty bool
	for _, s := range got {
		if s.CrewID == "" {
			sawEmpty = true
		}
	}
	if !sawEmpty {
		t.Error("NULL crew_id should surface as empty string row")
	}
}

// TestSpendByMission_EmptyResult exercises the no-rows path: the helper
// returns a fully-zeroed MissionSpend (not nil/error).
func TestSpendByMission_EmptyResult(t *testing.T) {
	db := openTestDB(t)
	got, err := SpendByMission(context.Background(), db, "mission_does_not_exist")
	if err != nil {
		t.Fatalf("SpendByMission: %v", err)
	}
	if got.MissionID != "mission_does_not_exist" {
		t.Errorf("MissionID echo: %q", got.MissionID)
	}
	if got.CallCount != 0 || got.CostUSD != 0 {
		t.Errorf("expected zero metrics, got %+v", got)
	}
}

// TestSubscriptionUsage_EmptyPlanLabelledUnknown — pre-migration rows
// have NULL/empty subscription_plan; the rollup labels them "unknown".
func TestSubscriptionUsage_EmptyPlanLabelledUnknown(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(tsLayout)

	mustExec(t, db, `INSERT INTO cost_ledger
		(id, workspace_id, ts, provider, model, billing_mode, subscription_plan, cost_confidence) VALUES
		('s1', 'ws1', ?, 'anthropic', 'claude-opus-4-7', 'flat_rate', '', 'unknown'),
		('s2', 'ws1', ?, 'anthropic', 'claude-opus-4-7', 'flat_rate', NULL, 'unknown'),
		('s3', 'ws1', ?, 'anthropic', 'claude-opus-4-7', 'flat_rate', 'Anthropic Max', 'unknown')`,
		now, now, now)

	rows, err := SubscriptionUsageByPlan(ctx, db, "ws1", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("SubscriptionUsageByPlan: %v", err)
	}
	plans := map[string]int64{}
	for _, r := range rows {
		plans[r.SubscriptionPlan] = r.CallCount
	}
	if plans["unknown"] != 2 {
		t.Errorf("expected 2 calls under 'unknown', got %d (plans=%+v)", plans["unknown"], plans)
	}
	if plans["Anthropic Max"] != 1 {
		t.Errorf("expected 1 call under Anthropic Max, got %d", plans["Anthropic Max"])
	}
}

// itoa is a tiny helper to keep the seeding loops readable without
// pulling in strconv.Itoa for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	if n < 0 {
		digits = append(digits, '-')
		n = -n
	}
	start := len(digits)
	for n > 0 {
		digits = append(digits, byte('0'+n%10))
		n /= 10
	}
	// reverse the appended portion
	for i, j := start, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}
