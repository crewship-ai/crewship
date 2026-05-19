package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
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
