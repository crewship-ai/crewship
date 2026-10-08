//go:build linux

package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
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
	cmd, done, output := startDirectRunProcess(t, runID, []string{"sleep", "60"})
	loc := RunLocation{ContainerID: "test", AgentSlug: slug, RunID: runID}
	if err := awaitDirectRunAlive(o, loc, done, output, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	rs := RunState{ID: runID, AgentID: agentID, Status: "running", StartedAt: time.Now(), ContainerID: "test"}
	if recordSlug {
		rs.AgentSlug = slug
	}
	b, _ := json.Marshal(rs)
	_ = st.Set(context.Background(), "agent_runs", runID, b)
	return rs, cmd, done
}

// startDirectRunProcess launches argv under the direct-run wrapper with its
// combined output captured, so a fixture that dies during startup can say why.
func startDirectRunProcess(t *testing.T, runID string, argv []string) (*exec.Cmd, chan error, startupOutput) {
	t.Helper()
	args := directRunCommand(runID, argv)
	cmd := exec.Command(args[0], args[1:]...)
	f, err := os.CreateTemp(t.TempDir(), "direct-run-output-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = exec.Command("rm", "-f", directRunPIDFile(runID)).Run()
	})
	return cmd, done, startupOutput{path: f.Name()}
}

// awaitDirectRunAlive polls the probe until the run reads alive. It fails
// with the evidence a bare "never became alive" used to throw away (#2892):
// whether the wrapper exited and with what status, its output, the last probe
// answer and the PID file it was supposed to write. done is drained only when
// the process has exited; the exit status is put back for the caller.
func awaitDirectRunAlive(o *Orchestrator, loc RunLocation, done chan error, output startupOutput, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var alive bool
	var probeErr error
	for {
		alive, probeErr = o.RunIsAliveAt(context.Background(), loc)
		if probeErr == nil && alive {
			return nil
		}
		select {
		case waitErr := <-done:
			done <- waitErr
			return fmt.Errorf("run never became alive: wrapper exited during startup (%s); last probe alive=%v err=%v; %s; output: %q",
				exitDescription(waitErr), alive, probeErr, pidFileEvidence(loc.RunID), output.String())
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("run never became alive within %s: wrapper still running; last probe alive=%v err=%v; %s; output: %q",
				timeout, alive, probeErr, pidFileEvidence(loc.RunID), output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func exitDescription(err error) string {
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return "exit status 0"
	case errors.As(err, &exitErr):
		return exitErr.Error()
	default:
		return "wait: " + err.Error()
	}
}

func pidFileEvidence(runID string) string {
	b, err := os.ReadFile(directRunPIDFile(runID))
	if err != nil {
		return "pid file: " + err.Error()
	}
	return fmt.Sprintf("pid file: %q", strings.TrimSpace(string(b)))
}

// startupOutput is the wrapper's stdout+stderr. A file rather than a pipe:
// with a pipe, Wait would also wait for the setsid child holding it open.
type startupOutput struct{ path string }

func (o startupOutput) String() string {
	b, err := os.ReadFile(o.path)
	if err != nil {
		return "(unreadable: " + err.Error() + ")"
	}
	return string(b)
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

func (s failingCancellationState) Update(context.Context, string, string, func([]byte) ([]byte, error)) error {
	return errors.New("runtime state disk unavailable")
}
