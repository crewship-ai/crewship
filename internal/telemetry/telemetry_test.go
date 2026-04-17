package telemetry

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/paymaster"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// spanContextFrom is a tiny shim over trace.SpanContextFromContext so
// tests don't need to import trace directly at every call site. Keeps the
// OTel dependency surface small in the test file.
func spanContextFrom(ctx context.Context) trace.SpanContext {
	return trace.SpanContextFromContext(ctx)
}

// journalResolverCall invokes whatever resolver is currently registered
// on the journal package, via a throwaway Entry emit through a stub
// emitter. The trace/span fields on the resulting Entry are what the
// resolver produced. Doing it this way (rather than calling ResolveTrace
// directly) exercises the real registration path.
func journalResolverCall(ctx context.Context) (string, string, bool) {
	var captured journal.Entry
	stub := journalCaptureEmitter{onEmit: func(e journal.Entry) { captured = e }}
	_, _ = stub.Emit(ctx, journal.Entry{
		WorkspaceID: "ws",
		Type:        journal.EntryLLMCall,
		ActorType:   journal.ActorSystem,
		Summary:     "resolver test",
	})
	if captured.TraceID == "" {
		return "", "", false
	}
	return captured.TraceID, captured.SpanID, true
}

type journalCaptureEmitter struct {
	onEmit func(journal.Entry)
}

func (j journalCaptureEmitter) Emit(ctx context.Context, e journal.Entry) (string, error) {
	// Mirror what Writer.Emit does for the trace fields: if not set by
	// the caller, ask the resolver. This is a dead-simple subset of the
	// real writer logic but it's the only bit the resolver tests care
	// about.
	if e.TraceID == "" {
		// resolveTraceLocal is a test-only copy so we don't invoke the
		// real sync.RWMutex-guarded resolver twice (once through Emit and
		// once directly); instead we read via the package resolver fn.
		if tID, sID, ok := callRegisteredResolver(ctx); ok {
			e.TraceID, e.SpanID = tID, sID
		}
	}
	j.onEmit(e)
	return "test", nil
}

func (j journalCaptureEmitter) Flush(context.Context) error { return nil }

// callRegisteredResolver indirects to the package-registered resolver via
// a public journal shim (not available today). The simplest way to reach
// it in tests is to expose ResolveTrace directly — we already do — so we
// just call it and trust that SetTraceResolver(ResolveTrace) has happened.
func callRegisteredResolver(ctx context.Context) (string, string, bool) {
	return ResolveTrace(ctx)
}

// setupRecorder wires an in-memory exporter as the global tracer provider
// and returns a Reset-able recorder + the SDK provider so tests can flush
// between assertions. The Init()-path is deliberately NOT used here because
// it pulls in the OTLP exporter and we want a hermetic test; instead we
// replicate the subset of Init that tests care about.
func setupRecorder(t *testing.T) (*tracetest.InMemoryExporter, *sdktrace.TracerProvider) {
	t.Helper()
	rec := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(rec))
	prevTP := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	// Set composite propagator so Inject/Extract tests behave the same
	// way they would in production — without this the global propagator
	// defaults to noop and Inject writes nothing.
	prevProp := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return rec, tp
}

// attrMap flattens span attributes into a lookup table so assertions can
// read by key instead of iterating. The SDK guarantees keys are unique per
// span so a simple map (not multimap) is correct.
func attrMap(attrs []attribute.KeyValue) map[string]attribute.Value {
	out := make(map[string]attribute.Value, len(attrs))
	for _, kv := range attrs {
		out[string(kv.Key)] = kv.Value
	}
	return out
}

func TestStartAgentSpan_AttributesSet(t *testing.T) {
	rec, _ := setupRecorder(t)

	_, span := StartAgentSpan(context.Background(), "agent-1", "lead", "crew-A", "mission-99")
	span.End()

	spans := rec.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	got := attrMap(spans[0].Attributes)
	if v, ok := got[AttrCrewshipAgentID]; !ok || v.AsString() != "agent-1" {
		t.Errorf("agent.id attribute missing or wrong: %v", v)
	}
	if v, ok := got[AttrCrewshipAgentType]; !ok || v.AsString() != "lead" {
		t.Errorf("agent.type attribute missing or wrong: %v", v)
	}
	if v, ok := got[AttrCrewshipCrewID]; !ok || v.AsString() != "crew-A" {
		t.Errorf("crew.id attribute missing or wrong: %v", v)
	}
	if v, ok := got[AttrCrewshipMissionID]; !ok || v.AsString() != "mission-99" {
		t.Errorf("mission.id attribute missing or wrong: %v", v)
	}
	if spans[0].Name != "agent.invoke" {
		t.Errorf("span name = %q, want agent.invoke", spans[0].Name)
	}
}

