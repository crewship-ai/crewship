package api

// Coverage for proxy.go — AgentDebug, AgentLogs, AgentStop and
// ChatMessages. Reuses the Unix-socket IPC harness from proxy_test.go
// (newUnixIPCServer) so the handlers round-trip through their real ipcGet/
// ipcPost helpers instead of only hitting the "socket unreachable" branch.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// covProxyRig seeds the standard user/workspace/crew/agent fixture and
// returns a ProxyHandler bound to the given socket.
func covProxyRig(t *testing.T, sock string) (h *ProxyHandler, userID, wsID, crewID, agentID string) {
	t.Helper()
	h = NewProxyHandler(setupTestDB(t), newTestLogger(), sock)
	userID = seedTestUser(t, h.db)
	wsID = seedTestWorkspace(t, h.db, userID)
	crewID = seedCrewRow(t, h.db, "crew-pxc", wsID, "PXC", "pxc")
	agentID = seedAgentRow(t, h.db, "agent-pxc", wsID, crewID, "Px Agent", "px-agent", "AGENT")
	return h, userID, wsID, crewID, agentID
}

// covIPCJSON answers every IPC path from a route→payload map; unmatched
// paths get a 404 with a non-JSON body.
func covIPCJSON(t *testing.T, routes map[string]any) string {
	t.Helper()
	return newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if payload, ok := routes[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(payload)
			return
		}
		http.NotFound(w, r)
	}))
}

// ---- AgentDebug ----

func TestAgentDebug_RoleGateAndNotFound(t *testing.T) {
	h, userID, wsID, _, agentID := covProxyRig(t, "/tmp/no-such-socket-pxc")

	t.Run("MEMBER forbidden", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("agentId", agentID)
		req = withWorkspaceUser(req, userID, wsID, "MEMBER")
		rr := httptest.NewRecorder()
		h.AgentDebug(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})
	t.Run("unknown agent 404", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("agentId", "ghost")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.AgentDebug(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})
}

