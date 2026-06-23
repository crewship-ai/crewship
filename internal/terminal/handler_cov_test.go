package terminal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/gorilla/websocket"
)

// covContainer is a recording ContainerProvider with scriptable status
// transitions and exec results, used to drive the deeper ServeHTTP paths
// (container start, attach mode, full bridge) that the smoke mocks in
// handler_test.go don't reach.
type covContainer struct {
	mu             sync.Mutex
	states         []string // consumed one per ContainerStatus call; last repeats
	ensureErr      error
	ensureCalls    int
	execErr        error
	execCalls      []provider.ExecConfig
	inspectRunning bool
	inspectExit    int
	inspectErr     error
}

func (m *covContainer) EnsureCrewRuntime(_ context.Context, c provider.CrewConfig) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureCalls++
	if m.ensureErr != nil {
		return "", m.ensureErr
	}
	m.states = []string{"running"}
	return "container-" + c.Slug, nil
}
func (m *covContainer) StopCrewRuntime(context.Context, string) error   { return nil }
func (m *covContainer) RemoveCrewRuntime(context.Context, string) error { return nil }
func (m *covContainer) ContainerStatus(_ context.Context, id string) (*provider.ContainerStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := "running"
	if len(m.states) > 0 {
		state = m.states[0]
		if len(m.states) > 1 {
			m.states = m.states[1:]
		}
	}
	return &provider.ContainerStatus{ID: id, State: state}, nil
}
func (m *covContainer) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (m *covContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execCalls = append(m.execCalls, cfg)
	if m.execErr != nil {
		return nil, m.execErr
	}
	return &provider.ExecResult{ExecID: "exec-cov", Reader: io.NopCloser(strings.NewReader(""))}, nil
}
func (m *covContainer) ExecInspect(context.Context, string) (bool, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inspectRunning, m.inspectExit, m.inspectErr
}
func (m *covContainer) CrewContainerName(slug string) string { return "crewship-team-" + slug }
func (m *covContainer) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}

func (m *covContainer) lastExecCmd() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.execCalls) == 0 {
		return nil
	}
	return m.execCalls[len(m.execCalls)-1].Cmd
}

// covInteractive layers InteractiveExecProvider on covContainer, handing
// the bridge a real net.Pipe so the test can speak PTY from the other end.
type covInteractive struct {
	*covContainer
	mu          sync.Mutex
	conn        net.Conn // server side of the pipe handed to the handler
	gotCfg      *provider.InteractiveExecConfig
	resizeCalls [][2]uint16
}

func (i *covInteractive) ExecInteractive(_ context.Context, cfg provider.InteractiveExecConfig) (*provider.InteractiveExecResult, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	c := cfg
	i.gotCfg = &c
	return &provider.InteractiveExecResult{ExecID: "iexec-cov", Conn: i.conn}, nil
}

func (i *covInteractive) ExecResize(_ context.Context, _ string, rows, cols uint16) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.resizeCalls = append(i.resizeCalls, [2]uint16{rows, cols})
	return nil
}

func (i *covInteractive) config() *provider.InteractiveExecConfig {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.gotCfg
}

func (i *covInteractive) resizes() [][2]uint16 {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([][2]uint16, len(i.resizeCalls))
	copy(out, i.resizeCalls)
	return out
}

// seedTerminalDB opens a migrated SQLite DB with user u1 as OWNER of
// workspace w1 owning crew c1 (slug crew-a).
func seedTerminalDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "termcov.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db.DB, silentLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db.DB, `INSERT INTO users (id, email, created_at, updated_at) VALUES ('u1','u1@x',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES ('w1','WS','ws',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES ('wm1','w1','u1','OWNER',?)`, now)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('c1','w1','Crew','crew-a',?,?)`, now, now)
	return db.DB
}

// dialTerminalDone is like dialTerminal but also returns a channel closed
// when ServeHTTP returns, so tests can assert session termination.
func dialTerminalDone(t *testing.T, h *Handler) (*websocket.Conn, <-chan struct{}) {
	t.Helper()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		close(done)
	}))
	t.Cleanup(srv.Close)

	header := http.Header{}
	header.Set("X-Crewship-Client", "test")
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), header)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, done
}

