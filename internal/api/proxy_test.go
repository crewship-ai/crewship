package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// proxy.go — IPC helpers (ipcGet/ipcPost/ipcPut/proxyJSON) and AgentGitLog.
//
// Existing internal_handlers_test.go exercises the role/lookup gates on the
// other proxy endpoints via a non-existent socket path, which is enough to
// pin "fail safely when crewshipd is unreachable" but never exercises the
// IPC verbs themselves. This file stands up a real Unix-socket-backed HTTP
// server so the GET/POST/PUT helpers actually round-trip, and covers the
// AgentGitLog response-shape branches that depend on that round-trip.
// ---------------------------------------------------------------------------

// newUnixIPCServer starts an httptest server bound to a Unix socket under
// a short path. Darwin's sockaddr_un.sun_path is 104 bytes — t.TempDir()
// under /var/folders blows past that, so we hand-pick a short /tmp path.
// The handler echoes the method/path/body back so tests can assert what
// the proxy sent. Cleaned up on test exit.
func newUnixIPCServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	sock := filepath.Join(os.TempDir(), "cs-"+hex.EncodeToString(nonce[:])+".sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sock, err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
		_ = ln.Close()
		_ = os.Remove(sock)
	})
	return sock
}

func newProxyHandlerForTest(t *testing.T, socketPath string) *ProxyHandler {
	t.Helper()
	return NewProxyHandler(setupTestDB(t), newTestLogger(), socketPath)
}

// ---- ipcGet / ipcPost / ipcPut ----

func TestProxyIPC_GetPostPut_RoundTrip(t *testing.T) {
	type call struct {
		method, path, body, contentType string
	}
	got := make(chan call, 3)
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- call{
			method:      r.Method,
			path:        r.URL.Path,
			body:        string(body),
			contentType: r.Header.Get("Content-Type"),
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	h := newProxyHandlerForTest(t, sock)
	ctx := context.Background()

	// GET — no body, no content-type expected
	resp, err := h.ipcGet(ctx, "/health")
	if err != nil {
		t.Fatalf("ipcGet: %v", err)
	}
	_ = resp.Body.Close()
	c := <-got
	if c.method != "GET" || c.path != "/health" || c.body != "" {
		t.Errorf("ipcGet sent %+v, want GET /health (no body)", c)
	}
	if c.contentType != "" {
		t.Errorf("ipcGet set Content-Type=%q on GET; should be empty", c.contentType)
	}

	// POST — sets Content-Type: application/json, forwards body
	resp, err = h.ipcPost(ctx, "/issues", strings.NewReader(`{"title":"x"}`))
	if err != nil {
		t.Fatalf("ipcPost: %v", err)
	}
	_ = resp.Body.Close()
	c = <-got
	if c.method != "POST" || c.path != "/issues" {
		t.Errorf("ipcPost sent %s %s, want POST /issues", c.method, c.path)
	}
	if c.body != `{"title":"x"}` {
		t.Errorf("ipcPost body = %q, want forwarded payload", c.body)
	}
	if c.contentType != "application/json" {
		t.Errorf("ipcPost Content-Type = %q, want application/json", c.contentType)
	}

	// PUT — forwards body but does NOT set Content-Type (source contract).
	resp, err = h.ipcPut(ctx, "/issues/1", strings.NewReader(`{"state":"closed"}`))
	if err != nil {
		t.Fatalf("ipcPut: %v", err)
	}
	_ = resp.Body.Close()
	c = <-got
	if c.method != "PUT" || c.path != "/issues/1" || c.body != `{"state":"closed"}` {
		t.Errorf("ipcPut sent %+v, want PUT /issues/1 with body", c)
	}
	if c.contentType != "" {
		t.Errorf("ipcPut set Content-Type=%q; current source intentionally does not set it", c.contentType)
	}
}

func TestProxyIPC_UnreachableSocket_ReturnsError(t *testing.T) {
	// Pointing at a nonexistent path must surface as an error from the
	// IPC helper rather than masquerading as a 5xx response.
	h := newProxyHandlerForTest(t, "/tmp/definitely-not-a-socket-xyz-overnight-test")
	for _, fn := range []func() (*http.Response, error){
		func() (*http.Response, error) { return h.ipcGet(context.Background(), "/x") },
		func() (*http.Response, error) {
			return h.ipcPost(context.Background(), "/x", strings.NewReader("{}"))
		},
		func() (*http.Response, error) {
			return h.ipcPut(context.Background(), "/x", strings.NewReader("{}"))
		},
	} {
		if _, err := fn(); err == nil {
			t.Fatal("expected error on unreachable socket")
		}
	}
}

// ---- proxyJSON ----

func TestProxyJSON_CopiesStatusBodyAndForcesJSONContentType(t *testing.T) {
	// proxyJSON forwards the upstream status verbatim and overwrites the
	// downstream Content-Type to application/json — the client always
	// receives JSON regardless of what the sidecar sent.
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately use a non-2xx + non-JSON Content-Type to verify
		// proxyJSON forwards the status as-is and overrides Content-Type.
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"err":"i am a teapot"}`))
	}))
	h := newProxyHandlerForTest(t, sock)
	resp, err := h.ipcGet(context.Background(), "/x")
	if err != nil {
		t.Fatalf("ipcGet: %v", err)
	}
	rr := httptest.NewRecorder()
	h.proxyJSON(rr, resp)

	if rr.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418 (verbatim upstream forward)", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (proxyJSON overrides upstream)", got)
	}
	if rr.Body.String() != `{"err":"i am a teapot"}` {
		t.Errorf("body = %q, want forwarded payload", rr.Body.String())
	}
}

// ---- AgentGitLog ----

// seedAgent inserts an agent row optionally tied to a crew. crewID == ""
// inserts the agent with crew_id NULL so the "not assigned" branch fires.
func seedAgentForProxy(t *testing.T, h *ProxyHandler, agentID, wsID, slug, crewID string) {
	t.Helper()
	var crew interface{}
	if crewID != "" {
		seedCrewRow(t, h.db, crewID, wsID, "C-"+crewID, "c-"+crewID)
		crew = crewID
	}
	_, err := h.db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status,
		cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		VALUES (?, ?, ?, ?, ?, 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`,
		agentID, wsID, crew, "N-"+agentID, slug)
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
}

