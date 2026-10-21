package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrivateConversationCardsAuditBeforeMetadata(t *testing.T) {
	for _, fault := range []bool{false, true} {
		t.Run(map[bool]string{false: "audit", true: "audit unavailable"}[fault], func(t *testing.T) {
			h, ws, author, owner, admin, saved := privateConversationAuditFixture(t)
			probe := privateConversationAuditProbe(t, h.db)
			h.db.SetMaxOpenConns(1) // Auditing inside the open catalog transaction would deadlock.
			if fault {
				execOrFatal(t, h.db, `CREATE TRIGGER reject_context_inspection BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'fixture audit unavailable'); END`)
			}
			for _, user := range []string{author, admin, owner} {
				req := documentResultsRequest(t, h, ws, user)
				response := httptest.NewRecorder()
				gate := &documentAuditGateWriter{ResponseRecorder: response, beforeHeader: func(code int) {
					if user == owner && code == 200 && privateConversationAuditCount(t, req.Context(), probe, ws, owner, author) == 0 {
						t.Error("OWNER saved-result title released before source-context audit")
					}
				}}
				h.ListChatDocumentResults(gate, req)
				if req.Context().Err() != nil {
					t.Fatalf("single-connection card read/audit stalled: %v", req.Context().Err())
				}
				disclosed := strings.Contains(response.Body.String(), saved.Title) || strings.Contains(response.Body.String(), saved.PageID)
				if user == admin || user == owner && fault {
					if disclosed {
						t.Errorf("protected card disclosed user=%s status=%d", user, response.Code)
					}
				} else if response.Code != 200 || !disclosed {
					t.Errorf("authorized card missing user=%s status=%d", user, response.Code)
				}
			}
		})
	}
}
