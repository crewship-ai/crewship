package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Real private Document admission/persistence and HTTP handlers. IPC explicitly
// supplies unscoped log Content/Metadata copies; this is not a model/log-writer run.
func TestPrivateArtifactAgentLogCopiesRequireAudienceAndAudit(t *testing.T) {
	for _, surface := range []string{"logs", "debug"} {
		for _, actor := range []string{"author", "ADMIN", "OWNER", "OWNER audit unavailable", "mixed author"} {
			t.Run(surface+"/"+actor, func(t *testing.T) {
				h, workspace, author, owner, admin, saved := privateConversationAuditFixture(t)
				// Debug retains its existing manage-role gate. Author identity does
				// not grant that role; this fixture supplies actual ADMIN standing.
				execOrFatal(t, h.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, workspace, author)
				if actor == "mixed author" {
					execOrFatal(t, h.db, `INSERT INTO users(id,email) VALUES('other-log-author','other-log-author@example.test')`)
					execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('other-log-author-member',?,'other-log-author','MEMBER')`, workspace)
					execOrFatal(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('other-private-log-chat',?,'principal-agent','other-log-author','private')`, workspace)
					execOrFatal(t, h.db, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES('other-log-consent','other-private-log-chat','principal-agent','user','Create document','2026-10-19T10:00:00Z','other-log-author')`)
					execOrFatal(t, h.db, `INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at) VALUES('other-log-consent',?,'other-log-author','other-private-log-chat','other-log-consent','2026-10-19T10:00:00Z')`, workspace)
				}
				if actor == "OWNER audit unavailable" {
					execOrFatal(t, h.db, `CREATE TRIGGER log_copy_reject_audit BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN SELECT RAISE(ABORT,'fixture audit unavailable'); END`)
				}
				// No chat/run identity is supplied by these copies. A reader must
				// qualify every protected context, not infer a safe line subset.
				logs := map[string]any{"logs": []any{map[string]any{"content": saved.Title, "metadata": map[string]string{"document_url": saved.URL}}}}
				socket := covIPCJSON(t, map[string]any{
					"/agents/principal-agent/logs": logs,
					"/debug/logs":                  logs, "/debug/info": map[string]string{"version": "fixture"},
					"/agents/principal-agent/status": map[string]string{"status": "running"},
				})
				proxy := NewProxyHandler(h.db, newTestLogger(), socket)
				user, role := author, "ADMIN"
				if actor == "ADMIN" {
					user = admin
				} else if strings.HasPrefix(actor, "OWNER") {
					user, role = owner, "OWNER"
				}
				request := pagesRequest(t, http.MethodGet, "/fixture", workspace, user, role, "")
				request.SetPathValue("agentId", "principal-agent")
				ctx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
				defer cancel()
				request = request.WithContext(ctx)
				h.db.SetMaxOpenConns(1)
				probe := privateConversationAuditProbe(t, h.db)
				response := httptest.NewRecorder()
				gate := &documentAuditGateWriter{ResponseRecorder: response, beforeHeader: func(status int) {
					if actor == "OWNER" && status == 200 && privateConversationAuditCount(t, ctx, probe, workspace, owner, author) == 0 {
						t.Error("foreign OWNER log copies released before durable source audit")
					}
				}}
				if surface == "logs" {
					proxy.AgentLogs(gate, request)
				} else {
					proxy.AgentDebug(gate, request)
				}
				if ctx.Err() != nil {
					t.Fatalf("log read/audit stalled: %v", ctx.Err())
				}
				disclosed := strings.Contains(response.Body.String(), saved.Title) || strings.Contains(response.Body.String(), saved.Slug)
				switch actor {
				case "author", "OWNER":
					if response.Code != 200 || !strings.Contains(response.Body.String(), saved.Title) {
						t.Errorf("authorized log fixture missing status=%d", response.Code)
					}
				case "ADMIN", "mixed author":
					if response.Code != 404 || disclosed {
						t.Errorf("unscoped log contexts exposed to actor=%s status=%d", actor, response.Code)
					}
				default:
					if response.Code < 500 || disclosed {
						t.Errorf("required audit failure released log copies status=%d", response.Code)
					}
				}
			})
		}
	}
}
