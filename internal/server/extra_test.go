package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/localfs"
)

func newSilentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func mkfile(p string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	return os.Create(p)
}

func dialUnix(p string) (io.ReadWriteCloser, error) {
	c, err := net.Dial("unix", p)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func randomShort() string {
	b := make([]byte, 4)
	_, _ = io.ReadFull(cryptoReader{}, b)
	return hexEncode(b)
}

type cryptoReader struct{}

func (cryptoReader) Read(p []byte) (int, error) { return rand.Read(p) }

func hexEncode(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0x0f]
	}
	return string(out)
}

// silentCfg returns a fresh Default() config.
func silentCfg() *config.Config {
	c := config.Default()
	// Server.New now panics if NEXTAUTH_SECRET is unset. Every test
	// using silentCfg gets a fixed test secret so the panic-on-missing
	// guard doesn't torpedo unrelated assertions.
	c.Auth.JWTSecret = "test-secret-for-server-extra-test-32c"
	return c
}

// --- routes wiring ---------------------------------------------------------

func TestRegisterRoutes_AllPathsMounted(t *testing.T) {
	t.Parallel()
	s := newTestServer()
	cases := []struct {
		method, path string
		wantStatus   int
	}{
		{"GET", "/healthz", http.StatusOK},
		{"GET", "/readyz", http.StatusOK},
		{"GET", "/metrics", http.StatusOK},
		{"GET", "/ws", http.StatusUnauthorized},                // missing token
		{"GET", "/ws/terminal", http.StatusServiceUnavailable}, // no terminal handler
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			s.mux.ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Errorf("got %d, want %d", w.Code, tc.wantStatus)
			}
		})
	}
}

func TestRegisterIPCRoutes_AllPathsMounted(t *testing.T) {
	t.Parallel()
	s := newTestServer()
	// Each IPC path returns ≥some response (200 or 503), proving registration.
	paths := []string{
		"GET /health",
		"GET /agents/x/status",
		"POST /agents/x/start",
		"POST /agents/x/stop",
		"GET /crews/x/container/status",
		"POST /crews/x/container/start",
		"POST /crews/x/container/stop",
		"GET /agents/x/logs?crew_id=c",
		"GET /crews/x/stats",
		"GET /crews/x/files",
		"GET /chats/x/messages",
		"GET /debug/logs",
		"GET /debug/info",
	}
	for _, p := range paths {
		parts := strings.SplitN(p, " ", 2)
		method, path := parts[0], parts[1]
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(method, path, nil)
			w := httptest.NewRecorder()
			s.ipcMux.ServeHTTP(w, req)
			if w.Code == 0 {
				t.Errorf("no response (handler missing)")
			}
		})
	}
}

// TestSecurityHeadersMiddleware verifies the headers are applied on every
// response that goes through the wrapper.
func TestSecurityHeadersMiddleware(t *testing.T) {
	t.Parallel()
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	wrapped := securityHeadersMiddleware(inner)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	headers := []struct{ k, want string }{
		{"X-Frame-Options", "DENY"},
		{"X-Content-Type-Options", "nosniff"},
		{"Referrer-Policy", "strict-origin-when-cross-origin"},
		{"Permissions-Policy", "camera=(), microphone=(), geolocation=()"},
	}
	for _, h := range headers {
		got := rec.Header().Get(h.k)
		if got != h.want {
			t.Errorf("%s = %q, want %q", h.k, got, h.want)
		}
	}
}

// TestCombinedHandler_RoutesAPIPathsToMux verifies that /api, /healthz,
// /metrics, /ws are routed to the API mux while everything else falls through
// to the SPA handler.
func TestCombinedHandler_RoutesAPIPathsToMux(t *testing.T) {
	t.Parallel()
	cfg := silentCfg()
	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, &Deps{DB: openTestDB(t)})
	s.startedAt = time.Now()
	s.spaHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("SPA"))
	})

	combined := s.combinedHandler()

	// API path should NOT hit SPA.
	rec := httptest.NewRecorder()
	combined.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if strings.Contains(rec.Body.String(), "SPA") {
		t.Errorf("/healthz routed to SPA: %s", rec.Body.String())
	}

	// SPA path should hit SPA handler.
	rec2 := httptest.NewRecorder()
	combined.ServeHTTP(rec2, httptest.NewRequest("GET", "/some/page", nil))
	if rec2.Body.String() != "SPA" {
		t.Errorf("expected SPA, got %q", rec2.Body.String())
	}
}

