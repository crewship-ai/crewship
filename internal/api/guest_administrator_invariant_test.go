package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGuestCannotAcquireAdministrationElsewhere(t *testing.T) {
	for _, path := range []string{"invitation", "role", "instance-insert", "instance-update", "transfer", "create-workspace", "provision", "signup-redeem"} {
		t.Run(path, func(t *testing.T) {
			f := newInstanceFixture(t)
			t.Cleanup(f.r.Shutdown)
			execOrFatal(t, f.db, `INSERT INTO users(id,email) VALUES('guest','guest@example.test')`)
			execOrFatal(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('guest-source','ws-new','guest','VIEWER','restricted')`)
			if path == "role" || path == "instance-update" || path == "transfer" {
				execOrFatal(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('guest-target','ws-old','guest','MEMBER')`)
			}
			method, url, body, token := "PUT", "/api/v1/admin/instance/workspaces/ws-old/members/guest", `{"role":"ADMIN"}`, f.boss
			switch path {
			case "invitation", "signup-redeem":
				seedInvitation(t, f.db, "guest-admin-invite", "ws-old", "boss", "guest@example.test", "ADMIN", time.Now().Add(time.Hour), nil)
				if path == "signup-redeem" {
					tx, err := f.db.BeginTx(t.Context(), nil)
					if err != nil {
						t.Fatal(err)
					}
					defer tx.Rollback()
					_, err = redeemPendingInvitations(t.Context(), tx, "guest", "guest@example.test", "tok-guest-admin-invite", time.Now().UTC().Format(time.RFC3339))
					if err == nil {
						t.Fatal("signup redemption accepted Guest administrator")
					}
					return
				}
				method, url, body = "POST", "/api/v1/auth/invitation/accept", `{"token":"tok-guest-admin-invite"}`
				token = mintTokenFor(t, f.db, "guest", "guest-admin-invariant-"+strings.Repeat("0", 40))
			case "role":
				method, url = "PATCH", "/api/v1/workspaces/ws-old/members/guest-target"
			case "transfer":
				method, url, body = "POST", "/api/v1/admin/instance/workspaces/ws-old/transfer-ownership", `{"user_id":"guest"}`
			case "create-workspace":
				method, url, body = "POST", "/api/v1/admin/instance/workspaces", `{"name":"Guest owned","slug":"guest-owned","owner_user_id":"guest"}`
			case "provision":
				method, url, body = "POST", "/api/v1/workspaces/ws-old/members/provision", `{"email":"guest@example.test","role":"ADMIN"}`
			}
			req := httptest.NewRequest(method, url, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Workspace-ID", "ws-old")
			rr := httptest.NewRecorder()
			f.r.ServeHTTP(rr, req)
			if rr.Code != 409 {
				t.Fatalf("got %d, want 409: %s", rr.Code, rr.Body.String())
			}
			var n int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE user_id='guest' AND role IN ('OWNER','ADMIN')`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("administrator left behind: %d %v", n, err)
			}
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM workspace_invitations WHERE id='guest-admin-invite' AND accepted_at IS NOT NULL`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("invitation consumed: %d %v", n, err)
			}
		})
	}
}

// Exercise the invariant below handlers, including atomic repair of one row.
func TestGuestAdministratorDatabaseInvariant(t *testing.T) {
	db := setupTestDB(t)
	owner, ws := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('invariant-user','invariant@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('invariant-other','Other','invariant-other')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('invariant-guest',?,'invariant-user','VIEWER','restricted')`, ws)
	for _, statement := range []string{
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('forbidden','invariant-other','invariant-user','ADMIN')`,
		`UPDATE workspace_members SET role='OWNER' WHERE id='invariant-guest'`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('forbidden','invariant-other','invariant-user','ADMIN','restricted')`,
	} {
		if _, err := db.Exec(statement); err == nil || !strings.Contains(err.Error(), "guest administrator conflict") {
			t.Fatalf("invariant missed %s: %v", statement, err)
		}
	}
	// Updating a row to a valid new state must exclude its own old state.
	execOrFatal(t, db, `UPDATE workspace_members SET access_mode='trusted',role='ADMIN' WHERE id='invariant-guest'`)
	if _, err := db.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('forbidden','invariant-other','invariant-user','VIEWER','restricted')`); err == nil {
		t.Fatal("restricted insertion stranded administrator")
	}
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('invariant-trusted','invariant-other','invariant-user','VIEWER')`)
	if _, err := db.Exec(`UPDATE workspace_members SET access_mode='restricted' WHERE id='invariant-trusted'`); err == nil {
		t.Fatal("restricted update stranded administrator")
	}
	execOrFatal(t, db, `UPDATE workspace_members SET access_mode='restricted',role='VIEWER' WHERE id='invariant-guest'`)
	// Moving an administrative membership to a restricted identity is guarded too.
	if _, err := db.Exec(`UPDATE workspace_members SET user_id='invariant-user' WHERE user_id=? AND workspace_id=?`, owner, ws); err == nil {
		t.Fatal("user reassignment bypassed invariant")
	}
}
