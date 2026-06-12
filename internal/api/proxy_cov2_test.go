package api

// Second coverage pass for proxy.go: the ipc helper request-construction
// failures, proxyJSON's stream-error tolerance, CrewshipdHealth's proxied
// success, AgentDebug/AgentStop DB-error paths, the crew-less agent branch,
// AgentLogs' unreachable-sidecar fallback, ChatMessages' non-member 403 and
// AgentGitLog's read-role gate.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPX2_IPCHelpers_InvalidPath(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/px2-no-socket")
	ctx := context.Background()
	// A control character in the path makes http.NewRequestWithContext fail
	// before any dialing happens.
	if _, err := h.ipcGet(ctx, "/bad\npath"); err == nil {
		t.Error("ipcGet: want request-construction error")
	}
	if _, err := h.ipcPost(ctx, "/bad\npath", nil); err == nil {
		t.Error("ipcPost: want request-construction error")
	}
	if _, err := h.ipcPut(ctx, "/bad\npath", nil); err == nil {
		t.Error("ipcPut: want request-construction error")
	}
}

// covPX2ErrReader fails mid-stream so proxyJSON's copy-error branch runs.
type covPX2ErrReader struct{}

func (covPX2ErrReader) Read([]byte) (int, error) { return 0, errors.New("stream snapped") }
func (covPX2ErrReader) Close() error             { return nil }

func TestPX2_ProxyJSON_CopyError(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/px2-no-socket-2")
	rr := httptest.NewRecorder()
	resp := &http.Response{StatusCode: 200, Body: covPX2ErrReader{}, Header: make(http.Header)}
	h.proxyJSON(rr, resp) // must not panic; error is logged at debug
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestPX2_CrewshipdHealth_Proxied(t *testing.T) {
	sock := covIPCJSON(t, map[string]any{"/health": map[string]string{"status": "ok"}})
	h := newProxyHandlerForTest(t, sock)
	rr := httptest.NewRecorder()
	h.CrewshipdHealth(rr, httptest.NewRequest("GET", "/x", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"status":"ok"`) {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestPX2_AgentDebug_DBError500(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/px2-no-socket-3")
	h.db.Close()
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("agentId", "a1")
	req = withWorkspaceUser(req, "u1", "ws1", "OWNER")
	rr := httptest.NewRecorder()
	h.AgentDebug(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPX2_AgentDebug_AgentWithoutCrew(t *testing.T) {
	h, userID, wsID, _, _ := covProxyRig(t, "/tmp/px2-no-socket-4")
	loner := seedAgentRow(t, h.db, "agent-px2-loner", wsID, "", "Loner", "px2-loner", "COORDINATOR")
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("agentId", loner)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentDebug(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"agent_logs":[]`) {
		t.Errorf("body = %s, want empty agent_logs", rr.Body.String())
	}
}

func TestPX2_AgentLogs_SidecarUnreachable_EmptyList(t *testing.T) {
	h, userID, wsID, _, agentID := covProxyRig(t, "/tmp/px2-no-socket-5")
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentLogs(rr, req)
	if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatalf("status = %d body=%q, want 200 []", rr.Code, rr.Body.String())
	}
}

func TestPX2_AgentStop_ExistsCheckDBError500(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/px2-no-socket-6")
	h.db.Close()
	req := httptest.NewRequest("POST", "/x", nil)
	req.SetPathValue("agentId", "a1")
	req = withWorkspaceUser(req, "u1", "ws1", "OWNER")
	rr := httptest.NewRecorder()
	h.AgentStop(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPX2_AgentStop_UpdateFails500(t *testing.T) {
	h, userID, wsID, _, agentID := covProxyRig(t, "/tmp/px2-no-socket-7")
	if _, err := h.db.Exec(`
		CREATE TRIGGER px2_block_stop BEFORE UPDATE ON agents
		WHEN NEW.status = 'STOPPED'
		BEGIN SELECT RAISE(ABORT, 'px2 no stop'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	req := httptest.NewRequest("POST", "/x", nil)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentStop(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPX2_ChatMessages_NonMember403(t *testing.T) {
	h, userID, wsID, _, agentID := covProxyRig(t, "/tmp/px2-no-socket-8")
	// Chat lives in a different workspace the caller is not a member of.
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-px2-b', 'B', 'ws-px2-b')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES ('chat-px2-b', ?, 'ws-px2-b', 'x', 'CHAT', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`,
		agentID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("chatId", "chat-px2-b")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ChatMessages(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPX2_AgentGitLog_EmptyRole403(t *testing.T) {
	h, userID, wsID, _, agentID := covProxyRig(t, "/tmp/px2-no-socket-9")
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "") // empty role fails closed
	rr := httptest.NewRecorder()
	h.AgentGitLog(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

var _ io.ReadCloser = covPX2ErrReader{}
