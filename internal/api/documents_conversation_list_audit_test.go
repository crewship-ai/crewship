package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Shared fixture for these source-context inspection regressions. Creation uses
// the corrected PRIVATE source before admission. No model or deployed proof.
func privateConversationAuditFixture(t *testing.T) (*PageHandler, string, string, string, string, documentCreationResult) {
	t.Helper()
	h, workspace, author, saved := documentResultsFixture(t)
	const owner, admin = "context-audit-owner", "context-audit-admin"
	pagesSeedUser(t, h, workspace, owner, "context-owner@example.test", "OWNER")
	pagesSeedUser(t, h, workspace, admin, "context-admin@example.test", "ADMIN")
	execOrFatal(t, h.db, `UPDATE chats SET title=? WHERE id='principal-chat'`, saved.Title)
	return h, workspace, author, owner, admin, saved
}

// This fixture has never admitted private output; deleting consent is not an
// unseal operation after durable classification has been introduced.
func ordinaryConversationAuditFixture(t *testing.T) (*PageHandler, string, string, string, string) {
	t.Helper()
	f := newArtifactPrincipalFixture(t)
	h, workspace, author := f.pages, f.rig.wsID, f.rig.userID
	const owner, admin = "context-audit-owner", "context-audit-admin"
	pagesSeedUser(t, h, workspace, owner, "context-owner@example.test", "OWNER")
	pagesSeedUser(t, h, workspace, admin, "context-admin@example.test", "ADMIN")
	execOrFatal(t, h.db, `UPDATE chats SET visibility='private',title='late-private-conversation' WHERE id='principal-chat'`)
	return h, workspace, author, owner, admin
}
func privateConversationAuditProbe(t *testing.T, db *sql.DB) *sql.DB {
	t.Helper()
	probe, err := sql.Open("sqlite", "file:"+reviewDBPath(t, db))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = probe.Close() })
	return probe
}
func privateConversationAuditCount(t *testing.T, ctx context.Context, db *sql.DB, workspace, actor, author string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND entity_type='chat' AND entity_id='principal-chat' AND workspace_id=? AND user_id=? AND json_extract(metadata,'$.owner_user_id')=? AND json_extract(metadata,'$.output_source')='conversation'`, workspace, actor, author).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
func TestPrivateConversationListAuditBeforeMetadata(t *testing.T) {
	for _, fault := range []bool{false, true} {
		t.Run(map[bool]string{false: "audit", true: "audit unavailable"}[fault], func(t *testing.T) {
			h, ws, author, owner, admin, saved := privateConversationAuditFixture(t)
			if fault {
				execOrFatal(t, h.db, `CREATE TRIGGER reject_context_inspection BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'fixture audit unavailable'); END`)
			}
			handler := NewAgentHandler(h.db, newTestLogger())
			for _, tc := range []struct{ user, role string }{{author, "MEMBER"}, {admin, "ADMIN"}, {owner, "OWNER"}} {
				req := pagesRequest(t, "GET", "/api/v1/agents/principal-agent/chats", ws, tc.user, tc.role, "")
				req.SetPathValue("agentId", "principal-agent")
				response := httptest.NewRecorder()
				gate := &documentAuditGateWriter{ResponseRecorder: response, beforeHeader: func(code int) {
					if tc.user == owner && code == 200 && privateConversationAuditCount(t, req.Context(), h.db, ws, owner, author) == 0 {
						t.Error("OWNER list metadata released before source-context audit")
					}
				}}
				handler.ListChats(gate, req)
				disclosed := strings.Contains(response.Body.String(), saved.Title)
				if tc.user == admin || tc.user == owner && fault {
					if disclosed {
						t.Errorf("private list title disclosed to %s status=%d", tc.role, response.Code)
					}
				} else if response.Code != http.StatusOK || !disclosed {
					t.Errorf("authorized list missing: %s status=%d", tc.role, response.Code)
				}
			}
		})
	}
}