func TestAgentGitLog_NotFound_UnknownAgent(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/no-such-socket")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	req := httptest.NewRequest("GET", "/api/v1/agents/missing/git-log", nil)
	req.SetPathValue("agentId", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentGitLog(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestAgentGitLog_NotFound_AgentNotAssignedToCrew(t *testing.T) {
	// An agent row exists in the workspace but crew_id is NULL — handler
	// must 404 (the git-log endpoint is crew-scoped).
	h := newProxyHandlerForTest(t, "/tmp/no-such-socket")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedAgentForProxy(t, h, "lone-agent", wsID, "lone", "")

	req := httptest.NewRequest("GET", "/api/v1/agents/lone-agent/git-log", nil)
	req.SetPathValue("agentId", "lone-agent")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentGitLog(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("agent-without-crew status = %d, want 404", rr.Code)
	}
}

func TestAgentGitLog_CrossWorkspace_NotFound(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/no-such-socket")
	userID := seedTestUser(t, h.db)
	wsA := seedTestWorkspace(t, h.db, userID)

	wsB := "ws-git-foreign"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f-git')`, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	seedAgentForProxy(t, h, "agent-foreign", wsB, "foreign", "crew-foreign")

	req := httptest.NewRequest("GET", "/api/v1/agents/agent-foreign/git-log", nil)
	req.SetPathValue("agentId", "agent-foreign")
	req = withWorkspaceUser(req, userID, wsA, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentGitLog(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace status = %d, want 404", rr.Code)
	}
}

func TestAgentGitLog_UnreachableIPC_502(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/no-such-socket-overnight-test")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedAgentForProxy(t, h, "ag", wsID, "ag-slug", "crew-1")

	req := httptest.NewRequest("GET", "/api/v1/agents/ag/git-log", nil)
	req.SetPathValue("agentId", "ag")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentGitLog(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("unreachable IPC status = %d, want 502", rr.Code)
	}
}

func TestAgentGitLog_HappyPath_UnwrapsCommits(t *testing.T) {
	var gotPath string
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"commits":[{"sha":"abc","subject":"first"}],"unrelated":"x"}`))
	}))
	h := newProxyHandlerForTest(t, sock)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedAgentForProxy(t, h, "ag-happy", wsID, "ag-slug", "crew-happy")

	req := httptest.NewRequest("GET", "/api/v1/agents/ag-happy/git-log", nil)
	req.SetPathValue("agentId", "ag-happy")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentGitLog(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	wantPath := "/crews/crew-happy/git-log?agent_slug=ag-slug"
	if gotPath != wantPath {
		t.Errorf("IPC path = %q, want %q", gotPath, wantPath)
	}
	// Handler unwraps the "commits" array; the wrapper object is dropped.
	var got []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if len(got) != 1 || got[0]["sha"] != "abc" {
		t.Errorf("commits = %+v, want [{sha:abc, subject:first}]", got)
	}
}

func TestAgentGitLog_MalformedUpstream_ReturnsEmptyArray(t *testing.T) {
	// Upstream returns 200 but body has no "commits" key (or it's not
	// JSON at all). Handler must fall back to "[]" rather than 500 —
	// the UI renders an empty list, the operator notices in the logs.
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-json-at-all`))
	}))
	h := newProxyHandlerForTest(t, sock)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedAgentForProxy(t, h, "ag-mal", wsID, "ag-mal", "crew-mal")

	req := httptest.NewRequest("GET", "/api/v1/agents/ag-mal/git-log", nil)
	req.SetPathValue("agentId", "ag-mal")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentGitLog(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("malformed-upstream status = %d, want 200 with empty array", rr.Code)
	}
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Errorf("body = %q, want \"[]\"", rr.Body.String())
	}
}
