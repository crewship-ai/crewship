package terminal

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/gorilla/websocket"
)

// --- mock container provider ------------------------------------------------

type mockContainer struct {
	state string // "running", "stopped"
}

func (m *mockContainer) EnsureCrewRuntime(_ context.Context, c provider.CrewConfig) (string, error) {
	m.state = "running"
	return "container-" + c.Slug, nil
}
func (m *mockContainer) StopCrewRuntime(context.Context, string) error   { return nil }
func (m *mockContainer) RemoveCrewRuntime(context.Context, string) error { return nil }
func (m *mockContainer) ContainerStatus(_ context.Context, id string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{ID: id, State: m.state, Uptime: "1h"}, nil
}
func (m *mockContainer) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (m *mockContainer) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{ExecID: "exec-1", Reader: io.NopCloser(strings.NewReader(""))}, nil
}
func (m *mockContainer) ExecInspect(context.Context, string) (bool, int, error) {
	return false, 0, nil
}
func (m *mockContainer) CrewContainerName(slug string) string {
	return "crewship-team-" + slug
}
func (m *mockContainer) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}

// interactiveMock implements provider.InteractiveExecProvider on top of mockContainer.
type interactiveMock struct {
	*mockContainer
	execErr error
}

func (i *interactiveMock) ExecInteractive(_ context.Context, _ provider.InteractiveExecConfig) (*provider.InteractiveExecResult, error) {
	if i.execErr != nil {
		return nil, i.execErr
	}
	return &provider.InteractiveExecResult{ExecID: "exec-1", Conn: nopConn{}}, nil
}
func (i *interactiveMock) ExecResize(context.Context, string, uint16, uint16) error { return nil }

// nopConn satisfies io.ReadWriteCloser, blocking forever on reads so the
// terminal goroutine doesn't spin or terminate prematurely.
type nopConn struct{}

func (nopConn) Read(p []byte) (int, error)  { select {} }
func (nopConn) Write(p []byte) (int, error) { return len(p), nil }
func (nopConn) Close() error                { return nil }

// silentLogger discards log output to keep tests quiet.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newTestValidator(t *testing.T) *auth.JWTValidator {
	t.Helper()
	v, err := auth.NewJWTValidator("supersecretkeythatisatleast32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	return v
}

// dialTerminal wires the Handler behind an httptest server and returns a live
// websocket conn for the test to drive.
func dialTerminal(t *testing.T, h *Handler) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// readJSONFrame consumes one text frame and decodes it.
func readJSONFrame(t *testing.T, c *websocket.Conn) map[string]string {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	mt, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if mt != websocket.TextMessage {
		t.Fatalf("expected text frame, got %d", mt)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json: %v (data=%s)", err, data)
	}
	return m
}

