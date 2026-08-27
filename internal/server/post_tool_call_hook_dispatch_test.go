package server

// post_tool_call_hook_dispatch_test.go proves post_tool_call — the one
// hook event of the ten found alongside pre_tool_call (#2132) that had a
// real per-call observation point (postToolCallObserver.Observe) but no
// hooks.Dispatch call anywhere in it — now actually fires a
// user-registered hook, and does so independent of the built-in
// behavior-monitor's governance/sampling gate: a workspace with the
// watchdog OFF (the default) must still see its own post_tool_call hooks
// run on every observed tool call.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestPostToolCallObserver_DispatchesUserHookWithoutGovernanceEnabled(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true") // httptest binds 127.0.0.1

	d := testutil.MigratedDB(t)
	if _, err := d.DB.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, autonomy_level, behavior_mode, network_mode)
		 VALUES ('cr1', 'ws1', 'Crew', 'cr1', 'guided', 'warn', 'free')`); err != nil {
		t.Fatal(err)
	}
	// Deliberately no governance.Upsert call: this workspace's behavioral
	// watchdog is OFF (the default), exercising the exact case Observe
	// used to short-circuit on before doing anything post_tool_call-shaped.

	var hit bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	if _, err := hooks.Register(context.Background(), d.DB, hooks.Hook{
		WorkspaceID:   "ws1",
		Event:         hooks.EventPostToolCall,
		HandlerKind:   hooks.HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": ts.URL},
		Blocking:      true,
		Enabled:       true,
	}, false); err != nil {
		t.Fatalf("register hook: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	obs := newPostToolCallObserver(logger, nil, d.DB)
	obs.Observe(orchestrator.ToolCallObservation{
		WorkspaceID: "ws1",
		CrewID:      "cr1",
		AgentID:     "agent-a",
		ToolName:    "shell_exec",
	})

	if !hit {
		t.Fatal("post_tool_call hook was never dispatched despite governance being disabled for the workspace")
	}
}