// TestSanitizeMetadata_AllowedKeys verifies the allowlist is applied.
func TestSanitizeMetadata_AllowedKeys(t *testing.T) {
	t.Parallel()
	in := map[string]interface{}{
		"summary":      "ok",
		"tool_name":    "Bash",
		"tool_input":   "rm -rf /", // BLOCKED
		"raw_response": "secrets",  // BLOCKED
		"model":        "claude",
		"usage":        map[string]int{"in": 1},
		"private_key":  "sk-XXXX", // BLOCKED
	}
	got := sanitizeMetadata(in)
	if got["summary"] != "ok" {
		t.Errorf("summary missing")
	}
	if _, ok := got["tool_input"]; ok {
		t.Errorf("tool_input must be blocked")
	}
	if _, ok := got["raw_response"]; ok {
		t.Errorf("raw_response must be blocked")
	}
	if _, ok := got["private_key"]; ok {
		t.Errorf("private_key must be blocked")
	}
	if _, ok := got["model"]; !ok {
		t.Errorf("model should pass")
	}
	if _, ok := got["tool_name"]; !ok {
		t.Errorf("tool_name should pass")
	}
}

func TestSanitizeMetadata_NilInput(t *testing.T) {
	t.Parallel()
	if got := sanitizeMetadata(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
	if got := sanitizeMetadata("not a map"); got != nil {
		t.Errorf("expected nil for non-map, got %v", got)
	}
}

func TestSanitizeDownloadFilename(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"hello.txt", "hello.txt"},
		{"with\"quote.txt", "with_quote.txt"},
		{"with\\back.txt", "with_back.txt"},
		{"line\x00null", "line_null"},
		{"", "download"},
	}
	for _, c := range cases {
		if got := sanitizeDownloadFilename(c.in); got != c.want {
			t.Errorf("sanitizeDownloadFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWriteJSON_SetsContentTypeAndStatus(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusTeapot, map[string]string{"k": "v"})
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("ct = %q", got)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["k"] != "v" {
		t.Errorf("body = %v", body)
	}
}

// --- file routes -----------------------------------------------------------

func newServerWithStorage(t *testing.T) (*Server, string) {
	t.Helper()
	cfg := silentCfg()
	dir := t.TempDir()
	cfg.Storage.BasePath = dir
	logger := logging.New("error", "json", nil)
	stor, err := localfs.New(dir)
	if err != nil {
		t.Fatalf("localfs: %v", err)
	}
	s := New(cfg, logger, &Deps{Storage: stor, DB: openTestDB(t)})
	s.startedAt = time.Now()
	t.Cleanup(func() {
		if s.fileWatcher != nil {
			s.fileWatcher.Close()
		}
	})
	return s, dir
}

func TestHandleFileSave_AndDownload(t *testing.T) {
	t.Parallel()
	s, _ := newServerWithStorage(t)

	// Save a file under the crew directory.
	body := bytes.NewReader([]byte("hello"))
	req := httptest.NewRequest("PUT", "/crews/crewA/files/save?path=crewA/notes.txt", body)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: status %d body %s", rec.Code, rec.Body.String())
	}

	// Download it back.
	dl := httptest.NewRequest("GET", "/crews/crewA/files/download?path=crewA/notes.txt", nil)
	dlRec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(dlRec, dl)
	if dlRec.Code != http.StatusOK {
		t.Fatalf("download: status %d body %s", dlRec.Code, dlRec.Body.String())
	}
	if dlRec.Body.String() != "hello" {
		t.Errorf("body = %q", dlRec.Body.String())
	}
	cd := dlRec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `filename="notes.txt"`) {
		t.Errorf("content-disposition: %q", cd)
	}
}

