package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Real migrated SQL and attachment HTTP handler; the existing IPC fixture only
// stores the upload. Assertions concern protected metadata, not blob downloads.
func TestPrivateArtifactAttachmentReadRequiresOwnerAudit(t *testing.T) {
	for _, tc := range []struct {
		name, reader, role string
		rejectAudit        bool
		want               int
	}{
		{"author", "author", "OWNER", false, 200},
		{"author with unavailable audit", "author", "OWNER", true, 200},
		{"foreign OWNER", "attachment-owner", "OWNER", false, 200},
		{"foreign OWNER unavailable audit", "attachment-owner", "OWNER", true, 500},
		{"other ADMIN", "attachment-admin", "ADMIN", false, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, author, workspace, agent, chat := newChatAttachmentFixture(t)
			const filename = "private-output-canary.txt"
			code, _, body := uploadChatAttachment(t, h, author, workspace, agent, chat, filename, "private attachment fixture")
			if code != http.StatusCreated {
				t.Fatalf("upload fixture: %d %s", code, body)
			}
			for _, member := range []struct{ id, role string }{{"attachment-owner", "OWNER"}, {"attachment-admin", "ADMIN"}} {
				execOrFatal(t, h.db, `INSERT INTO users(id,email) VALUES(?,?)`, member.id, member.id+"@example.test")
				execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,?)`, member.id+"-member", workspace, member.id, member.role)
			}
			execOrFatal(t, h.db, `UPDATE chats SET visibility='private' WHERE id=?`, chat)
			execOrFatal(t, h.db, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES('attachment-consent',?,?,'user','Create document','2026-10-19T10:00:00Z',?)`, chat, agent, author)
			execOrFatal(t, h.db, `INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at) VALUES('attachment-consent',?,?,?,'attachment-consent','2026-10-19T10:00:00Z')`, workspace, author, chat)
			if tc.rejectAudit {
				execOrFatal(t, h.db, `CREATE TRIGGER attachment_reject_audit BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN SELECT RAISE(ABORT,'fixture audit unavailable'); END`)
			}
			reader := tc.reader
			if reader == "author" {
				reader = author
			}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agent+"/chats/"+chat+"/attachments", nil)
			request.SetPathValue("agentId", agent)
			request.SetPathValue("chatId", chat)
			request = withWorkspaceUser(request, reader, workspace, tc.role)
			ctx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
			defer cancel()
			request = request.WithContext(ctx)
			// Auditing inside an unclosed read transaction must not pass by
			// borrowing another connection. Audit must precede the HTTP success.
			h.db.SetMaxOpenConns(1)
			auditedBeforeBytes := false
			writer := &privateAttachmentAuditWriter{ResponseRecorder: httptest.NewRecorder(), before: func(status int) {
				if status != http.StatusOK {
					return
				}
				var count int
				if err := h.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_logs WHERE workspace_id=? AND user_id=? AND action='artifact.private_owner_accessed' AND entity_type='chat' AND entity_id=? AND json_extract(metadata,'$.owner_user_id')=? AND json_extract(metadata,'$.output_source')='conversation'`, workspace, reader, chat, author).Scan(&count); err != nil {
					t.Fatal(err)
				}
				auditedBeforeBytes = count > 0
			}}
			h.ListAgentChatAttachments(writer, request)
			if ctx.Err() != nil {
				t.Fatalf("attachment read/audit escaped the available connection: %v", ctx.Err())
			}
			if writer.Code != tc.want {
				t.Fatalf("attachment read status=%d want%d", writer.Code, tc.want)
			}
			if tc.want == http.StatusOK {
				if !strings.Contains(writer.Body.String(), filename) {
					t.Fatal("positive fixture did not return attachment metadata")
				}
				if reader != author && !auditedBeforeBytes {
					t.Error("foreign OWNER received protected attachment metadata before durable audit")
				}
			} else if strings.Contains(writer.Body.String(), filename) || strings.Contains(writer.Body.String(), "/attachments/") {
				t.Error("refused attachment read released protected metadata")
			}
		})
	}
}

type privateAttachmentAuditWriter struct {
	*httptest.ResponseRecorder
	before func(int)
	wrote  bool
}

func (w *privateAttachmentAuditWriter) WriteHeader(status int) {
	if !w.wrote {
		w.wrote = true
		w.before(status)
	}
	w.ResponseRecorder.WriteHeader(status)
}
func (w *privateAttachmentAuditWriter) Write(value []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseRecorder.Write(value)
}
