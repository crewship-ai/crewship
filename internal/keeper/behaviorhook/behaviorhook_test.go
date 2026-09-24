package behaviorhook_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/crewship-ai/crewship/internal/keeper/behaviorhook"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/policy"
	"github.com/crewship-ai/crewship/internal/testutil"

	_ "modernc.org/sqlite"
)

func newLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

type cannedProvider struct{ content string }

func (c *cannedProvider) Complete(ctx context.Context, r llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: c.content}, nil
}
func (c *cannedProvider) Stream(ctx context.Context, r llm.Request, h func(llm.StreamEvent) error) (*llm.Response, error) {
	resp, _ := c.Complete(ctx, r)
	_ = h(llm.StreamEvent{Type: "done", Response: resp})
	return resp, nil
}
func (c *cannedProvider) Name() string { return "canned" }

func setupDB(t *testing.T, crewID, level, mode string) *policy.Resolver {
	t.Helper()
	d := testutil.MigratedDB(t)
	if _, err := d.DB.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, autonomy_level, behavior_mode) VALUES (?, 'ws1', ?, ?, ?, ?)`,
		crewID, crewID+"-name", crewID, level, mode,
	); err != nil {
		t.Fatal(err)
	}
	return policy.NewResolver(d.DB)
}

func TestHook_Samples_Every_N(t *testing.T) {
	res := setupDB(t, "cr1", "guided", "warn")
	gk := gatekeeper.New(&cannedProvider{content: `{"decision":"ALLOW","reason":"ok","risk":1}`}, "claude-haiku-4-5", newLogger())
	ev := gatekeeper.NewBehaviorEvaluator(gk, newLogger())

	h := behaviorhook.New(ev, res, newLogger())
	h.SetSampleEvery(3)

	fires := 0
	for i := 0; i < 10; i++ {
		sample, err := h.MaybeEvaluate(context.Background(), hooks.EventContext{
			Event:    hooks.EventPostToolCall,
			CrewID:   "cr1",
			AgentID:  "agent-a",
			ToolName: "shell_exec",
		})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if sample != nil {
			fires++
		}
	}
	// 10 calls, every 3rd fires → calls 3, 6, 9 fire = 3 fires.
	if fires != 3 {
		t.Errorf("fires = %d, want 3 (every-3rd over 10 calls)", fires)
	}
}

func TestHook_BlocksInBlockMode(t *testing.T) {
	res := setupDB(t, "cr1", "guided", "block")
	// DENY in block mode + guided → BlockInbox + ShouldBlock=true.
	gk := gatekeeper.New(&cannedProvider{content: `{"decision":"DENY","reason":"destructive sequence","risk":9}`}, "claude-haiku-4-5", newLogger())
	ev := gatekeeper.NewBehaviorEvaluator(gk, newLogger())

	h := behaviorhook.New(ev, res, newLogger())
	h.SetSampleEvery(1) // fire every call

	sample, err := h.MaybeEvaluate(context.Background(), hooks.EventContext{
		Event:    hooks.EventPostToolCall,
		CrewID:   "cr1",
		AgentID:  "agent-a",
		ToolName: "shell_exec",
		Payload:  map[string]any{"cmd": "rm -rf /"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sample == nil {
		t.Fatal("expected a fired sample with SampleEvery=1")
	}
	if sample.Blocked == nil {
		t.Fatal("expected BlockedError in block mode + DENY")
	}
	var typed *hooks.BlockedError
	if !errors.As(sample.Blocked, &typed) {
		t.Fatalf("expected *hooks.BlockedError, got %T", sample.Blocked)
	}
	if typed.Event != hooks.EventPostToolCall {
		t.Errorf("Event = %q, want post_tool_call", typed.Event)
	}
	// #2575: the verdict rides along for routing even (especially) when it
	// blocks — a block-mode DENY also owes the operator an inbox item.
	if sample.Verdict == nil {
		t.Fatal("expected the verdict to ride along with the block")
	}
	if sample.Verdict.Decision != gatekeeper.BehaviorDeny {
		t.Errorf("verdict = %q, want DENY", sample.Verdict.Decision)
	}
	if string(sample.Verdict.PolicyDecision) != "block_inbox" {
		t.Errorf("policy decision = %q, want block_inbox", string(sample.Verdict.PolicyDecision))
	}
}

func TestHook_WarnMode_NeverBlocks(t *testing.T) {
	res := setupDB(t, "cr1", "guided", "warn")
	// DENY in warn mode degrades to inbox; no block.
	gk := gatekeeper.New(&cannedProvider{content: `{"decision":"DENY","reason":"anti-pattern","risk":7}`}, "claude-haiku-4-5", newLogger())
	ev := gatekeeper.NewBehaviorEvaluator(gk, newLogger())

	h := behaviorhook.New(ev, res, newLogger())
	h.SetSampleEvery(1)

	sample, err := h.MaybeEvaluate(context.Background(), hooks.EventContext{
		Event:    hooks.EventPostToolCall,
		CrewID:   "cr1",
		AgentID:  "agent-a",
		ToolName: "shell_exec",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sample == nil {
		t.Fatal("expected a fired sample")
	}
	if sample.Blocked != nil {
		t.Errorf("expected no block in warn mode; got %+v", sample.Blocked)
	}
}

// #2575: a WARN verdict must come back on the Sample for the caller to
// route. Before the Sample return type, a non-blocking verdict was
// indistinguishable from "nothing happened" — the adapter could not have
// persisted it even if it had wanted to.
func TestHook_WarnVerdictRidesTheSample(t *testing.T) {
	res := setupDB(t, "cr1", "guided", "warn")
	gk := gatekeeper.New(&cannedProvider{content: `{"decision":"WARN","reason":"tight loop suspicion","risk":4}`}, "claude-haiku-4-5", newLogger())
	ev := gatekeeper.NewBehaviorEvaluator(gk, newLogger())

	h := behaviorhook.New(ev, res, newLogger())
	h.SetSampleEvery(1)

	sample, err := h.MaybeEvaluate(context.Background(), hooks.EventContext{
		Event:    hooks.EventPostToolCall,
		CrewID:   "cr1",
		AgentID:  "agent-a",
		ToolName: "shell_exec",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sample == nil || sample.Verdict == nil {
		t.Fatal("expected a fired sample with a verdict")
	}
	if sample.Verdict.Decision != gatekeeper.BehaviorWarn {
		t.Errorf("verdict = %q, want WARN", sample.Verdict.Decision)
	}
	if sample.Blocked != nil {
		t.Errorf("WARN must never block; got %+v", sample.Blocked)
	}
	if string(sample.Verdict.PolicyDecision) != "auto_log_inbox" {
		t.Errorf("policy decision = %q, want auto_log_inbox at guided/warn",
			string(sample.Verdict.PolicyDecision))
	}
}

func TestHook_Disabled_WithZeroSampleEvery(t *testing.T) {
	res := setupDB(t, "cr1", "guided", "warn")
	gk := gatekeeper.New(&cannedProvider{}, "claude-haiku-4-5", newLogger())
	ev := gatekeeper.NewBehaviorEvaluator(gk, newLogger())
	h := behaviorhook.New(ev, res, newLogger())
	h.SetSampleEvery(0)

	for i := 0; i < 5; i++ {
		sample, err := h.MaybeEvaluate(context.Background(), hooks.EventContext{
			CrewID: "cr1", AgentID: "a", ToolName: "t",
		})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if sample != nil {
			t.Fatalf("fire on call %d with SampleEvery=0", i)
		}
	}
}

func TestHook_NilDependencies_SkipsSilently(t *testing.T) {
	h := behaviorhook.New(nil, nil, newLogger())
	sample, err := h.MaybeEvaluate(context.Background(), hooks.EventContext{CrewID: "x"})
	if sample != nil {
		t.Error("fire with nil ev + resolver; expected skip")
	}
	if !errors.Is(err, behaviorhook.ErrNotConfigured) {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}
}

func TestHook_GlobalSingleton(t *testing.T) {
	if behaviorhook.Get() != nil {
		// Other tests may have set the global; reset to nil for this test.
		behaviorhook.Set(nil)
	}
	if behaviorhook.Get() != nil {
		t.Fatal("Get returned non-nil after Set(nil)")
	}
	h := behaviorhook.New(nil, nil, newLogger())
	behaviorhook.Set(h)
	if behaviorhook.Get() != h {
		t.Error("Get did not return the installed Hook")
	}
	behaviorhook.Set(nil) // restore default for other tests
}
