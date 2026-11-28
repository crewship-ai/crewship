package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
)

func TestChatRunAuditFollowsChatAudienceAndDeletion(t *testing.T) {
	h, userID, wsID, _, agentID := covProxyRig(t, "")
	const chatID, runID = "audit-chat", "audit-run"
	if _, err := h.db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, title)
		VALUES (?, ?, ?, ?, 'synthetic audit')`, chatID, agentID, wsID, userID); err != nil {
		t.Fatal(err)
	}
	storeFor := func() *conversation.Store {
		return conversation.NewStore(t.TempDir(), slog.New(slog.DiscardHandler), conversation.WithDB(h.db))
	}
	if ok, err := storeFor().BeginRunAudit(t.Context(), chatID, runID); !ok || err != nil {
		t.Fatalf("create audit: %v", err)
	}
	get := func(want int, workspace, run string) string {
		t.Helper()
		req := withWorkspaceUser(httptest.NewRequest("GET", "/", nil), userID, workspace, "OWNER")
		req.SetPathValue("chatId", chatID)
		req.SetPathValue("runId", run)
		response := httptest.NewRecorder()
		h.ChatRunAudit(response, req)
		if response.Code != want {
			t.Fatalf("audit HTTP %d, want %d: %s", response.Code, want, response.Body)
		}
		return response.Body.String()
	}
	if got := get(http.StatusOK, wsID, runID); !strings.Contains(got, `"status":"incomplete"`) || strings.Contains(got, "events") {
		t.Fatalf("audit after restart before publication: %s", got)
	}
	wire := []byte(`[{"type":"result","result":"synthetic done"}]`)
	if err := storeFor().FinishRunAudit(t.Context(), chatID, runID, conversation.RunAuditStaged, wire); err != nil {
		t.Fatal(err)
	}
	if got := get(http.StatusOK, wsID, runID); !strings.Contains(got, `"status":"incomplete"`) || strings.Contains(got, "events") {
		t.Fatalf("audit after restart with only the capture committed: %s", got)
	}
	if err := storeFor().PublishRunAudit(t.Context(), chatID, runID); err != nil {
		t.Fatal(err)
	}
	if got := get(http.StatusOK, wsID, runID); !strings.Contains(got, `"status":"archived"`) || !strings.Contains(got, "synthetic done") {
		t.Fatalf("audit after publication: %s", got)
	}
	get(http.StatusNotFound, wsID, "other-run")
	get(http.StatusNotFound, "other-workspace", runID)
	if _, err := h.db.Exec(`DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, wsID, userID); err != nil {
		t.Fatal(err)
	}
	get(http.StatusNotFound, wsID, runID)
	if _, err := h.db.Exec(`DELETE FROM chats WHERE id=?`, chatID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM chat_run_audits WHERE chat_id=?`, chatID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("chat deletion left %d audit records: %v", count, err)
	}
}
