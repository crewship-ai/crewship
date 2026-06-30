package telemetry

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestRoutineSpans confirms the helpers emit spans with the expected name
// and attribute set. A noop tracer (the package default until OTel.Init
// runs) returns a noop span which is hard to introspect — install an
// in-memory exporter through a stdouttrace TracerProvider so we can
// assert on captured spans.
func TestRoutineSpans(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	ctx, runSpan := StartRoutineRunSpan(context.Background(), "nightly-report", "run-123", "pipe-456")
	_, stepSpan := StartRoutineStepSpan(ctx, "step-summarize", "agent_run", 2)
	stepSpan.End()
	runSpan.End()

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}

	// Spans are ordered newest-first by the exporter — child span ended first.
	step := spans[0]
	if step.Name != "routine.step" {
		t.Errorf("step span name = %q, want routine.step", step.Name)
	}
	stepAttrs := map[string]any{}
	for _, kv := range step.Attributes {
		stepAttrs[string(kv.Key)] = kv.Value.AsInterface()
	}
	if stepAttrs[AttrCrewshipRoutineStepID] != "step-summarize" {
		t.Errorf("step id attr = %v, want step-summarize", stepAttrs[AttrCrewshipRoutineStepID])
	}
	if stepAttrs[AttrCrewshipRoutineStepType] != "agent_run" {
		t.Errorf("step type attr = %v, want agent_run", stepAttrs[AttrCrewshipRoutineStepType])
	}
	if stepAttrs[AttrCrewshipRoutineStepAttempt] != int64(2) {
		t.Errorf("step attempt attr = %v, want 2", stepAttrs[AttrCrewshipRoutineStepAttempt])
	}

	run := spans[1]
	if run.Name != "routine.run" {
		t.Errorf("run span name = %q, want routine.run", run.Name)
	}
	runAttrs := map[string]any{}
	for _, kv := range run.Attributes {
		runAttrs[string(kv.Key)] = kv.Value.AsInterface()
	}
	if runAttrs[AttrCrewshipRoutineSlug] != "nightly-report" {
		t.Errorf("slug = %v, want nightly-report", runAttrs[AttrCrewshipRoutineSlug])
	}
	if runAttrs[AttrCrewshipRoutineRunID] != "run-123" {
		t.Errorf("run id = %v, want run-123", runAttrs[AttrCrewshipRoutineRunID])
	}
	if runAttrs[AttrCrewshipRoutinePipelineID] != "pipe-456" {
		t.Errorf("pipeline id = %v, want pipe-456", runAttrs[AttrCrewshipRoutinePipelineID])
	}

	// Parent/child relationship — the step's parent span ID matches the run span ID.
	if step.Parent.SpanID() != run.SpanContext.SpanID() {
		t.Errorf("step parent span id %s != run span id %s",
			step.Parent.SpanID(), run.SpanContext.SpanID())
	}
}

func TestRecordRunAgentToolSpan(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	ctx, runSpan := StartRoutineRunSpan(context.Background(), "nightly", "run-9", "pipe-9")
	stepCtx, stepSpan := StartRoutineStepSpan(ctx, "step-x", "agent_run", 0)

	start := time.Now().Add(-time.Second)
	RecordRunAgentToolSpan(stepCtx, "bash", "Bash", 0, start, 250, "ok",
		map[string]string{"tool": "Bash", "model": "claude-opus-4-8"})
	RecordRunAgentToolSpan(stepCtx, "mcp_tool", "save_routine", 1, start, 30, "error",
		map[string]string{"tool": "mcp__crewship-routines__save_routine"})

	stepSpan.End()
	runSpan.End()

	spans := exp.GetSpans()
	// 2 tool spans + step + run.
	if len(spans) != 4 {
		t.Fatalf("expected 4 spans, got %d", len(spans))
	}

	var tool, step tracetest.SpanStub
	for _, s := range spans {
		switch s.Name {
		case "routine.tool":
			if attrOf(s, AttrCrewshipRoutineToolStatus) == "ok" {
				tool = s
			}
		case "routine.step":
			step = s
		}
	}
	if tool.Name != "routine.tool" {
		t.Fatalf("did not find the ok routine.tool span")
	}
	// Tool span nests under the STEP span (run → step → tool).
	if tool.Parent.SpanID() != step.SpanContext.SpanID() {
		t.Errorf("tool parent %s != step span %s", tool.Parent.SpanID(), step.SpanContext.SpanID())
	}
	if attrOf(tool, AttrCrewshipRoutineToolKind) != "bash" {
		t.Errorf("tool kind = %v, want bash", attrOf(tool, AttrCrewshipRoutineToolKind))
	}
	if attrOf(tool, "crewship.routine.tool.model") != "claude-opus-4-8" {
		t.Errorf("tool model attr = %v", attrOf(tool, "crewship.routine.tool.model"))
	}
	// Backdated duration: ~250ms, not zero-width.
	if d := tool.EndTime.Sub(tool.StartTime).Milliseconds(); d != 250 {
		t.Errorf("tool span duration = %dms, want 250", d)
	}
	// The errored tool span must exist AND carry codes.Error.
	foundErrorSpan := false
	for _, s := range spans {
		if s.Name == "routine.tool" && attrOf(s, AttrCrewshipRoutineToolStatus) == "error" {
			foundErrorSpan = true
			if s.Status.Code != codes.Error {
				t.Errorf("error tool span status = %v, want Error", s.Status.Code)
			}
		}
	}
	if !foundErrorSpan {
		t.Fatalf("did not find the error routine.tool span")
	}
}

func attrOf(s tracetest.SpanStub, key string) any {
	for _, kv := range s.Attributes {
		if string(kv.Key) == key {
			return kv.Value.AsInterface()
		}
	}
	return nil
}
