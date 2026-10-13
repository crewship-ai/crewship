package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDurableRunStateFailurePreventsLaunch(t *testing.T) {
	state := &covFailSetState{memState: newMemState()}
	container := covNewRunContainer(covRunOpts{stream: "{}\n"})
	o := New(container, state, covQuietLogger())
	req := covRunReq()
	req.DurableOutputDir = "/persistent/runs"
	gateCalled := false
	req.ExecGate = func(_ context.Context) error { gateCalled = true; return nil }
	err := o.RunAgent(t.Context(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "persist durable execution before launch") {
		t.Fatalf("persistence failure not fatal: %v", err)
	}
	if gateCalled {
		t.Fatal("process creation was admitted without durable identity")
	}
	for _, call := range container.snapshotCalls() {
		if strings.Contains(strings.Join(call.Cmd, " "), "run-launch") {
			t.Fatal("launched after persistence failure")
		}
	}
}

func TestDuplicateRunAdmissionPreservesOriginalControl(t *testing.T) {
	o := New(covNewRunContainer(covRunOpts{}), newMemState(), covQuietLogger())
	req := covRunReq()
	ctx, finish, err := o.trackAgentRun(t.Context(), &req)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	original, _ := o.agentRuns.Load(req.RunID)
	duplicate := covRunReq()
	_, cleanup, err := o.trackAgentRun(t.Context(), &duplicate)
	if !errors.Is(err, ErrRunAlreadyOwned) || cleanup != nil {
		t.Fatalf("duplicate admission: %v", err)
	}
	current, _ := o.agentRuns.Load(req.RunID)
	if current != original || ctx.Err() != nil {
		t.Fatal("duplicate replaced or cancelled original owner")
	}
}

func TestDurableRunCannotOverwriteRecoveredIdentity(t *testing.T) {
	state := newMemState()
	o := New(covNewRunContainer(covRunOpts{}), state, covQuietLogger())
	req := covRunReq()
	req.DurableOutputDir = "/persistent/runs"
	original := []byte(`{"id":"run-cov1","status":"running"}`)
	if err := state.Set(t.Context(), "agent_runs", req.RunID, original); err != nil {
		t.Fatal(err)
	}
	_, _, err := o.trackAgentRun(t.Context(), &req)
	if !errors.Is(err, ErrRunAlreadyOwned) {
		t.Fatalf("recovered identity accepted for launch: %v", err)
	}
	got, _ := state.Get(t.Context(), "agent_runs", req.RunID)
	if string(got) != string(original) {
		t.Fatal("recovered identity overwritten")
	}
}

func TestDurableOutputCannotBeCancelledByAbsenceOnly(t *testing.T) {
	state := newMemState()
	o := New(covNewRunContainer(covRunOpts{}), state, covQuietLogger())
	run := RunState{ID: "retained", Status: "running", Output: &RunOutputState{Version: 1, Directory: "/persistent/retained/output"}}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err = state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
		t.Fatal(err)
	}
	changed, err := o.RecordRecoveredAbsence(t.Context(), run.ID, run)
	if err != nil || changed {
		t.Fatalf("retained output terminalized by absence: changed=%v err=%v", changed, err)
	}
}
