package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/testutil"
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
//
// The schema comes from testutil's process-wide migrated template
// (one migration run per test binary, then a ~1ms file copy per call)
// rather than from a per-call database.Migrate. This helper is invoked
// ~80 times in this package and the migration chain was the single
// largest contributor to its wall-clock. The template is produced by
// database.Migrate and opened with database.Open, so the schema and
// the pragmas (foreign_keys ON, WAL, busy_timeout) are the same ones
// production runs — including the busy_timeout that keeps the
// lifecycle boot tests from flaking on a transient writer lock when
// recoverOrphanedRuns writes while a test polls a row.
func openTestDB(t *testing.T) *sql.DB {
	if t == nil {
		// No t.Cleanup hook is available. Same contract as before: the
		// per-call temp file leaks until the process exits, which for
		// bare newTestServer() callers means "immediately after".
		db, _, err := testutil.NewMigratedDB()
		if err != nil {
			panic(err)
		}
		return db.DB
	}
	return testutil.MigratedSQLDB(t)
}

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

// TestWebSocketUnauthenticated replaces the old TestWebSocketMissingToken:
// /ws no longer 401s pre-upgrade. Since #1254 pt.2 the hub upgrades
// unconditionally and authenticates via the first post-upgrade frame
// (ws.Hub.authenticateUpgradedConn) — a `?token=` query param would leak
// the ticket into proxy/access logs, browser history, and Referer headers.
// This test drives the production route (routes.go GET /ws → s.wsHub),
// not the hub directly: a client whose first frame is not a valid auth
// message gets an error frame back and the connection is closed without
// ever being registered.
func TestWebSocketUnauthenticated(t *testing.T) {
	s := newTestServerForT(t)
	srv := httptest.NewServer(s.mux)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, err := websocket.Dial(wsURL, "", srv.URL)
	if err != nil {
		t.Fatalf("dial (upgrade must succeed pre-auth): %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// First frame is not an auth message — the server must reject the
	// connection instead of treating it as authenticated.
	if _, err := conn.Write([]byte(`{"type":"subscribe","channel":"chat:general"}`)); err != nil {
		t.Fatalf("write first frame: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := websocket.Message.Receive(conn, &raw); err != nil {
		t.Fatalf("expected an auth-error frame before close, got read error: %v", err)
	}
	var frame struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("non-JSON frame %q: %v", raw, err)
	}
	if frame.Type != "error" {
		t.Fatalf("expected type=error frame, got %q", raw)
	}
	// After the rejection the server closes the socket — no client is
	// registered, so no further frames may arrive.
	if err := websocket.Message.Receive(conn, &raw); err == nil {
		t.Fatalf("expected the connection to be closed after rejected auth, got frame %q", raw)
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
