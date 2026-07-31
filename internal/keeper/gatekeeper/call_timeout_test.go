package gatekeeper_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// The budget an evaluator that was BUILT ONCE runs under.
//
// WithCallTimeout captures a duration at construction, which is all the access
// judge needs — it is rebuilt whenever its configuration changes (keeper_lazy.go).
// The four Keeper Reviews evaluators are not: they are constructed at boot and
// their pointers are captured by the route handler, so a budget captured there
// would be the boot-time one forever, exactly the way the aux MODEL was before
// #1556. WithCallTimeoutResolver is the same seam one field over: read per call.

// A configured budget must reach the call, and a later edit must reach the NEXT
// call without anything being rebuilt.
func TestCallTimeoutResolver_IsReadPerCall(t *testing.T) {
	var budget atomic.Int64
	budget.Store(int64(40 * time.Millisecond))

	g := gatekeeper.New(&blockingProvider{}, "test-model", newTestLogger(),
		gatekeeper.WithCallTimeoutResolver(func() time.Duration {
			return time.Duration(budget.Load())
		}))

	if got := g.CallTimeout(); got != 40*time.Millisecond {
		t.Errorf("CallTimeout = %s, want the resolved 40ms", got)
	}

	// The provider waits for the caller's context, so the gatekeeper's own
	// deadline is what ends the call — and the DENY reason names the budget that
	// was exceeded.
	resp, err := g.Evaluate(context.Background(), tierReq(keeper.SecurityLevelL2, goodIntent))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision != string(keeper.DecisionDeny) || !resp.InfraFailure {
		t.Fatalf("decision = %s (infra=%v), want a fail-closed DENY", resp.Decision, resp.InfraFailure)
	}
	if !strings.Contains(resp.Reason, "40ms") {
		t.Errorf("reason %q does not name the resolved budget", resp.Reason)
	}

	// The operator raises it. No rebuild happens — this evaluator's pointer is
	// held by the route handler — so the next call has to read the new value.
	budget.Store(int64(90 * time.Millisecond))
	if got := g.CallTimeout(); got != 90*time.Millisecond {
		t.Errorf("CallTimeout = %s after the edit, want 90ms", got)
	}
	resp, err = g.Evaluate(context.Background(), tierReq(keeper.SecurityLevelL2, goodIntent))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Reason, "90ms") {
		t.Errorf("reason %q still names the old budget — the resolver was read once", resp.Reason)
	}
}

// A timed-out evaluator must name the command that raises ITS budget. The
// built-in text names the credential judge's setting, which for one of the four
// Keeper Reviews slots would send the operator to change a number governing a
// different model — the same "setting that does nothing" the per-slot budget
// exists to stop being.
func TestTimeoutRemedy_NamesTheSettingThatGovernsThisEvaluator(t *testing.T) {
	g := gatekeeper.New(&blockingProvider{}, "test-model", newTestLogger(),
		gatekeeper.WithCallTimeoutResolver(func() time.Duration { return 30 * time.Millisecond }),
		gatekeeper.WithTimeoutRemedy("crewship keeper aux set behavior --timeout 40s"))

	resp, err := g.Evaluate(context.Background(), tierReq(keeper.SecurityLevelL2, goodIntent))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Reason, "keeper aux set behavior --timeout") {
		t.Errorf("reason %q does not name this evaluator's own budget setting", resp.Reason)
	}
	if strings.Contains(resp.Reason, "--judge-timeout") {
		t.Errorf("reason %q sends the operator to the credential judge's setting", resp.Reason)
	}
}

// Unset, the remedy is the credential judge's — the access path is the caller
// that has always been right about it.
func TestTimeoutRemedy_DefaultsToTheJudgeSetting(t *testing.T) {
	g := gatekeeper.New(&blockingProvider{}, "test-model", newTestLogger(),
		gatekeeper.WithCallTimeout(30*time.Millisecond))

	resp, err := g.Evaluate(context.Background(), tierReq(keeper.SecurityLevelL2, goodIntent))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Reason, "--judge-timeout") {
		t.Errorf("reason %q does not name the judge budget setting", resp.Reason)
	}
}

// A resolver that has nothing to say (no store, an unset slot) must not clear the
// bound: the fallback constant is what audit M4 added, and an unbounded model
// call is the failure it exists to prevent.
func TestCallTimeoutResolver_NonPositiveKeepsTheBound(t *testing.T) {
	g := gatekeeper.New(&mockProvider{content: `{"decision":"ALLOW","reason":"ok","risk":2}`},
		"test-model", newTestLogger(),
		gatekeeper.WithCallTimeoutResolver(func() time.Duration { return 0 }))

	if got := g.CallTimeout(); got != 20*time.Second {
		t.Errorf("CallTimeout = %s, want the 20s built-in fallback", got)
	}
}

// A nil resolver is the test/embedded wiring, and it must behave exactly as a
// Gatekeeper built without the option.
func TestCallTimeoutResolver_NilIsTheBuiltInBound(t *testing.T) {
	g := gatekeeper.New(&mockProvider{content: `{"decision":"ALLOW","reason":"ok","risk":2}`},
		"test-model", newTestLogger(), gatekeeper.WithCallTimeoutResolver(nil))

	if got := g.CallTimeout(); got != 20*time.Second {
		t.Errorf("CallTimeout = %s, want the 20s built-in fallback", got)
	}
}

// A live resolver is more specific than a value captured at construction: the
// caller that supplies both is saying "this is the budget right now".
func TestCallTimeoutResolver_WinsOverTheCapturedValue(t *testing.T) {
	g := gatekeeper.New(&mockProvider{content: `{"decision":"ALLOW","reason":"ok","risk":2}`},
		"test-model", newTestLogger(),
		gatekeeper.WithCallTimeout(3*time.Second),
		gatekeeper.WithCallTimeoutResolver(func() time.Duration { return 7 * time.Second }))

	if got := g.CallTimeout(); got != 7*time.Second {
		t.Errorf("CallTimeout = %s, want the resolver's 7s", got)
	}

	// …but only while it answers. A resolver that goes quiet falls back to the
	// captured value rather than to the generic constant.
	g2 := gatekeeper.New(&mockProvider{content: `{"decision":"ALLOW","reason":"ok","risk":2}`},
		"test-model", newTestLogger(),
		gatekeeper.WithCallTimeout(3*time.Second),
		gatekeeper.WithCallTimeoutResolver(func() time.Duration { return 0 }))

	if got := g2.CallTimeout(); got != 3*time.Second {
		t.Errorf("CallTimeout = %s, want the captured 3s", got)
	}
}
