package api

import (
	"net/http"
	"testing"
)

// Every instance change and its audit entry land together or not at all. An
// instance admin who can grant themselves (or anyone) power with no trace is
// the one thing the instance audit exists to rule out, so a failed audit write
// has to undo the change and answer 500 — not 204 with nothing recorded.
func TestInstanceMutationsRollBackWhenTheAuditCannotBeWritten(t *testing.T) {
	scalar := func(f *instanceFixture, q string, args ...any) string {
		f.t.Helper()
		var s string
		if err := f.db.QueryRow(q, args...).Scan(&s); err != nil {
			return "<none>"
		}
		return s
	}
	cases := []struct {
		name         string
		setup        func(f *instanceFixture)
		method, path string
		body         string
		state        func(f *instanceFixture) string
	}{
		{name: "grant admin", method: "PUT", path: "/api/v1/admin/instance/admins/carol",
			state: func(f *instanceFixture) string {
				return scalar(f, `SELECT COALESCE(instance_role,'') FROM users WHERE id='carol'`)
			}},
		{name: "revoke admin",
			setup: func(f *instanceFixture) {
				mustExec(f.t, f.db, `UPDATE users SET instance_role='ADMIN' WHERE id IN ('boss','wsadmin')`)
			},
			method: "DELETE", path: "/api/v1/admin/instance/admins/wsadmin",
			state: func(f *instanceFixture) string {
				return scalar(f, `SELECT COALESCE(instance_role,'') FROM users WHERE id='wsadmin'`)
			}},
		{name: "suspend", method: "POST", path: "/api/v1/admin/instance/people/carol/suspend",
			state: func(f *instanceFixture) string {
				return scalar(f, `SELECT COALESCE(suspended_at,'') FROM users WHERE id='carol'`) + "|" +
					scalar(f, `SELECT COUNT(*) FROM cli_tokens WHERE user_id='carol' AND revoked_at IS NULL`)
			}},
		{name: "reactivate",
			setup: func(f *instanceFixture) {
				mustExec(f.t, f.db, `UPDATE users SET suspended_at='2026-09-01' WHERE id='carol'`)
			},
			method: "POST", path: "/api/v1/admin/instance/people/carol/reactivate",
			state: func(f *instanceFixture) string {
				return scalar(f, `SELECT COALESCE(suspended_at,'') FROM users WHERE id='carol'`)
			}},
		{name: "create person", method: "POST", path: "/api/v1/admin/instance/people", body: `{"email":"new@ex.com","memberships":[{"workspace_id":"ws-new","role":"MEMBER"}]}`,
			state: func(f *instanceFixture) string {
				return scalar(f, `SELECT COUNT(*) FROM users WHERE email='new@ex.com'`)
			}},
		{name: "issue setup link", method: "POST", path: "/api/v1/admin/instance/people/carol/setup-link",
			state: func(f *instanceFixture) string {
				return scalar(f, `SELECT COUNT(*) FROM verification_tokens WHERE identifier='carol@ex.com'`)
			}},
		{name: "revoke setup link",
			setup: func(f *instanceFixture) {
				mustExec(f.t, f.db, `INSERT INTO verification_tokens (identifier, token, expires, purpose) VALUES ('carol@ex.com','t-1','2099-01-01','account_setup')`)
			},
			method: "DELETE", path: "/api/v1/admin/instance/people/carol/setup-link",
			state: func(f *instanceFixture) string {
				return scalar(f, `SELECT COUNT(*) FROM verification_tokens WHERE identifier='carol@ex.com'`)
			}},
		{name: "set membership", method: "PUT", path: "/api/v1/admin/instance/workspaces/ws-new/members/wsadmin", body: `{"role":"MEMBER"}`,
			state: func(f *instanceFixture) string { return f.role("ws-new", "wsadmin") }},
		{name: "remove membership", method: "DELETE", path: "/api/v1/admin/instance/workspaces/ws-old/members/wsadmin",
			state: func(f *instanceFixture) string { return f.role("ws-old", "wsadmin") }},
		{name: "create workspace", method: "POST", path: "/api/v1/admin/instance/workspaces", body: `{"name":"Ops","slug":"ops","owner_user_id":"carol"}`,
			state: func(f *instanceFixture) string { return scalar(f, `SELECT COUNT(*) FROM workspaces WHERE slug='ops'`) }},
		{name: "transfer ownership",
			setup: func(f *instanceFixture) {
				mustExec(f.t, f.db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-x','ws-new','wsadmin','ADMIN')`)
			},
			method: "POST", path: "/api/v1/admin/instance/workspaces/ws-new/transfer-ownership", body: `{"user_id":"wsadmin"}`,
			state: func(f *instanceFixture) string { return f.role("ws-new", "carol") + "|" + f.role("ws-new", "wsadmin") }},
		{name: "delete workspace", method: "DELETE", path: "/api/v1/admin/instance/workspaces/ws-new", body: `{"confirm_slug":"ws-new"}`,
			state: func(f *instanceFixture) string {
				return scalar(f, `SELECT COALESCE(deleted_at,'') FROM workspaces WHERE id='ws-new'`)
			}},
		{name: "keeper defaults", method: "PUT", path: "/api/v1/admin/instance/keeper/governance/defaults", body: `{"set":{"enabled":true}}`,
			state: func(f *instanceFixture) string {
				return scalar(f, `SELECT COUNT(*) FROM app_settings WHERE key = 'keeper.governance_defaults'`)
			}},
		{name: "keeper governance", method: "PUT", path: "/api/v1/admin/instance/keeper/governance", body: `{"all":true,"set":{"enabled":true}}`,
			state: func(f *instanceFixture) string { return scalar(f, `SELECT COUNT(*) FROM keeper_governance_settings`) }},
		{name: "retention", method: "PUT", path: "/api/v1/admin/instance/retention", body: `{"workspace_ids":null,"windows":{"inbox_days":30,"routine_runs_days":14}}`,
			state: func(f *instanceFixture) string {
				return scalar(f, `SELECT COUNT(*) FROM retention_settings`) + "|" +
					scalar(f, `SELECT COALESCE(GROUP_CONCAT(COALESCE(run_retention_days,'-')),'') FROM workspaces`) + "|" +
					scalar(f, `SELECT COUNT(*) FROM app_settings WHERE key = 'retention.defaults'`)
			}},
		{name: "retention defaults", method: "PUT", path: "/api/v1/admin/instance/retention/defaults", body: `{"windows":{"inbox_days":30}}`,
			state: func(f *instanceFixture) string {
				return scalar(f, `SELECT COUNT(*) FROM app_settings WHERE key = 'retention.defaults'`)
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newInstanceFixture(t)
			t.Setenv("CREWSHIP_PUBLIC_URL", "https://crewship.example")
			if c.setup != nil {
				c.setup(f)
			}
			before := c.state(f)
			mustExec(t, f.db, `CREATE TRIGGER reject_instance_audit BEFORE INSERT ON instance_audit_logs BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`)
			rr := f.do(f.boss, c.method, c.path, c.body)
			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("%s %s = %d with the audit failing, want 500: %s", c.method, c.path, rr.Code, rr.Body.String())
			}
			if after := c.state(f); after != before {
				t.Fatalf("state changed without an audit entry: %q → %q", before, after)
			}
		})
	}
}

