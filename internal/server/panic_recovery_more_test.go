package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/crashreport"
	"github.com/crewship-ai/crewship/internal/database"
)

// captureRecord is one event captured by the fake crashreport backend.
type captureRecord struct {
	err  error
	tags map[string]string
}

// recordingCrashBackend is a crashreport.Backend stub that records every
// Capture call so tests can assert tag shape + error message without
// standing up a Sentry client. Mirrors the unexported fakeBackend in
// internal/crashreport but is duplicated here because tests in different
// packages can't share unexported types.
type recordingCrashBackend struct {
	mu       sync.Mutex
	captured []captureRecord
}

func (r *recordingCrashBackend) Init(_, _, _ string) error { return nil }

func (r *recordingCrashBackend) Capture(err error, tags map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Defensive copy so a caller mutating the tag map after the call
	// (unlikely but legal per the Backend contract) can't tamper with
	// the recorded snapshot.
	cp := make(map[string]string, len(tags))
	for k, v := range tags {
		cp[k] = v
	}
	r.captured = append(r.captured, captureRecord{err: err, tags: cp})
}

func (r *recordingCrashBackend) Flush(_ time.Duration) {}

func (r *recordingCrashBackend) snapshot() []captureRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]captureRecord, len(r.captured))
	copy(out, r.captured)
	return out
}

// enableCrashCapture wires a recording backend into crashreport.State so
// the middleware's Capture call actually reaches it. crashreport.Capture
// is a silent no-op until State.enabled=true AND a DSN is resolved AND
// Init has succeeded, so we set DSN, opt in via the app_settings table,
// then run Init() against an in-memory DB.
//
// Returns the recorder + a cleanup that restores the previous backend
// and DSN. Each call is isolated; t.Cleanup wires the teardown.
func enableCrashCapture(t *testing.T) *recordingCrashBackend {
	t.Helper()

	// In-memory SQLite + migrations so consentState / SetOptIn / Init can
	// read and write app_settings.
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "crash.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	prevDSN := crashreport.DSN
	crashreport.DSN = "https://fake@sentry.example/1"
	prevBackend := crashreport.CurrentBackend()
	rec := &recordingCrashBackend{}
	crashreport.SetBackend(rec)

	if _, _, err := crashreport.SetOptIn(context.Background(), db.DB, true); err != nil {
		t.Fatalf("opt in: %v", err)
	}
	if err := crashreport.Init(context.Background(), db.DB, "v0.0.0-test", slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("crashreport Init: %v", err)
	}
	if !crashreport.IsEnabled() {
		t.Fatalf("crashreport must be enabled after opt-in + DSN")
	}

	t.Cleanup(func() {
		crashreport.SetBackend(prevBackend)
		crashreport.DSN = prevDSN
	})
	return rec
}

// jsonLogBuf returns a slog.Logger that writes JSON-encoded records into
// the returned buffer so tests can decode and assert attribute keys +
// values precisely. Text handlers split values across spaces and break
// on stack traces with newlines; JSON is the only format that survives
// round-trip cleanly.
func jsonLogBuf() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	l := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return l, buf
}

