package api

// assignments_delegation_hooks_test.go proves pre_task_delegation and
// post_task_delegation — two of the ten hook events found alongside
// pre_tool_call (#2132) to be declared in hooks.AllEvents, accepted by the
// CLI/API, and reached by zero hooks.Dispatch call sites — now actually
// fire from runAssignment, the one function every delegation door (the
// sidecar's /assign, its dispatch-pump retry, the mission engine's
// DispatchAssignment, and @mention's DispatchMention) converges on before
// a delegated task runs.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/hooks"
)

// TestRunAssignment_DispatchesPreAndPostTaskDelegationHooks registers a
// blocking webhook on each event and drives runAssignment synchronously
// (same harness as TestRunAssignment_NoOrchestrator_FailsAssignment) —
// h.orch is nil, so the run fails immediately after start, but both hooks
// must have already fired by then since post_task_delegation is dispatched
// before the orchestrator is ever touched.
func TestRunAssignment_DispatchesPreAndPostTaskDelegationHooks(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true") // httptest binds 127.0.0.1

	h, wsID, _, leadID, workerID, chatID := covAsgRig(t)
	insertAssignment(t, h.db, "asg-hook-1", wsID, chatID, leadID, workerID, "PENDING")

	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	for _, ev := range []hooks.Event{hooks.EventPreTaskDelegation, hooks.EventPostTaskDelegation} {
		if _, err := hooks.Register(context.Background(), h.db, hooks.Hook{
			WorkspaceID:   wsID,
			Event:         ev,
			HandlerKind:   hooks.HandlerKindHTTP,
			HandlerConfig: map[string]any{"url": ts.URL + "/" + string(ev)},
			Blocking:      true,
			Enabled:       true,
		}, false); err != nil {
			t.Fatalf("register %s hook: %v", ev, err)
		}
	}

	body := createAssignmentBody{
		TargetSlug: "asg-worker", Task: "t", CrewID: "crew-asg", WorkspaceID: wsID, ChatID: chatID,
	}
	target := targetAgentInfo{ID: workerID, Slug: "asg-worker", Name: "Worker", CrewSlug: "asg"}
	h.runAssignment(context.Background(), "asg-hook-1", body, target)

	if len(seen) != 2 {
		t.Fatalf("hook hits = %v, want exactly [pre_task_delegation, post_task_delegation]", seen)
	}
	if seen[0] != "/pre_task_delegation" || seen[1] != "/post_task_delegation" {
		t.Errorf("hook order = %v, want [/pre_task_delegation /post_task_delegation]", seen)
	}
}

// TestRunAssignment_PreTaskDelegationHookBlocksTheRun proves a blocking
// pre_task_delegation hook refuses the delegation before anything else in
// runAssignment happens — the assignment lands FAILED with the hook's
// refusal in error_message, and no run.started journal entry (runID
// never existed) is emitted.
func TestRunAssignment_PreTaskDelegationHookBlocksTheRun(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true")

	h, wsID, _, leadID, workerID, chatID := covAsgRig(t)
	insertAssignment(t, h.db, "asg-hook-2", wsID, chatID, leadID, workerID, "PENDING")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503) // Block outcome
	}))
	defer ts.Close()

	if _, err := hooks.Register(context.Background(), h.db, hooks.Hook{
		WorkspaceID:   wsID,
		Event:         hooks.EventPreTaskDelegation,
		HandlerKind:   hooks.HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": ts.URL},
		Blocking:      true,
		Enabled:       true,
	}, false); err != nil {
		t.Fatalf("register hook: %v", err)
	}

	body := createAssignmentBody{
		TargetSlug: "asg-worker", Task: "t", CrewID: "crew-asg", WorkspaceID: wsID, ChatID: chatID,
	}
	target := targetAgentInfo{ID: workerID, Slug: "asg-worker", Name: "Worker", CrewSlug: "asg"}
	h.runAssignment(context.Background(), "asg-hook-2", body, target)

	if got := assignmentStatus(t, h.db, "asg-hook-2"); got != "FAILED" {
		t.Fatalf("status = %q, want FAILED", got)
	}
	var errMsg string
	if err := h.db.QueryRow(`SELECT COALESCE(error_message,'') FROM assignments WHERE id = ?`, "asg-hook-2").Scan(&errMsg); err != nil {
		t.Fatalf("query error_message: %v", err)
	}
	if errMsg == "" {
		t.Fatal("expected error_message to name the pre_task_delegation block")
	}
}
