package pipeline

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestEmitStepContainerReady_JournalShape(t *testing.T) {
	em := &captureEmitter{}
	emitStepContainerReady(context.Background(), em, "ws_1", "crew_1", containerReady{
		RunID:       "run_42",
		PipelineID:  "pl_1",
		StepID:      "fetch",
		ContainerID: "ctr_abc",
		DurationMs:  4200,
	})

	if len(em.entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(em.entries))
	}
	e := em.entries[0]
	if e.Type != journal.EntryPipelineStepContainerReady {
		t.Errorf("type = %q, want %q", e.Type, journal.EntryPipelineStepContainerReady)
	}
	if e.WorkspaceID != "ws_1" || e.CrewID != "crew_1" {
		t.Errorf("workspace/crew = %q/%q", e.WorkspaceID, e.CrewID)
	}
	// trace_id + actor_id anchor to the run so the runs/logs API pulls it via
	// the trace_id index alongside the step spans.
	if e.TraceID != "run_42" || e.ActorID != "run_42" {
		t.Errorf("trace_id/actor_id = %q/%q, want run_42", e.TraceID, e.ActorID)
	}
	if e.Payload["step_id"] != "fetch" {
		t.Errorf("payload.step_id = %v", e.Payload["step_id"])
	}
	// duration_ms is the load-bearing field — routine logs surfaces it as the
	// duration column so claim→first-step is a direct read.
	if e.Payload["duration_ms"] != int64(4200) {
		t.Errorf("payload.duration_ms = %v (want int64 4200)", e.Payload["duration_ms"])
	}
	if e.Payload["run_id"] != "run_42" {
		t.Errorf("payload.run_id = %v", e.Payload["run_id"])
	}
}

func TestEmitStepContainerReady_NilEmitterSafe(t *testing.T) {
	emitStepContainerReady(context.Background(), nil, "ws", "crew", containerReady{RunID: "r"})
}

// A step outside a routine run (no run_id/step_id) must not emit — the metric
// is only meaningful for a routine step.
func TestEmitStepContainerReady_SkipsWhenNotAStep(t *testing.T) {
	em := &captureEmitter{}
	emitStepContainerReady(context.Background(), em, "ws", "crew", containerReady{DurationMs: 10})
	if len(em.entries) != 0 {
		t.Fatalf("expected 0 entries when run_id/step_id absent, got %d", len(em.entries))
	}
}
