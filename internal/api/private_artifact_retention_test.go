package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/retention"
)

// Actual configured-retention count/sweep and diagnostics handlers. Retained
// private-copy authority is pinned; this does not implement chat tombstones or
// a general erasure policy. IPC is a deterministic unscoped log-copy fixture.
func TestPrivateArtifactRetentionPreservesCopyAudience(t *testing.T) {
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
				admin = "retention-context-admin"
				pagesSeedUser(t, pages, workspace, admin, "retention-context-admin@example.test", "ADMIN")
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
			// These fixtures hold no assignments, escalations, approvals, pending
			// pipeline runs or reply inbox items. Age only this real source chat;
			// do not delete consent or its immutable classification to make it idle.
			execOrFatal(t, pages.db, `UPDATE chats SET created_at='2026-01-01T00:00:00Z',updated_at='2026-01-01T00:00:00Z',last_activity_at='2026-01-01T00:00:00Z' WHERE id='principal-chat'`)
			days, now := 1, time.Now().UTC()
			count, err := retention.Count(t.Context(), pages.db, workspace, retention.Chats, &days, now)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if protected {
				want = 0
			}
			if count != int64(want) {
				t.Errorf("configured retention candidate count=%d want=%d", count, want)
			}
			deleted, more, err := retention.SweepChats(t.Context(), pages.db, workspace, days, now)
			if err != nil {
				t.Fatal(err)
			}
			if deleted != want || more {
				t.Errorf("configured retention swept=%d want=%d more=%v", deleted, want, more)
			}
			var chats, messages, sealed int
			if err := pages.db.QueryRow(`SELECT
 (SELECT count(*) FROM chats WHERE id='principal-chat'),
 (SELECT count(*) FROM conversation_messages WHERE session_id='principal-chat'),
 (SELECT count(*) FROM chats WHERE id='principal-chat' AND private_artifact_author_user_id=?)`, author).Scan(&chats, &messages, &sealed); err != nil {
				t.Fatal(err)
			}
			if protected && (chats != 1 || messages != beforeMessages || sealed != 1) {
				t.Errorf("retention erased source privacy proof: chats=%d messages=%d sealed=%d", chats, messages, sealed)
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
					t.Errorf("post-retention ADMIN log copy: status=%d disclosed=%v", logs.Code, disclosed)
				}
			} else if logs.Code != http.StatusOK || !disclosed {
				t.Errorf("ordinary log positive control: status=%d", logs.Code)
			}
		})
	}
}
