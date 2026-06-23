package api

// workspaces_membership.go coverage top-up #3 — the scan-error and
// write-failure forks that cov/cov2 left behind. License-limit branches
// (101-108 / 330-337) are NOT covered here: v0.1's CheckMemberLimit is
// a hard-coded nil return, so those forks are unreachable without
// changing production code.
//
// All tests are prefixed TestCov3WM.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cov3WMRig(t *testing.T) (*WorkspaceHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewWorkspaceHandler(db, newTestLogger()), db, userID, wsID
}

func cov3WMReq(method, target, body, wsID, role string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	return r.WithContext(withWorkspace(r.Context(), wsID, role))
}

func cov3WMTrigger(t *testing.T, db *sql.DB, name, body string) {
	t.Helper()
	if _, err := db.Exec(`CREATE TRIGGER ` + name + ` ` + body); err != nil {
		t.Fatalf("create trigger %s: %v", name, err)
	}
}

// --- ListMembers: scan error via lax schema ---

func TestCov3WMListMembers_ScanError500(t *testing.T) {
	h, db, userID, wsID := cov3WMRig(t)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE workspace_members`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE workspace_members (
		id TEXT PRIMARY KEY, workspace_id TEXT, user_id TEXT, role TEXT,
		created_at TEXT DEFAULT (datetime('now')), updated_at TEXT DEFAULT (datetime('now')))`); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	// NULL role fails the Scan into a plain string.
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role)
		VALUES ('wm-null', ?, ?, NULL)`, wsID, userID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ListMembers(rec, cov3WMReq("GET", "/x", "", wsID, "OWNER"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (NULL role scan), body=%s", rec.Code, rec.Body.String())
	}
}

// --- AddMember: insert blocked → 500 ---

func TestCov3WMAddMember_InsertBlocked500(t *testing.T) {
	h, db, _, wsID := cov3WMRig(t)
	// Second user who is not yet a member.
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name, hashed_password)
		VALUES ('u-new3', 'u-new3@example.com', 'New', 'x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	cov3WMTrigger(t, db, "cov3wm_add", `BEFORE INSERT ON workspace_members
		BEGIN SELECT RAISE(ABORT, 'blocked'); END`)

	rec := httptest.NewRecorder()
	h.AddMember(rec, cov3WMReq("POST", "/x", `{"user_id":"u-new3","role":"MEMBER"}`, wsID, "OWNER"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (insert blocked), body=%s", rec.Code, rec.Body.String())
	}
}

// --- RemoveMember: delete blocked → 500 ---

func TestCov3WMRemoveMember_DeleteBlocked500(t *testing.T) {
	h, db, _, wsID := cov3WMRig(t)
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name, hashed_password)
		VALUES ('u-rm3', 'u-rm3@example.com', 'Rm', 'x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role)
		VALUES ('wm-rm3', ?, 'u-rm3', 'MEMBER')`, wsID); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	cov3WMTrigger(t, db, "cov3wm_del", `BEFORE DELETE ON workspace_members
		BEGIN SELECT RAISE(ABORT, 'blocked'); END`)

	req := cov3WMReq("DELETE", "/x", "", wsID, "OWNER")
	req.SetPathValue("memberId", "wm-rm3")
	rec := httptest.NewRecorder()
	h.RemoveMember(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (delete blocked), body=%s", rec.Code, rec.Body.String())
	}
}

// --- ListInvitations: scan error via lax schema ---

func TestCov3WMListInvitations_ScanError500(t *testing.T) {
	h, db, userID, wsID := cov3WMRig(t)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE workspace_invitations`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE workspace_invitations (
		id TEXT PRIMARY KEY, workspace_id TEXT, email TEXT, role TEXT,
		invited_by TEXT, token TEXT, expires_at TEXT, accepted_at TEXT,
		created_at TEXT DEFAULT (datetime('now')))`); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_invitations (id, workspace_id, email, role, invited_by, token, expires_at)
		VALUES ('inv-null', ?, 'x@example.com', NULL, ?, 'tok3', '2099-01-01T00:00:00Z')`, wsID, userID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ListInvitations(rec, cov3WMReq("GET", "/x", "", wsID, "OWNER"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (NULL role scan), body=%s", rec.Code, rec.Body.String())
	}
}

// --- CreateInvitation: insert blocked → 500 ---

func TestCov3WMCreateInvitation_InsertBlocked500(t *testing.T) {
	h, db, userID, wsID := cov3WMRig(t)
	cov3WMTrigger(t, db, "cov3wm_inv", `BEFORE INSERT ON workspace_invitations
		BEGIN SELECT RAISE(ABORT, 'blocked'); END`)

	req := cov3WMReq("POST", "/x", `{"email":"new3@example.com"}`, wsID, "OWNER")
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rec := httptest.NewRecorder()
	h.CreateInvitation(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (insert blocked), body=%s", rec.Code, rec.Body.String())
	}
}

// --- AddMember: member-exists check error → 500 ---

func TestCov3WMAddMember_ExistsCheckError500(t *testing.T) {
	h, db, _, wsID := cov3WMRig(t)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE workspace_members`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	h.AddMember(rec, cov3WMReq("POST", "/x", `{"user_id":"whoever"}`, wsID, "OWNER"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (exists check), body=%s", rec.Code, rec.Body.String())
	}
}
