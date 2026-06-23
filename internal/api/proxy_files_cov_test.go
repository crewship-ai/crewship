package api

// Coverage for proxy_files.go — the Agent*/Crew* file handlers' happy
// paths and path-scoping rules, driven through a real Unix-socket IPC
// server (newUnixIPCServer from proxy_test.go). The pre-existing
// proxy_files_test.go covers the RBAC gates and unreachable-socket
// dispatch; this file covers the body-unwrap, download streaming, and
// the relative/full/cross-agent path resolution matrix.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// covPFRig: IPC server with a programmable mux + seeded agent/crew rows.
type covPFCall struct {
	method, path, rawQuery, body string
}

func covPFRigServer(t *testing.T) (sock string, calls *[]covPFCall, respond func(http.ResponseWriter, *http.Request)) {
	t.Helper()
	var recorded []covPFCall
	var handler func(http.ResponseWriter, *http.Request)
	sock = newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		recorded = append(recorded, covPFCall{r.Method, r.URL.Path, r.URL.RawQuery, string(b)})
		if handler != nil {
			handler(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[{"name":"a.txt"}]}`))
	}))
	return sock, &recorded, func(w http.ResponseWriter, r *http.Request) {}
}

func covPFRig(t *testing.T) (h *ProxyHandler, userID, wsID, crewID, agentID string, calls *[]covPFCall) {
	t.Helper()
	sock, calls, _ := covPFRigServer(t)
	h = NewProxyHandler(setupTestDB(t), newTestLogger(), sock)
	userID = seedTestUser(t, h.db)
	wsID = seedTestWorkspace(t, h.db, userID)
	crewID = seedCrewRow(t, h.db, "crew-pf2", wsID, "PF2", "pf2")
	agentID = seedAgentRow(t, h.db, "agent-pf2", wsID, crewID, "PF Agent", "pf-agent", "AGENT")
	return
}

// ---- AgentFiles ----

func TestAgentFiles_HappyPathWithSubdirAndRecursive(t *testing.T) {
	h, userID, wsID, crewID, agentID, calls := covPFRig(t)
	req := httptest.NewRequest("GET", "/x?recursive=true&subdir=workspace/demo", nil)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentFiles(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var files []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&files); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(files) != 1 || files[0]["name"] != "a.txt" {
		t.Errorf("files = %v", files)
	}
	if len(*calls) != 1 {
		t.Fatalf("ipc calls = %d, want 1", len(*calls))
	}
	c := (*calls)[0]
	if c.path != "/crews/"+crewID+"/files" {
		t.Errorf("ipc path = %q", c.path)
	}
	for _, want := range []string{"agent_slug=pf-agent", "recursive=true", "subdir=workspace%2Fdemo"} {
		if !strings.Contains(c.rawQuery, want) {
			t.Errorf("query %q missing %q", c.rawQuery, want)
		}
	}
}

func TestAgentFiles_InvalidSubdir400(t *testing.T) {
	h, userID, wsID, _, agentID, _ := covPFRig(t)
	req := httptest.NewRequest("GET", "/x?subdir=../../etc", nil)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentFiles(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestAgentFiles_NoCrewReturnsEmpty(t *testing.T) {
	h, userID, wsID, _, _, _ := covPFRig(t)
	crewless := seedAgentRow(t, h.db, "agent-pf-nc", wsID, "", "NC", "pf-nc", "COORDINATOR")
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("agentId", crewless)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentFiles(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "[]\n" {
		t.Errorf("status=%d body=%q, want 200 []", rr.Code, rr.Body.String())
	}
}

// ---- AgentFileDownload ----

func TestAgentFileDownload_PathResolutionMatrix(t *testing.T) {
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="x.toml"`)
		_, _ = w.Write([]byte("file-bytes:" + r.URL.Query().Get("path")))
	}))
	h := NewProxyHandler(setupTestDB(t), newTestLogger(), sock)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-dl", wsID, "DL", "dl")
	agentID := seedAgentRow(t, h.db, "agent-dl", wsID, crewID, "DL Agent", "dl-agent", "AGENT")

	run := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/x?path="+path, nil)
		req.SetPathValue("agentId", agentID)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.AgentFileDownload(rr, req)
		return rr
	}

	t.Run("relative path gets prefixed", func(t *testing.T) {
		rr := run("workspace/x.toml")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		want := "file-bytes:" + crewID + "/dl-agent/workspace/x.toml"
		if rr.Body.String() != want {
			t.Errorf("body = %q, want %q", rr.Body.String(), want)
		}
		if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "x.toml") {
			t.Errorf("Content-Disposition = %q", cd)
		}
		if ct := rr.Header().Get("Content-Type"); ct != "application/octet-stream" {
			t.Errorf("Content-Type = %q", ct)
		}
	})

	t.Run("full storage path accepted as-is", func(t *testing.T) {
		rr := run(crewID + "/dl-agent/workspace/y.toml")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		want := "file-bytes:" + crewID + "/dl-agent/workspace/y.toml"
		if rr.Body.String() != want {
			t.Errorf("body = %q, want %q", rr.Body.String(), want)
		}
	})

	t.Run("sibling agent path rejected 403", func(t *testing.T) {
		rr := run(crewID + "/other-agent/secrets.txt")
		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})

	t.Run("missing path 400", func(t *testing.T) {
		rr := run("")
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})

	t.Run("traversal path 400", func(t *testing.T) {
		rr := run("..%2F..%2Fetc%2Fpasswd")
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})

	t.Run("unknown agent 404", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/x?path=f", nil)
		req.SetPathValue("agentId", "ghost")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.AgentFileDownload(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})
}

