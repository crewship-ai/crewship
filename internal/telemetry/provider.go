// Package telemetry provides the OpenTelemetry plumbing for Crewship.
//
// The package owns three concerns:
//
//  1. Provider init (this file) — build a SDK tracer provider backed by an
//     OTLP HTTP exporter, or gracefully fall back to a no-op tracer when no
//     endpoint is configured. Unconfigured workspaces still compile and the
//     spans created by the rest of the code are valid trace.Span values,
//     they just never leave the process.
//
//  2. Span builders (spans.go) — typed helpers that create agent, tool, and
//     LLM spans with the attributes prescribed by the OTel GenAI Semantic
//     Conventions (gen_ai.system, gen_ai.request.model, gen_ai.usage.*).
//
//  3. Propagation + journal integration (propagation.go) — W3C Trace Context
//     injection/extraction over HTTP headers and a resolver that feeds the
//     journal package so every journal entry is stamped with the current
//     trace/span identifiers.
//
// Design principles:
//
//   - Zero-config safety. If OTEL_EXPORTER_OTLP_ENDPOINT is unset and Init
//     is called with an empty endpoint, the shutdown function is a no-op
//     and otel.GetTracerProvider() keeps returning the noop provider. This
//     lets developers run the binary without a collector.
//   - HTTP exporter, not gRPC. The gRPC exporter pulls in the grpc module
//     which balloons the build; HTTP is fine for the expected volume and
//     plays nicer with corporate proxies.
//   - Resource attributes are set once at Init so every span carries
//     service.name and the Crewship deployment identifier without the
//     spans package having to care.
package telemetry

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
)

// tracerName is the instrumentation-scope name used for every Tracer this
// package hands out. Keeping it stable lets downstream collectors route
// crewship spans even across service.name changes (local/prod/ee).
const tracerName = "github.com/crewship-ai/crewship/internal/telemetry"

// initState holds the previously-built tracer provider and associated
// exporter so Init is idempotent. Re-calling Init replaces the global
// provider and shuts down the previous one cleanly; useful during tests
// that need to swap exporters mid-run.
var (
	initMu    sync.Mutex
	initState *providerState
)

type providerState struct {
	tp       *sdktrace.TracerProvider
	exporter *otlptrace.Exporter
}

// Init wires up the OTLP HTTP exporter, registers the resulting tracer
// provider with otel, and returns a shutdown func the caller must invoke
// on process exit to flush the batch span processor.
//
// Endpoint resolution is ResolveTracesEndpoint's — explicit argument, then
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, then OTEL_EXPORTER_OTLP_ENDPOINT, then
// nothing, which yields a no-op tracer. Callers that want to log where spans
// are actually going should call ResolveTracesEndpoint themselves rather than
// log what they configured; the two differ by the signal path.
//
// serviceName is required; it becomes the resource attribute
// service.name and is the primary grouping in every collector UI. Passing
// an empty string is allowed (it's coerced to "crewship") but doing so
// obscures service boundaries in the trace explorer.
//
// The propagator is set to the W3C composite (TraceContext + Baggage) so
// downstream services and sidecar hops see the same headers. This is the
// OTel default but setting it explicitly means the behavior is stable
// even if we don't otherwise touch global propagators.
func Init(ctx context.Context, endpoint string, serviceName string) (func(), error) {
	initMu.Lock()
	defer initMu.Unlock()

	// Tear down any previous provider so repeat calls (tests, reloads) are
	// idempotent. We swallow shutdown errors because the new provider is
	// about to replace the old one either way.
	if initState != nil {
		_ = initState.tp.Shutdown(context.Background())
		initState = nil
	}

	endpoint, _ = ResolveTracesEndpoint(endpoint)
	if serviceName == "" {
		serviceName = "crewship"
	}

	// Register the W3C composite propagator unconditionally so
	// Inject/Extract work even when the exporter is disabled. Without
	// this, cross-process links are silently dropped in no-op mode.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if endpoint == "" {
		// No endpoint → leave the global as the noop provider. Return a
		// no-op shutdown so callers can always `defer shutdown()`.
		return func() {}, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion()),
		),
	)
	if err != nil {
		return func() {}, fmt.Errorf("telemetry: build resource: %w", err)
	}

	// otlptracehttp accepts either an endpoint (host:port) or a full URL
	// via WithEndpointURL. We prefer the URL form because it also carries
	// the scheme (http vs https), which matters for self-signed collectors.
	opts := []otlptracehttp.Option{}
	if isURL(endpoint) {
		opts = append(opts, otlptracehttp.WithEndpointURL(endpoint))
	} else {
		opts = append(opts, otlptracehttp.WithEndpoint(endpoint))
		// Bare host:port defaults to plaintext because TLS usually needs a
		// scheme anyway; operators wanting TLS should pass a https:// URL.
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	exp, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return func() {}, fmt.Errorf("telemetry: create exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp,
			sdktrace.WithBatchTimeout(5*time.Second),
			sdktrace.WithMaxQueueSize(2048),
			sdktrace.WithMaxExportBatchSize(512),
		),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	initState = &providerState{tp: tp, exporter: exp}

	shutdown := func() {
		// Use a bounded context so shutdown never blocks forever; 5s is
		// generous given the exporter batch timeout is the same.
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		initMu.Lock()
		defer initMu.Unlock()
		if initState != nil && initState.tp == tp {
			_ = tp.Shutdown(sctx)
			initState = nil
		}
	}
	return shutdown, nil
}