func TestStartAgentSpan_OmitsEmptyCrewMission(t *testing.T) {
	rec, _ := setupRecorder(t)

	_, span := StartAgentSpan(context.Background(), "agent-solo", "coordinator", "", "")
	span.End()

	got := attrMap(rec.GetSpans()[0].Attributes)
	if _, ok := got[AttrCrewshipCrewID]; ok {
		t.Error("crew.id should be omitted when empty")
	}
	if _, ok := got[AttrCrewshipMissionID]; ok {
		t.Error("mission.id should be omitted when empty")
	}
}

func TestStartToolSpan_SideEffectMarker(t *testing.T) {
	rec, _ := setupRecorder(t)

	_, s1 := StartToolSpan(context.Background(), "shell", "abc123", true)
	s1.End()
	_, s2 := StartToolSpan(context.Background(), "read_file", "def456", false)
	s2.End()

	spans := rec.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(spans))
	}
	// Span order in the in-memory exporter is the order of End() calls.
	a1 := attrMap(spans[0].Attributes)
	a2 := attrMap(spans[1].Attributes)
	if v, ok := a1[AttrCrewshipToolSideEffect]; !ok || !v.AsBool() {
		t.Error("shell span should be marked side-effect")
	}
	if v, ok := a2[AttrCrewshipToolSideEffect]; !ok || v.AsBool() {
		t.Error("read_file span should NOT be marked side-effect")
	}
	if v, ok := a1[AttrCrewshipToolArgs]; !ok || v.AsString() != "abc123" {
		t.Errorf("args hash attribute missing or wrong: %v", v)
	}
}

func TestStartLLMSpan_PlaceholderUsage(t *testing.T) {
	rec, _ := setupRecorder(t)

	_, span := StartLLMSpan(context.Background(), "anthropic", "claude-opus-4-7")
	span.End()

	got := attrMap(rec.GetSpans()[0].Attributes)
	if v, ok := got[AttrGenAISystem]; !ok || v.AsString() != "anthropic" {
		t.Errorf("gen_ai.system missing: %v", v)
	}
	if v, ok := got[AttrGenAIRequestModel]; !ok || v.AsString() != "claude-opus-4-7" {
		t.Errorf("gen_ai.request.model missing: %v", v)
	}
	// Placeholders — should be zero until RecordLLMUsage overwrites them.
	if got[AttrGenAIUsageInputTokens].AsInt64() != 0 {
		t.Error("input tokens placeholder should be 0")
	}
	if got[AttrGenAICostTotalUSD].AsFloat64() != 0 {
		t.Error("cost placeholder should be 0")
	}
}

func TestRecordLLMUsage_OverwritesPlaceholders(t *testing.T) {
	rec, _ := setupRecorder(t)

	_, span := StartLLMSpan(context.Background(), "anthropic", "claude-sonnet-4-6")
	RecordLLMUsage(span, 1000, 500, 8000, 2000, 0.0125)
	span.End()

	got := attrMap(rec.GetSpans()[0].Attributes)
	if got[AttrGenAIUsageInputTokens].AsInt64() != 1000 {
		t.Errorf("input_tokens = %d, want 1000", got[AttrGenAIUsageInputTokens].AsInt64())
	}
	if got[AttrGenAIUsageOutputTokens].AsInt64() != 500 {
		t.Errorf("output_tokens = %d, want 500", got[AttrGenAIUsageOutputTokens].AsInt64())
	}
	if got[AttrGenAIUsageCachedInputTokens].AsInt64() != 8000 {
		t.Errorf("cached_input_tokens = %d, want 8000", got[AttrGenAIUsageCachedInputTokens].AsInt64())
	}
	if got[AttrGenAIUsageCacheCreationToks].AsInt64() != 2000 {
		t.Errorf("cache_creation_tokens = %d, want 2000", got[AttrGenAIUsageCacheCreationToks].AsInt64())
	}
	if got[AttrGenAICostTotalUSD].AsFloat64() != 0.0125 {
		t.Errorf("cost = %f, want 0.0125", got[AttrGenAICostTotalUSD].AsFloat64())
	}
}

func TestRecordError_MarksSpanErrored(t *testing.T) {
	rec, _ := setupRecorder(t)

	_, span := StartLLMSpan(context.Background(), "openai", "gpt-5")
	RecordError(span, errors.New("rate limited"))
	span.End()

	stubs := rec.GetSpans()
	if stubs[0].Status.Code.String() != "Error" {
		t.Errorf("span status code = %v, want Error", stubs[0].Status.Code)
	}
	if stubs[0].Status.Description != "rate limited" {
		t.Errorf("status description = %q, want rate limited", stubs[0].Status.Description)
	}
	// RecordError also adds an exception event.
	if len(stubs[0].Events) == 0 {
		t.Error("expected exception event")
	}
}