func TestAgentFileDownload_SidecarNon200Maps404(t *testing.T) {
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	h := NewProxyHandler(setupTestDB(t), newTestLogger(), sock)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-dl2", wsID, "DL2", "dl2")
	agentID := seedAgentRow(t, h.db, "agent-dl2", wsID, crewID, "A", "dl2-agent", "AGENT")

	req := httptest.NewRequest("GET", "/x?path=missing.txt", nil)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentFileDownload(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// ---- AgentFileSave ----

func TestAgentFileSave_Matrix(t *testing.T) {
	var gotPath, gotBody string
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotPath = r.URL.Query().Get("path")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"saved":true}`))
	}))
	h := NewProxyHandler(setupTestDB(t), newTestLogger(), sock)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-sv", wsID, "SV", "sv")
	agentID := seedAgentRow(t, h.db, "agent-sv", wsID, crewID, "A", "sv-agent", "AGENT")

	run := func(path, body, role, agent string) *httptest.ResponseRecorder {
		url := "/x"
		if path != "" {
			url += "?path=" + path
		}
		req := httptest.NewRequest("PUT", url, strings.NewReader(body))
		req.SetPathValue("agentId", agent)
		req = withWorkspaceUser(req, userID, wsID, role)
		rr := httptest.NewRecorder()
		h.AgentFileSave(rr, req)
		return rr
	}

	t.Run("happy path proxies body and prefixes path", func(t *testing.T) {
		rr := run("notes/todo.md", "file content", "OWNER", agentID)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
		}
		if gotBody != "file content" {
			t.Errorf("sidecar body = %q", gotBody)
		}
		if want := crewID + "/sv-agent/notes/todo.md"; gotPath != want {
			t.Errorf("sidecar path = %q, want %q", gotPath, want)
		}
		if !strings.Contains(rr.Body.String(), `"saved":true`) {
			t.Errorf("response = %q", rr.Body.String())
		}
	})
	t.Run("cross-agent write 403", func(t *testing.T) {
		rr := run(crewID+"/other-agent/x.txt", "x", "OWNER", agentID)
		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})
	t.Run("missing path 400", func(t *testing.T) {
		rr := run("", "x", "OWNER", agentID)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("unknown agent 404", func(t *testing.T) {
		rr := run("a.txt", "x", "OWNER", "ghost")
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})
	t.Run("traversal 400", func(t *testing.T) {
		rr := run("..%2Fescape.txt", "x", "OWNER", agentID)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
}

// ---- CrewFiles ----

func TestCrewFiles_Matrix(t *testing.T) {
	h, userID, wsID, crewID, _, calls := covPFRig(t)

	run := func(query, crew string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/x"+query, nil)
		req.SetPathValue("crewId", crew)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.CrewFiles(rr, req)
		return rr
	}

	t.Run("happy path with all query params", func(t *testing.T) {
		rr := run("?agent_slug=pf-agent&recursive=true&subdir=shared", crewID)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
		}
		var files []map[string]any
		if err := json.NewDecoder(rr.Body).Decode(&files); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(files) != 1 {
			t.Errorf("files = %v", files)
		}
		c := (*calls)[len(*calls)-1]
		for _, want := range []string{"agent_slug=pf-agent", "recursive=true", "subdir=shared"} {
			if !strings.Contains(c.rawQuery, want) {
				t.Errorf("query %q missing %q", c.rawQuery, want)
			}
		}
	})
	t.Run("unknown crew 404", func(t *testing.T) {
		rr := run("", "ghost-crew")
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})
	t.Run("invalid subdir 400", func(t *testing.T) {
		rr := run("?subdir=../up", crewID)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
}

// ---- CrewFileDownload ----

func TestCrewFileDownload_Matrix(t *testing.T) {
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("path"), "missing") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", "9")
		_, _ = w.Write([]byte("crew-data"))
	}))
	h := NewProxyHandler(setupTestDB(t), newTestLogger(), sock)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-cfd", wsID, "CFD", "cfd")

	run := func(query, crew string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/x"+query, nil)
		req.SetPathValue("crewId", crew)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.CrewFileDownload(rr, req)
		return rr
	}

	t.Run("happy path streams bytes", func(t *testing.T) {
		rr := run("?path=shared/data.csv", crewID)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		if rr.Body.String() != "crew-data" {
			t.Errorf("body = %q", rr.Body.String())
		}
		if cl := rr.Header().Get("Content-Length"); cl != "9" {
			t.Errorf("Content-Length = %q", cl)
		}
	})
	t.Run("sidecar 404 maps to 404", func(t *testing.T) {
		rr := run("?path=missing.csv", crewID)
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})
	t.Run("missing path 400", func(t *testing.T) {
		rr := run("", crewID)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("unknown crew 404", func(t *testing.T) {
		rr := run("?path=x", "ghost")
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})
	t.Run("traversal 400", func(t *testing.T) {
		rr := run("?path=..%2Fpasswd", crewID)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
}