// ResolveTracesEndpoint returns the exact URL spans will be POSTed to, and
// whether tracing is configured at all. Exported so the caller can log the
// resolved destination rather than the configured one — "endpoint=http://…"
// with no path is precisely the log line that hid #1868 for a release.
//
// Precedence, matching the OpenTelemetry protocol-exporter spec:
//
//  1. the explicit argument — a Crewship-level override, treated as a base
//     URL like the generic variable
//  2. OTEL_EXPORTER_OTLP_TRACES_ENDPOINT — the full trace URL, used verbatim
//  3. OTEL_EXPORTER_OTLP_ENDPOINT — a base URL shared by every signal, with
//     the traces path appended
//  4. nothing configured → ("", false), and Init installs a noop tracer
func ResolveTracesEndpoint(explicit string) (string, bool) {
	if v := strings.TrimSpace(explicit); v != "" {
		return withTracesPath(v), true
	}
	// Signal-specific wins, and is the escape hatch for anyone whose
	// collector serves traces somewhere the base-URL rule would not reach.
	if v := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")); v != "" {
		return v, true
	}
	if v := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")); v != "" {
		return withTracesPath(v), true
	}
	return "", false
}

// withTracesPath turns a base collector URL into the traces URL.
//
// The spec is unambiguous that a base endpoint gets the signal path appended
// — including when the base already has a path of its own, which is how a
// project-scoped backend like Langfuse's `/api/public/otel` is meant to
// resolve to `/api/public/otel/v1/traces`. We used to leave any path alone
// and so never reached those backends at all; the collector answers 200 on
// the prefix and drops the payload, which looks exactly like working.
//
// One deliberate deviation: a path that already ends in the traces segment
// is left as-is rather than doubled. The spec would have us produce
// `/v1/traces/v1/traces`, and someone who put the full URL in the generic
// variable has a working deployment today. Silently breaking it is the very
// failure this function exists to prevent, and
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is the supported way to be explicit.
//
// A bare `host:port` is not a URL and is returned untouched — Init sends it
// down the WithEndpoint branch, where the exporter supplies the path.
func withTracesPath(raw string) string {
	if !isURL(raw) {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		// Malformed input is the exporter's to reject, with its own message.
		return raw
	}
	p := strings.TrimSuffix(u.Path, "/")
	if strings.HasSuffix(p, tracesPath) {
		u.Path = p
		return u.String()
	}
	u.Path = p + tracesPath
	return u.String()
}

// tracesPath is the OTLP/HTTP signal path for traces, fixed by the spec.
const tracesPath = "/v1/traces"

// isURL is a cheap heuristic — we only need to distinguish host:port from
// http(s)://… so startswith is sufficient. Parsing is overkill here and
// costs us predictability when the collector is behind a unix-domain socket
// forwarder.
func isURL(s string) bool {
	return len(s) >= 7 && (s[:7] == "http://" || (len(s) >= 8 && s[:8] == "https://"))
}

// serviceVersion returns the resource attribute for service.version. We
// pull from the env var so operators can stamp the running binary without
// recompiling. Empty version is OK — semconv treats it as unset.
func serviceVersion() string {
	if v := os.Getenv("CREWSHIP_VERSION"); v != "" {
		return v
	}
	return "dev"
}