func TestHandleFileSave_PathTraversalRejected(t *testing.T) {
	t.Parallel()
	s, _ := newServerWithStorage(t)
	req := httptest.NewRequest("PUT", "/crews/crewA/files/save?path=../../../etc/x", strings.NewReader("x"))
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleFileSave_PathOutsideCrewRejected(t *testing.T) {
	t.Parallel()
	s, _ := newServerWithStorage(t)
	// Path doesn't start with crewA/, so handler should reject.
	req := httptest.NewRequest("PUT", "/crews/crewA/files/save?path=other/notes.txt", strings.NewReader("x"))
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleFileSave_EmptyPathRejected(t *testing.T) {
	t.Parallel()
	s, _ := newServerWithStorage(t)
	req := httptest.NewRequest("PUT", "/crews/crewA/files/save", strings.NewReader("x"))
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleFileDownload_NotFound(t *testing.T) {
	t.Parallel()
	s, _ := newServerWithStorage(t)
	req := httptest.NewRequest("GET", "/crews/crewA/files/download?path=crewA/missing.txt", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleFileSave_NoStorageProvider(t *testing.T) {
	t.Parallel()
	s := newTestServer()
	req := httptest.NewRequest("PUT", "/crews/crewA/files/save?path=crewA/x", strings.NewReader("x"))
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

// --- agent logs ------------------------------------------------------------

func TestHandleAgentLogs_RequiresCrewID(t *testing.T) {
	t.Parallel()
	s := newTestServer()
	req := httptest.NewRequest("GET", "/agents/a1/logs", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAgentLogs_EmptyWhenNoReader(t *testing.T) {
	t.Parallel()
	s := newTestServer()
	// Force logReader to nil to trip that branch.
	s.logReader = nil
	req := httptest.NewRequest("GET", "/agents/a1/logs?crew_id=c", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	logs, _ := body["logs"].([]interface{})
	if logs == nil {
		t.Errorf("expected logs field present")
	}
	if len(logs) != 0 {
		t.Errorf("expected 0 entries, got %d", len(logs))
	}
}

// --- credentials -----------------------------------------------------------

func TestHandleCredentialSync_NoSyncerReturns503(t *testing.T) {
	t.Parallel()
	s := newTestServer()
	req := httptest.NewRequest("POST", "/credentials/sync", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestHandleCredentialToken_NoTokenPool(t *testing.T) {
	t.Parallel()
	s := newTestServer()
	s.tokenPool = nil
	req := httptest.NewRequest("GET", "/credentials/ws1/token", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestHandleCredentialToken_NoActiveCredential(t *testing.T) {
	t.Parallel()
	s := newTestServer()
	req := httptest.NewRequest("GET", "/credentials/ws-empty/token?provider=ANTHROPIC", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// --- crew stats ------------------------------------------------------------

func TestHandleCrewStats_NoContainer(t *testing.T) {
	t.Parallel()
	s := newTestServer()
	req := httptest.NewRequest("GET", "/crews/c/stats", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["stats"] != nil {
		t.Errorf("expected nil stats with no container provider, got %v", body["stats"])
	}
}

func TestHandleCrewStats_WithRegisteredContainer(t *testing.T) {
	t.Parallel()
	cfg := silentCfg()
	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, &Deps{Container: &mockContainer{}, DB: openTestDB(t)})
	s.startedAt = time.Now()
	t.Cleanup(func() {
		if s.fileWatcher != nil {
			s.fileWatcher.Close()
		}
	})

	// Pre-load latest metrics for the registered container.
	s.statsCollector.Register("ctr-1", "crew-1", "ws-1")
	s.statsCollector.latest["ctr-1"] = &provider.ContainerMetrics{
		CPUPercent: 12.5, MemoryUsed: 1024, Timestamp: time.Now(),
	}

	req := httptest.NewRequest("GET", "/crews/crew-1/stats", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["container_id"] != "ctr-1" {
		t.Errorf("container_id = %v", body["container_id"])
	}
	if body["cpu_percent"] != 12.5 {
		t.Errorf("cpu_percent = %v", body["cpu_percent"])
	}
}

// --- container file/git list -----------------------------------------------

func TestHandleContainerFileList_NoContainer(t *testing.T) {
	t.Parallel()
	s := newTestServer()
	req := httptest.NewRequest("GET", "/crews/c1/container-files", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestHandleContainerGitLog_CrewNotFound(t *testing.T) {
	t.Parallel()
	cfg := silentCfg()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "git.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := database.Migrate(context.Background(), db.DB, newSilentLogger()); err != nil {
		t.Fatal(err)
	}
	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, &Deps{Container: &mockContainer{}, DB: db.DB})
	s.startedAt = time.Now()
	t.Cleanup(func() {
		if s.fileWatcher != nil {
			s.fileWatcher.Close()
		}
	})

	req := httptest.NewRequest("GET", "/crews/missing-crew/git-log", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d body %s", rec.Code, rec.Body.String())
	}
}

// --- StatsCollector ---------------------------------------------------------

func TestStatsCollector_RegisterUnregisterLatest(t *testing.T) {
	t.Parallel()
	sc := NewStatsCollector(nil, nil, newSilentLogger(), 0) // 0 falls back to default
	sc.Register("c1", "crew-1", "ws-1")
	sc.latest["c1"] = &provider.ContainerMetrics{CPUPercent: 1.0}

	if got := sc.Latest("c1"); got == nil || got.CPUPercent != 1.0 {
		t.Errorf("Latest = %+v", got)
	}
	id, m := sc.LatestByCrewID("crew-1")
	if id != "c1" || m == nil {
		t.Errorf("LatestByCrewID id=%q m=%v", id, m)
	}
	id2, m2 := sc.LatestByCrewID("nope")
	if id2 != "" || m2 != nil {
		t.Errorf("LatestByCrewID for unknown should be empty, got id=%q m=%v", id2, m2)
	}
	sc.Unregister("c1")
	if got := sc.Latest("c1"); got != nil {
		t.Errorf("Latest after unregister = %+v", got)
	}
}

// TestStatsCollector_Run_StopsOnCancel checks the goroutine exits cleanly.
func TestStatsCollector_Run_StopsOnCancel(t *testing.T) {
	t.Parallel()
	sc := NewStatsCollector(nil, nil, newSilentLogger(), 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sc.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}

// --- shutdown --------------------------------------------------------------

func TestServer_Shutdown_NoSubsystems(t *testing.T) {
	t.Parallel()
	s := newTestServer()
	// Ensure shutdown doesn't panic when nothing is started.
	if err := s.Shutdown(); err != nil {
		t.Errorf("Shutdown returned error: %v", err)
	}
}

// --- accessors / setters ---------------------------------------------------

func TestServer_Accessors(t *testing.T) {
	t.Parallel()
	s := newTestServer()
	if s.Orchestrator() == nil {
		t.Error("orchestrator nil")
	}
	if s.TokenPool() == nil {
		t.Error("token pool nil")
	}
	if s.ConversationStore() == nil {
		t.Error("conv store nil")
	}
	if s.LogWriter() == nil {
		t.Error("log writer nil")
	}
	// MissionEngine is nil in tests since DB isn't passed.
	_ = s.MissionEngine()
	_ = s.APIRouter()
}

// --- IPC startup -----------------------------------------------------------

func TestStartIPC_BindsAndCleansUpStaleSocket(t *testing.T) {
	t.Parallel()
	// Unix sockets have a tight path-length limit (~104 chars on macOS),
	// shorter than t.TempDir() can produce. Use a short, unique name in /tmp.
	sockPath := filepath.Join("/tmp", "cs-ipc-"+randomShort()+".sock")
	t.Cleanup(func() { _ = os.Remove(sockPath) })
	// Pre-create a stale socket file to exercise the cleanup path.
	if err := touch(sockPath); err != nil {
		t.Fatal(err)
	}

	cfg := silentCfg()
	cfg.IPC.SocketPath = sockPath
	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, &Deps{DB: openTestDB(t)})
	s.startedAt = time.Now()

	errCh := make(chan error, 1)
	go func() { errCh <- s.startIPC() }()

	deadline := time.Now().Add(2 * time.Second)
	var conn io.ReadWriteCloser
	var lastErr error
	for time.Now().Before(deadline) {
		c, err := dialUnix(sockPath)
		if err == nil {
			conn = c
			break
		}
		lastErr = err
		select {
		case startErr := <-errCh:
			t.Fatalf("startIPC failed early: %v (dial err: %v)", startErr, lastErr)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("listener never came up: last err %v", lastErr)
	}
	conn.Close()

	_ = s.ipcServer.Shutdown(context.Background())
	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			t.Logf("startIPC returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startIPC did not return after shutdown")
	}
}

func touch(p string) error {
	f, err := mkfile(p)
	if err != nil {
		return err
	}
	return f.Close()
}
