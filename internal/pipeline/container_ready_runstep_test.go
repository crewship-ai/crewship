package pipeline

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// TestOrchestratorRunner_RunStep_EmitsOneContainerReady drives the real
// RunStep path (not just the emit helper) and asserts that a successful
// EnsureCrewRuntime produces EXACTLY ONE pipeline.step.container_ready record.
// Without this the emit block in runner_orchestrator.go could be deleted and no
// test would go red — only the helper was covered before.
//
// The record is necessarily ordered AFTER pipeline.step.started: the executor
// emits step.started before it hands the step to the runner, and the runner
// emits container_ready mid-RunStep — so the ordering is structural. Here we
// assert the runner emits the container_ready and nothing spurious alongside it.
func TestOrchestratorRunner_RunStep_EmitsOneContainerReady(t *testing.T) {
	container := &orchCovContainer{
		agentStream: "hello\n" +
			`{"type":"result","subtype":"success","total_cost_usd":0.1,"usage":{"input_tokens":1,"output_tokens":2}}` + "\n",
	}
	resolver := &orchCovResolver{info: covChatInfo()}
	r := newOrchRunnerRig(t, container, resolver)
	em := &captureEmitter{}
	r.journalE = em

	_, err := r.RunStep(context.Background(), AgentStepRequest{
		WorkspaceID:   "ws_cov",
		AuthorCrewID:  "crew_cov",
		AgentSlug:     "cov-agent",
		Prompt:        "go",
		PipelineID:    "pln_cov",
		PipelineRunID: "run_cov",
		StepID:        "s1",
		Attempt:       1,
	})
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}

	var ready []journal.Entry
	for _, e := range em.entries {
		if e.Type == journal.EntryPipelineStepContainerReady {
			ready = append(ready, e)
		}
	}
	if len(ready) != 1 {
		t.Fatalf("want exactly 1 pipeline.step.container_ready, got %d (types: %v)", len(ready), em.typesEmitted())
	}
	e := ready[0]
	if e.Payload["step_id"] != "s1" {
		t.Errorf("payload.step_id = %v, want s1", e.Payload["step_id"])
	}
	if e.TraceID != "run_cov" {
		t.Errorf("trace_id = %q, want run_cov (so routine logs can pull it)", e.TraceID)
	}
	if _, ok := e.Payload["duration_ms"]; !ok {
		t.Errorf("payload missing duration_ms: %v", e.Payload)
	}
}

// TestOrchestratorRunner_RunStep_ContainerReadyCarriesAttempt is finding #2: a
// transient-retry re-entry emits a second record for the SAME step_id, so the
// attempt number must ride the payload to keep the two apart. RunStep stamps it
// from the request (set by runRunnerWithTransientRetry).
func TestOrchestratorRunner_RunStep_ContainerReadyCarriesAttempt(t *testing.T) {
	container := &orchCovContainer{
		agentStream: "hello\n" +
			`{"type":"result","subtype":"success"}` + "\n",
	}
	resolver := &orchCovResolver{info: covChatInfo()}
	r := newOrchRunnerRig(t, container, resolver)
	em := &captureEmitter{}
	r.journalE = em

	if _, err := r.RunStep(context.Background(), AgentStepRequest{
		WorkspaceID:   "ws_cov",
		AuthorCrewID:  "crew_cov",
		AgentSlug:     "cov-agent",
		Prompt:        "go",
		PipelineID:    "pln_cov",
		PipelineRunID: "run_cov",
		StepID:        "s1",
		Attempt:       2, // a retry
	}); err != nil {
		t.Fatalf("RunStep: %v", err)
	}

	for _, e := range em.entries {
		if e.Type == journal.EntryPipelineStepContainerReady {
			if e.Payload["attempt"] != 2 {
				t.Fatalf("payload.attempt = %v, want 2", e.Payload["attempt"])
			}
			return
		}
	}
	t.Fatal("no container_ready entry emitted")
}