// TestPanicRecovery_LogsStructuredFields_WithMethodPathPanicStack guards
// the operator-facing log shape: a recovered panic must emit a single
// ERROR record carrying method, path, the panic value, and a stack
// trace. The dashboard alert query depends on these field names, so
// renames here ripple to ops tooling.
func TestPanicRecovery_LogsStructuredFields_WithMethodPathPanicStack(t *testing.T) {
	t.Parallel()

	panicker := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("specific-panic-marker-xyz")
	})
	logger, buf := jsonLogBuf()
	wrapped := panicRecoveryMiddleware(logger, panicker)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/structured-fields", nil)
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", rec.Code)
	}

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatalf("decode log line: %v\nraw: %q", err, buf.String())
	}
	if got, want := entry["msg"], "HTTP handler panic recovered"; got != want {
		t.Errorf("msg: want %q, got %q", want, got)
	}
	if got, want := entry["level"], "ERROR"; got != want {
		t.Errorf("level: want %q, got %v", want, got)
	}
	if got, want := entry["method"], http.MethodPost; got != want {
		t.Errorf("method: want %q, got %v", want, got)
	}
	if got, want := entry["path"], "/api/v1/structured-fields"; got != want {
		t.Errorf("path: want %q, got %v", want, got)
	}
	if got, want := entry["panic"], "specific-panic-marker-xyz"; got != want {
		t.Errorf("panic: want %q, got %v", want, got)
	}
	stack, ok := entry["stack"].(string)
	if !ok || stack == "" {
		t.Errorf("stack field missing or not a string: %v", entry["stack"])
	}
	// runtime/debug.Stack always names this package in the goroutine
	// dump — if it doesn't, the middleware swapped to runtime.Caller
	// or similar and lost frame context for ops.
	if !strings.Contains(stack, "panic_recovery") {
		t.Errorf("stack should mention panic_recovery frame, got: %s", stack)
	}
}

// TestPanicRecovery_CallsCrashreportCapture_WithSurfaceTags locks in the
// crashreport.Capture contract: tags must include surface=http_handler,
// method, and path, and the captured error must mention the route. The
// Sentry dashboard groups events by these tags; a regression here turns
// every recovered panic into a same-issue lump instead of route-bucketed
// signal.
func TestPanicRecovery_CallsCrashreportCapture_WithSurfaceTags(t *testing.T) {
	// Not parallel — toggling crashreport.DSN + CurrentBackend is a
	// process-global mutation. The state holder is RW-lock-guarded but
	// running parallel subtests would still race the DSN var swap.
	cap := enableCrashCapture(t)

	panicker := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(errors.New("synthesised-handler-failure"))
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	wrapped := panicRecoveryMiddleware(logger, panicker)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/tagged-capture", nil)
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", rec.Code)
	}

	events := cap.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected exactly one Capture call, got %d", len(events))
	}
	ev := events[0]
	if ev.err == nil {
		t.Fatal("captured error must not be nil")
	}
	if !strings.Contains(ev.err.Error(), "PATCH") || !strings.Contains(ev.err.Error(), "/api/v1/tagged-capture") {
		t.Errorf("captured error should mention method+path, got %q", ev.err.Error())
	}
	if !strings.Contains(ev.err.Error(), "synthesised-handler-failure") {
		t.Errorf("captured error should wrap the panic value, got %q", ev.err.Error())
	}
	if got, want := ev.tags["surface"], "http_handler"; got != want {
		t.Errorf("tag surface: want %q, got %q", want, got)
	}
	if got, want := ev.tags["method"], "PATCH"; got != want {
		t.Errorf("tag method: want %q, got %q", want, got)
	}
	if got, want := ev.tags["path"], "/api/v1/tagged-capture"; got != want {
		t.Errorf("tag path: want %q, got %q", want, got)
	}
}

// TestPanicRecovery_WebSocketUpgrade_DoesNotWrite500Body locks in the
// hijack-safe path: once the request is identified as a WebSocket
// upgrade (path /ws, /ws/terminal, or Upgrade: websocket header), the
// middleware must log + report but NOT call WriteHeader/Write. Emitting
// a 500 after the ws layer has hijacked corrupts the in-flight frame
// stream.
func TestPanicRecovery_WebSocketUpgrade_DoesNotWrite500Body(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		path    string
		upgrade string
	}{
		{"ws path", "/ws", ""},
		{"ws terminal path", "/ws/terminal", ""},
		{"upgrade header on api path", "/api/v1/agent/stream", "websocket"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			panicker := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				panic("ws-handler-blew-up")
			})
			logger, buf := jsonLogBuf()
			wrapped := panicRecoveryMiddleware(logger, panicker)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.upgrade != "" {
				req.Header.Set("Upgrade", tc.upgrade)
			}
			wrapped.ServeHTTP(rec, req)

			// ResponseRecorder.Code defaults to 200 when WriteHeader is
			// never called — that's exactly the signal that the
			// middleware bailed before writing a status.
			if rec.Code != http.StatusOK {
				t.Errorf("ws upgrade path: middleware must NOT write a status, got %d", rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("ws upgrade path: middleware must NOT write a body, got %q", rec.Body.String())
			}
			// The log line must still record the panic — operators
			// need the signal even though the response was suppressed.
			if !strings.Contains(buf.String(), "HTTP handler panic recovered") {
				t.Errorf("ws upgrade path: panic must still be logged, log was: %s", buf.String())
			}
			if !strings.Contains(buf.String(), "ws-handler-blew-up") {
				t.Errorf("ws upgrade path: log must include panic value, log was: %s", buf.String())
			}
		})
	}
}

