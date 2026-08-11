package telemetry

// Coverage tests for provider.go Init — the exporter-backed path that the
// existing no-endpoint test skips. A local httptest collector receives the
// OTLP HTTP POSTs so the full pipeline (resource → batcher → exporter →
// flush-on-shutdown) is exercised without external dependencies.
//
// These tests mutate the global otel tracer provider; they must not run
// in parallel and they restore the prior global on exit.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel"
)

// stubCollector returns an httptest server that 200s every request and
// counts hits on /v1/traces.
func stubCollector(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" {
			hits.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// restoreGlobalProvider snapshots the current global tracer provider and
// restores it when the test ends so the SDK provider these tests install
// can't leak into other tests in the package.
func restoreGlobalProvider(t *testing.T) {
	t.Helper()
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
}

func TestInit_WithURLEndpoint_ExportsSpans(t *testing.T) {
	restoreGlobalProvider(t)
	srv, hits := stubCollector(t)
	ctx := context.Background()

	shutdown, err := Init(ctx, srv.URL, "cov-test")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	initMu.Lock()
	if initState == nil {
		initMu.Unlock()
		t.Fatal("initState should be populated for a configured endpoint")
	}
	if got := otel.GetTracerProvider(); got != initState.tp {
		initMu.Unlock()
		t.Error("global tracer provider not swapped to the SDK provider")
	}
	initMu.Unlock()

	// Emit one span; shutdown flushes the batcher so the stub collector
	// must have received at least one /v1/traces POST afterwards.
	_, span := otel.Tracer(tracerName).Start(ctx, "cov-span")
	span.End()
	shutdown()

	if hits.Load() == 0 {
		t.Error("no OTLP trace export reached the collector after shutdown flush")
	}
	initMu.Lock()
	if initState != nil {
		t.Error("shutdown should clear initState")
	}
	initMu.Unlock()

	// Second shutdown call is a no-op (initState already nil) — must not
	// panic.
	shutdown()
}

func TestWithTracesPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"base url gets the signal path", "http://localhost:4318", "http://localhost:4318/v1/traces"},
		{"trailing slash is still a base url", "http://localhost:4318/", "http://localhost:4318/v1/traces"},
		{"https base url", "https://collector.example.com", "https://collector.example.com/v1/traces"},
		{"explicit traces path is not doubled", "http://localhost:4318/v1/traces", "http://localhost:4318/v1/traces"},
		{"trailing slash on the traces path is not doubled", "http://localhost:4318/v1/traces/", "http://localhost:4318/v1/traces"},
		// The case #1870 is about: a project-scoped base URL is still a base
		// URL, so the signal path belongs on the end of it.
		{"project-scoped prefix gets the signal path", "https://cloud.example.com/api/public/otel", "https://cloud.example.com/api/public/otel/v1/traces"},
		{"query string survives", "http://localhost:4318?tenant=a", "http://localhost:4318/v1/traces?tenant=a"},
		{"bare host:port is not a URL and is untouched", "127.0.0.1:4318", "127.0.0.1:4318"},
		{"unparseable input is passed through", "http://[::1", "http://[::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withTracesPath(tt.in); got != tt.want {
				t.Errorf("withTracesPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveTracesEndpoint(t *testing.T) {
	const (
		generic = "OTEL_EXPORTER_OTLP_ENDPOINT"
		signal  = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	)
	tests := []struct {
		name           string
		explicit       string
		env            map[string]string
		want           string
		wantConfigured bool
	}{
		{
			name: "nothing configured", want: "", wantConfigured: false,
		},
		{
			name: "generic base url gets the signal path",
			env:  map[string]string{generic: "http://localhost:4318"},
			want: "http://localhost:4318/v1/traces", wantConfigured: true,
		},
		{
			name: "generic prefix gets the signal path too",
			env:  map[string]string{generic: "https://cloud.example.com/api/public/otel"},
			want: "https://cloud.example.com/api/public/otel/v1/traces", wantConfigured: true,
		},
		{
			// The signal-specific variable is a full URL by definition, so a
			// prefix here means the operator meant exactly that.
			name: "signal-specific url is verbatim",
			env:  map[string]string{signal: "https://cloud.example.com/api/public/otel"},
			want: "https://cloud.example.com/api/public/otel", wantConfigured: true,
		},
		{
			name: "signal-specific wins over generic",
			env:  map[string]string{generic: "http://localhost:4318", signal: "http://elsewhere:4318/custom"},
			want: "http://elsewhere:4318/custom", wantConfigured: true,
		},
		{
			name:     "explicit argument wins over both",
			explicit: "http://argument:4318",
			env:      map[string]string{generic: "http://localhost:4318", signal: "http://elsewhere:4318/custom"},
			want:     "http://argument:4318/v1/traces", wantConfigured: true,
		},
		{
			name: "whitespace-only env is not configuration",
			env:  map[string]string{generic: "   "},
			want: "", wantConfigured: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(generic, "")
			t.Setenv(signal, "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			got, ok := ResolveTracesEndpoint(tt.explicit)
			if got != tt.want || ok != tt.wantConfigured {
				t.Errorf("ResolveTracesEndpoint(%q) = (%q, %v), want (%q, %v)",
					tt.explicit, got, ok, tt.want, tt.wantConfigured)
			}
		})
	}
}

// pathRecorder is a collector that records the path of every request it is
// sent, so a test can assert where spans actually landed rather than that
// something was sent somewhere.
func pathRecorder(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// emitOneSpan runs a full Init → span → shutdown cycle so the batcher flushes
// before the caller inspects what the collector received.
func emitOneSpan(t *testing.T, endpoint string) {
	t.Helper()
	shutdown, err := Init(context.Background(), endpoint, "cov-test")
	if err != nil {
		t.Fatalf("Init(%q): %v", endpoint, err)
	}
	_, span := otel.Tracer(tracerName).Start(context.Background(), "cov-span")
	span.End()
	shutdown()
}

// A project-scoped base URL is still a base URL: the signal path belongs on
// the end of it, which is what makes backends like Langfuse reachable. Before
// #1870 we posted to the bare prefix, where a collector answers 200 and drops
// the payload.
func TestInit_PrefixedURLEndpointGetsTracesPath(t *testing.T) {
	restoreGlobalProvider(t)
	srv, seen := pathRecorder(t)

	emitOneSpan(t, srv.URL+"/api/public/otel")

	paths := seen()
	if len(paths) == 0 {
		t.Fatal("no OTLP trace export reached the collector after shutdown flush")
	}
	for _, p := range paths {
		if p != "/api/public/otel/v1/traces" {
			t.Errorf("export posted to %q, want /api/public/otel/v1/traces", p)
		}
	}
}

// The signal-specific variable is a full URL by definition, so whatever it
// says is where spans go — no path is appended to it.
func TestInit_SignalSpecificEnvIsUsedVerbatim(t *testing.T) {
	restoreGlobalProvider(t)
	srv, seen := pathRecorder(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://unused.invalid:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", srv.URL+"/custom/trace/sink")

	emitOneSpan(t, "")

	paths := seen()
	if len(paths) == 0 {
		t.Fatal("signal-specific endpoint received nothing — generic endpoint won")
	}
	for _, p := range paths {
		if p != "/custom/trace/sink" {
			t.Errorf("export posted to %q, want /custom/trace/sink verbatim", p)
		}
	}
}

func TestInit_BareHostPortUsesInsecureExporter(t *testing.T) {
	restoreGlobalProvider(t)
	ctx := context.Background()

	// host:port form (no scheme) takes the WithEndpoint+WithInsecure
	// branch. No spans are emitted so nothing tries to dial the address.
	shutdown, err := Init(ctx, "127.0.0.1:4318", "cov-test")
	if err != nil {
		t.Fatalf("Init bare host:port: %v", err)
	}
	initMu.Lock()
	ok := initState != nil
	initMu.Unlock()
	if !ok {
		t.Fatal("initState should be set for host:port endpoint")
	}
	shutdown()
}

func TestInit_EnvEndpointAndServiceNameCoercion(t *testing.T) {
	restoreGlobalProvider(t)
	srv, _ := stubCollector(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	ctx := context.Background()

	// Empty endpoint arg → env var wins; empty serviceName → "crewship".
	shutdown, err := Init(ctx, "", "")
	if err != nil {
		t.Fatalf("Init from env: %v", err)
	}
	initMu.Lock()
	ok := initState != nil
	initMu.Unlock()
	if !ok {
		t.Fatal("env-resolved endpoint should configure a real provider")
	}
	shutdown()
}

func TestInit_ReinitTearsDownPreviousProvider(t *testing.T) {
	restoreGlobalProvider(t)
	srv, _ := stubCollector(t)
	ctx := context.Background()

	shutdown1, err := Init(ctx, srv.URL, "first")
	if err != nil {
		t.Fatalf("Init #1: %v", err)
	}
	initMu.Lock()
	first := initState.tp
	initMu.Unlock()

	shutdown2, err := Init(ctx, srv.URL, "second")
	if err != nil {
		t.Fatalf("Init #2: %v", err)
	}
	initMu.Lock()
	second := initState.tp
	initMu.Unlock()
	if first == second {
		t.Error("re-init should build a fresh provider")
	}

	// The stale shutdown must be a no-op (its tp no longer matches) and
	// must NOT clear the current state.
	shutdown1()
	initMu.Lock()
	if initState == nil || initState.tp != second {
		t.Error("stale shutdown cleared the active provider state")
	}
	initMu.Unlock()

	shutdown2()
	initMu.Lock()
	if initState != nil {
		t.Error("active shutdown should clear initState")
	}
	initMu.Unlock()
}
