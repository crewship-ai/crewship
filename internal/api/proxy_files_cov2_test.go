package api

// Second coverage pass for proxy_files.go: the unknown-agent 404s, the
// sidecar-unreachable 502/404 branches, the non-JSON IPC fallback, the
// full-storage-path acceptance in AgentFileSave, the role gates on the
// crew-file trio + AgentContainerFiles, and the crewExists DB-error 500s.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func covPF2Req(method, target string) *http.Request {
	return httptest.NewRequest(method, target, strings.NewReader("body"))
}

func TestPF2_AgentFiles_UnknownAgent404(t *testing.T) {
	h, userID, wsID, _, _ := covProxyRig(t, "/tmp/pf2-no-socket-1")
	req := covPF2Req("GET", "/x")
	req.SetPathValue("agentId", "ghost")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentFiles(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPF2_AgentFiles_SidecarUnreachable502(t *testing.T) {
	h, userID, wsID, _, agentID := covProxyRig(t, "/tmp/pf2-no-socket-2")
	req := covPF2Req("GET", "/x")
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentFiles(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPF2_AgentFiles_NonJSONResponse_EmptyList(t *testing.T) {
	sock := covIPCJSON(t, map[string]any{}) // every path 404s with non-JSON body
	h, userID, wsID, _, agentID := covProxyRig(t, sock)
	req := covPF2Req("GET", "/x")
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentFiles(rr, req)
	if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatalf("status = %d body=%q, want 200 []", rr.Code, rr.Body.String())
	}
}

func TestPF2_AgentFileDownload_SidecarUnreachable404(t *testing.T) {
	h, userID, wsID, _, agentID := covProxyRig(t, "/tmp/pf2-no-socket-3")
	req := covPF2Req("GET", "/x?path=workspace/a.txt")
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentFileDownload(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPF2_AgentFileSave_FullStoragePathAccepted(t *testing.T) {
	var gotPath string
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Query().Get("path")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"saved":true}`))
	}))
	h, userID, wsID, crewID, agentID := covProxyRig(t, sock)

	// Full storage path already prefixed with <crewID>/<slug>/.
	full := crewID + "/px-agent/workspace/x.toml"
	req := covPF2Req("PUT", "/x?path="+full)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentFileSave(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"saved":true`) {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if gotPath != full {
		t.Errorf("forwarded path = %q, want %q (no double prefix)", gotPath, full)
	}
}

func TestPF2_AgentFileSave_SidecarUnreachable502(t *testing.T) {
	h, userID, wsID, _, agentID := covProxyRig(t, "/tmp/pf2-no-socket-4")
	req := covPF2Req("PUT", "/x?path=workspace/x.toml")
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentFileSave(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rr.Code, rr.Body.String())
	}
}

// ---- Crew file trio ----

func TestPF2_CrewFiles_RoleGateAndDBError(t *testing.T) {
	t.Run("empty role 403", func(t *testing.T) {
		h, userID, wsID, crewID, _ := covProxyRig(t, "/tmp/pf2-no-socket-5")
		req := covPF2Req("GET", "/x")
		req.SetPathValue("crewId", crewID)
		req = withWorkspaceUser(req, userID, wsID, "")
		rr := httptest.NewRecorder()
		h.CrewFiles(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rr.Code)
		}
	})
	t.Run("crewExists DB error 500", func(t *testing.T) {
		h := newProxyHandlerForTest(t, "/tmp/pf2-no-socket-6")
		h.db.Close()
		req := covPF2Req("GET", "/x")
		req.SetPathValue("crewId", "c1")
		req = withWorkspaceUser(req, "u1", "ws1", "OWNER")
		rr := httptest.NewRecorder()
		h.CrewFiles(rr, req)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rr.Code)
		}
	})
	t.Run("sidecar unreachable 502", func(t *testing.T) {
		h, userID, wsID, crewID, _ := covProxyRig(t, "/tmp/pf2-no-socket-7")
		req := covPF2Req("GET", "/x?recursive=true&agent_slug=px-agent")
		req.SetPathValue("crewId", crewID)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.CrewFiles(rr, req)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rr.Code)
		}
	})
	t.Run("non-JSON IPC fallback", func(t *testing.T) {
		sock := covIPCJSON(t, map[string]any{})
		h, userID, wsID, crewID, _ := covProxyRig(t, sock)
		req := covPF2Req("GET", "/x")
		req.SetPathValue("crewId", crewID)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.CrewFiles(rr, req)
		if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != "[]" {
			t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
		}
	})
}

func TestPF2_CrewFileDownload_Gates(t *testing.T) {
	t.Run("empty role 403", func(t *testing.T) {
		h, userID, wsID, crewID, _ := covProxyRig(t, "/tmp/pf2-no-socket-8")
		req := covPF2Req("GET", "/x?path=shared/a.txt")
		req.SetPathValue("crewId", crewID)
		req = withWorkspaceUser(req, userID, wsID, "")
		rr := httptest.NewRecorder()
		h.CrewFileDownload(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rr.Code)
		}
	})
	t.Run("crewExists DB error 500", func(t *testing.T) {
		h := newProxyHandlerForTest(t, "/tmp/pf2-no-socket-9")
		h.db.Close()
		req := covPF2Req("GET", "/x?path=shared/a.txt")
		req.SetPathValue("crewId", "c1")
		req = withWorkspaceUser(req, "u1", "ws1", "OWNER")
		rr := httptest.NewRecorder()
		h.CrewFileDownload(rr, req)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rr.Code)
		}
	})
	t.Run("sidecar unreachable 404", func(t *testing.T) {
		h, userID, wsID, crewID, _ := covProxyRig(t, "/tmp/pf2-no-socket-10")
		req := covPF2Req("GET", "/x?path=shared/a.txt")
		req.SetPathValue("crewId", crewID)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.CrewFileDownload(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rr.Code)
		}
	})
}

func TestPF2_CrewFileSave_ExistsCheckDBError500(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/pf2-no-socket-11")
	h.db.Close()
	req := covPF2Req("PUT", "/x?path=shared/a.txt")
	req.SetPathValue("crewId", "c1")
	req = withWorkspaceUser(req, "u1", "ws1", "OWNER")
	rr := httptest.NewRecorder()
	h.CrewFileSave(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPF2_AgentContainerFiles_EmptyRole403(t *testing.T) {
	h, userID, wsID, _, agentID := covProxyRig(t, "/tmp/pf2-no-socket-12")
	req := covPF2Req("GET", "/x")
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "")
	rr := httptest.NewRecorder()
	h.AgentContainerFiles(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}