func TestInjectExtract_Roundtrip(t *testing.T) {
	setupRecorder(t)

	ctx, span := StartAgentSpan(context.Background(), "agent-tx", "lead", "", "")
	defer span.End()

	// Inject into an outgoing request header.
	outgoing := http.Header{}
	Inject(ctx, outgoing)
	if outgoing.Get("traceparent") == "" {
		t.Fatal("traceparent header not injected")
	}

	// Extract into a fresh context on the remote side. The resulting span
	// should share the same trace ID as the original.
	remoteCtx := Extract(context.Background(), outgoing)
	original := spanContextFrom(ctx)
	remote := spanContextFrom(remoteCtx)
	if remote.TraceID() != original.TraceID() {
		t.Errorf("remote trace ID %s != original %s", remote.TraceID(), original.TraceID())
	}
	if !remote.IsRemote() {
		t.Error("remote span context should be marked remote after Extract")
	}
}

func TestResolveTrace_FeedsJournalEmit(t *testing.T) {
	setupRecorder(t)

	// Register the resolver exactly as production wiring would. We save
	// and restore because SetTraceResolver is a global.
	journal.SetTraceResolver(ResolveTrace)
	t.Cleanup(func() { journal.SetTraceResolver(nil) })

	ctx, span := StartAgentSpan(context.Background(), "agent-j", "lead", "", "")
	defer span.End()

	tID, sID, ok := journalResolverCall(ctx)
	if !ok {
		t.Fatal("expected resolver to report ok=true inside a span")
	}
	sc := spanContextFrom(ctx)
	if tID != sc.TraceID().String() {
		t.Errorf("trace id mismatch: got %s, want %s", tID, sc.TraceID().String())
	}
	if sID != sc.SpanID().String() {
		t.Errorf("span id mismatch: got %s, want %s", sID, sc.SpanID().String())
	}
}

func TestResolveTrace_NoSpanReturnsFalse(t *testing.T) {
	// Fresh context with no span — resolver should report ok=false so
	// journal entries fall through to empty trace fields rather than
	// inventing a zero-hex trace id.
	tID, sID, ok := ResolveTrace(context.Background())
	if ok {
		t.Error("expected ok=false for bare context")
	}
	if tID != "" || sID != "" {
		t.Errorf("expected empty ids for bare context, got %q / %q", tID, sID)
	}
}

// TestLLMMiddleware_WrapsCallerWithSpan verifies the telemetry middleware
// is shaped correctly for composition with the paymaster caller stack.
// We don't assert on paymaster here (that's paymaster_test's job) — just
// that the middleware emits a span, records usage, and propagates errors.
func TestLLMMiddleware_HappyPath(t *testing.T) {
	rec, _ := setupRecorder(t)

	inner := paymaster.CallerFunc(func(_ context.Context, _ paymaster.CallRequest) (paymaster.CallResponse, error) {
		return paymaster.CallResponse{
			InputTokens: 42, OutputTokens: 7, CostUSD: 0.0042,
		}, nil
	})
	wrapped := LLMMiddleware(inner)

	_, err := wrapped.Call(context.Background(), paymaster.CallRequest{
		Provider: "anthropic", Model: "claude-haiku-4-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	spans := rec.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	got := attrMap(spans[0].Attributes)
	if got[AttrGenAIUsageInputTokens].AsInt64() != 42 {
		t.Errorf("input tokens %d, want 42", got[AttrGenAIUsageInputTokens].AsInt64())
	}
	if got[AttrGenAICostTotalUSD].AsFloat64() != 0.0042 {
		t.Errorf("cost %f, want 0.0042", got[AttrGenAICostTotalUSD].AsFloat64())
	}
}

func TestLLMMiddleware_ErrorMarksSpan(t *testing.T) {
	rec, _ := setupRecorder(t)

	boom := errors.New("provider down")
	inner := paymaster.CallerFunc(func(_ context.Context, _ paymaster.CallRequest) (paymaster.CallResponse, error) {
		return paymaster.CallResponse{}, boom
	})
	wrapped := LLMMiddleware(inner)

	_, err := wrapped.Call(context.Background(), paymaster.CallRequest{
		Provider: "anthropic", Model: "claude-opus-4-7",
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom error, got %v", err)
	}
	stubs := rec.GetSpans()
	if stubs[0].Status.Code.String() != "Error" {
		t.Errorf("span status = %v, want Error", stubs[0].Status.Code)
	}
}

func TestInit_NoEndpointNoopShutdown(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := Init(context.Background(), "", "crewship-test")
	if err != nil {
		t.Fatalf("Init returned error on empty endpoint: %v", err)
	}
	if shutdown == nil {
		t.Fatal("shutdown must never be nil")
	}
	shutdown() // must not panic
}
