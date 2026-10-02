//go:build linux

package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os/exec"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// startDirectRun starts a real process the way a direct run does and persists
// its state as RunAgent would, without an in-process owner — exactly what a
// server restart leaves behind while tmux/direct processes keep running.
func startDirectRun(t *testing.T, o *Orchestrator, st *memState, agentID, slug string, recordSlug bool) (RunState, *exec.Cmd, chan error) {
	t.Helper()
	runID := NewRunID()
	args := directRunCommand(runID, []string{"sleep", "60"})
	cmd := exec.Command(args[0], args[1:]...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = exec.Command("rm", "-f", directRunPIDFile(runID)).Run()
	})
	loc := RunLocation{ContainerID: "test", AgentSlug: slug, RunID: runID}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if alive, err := o.RunIsAliveAt(context.Background(), loc); err == nil && alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never became alive")
		}
		time.Sleep(20 * time.Millisecond)
	}
	rs := RunState{ID: runID, AgentID: agentID, Status: "running", StartedAt: time.Now(), ContainerID: "test"}
	if recordSlug {
		rs.AgentSlug = slug
	}
	b, _ := json.Marshal(rs)
	_ = st.Set(context.Background(), "agent_runs", runID, b)
	return rs, cmd, done
}

func runStatus(t *testing.T, st *memState, runID string) string {
	t.Helper()
	b, _ := st.Get(context.Background(), "agent_runs", runID)
	var rs RunState
	_ = json.Unmarshal(b, &rs)
	return rs.Status
}

// A08 + A10 (operational): deleting one agent stops its live runs — found in
// persisted state with no in-process owner, as after a restart — and only
// its runs; another agent's run keeps going. A run is only marked cancelled
// once its process is confirmed gone.
func TestStopDeletedAgentRunsStopsOnlyTheDeletedAgent(t *testing.T) {
	st := newMemState()
	o := New(stopProcessContainer{}, st, slog.Default())
	deleted, _, deletedDone := startDirectRun(t, o, st, "agent-gone", "gone", true)
	// Persisted before RunState carried the slug: the lookup supplies it.
	legacy, _, legacyDone := startDirectRun(t, o, st, "agent-gone", "gone", false)
	live, _, liveDone := startDirectRun(t, o, st, "agent-live", "live", true)

	lookup := func(_ context.Context, id string) (bool, string, error) {
		return id == "agent-gone", map[string]string{"agent-gone": "gone", "agent-live": "live"}[id], nil
	}
	deadline := time.Now().Add(10 * time.Second)
	var res DeletedAgentRunsResult
	for {
		r := o.StopDeletedAgentRuns(context.Background(), "", lookup)
		res.Stopped += r.Stopped
		if r.Pending == 0 && res.Stopped == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("deleted agent runs not stopped: %+v", r)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, d := range []chan error{deletedDone, legacyDone} {
		select {
		case <-d:
		case <-time.After(2 * time.Second):
			t.Fatal("a deleted agent's process kept running")
		}
	}
	select {
	case <-liveDone:
		t.Fatal("the live agent's run was stopped")
	case <-time.After(300 * time.Millisecond):
	}
	if got := runStatus(t, st, deleted.ID); got != "cancelled" {
		t.Errorf("deleted run status %q", got)
	}
	if got := runStatus(t, st, legacy.ID); got != "cancelled" {
		t.Errorf("legacy deleted run status %q", got)
	}
	if got := runStatus(t, st, live.ID); got != "running" {
		t.Errorf("live run status %q", got)
	}
}

// A lookup that cannot answer keeps the run pending; it is never assumed
// stopped or left alone silently.
func TestStopDeletedAgentRunsUnknownOwnerStaysPending(t *testing.T) {
	st := newMemState()
	o := New(stopProcessContainer{}, st, slog.Default())
	rs, _, done := startDirectRun(t, o, st, "agent-x", "x", true)
	res := o.StopDeletedAgentRuns(context.Background(), "", func(context.Context, string) (bool, string, error) {
		return false, "", errors.New("database unavailable")
	})
	if res.Pending != 1 || res.Stopped != 0 || len(res.Errors) == 0 {
		t.Fatalf("%+v", res)
	}
	select {
	case <-done:
		t.Fatal("process stopped on an unanswered lookup")
	case <-time.After(200 * time.Millisecond):
	}
	if got := runStatus(t, st, rs.ID); got != "running" {
		t.Fatalf("status %q", got)
	}
}

// The creation boundary refuses a deleted agent even when its run was
// accepted earlier: nothing is created.
func TestAgentLivenessRefusesExecAtTheGate(t *testing.T) {
	o := New(&mockContainer{}, newMemState(), slog.Default())
	o.SetAgentLiveness(func(_ context.Context, id string) error {
		if id == "agent-gone" {
			return errors.New("agent was deleted")
		}
		return nil
	})
	for id, wantErr := range map[string]bool{"agent-gone": true, "agent-live": false} {
		req := AgentRunRequest{AgentID: id, AgentSlug: id, RunID: NewRunID(), ContainerID: "c"}
		ctx, finish := o.trackAgentRun(context.Background(), &req)
		err := req.ExecGate(ctx)
		finish()
		if (err != nil) != wantErr {
			t.Errorf("%s: gate err=%v", id, err)
		}
	}
}

// A run without an agent id has no agents row to be deleted from; the
// liveness check must not turn that into a refusal (agentMayRun fails closed
// on a missing row).
func TestAgentLivenessSkipsARunWithoutAnAgentID(t *testing.T) {
	o := New(&mockContainer{}, newMemState(), slog.Default())
	called := false
	o.SetAgentLiveness(func(context.Context, string) error {
		called = true
		return errors.New("no such agent")
	})
	req := AgentRunRequest{AgentSlug: "s", RunID: NewRunID(), ContainerID: "c"}
	ctx, finish := o.trackAgentRun(context.Background(), &req)
	defer finish()
	if err := req.ExecGate(ctx); err != nil || called {
		t.Fatalf("gate err=%v, liveness called=%v", err, called)
	}
}

var _ provider.ContainerProvider = stopProcessContainer{}

type failingCancellationState struct{ *memState }

func (s failingCancellationState) Set(context.Context, string, string, []byte) error {
	return errors.New("runtime state disk unavailable")
}
func TestDeletedAgentStopRequiresDurableCancellation(t *testing.T) {
	state := newMemState()
	run := RunState{ID: NewRunID(), AgentID: "gone", AgentSlug: "gone", ContainerID: "test", Status: "running"}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
		t.Fatal(err)
	}
	o := New(stopProcessContainer{}, failingCancellationState{state}, slog.Default())
	lookup := func(context.Context, string) (bool, string, error) { return true, "gone", nil }
	res := o.StopDeletedAgentRuns(t.Context(), "gone", lookup)
	if res.Stopped != 0 || res.Pending != 1 || len(res.Errors) != 1 {
		t.Fatalf("failed durable cancellation acknowledged: %+v", res)
	}
	if got := runStatus(t, state, run.ID); got != "running" {
		t.Fatal(got)
	}
	// A later pass can persist the same already-absent runtime exactly once.
	recovered := New(stopProcessContainer{}, state, slog.Default())
	res = recovered.StopDeletedAgentRuns(t.Context(), "gone", lookup)
	if res.Stopped != 1 || res.Pending != 0 || len(res.Errors) != 0 {
		t.Fatalf("retry: %+v", res)
	}
	if got := runStatus(t, state, run.ID); got != "cancelled" {
		t.Fatal(got)
	}
}
