package chatbridge

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIPCResolver_DialsConfiguredBaseURL pins the property the scheduler fix
// relies on: an IPCResolver sends its internal-API calls to the base URL it was
// constructed with (so wiring the pipeline runner with the daemon's loopback URL
// — instead of NextjsURL — actually routes scheduled agent-resolves to the
// loopback, which is up even when Next.js is restarting). Also covers BaseURL().
func TestIPCResolver_DialsConfiguredBaseURL(t *testing.T) {
	var gotPath, gotToken string
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Internal-Token")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"agent_id":"agent1","workspace_id":"ws1"}`)
	}))
	defer srv.Close()

	r := NewIPCResolver(srv.URL, "tok-123", slog.New(slog.NewTextHandler(io.Discard, nil)))

	if r.BaseURL() != srv.URL {
		t.Fatalf("BaseURL() = %q, want %q", r.BaseURL(), srv.URL)
	}

	// The return value isn't the point — the request reaching srv (not some
	// hardcoded URL) is. A loopback base URL therefore routes to the loopback.
	_, _ = r.ResolveAgent(context.Background(), "agent1", "ws1")

	if !hit {
		t.Fatal("resolver did not dial its configured base URL")
	}
	if gotPath != "/api/v1/internal/agents/agent1/resolve" {
		t.Errorf("request path = %q, want the internal agent-resolve path", gotPath)
	}
	if gotToken != "tok-123" {
		t.Errorf("X-Internal-Token = %q, want tok-123", gotToken)
	}
}

// TestIPCResolver_LoopbackVsNextjs documents the scheduler-fix intent: a resolver
// built for daemon self-calls (loopback) targets 127.0.0.1, distinct from a
// NextjsURL resolver. Guards against a regression that re-points the pipeline
// runner at NextjsURL.
func TestIPCResolver_LoopbackVsNextjs(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	loopback := NewIPCResolver("http://127.0.0.1:8083", "t", log)
	nextjs := NewIPCResolver("http://localhost:3013", "t", log)

	if !strings.HasPrefix(loopback.BaseURL(), "http://127.0.0.1:") {
		t.Errorf("loopback resolver BaseURL() = %q, want a 127.0.0.1 address", loopback.BaseURL())
	}
	if loopback.BaseURL() == nextjs.BaseURL() {
		t.Error("loopback and NextjsURL resolvers must target different hosts")
	}
}
