package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/harbormaster"
)

func TestCanonicalApprovalDenialRetainsCurrentSourceAudience(t *testing.T) {
	for _, scenario := range []string{"recreated-chat", "deleted-chat", "revoked-requester"} {
		t.Run(scenario, func(t *testing.T) {
			d := newApprovalDecisionRig(t)
			execOrFatal(t, d.f.h.db, `INSERT INTO users(id,email) VALUES('denial-admin','denial-admin@example.test')`)
			execOrFatal(t, d.f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('denial-admin-member',?,'denial-admin','ADMIN')`, d.f.workspace)
			d.a.UserID = "denial-admin"
			wantRead, wantDecision := http.StatusNotFound, http.StatusForbidden
			switch scenario {
			case "recreated-chat", "deleted-chat":
				execOrFatal(t, d.f.h.db, `DELETE FROM chats WHERE id=?`, d.f.chat)
				if scenario == "recreated-chat" {
					execOrFatal(t, d.f.h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,?,?,'private')`, d.f.chat, d.f.workspace, d.f.agent, d.f.user)
				}
			case "revoked-requester":
				execOrFatal(t, d.f.h.db, `UPDATE users SET suspended_at='2026-10-16' WHERE id=?`, d.f.user)
				wantRead, wantDecision = http.StatusOK, http.StatusOK
			}
			req := withWorkspaceUser(httptest.NewRequest("GET", "/", nil), d.a.UserID, d.f.workspace, "ADMIN")
			req.SetPathValue("id", d.id)
			rr := httptest.NewRecorder()
			NewApprovalsHandler(d.f.h.db, newTestLogger(), d.r.Journal()).Get(rr, req)
			if rr.Code != wantRead {
				t.Fatalf("read: got %d want %d: %s", rr.Code, wantRead, rr.Body.String())
			}
			if rr = d.inbox("denied"); rr.Code != wantDecision {
				t.Fatalf("denial: got %d want %d: %s", rr.Code, wantDecision, rr.Body.String())
			}
			row, err := harbormaster.Get(t.Context(), d.f.h.db, d.f.workspace, d.id)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := harbormaster.StatusPending
			if scenario == "revoked-requester" {
				wantStatus = harbormaster.StatusDenied
			}
			if row.Status != wantStatus {
				t.Fatalf("stored status: got %s want %s", row.Status, wantStatus)
			}
		})
	}
}
