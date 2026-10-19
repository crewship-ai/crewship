package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The real SQL driver commits consent after the initial audience/scope read.
// Neither authorization verdicts nor attachment/list response data are mocked.
func TestPrivateArtifactProjectionSealsDuringRead(t *testing.T) {
	for _, surface := range []string{"chat list", "attachment list"} {
		for _, actor := range []string{"ADMIN", "OWNER", "OWNER audit unavailable"} {
			t.Run(surface+"/"+actor, func(t *testing.T) {
				var db *sql.DB
				var workspace, author, owner, admin, agent, chat, marker, match string
				var seal func()
				var serve func(*sql.DB, http.ResponseWriter, *http.Request)
				if surface == "chat list" {
					h, ws, a, o, ad := ordinaryConversationAuditFixture(t)
					db, workspace, author, owner, admin = h.db, ws, a, o, ad
					agent, chat, marker, match = "principal-agent", "principal-chat", "late-private-conversation", "SELECT c.id, COUNT(m.id)"
					seal = func() {
						execOrFatal(t, db, `INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at) SELECT m.id,c.workspace_id,c.created_by,c.id,'late-list','2026-10-19T10:00:00Z' FROM conversation_messages m JOIN chats c ON c.id=m.session_id WHERE c.id='principal-chat' AND m.id='principal-message'`)
					}
					serve = func(reader *sql.DB, w http.ResponseWriter, r *http.Request) {
						NewAgentHandler(reader, newTestLogger()).ListChats(w, r)
					}
				} else {
					h, _, a, ws, ag, ch := newChatAttachmentFixture(t)
					db, workspace, author, agent, chat = h.db, ws, a, ag, ch
					owner, admin, marker, match = "late-attachment-owner", "late-attachment-admin", "late-private-output.txt", "FROM attachments a"
					if code, _, body := uploadChatAttachment(t, h, author, workspace, agent, chat, marker, "fixture"); code != 201 {
						t.Fatalf("upload fixture=%d %s", code, body)
					}
					for _, user := range []struct{ id, role string }{{owner, "OWNER"}, {admin, "ADMIN"}} {
						execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, user.id, user.id+"@example.test")
						execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,?)`, user.id+"-member", workspace, user.id, user.role)
					}
					execOrFatal(t, db, `UPDATE chats SET visibility='private' WHERE id=?`, chat)
					execOrFatal(t, db, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES('late-attachment-consent',?,?,'user','Create document','2026-10-19T10:00:00Z',?)`, chat, agent, author)
					seal = func() {
						execOrFatal(t, db, `INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at) VALUES('late-attachment-consent',?,?,?,'late-attachment','2026-10-19T10:00:00Z')`, workspace, author, chat)
					}
					serve = func(reader *sql.DB, w http.ResponseWriter, r *http.Request) {
						h.db = reader
						h.ListAgentChatAttachments(w, r)
					}
				}
				user, role := owner, "OWNER"
				if actor == "ADMIN" {
					user, role = admin, "ADMIN"
				}
				request := pagesRequest(t, http.MethodGet, "/fixture", workspace, user, role, "")
				request.SetPathValue("agentId", agent)
				request.SetPathValue("chatId", chat)
				baseline := httptest.NewRecorder()
				serve(db, baseline, request)
				if baseline.Code != 200 || !strings.Contains(baseline.Body.String(), marker) {
					t.Fatalf("ordinary positive baseline missing status=%d", baseline.Code)
				}
				if actor == "OWNER audit unavailable" {
					execOrFatal(t, db, `CREATE TRIGGER late_projection_reject_audit BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN SELECT RAISE(ABORT,'fixture audit unavailable'); END`)
				}
				hook := &reviewQueryHook{match: match, nth: 1, run: seal}
				reader := sql.OpenDB(&reviewHookConnector{dsn: "file:" + reviewDBPath(t, db), inner: db.Driver(), hook: hook})
				t.Cleanup(func() { _ = reader.Close() })
				reader.SetMaxOpenConns(1)
				ctx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
				defer cancel()
				request = request.WithContext(ctx)
				response := httptest.NewRecorder()
				gate := &documentAuditGateWriter{ResponseRecorder: response, beforeHeader: func(status int) {
					if actor != "OWNER" || status != 200 {
						return
					}
					var count int
					if err := db.QueryRowContext(ctx, `SELECT count(*) FROM audit_logs WHERE workspace_id=? AND user_id=? AND entity_type='chat' AND entity_id=? AND action='artifact.private_owner_accessed' AND json_extract(metadata,'$.owner_user_id')=? AND json_extract(metadata,'$.output_source')='conversation'`, workspace, user, chat, author).Scan(&count); err != nil {
						t.Fatal(err)
					}
					if count == 0 {
						t.Error("late-sealed OWNER metadata released before durable audit")
					}
				}}
				serve(reader, gate, request)
				if !hook.didFire() || ctx.Err() != nil {
					t.Fatalf("late consent schedule failed: fired=%v context=%v", hook.didFire(), ctx.Err())
				}
				var seals int
				if err := db.QueryRowContext(ctx, `SELECT count(*) FROM document_creation_consents WHERE chat_id=?`, chat).Scan(&seals); err != nil || seals != 1 {
					t.Fatalf("actual consent missing count=%d err=%v", seals, err)
				}
				disclosed := strings.Contains(response.Body.String(), marker)
				if actor == "OWNER" {
					if response.Code != 200 || !disclosed {
						t.Errorf("authorized late-sealed metadata missing status=%d", response.Code)
					}
				} else if disclosed {
					t.Errorf("late seal/audit refusal released protected metadata actor=%s status=%d", actor, response.Code)
				} else if actor == "ADMIN" && response.Code != 200 && response.Code != 403 && response.Code != 404 {
					t.Errorf("ADMIN negative came from an unrelated failure status=%d", response.Code)
				} else if actor == "OWNER audit unavailable" && response.Code < 500 {
					t.Errorf("required audit refusal did not report unavailable status=%d", response.Code)
				}
			})
		}
	}
}
