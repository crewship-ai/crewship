package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Routine-step attribute keys. These belong to the crewship.* namespace
// because routines are a Crewship-specific concept — the GenAI semconv
// has no analogue for "a versioned, scheduled, multi-step workflow."
// Operators querying their collector for "all routines named X" use
// these attributes; renaming requires a coordinated dashboard update.
const (
	AttrCrewshipRoutineSlug      = "crewship.routine.slug"
	AttrCrewshipRoutineRunID     = "crewship.routine.run_id"
	AttrCrewshipRoutinePipelineID = "crewship.routine.pipeline_id"
	AttrCrewshipRoutineStepID    = "crewship.routine.step.id"
	AttrCrewshipRoutineStepType  = "crewship.routine.step.type"
	AttrCrewshipRoutineStepAttempt = "crewship.routine.step.attempt"
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
