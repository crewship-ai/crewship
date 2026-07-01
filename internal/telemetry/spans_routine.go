package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Routine-step attribute keys. These belong to the crewship.* namespace
// because routines are a Crewship-specific concept — the GenAI semconv
// has no analogue for "a versioned, scheduled, multi-step workflow."
// Operators querying their collector for "all routines named X" use
// these attributes; renaming requires a coordinated dashboard update.
const (
	AttrCrewshipRoutineSlug        = "crewship.routine.slug"
	AttrCrewshipRoutineRunID       = "crewship.routine.run_id"
	AttrCrewshipRoutinePipelineID  = "crewship.routine.pipeline_id"
	AttrCrewshipRoutineStepID      = "crewship.routine.step.id"
	AttrCrewshipRoutineStepType    = "crewship.routine.step.type"
	AttrCrewshipRoutineStepAttempt = "crewship.routine.step.attempt"

	// Sub-span (tool) attribute keys. A run_agent step's INTERNAL actions —
	// the agent's individual tool calls — nest under the step span as
	// `routine.tool` children, so an OTEL waterfall reads run → step → tool.
	AttrCrewshipRoutineToolKind   = "crewship.routine.tool.kind"
	AttrCrewshipRoutineToolName   = "crewship.routine.tool.name"
	AttrCrewshipRoutineToolSeq    = "crewship.routine.tool.seq"
	AttrCrewshipRoutineToolStatus = "crewship.routine.tool.status"
)

// StartRoutineRunSpan opens the outermost span for a single routine
// invocation. Every step span below it becomes a child via context
// propagation. pipelineID is the immutable version pointer (so a trace
// can be re-run against the exact same DSL even after the routine has
// been edited); runID is the per-execution UUID that ties the span to
// journal entries and eval_runs.
func StartRoutineRunSpan(ctx context.Context, routineSlug, runID, pipelineID string) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.String(AttrCrewshipRoutineSlug, routineSlug),
	}
	if runID != "" {
		attrs = append(attrs, attribute.String(AttrCrewshipRoutineRunID, runID))
	}
	if pipelineID != "" {
		attrs = append(attrs, attribute.String(AttrCrewshipRoutinePipelineID, pipelineID))
	}
	return otel.Tracer(tracerName).Start(ctx, "routine.run",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...),
	)
}

// StartRoutineStepSpan opens a span around one step execution. The step
// span carries enough metadata that a debugger can reproduce the exact
// invocation from journal data alone (step.id is the immutable DSL key,
// step.type tells the consumer whether this was an LLM call worth
// looking at or a transform/HTTP step). attempt is 0-indexed and tracks
// the retry chain — a step that escalated through trivial→fast→moderate
// produces three sibling spans at attempt=0/1/2.
func StartRoutineStepSpan(ctx context.Context, stepID, stepType string, attempt int) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.String(AttrCrewshipRoutineStepID, stepID),
		attribute.String(AttrCrewshipRoutineStepType, stepType),
		attribute.Int(AttrCrewshipRoutineStepAttempt, attempt),
	}
	return otel.Tracer(tracerName).Start(ctx, "routine.step",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...),
	)
}

// RecordRunAgentToolSpan emits a CHILD span of the current routine-step span
// (ctx must carry the StartRoutineStepSpan context) representing one agent tool
// invocation. Unlike the run/step spans this one is opened AND closed in a
// single call because the orchestrator only learns the tool's duration once the
// tool_result arrives — we backdate the span to the real start/end instants via
// WithTimestamp so the collector renders an accurate waterfall rather than a
// zero-width marker at emit time.
//
// seq orders sibling tool spans within a step. status "error" flips the span
// status to codes.Error so a failed tool stands out in the trace UI. attrs is
// the small bag mirrored from RunAgentSpan.Attributes (tool, host,
// artifact_path, model) — kept as strings so the OTEL attribute set stays
// flat and queryable.
func RecordRunAgentToolSpan(ctx context.Context, kind, name string, seq int, startedAt time.Time, durationMs int64, status string, attrs map[string]string) {
	spanAttrs := []attribute.KeyValue{
		attribute.String(AttrCrewshipRoutineToolKind, kind),
		attribute.String(AttrCrewshipRoutineToolName, name),
		attribute.Int(AttrCrewshipRoutineToolSeq, seq),
		attribute.String(AttrCrewshipRoutineToolStatus, status),
	}
	for k, v := range attrs {
		spanAttrs = append(spanAttrs, attribute.String("crewship.routine.tool."+k, v))
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	_, span := otel.Tracer(tracerName).Start(ctx, "routine.tool",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(spanAttrs...),
		trace.WithTimestamp(startedAt),
	)
	if status == "error" {
		span.SetStatus(codes.Error, "tool reported error")
	}
	span.End(trace.WithTimestamp(startedAt.Add(time.Duration(durationMs) * time.Millisecond)))
}
