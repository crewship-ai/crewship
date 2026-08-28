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
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/hooks"
)

// TestRunAssignment_DispatchesPreAndPostTaskDelegationHooks registers a
// blocking pre-hook and an asynchronous post-hook, then drives runAssignment
// (same harness as TestRunAssignment_NoOrchestrator_FailsAssignment) —
// h.orch is nil, so the run fails immediately after start, but both hooks
// must have already fired by then since post_task_delegation is dispatched
// before the orchestrator is ever touched.
func TestRunAssignment_DispatchesPreAndPostTaskDelegationHooks(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true") // httptest binds 127.0.0.1

	h, wsID, _, leadID, workerID, chatID := covAsgRig(t)
	insertAssignment(t, h.db, "asg-hook-1", wsID, chatID, leadID, workerID, "PENDING")

	seen := make(chan string, 2)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.Path
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
			Blocking:      ev.SupportsBlocking(),
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

	got := make([]string, 0, 2)
	for len(got) < 2 {
		select {
		case path := <-seen:
			got = append(got, path)
		case <-time.After(time.Second):
			t.Fatalf("hook hits = %v, want exactly [pre_task_delegation, post_task_delegation]", got)
		}
	}
	if got[0] != "/pre_task_delegation" || got[1] != "/post_task_delegation" {
		t.Errorf("hook order = %v, want [/pre_task_delegation /post_task_delegation]", got)
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
	if !strings.Contains(errMsg, "pre_task_delegation hook blocked") {
		t.Fatalf("error_message = %q, want it to name the pre_task_delegation block", errMsg)
	}
}

// TestRunAssignment_PreTaskDelegationDispatchFailureIsNotReportedAsABlock
// pins the distinction internal/hooks/types.go's DispatchError doc asks
// call sites to preserve. Both kinds fail the delegation closed — a gate
// that could not be evaluated must not read as passed — but they are
// different answers, and an operator told "a hook blocked this" goes
// looking for a policy that does not exist when the real cause is a broken
// handler or an unreadable registry.
//
// The failure is induced with a subagent hook and no SubagentHandler
// installed: that is the one handler kind that returns a plain error
// (ErrSubagentHandlerNotConfigured) with no network involved, so the
// DispatchError arm is reached deterministically rather than raced for.
func TestRunAssignment_PreTaskDelegationDispatchFailureIsNotReportedAsABlock(t *testing.T) {
	h, wsID, _, leadID, workerID, chatID := covAsgRig(t)
	insertAssignment(t, h.db, "asg-hook-3", wsID, chatID, leadID, workerID, "PENDING")

	if _, err := hooks.Register(context.Background(), h.db, hooks.Hook{
		WorkspaceID:   wsID,
		Event:         hooks.EventPreTaskDelegation,
		HandlerKind:   hooks.HandlerKindSubagent,
		HandlerConfig: map[string]any{"agent_id": "nobody"},
		Blocking:      true,
		Enabled:       true,
	}, false); err != nil {
		t.Fatalf("register hook: %v", err)
	}

	body := createAssignmentBody{
		TargetSlug: "asg-worker", Task: "t", CrewID: "crew-asg", WorkspaceID: wsID, ChatID: chatID,
	}
	target := targetAgentInfo{ID: workerID, Slug: "asg-worker", Name: "Worker", CrewSlug: "asg"}
	h.runAssignment(context.Background(), "asg-hook-3", body, target)

	// Still fails closed: an unevaluable gate is not a passed gate.
	if got := assignmentStatus(t, h.db, "asg-hook-3"); got != "FAILED" {
		t.Fatalf("status = %q, want FAILED — an unevaluable pre_task_delegation gate must not let the delegation through", got)
	}
	var errMsg string
	if err := h.db.QueryRow(`SELECT COALESCE(error_message,'') FROM assignments WHERE id = ?`, "asg-hook-3").Scan(&errMsg); err != nil {
		t.Fatalf("query error_message: %v", err)
	}
	if strings.Contains(errMsg, "blocked") {
		t.Errorf("error_message = %q — a dispatch failure is being reported as a policy block", errMsg)
	}
	if !strings.Contains(errMsg, "could not be evaluated") {
		t.Errorf("error_message = %q, want it to say the hook could not be evaluated", errMsg)
	}
}
