package telemetry

import (
	"context"
	"net/http"

	"github.com/crewship-ai/crewship/internal/journal"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Inject writes the current trace context into the supplied HTTP header
// map so downstream services can continue the same trace. Uses the
// globally registered propagator (set in Init) so the headers are whatever
// W3C Trace Context + Baggage dictate: `traceparent`, `tracestate`,
// `baggage`.
//
// If ctx has no active span, this is a no-op — propagators are smart
// enough to skip writing a zero-valued context. That's why we don't guard
// with SpanContext().IsValid() here.
func Inject(ctx context.Context, header http.Header) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(header))
}

// Extract reads trace context from the supplied HTTP header and returns a
// derived context carrying the remote span context as a "link" parent.
// Handlers typically use this at the entry point of an HTTP route so any
// span started after the call inherits the incoming trace.
//
// An extraction with no valid header produces a context with an invalid
// remote span context; that's harmless — StartXxxSpan will just start a
// root span. No error is returned because OTel's contract is best-effort:
// trace headers are informational, their absence is not a failure.
func Extract(ctx context.Context, header http.Header) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(header))
}

// ResolveTrace is the function that journal.SetTraceResolver expects: it
// reads the active span from ctx and returns its trace ID + span ID in
// hex. The ok return is false when no valid span is present, which lets
// the journal code distinguish "no telemetry today" from "empty string
// legitimately set by the caller".
//
// This is the only direct coupling point between the journal package and
// OpenTelemetry. journal.SetTraceResolver(telemetry.ResolveTrace) is the
// line callers must execute once at startup (typically right after
// telemetry.Init). Without it, every journal entry gets empty trace_id
// and the two products aren't joinable in the UI.
func ResolveTrace(ctx context.Context) (traceID, spanID string, ok bool) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return "", "", false
	}
	return sc.TraceID().String(), sc.SpanID().String(), true
}

// RegisterJournalResolver is a convenience wrapper that registers
// ResolveTrace with the journal package. Centralising the call here means
// the journal package doesn't need to know about OTel types and callers
// can wire the two together in a single line:
//
//	telemetry.RegisterJournalResolver()
//
// The split between ResolveTrace (pure function, easy to test) and
// RegisterJournalResolver (side-effectful registration) is intentional:
// tests exercise ResolveTrace directly and only a single test hits the
// global resolver state.
func RegisterJournalResolver() {
	journal.SetTraceResolver(ResolveTrace)
}