func TestAgentDebug_UnreachableSidecar_StillReturnsAgentInfo(t *testing.T) {
	h, userID, wsID, _, agentID := covProxyRig(t, "/tmp/no-such-socket-pxc2")
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentDebug(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["crewshipd_reachable"] != false {
		t.Errorf("crewshipd_reachable = %v, want false", out["crewshipd_reachable"])
	}
	runtime, ok := out["runtime"].(map[string]any)
	if !ok || runtime["status"] != "unreachable" {
		t.Errorf("runtime = %v, want unreachable marker", out["runtime"])
	}
	agent, ok := out["agent"].(map[string]any)
	if !ok || agent["id"] != agentID {
		t.Errorf("agent = %v", out["agent"])
	}
}

func TestAgentDebug_ReachableSidecar_FullPayload(t *testing.T) {
	routesReady := make(map[string]any)
	sock := covIPCJSON(t, routesReady)
	h, userID, wsID, crewID, agentID := covProxyRig(t, sock)
	routesReady["/debug/info"] = map[string]any{"version": "test"}
	routesReady["/agents/"+agentID+"/status"] = map[string]any{"status": "running"}
	routesReady["/debug/logs"] = map[string]any{"logs": []string{"line1"}}
	routesReady["/agents/"+agentID+"/logs"] = map[string]any{"logs": []string{"agent-line"}}
	_ = crewID

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentDebug(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["crewshipd_reachable"] != true {
		t.Errorf("crewshipd_reachable = %v, want true", out["crewshipd_reachable"])
	}
	crewshipd, _ := out["crewshipd"].(map[string]any)
	if crewshipd["version"] != "test" {
		t.Errorf("crewshipd = %v", out["crewshipd"])
	}
	runtime, _ := out["runtime"].(map[string]any)
	if runtime["status"] != "running" {
		t.Errorf("runtime = %v", out["runtime"])
	}
	logs, _ := out["service_logs"].([]any)
	if len(logs) != 1 || logs[0] != "line1" {
		t.Errorf("service_logs = %v", out["service_logs"])
	}
	agentLogs, _ := out["agent_logs"].([]any)
	if len(agentLogs) != 1 || agentLogs[0] != "agent-line" {
		t.Errorf("agent_logs = %v", out["agent_logs"])
	}
}

// ---- AgentLogs ----

func TestAgentLogs_Branches(t *testing.T) {
	routes := make(map[string]any)
	sock := covIPCJSON(t, routes)
	h, userID, wsID, _, agentID := covProxyRig(t, sock)
	// Sidecar keys agent logs by slug, not id.
	routes["/agents/px-agent/logs"] = map[string]any{"logs": []any{map[string]any{"msg": "hello"}}}

	t.Run("unknown agent 404", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("agentId", "ghost")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.AgentLogs(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})

	t.Run("agent without crew returns empty list", func(t *testing.T) {
		crewless := seedAgentRow(t, h.db, "agent-nocrew-px", wsID, "", "NC", "nc-px", "COORDINATOR")
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("agentId", crewless)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.AgentLogs(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if got := rr.Body.String(); got != "[]\n" {
			t.Errorf("body = %q, want []", got)
		}
	})

	t.Run("happy path unwraps logs array", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/x?limit=10&offset=0", nil)
		req.SetPathValue("agentId", agentID)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.AgentLogs(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		var logs []map[string]any
		if err := json.NewDecoder(rr.Body).Decode(&logs); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(logs) != 1 || logs[0]["msg"] != "hello" {
			t.Errorf("logs = %v", logs)
		}
	})

	t.Run("non-JSON sidecar response degrades to empty list", func(t *testing.T) {
		// Re-point the route at a payload-less 404 by deleting it.
		delete(routes, "/agents/px-agent/logs")
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("agentId", agentID)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.AgentLogs(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if got := rr.Body.String(); got != "[]\n" {
			t.Errorf("body = %q, want []", got)
		}
	})
}

// ---- AgentStop ----

func TestAgentStop_Branches(t *testing.T) {
	routes := map[string]any{}
	sock := covIPCJSON(t, routes)
	h, userID, wsID, _, agentID := covProxyRig(t, sock)

	t.Run("VIEWER forbidden", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/x", nil)
		req.SetPathValue("agentId", agentID)
		req = withWorkspaceUser(req, userID, wsID, "VIEWER")
		rr := httptest.NewRecorder()
		h.AgentStop(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})

	t.Run("unknown agent 404", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/x", nil)
		req.SetPathValue("agentId", "ghost")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.AgentStop(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})

	t.Run("happy path stops and persists STOPPED", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/x", nil)
		req.SetPathValue("agentId", agentID)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.AgentStop(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		var out map[string]string
		if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out["status"] != "STOPPED" || out["id"] != agentID {
			t.Errorf("out = %v", out)
		}
		var status string
		if err := h.db.QueryRow(`SELECT status FROM agents WHERE id = ?`, agentID).Scan(&status); err != nil {
			t.Fatalf("query status: %v", err)
		}
		if status != "STOPPED" {
			t.Errorf("db status = %q, want STOPPED", status)
		}
	})
}

// ---- ChatMessages ----

func TestChatMessages_Branches(t *testing.T) {
	routes := make(map[string]any)
	sock := covIPCJSON(t, routes)
	h, userID, wsID, _, agentID := covProxyRig(t, sock)
	chatID := "chat-pxc"
	if _, err := h.db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, title)
		VALUES (?, ?, ?, ?, 'px chat')`, chatID, agentID, wsID, userID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	routes["/chats/"+chatID+"/messages"] = map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}

	t.Run("empty role forbidden", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("chatId", chatID)
		req = withWorkspaceUser(req, userID, wsID, "")
		rr := httptest.NewRecorder()
		h.ChatMessages(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})

	t.Run("nonexistent chat returns empty messages", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("chatId", "no-such-chat")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.ChatMessages(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		var out map[string][]any
		if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(out["messages"]) != 0 {
			t.Errorf("messages = %v, want empty", out["messages"])
		}
	})

	t.Run("non-member forbidden", func(t *testing.T) {
		stranger := "user-stranger-pxc"
		if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 's@x.com', 'S')`, stranger); err != nil {
			t.Fatalf("seed stranger: %v", err)
		}
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("chatId", chatID)
		req = withWorkspaceUser(req, stranger, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.ChatMessages(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})

	t.Run("happy path proxies messages", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/x?limit=999&offset=0", nil) // limit clamps to 500
		req.SetPathValue("chatId", chatID)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.ChatMessages(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		var out map[string][]map[string]any
		if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(out["messages"]) != 1 || out["messages"][0]["content"] != "hi" {
			t.Errorf("messages = %v", out["messages"])
		}
	})

	t.Run("sidecar unreachable 502", func(t *testing.T) {
		h2, userID2, wsID2, _, agentID2 := covProxyRig(t, "/tmp/no-such-socket-pxc3")
		if _, err := h2.db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, title)
			VALUES ('chat-down', ?, ?, ?, 'x')`, agentID2, wsID2, userID2); err != nil {
			t.Fatalf("seed chat: %v", err)
		}
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("chatId", "chat-down")
		req = withWorkspaceUser(req, userID2, wsID2, "OWNER")
		rr := httptest.NewRecorder()
		h2.ChatMessages(rr, req)
		if rr.Code != http.StatusBadGateway {
			t.Errorf("status = %d, want 502", rr.Code)
		}
	})
}
