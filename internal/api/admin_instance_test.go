package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// instanceFixture: two workspaces. "boss" owns the oldest one and so is named
// instance admin by the one-time bootstrap on first use; "wsadmin" is ADMIN of it and nothing more;
// "carol" owns the second one and is not a member of the first.
type instanceFixture struct {
	t       *testing.T
	db      *sql.DB
	r       *Router
	boss    string // token
	wsAdmin string // token
}

func newInstanceFixture(t *testing.T) *instanceFixture {
	t.Helper()
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	// Backups run whatever the test machine's disk has left; the space
	// floor has its own tests (internal/backupplan).
	t.Setenv("CREWSHIP_BACKUP_MIN_FREE_PERCENT", "0")
	db := setupTestDB(t)
	seedInstanceWorkspace(t, db, "ws-old", "2026-01-01 00:00:00")
	seedInstanceWorkspace(t, db, "ws-new", "2026-06-01 00:00:00")
	seedInstanceUser(t, db, "boss", "boss@ex.com", "ws-old", "OWNER")
	seedInstanceUser(t, db, "wsadmin", "wsadmin@ex.com", "ws-old", "ADMIN")
	seedInstanceUser(t, db, "carol", "carol@ex.com", "ws-new", "OWNER")
	// The one-time bootstrap names boss, as it would have long before any of
	// these tests' requests on a real install.
	if _, err := ensureInstanceAdminBootstrap(context.Background(), db); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return &instanceFixture{
		t: t, db: db, r: r,
		boss:    mintTokenFor(t, db, "boss", "instfixboss0000000000000000"),
		wsAdmin: mintTokenFor(t, db, "wsadmin", "instfixwsadmin0000000000000"),
	}
}

func (f *instanceFixture) do(tok, method, path, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	f.r.ServeHTTP(rr, req)
	return rr
}

func (f *instanceFixture) role(ws, user string) string {
	f.t.Helper()
	var role string
	err := f.db.QueryRow(`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, ws, user).Scan(&role)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		f.t.Fatalf("role: %v", err)
	}
	return role
}

func (f *instanceFixture) audited(action string) bool {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action = ?`, action).Scan(&n); err != nil {
		f.t.Fatalf("audit: %v", err)
	}
	return n > 0
}

func wantCode(t *testing.T, rr *httptest.ResponseRecorder, want int, what string) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("%s = %d, want %d: %s", what, rr.Code, want, rr.Body.String())
	}
}

// A workspace ADMIN is not an instance admin: every instance route refuses
// them, with no workspace in the request to hide behind.
func TestInstanceRoutes_RefuseAWorkspaceAdmin(t *testing.T) {
	f := newInstanceFixture(t)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/admin/instance/people", `{"email":"x@ex.com"}`},
		{"PUT", "/api/v1/admin/instance/workspaces/ws-new/members/wsadmin", `{"role":"OWNER"}`},
		{"DELETE", "/api/v1/admin/instance/workspaces/ws-new/members/carol", ""},
		{"POST", "/api/v1/admin/instance/workspaces", `{"name":"X1","slug":"x1","owner_user_id":"wsadmin"}`},
		{"POST", "/api/v1/admin/instance/workspaces/ws-new/transfer-ownership", `{"user_id":"wsadmin"}`},
		{"DELETE", "/api/v1/admin/instance/workspaces/ws-new", `{"confirm_slug":"ws-new"}`},
		{"POST", "/api/v1/admin/instance/people/carol/suspend", ""},
		{"PUT", "/api/v1/admin/instance/admins/wsadmin", ""},
		{"GET", "/api/v1/admin/instance/audit", ""},
	} {
		if rr := f.do(f.wsAdmin, c.method, c.path, c.body); rr.Code != http.StatusForbidden {
			t.Errorf("workspace ADMIN %s %s = %d, want 403", c.method, c.path, rr.Code)
		}
	}
	if f.role("ws-new", "wsadmin") != "" {
		t.Fatal("a refused call changed a membership")
	}
}

