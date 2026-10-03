package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

type retainedResultProvider struct {
	provider.ContainerProvider
	response string
	code     int
	calls    []provider.ExecConfig
}

func (p *retainedResultProvider) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	p.calls = append(p.calls, cfg)
	return &provider.ExecResult{ExecID: "read-result", Reader: io.NopCloser(strings.NewReader(p.response))}, nil
}
func (p *retainedResultProvider) ExecInspect(context.Context, string) (bool, int, error) {
	return false, p.code, nil
}

func TestRetainedResultProbeRequiresValidatedTransport(t *testing.T) {
	valid := `{"version":1,"sequence":3,"complete":true,"result":{"exit_code":7,"reason":"exited"}}`
	for _, tc := range []struct {
		name, response string
		code           int
		want           bool
	}{
		{"actual-nonzero-result", valid, 0, true},
		{"probe-failed", valid, 125, false},
		{"trailing-output", valid + " trailing", 0, false},
		{"missing-result", `{"version":1,"sequence":3,"complete":true}`, 0, false},
		{"oversized", strings.Repeat("x", 4097), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &retainedResultProvider{response: tc.response, code: tc.code}
			o := &Orchestrator{container: p}
			run := RunState{ID: "run-1", ContainerID: "container", Output: &RunOutputState{Version: 1, Directory: "/runs/run-1/output"}}
			snapshot, err := o.ReadRetainedRunResult(t.Context(), run)
			if (err == nil) != tc.want {
				t.Fatalf("snapshot=%+v err=%v", snapshot, err)
			}
			if tc.want && snapshot.Result.ExitCode != 7 {
				t.Fatal("probe success replaced actual exit code")
			}
			if len(p.calls) != 1 || p.calls[0].Cmd[1] != "run-result" || p.calls[0].User != "1001:1001" {
				t.Fatal("probe launched work or used wrong principal")
			}
		})
	}
}

func TestRetainedResultRecordedWithoutCompletingUnreplayedRun(t *testing.T) {
	state := newMemState()
	o := &Orchestrator{state: state}
	run := RunState{ID: "run-1", Status: "running", Output: &RunOutputState{Version: 1, Directory: "/runs/run-1/output"}}
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err = state.Set(t.Context(), "agent_runs", run.ID, data); err != nil {
		t.Fatal(err)
	}
	snapshot := runoutput.Snapshot{Version: 1, Sequence: 3, Complete: true, Result: &runoutput.Result{ExitCode: 7, Reason: "exited"}}
	updated, err := o.RecordRetainedRunResult(t.Context(), run.ID, run, snapshot)
	if err != nil || updated.Status != "running" || updated.Output.RetainedResult == nil || updated.Output.RetainedResult.Result.ExitCode != 7 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	snapshot.Result = &runoutput.Result{ExitCode: 0, Reason: "exited"}
	if _, err = o.RecordRetainedRunResult(t.Context(), run.ID, run, snapshot); err == nil {
		t.Fatal("terminal result rewritten")
	}
}

func TestRetainedResultCancelledProbeDoesNotExecute(t *testing.T) {
	p := &retainedResultProvider{}
	o := &Orchestrator{container: p}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	run := RunState{ID: "run-1", ContainerID: "container", Output: &RunOutputState{Version: 1, Directory: "/runs/run-1/output"}}
	if _, err := o.ReadRetainedRunResult(ctx, run); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled probe: %v", err)
	}
	if len(p.calls) != 0 {
		t.Fatal("cancelled recovery created an exec")
	}
}

func TestRetainedResultRejectsMismatchedStateKeyAndCorruptPrior(t *testing.T) {
	for _, scenario := range []string{"wrong-key", "corrupt-prior"} {
		t.Run(scenario, func(t *testing.T) {
			state := newMemState()
			o := &Orchestrator{state: state}
			snapshot := runoutput.Snapshot{Version: 1, Sequence: 3, Complete: true, Result: &runoutput.Result{ExitCode: 0, Reason: "exited"}}
			run := RunState{ID: "run-1", Status: "running", Output: &RunOutputState{Version: 1, Directory: "/runs/run-1/output"}}
			key := run.ID
			if scenario == "wrong-key" {
				key = "other-run"
			} else {
				prior := snapshot
				prior.Version = 2
				run.Output.RetainedResult = &prior
			}
			before, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			if err = state.Set(t.Context(), "agent_runs", key, before); err != nil {
				t.Fatal(err)
			}
			if _, err = o.RecordRetainedRunResult(t.Context(), key, run, snapshot); err == nil {
				t.Fatal("invalid recovered state accepted")
			}
			after, err := state.Get(t.Context(), "agent_runs", key)
			if err != nil || string(after) != string(before) {
				t.Fatal("invalid record mutated")
			}
		})
	}
}