func authAndInit(t *testing.T, conn *websocket.Conn, v *auth.JWTValidator, init map[string]any) {
	t.Helper()
	tok, err := v.IssueWSTicket("u1", "sess", "", "u1@x")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	authMsg, _ := json.Marshal(map[string]string{"type": "auth", "token": tok})
	if err := conn.WriteMessage(websocket.TextMessage, authMsg); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	initMsg, _ := json.Marshal(init)
	if err := conn.WriteMessage(websocket.TextMessage, initMsg); err != nil {
		t.Fatalf("write init: %v", err)
	}
}

// readBinaryFrame consumes frames until a binary one arrives.
func readBinaryFrame(t *testing.T, c *websocket.Conn) []byte {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("read binary frame: %v", err)
		}
		if mt == websocket.BinaryMessage {
			return data
		}
	}
}

// TestServeHTTP_FullShellSession drives the complete happy path: defaults
// applied (mode=shell, 24x80), ExecInteractive wired to /crew/shared, the
// container→ws and ws→container bridges both moving bytes, resize control
// frames reaching ExecResize, and clean teardown when the client hangs up.
func TestServeHTTP_FullShellSession(t *testing.T) {
	v := newTestValidator(t)
	db := seedTerminalDB(t)

	serverSide, testSide := net.Pipe()
	im := &covInteractive{covContainer: &covContainer{states: []string{"running"}}, conn: serverSide}
	h := New(im, v, db, silentLogger())

	conn, done := dialTerminalDone(t, h)
	authAndInit(t, conn, v, map[string]any{"crew_id": "c1", "crew_slug": "crew-a"})

	// container → ws: write on the PTY side, expect a binary ws frame.
	go func() {
		testSide.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, _ = testSide.Write([]byte("hello-from-pty"))
	}()
	if got := readBinaryFrame(t, conn); string(got) != "hello-from-pty" {
		t.Errorf("stdout bridge delivered %q, want hello-from-pty", got)
	}

	// Exec config captured with defaults applied.
	cfg := im.config()
	if cfg == nil {
		t.Fatal("ExecInteractive never called")
	}
	if cfg.WorkingDir != "/crew/shared" {
		t.Errorf("WorkingDir = %q, want /crew/shared", cfg.WorkingDir)
	}
	if len(cfg.Cmd) != 2 || cfg.Cmd[0] != "/bin/bash" || cfg.Cmd[1] != "--login" {
		t.Errorf("Cmd = %v, want [/bin/bash --login]", cfg.Cmd)
	}
	if cfg.Rows != 24 || cfg.Cols != 80 {
		t.Errorf("default size = %dx%d, want 24x80", cfg.Rows, cfg.Cols)
	}
	if cfg.User != "1001:1001" {
		t.Errorf("User = %q, want 1001:1001", cfg.User)
	}
	if cfg.ContainerID != "crewship-team-crew-a" {
		t.Errorf("ContainerID = %q (slug must come from DB, not client)", cfg.ContainerID)
	}

	// ws → container: resize control frame reaches ExecResize.
	resize, _ := json.Marshal(map[string]any{"type": "resize", "rows": 50, "cols": 120})
	if err := conn.WriteMessage(websocket.TextMessage, resize); err != nil {
		t.Fatalf("write resize: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(im.resizes()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("resize never reached ExecResize")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if r := im.resizes()[0]; r != [2]uint16{50, 120} {
		t.Errorf("resize = %v, want [50 120]", r)
	}

	// ws → container: binary stdin lands on the PTY.
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("typed")); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	buf := make([]byte, 5)
	testSide.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(testSide, buf); err != nil {
		t.Fatalf("read stdin from pty side: %v", err)
	}
	if string(buf) != "typed" {
		t.Errorf("stdin bridge delivered %q, want typed", buf)
	}

	// Client hangs up → session must terminate.
	conn.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeHTTP did not return after client close")
	}
}