func TestInstanceCreatePerson(t *testing.T) {
	f := newInstanceFixture(t)
	rr := f.do(f.boss, "POST", "/api/v1/admin/instance/people",
		`{"email":"New@Ex.com","full_name":"Nova","memberships":[{"workspace_id":"ws-old","role":"member"},{"workspace_id":"ws-new","role":"ADMIN"}]}`)
	wantCode(t, rr, http.StatusCreated, "create person")
	var body createPersonResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body.Email != "new@ex.com" || !strings.Contains(body.SetupURL, "/reset-password?token=") || body.ExpiresAt == "" {
		t.Errorf("response = %+v, want the lower-cased email and a setup link", body)
	}
	if f.role("ws-old", body.UserID) != "MEMBER" || f.role("ws-new", body.UserID) != "ADMIN" {
		t.Error("the new person did not land in both workspaces with their roles")
	}
	if !f.audited("instance.user_created") {
		t.Error("no instance audit row")
	}

	// The same address again is a membership change, never a second account.
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/people", `{"email":"new@ex.com"}`), http.StatusConflict, "duplicate")
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/people", `{"email":"z@ex.com","memberships":[{"workspace_id":"nope","role":"MEMBER"}]}`), http.StatusNotFound, "unknown workspace")
	var n int
	_ = f.db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = 'z@ex.com'`).Scan(&n)
	if n != 0 {
		t.Error("a refused create left an account behind")
	}
}

// Access anywhere, including a workspace the admin is not in, and the one
// thing the workspace ladder cannot do: make an owner.
func TestInstanceMemberships(t *testing.T) {
	f := newInstanceFixture(t)
	path := "/api/v1/admin/instance/workspaces/ws-new/members/wsadmin"
	wantCode(t, f.do(f.boss, "PUT", path, `{"role":"MANAGER"}`), http.StatusCreated, "add")
	if f.role("ws-new", "wsadmin") != "MANAGER" || !f.audited("instance.member_added") {
		t.Fatal("add did not land or was not audited")
	}
	wantCode(t, f.do(f.boss, "PUT", path, `{"role":"OWNER"}`), http.StatusOK, "promote to owner")
	if f.role("ws-new", "wsadmin") != "OWNER" {
		t.Fatal("promote to OWNER did not land")
	}
	wantCode(t, f.do(f.boss, "PUT", path, `{"role":"GOD"}`), http.StatusBadRequest, "bad role")

	// carol is now one of two owners, so she may step down; wsadmin then may not.
	wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/workspaces/ws-new/members/carol", `{"role":"ADMIN"}`), http.StatusOK, "demote a second owner")
	wantCode(t, f.do(f.boss, "PUT", path, `{"role":"MEMBER"}`), http.StatusConflict, "demote the last owner")
	wantCode(t, f.do(f.boss, "DELETE", path, ""), http.StatusConflict, "remove the last owner")
	if f.role("ws-new", "wsadmin") != "OWNER" {
		t.Fatal("a refused change moved the last owner")
	}

	wantCode(t, f.do(f.boss, "DELETE", "/api/v1/admin/instance/workspaces/ws-new/members/carol", ""), http.StatusNoContent, "remove")
	if f.role("ws-new", "carol") != "" || !f.audited("instance.member_removed") {
		t.Fatal("remove did not land or was not audited")
	}
	wantCode(t, f.do(f.boss, "DELETE", "/api/v1/admin/instance/workspaces/ws-new/members/carol", ""), http.StatusNotFound, "remove again")
	wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/workspaces/ws-new/members/ghost", `{"role":"MEMBER"}`), http.StatusNotFound, "unknown person")
}

func TestInstanceWorkspaces(t *testing.T) {
	f := newInstanceFixture(t)
	rr := f.do(f.boss, "POST", "/api/v1/admin/instance/workspaces", `{"name":"Lab","slug":"lab","owner_user_id":"carol"}`)
	wantCode(t, rr, http.StatusCreated, "create workspace")
	var ws struct{ ID string }
	_ = json.Unmarshal(rr.Body.Bytes(), &ws)
	if f.role(ws.ID, "carol") != "OWNER" || f.role(ws.ID, "boss") != "" {
		t.Fatal("the named owner must own it and the admin must not be added")
	}
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/workspaces", `{"name":"Lab","slug":"lab","owner_user_id":"carol"}`), http.StatusConflict, "slug taken")
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/workspaces", `{"name":"L2","slug":"l2","owner_user_id":"ghost"}`), http.StatusBadRequest, "unknown owner")

	// Hand-over: the new owner must be a member; the old one becomes ADMIN.
	tr := "/api/v1/admin/instance/workspaces/" + ws.ID + "/transfer-ownership"
	wantCode(t, f.do(f.boss, "POST", tr, `{"user_id":"wsadmin"}`), http.StatusConflict, "transfer to a non-member")
	wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/workspaces/"+ws.ID+"/members/wsadmin", `{"role":"MEMBER"}`), http.StatusCreated, "add member")
	wantCode(t, f.do(f.boss, "POST", tr, `{"user_id":"wsadmin"}`), http.StatusOK, "transfer")
	if f.role(ws.ID, "wsadmin") != "OWNER" || f.role(ws.ID, "carol") != "ADMIN" {
		t.Fatalf("after transfer: wsadmin=%s carol=%s, want OWNER/ADMIN", f.role(ws.ID, "wsadmin"), f.role(ws.ID, "carol"))
	}

	// Delete: the slug confirms it, and the audit row outlives the workspace.
	del := "/api/v1/admin/instance/workspaces/" + ws.ID
	wantCode(t, f.do(f.boss, "DELETE", del, `{"confirm_slug":"nope"}`), http.StatusBadRequest, "wrong slug")
	wantCode(t, f.do(f.boss, "DELETE", del, `{"confirm_slug":"lab"}`), http.StatusNoContent, "delete")
	var deleted sql.NullString
	_ = f.db.QueryRow(`SELECT deleted_at FROM workspaces WHERE id = ?`, ws.ID).Scan(&deleted)
	if !deleted.Valid || f.role(ws.ID, "wsadmin") != "" {
		t.Fatal("workspace not deleted, or memberships left behind")
	}
	var target string
	_ = f.db.QueryRow(`SELECT target_workspace_id FROM instance_audit_logs WHERE action = 'instance.workspace_deleted'`).Scan(&target)
	if target != ws.ID {
		t.Errorf("deletion audit row names %q, want %q", target, ws.ID)
	}
	wantCode(t, f.do(f.boss, "DELETE", del, `{"confirm_slug":"lab"}`), http.StatusNotFound, "delete again")
}

// A suspended account is shut out everywhere at once, and comes back intact.
func TestInstanceSuspend(t *testing.T) {
	f := newInstanceFixture(t)
	carolTok := mintTokenFor(t, f.db, "carol", "instfixcarol000000000000000")
	if rr := f.do(carolTok, "GET", "/api/v1/workspaces", ""); rr.Code != http.StatusOK {
		t.Fatalf("carol before suspension = %d", rr.Code)
	}
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/people/carol/suspend", `{"reason":"left the company"}`), http.StatusOK, "suspend")
	if rr := f.do(carolTok, "GET", "/api/v1/workspaces", ""); rr.Code != http.StatusUnauthorized {
		t.Errorf("carol's CLI token after suspension = %d, want 401", rr.Code)
	}
	if !accountSuspended(t.Context(), f.db, "carol") || f.role("ws-new", "carol") != "OWNER" {
		t.Error("suspension must keep memberships")
	}
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/people/boss/suspend", ""), http.StatusConflict, "suspend yourself")

	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/people/carol/reactivate", ""), http.StatusNoContent, "reactivate")
	if accountSuspended(t.Context(), f.db, "carol") {
		t.Error("still suspended")
	}
	if !f.audited("instance.user_suspended") || !f.audited("instance.user_reactivated") {
		t.Error("suspend/reactivate not audited")
	}
}

func TestInstanceSetupLinks(t *testing.T) {
	f := newInstanceFixture(t)
	rr := f.do(f.boss, "POST", "/api/v1/admin/instance/people", `{"email":"pending@ex.com"}`)
	wantCode(t, rr, http.StatusCreated, "create")
	var p createPersonResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &p)

	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/people/"+p.UserID+"/setup-link", "")
	wantCode(t, rr, http.StatusOK, "reissue")
	if !strings.Contains(rr.Body.String(), "setup_url") || strings.Contains(rr.Body.String(), p.SetupURL) {
		t.Error("reissue must return a new link, not the old one")
	}
	wantCode(t, f.do(f.boss, "DELETE", "/api/v1/admin/instance/people/"+p.UserID+"/setup-link", ""), http.StatusNoContent, "revoke")
	wantCode(t, f.do(f.boss, "DELETE", "/api/v1/admin/instance/people/"+p.UserID+"/setup-link", ""), http.StatusNotFound, "revoke again")

	// Somebody controls this account: a setup token for it is a takeover.
	mustExec(t, f.db, `UPDATE users SET hashed_password = 'x' WHERE id = 'carol'`)
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/people/carol/setup-link", ""), http.StatusConflict, "claimed account")
}

// Naming admins: the fallback owner names someone, the list becomes the whole
// truth, and nobody removes themselves.
func TestInstanceAdmins(t *testing.T) {
	f := newInstanceFixture(t)
	wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/admins/boss", ""), http.StatusNoContent, "name yourself")
	wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/admins/wsadmin", ""), http.StatusNoContent, "name wsadmin")
	if rr := f.do(f.wsAdmin, "GET", "/api/v1/admin/instance/audit", ""); rr.Code != http.StatusOK {
		t.Fatalf("a named admin GET audit = %d", rr.Code)
	}
	wantCode(t, f.do(f.boss, "DELETE", "/api/v1/admin/instance/admins/boss", ""), http.StatusConflict, "remove yourself")
	wantCode(t, f.do(f.wsAdmin, "DELETE", "/api/v1/admin/instance/admins/boss", ""), http.StatusNoContent, "another admin removes boss")
	// boss is still OWNER of the oldest workspace, but someone is named now,
	// so the fallback no longer makes boss an admin.
	if rr := f.do(f.boss, "GET", "/api/v1/admin/instance/audit", ""); rr.Code != http.StatusForbidden {
		t.Errorf("removed admin GET audit = %d, want 403", rr.Code)
	}
	wantCode(t, f.do(f.wsAdmin, "DELETE", "/api/v1/admin/instance/admins/carol", ""), http.StatusNotFound, "remove a non-admin")
}

// An instance admin runs the console wherever they stand: the lists answer
// for the whole instance and say who administers it.
func TestInstanceAdminSeesTheWholeInstance(t *testing.T) {
	f := newInstanceFixture(t)
	req := func(tok, path string) *httptest.ResponseRecorder {
		return f.do(tok, "GET", path+"?workspace_id=ws-old", "")
	}
	rr := req(f.boss, "/api/v1/admin/users")
	wantCode(t, rr, http.StatusOK, "boss lists users")
	if rr.Header().Get(adminScopeHeader) != adminScopeInstance {
		t.Errorf("scope = %q, want instance", rr.Header().Get(adminScopeHeader))
	}
	var users []struct {
		ID                  string
		InstanceAdmin       bool    `json:"instance_admin"`
		InstanceAdminSource *string `json:"instance_admin_source"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &users)
	seen := map[string]bool{}
	for _, u := range users {
		seen[u.ID] = u.InstanceAdmin
		// boss owns the oldest workspace and was named by the one-time
		// bootstrap the first time anyone asked (instance_admin.go).
		if u.ID == "boss" && (u.InstanceAdminSource == nil || *u.InstanceAdminSource != instanceAdminSourceRole) {
			t.Errorf("boss source = %v, want role (named by the bootstrap)", u.InstanceAdminSource)
		}
	}
	if len(users) != 3 || !seen["boss"] || seen["carol"] || seen["wsadmin"] {
		t.Errorf("users = %+v, want all three with only boss an instance admin", users)
	}

	rr = req(f.wsAdmin, "/api/v1/admin/workspaces")
	wantCode(t, rr, http.StatusOK, "workspace admin lists workspaces")
	if rr.Header().Get(adminScopeHeader) != adminScopeWS || strings.Contains(rr.Body.String(), "ws-new") {
		t.Error("a workspace admin must see only their workspace")
	}
	rr = req(f.boss, "/api/v1/admin/workspaces")
	if !strings.Contains(rr.Body.String(), `"owners":[{"id":"carol"`) {
		t.Errorf("workspace list must name owners: %s", rr.Body.String())
	}
}
