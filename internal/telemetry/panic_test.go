package telemetry

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestRecoverPanic_StampsAndRepanics confirms the deferred-recover
// helper does the three things it advertises: extracts stack, stamps
// the span as errored, and re-panics the wrapped value. Without the
// re-panic the runtime's normal post-mortem (or an outer defer) would
// never know the goroutine crashed.
func TestRecoverPanic_StampsAndRepanics(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected re-panic but recovered nil")
		}
		pws, ok := r.(*PanicWithStack)
		if !ok {
			t.Fatalf("expected *PanicWithStack, got %T", r)
		}
		if pws.Value != "boom" {
			t.Errorf("Value = %v, want \"boom\"", pws.Value)
		}
		if !strings.Contains(string(pws.Stack), "TestRecoverPanic_StampsAndRepanics") {
			t.Errorf("Stack missing test function name; got first 200 bytes: %s",
				string(pws.Stack)[:min(len(pws.Stack), 200)])
		}

		spans := exp.GetSpans()
		if len(spans) != 1 {
			t.Fatalf("expected 1 span, got %d", len(spans))
		}
		s := spans[0]
		// Span must be errored.
		if s.Status.Code.String() != "Error" {
			t.Errorf("span status = %s, want Error", s.Status.Code)
		}
		// Stack attribute must be present and non-empty.
		var sawStack, sawValue bool
		for _, kv := range s.Attributes {
			if string(kv.Key) == "crewship.panic.stack" && len(kv.Value.AsString()) > 0 {
				sawStack = true
			}
			if string(kv.Key) == "crewship.panic.value" && kv.Value.AsString() == "boom" {
				sawValue = true
			}
		}
		if !sawStack {
			t.Error("span missing crewship.panic.stack attribute")
		}
		if !sawValue {
			t.Error("span missing crewship.panic.value=boom attribute")
		}
	}()

	_, span := otel.Tracer("test").Start(context.Background(), "panicking-op")
	// Simulate the production pattern: panic, then recover in the
	// defer below, hand the value to RecoverPanic.
	defer func() {
		if r := recover(); r != nil {
			RecoverPanic(span, r)
		}
	}()
	panic("boom")
}

// TestRecoverPanic_PreservesStackAcrossNestedRecovers is the whole
// reason PanicWithStack exists: when runStep → runDSL → RunAgent each
// recover and re-panic, the OUTER recovers see a *PanicWithStack
// value (not the raw original) and must NOT re-capture the stack
// because debug.Stack() at the outer site points at the re-panic
// line, not the crash. The wrapper passes the original-site stack
// forward.
func TestRecoverPanic_PreservesStackAcrossNestedRecovers(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	// Outermost recover — captures the re-thrown wrapper.
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected re-panic, got nil")
		}
		pws, ok := r.(*PanicWithStack)
		if !ok {
			t.Fatalf("outer recover: expected *PanicWithStack, got %T", r)
		}
		// Critical assertion: the stack must point at the ORIGINAL
		// crash function (innerFunc), not at the re-panic line of
		// the middle defer.
		if !strings.Contains(string(pws.Stack), "innerFunc") {
			t.Errorf("stack lost original site; got: %s",
				string(pws.Stack)[:min(len(pws.Stack), 400)])
		}
	}()

	_, outerSpan := otel.Tracer("test").Start(context.Background(), "outer")
	defer func() {
		if r := recover(); r != nil {
			RecoverPanic(outerSpan, r)
		}
	}()

	_, innerSpan := otel.Tracer("test").Start(context.Background(), "inner")
	defer func() {
		if r := recover(); r != nil {
			RecoverPanic(innerSpan, r)
		}
	}()

	innerFunc(t)
}

func innerFunc(t *testing.T) {
	t.Helper()
	panic("original-crash")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