func TestServeHTTP_NoValidatorReturns503(t *testing.T) {
	t.Parallel()
	h := New(&mockContainer{state: "running"}, nil, nil, silentLogger())
	req := httptest.NewRequest("GET", "/ws/terminal", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestServeHTTP_AuthMessageInvalid(t *testing.T) {
	t.Parallel()
	h := New(&mockContainer{state: "running"}, newTestValidator(t), nil, silentLogger())
	conn := dialTerminal(t, h)

	// Send malformed first message.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"foo":"bar"}`)); err != nil {
		t.Fatal(err)
	}
	got := readJSONFrame(t, conn)
	if got["type"] != "error" {
		t.Errorf("expected error frame, got %+v", got)
	}
	if !strings.Contains(strings.ToLower(got["message"]), "auth") {
		t.Errorf("message should mention auth, got %q", got["message"])
	}
}

func TestServeHTTP_AuthTokenInvalid(t *testing.T) {
	t.Parallel()
	h := New(&mockContainer{state: "running"}, newTestValidator(t), nil, silentLogger())
	conn := dialTerminal(t, h)

	auth, _ := json.Marshal(map[string]string{"type": "auth", "token": "not-a-real-token"})
	if err := conn.WriteMessage(websocket.TextMessage, auth); err != nil {
		t.Fatal(err)
	}
	got := readJSONFrame(t, conn)
	if got["type"] != "error" || got["message"] != "invalid token" {
		t.Errorf("expected invalid token error, got %+v", got)
	}
}

func TestServeHTTP_InitMissingFields(t *testing.T) {
	t.Parallel()
	v := newTestValidator(t)
	h := New(&mockContainer{state: "running"}, v, nil, silentLogger())

	tok, err := v.IssueWSTicket("u1", "test-session", "", "u1@example.com")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	conn := dialTerminal(t, h)
	authMsg, _ := json.Marshal(map[string]string{"type": "auth", "token": tok})
	if err := conn.WriteMessage(websocket.TextMessage, authMsg); err != nil {
		t.Fatal(err)
	}
	// Init with empty crew_slug + crew_id.
	init, _ := json.Marshal(map[string]any{"mode": "shell"})
	if err := conn.WriteMessage(websocket.TextMessage, init); err != nil {
		t.Fatal(err)
	}
	got := readJSONFrame(t, conn)
	if got["type"] != "error" {
		t.Errorf("expected error frame, got %+v", got)
	}
	if !strings.Contains(got["message"], "crew") {
		t.Errorf("expected crew-related error, got %q", got["message"])
	}
}

func TestServeHTTP_InvalidAgentSlugRejected(t *testing.T) {
	t.Parallel()
	v := newTestValidator(t)
	// Use a DB so verifyAccess succeeds. We seed user u1, workspace w1 (member),
	// and crew c1 in workspace w1.
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "term.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db.DB, silentLogger()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db.DB, `INSERT INTO users (id, email, created_at, updated_at) VALUES ('u1','u1@x',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES ('w1','WS','ws',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES ('wm1','w1','u1','OWNER',?)`, now)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('c1','w1','Crew','crew-a',?,?)`, now, now)

	h := New(&interactiveMock{mockContainer: &mockContainer{state: "running"}}, v, db.DB, silentLogger())

	tok, err := v.IssueWSTicket("u1", "test-session", "", "")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	conn := dialTerminal(t, h)
	authMsg, _ := json.Marshal(map[string]string{"type": "auth", "token": tok})
	conn.WriteMessage(websocket.TextMessage, authMsg)

	// agent_slug contains an illegal character — must be rejected.
	init, _ := json.Marshal(map[string]any{
		"mode":       "shell",
		"crew_id":    "c1",
		"crew_slug":  "crew-a",
		"agent_slug": "../etc",
	})
	if err := conn.WriteMessage(websocket.TextMessage, init); err != nil {
		t.Fatal(err)
	}
	got := readJSONFrame(t, conn)
	if got["type"] != "error" || !strings.Contains(got["message"], "agent_slug") {
		t.Errorf("expected agent_slug error, got %+v", got)
	}
}

func TestServeHTTP_AccessDeniedForNonMember(t *testing.T) {
	t.Parallel()
	v := newTestValidator(t)

	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "term.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db.DB, silentLogger()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// user uX is NOT in workspace w1
	mustExec(t, db.DB, `INSERT INTO users (id, email, created_at, updated_at) VALUES ('uX','x@x',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES ('w1','WS','ws',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('c1','w1','Crew','crew-a',?,?)`, now, now)

	h := New(&mockContainer{state: "running"}, v, db.DB, silentLogger())

	tok, err := v.IssueWSTicket("uX", "test-session", "", "")
	if err != nil {
		t.Fatalf("issue ws ticket: %v", err)
	}
	conn := dialTerminal(t, h)
	authMsg, _ := json.Marshal(map[string]string{"type": "auth", "token": tok})
	conn.WriteMessage(websocket.TextMessage, authMsg)

	init, _ := json.Marshal(map[string]any{"mode": "shell", "crew_id": "c1", "crew_slug": "crew-a"})
	conn.WriteMessage(websocket.TextMessage, init)

	got := readJSONFrame(t, conn)
	if got["type"] != "error" || got["message"] != "access denied" {
		t.Errorf("expected access denied error, got %+v", got)
	}
}

// TestVerifyAccess_NoDBFailsClosed pins Patch F: a nil db is a config
// bug, not "dev mode". The handler must refuse the terminal session
// rather than silently letting any valid JWT open a shell. Production's
// server.New panics on deps.DB == nil so this branch is unreachable
// there, but we still want it as a belt-and-braces guard against a
// future test fixture wiring a handler with no db.
func TestVerifyAccess_NoDBFailsClosed(t *testing.T) {
	t.Parallel()
	h := &Handler{logger: silentLogger()}
	err := h.verifyAccess(context.Background(), "any", "crew")
	if err == nil {
		t.Errorf("expected error with no DB (fail-closed), got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "not configured") {
		t.Errorf("error should mention misconfiguration; got %v", err)
	}
}

func TestVerifyAccess_ViewerRoleRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "va.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db.DB, silentLogger()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db.DB, `INSERT INTO users (id, email, created_at, updated_at) VALUES ('u1','u1@x',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES ('w1','WS','ws',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES ('wm1','w1','u1','VIEWER',?)`, now)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('c1','w1','Crew','c',?,?)`, now, now)

	h := &Handler{db: db.DB, logger: silentLogger()}
	err = h.verifyAccess(context.Background(), "u1", "c1")
	if err == nil || !strings.Contains(err.Error(), "insufficient role") {
		t.Errorf("expected insufficient role error, got %v", err)
	}
}

func TestVerifyAccess_MemberRoleAllowed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "va.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db.DB, silentLogger()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db.DB, `INSERT INTO users (id, email, created_at, updated_at) VALUES ('u1','u1@x',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES ('w1','WS','ws',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES ('wm1','w1','u1','MEMBER',?)`, now)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('c1','w1','Crew','c',?,?)`, now, now)

	h := &Handler{db: db.DB, logger: silentLogger()}
	if err := h.verifyAccess(context.Background(), "u1", "c1"); err != nil {
		t.Errorf("unexpected: %v", err)
	}
}

func TestValidSlugRegex(t *testing.T) {
	t.Parallel()
	good := []string{"agent-1", "alpha", "x_y_z", "Z9", "a-b-c"}
	bad := []string{"", "-leading", "_leading", "../etc", "with space", "x.y", "x/y"}
	for _, s := range good {
		if !validSlugRe.MatchString(s) {
			t.Errorf("expected %q to match", s)
		}
	}
	for _, s := range bad {
		if validSlugRe.MatchString(s) {
			t.Errorf("expected %q NOT to match", s)
		}
	}
}

func TestWriteError_AndWriteInfo(t *testing.T) {
	t.Parallel()
	h := New(&mockContainer{}, nil, nil, silentLogger())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := h.upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer c.Close()
		h.writeError(c, "oops")
		h.writeInfo(c, "yo")
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	got := readJSONFrame(t, conn)
	if got["type"] != "error" || got["message"] != "oops" {
		t.Errorf("unexpected error frame: %+v", got)
	}
	got2 := readJSONFrame(t, conn)
	if got2["type"] != "info" || got2["message"] != "yo" {
		t.Errorf("unexpected info frame: %+v", got2)
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