// flushAfterHeaderWriter is a ResponseWriter that records whether
// WriteHeader was called multiple times so the test can assert the
// middleware's 500 attempt landed as a no-op after the handler already
// flushed.
type flushAfterHeaderWriter struct {
	headers      http.Header
	body         bytes.Buffer
	statusCalls  []int
	wroteHeader  bool
	bytesWritten int
}

func newFlushWriter() *flushAfterHeaderWriter {
	return &flushAfterHeaderWriter{headers: http.Header{}}
}

func (w *flushAfterHeaderWriter) Header() http.Header { return w.headers }

func (w *flushAfterHeaderWriter) WriteHeader(code int) {
	w.statusCalls = append(w.statusCalls, code)
	// Mimic net/http's real behaviour: only the first WriteHeader wins.
	// Subsequent calls become observable here (we count them) but do not
	// overwrite the committed status. This is what the production
	// middleware comment describes as "WriteHeader is a no-op".
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
}

func (w *flushAfterHeaderWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.body.Write(b)
	w.bytesWritten += n
	return n, err
}

// TestPanicRecovery_PanicAfterWriteHeader_NoCrash exercises the documented
// degraded path: when the handler already flushed a status before
// panicking, the middleware's WriteHeader(500) is a no-op and the body
// gets a tail-appended "internal server error" — the Go runtime logs
// the no-op WriteHeader itself, but the process must not crash and the
// committed status must not be retroactively changed.
func TestPanicRecovery_PanicAfterWriteHeader_NoCrash(t *testing.T) {
	t.Parallel()

	panicker := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("partial-payload"))
		panic("after-flush")
	})
	logger, buf := jsonLogBuf()
	wrapped := panicRecoveryMiddleware(logger, panicker)

	fw := newFlushWriter()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/after-flush", nil)

	// Must not propagate; the recover() is the contract under test.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic leaked past middleware: %v", r)
		}
	}()
	wrapped.ServeHTTP(fw, req)

	// The handler committed 202 first; that's what stays committed even
	// though the middleware tried to overwrite with 500. The second
	// WriteHeader call IS observable (statusCalls len 2) — that's the
	// "Go runtime would log a no-op WriteHeader" signal.
	if len(fw.statusCalls) < 2 {
		t.Fatalf("middleware should have attempted a second WriteHeader, calls=%v", fw.statusCalls)
	}
	if fw.statusCalls[0] != http.StatusAccepted {
		t.Errorf("first WriteHeader should be 202 (handler's commit), got %d", fw.statusCalls[0])
	}
	if fw.statusCalls[1] != http.StatusInternalServerError {
		t.Errorf("middleware should have attempted 500, got %d", fw.statusCalls[1])
	}
	body := fw.body.String()
	if !strings.Contains(body, "partial-payload") {
		t.Errorf("body should retain handler's pre-panic write, got %q", body)
	}
	if !strings.Contains(body, "internal server error") {
		t.Errorf("middleware should still attempt the 500 body even after flush, got %q", body)
	}
	if !strings.Contains(buf.String(), "after-flush") {
		t.Errorf("log should still record the panic value, got: %s", buf.String())
	}
}
