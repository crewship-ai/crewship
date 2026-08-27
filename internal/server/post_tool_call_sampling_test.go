package server

// post_tool_call_sampling_test.go — issue #1001 M3.
//
// The end-to-end half of the sampling-rate slice: the observer is the ONE
// production caller of the behaviour hook, and it already reads the workspace's
// governance row on every observation (to learn whether the watchdog is on at
// all). This asserts it carries the cadence out of that same read instead of
// leaving every workspace on the hardwired every-5th-call default.
//
// On main this test fails at "an operator asked for every call and got every
// fifth" — which is the whole gap: behaviorhook.SetSampleEvery existed with zero
// production callers.

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/behaviorhook"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/policy"
	"github.com/crewship-ai/crewship/internal/testutil"

	_ "modernc.org/sqlite"
)

// countingJudge stands in for the governance model and counts how many times it
// was actually asked — the number the sampling rate exists to control.
type countingJudge struct{ calls atomic.Int64 }

func (c *countingJudge) Complete(ctx context.Context, r llm.Request) (*llm.Response, error) {
	c.calls.Add(1)
	return &llm.Response{Content: `{"decision":"ALLOW","reason":"ok","risk":1}`}, nil
}

func (c *countingJudge) Stream(ctx context.Context, r llm.Request, h func(llm.StreamEvent) error) (*llm.Response, error) {
	resp, _ := c.Complete(ctx, r)
	_ = h(llm.StreamEvent{Type: "done", Response: resp})
	return resp, nil
}

func (c *countingJudge) Name() string { return "counting" }

// observeN installs a hook over a counting judge, runs n observations through
// the real observer against a workspace configured with the given cadence, and
// reports how many judge calls that produced.
func observeN(t *testing.T, sampleEvery, n int) int64 {
	t.Helper()

	d := testutil.MigratedDB(t)
	if _, err := d.DB.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, autonomy_level, behavior_mode)
		 VALUES ('cr1', 'ws1', 'Crew', 'cr1', 'guided', 'warn')`); err != nil {
		t.Fatal(err)
	}
	if err := governance.Upsert(context.Background(), d.DB, "ws1", governance.Settings{
		Enabled:             true,
		DenyNotifyMinRisk:   governance.DefaultDenyNotifyMinRisk,
		BehaviorSampleEvery: sampleEvery,
		// updated_by is FK'd to users; a system write leaves it null.
	}, ""); err != nil {
		t.Fatalf("governance upsert: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	judge := &countingJudge{}
	gk := gatekeeper.New(judge, "test-judge", logger)
	hook := behaviorhook.New(gatekeeper.NewBehaviorEvaluator(gk, logger), policy.NewResolver(d.DB), logger)

	prev := behaviorhook.Get()
	behaviorhook.Set(hook)
	t.Cleanup(func() { behaviorhook.Set(prev) })

	obs := newPostToolCallObserver(logger, nil, d.DB)
	for i := 0; i < n; i++ {
		obs.Observe(orchestrator.ToolCallObservation{
			WorkspaceID: "ws1",
			CrewID:      "cr1",
			AgentID:     "agent-a",
			ToolName:    "shell_exec",
		})
	}
	return judge.calls.Load()
}

func TestPostToolCallObserver_HonoursWorkspaceSamplingRate(t *testing.T) {
	cases := []struct {
		name        string
		sampleEvery int
		calls       int
		wantJudged  int64
	}{
		// The value the whole slice is about: an operator who wants every tool
		// call reviewed gets every tool call reviewed.
		{"every call", 1, 10, 10},
		{"every other call", 2, 10, 5},
		// Unset (0) is the pre-existing behaviour, unchanged on upgrade.
		{"unset keeps the built-in default", 0, 10, 2},
		{"an explicit default matches the built-in", governance.DefaultBehaviorSampleEvery, 10, 2},
		{"a slack cadence judges rarely", 20, 40, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := observeN(t, tc.sampleEvery, tc.calls); got != tc.wantJudged {
				t.Errorf("judge calls = %d over %d tool calls at behavior_sample_every=%d, want %d",
					got, tc.calls, tc.sampleEvery, tc.wantJudged)
			}
		})
	}
}
