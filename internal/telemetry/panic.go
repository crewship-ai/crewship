package telemetry

import (
	"fmt"
	"runtime/debug"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// PanicWithStack carries a panic value through nested deferred
// recover sites without losing the stack trace from the original
// crash location.
//
// Why this exists: Go's `recover()` followed by `panic(r)` re-emits
// the panic, but the runtime stack attached to the new panic starts
// at the re-panic call site. With three nested defers (runStep →
// runDSL → RunAgent) each doing recover + RecordError + re-panic,
// the runtime's eventual "unrecovered panic" stack trace begins at
// the outermost re-panic line, not at the original crash. Operators
// reading post-mortem logs see "panic at executor.go:709" and have
// no idea what actually exploded.
//
// The wrapper carries the captured stack so each outer recover can
// stamp the span with the same authoritative trace, and the runtime
// (when no outer recover exists) still surfaces a meaningful trace
// because we leave the wrapped value intact for downstream readers.
type PanicWithStack struct {
	Value any
	Stack []byte
}

// Error satisfies error so the wrapper can be passed to RecordError
// directly. We prefix with "panic:" so trace UIs that group by error
// string keep all crash spans clustered.
func (p *PanicWithStack) Error() string {
	return fmt.Sprintf("panic: %v", p.Value)
}

// RecoverPanic is the standard pattern for a nested deferred recover
// that wants to (a) stamp the active span with the panic + stack as
// span attributes, (b) close the span as errored, (c) re-panic so an
// outer defer or the runtime ultimately handles the crash.
//
// `r` is the result of `recover()`. If `r == nil`, this is a no-op
// (the deferred function on the success path can still call this
// safely). On the panic path it always re-panics — callers do NOT
// return after this function; control transfers via panic.
//
// First recover in a chain captures debug.Stack(). Subsequent recovers
// (which see a *PanicWithStack value) reuse the captured stack so the
// stack attribute on every span in the chain points at the ORIGINAL
// crash site, not at the re-panic line of the inner defer.
//
// Outermost recover (typically the entry-point function with no
// further defers above it) should NOT call this — it should stop the
// panic by NOT re-panicking. Use RecoverPanicNoRethrow instead, or
// inline the same pattern without the trailing panic call.
func RecoverPanic(span trace.Span, r any) {
	if r == nil {
		return
	}
	pws := wrapPanic(r)
	stampPanicSpan(span, pws)
	span.End()
	panic(pws)
}

// wrapPanic produces a PanicWithStack from a raw recover() value,
// preserving an already-captured stack when the value is already
// wrapped. Exported for tests; not for routine consumption.
func wrapPanic(r any) *PanicWithStack {
	if pws, ok := r.(*PanicWithStack); ok {
		return pws
	}
	return &PanicWithStack{Value: r, Stack: debug.Stack()}
}

// stampPanicSpan attaches the panic value + stack as span attributes
// and marks the span as errored. The "panic.stack" attribute uses the
// crewship.* namespace because OTel's GenAI semconv has no analogue;
// span error status follows OTel's `RecordException` convention.
func stampPanicSpan(span trace.Span, pws *PanicWithStack) {
	if span == nil {
		return
	}
	span.SetAttributes(
		attribute.String("crewship.panic.value", fmt.Sprintf("%v", pws.Value)),
		attribute.String("crewship.panic.stack", string(pws.Stack)),
	)
	RecordError(span, pws)
}