// Removing a member first hands their pages to a crew. That hand-over is part
// of the removal: when the audit entry cannot be written the pages stay with
// the person, exactly like the membership (review follow-up, 2026-09-30).
func TestInstanceRemoveMemberKeepsThePagesWhenTheAuditFails(t *testing.T) {
	f := newInstanceFixture(t)
	wmSeedCrew(t, f.db, "ws-old", "rv-crew", "Review", "review")
	wmSeedCrewMember(t, f.db, "rv-crew", "wsadmin")
	wmSeedPage(t, f.db, "ws-old", "rv-page", "review", "Review", "wsadmin", "")
	mustExec(t, f.db, `CREATE TRIGGER reject_instance_audit BEFORE INSERT ON instance_audit_logs BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`)

	rr := f.do(f.boss, "DELETE", "/api/v1/admin/instance/workspaces/ws-old/members/wsadmin", "")
	wantCode(t, rr, http.StatusInternalServerError, "remove with the audit failing")

	var owner, crew string
	if err := f.db.QueryRow(`SELECT COALESCE(owner_user_id,''), COALESCE(owner_crew_id,'') FROM pages WHERE id = 'rv-page'`).Scan(&owner, &crew); err != nil {
		t.Fatal(err)
	}
	if owner != "wsadmin" || crew != "" || f.role("ws-old", "wsadmin") == "" {
		t.Fatalf("page owner = %q/%q, membership = %q; want everything as it was", owner, crew, f.role("ws-old", "wsadmin"))
	}
}
