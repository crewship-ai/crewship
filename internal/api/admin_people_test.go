package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/backup"
)

// Admin › Workspaces and Admin › Users: who sees what, what each row
// carries, and the per-person actions (sessions, sign-out, unlock).

type peopleFixture struct {
	db    *sql.DB
	store sessions.Store
}

// newPeopleFixture seeds two workspaces:
//
//	ws-a: owner@a (OWNER), admin@a (ADMIN), member@a (MEMBER)
//	ws-b: other@b (OWNER)
//
// plus loner@x, who belongs to no workspace.
func newPeopleFixture(t *testing.T) *peopleFixture {
	t.Helper()
	db := setupTestDB(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	for _, u := range [][2]string{{"u-owner", "owner@a.test"}, {"u-admin", "admin@a.test"}, {"u-member", "member@a.test"}, {"u-other", "other@b.test"}, {"u-loner", "loner@x.test"}} {
		exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`, u[0], u[1], u[0])
	}
	exec(`INSERT INTO workspaces (id, name, slug, preferred_language, run_retention_days, allow_privileged_credentials) VALUES ('ws-a', 'Alpha', 'alpha', 'cs', 30, 1)`)
	exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-b', 'Beta', 'beta')`)
	exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m1', 'ws-a', 'u-owner', 'OWNER'), ('m2', 'ws-a', 'u-admin', 'ADMIN'), ('m3', 'ws-a', 'u-member', 'MEMBER'), ('m4', 'ws-b', 'u-other', 'OWNER'), ('m5', 'ws-b', 'u-member', 'MEMBER')`)
	future := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	exec(`INSERT INTO workspace_invitations (id, workspace_id, email, role, invited_by, token, expires_at) VALUES
		('inv1', 'ws-a', 'new@a.test', 'MEMBER', 'u-owner', 't1', ?),
		('inv2', 'ws-a', 'old@a.test', 'MEMBER', 'u-owner', 't2', ?)`, future, past)
	now := time.Now().UTC()
	for i, tr := range []string{"tr1", "tr2", "tr3"} {
		exec(`INSERT INTO journal_entries (id, workspace_id, entry_type, severity, actor_type, summary, trace_id, ts)
			VALUES (?, 'ws-a', 'run.started', 'info', 'system', 'run', ?, ?)`, "je"+tr, tr, now.Add(-time.Duration(i)*time.Minute).Format(time.RFC3339Nano))
	}
	// A second journal row on the same trace must not count twice, and a
	// run older than a week not at all.
	exec(`INSERT INTO journal_entries (id, workspace_id, entry_type, severity, actor_type, summary, trace_id, ts)
		VALUES ('je-dup', 'ws-a', 'run.started', 'info', 'system', 'run', 'tr1', ?), ('je-old', 'ws-a', 'run.started', 'info', 'system', 'run', 'tr-old', ?)`,
		now.Format(time.RFC3339Nano), now.Add(-10*24*time.Hour).Format(time.RFC3339Nano))
	exec(`UPDATE users SET failed_login_count = 5, locked_until = ? WHERE id = 'u-member'`, future)
	exec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, scopes) VALUES ('tok1', 'u-member', 'laptop', 'h1', '["agents:read"]'), ('tok2', 'u-member', 'gone', 'h2', NULL)`)
	exec(`UPDATE cli_tokens SET revoked_at = ? WHERE id = 'tok2'`, past)
	store := sessions.NewDBStore(db)
	for i := 0; i < 2; i++ {
		if _, err := store.Create(context.Background(), "u-member", "Chrome on macOS", "10.0.0.1", time.Hour); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}
	return &peopleFixture{db: db, store: store}
}

func peopleReq(method, path, userID, email, wsID, role string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	ctx := withUser(req.Context(), &AuthUser{ID: userID, Email: email})
	return req.WithContext(withWorkspace(ctx, wsID, role))
}

func TestAdminPeople_ListWorkspaces_Scope(t *testing.T) {
	f := newPeopleFixture(t)
	h := NewAdminHandler(f.db, newTestLogger())

	cases := []struct {
		name      string
		owner     string
		wantScope string
		wantIDs   int
	}{
		{"workspace admin sees only the current workspace", "", adminScopeWS, 1},
		{"instance owner sees every workspace", "admin@a.test", adminScopeInstance, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(backup.InstanceOwnerEmailEnv, tc.owner)
			rr := httptest.NewRecorder()
			h.ListWorkspaces(rr, peopleReq("GET", "/api/v1/admin/workspaces", "u-admin", "admin@a.test", "ws-a", "ADMIN"))
			if rr.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get(adminScopeHeader); got != tc.wantScope {
				t.Errorf("scope header = %q, want %q", got, tc.wantScope)
			}
			var out []map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if len(out) != tc.wantIDs {
				t.Fatalf("rows = %d, want %d", len(out), tc.wantIDs)
			}
		})
	}
}

func TestAdminPeople_ListWorkspaces_Fields(t *testing.T) {
	f := newPeopleFixture(t)
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	rr := httptest.NewRecorder()
	NewAdminHandler(f.db, newTestLogger()).ListWorkspaces(rr, peopleReq("GET", "/", "u-admin", "admin@a.test", "ws-a", "ADMIN"))
	var out []struct {
		PreferredLanguage          *string `json:"preferred_language"`
		RunRetentionDays           *int    `json:"run_retention_days"`
		AllowPrivilegedCredentials bool    `json:"allow_privileged_credentials"`
		PendingInvitations         int     `json:"pending_invitations"`
		Runs7d                     int     `json:"runs_7d"`
		RunsByDay                  []int   `json:"runs_by_day"`
		Cost30d                    float64 `json:"cost_30d_usd"`
		Current                    bool    `json:"current"`
		Members                    int     `json:"_count_members"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || len(out) != 1 {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	w := out[0]
	if w.PreferredLanguage == nil || *w.PreferredLanguage != "cs" || w.RunRetentionDays == nil || *w.RunRetentionDays != 30 || !w.AllowPrivilegedCredentials {
		t.Errorf("settings not carried: %+v", w)
	}
	if w.PendingInvitations != 1 {
		t.Errorf("pending_invitations = %d, want 1 (the expired one does not count)", w.PendingInvitations)
	}
	if w.Runs7d != 3 || len(w.RunsByDay) != 7 {
		t.Errorf("runs = %d by day %v, want 3 distinct traces in the week", w.Runs7d, w.RunsByDay)
	}
	sum := 0
	for _, n := range w.RunsByDay {
		sum += n
	}
	if sum != w.Runs7d {
		t.Errorf("runs_by_day sums to %d, runs_7d is %d", sum, w.Runs7d)
	}
	if !w.Current || w.Members != 3 {
		t.Errorf("current=%v members=%d", w.Current, w.Members)
	}
}

func TestAdminPeople_ListUsers(t *testing.T) {
	f := newPeopleFixture(t)
	h := NewAdminHandler(f.db, newTestLogger())

	type user struct {
		ID          string `json:"id"`
		Role        *string
		Memberships []struct {
			MemberID    string `json:"member_id"`
			WorkspaceID string `json:"workspace_id"`
			Role        string `json:"role"`
		} `json:"memberships"`
		ActiveSessions   int     `json:"active_sessions"`
		CLITokens        int     `json:"cli_tokens"`
		LockedUntil      *string `json:"locked_until"`
		FailedLoginCount int     `json:"failed_login_count"`
		LastActiveAt     *string `json:"last_active_at"`
	}
	list := func(t *testing.T, owner string) (map[string]user, string) {
		t.Helper()
		t.Setenv(backup.InstanceOwnerEmailEnv, owner)
		rr := httptest.NewRecorder()
		h.ListUsers(rr, peopleReq("GET", "/", "u-admin", "admin@a.test", "ws-a", "ADMIN"))
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		var out []user
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		m := map[string]user{}
		for _, u := range out {
			m[u.ID] = u
		}
		return m, rr.Header().Get(adminScopeHeader)
	}

	t.Run("workspace scope: members only, memberships only here", func(t *testing.T) {
		m, scope := list(t, "")
		if scope != adminScopeWS || len(m) != 3 {
			t.Fatalf("scope %q, %d users", scope, len(m))
		}
		mem := m["u-member"]
		if len(mem.Memberships) != 1 || mem.Memberships[0].WorkspaceID != "ws-a" || mem.Memberships[0].MemberID != "m3" {
			t.Errorf("memberships leak other workspaces: %+v", mem.Memberships)
		}
		if mem.ActiveSessions != 2 || mem.CLITokens != 1 || mem.LockedUntil == nil || mem.FailedLoginCount != 5 || mem.LastActiveAt == nil {
			t.Errorf("activity fields: %+v", mem)
		}
		if m["u-owner"].LockedUntil != nil {
			t.Error("an unlocked account reports a lock")
		}
	})

	t.Run("instance scope: everyone, every membership", func(t *testing.T) {
		m, scope := list(t, "admin@a.test")
		if scope != adminScopeInstance || len(m) != 5 {
			t.Fatalf("scope %q, %d users", scope, len(m))
		}
		if got := len(m["u-member"].Memberships); got != 2 {
			t.Errorf("u-member memberships = %d, want 2", got)
		}
		if l := m["u-loner"]; len(l.Memberships) != 0 || l.Role != nil {
			t.Errorf("a person in no workspace: %+v", l)
		}
		if r := m["u-member"].Role; r == nil || *r != "MEMBER" {
			t.Errorf("role keeps meaning the current workspace's: %v", r)
		}
	})
}

func TestAdminPeople_Actions_Authz(t *testing.T) {
	cases := []struct {
		name   string
		caller string
		email  string
		role   string
		owner  string
		target string
		want   int
	}{
		{"admin reads a member", "u-admin", "admin@a.test", "ADMIN", "", "u-member", http.StatusOK},
		{"member outside the workspace is not found", "u-admin", "admin@a.test", "ADMIN", "", "u-other", http.StatusNotFound},
		{"unknown id is not found either", "u-admin", "admin@a.test", "ADMIN", "", "nobody", http.StatusNotFound},
		{"admin may not act on the owner", "u-admin", "admin@a.test", "ADMIN", "", "u-owner", http.StatusForbidden},
		{"owner may act on the owner", "u-owner", "owner@a.test", "OWNER", "", "u-owner", http.StatusOK},
		{"instance owner reaches another workspace", "u-admin", "admin@a.test", "ADMIN", "admin@a.test", "u-other", http.StatusOK},
		{"manager is refused", "u-member", "member@a.test", "MANAGER", "", "u-admin", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPeopleFixture(t)
			t.Setenv(backup.InstanceOwnerEmailEnv, tc.owner)
			h := NewAdminUsersHandler(f.db, newTestLogger(), f.store)
			req := peopleReq("GET", "/", tc.caller, tc.email, "ws-a", tc.role)
			req.SetPathValue("userId", tc.target)
			rr := httptest.NewRecorder()
			h.Sessions(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestAdminPeople_SessionsAndRevoke(t *testing.T) {
	f := newPeopleFixture(t)
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	h := NewAdminUsersHandler(f.db, newTestLogger(), f.store)
	call := func(fn http.HandlerFunc, method string, pv map[string]string) *httptest.ResponseRecorder {
		req := peopleReq(method, "/", "u-admin", "admin@a.test", "ws-a", "ADMIN")
		for k, v := range pv {
			req.SetPathValue(k, v)
		}
		rr := httptest.NewRecorder()
		fn(rr, req)
		return rr
	}

	rr := call(h.Sessions, "GET", map[string]string{"userId": "u-member"})
	var out struct {
		Sessions []struct {
			ID        string `json:"id"`
			IP        string `json:"ip"`
			UserAgent string `json:"user_agent"`
		} `json:"sessions"`
		CLITokens []struct {
			Name   string   `json:"name"`
			Scopes []string `json:"scopes"`
		} `json:"cli_tokens"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sessions) != 2 || out.Sessions[0].IP != "10.0.0.1" || out.Sessions[0].UserAgent == "" {
		t.Fatalf("sessions: %+v", out.Sessions)
	}
	if len(out.CLITokens) != 1 || out.CLITokens[0].Name != "laptop" || len(out.CLITokens[0].Scopes) != 1 {
		t.Fatalf("cli tokens (revoked one must not show): %+v", out.CLITokens)
	}

	// Another person's session id is "not found", never revoked.
	other, err := f.store.Create(context.Background(), "u-owner", "x", "y", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if rr := call(h.RevokeSession, "POST", map[string]string{"userId": "u-member", "sessionId": other.ID}); rr.Code != http.StatusNotFound {
		t.Fatalf("cross-person revoke = %d, want 404", rr.Code)
	}

	if rr := call(h.RevokeSession, "POST", map[string]string{"userId": "u-member", "sessionId": out.Sessions[0].ID}); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d: %s", rr.Code, rr.Body.String())
	}
	var reason string
	_ = f.db.QueryRow(`SELECT revoked_reason FROM user_sessions WHERE id = ?`, out.Sessions[0].ID).Scan(&reason)
	if reason != reasonAdminRevoke {
		t.Errorf("revoked_reason = %q", reason)
	}

	rr = call(h.RevokeAllSessions, "POST", map[string]string{"userId": "u-member"})
	var all struct {
		Revoked int `json:"revoked"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &all)
	if rr.Code != http.StatusOK || all.Revoked != 1 {
		t.Fatalf("revoke-all = %d %s", rr.Code, rr.Body.String())
	}

	var audits int
	_ = f.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE entity_type = 'USER' AND entity_id = 'u-member' AND action IN ('user.session_revoked', 'user.sessions_revoked')`).Scan(&audits)
	if audits != 2 {
		t.Errorf("audit rows = %d, want 2", audits)
	}
}

func TestAdminPeople_Unlock(t *testing.T) {
	f := newPeopleFixture(t)
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	h := NewAdminUsersHandler(f.db, newTestLogger(), f.store)
	req := peopleReq("POST", "/", "u-admin", "admin@a.test", "ws-a", "ADMIN")
	req.SetPathValue("userId", "u-member")
	rr := httptest.NewRecorder()
	h.Unlock(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("unlock = %d: %s", rr.Code, rr.Body.String())
	}
	var locked sql.NullString
	var failed int
	_ = f.db.QueryRow(`SELECT locked_until, failed_login_count FROM users WHERE id = 'u-member'`).Scan(&locked, &failed)
	if locked.Valid || failed != 0 {
		t.Errorf("after unlock: locked_until=%v failed=%d", locked, failed)
	}
	var audits int
	_ = f.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'user.unlocked' AND entity_id = 'u-member'`).Scan(&audits)
	if audits != 1 {
		t.Errorf("audit rows = %d", audits)
	}
}
