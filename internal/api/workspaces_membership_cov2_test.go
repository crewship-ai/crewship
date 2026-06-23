package api

// Second coverage pass for workspaces_membership.go — empty member list,
// license no-op gates, RemoveMember guard, and DB error branches reached
// by dropping the table queried mid-flow.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/license"
)

func covWM2Rig(t *testing.T) (*WorkspaceHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewWorkspaceHandler(db, newTestLogger()), db, userID, wsID
}

func covWM2Req(userID, wsID, role, method, body string) *http.Request {
	req := httptest.NewRequest(method, "/x", strings.NewReader(body))
	return withWorkspaceUser(req, userID, wsID, role)
}

func TestCovWM2ListMembers_Empty(t *testing.T) {
	h, db, userID, _ := covWM2Rig(t)
	// Second workspace without any member rows.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-empty', 'E', 'e')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ListMembers(rec, covWM2Req(userID, "ws-empty", "OWNER", "GET", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("body = %q, want []", rec.Body.String())
	}
}

func TestCovWM2AddMember_LicenseGateAndHappyPath(t *testing.T) {
	h, db, userID, wsID := covWM2Rig(t)
	h.SetLicense(&license.License{}) // v0.1 no-op — exercises the gate
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('u-add', 'a@x', 'A')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	rec := httptest.NewRecorder()
	h.AddMember(rec, covWM2Req(userID, wsID, "OWNER", "POST", `{"user_id":"u-add"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var role string
	if err := db.QueryRow(`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = 'u-add'`, wsID).Scan(&role); err != nil {
		t.Fatalf("query: %v", err)
	}
	if role != "MEMBER" {
		t.Errorf("role = %q, want default MEMBER", role)
	}
}

func TestCovWM2AddMember_UserCheckError500(t *testing.T) {
	h, db, userID, wsID := covWM2Rig(t)
	// Member check (workspace_members) passes; users lookup fails.
	if _, err := db.Exec(`DROP TABLE users`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	h.AddMember(rec, covWM2Req(userID, wsID, "OWNER", "POST", `{"user_id":"ghost"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestCovWM2RemoveMember_MissingID400(t *testing.T) {
	h, _, userID, wsID := covWM2Rig(t)
	rec := httptest.NewRecorder()
	h.RemoveMember(rec, covWM2Req(userID, wsID, "OWNER", "DELETE", ""))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestCovWM2CreateInvitation_LicenseGate(t *testing.T) {
	h, db, userID, wsID := covWM2Rig(t)
	h.SetLicense(&license.License{})
	rec := httptest.NewRecorder()
	req := covWM2Req(userID, wsID, "OWNER", "POST", `{"email":"new@x"}`)
	h.CreateInvitation(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_invitations WHERE workspace_id = ? AND email = 'new@x'`, wsID).Scan(&n); err != nil || n != 1 {
		t.Errorf("invitation row missing (n=%d err=%v)", n, err)
	}
}

func TestCovWM2CreateInvitation_InviteCheckError500(t *testing.T) {
	h, db, userID, wsID := covWM2Rig(t)
	// Member-by-email check passes (workspace_members + users intact);
	// the duplicate-invite check fails.
	if _, err := db.Exec(`DROP TABLE workspace_invitations`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	h.CreateInvitation(rec, covWM2Req(userID, wsID, "OWNER", "POST", `{"email":"x@y"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
