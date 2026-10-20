package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Real registered schema, authenticated router and configured journal key are
// shared with the existing authorization tests. No account setup secret is used.
func memberCreateFixture(t *testing.T, mode string) (*authorizationAuditFixture, func() *httptest.ResponseRecorder) {
	t.Helper()
	t.Setenv("CREWSHIP_PUBLIC_URL", "https://fixture.example.test")
	f := newAuthorizationAuditFixture(t)
	if mode != "new" {
		execOrFatal(t, f.db, `INSERT INTO users(id,email,hashed_password) VALUES('create-subject','member-create@example.test','already-claimed')`)
	}
	call := func() *httptest.ResponseRecorder {
		if mode == "add" {
			return f.do("POST", "/members", `{"user_id":"create-subject","role":"MEMBER"}`)
		}
		return f.do("POST", "/members/provision", `{"email":"member-create@example.test","role":"MEMBER"}`)
	}
	return f, call
}

func memberCreateCounts(t *testing.T, f *authorizationAuditFixture) (users, members, tokens, events int) {
	t.Helper()
	for _, q := range []struct {
		sql  string
		args []any
		dest *int
	}{
		{`SELECT count(*) FROM users WHERE email='member-create@example.test'`, nil, &users},
		{`SELECT count(*) FROM workspace_members m JOIN users u ON u.id=m.user_id WHERE m.workspace_id=? AND u.email='member-create@example.test'`, []any{f.workspace}, &members},
		{`SELECT count(*) FROM verification_tokens WHERE identifier='member-create@example.test' AND purpose='account_setup'`, nil, &tokens},
		{`SELECT count(*) FROM journal_entries WHERE workspace_id=? AND entry_type='member.added'`, []any{f.workspace}, &events},
	} {
		if err := f.db.QueryRow(q.sql, q.args...).Scan(q.dest); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestMemberCreateAuditNativeCalibration(t *testing.T) {
	for _, mode := range []string{"add", "claimed", "new"} {
		t.Run(mode, func(t *testing.T) {
			f, call := memberCreateFixture(t, mode)
			var fk int
			if err := f.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
				t.Fatalf("native FK calibration: %d %v", fk, err)
			}
			out := call()
			users, members, tokens, _ := memberCreateCounts(t, f)
			wantTokens := 0
			if mode == "new" {
				wantTokens = 1
			}
			if out.Code != 201 || users != 1 || members != 1 || tokens != wantTokens {
				t.Fatalf("healthy real writer calibration: status=%d users=%d members=%d tokens=%d", out.Code, users, members, tokens)
			}
			if mode != "add" {
				var body provisionResponse
				if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if (body.SetupURL != "") != (mode == "new") || body.CreatedUser != (mode == "new") {
					t.Fatal("claimed/new setup-link contract changed")
				}
			}
			f.checkChain(t)
		})
	}
}

func TestMemberCreateAuditAtomicity(t *testing.T) {
	for _, mode := range []string{"add", "claimed", "new"} {
		for _, fault := range []string{"healthy", "ABORT", "IGNORE", "after-undo", "deferred-commit", "insert-ignore"} {
			t.Run(mode+"/"+fault, func(t *testing.T) {
				f, call := memberCreateFixture(t, mode)
				switch fault {
				case "ABORT":
					execOrFatal(t, f.db, `CREATE TRIGGER create_audit_fault BEFORE INSERT ON journal_entries WHEN NEW.entry_type='member.added' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`)
				case "IGNORE":
					execOrFatal(t, f.db, `CREATE TRIGGER create_audit_fault BEFORE INSERT ON journal_entries WHEN NEW.entry_type='member.added' BEGIN SELECT RAISE(IGNORE); END`)
				case "after-undo":
					execOrFatal(t, f.db, `CREATE TRIGGER create_audit_fault AFTER INSERT ON journal_entries WHEN NEW.entry_type='member.added' BEGIN DELETE FROM workspace_members WHERE workspace_id=NEW.workspace_id AND user_id IN (SELECT id FROM users WHERE email='member-create@example.test'); END`)
				case "deferred-commit":
					execOrFatal(t, f.db, `CREATE TABLE create_commit_fault(parent TEXT REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED)`)
					execOrFatal(t, f.db, `CREATE TRIGGER create_audit_fault AFTER INSERT ON journal_entries WHEN NEW.entry_type='member.added' BEGIN INSERT INTO create_commit_fault VALUES('missing-parent'); END`)
				case "insert-ignore":
					execOrFatal(t, f.db, `CREATE TRIGGER create_member_fault BEFORE INSERT ON workspace_members WHEN NEW.user_id IN (SELECT id FROM users WHERE email='member-create@example.test') BEGIN SELECT RAISE(IGNORE); END`)
				}
				out := call()
				users, members, tokens, events := memberCreateCounts(t, f)
				if fault == "healthy" {
					if out.Code != 201 || members != 1 || events != 1 || f.published.Load() != 1 {
						t.Errorf("membership requires same-TX chained event and postcommit notification: status=%d members=%d events=%d notifications=%d", out.Code, members, events, f.published.Load())
					}
					if events == 1 {
						var actor, payload string
						if err := f.db.QueryRow(`SELECT actor_id,payload FROM journal_entries WHERE workspace_id=? AND entry_type='member.added'`, f.workspace).Scan(&actor, &payload); err != nil {
							t.Fatal(err)
						}
						if actor != f.owner || !strings.Contains(payload, "member_id") || strings.Contains(payload, "member-create@example.test") || strings.Contains(payload, "token") {
							t.Error("membership audit lost actor/target or contains private provisioning material")
						}
					}
				} else {
					wantUsers := 1
					if mode == "new" {
						wantUsers = 0
					}
					if out.Code != 500 || users != wantUsers || members != 0 || tokens != 0 || events != 0 || f.published.Load() != 0 {
						t.Errorf("failed audit/insert must roll back all account, membership and setup changes: status=%d users=%d members=%d tokens=%d events=%d notifications=%d", out.Code, users, members, tokens, events, f.published.Load())
					}
				}
				f.checkChain(t)
			})
		}
	}
}

func TestMemberCreateAuditRechecksActor(t *testing.T) {
	for _, mode := range []string{"add", "claimed", "new"} {
		for _, change := range []string{"demoted", "removed", "suspended", "member-with-foreign-guest"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				f, call := memberCreateFixture(t, mode)
				// This callback runs at the first body read, after real middleware admission.
				f.beforeBody = func() {
					switch change {
					case "demoted":
						execOrFatal(t, f.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, f.workspace, f.owner)
					case "removed":
						execOrFatal(t, f.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.workspace, f.owner)
					case "suspended":
						execOrFatal(t, f.db, `UPDATE users SET suspended_at='2026-10-19T00:00:00Z' WHERE id=?`, f.owner)
					case "member-with-foreign-guest":
						// Restricted accounts cannot simultaneously hold OWNER. Demote first;
						// a distinct legal Guest membership tests the combined denial. This
						// does not isolate the global ceiling from the MEMBER role refusal.
						execOrFatal(t, f.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, f.workspace, f.owner)
						execOrFatal(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('create-guest-ws','Guest fixture','create-guest-ws')`)
						execOrFatal(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('create-guest-member','create-guest-ws',?,'VIEWER','restricted')`, f.owner)
					}
				}
				out := call()
				users, members, tokens, events := memberCreateCounts(t, f)
				wantUsers := 1
				if mode == "new" {
					wantUsers = 0
				}
				if out.Code != 403 || users != wantUsers || members != 0 || tokens != 0 || events != 0 || f.published.Load() != 0 {
					t.Errorf("stale middleware actor must not grant membership: status=%d users=%d members=%d tokens=%d events=%d notifications=%d", out.Code, users, members, tokens, events, f.published.Load())
				}
			})
		}
	}
}

