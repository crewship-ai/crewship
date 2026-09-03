package paymaster

// budget_breach_debounce_test.go covers #2153: on_budget_exceeded must
// dispatch once per breach (per budget, per period, per limit), not once
// per LLM call made while a budget stays over. Pre-fix, Enforce dispatched
// the hook on every over-budget call and twice on a call that breached two
// budgets — see the comment this replaced in budgets.go.
//
// TestEnforce_OnBudgetExceeded_FiresOnceAcrossRepeatedCalls and
// TestEnforce_OnBudgetExceeded_TwoBudgetsFireOnceEach exercise the fix
// through Enforce end-to-end (real webhook, real dispatch). Both are RED
// on pre-#2153 main: the first sees 5 hits instead of 1, the second sees
// more than 2. TestAnnounceBudgetBreach pins the pure debounce-key logic
// directly — period rollover and limit-raise re-firing — without needing
// to fake wall-clock time through Enforce/Check.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/hooks"
)

// waitForAtLeast polls get (in the foreground, no background goroutine of
// its own) until it reports >= want or timeout elapses. Dispatch's
// non-blocking pass runs handlers in a goroutine, so the webhook hit count
// isn't guaranteed to be visible the instant Enforce returns.
func waitForAtLeast(t *testing.T, get func() int32, want int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if get() >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for count >= %d, got %d", want, get())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestEnforce_OnBudgetExceeded_FiresOnceAcrossRepeatedCalls breaches a
// hard-mode budget once, then calls Enforce five times in a row against
// the still-exceeded scope (the same shape as five consecutive LLM calls
// made while over budget). The webhook must see exactly 1 hit, not 5.
func TestEnforce_OnBudgetExceeded_FiresOnceAcrossRepeatedCalls(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true") // httptest binds 127.0.0.1
	breachAnnounced = sync.Map{}                     // isolate from any other test's state

	db := openTestDBWithHooks(t)
	em := &fakeEmitter{}
	ctx := context.Background()

	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(200)
	}))
	defer ts.Close()

	if _, err := hooks.Register(ctx, db, hooks.Hook{
		WorkspaceID:   "ws-debounce-1",
		Event:         hooks.EventOnBudgetExceeded,
		HandlerKind:   hooks.HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": ts.URL},
		Enabled:       true,
	}, false); err != nil {
		t.Fatalf("register hook: %v", err)
	}

	mustExec(t, db, `INSERT INTO budget_limits (id, workspace_id, scope_kind, scope_id, window, limit_usd, mode)
	                 VALUES ('b-debounce-1', 'ws-debounce-1', 'workspace', 'ws-debounce-1', 'day', 1.0, 'hard')`)
	now := time.Now().UTC().Format(tsLayout)
	mustExec(t, db, `INSERT INTO cost_ledger (id, workspace_id, ts, provider, model, cost_usd)
	                 VALUES ('c-debounce-1', 'ws-debounce-1', ?, 'anthropic', 'claude-opus-4-7', 1.50)`, now)

	for i := 0; i < 5; i++ {
		if err := Enforce(ctx, db, em, Scope{WorkspaceID: "ws-debounce-1"}); err == nil {
			t.Fatalf("call %d: expected BudgetExceededError, got nil", i)
		}
	}

	waitForAtLeast(t, func() int32 { return atomic.LoadInt32(&hits) }, 1, time.Second)
	// Give any extra (pre-fix-shaped) dispatches a window to land before
	// asserting the count settled at exactly 1.
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("on_budget_exceeded dispatched %d times across 5 over-budget calls, want exactly 1", got)
	}
}

