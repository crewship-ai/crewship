package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/journal"
)

// A routine step whose agent is busy (a chat, an assignment or another
// routine holds the agent) waits for the agent instead of failing at once.
// The resolver error marks that the step got past the lock; no model or
// container is reached.
func busyAgentRig(t *testing.T) (*OrchestratorRunner, *chatbridge.AgentRunLock) {
	t.Helper()
	resolver := &orchCovResolver{info: covChatInfo(), resolveErr: errors.New("reached agent resolution")}
	r := newOrchRunnerRig(t, &orchCovContainer{}, resolver)
	r.journalE = &captureEmitter{}
	lock := chatbridge.NewAgentRunLock()
	r.SetAgentRunLock(lock)
	if !lock.TryStart("agent_cov") {
		t.Fatal("could not hold the agent")
	}
	return r, lock
}

var busyAgentStep = AgentStepRequest{
	WorkspaceID: "ws_cov", AuthorCrewID: "crew_cov", AgentSlug: "cov-agent",
	PipelineID: "pipeline-busy", PipelineRunID: "run-busy", StepID: "classify", Attempt: 1,
	PipelineSlug: "accounting",
}

func TestRunStep_BusyAgentWaitsForRelease(t *testing.T) {
	r, lock := busyAgentRig(t)
	go func() {
		time.Sleep(50 * time.Millisecond)
		lock.End("agent_cov")
	}()
	_, err := r.RunStep(context.Background(), busyAgentStep)
	if errors.Is(err, ErrAgentBusy) {
		t.Fatalf("step failed as busy instead of waiting for the agent: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "reached agent resolution") {
		t.Fatalf("step did not proceed after the agent was released: %v", err)
	}
	if lock.InFlight("agent_cov") {
		t.Fatal("step kept the agent after it returned")
	}
	assertAgentWaitEvents(t, r, "acquired")
}

func TestRunStep_BusyAgentWaitIsBounded(t *testing.T) {
	r, lock := busyAgentRig(t)
	r.agentBusyWait = 40 * time.Millisecond
	start := time.Now()
	_, err := r.RunStep(context.Background(), busyAgentStep)
	if !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("want ErrAgentBusy after the wait bound, got %v", err)
	}
	if !strings.Contains(err.Error(), "stayed busy") {
		t.Fatalf("error does not say the agent stayed busy: %v", err)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatal("step gave up before its wait bound")
	}
	// The holder's single release frees the agent: the waiter left no claim.
	lock.End("agent_cov")
	if lock.InFlight("agent_cov") {
		t.Fatal("a step that gave up left a claim on the agent")
	}
	assertAgentWaitEvents(t, r, "timed_out")
}

func TestRunStep_BusyAgentWaitStopsOnCancel(t *testing.T) {
	r, lock := busyAgentRig(t)
	defer lock.End("agent_cov")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	_, err := r.RunStep(ctx, busyAgentStep)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled when the run is stopped while waiting, got %v", err)
	}
	assertAgentWaitEvents(t, r, "cancelled")
}

func assertAgentWaitEvents(t *testing.T, r *OrchestratorRunner, outcome string) {
	t.Helper()
	entries := r.journalE.(*captureEmitter).entries
	if len(entries) != 2 {
		t.Fatalf("want two wait events, got %v", entries)
	}
	var waitID string
	for i, phase := range []string{"waiting", outcome} {
		entry := entries[i]
		p := entry.Payload
		if entry.Type != journal.EntryPipelineStepAgentWait ||
			entry.TraceID != busyAgentStep.PipelineRunID || entry.WorkspaceID != busyAgentStep.WorkspaceID ||
			p["phase"] != phase || p["run_id"] != busyAgentStep.PipelineRunID ||
			p["pipeline_slug"] != busyAgentStep.PipelineSlug ||
			p["step_id"] != busyAgentStep.StepID || p["agent_slug"] != busyAgentStep.AgentSlug ||
			p["attempt"] != busyAgentStep.Attempt {
			t.Fatalf("unexpected %s event: %+v", phase, entry)
		}
		if i == 0 {
			waitID, _ = p["wait_id"].(string)
			continue
		}
		if waitID == "" || p["wait_id"] != waitID || p["duration_ms"] != entry.TS.Sub(entries[0].TS).Milliseconds() {
			t.Fatalf("wait events do not correlate: %+v", entries)
		}
	}
}

// The wait bound follows the step's own timeout; without one it is the
// runner default.
func TestAgentBusyWaitBound(t *testing.T) {
	r := &OrchestratorRunner{}
	if got := r.busyWaitFor(AgentStepRequest{TimeoutSec: 90}); got != 90*time.Second {
		t.Fatalf("step timeout 90s: wait %v", got)
	}
	if got := r.busyWaitFor(AgentStepRequest{}); got != defaultAgentBusyWait {
		t.Fatalf("no step timeout: wait %v, want %v", got, defaultAgentBusyWait)
	}
	r.agentBusyWait = time.Second
	if got := r.busyWaitFor(AgentStepRequest{TimeoutSec: 90}); got != time.Second {
		t.Fatalf("override: wait %v", got)
	}
}
