package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// proxy_files.go — CrewFileSave + AgentContainerFiles.
//
// The 5 sibling Agent*/Crew* file handlers are partially covered by
// internal_handlers_test.go via the "no such socket" pattern; these
// two zero-coverage handlers get the same RBAC + lookup + IPC-dispatch
// exercise, plus the happy-path body-unwrap that AgentContainerFiles
// implements.
// ---------------------------------------------------------------------------

func newProxyHandlerWithCrewWorkspace(t *testing.T, socketPath string) (*ProxyHandler, string, string, string) {
	t.Helper()
	h := NewProxyHandler(setupTestDB(t), newTestLogger(), socketPath)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedCrewRow(t, h.db, "crew-pf", wsID, "PF", "pf")
	return h, userID, wsID, "crew-pf"
}

// ---- CrewFileSave ----

func TestCrewFileSave_VIEWER_Forbidden(t *testing.T) {
	h, userID, wsID, crewID := newProxyHandlerWithCrewWorkspace(t, "/tmp/no-such-socket")
	req := httptest.NewRequest("PUT", "/x?path=foo.txt", strings.NewReader("body"))
	req.SetPathValue("crewId", crewID)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.CrewFileSave(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("VIEWER status = %d, want 403", rr.Code)
	}
}

func TestCrewFileSave_MissingPathParam_400(t *testing.T) {
	h, userID, wsID, crewID := newProxyHandlerWithCrewWorkspace(t, "/tmp/no-such-socket")
	req := httptest.NewRequest("PUT", "/x", strings.NewReader("body")) // no ?path=
	req.SetPathValue("crewId", crewID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CrewFileSave(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing path)", rr.Code)
	}
}

func TestCrewFileSave_UnknownCrew_404(t *testing.T) {
	h, userID, wsID, _ := newProxyHandlerWithCrewWorkspace(t, "/tmp/no-such-socket")
	req := httptest.NewRequest("PUT", "/x?path=foo.txt", strings.NewReader("body"))
	req.SetPathValue("crewId", "missing-crew")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CrewFileSave(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCrewFileSave_CrossWorkspaceCrew_404(t *testing.T) {
	// Pin the no-cross-workspace-leak contract — crewExists scopes by
	// workspace_id, so a crew in a different workspace must 404.
	h, userID, wsA, _ := newProxyHandlerWithCrewWorkspace(t, "/tmp/no-such-socket")
	wsB := "ws-pf-foreign"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f-pf')`, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	seedCrewRow(t, h.db, "crew-foreign-pf", wsB, "F", "f-pf")

	req := httptest.NewRequest("PUT", "/x?path=foo.txt", strings.NewReader("body"))
	req.SetPathValue("crewId", "crew-foreign-pf")
	req = withWorkspaceUser(req, userID, wsA, "OWNER")
	rr := httptest.NewRecorder()
	h.CrewFileSave(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace status = %d, want 404", rr.Code)
	}
}

func TestCrewFileSave_PathTraversal_400(t *testing.T) {
	// normalizeRequestPath rejects "../" — the gate that keeps the
	// IPC path-param from escaping the crew's file root. Pin every
	// classic traversal shape.
	h, userID, wsID, crewID := newProxyHandlerWithCrewWorkspace(t, "/tmp/no-such-socket")

	for _, p := range []string{"../escape.txt", "subdir/../../etc/passwd", "/absolute/path"} {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/x?path="+p, strings.NewReader("body"))
			req.SetPathValue("crewId", crewID)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.CrewFileSave(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("path=%q: status = %d, want 400", p, rr.Code)
			}
		})
	}
}

func TestCrewFileSave_IPCUnreachable_502(t *testing.T) {
	h, userID, wsID, crewID := newProxyHandlerWithCrewWorkspace(t, "/tmp/no-such-socket-pf")
	req := httptest.NewRequest("PUT", "/x?path=foo.txt", strings.NewReader("body"))
	req.SetPathValue("crewId", crewID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CrewFileSave(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rr.Code)
	}
}