// TestEnforce_OnBudgetExceeded_TwoBudgetsFireOnceEach breaches two
// separate budgets (same scope, different windows) with a single Enforce
// call. Each budget is a distinct triggering condition, so the webhook
// must see exactly 2 hits — one per budget — not 1 (over-collapsed) and
// not more than 2 (the debounce misfiring on repeats within the same
// call).
func TestEnforce_OnBudgetExceeded_TwoBudgetsFireOnceEach(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true")
	breachAnnounced = sync.Map{}

	db := openTestDBWithHooks(t)
	em := &fakeEmitter{}
	ctx := context.Background()

	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(200)
	}))
	defer ts.Close()

	if _, err := hooks.Register(ctx, db, hooks.Hook{
		WorkspaceID:   "ws-debounce-2",
		Event:         hooks.EventOnBudgetExceeded,
		HandlerKind:   hooks.HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": ts.URL},
		Enabled:       true,
	}, false); err != nil {
		t.Fatalf("register hook: %v", err)
	}

	mustExec(t, db, `INSERT INTO budget_limits (id, workspace_id, scope_kind, scope_id, window, limit_usd, mode) VALUES
		('b-debounce-2-day', 'ws-debounce-2', 'workspace', 'ws-debounce-2', 'day', 1.0, 'hard'),
		('b-debounce-2-month', 'ws-debounce-2', 'workspace', 'ws-debounce-2', 'month', 1.0, 'hard')`)
	now := time.Now().UTC().Format(tsLayout)
	mustExec(t, db, `INSERT INTO cost_ledger (id, workspace_id, ts, provider, model, cost_usd)
	                 VALUES ('c-debounce-2', 'ws-debounce-2', ?, 'anthropic', 'claude-opus-4-7', 1.50)`, now)

	if err := Enforce(ctx, db, em, Scope{WorkspaceID: "ws-debounce-2"}); err == nil {
		t.Fatal("expected BudgetExceededError")
	}

	waitForAtLeast(t, func() int32 { return atomic.LoadInt32(&hits) }, 2, time.Second)
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("on_budget_exceeded dispatched %d times for a call breaching 2 budgets, want exactly 2 (one per budget)", got)
	}
}

// TestAnnounceBudgetBreach pins the pure debounce-key logic: same period
// suppresses repeats, a period rollover re-announces, a limit raise
// re-announces even within the same period, a windowless (mission) budget
// only re-announces on a limit raise (never on time passing alone), and
// distinct budgets never share state. Steps run in order — later steps
// depend on state left by earlier ones, same as the real call sequence
// Enforce produces.
func TestAnnounceBudgetBreach(t *testing.T) {
	breachAnnounced = sync.Map{}

	base := Budget{ID: "b-unit-1", WorkspaceID: "ws-unit-1", ScopeKind: ScopeWorkspace, ScopeID: "ws-unit-1", Window: WindowDay, LimitUSD: 1.0}
	day1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	day2 := day1.AddDate(0, 0, 1)

	raised := base
	raised.LimitUSD = 2.0

	crewBudget := Budget{ID: "b-unit-2", WorkspaceID: "ws-unit-1", ScopeKind: ScopeCrew, ScopeID: "crewA", Window: WindowDay, LimitUSD: 1.0}

	mission := Budget{ID: "b-unit-3", WorkspaceID: "ws-unit-1", ScopeKind: ScopeMission, ScopeID: "m1", Window: WindowMission, LimitUSD: 5.0}
	raisedMission := mission
	raisedMission.LimitUSD = 10.0

	steps := []struct {
		name string
		b    Budget
		now  time.Time
		want bool // whether THIS call should announce (dispatch)
	}{
		{"first breach announces", base, day1, true},
		{"same period does not re-announce", base, day1, false},
		{"later call same day window does not re-announce", base, day1.Add(6 * time.Hour), false},
		{"period rolls, announces again", base, day2, true},
		{"new period settles, no re-announce", base, day2, false},
		{"limit raised in same period re-announces", raised, day2, true},
		{"raised limit settles, no re-announce", raised, day2, false},
		{"distinct crew-scoped budget announces independently", crewBudget, day2, true},
		{"mission-window budget announces once", mission, day2, true},
		{"mission window has no period — time alone doesn't re-announce", mission, day2.Add(72 * time.Hour), false},
		{"mission-window limit raise re-announces", raisedMission, day2, true},
	}

	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			if got := announceBudgetBreach(s.b, s.now); got != s.want {
				t.Errorf("announceBudgetBreach(%+v, %v) = %v, want %v", s.b, s.now, got, s.want)
			}
		})
	}
}
