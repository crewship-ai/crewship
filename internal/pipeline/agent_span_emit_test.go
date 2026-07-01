package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

func TestEmitRunAgentSpan_JournalShape(t *testing.T) {
	em := &captureEmitter{}
	span := orchestrator.RunAgentSpan{
		RunID:      "run_42",
		StepID:     "summarize",
		Seq:        3,
		Kind:       "mcp_tool",
		Name:       "save_routine",
		Detail:     "save_routine",
		StartedAt:  time.Date(2026, 6, 30, 9, 0, 0, 0, time.UTC),
		DurationMs: 120,
		Status:     "error",
		Attributes: map[string]string{"tool": "mcp__crewship-routines__save_routine", "model": "claude-opus-4-8"},
	}

	emitRunAgentSpan(context.Background(), em, "ws_1", "crew_1", span)

	if len(em.entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(em.entries))
	}
	e := em.entries[0]
	if e.Type != journal.EntryRunAgentSpan {
		t.Errorf("type = %q, want %q", e.Type, journal.EntryRunAgentSpan)
	}
	if e.WorkspaceID != "ws_1" || e.CrewID != "crew_1" {
		t.Errorf("workspace/crew = %q/%q", e.WorkspaceID, e.CrewID)
	}
	// trace_id AND actor_id must both anchor to the run so the runs API can
	// pull the sub-spans back via the trace_id index.
	if e.TraceID != "run_42" || e.ActorID != "run_42" {
		t.Errorf("trace_id/actor_id = %q/%q, want run_42", e.TraceID, e.ActorID)
	}
	if e.ActorType != journal.ActorOrchestrator {
		t.Errorf("actor_type = %q", e.ActorType)
	}
	// Error tool → warn severity.
	if e.Severity != journal.SeverityWarn {
		t.Errorf("severity = %q, want warn for errored tool", e.Severity)
	}
	if e.Payload["step_id"] != "summarize" {
		t.Errorf("payload.step_id = %v", e.Payload["step_id"])
	}
	if e.Payload["seq"] != 3 {
		t.Errorf("payload.seq = %v, want 3", e.Payload["seq"])
	}
	if e.Payload["kind"] != "mcp_tool" || e.Payload["name"] != "save_routine" {
		t.Errorf("payload kind/name = %v/%v", e.Payload["kind"], e.Payload["name"])
	}
	if e.Payload["status"] != "error" {
		t.Errorf("payload.status = %v", e.Payload["status"])
	}
	if e.Payload["run_id"] != "run_42" {
		t.Errorf("payload.run_id = %v (needed for the v120 virtual run_id column)", e.Payload["run_id"])
	}
	attrs, ok := e.Payload["attributes"].(map[string]string)
	if !ok || attrs["model"] != "claude-opus-4-8" {
		t.Errorf("payload.attributes = %v", e.Payload["attributes"])
	}
}

func TestEmitRunAgentSpan_NilEmitterSafe(t *testing.T) {
	// Must not panic with a nil emitter (zero/disabled path).
	emitRunAgentSpan(context.Background(), nil, "ws", "crew", orchestrator.RunAgentSpan{RunID: "r"})
}

func TestEmitRunAgentSpan_OkSeverityInfo(t *testing.T) {
	em := &captureEmitter{}
	emitRunAgentSpan(context.Background(), em, "ws", "crew", orchestrator.RunAgentSpan{
		RunID: "r", StepID: "s", Kind: "bash", Name: "Bash", Status: "ok",
	})
	if em.entries[0].Severity != journal.SeverityInfo {
		t.Errorf("severity = %q, want info for ok tool", em.entries[0].Severity)
	}
}
