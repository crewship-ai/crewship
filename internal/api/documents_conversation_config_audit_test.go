package api

import (
	"database/sql"
	"net/http/httptest"
	"testing"
)

func TestPrivateConversationFinalConfigAuditsNewSeal(t *testing.T) {
	setTestEncryptionKey(t)
	for _, fault := range []bool{false, true} {
		t.Run(map[bool]string{false: "new seal", true: "new seal audit unavailable"}[fault], func(t *testing.T) {
			h, ws, author, owner, _ := ordinaryConversationAuditFixture(t)
			// Initial resolve is ordinary; a real consent commit seals it during config
			// assembly, before its last existing CaptureTrusted check. Use the existing
			// test-only SQLite driver seam, never a production hook or mocked verdict.
			if fault {
				execOrFatal(t, h.db, `CREATE TRIGGER reject_context_inspection BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'fixture audit unavailable'); END`)
			}
			hook := &reviewQueryHook{match: "SELECT c.workspace_id,c.agent_id,wm.id", nth: 3, run: func() {
				execOrFatal(t, h.db, `INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at) SELECT m.id,c.workspace_id,c.created_by,c.id,'during-config','2026-10-19T00:00:00Z' FROM conversation_messages m JOIN chats c ON c.id=m.session_id WHERE c.id='principal-chat' AND m.id='principal-message'`)
			}}
			reader := sql.OpenDB(&reviewHookConnector{dsn: "file:" + reviewDBPath(t, h.db), inner: h.db.Driver(), hook: hook})
			t.Cleanup(func() { _ = reader.Close() })
			handler := NewInternalHandler(reader, "fixture-host-token", newTestLogger())
			req := httptest.NewRequest("GET", "/api/v1/internal/chats/principal-chat/resolve-human?user_id="+owner, nil)
			req.SetPathValue("chatId", "principal-chat")
			response := httptest.NewRecorder()
			gate := &documentAuditGateWriter{ResponseRecorder: response, beforeHeader: func(code int) {
				if code == 200 && privateConversationAuditCount(t, req.Context(), h.db, ws, owner, author) == 0 {
					t.Error("final config released after a new seal without OWNER context audit")
				}
			}}
			handler.ResolveHumanChat(gate, req)
			if !hook.didFire() {
				t.Fatal("config interleaving did not reach the final authority check")
			}
			if fault {
				if response.Code == 200 {
					t.Error("final config ignored required audit failure")
				}
			} else if response.Code != 200 {
				t.Errorf("authorized config unavailable status=%d", response.Code)
			}
		})
	}
}