// TestServeHTTP_AgentShellCreatesHomeDir pins the agent-scoped shell: the
// working dir is the agent's home and a best-effort mkdir -p exec runs
// before the shell starts.
func TestServeHTTP_AgentShellCreatesHomeDir(t *testing.T) {
	v := newTestValidator(t)
	db := seedTerminalDB(t)

	serverSide, _ := net.Pipe()
	im := &covInteractive{covContainer: &covContainer{states: []string{"running"}}, conn: serverSide}
	h := New(im, v, db, silentLogger())

	conn, done := dialTerminalDone(t, h)
	authAndInit(t, conn, v, map[string]any{
		"crew_id": "c1", "crew_slug": "crew-a", "agent_slug": "agent-1",
		"rows": 30, "cols": 100,
	})

	deadline := time.Now().Add(2 * time.Second)
	for im.config() == nil {
		if time.Now().After(deadline) {
			t.Fatal("ExecInteractive never called")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cfg := im.config()
	if cfg.WorkingDir != "/crew/agents/agent-1" {
		t.Errorf("WorkingDir = %q, want /crew/agents/agent-1", cfg.WorkingDir)
	}
	if cfg.Rows != 30 || cfg.Cols != 100 {
		t.Errorf("size = %dx%d, want 30x100 (client values must pass through)", cfg.Rows, cfg.Cols)
	}
	mkdir := im.lastExecCmd()
	if len(mkdir) != 3 || mkdir[0] != "mkdir" || mkdir[1] != "-p" || mkdir[2] != "/crew/agents/agent-1" {
		t.Errorf("mkdir exec = %v, want [mkdir -p /crew/agents/agent-1]", mkdir)
	}

	conn.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end")
	}
}

// TestServeHTTP_StartsStoppedContainer pins the auto-start path: a
// stopped container triggers EnsureCrewRuntime, the client sees the
// "Starting container..." info frame, and the session proceeds once the
// readiness poll observes "running".
func TestServeHTTP_StartsStoppedContainer(t *testing.T) {
	v := newTestValidator(t)
	db := seedTerminalDB(t)

	serverSide, _ := net.Pipe()
	im := &covInteractive{covContainer: &covContainer{states: []string{"stopped"}}, conn: serverSide}
	h := New(im, v, db, silentLogger())

	conn, done := dialTerminalDone(t, h)
	authAndInit(t, conn, v, map[string]any{"crew_id": "c1", "crew_slug": "crew-a"})

	got := readJSONFrame(t, conn)
	if got["type"] != "info" || !strings.Contains(got["message"], "Starting container") {
		t.Errorf("expected starting-container info frame, got %+v", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for im.config() == nil {
		if time.Now().After(deadline) {
			t.Fatal("session never started after container boot")
		}
		time.Sleep(5 * time.Millisecond)
	}
	im.mu.Lock()
	ensures := im.covContainer.ensureCalls
	im.mu.Unlock()
	if ensures != 1 {
		t.Errorf("EnsureCrewRuntime calls = %d, want 1", ensures)
	}

	conn.Close()
	<-done
}

// TestServeHTTP_ContainerStartFails pins the EnsureCrewRuntime error
// path: the client gets a descriptive error frame and no session starts.
func TestServeHTTP_ContainerStartFails(t *testing.T) {
	v := newTestValidator(t)
	db := seedTerminalDB(t)

	im := &covInteractive{covContainer: &covContainer{
		states:    []string{"stopped"},
		ensureErr: errors.New("no capacity"),
	}}
	h := New(im, v, db, silentLogger())

	conn, _ := dialTerminalDone(t, h)
	authAndInit(t, conn, v, map[string]any{"crew_id": "c1", "crew_slug": "crew-a"})

	// First frame: info. Second: error.
	info := readJSONFrame(t, conn)
	if info["type"] != "info" {
		t.Fatalf("expected info frame first, got %+v", info)
	}
	got := readJSONFrame(t, conn)
	if got["type"] != "error" || !strings.Contains(got["message"], "failed to start container") {
		t.Errorf("expected start-failure error, got %+v", got)
	}
	if im.config() != nil {
		t.Error("ExecInteractive must not run after failed container start")
	}
}

// TestServeHTTP_ProviderWithoutInteractiveExec pins the capability probe:
// a provider that can't do interactive exec yields a clear error frame.
func TestServeHTTP_ProviderWithoutInteractiveExec(t *testing.T) {
	v := newTestValidator(t)
	db := seedTerminalDB(t)
	h := New(&covContainer{states: []string{"running"}}, v, db, silentLogger())

	conn, _ := dialTerminalDone(t, h)
	authAndInit(t, conn, v, map[string]any{"crew_id": "c1", "crew_slug": "crew-a"})

	got := readJSONFrame(t, conn)
	if got["type"] != "error" || !strings.Contains(got["message"], "not supported") {
		t.Errorf("expected not-supported error, got %+v", got)
	}
}

// TestServeHTTP_ExecInteractiveFails pins the shell-start error frame.
func TestServeHTTP_ExecInteractiveFails(t *testing.T) {
	v := newTestValidator(t)
	db := seedTerminalDB(t)
	h := New(&interactiveMock{
		mockContainer: &mockContainer{state: "running"},
		execErr:       errors.New("pty allocation failed"),
	}, v, db, silentLogger())

	conn, _ := dialTerminalDone(t, h)
	authAndInit(t, conn, v, map[string]any{"crew_id": "c1", "crew_slug": "crew-a"})

	got := readJSONFrame(t, conn)
	if got["type"] != "error" || !strings.Contains(got["message"], "failed to start shell") {
		t.Errorf("expected shell-start error, got %+v", got)
	}
}

// TestServeHTTP_InvalidInitJSON pins the malformed-init error frame.
func TestServeHTTP_InvalidInitJSON(t *testing.T) {
	v := newTestValidator(t)
	h := New(&covContainer{}, v, nil, silentLogger())

	conn, _ := dialTerminalDone(t, h)
	tok, err := v.IssueWSTicket("u1", "sess", "", "")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	authMsg, _ := json.Marshal(map[string]string{"type": "auth", "token": tok})
	if err := conn.WriteMessage(websocket.TextMessage, authMsg); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte("{{not json")); err != nil {
		t.Fatal(err)
	}
	got := readJSONFrame(t, conn)
	if got["type"] != "error" || got["message"] != "invalid init message" {
		t.Errorf("expected invalid init error, got %+v", got)
	}
}

// TestServeHTTP_ClientClosesBeforeInit pins the init-read failure branch:
// the handler must return (not hang) when the socket dies between auth
// and init.
func TestServeHTTP_ClientClosesBeforeInit(t *testing.T) {
	v := newTestValidator(t)
	h := New(&covContainer{}, v, nil, silentLogger())

	conn, done := dialTerminalDone(t, h)
	tok, err := v.IssueWSTicket("u1", "sess", "", "")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	authMsg, _ := json.Marshal(map[string]string{"type": "auth", "token": tok})
	if err := conn.WriteMessage(websocket.TextMessage, authMsg); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler hung after client closed pre-init")
	}
}

// TestServeHTTP_ClientClosesBeforeAuth pins the auth-read failure branch.
func TestServeHTTP_ClientClosesBeforeAuth(t *testing.T) {
	v := newTestValidator(t)
	h := New(&covContainer{}, v, nil, silentLogger())

	conn, done := dialTerminalDone(t, h)
	conn.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler hung after client closed pre-auth")
	}
}

