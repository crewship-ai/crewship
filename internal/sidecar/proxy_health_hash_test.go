package sidecar

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHealthReportsSidecarHash locks the #1008 detection signal: the sidecar's
// /health response advertises the build hash of the binary it is running, so
// the server can spot a container still serving a STALE bind-mounted sidecar
// after a redeploy (which otherwise degrades memory/egress silently).
func TestHealthReportsSidecarHash(t *testing.T) {
	proxy := newTestProxy(nil, []string{"localhost"})
	proxy.buildHash = "deadbeef1234"

	req := httptest.NewRequest("GET", "http://localhost:9119/health", nil)
	req.Host = "localhost:9119"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, `"sidecar_hash":"deadbeef1234"`) {
		t.Errorf("health body missing sidecar_hash: %q", body)
	}
}

// TestHealthSidecarHashEmptyWhenUnset — an unknown/unset hash must still emit a
// well-formed (empty) field rather than break the JSON, so an old sidecar that
// reports "" is treated as "unknown" (no false stale alarm) by the server.
func TestHealthSidecarHashEmptyWhenUnset(t *testing.T) {
	proxy := newTestProxy(nil, []string{"localhost"})
	req := httptest.NewRequest("GET", "http://localhost:9119/health", nil)
	req.Host = "localhost:9119"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)
	if body := w.Body.String(); !strings.Contains(body, `"sidecar_hash":""`) {
		t.Errorf("health body should carry an empty sidecar_hash when unset: %q", body)
	}
}

// TestSelfExeHashStable confirms selfExeHash returns a stable, non-empty digest
// (it hashes the running executable once and memoizes).
func TestSelfExeHashStable(t *testing.T) {
	a := selfExeHash()
	b := selfExeHash()
	if a == "" {
		t.Fatal("selfExeHash returned empty")
	}
	if a != b {
		t.Errorf("selfExeHash not stable: %q vs %q", a, b)
	}
}
