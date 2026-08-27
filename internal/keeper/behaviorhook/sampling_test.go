package behaviorhook_test

// sampling_test.go — issue #1001 M3: the per-call sampling cadence.
//
// The hook has always been able to sample at any cadence; what it could not do
// was be TOLD one per call. MaybeEvaluateEvery is that seam: the caller has just
// read the workspace's governance row (it has to, to know the watchdog is even
// on) and hands the cadence in from that same read, so a change takes effect on
// the next tool call instead of the next restart — the #1556 trap, one
// subsystem over.

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/crewship-ai/crewship/internal/keeper/behaviorhook"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// TestHook_MaybeEvaluateEvery_Cadence is the table the wiring exists for: at 1
// the evaluator fires on every tool call, at the default every 5th, and the
// caller passing "nothing" (0) gets the hook's own configured cadence rather
// than silence.
func TestHook_MaybeEvaluateEvery_Cadence(t *testing.T) {
	cases := []struct {
		name string
		// every is what the caller resolved from the workspace row.
		every int64
		// hookDefault is the instance-wide cadence, set at construction.
		hookDefault int64
		calls       int
		wantFires   int
	}{
		{"every call", 1, behaviorhook.DefaultSampleEvery, 10, 10},
		{"every other call", 2, behaviorhook.DefaultSampleEvery, 10, 5},
		{"the built-in default", behaviorhook.DefaultSampleEvery, behaviorhook.DefaultSampleEvery, 10, 2},
		{"unset falls back to the hook default", 0, behaviorhook.DefaultSampleEvery, 10, 2},
		{"unset with a tuned hook default", 0, 4, 12, 3},
		{"the ceiling never fires in a short run", 100, behaviorhook.DefaultSampleEvery, 10, 0},
		{"a hook default of 0 disables the monitor", 0, 0, 10, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := setupDB(t, "cr1", "guided", "warn")
			gk := gatekeeper.New(&cannedProvider{content: `{"decision":"ALLOW","reason":"ok","risk":1}`},
				"claude-haiku-4-5", newLogger())
			h := behaviorhook.New(gatekeeper.NewBehaviorEvaluator(gk, newLogger()), res, newLogger())
			h.SetSampleEvery(tc.hookDefault)

			fires := 0
			for i := 0; i < tc.calls; i++ {
				_, fired := h.MaybeEvaluateEvery(context.Background(), hooks.EventContext{
					Event:       hooks.EventPostToolCall,
					WorkspaceID: "ws1",
					CrewID:      "cr1",
					AgentID:     "agent-a",
					ToolName:    "shell_exec",
				}, tc.every)
				if fired {
					fires++
				}
			}
			if fires != tc.wantFires {
				t.Errorf("fires = %d over %d calls at every=%d (hook default %d), want %d",
					fires, tc.calls, tc.every, tc.hookDefault, tc.wantFires)
			}
		})
	}
}

// TestHook_MaybeEvaluateEvery_IsPerCall is the property that makes this a
// configuration surface rather than a boot flag: two consecutive calls with
// different cadences are each honoured, with no restart and no SetSampleEvery
// in between.
func TestHook_MaybeEvaluateEvery_IsPerCall(t *testing.T) {
	res := setupDB(t, "cr1", "guided", "warn")
	gk := gatekeeper.New(&cannedProvider{content: `{"decision":"ALLOW","reason":"ok","risk":1}`},
		"claude-haiku-4-5", newLogger())
	h := behaviorhook.New(gatekeeper.NewBehaviorEvaluator(gk, newLogger()), res, newLogger())

	ec := hooks.EventContext{
		Event: hooks.EventPostToolCall, WorkspaceID: "ws1",
		CrewID: "cr1", AgentID: "agent-a", ToolName: "shell_exec",
	}

	// Call 1 at the default cadence: not sampled (counter 1, 1%5 != 0).
	if _, fired := h.MaybeEvaluateEvery(context.Background(), ec, behaviorhook.DefaultSampleEvery); fired {
		t.Fatal("first call at the default cadence fired; want a sampling gate, not an every-call gate")
	}
	// Call 2, the operator having just tightened the workspace to every call.
	if _, fired := h.MaybeEvaluateEvery(context.Background(), ec, 1); !fired {
		t.Fatal("a cadence of 1 did not fire on the very next call — the value is not read per call")
	}
}

// TestHook_MaybeEvaluate_KeepsHookDefault pins the pre-existing entry point to
// its old behaviour: a caller that knows nothing about workspace settings still
// gets the hook's configured cadence.
func TestHook_MaybeEvaluate_KeepsHookDefault(t *testing.T) {
	res := setupDB(t, "cr1", "guided", "warn")
	gk := gatekeeper.New(&cannedProvider{content: `{"decision":"ALLOW","reason":"ok","risk":1}`},
		"claude-haiku-4-5", newLogger())
	h := behaviorhook.New(gatekeeper.NewBehaviorEvaluator(gk, newLogger()), res, newLogger())

	fires := 0
	for i := 0; i < 10; i++ {
		if _, fired := h.MaybeEvaluate(context.Background(), hooks.EventContext{
			Event: hooks.EventPostToolCall, WorkspaceID: "ws1",
			CrewID: "cr1", AgentID: "agent-a", ToolName: "shell_exec",
		}); fired {
			fires++
		}
	}
	if fires != 2 {
		t.Errorf("fires = %d over 10 calls on the default cadence, want 2", fires)
	}
}
