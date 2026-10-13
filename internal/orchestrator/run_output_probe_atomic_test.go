package orchestrator

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

// A competing terminal writer runs after Get for a Get/Set implementation,
// or immediately before Update for the transactional implementation. Only the
// transaction can preserve that writer's outcome without resurrecting the run.
func TestRetainedResultPreservesConcurrentTerminalOutcome(t *testing.T) {
	for _, status := range []string{"completed", "cancelled", "error"} {
		t.Run(status, func(t *testing.T) {
			state, err := bbolt.New(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			expected := RunState{ID: "retained-run", Status: "running", ContainerID: "original", Output: &RunOutputState{Version: 1, Directory: "/runs/retained-run/output"}}
			initial, err := json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			if err = state.Set(t.Context(), "agent_runs", expected.ID, initial); err != nil {
				t.Fatal(err)
			}
			terminal := expected
			terminal.Status, terminal.StopOrigin = status, "concurrent-authority"
			final, err := json.Marshal(terminal)
			if err != nil {
				t.Fatal(err)
			}
			wrapped := recoveredCompletionState{StateProvider: state, atomic: state, completed: final}
			o := &Orchestrator{state: wrapped}
			snapshot := runoutput.Snapshot{Version: 1, Sequence: 3, Complete: true, Result: &runoutput.Result{ExitCode: 0, Reason: "exited"}}
			if _, err = o.RecordRetainedRunResult(t.Context(), expected.ID, expected, snapshot); err != nil {
				t.Fatal(err)
			}
			got, err := state.Get(t.Context(), "agent_runs", expected.ID)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(final) {
				t.Fatalf("concurrent terminal result overwritten: %s", got)
			}
		})
	}
}

func TestRetainedResultDoesNotRecreateDeletedIdentity(t *testing.T) {
	state := newMemState()
	o := &Orchestrator{state: state}
	expected := RunState{ID: "deleted-run", Status: "running", Output: &RunOutputState{Version: 1, Directory: "/runs/deleted-run/output"}}
	snapshot := runoutput.Snapshot{Version: 1, Sequence: 2, Complete: true, Result: &runoutput.Result{ExitCode: 0, Reason: "exited"}}
	if _, err := o.RecordRetainedRunResult(t.Context(), expected.ID, expected, snapshot); err != nil {
		t.Fatal(err)
	}
	got, err := state.Get(t.Context(), "agent_runs", expected.ID)
	if err != nil || got != nil {
		t.Fatalf("deleted identity recreated: %s %v", got, err)
	}
}

func TestRetainedResultRefusesNonAtomicState(t *testing.T) {
	state := newMemState()
	// Expose only the public StateProvider methods to model an adapter which
	// cannot perform the read/modify/write under one storage transaction.
	o := &Orchestrator{state: struct{ provider.StateProvider }{state}}
	expected := RunState{ID: "run", Status: "running"}
	snapshot := runoutput.Snapshot{Version: 1, Sequence: 2, Complete: true, Result: &runoutput.Result{ExitCode: 0, Reason: "exited"}}
	if _, err := o.RecordRetainedRunResult(t.Context(), expected.ID, expected, snapshot); err == nil {
		t.Fatal("non-atomic retained write accepted")
	}
}

func TestRetainedResultPreservesReboundAuthority(t *testing.T) {
	for _, field := range []string{"authority", "endpoint", "crew", "mission"} {
		t.Run(field, func(t *testing.T) {
			state, err := bbolt.New(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			expected := RunState{ID: "retained-rebound", Status: "running", CrewID: "crew-original", MissionID: "mission-original", Output: &RunOutputState{Version: 2, Directory: "/runs/retained-rebound/output", Endpoint: "unix:///original", Authority: json.RawMessage(`{"start":"original"}`)}}
			original, _ := json.Marshal(expected)
			if err := state.Set(t.Context(), "agent_runs", expected.ID, original); err != nil {
				t.Fatal(err)
			}
			rebound := expected
			copyOutput := *expected.Output
			rebound.Output = &copyOutput
			switch field {
			case "authority":
				rebound.Output.Authority = json.RawMessage(`{"start":"replacement"}`)
			case "endpoint":
				rebound.Output.Endpoint = "unix:///replacement"
			case "crew":
				rebound.CrewID = "crew-replacement"
			case "mission":
				rebound.MissionID = "mission-replacement"
			}
			replacement, _ := json.Marshal(rebound)
			o := &Orchestrator{state: recoveredCompletionState{StateProvider: state, atomic: state, completed: replacement}}
			snapshot := runoutput.Snapshot{Version: 1, Sequence: 2, Complete: true, Result: &runoutput.Result{ExitCode: 0, Reason: "exited"}}
			if _, err := o.RecordRetainedRunResult(t.Context(), expected.ID, expected, snapshot); err != nil {
				t.Fatal(err)
			}
			got, err := state.Get(t.Context(), "agent_runs", expected.ID)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(replacement) {
				t.Fatalf("stale %s source modified rebound identity: %s", field, got)
			}
		})
	}
}