// TestServeHTTP_AttachMode covers the attach-mode arms: missing
// agent_slug, tmux-session check failure, inspect failure, dead session,
// and the happy path issuing `tmux attach`.
func TestServeHTTP_AttachMode(t *testing.T) {
	v := newTestValidator(t)

	run := func(t *testing.T, im *covInteractive, init map[string]any) (map[string]string, *covInteractive, *websocket.Conn) {
		t.Helper()
		db := seedTerminalDB(t)
		h := New(im, v, db, silentLogger())
		conn, _ := dialTerminalDone(t, h)
		authAndInit(t, conn, v, init)
		return readJSONFrame(t, conn), im, conn
	}

	t.Run("agent_slug required", func(t *testing.T) {
		im := &covInteractive{covContainer: &covContainer{states: []string{"running"}}}
		got, _, _ := run(t, im, map[string]any{"mode": "attach", "crew_id": "c1", "crew_slug": "crew-a"})
		if got["type"] != "error" || !strings.Contains(got["message"], "agent_slug is required") {
			t.Errorf("expected agent_slug error, got %+v", got)
		}
	})

	t.Run("tmux check exec fails", func(t *testing.T) {
		im := &covInteractive{covContainer: &covContainer{
			states:  []string{"running"},
			execErr: errors.New("exec broken"),
		}}
		got, _, _ := run(t, im, map[string]any{"mode": "attach", "crew_id": "c1", "crew_slug": "crew-a", "agent_slug": "agent-1"})
		if got["type"] != "error" || got["message"] != "agent is not running" {
			t.Errorf("expected agent-not-running error, got %+v", got)
		}
	})

	t.Run("inspect fails", func(t *testing.T) {
		im := &covInteractive{covContainer: &covContainer{
			states:     []string{"running"},
			inspectErr: errors.New("inspect down"),
		}}
		got, _, _ := run(t, im, map[string]any{"mode": "attach", "crew_id": "c1", "crew_slug": "crew-a", "agent_slug": "agent-1"})
		if got["type"] != "error" || !strings.Contains(got["message"], "failed to check agent session") {
			t.Errorf("expected inspect-failure error, got %+v", got)
		}
	})

	t.Run("no tmux session", func(t *testing.T) {
		im := &covInteractive{covContainer: &covContainer{
			states:         []string{"running"},
			inspectRunning: false,
			inspectExit:    1,
		}}
		got, _, _ := run(t, im, map[string]any{"mode": "attach", "crew_id": "c1", "crew_slug": "crew-a", "agent_slug": "agent-1"})
		if got["type"] != "error" || !strings.Contains(got["message"], "no active tmux session") {
			t.Errorf("expected dead-session error, got %+v", got)
		}
	})

	t.Run("attach happy path", func(t *testing.T) {
		serverSide, _ := net.Pipe()
		im := &covInteractive{
			covContainer: &covContainer{states: []string{"running"}, inspectRunning: false, inspectExit: 0},
			conn:         serverSide,
		}
		db := seedTerminalDB(t)
		h := New(im, v, db, silentLogger())
		conn, done := dialTerminalDone(t, h)
		authAndInit(t, conn, v, map[string]any{
			"mode": "attach", "crew_id": "c1", "crew_slug": "crew-a", "agent_slug": "agent-1",
		})

		deadline := time.Now().Add(2 * time.Second)
		for im.config() == nil {
			if time.Now().After(deadline) {
				t.Fatal("attach session never started")
			}
			time.Sleep(5 * time.Millisecond)
		}
		cfg := im.config()
		want := []string{"tmux", "attach", "-t", "agent-agent-1"}
		if len(cfg.Cmd) != len(want) || cfg.Cmd[0] != "tmux" || cfg.Cmd[3] != "agent-agent-1" {
			t.Errorf("attach Cmd = %v, want %v", cfg.Cmd, want)
		}
		if cfg.WorkingDir != "" {
			t.Errorf("attach WorkingDir = %q, want empty", cfg.WorkingDir)
		}
		// The tmux has-session probe ran first.
		probe := im.lastExecCmd()
		if len(probe) != 4 || probe[0] != "tmux" || probe[1] != "has-session" || probe[3] != "agent-agent-1" {
			t.Errorf("probe cmd = %v, want tmux has-session -t agent-agent-1", probe)
		}

		conn.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("attach session did not end")
		}
	})
}

// TestServeHTTP_SlugSpoofRejected pins the DB-resolved-slug defence: the
// client-supplied crew_slug is ignored in favor of the DB row, so the
// container name targets the real crew even when the init lies.
func TestServeHTTP_SlugSpoofRejected(t *testing.T) {
	v := newTestValidator(t)
	db := seedTerminalDB(t)

	serverSide, _ := net.Pipe()
	im := &covInteractive{covContainer: &covContainer{states: []string{"running"}}, conn: serverSide}
	h := New(im, v, db, silentLogger())

	conn, done := dialTerminalDone(t, h)
	// Spoofed slug "other-crew" — DB says crew c1 is "crew-a".
	authAndInit(t, conn, v, map[string]any{"crew_id": "c1", "crew_slug": "other-crew"})

	deadline := time.Now().Add(2 * time.Second)
	for im.config() == nil {
		if time.Now().After(deadline) {
			t.Fatal("session never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := im.config().ContainerID; got != "crewship-team-crew-a" {
		t.Errorf("container = %q — spoofed slug must not be honored", got)
	}

	conn.Close()
	<-done
}
