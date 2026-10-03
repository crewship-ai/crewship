package orchestrator

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"
)

type reviewAdmissionState struct {
	*memState
	admit func()
}

func (s *reviewAdmissionState) List(ctx context.Context, bucket string) (map[string][]byte, error) {
	if s.admit != nil {
		f := s.admit
		s.admit = nil
		f()
	}
	return s.memState.List(ctx, bucket)
}

func TestStopAgentDoesNotTreatConcurrentLiveAdmissionAsRecovered(t *testing.T) {
	release := make(chan struct{})
	close(release)
	c := stopReviewContainer{seen: make(chan struct{}, 1), release: release}
	state := &reviewAdmissionState{memState: newMemState()}
	o := New(c, state, slog.Default())
	req := AgentRunRequest{AgentID: "a", AgentSlug: "a", ContainerID: "fixture", RunID: NewRunID()}
	state.admit = func() {
		_, finish, trackErr := o.trackAgentRun(t.Context(), &req)
		if trackErr != nil {
			t.Fatal(trackErr)
		}
		t.Cleanup(finish)
		raw, err := json.Marshal(RunState{ID: req.RunID, AgentID: req.AgentID, AgentSlug: req.AgentSlug, ContainerID: req.ContainerID, Status: "running"})
		if err != nil {
			t.Fatal(err)
		}
		if err := state.Set(t.Context(), "agent_runs", req.RunID, raw); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	stopErr := o.StopAgent(ctx, "a")
	raw, err := state.Get(t.Context(), "agent_runs", req.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var run RunState
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	if run.Status != "running" {
		t.Fatalf("a live admission after the stop snapshot was treated as recovered: %s", run.Status)
	}
	if err := req.ExecGate(t.Context()); err != nil {
		t.Fatalf("a later admission should remain independent: %v", err)
	}
	if stopErr == nil {
		t.Fatal("no invocation in the original snapshot was stopped, but stop was acknowledged")
	}
}