func TestMemberCreateAuditSnapshotsStoredGuestRights(t *testing.T) {
	for _, undo := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "audit-undo-rights"}[undo], func(t *testing.T) {
			f, _ := memberCreateFixture(t, "new")
			execOrFatal(t, f.db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES('create-project',?,'Fixture','create-project')`, f.workspace)
			if undo {
				execOrFatal(t, f.db, `CREATE TRIGGER create_grant_undo AFTER INSERT ON journal_entries WHEN NEW.entry_type='member.added' BEGIN DELETE FROM access_grants WHERE member_id=json_extract(NEW.payload,'$.member_id'); END`)
			}
			// Deliberately noncanonical input order: the recorded snapshot comes from
			// normalized stored grants after ReplaceIn, not the request array.
			out := f.do("POST", "/members/provision", `{"email":"member-create@example.test","role":"VIEWER","access_mode":"restricted","rights":[{"kind":"project","id":"create-project","operation":"read"},{"kind":"project","id":"create-project","operation":"list"}]}`)
			users, members, tokens, events := memberCreateCounts(t, f)
			if undo {
				if out.Code != 500 || users != 0 || members != 0 || tokens != 0 || events != 0 || f.published.Load() != 0 {
					t.Fatalf("audit-modified initial rights committed: status=%d users=%d members=%d tokens=%d events=%d", out.Code, users, members, tokens, events)
				}
			} else {
				if out.Code != 201 || members != 1 || events != 1 {
					t.Fatalf("Guest provisioning failed: %d", out.Code)
				}
				var raw string
				if err := f.db.QueryRow(`SELECT payload FROM journal_entries WHERE workspace_id=? AND entry_type='member.added'`, f.workspace).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var payload struct {
					Rights     []struct{ Kind, ID, Operation string }
					AccessMode string `json:"access_mode"`
				}
				if err := json.Unmarshal([]byte(raw), &payload); err != nil {
					t.Fatal(err)
				}
				if payload.AccessMode != "restricted" || len(payload.Rights) != 2 || payload.Rights[0].Operation != "list" || payload.Rights[1].Operation != "read" || payload.Rights[0].ID != "create-project" {
					t.Fatal("initial authority is not the normalized stored snapshot")
				}
			}
			f.checkChain(t)
		})
	}
}

func TestMemberCreateAuditTrustedAdministrator(t *testing.T) {
	for _, mode := range []string{"add", "claimed", "new"} {
		t.Run(mode, func(t *testing.T) {
			f, call := memberCreateFixture(t, mode)
			f.beforeBody = func() {
				execOrFatal(t, f.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, f.workspace, f.owner)
			}
			out := call()
			_, members, _, events := memberCreateCounts(t, f)
			if out.Code != 201 || members != 1 || events != 1 || f.published.Load() != 1 {
				t.Fatalf("live trusted ADMIN may grant MEMBER: status=%d members=%d events=%d notifications=%d", out.Code, members, events, f.published.Load())
			}
			f.checkChain(t)
		})
	}
}