func TestCrewFileSave_HappyPath_ForwardsBodyAndPathToIPC(t *testing.T) {
	var gotPath, gotBody string
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.RawQuery != "" {
			gotPath += "?" + r.URL.RawQuery
		}
		// io.ReadAll: a single r.Body.Read can return partial data, so
		// the assertion would be flaky for bodies spanning multiple
		// reads (HTTP/1.1 chunked, slow client, etc.).
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read ipc request body: %v", err)
			return
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"saved":true}`))
	}))
	h, userID, wsID, crewID := newProxyHandlerWithCrewWorkspace(t, sock)

	req := httptest.NewRequest("PUT", "/x?path=foo.txt", strings.NewReader("hello bytes"))
	req.SetPathValue("crewId", crewID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CrewFileSave(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	wantPath := "/crews/" + crewID + "/files/save?path=foo.txt"
	if gotPath != wantPath {
		t.Errorf("IPC path = %q, want %q", gotPath, wantPath)
	}
	if gotBody != "hello bytes" {
		t.Errorf("IPC body = %q, want \"hello bytes\"", gotBody)
	}
}

// ---- AgentContainerFiles ----

func TestAgentContainerFiles_UnknownAgent_404(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/no-such-socket")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("agentId", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentContainerFiles(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestAgentContainerFiles_AgentWithoutCrew_404(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/no-such-socket")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedAgentForProxy(t, h, "ag-no-crew", wsID, "lone", "")

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("agentId", "ag-no-crew")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentContainerFiles(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (agent w/o crew_id)", rr.Code)
	}
}

func TestAgentContainerFiles_InvalidSubdir_400(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/no-such-socket")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedAgentForProxy(t, h, "ag-sub", wsID, "ag-sub", "crew-sub")

	req := httptest.NewRequest("GET", "/x?subdir=../../etc", nil)
	req.SetPathValue("agentId", "ag-sub")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentContainerFiles(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (subdir traversal)", rr.Code)
	}
}

func TestAgentContainerFiles_IPCUnreachable_502(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/no-such-socket-ipc-fail")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedAgentForProxy(t, h, "ag-ipc", wsID, "ag-ipc", "crew-ipc")

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("agentId", "ag-ipc")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentContainerFiles(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rr.Code)
	}
}

func TestAgentContainerFiles_HappyPath_UnwrapsFilesArray(t *testing.T) {
	var gotPath string
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.RawQuery != "" {
			gotPath += "?" + r.URL.RawQuery
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[{"name":"a.txt"},{"name":"b.txt"}],"unrelated":"x"}`))
	}))
	h := newProxyHandlerForTest(t, sock)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedAgentForProxy(t, h, "ag-happy", wsID, "ag-happy", "crew-happy")

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("agentId", "ag-happy")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentContainerFiles(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if gotPath != "/crews/crew-happy/container-files" {
		t.Errorf("IPC path = %q, want /crews/crew-happy/container-files", gotPath)
	}
	// Handler unwraps the "files" array; the wrapper object is dropped.
	var got []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if len(got) != 2 || got[0]["name"] != "a.txt" || got[1]["name"] != "b.txt" {
		t.Errorf("got %+v, want unwrapped files array", got)
	}
}

func TestAgentContainerFiles_HappyPath_ForwardsSubdirQueryParam(t *testing.T) {
	var gotPath string
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		_, _ = w.Write([]byte(`{"files":[]}`))
	}))
	h := newProxyHandlerForTest(t, sock)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedAgentForProxy(t, h, "ag-sd", wsID, "ag-sd", "crew-sd")

	req := httptest.NewRequest("GET", "/x?subdir=src/api", nil)
	req.SetPathValue("agentId", "ag-sd")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentContainerFiles(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	wantPath := "/crews/crew-sd/container-files?subdir=src%2Fapi"
	if gotPath != wantPath {
		t.Errorf("IPC path = %q, want %q (subdir URL-escaped + forwarded)", gotPath, wantPath)
	}
}

func TestAgentContainerFiles_MalformedUpstream_ReturnsEmptyArray(t *testing.T) {
	// Upstream returns non-JSON or no "files" key — handler falls back
	// to "[]" rather than 500. Mirrors the same fallback in AgentGitLog
	// that other tests pinned.
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not-json-at-all`))
	}))
	h := newProxyHandlerForTest(t, sock)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedAgentForProxy(t, h, "ag-mal", wsID, "ag-mal", "crew-mal")

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("agentId", "ag-mal")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentContainerFiles(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Errorf("body = %q, want \"[]\"", rr.Body.String())
	}
}

func TestAgentContainerFiles_CrossWorkspace_404(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/no-such-socket")
	userID := seedTestUser(t, h.db)
	wsA := seedTestWorkspace(t, h.db, userID)

	wsB := "ws-acf-foreign"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f-acf')`, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	seedAgentForProxy(t, h, "ag-foreign-acf", wsB, "f-acf", "crew-acf-foreign")

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("agentId", "ag-foreign-acf")
	req = withWorkspaceUser(req, userID, wsA, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentContainerFiles(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace status = %d, want 404", rr.Code)
	}
}
