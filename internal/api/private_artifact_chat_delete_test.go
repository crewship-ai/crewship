package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Actual handlers, admission and SQL retention; IPC supplies an explicitly
// unscoped log-copy canary. This is not a model run or a complete erasure flow.
func TestPrivateArtifactChatDeletePreservesCopyAudience(t *testing.T) {
	for _, lifecycle := range []string{"created Document", "consent only", "ordinary never sealed"} {
		t.Run(lifecycle, func(t *testing.T) {
			var pages *PageHandler
			var workspace, author, admin string
			canary := "Ordinary log copy fixture"
			protected := lifecycle != "ordinary never sealed"
			switch lifecycle {
			case "created Document":
				var saved documentCreationResult
				pages, workspace, author, _, admin, saved = privateConversationAuditFixture(t)
				canary = saved.Title + " " + saved.URL
			case "consent only":
				f, _ := documentCreationAdmissionFixture(t)
				pages, workspace, author = f.pages, f.a.WorkspaceID, f.a.HumanUserID
				admin = "chat-delete-admin"
				pagesSeedUser(t, pages, workspace, admin, "chat-delete-admin@example.test", "ADMIN")
				canary = "Private admitted prompt log-copy fixture"
			default:
				pages, workspace, author, _, admin = ordinaryConversationAuditFixture(t)
			}
			var beforeMessages, beforeSealed int
			if err := pages.db.QueryRow(`SELECT
 (SELECT count(*) FROM conversation_messages WHERE session_id='principal-chat'),
 (SELECT count(*) FROM chats WHERE id='principal-chat' AND workspace_id=? AND private_artifact_author_user_id=?)`, workspace, author).Scan(&beforeMessages, &beforeSealed); err != nil {
				t.Fatal(err)
			}
			wantSealed := 0
			if protected {
				wantSealed = 1
			}
			if beforeMessages == 0 || beforeSealed != wantSealed {
				t.Fatalf("retention setup: messages=%d sealed=%d", beforeMessages, beforeSealed)
			}
			handler := NewAgentHandler(pages.db, newTestLogger())
			response := deleteChatReq(t, handler, author, workspace, "MEMBER", "principal-agent", "principal-chat")
			wantStatus := http.StatusNoContent
			if protected {
				wantStatus = http.StatusConflict
			}
			if response.Code != wantStatus {
				t.Errorf("author chat deletion: status=%d want=%d", response.Code, wantStatus)
			}
			var chats, messages, sealed int
			if err := pages.db.QueryRow(`SELECT
 (SELECT count(*) FROM chats WHERE id='principal-chat'),
 (SELECT count(*) FROM conversation_messages WHERE session_id='principal-chat'),
 (SELECT count(*) FROM chats WHERE id='principal-chat' AND private_artifact_author_user_id=?)`, author).Scan(&chats, &messages, &sealed); err != nil {
				t.Fatal(err)
			}
			if protected && (chats != 1 || messages != beforeMessages || sealed != 1) {
				t.Errorf("deletion erased source privacy proof: chats=%d messages=%d sealed=%d", chats, messages, sealed)
			} else if !protected && (chats != 0 || messages != 0) {
				t.Errorf("ordinary chat cleanup changed: chats=%d messages=%d", chats, messages)
			}
			socket := covIPCJSON(t, map[string]any{"/agents/principal-agent/logs": map[string]any{"logs": []any{map[string]string{"content": canary}}}})
			proxy := NewProxyHandler(pages.db, newTestLogger(), socket)
			request := pagesRequest(t, http.MethodGet, "/api/v1/agents/principal-agent/logs", workspace, admin, "ADMIN", "")
			request.SetPathValue("agentId", "principal-agent")
			logs := httptest.NewRecorder()
			proxy.AgentLogs(logs, request)
			encodedCanary, _ := json.Marshal(canary)
			disclosed := strings.Contains(logs.Body.String(), string(encodedCanary))
			if protected {
				if logs.Code != http.StatusNotFound || disclosed {
					t.Errorf("post-delete ADMIN log copy: status=%d disclosed=%v", logs.Code, disclosed)
				}
			} else if logs.Code != http.StatusOK || !disclosed {
				t.Errorf("ordinary log positive control: status=%d", logs.Code)
			}
		})
	}
}
