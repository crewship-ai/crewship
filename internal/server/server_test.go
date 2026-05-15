package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/logging"
)

// openTestDB returns a freshly-migrated SQLite DB in a temp dir. The
// auth-lifecycle work made server.New() panic when deps.DB is nil, so
// every test that exercises the constructor needs a real DB to back
// the WS hub's sessions store. File-backed (not :memory:) so multiple
// goroutines see the same schema without `cache=shared` gymnastics.
//
// When called with a non-nil *testing.T the cleanup is registered;
// the bare-package newTestServer() helper passes nil, which is fine
// for unit tests that exit immediately.
func openTestDB(t *testing.T) *sql.DB {
	// File-backed SQLite per call so multiple goroutines see a stable
	// schema. Each invocation gets a unique path — the helper is
	// called from many parallel tests and the previous shared
	// `/tmp/test-auth-lifecycle.db` made them stomp each other when
	// run with `-count=1`.
	var path string
	if t != nil {
		path = filepath.Join(t.TempDir(), "test-auth-lifecycle.db")
	} else {
		// No t.Cleanup hook is available — use a process-unique name
		// in the OS temp dir so collisions between bare newTestServer()
		// callers can't happen. Files are tiny and the OS reaps temp
		// on shutdown anyway.
		path = filepath.Join(os.TempDir(), fmt.Sprintf("test-auth-lifecycle-%d-%d.db", os.Getpid(), atomic.AddInt64(&testDBCounter, 1)))
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_foreign_keys=on&_journal=WAL")
	if err != nil {
		if t != nil {
			t.Fatalf("open: %v", err)
		}
		panic(err)
	}
	if t != nil {
		t.Cleanup(func() { db.Close() })
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(context.Background(), db, logger); err != nil {
		if t != nil {
			t.Fatalf("migrate: %v", err)
		}
		panic(err)
	}
	return db
}

// testDBCounter is the per-process counter that gives each bare
// (no-*testing.T) openTestDB call a unique filename. Incremented
// atomically so concurrent table-driven tests don't collide.
var testDBCounter int64

func newTestServer() *Server {
	return newTestServerForT(nil)
}

// newTestServerForT builds a Server with a freshly-migrated in-memory
// SQLite so the WS hub gets a real sessions store. server.New() now
// panics without one (see CodeRabbit comment on PR #233 — the previous
// code silently fell back to ws.NopSessionsForTests, which downgraded
// production startup to "no revocation" if deps.DB was ever forgotten).
//
// Tests that don't pass a *testing.T (the package-level newTestServer
// returning *Server with no t.Cleanup hook) still get a working DB —
// it just leaks until the process exits, which for unit tests means
// "until t.Cleanup wraps everything anyway".
func newTestServerForT(t *testing.T) *Server {
	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-server-test-32chars-1"
	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, &Deps{DB: openTestDB(t)})
	s.startedAt = time.Now()
	if t != nil {
		// Cancel the catalog/runtime refresh goroutines that New() spawned
		// so they can't outrace t.TempDir() cleanup. See StopBackground
		// doc for why. Bare-helper (t==nil) callers are short-lived
		// process-level tests that exit before the refresh writes land.
		t.Cleanup(s.StopBackground)
	}
	return s
}

func parseJSON(t *testing.T, data []byte) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("invalid JSON response: %v, body: %s", err, string(data))
	}
	return body
}

func TestHealthz(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()

	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	body := parseJSON(t, w.Body.Bytes())
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %v", body["status"])
	}
	if body["service"] != "crewshipd" {
		t.Errorf("expected service crewshipd, got %v", body["service"])
	}
}

func TestReadyz(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()

	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	body := parseJSON(t, w.Body.Bytes())
	if body["status"] != true {
		t.Errorf("expected status true, got %v", body["status"])
	}
}

func TestMetrics(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/metrics", nil)
	// /metrics is now gated to loopback or token-bearing callers (F-003).
	// The Go-level scrape path is loopback, which the default test
	// fixture's RemoteAddr ("192.0.2.1:1234") isn't — set it explicitly so
	// we exercise the Prometheus-from-localhost code path.
	req.RemoteAddr = "127.0.0.1:55555"
	w := httptest.NewRecorder()

	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected text/plain content type, got %s", ct)
	}

	output := w.Body.String()
	expectedMetrics := []string{
		"crewshipd_uptime_seconds",
		"crewshipd_goroutines",
		"crewshipd_memory_alloc_bytes",
		"crewshipd_ws_connections",
	}
	for _, m := range expectedMetrics {
		if !strings.Contains(output, m) {
			t.Errorf("expected metric %s in output", m)
		}
	}
}

// TestMetrics_RemoteWithoutTokenIs404 is the regression guard for F-003 —
// pre-fix any client could scrape; now non-loopback callers without
// CREWSHIP_METRICS_TOKEN get 404 (404 not 401 to avoid confirming the
// endpoint exists).
func TestMetrics_RemoteWithoutTokenIs404(t *testing.T) {
	t.Setenv("CREWSHIP_METRICS_TOKEN", "")
	s := newTestServer()
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.RemoteAddr = "203.0.113.50:55555" // public peer
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 from unauthorized scrape, got %d", w.Code)
	}
}

func TestWebSocketMissingToken(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/ws", nil)
	w := httptest.NewRecorder()

	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestIPCEndpoints(t *testing.T) {
	s := newTestServer()

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantField  string
		wantValue  string
	}{
		{"health", "GET", "/health", http.StatusOK, "status", "ok"},
		{"agent status", "GET", "/agents/test-uuid/status", http.StatusOK, "agent_id", "test-uuid"},
		{"agent start", "POST", "/agents/test-uuid/start", http.StatusServiceUnavailable, "error", "container provider not configured"},
		{"agent stop", "POST", "/agents/test-uuid/stop", http.StatusOK, "agent_id", "test-uuid"},
		{"container status", "GET", "/crews/crew-uuid/container/status", http.StatusOK, "crew_id", "crew-uuid"},
		{"container start", "POST", "/crews/crew-uuid/container/start", http.StatusServiceUnavailable, "error", "container provider not configured"},
		{"container stop", "POST", "/crews/crew-uuid/container/stop", http.StatusServiceUnavailable, "error", "container provider not configured"},
		{"file list", "GET", "/crews/crew-uuid/files", http.StatusOK, "crew_id", "crew-uuid"},
		{"chat messages", "GET", "/chats/chat-uuid/messages", http.StatusOK, "chat_id", "chat-uuid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()

			s.ipcMux.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("expected status %d, got %d", tt.wantStatus, w.Code)
			}

			body := parseJSON(t, w.Body.Bytes())
			if body[tt.wantField] != tt.wantValue {
				t.Errorf("expected %s=%q, got %v", tt.wantField, tt.wantValue, body[tt.wantField])
			}
		})
	}
}
