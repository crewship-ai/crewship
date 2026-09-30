package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// The instance admins are always named. The old rule — "while nobody is
// named, the OWNERs of the oldest workspace are admins" — switched itself back
// on whenever the last named admin went, handing the instance to whoever owned
// that workspace. It is now a one-time bootstrap: the first time anyone asks,
// an install with nobody named (and no env owner) names those owners, records
// that it did, and never does it again.

func bootstrapped(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM app_settings WHERE key = ?`, instanceAdminBootstrapKey).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func role(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var r sql.NullString
	_ = db.QueryRow(`SELECT instance_role FROM users WHERE id = ?`, id).Scan(&r)
	return r.String
}

func TestBootstrapNamesTheOldestWorkspacesOwnersOnce(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	db := setupTestDB(t)
	seedInstanceWorkspace(t, db, "old", "2026-01-01 00:00:00")
	seedInstanceWorkspace(t, db, "new", "2026-06-01 00:00:00")
	seedInstanceUser(t, db, "first", "first@ex.com", "old", "OWNER")
	seedInstanceUser(t, db, "later", "later@ex.com", "new", "OWNER")

	ok, source, err := instanceAdminStatus(context.Background(), db, "first", "first@ex.com")
	if err != nil || !ok || source != instanceAdminSourceRole {
		t.Fatalf("first = %v %q %v, want named by the bootstrap", ok, source, err)
	}
	if role(t, db, "first") != "ADMIN" || role(t, db, "later") != "" {
		t.Fatalf("roles first=%q later=%q, want only the oldest workspace's owner named", role(t, db, "first"), role(t, db, "later"))
	}
	if !bootstrapped(t, db) {
		t.Fatal("the bootstrap left no record")
	}
	var audited int
	_ = db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action = 'instance.admin_bootstrapped'`).Scan(&audited)
	if audited != 1 {
		t.Fatalf("bootstrap audit entries = %d, want 1", audited)
	}

	// With every admin gone the old rule would hand the instance back to the
	// oldest workspace's owners. The bootstrap does not run twice.
	mustExec(t, db, `UPDATE users SET instance_role = NULL`)
	if ok, _, _ := instanceAdminStatus(context.Background(), db, "first", "first@ex.com"); ok {
		t.Fatal("owning the oldest workspace made someone an instance admin again after the bootstrap")
	}
}

func TestBootstrapKeepsAnInstanceThatAlreadyNamesSomeone(t *testing.T) {
	for _, tc := range []struct {
		name     string
		envOwner string
		setup    func(db *sql.DB)
	}{
		{"a named admin", "", func(db *sql.DB) { mustExec(t, db, `UPDATE users SET instance_role = 'ADMIN' WHERE id = 'ann'`) }},
		{"an env owner", "boss@ex.com", func(db *sql.DB) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(backup.InstanceOwnerEmailEnv, tc.envOwner)
			db := setupTestDB(t)
			seedInstanceWorkspace(t, db, "old", "2026-01-01 00:00:00")
			seedInstanceUser(t, db, "first", "first@ex.com", "old", "OWNER")
			seedInstanceUser(t, db, "ann", "ann@ex.com", "", "")
			tc.setup(db)
			if ok, _, _ := instanceAdminStatus(context.Background(), db, "first", "first@ex.com"); ok {
				t.Fatal("the oldest workspace's owner became an admin on an instance that already names one")
			}
			if role(t, db, "first") != "" || !bootstrapped(t, db) {
				t.Fatalf("role=%q bootstrapped=%v, want no promotion and the bootstrap marked done", role(t, db, "first"), bootstrapped(t, db))
			}
		})
	}
}

func TestBootstrapWaitsForTheFirstWorkspace(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	db := setupTestDB(t)
	mustExec(t, db, `DELETE FROM workspaces`)
	seedInstanceUser(t, db, "first", "first@ex.com", "", "")
	if ok, _, _ := instanceAdminStatus(context.Background(), db, "first", "first@ex.com"); ok || bootstrapped(t, db) {
		t.Fatal("an install with no workspace named someone, or marked the bootstrap done")
	}
	seedInstanceWorkspace(t, db, "old", "2026-01-01 00:00:00")
	mustExec(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-first', 'old', 'first', 'OWNER')`)
	if ok, _, _ := instanceAdminStatus(context.Background(), db, "first", "first@ex.com"); !ok {
		t.Fatal("the first workspace's owner was not named once there was one")
	}
}

// The last active instance admin cannot be removed or suspended: someone has
// to be left who can undo it. (The actor is an admin too and cannot act on
// themselves, so over HTTP this is the guard against two admins removing each
// other at the same moment; the check runs inside the change's transaction.)
func TestTheLastActiveInstanceAdminStays(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	db := setupTestDB(t)
	seedInstanceUser(t, db, "ann", "ann@ex.com", "", "")
	seedInstanceUser(t, db, "bob", "bob@ex.com", "", "")
	mustExec(t, db, `UPDATE users SET instance_role = 'ADMIN' WHERE id IN ('ann', 'bob')`)
	mustExec(t, db, `INSERT INTO app_settings (key, value) VALUES (?, 'test')`, instanceAdminBootstrapKey)

	check := func(target string) bool {
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		last, err := lastActiveInstanceAdminTx(context.Background(), tx, target)
		if err != nil {
			t.Fatal(err)
		}
		return last
	}
	if check("bob") {
		t.Fatal("bob is not the last admin while ann is one")
	}
	mustExec(t, db, `UPDATE users SET suspended_at = '2026-09-30' WHERE id = 'ann'`)
	if !check("bob") {
		t.Fatal("bob is the last active admin once ann is suspended")
	}
	t.Setenv(backup.InstanceOwnerEmailEnv, "owner@ex.com")
	seedInstanceUser(t, db, "owner", "owner@ex.com", "", "")
	if check("bob") {
		t.Fatal("the env owner is an admin too; bob is not the last")
	}
}

// Two admins removing each other at the same moment both pass the route's
// gate before either change lands. Replayed here by calling the handlers as
// bob after bob has already been removed: the change must still refuse to
// leave the instance with nobody.
func TestRevokeAndSuspendRefuseToLeaveNoAdmin(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	db := setupTestDB(t)
	seedInstanceUser(t, db, "ann", "ann@ex.com", "", "")
	seedInstanceUser(t, db, "bob", "bob@ex.com", "", "")
	mustExec(t, db, `UPDATE users SET instance_role = 'ADMIN' WHERE id = 'ann'`)
	mustExec(t, db, `INSERT INTO app_settings (key, value) VALUES (?, 'test')`, instanceAdminBootstrapKey)
	h := NewInstanceAdminHandler(db, newTestLogger(), nil, nil, nil)
	for _, c := range []struct {
		name   string
		method string
		path   string
		run    func(w http.ResponseWriter, r *http.Request)
	}{
		{"revoke", "DELETE", "/api/v1/admin/instance/admins/ann", h.RevokeAdmin},
		{"suspend", "POST", "/api/v1/admin/instance/people/ann/suspend", h.Suspend},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, nil)
			req.SetPathValue("userId", "ann")
			req = req.WithContext(context.WithValue(withUser(req.Context(), &AuthUser{ID: "bob", Email: "bob@ex.com"}), ctxInstanceAdmin, true))
			rr := httptest.NewRecorder()
			c.run(rr, req)
			if rr.Code != http.StatusConflict {
				t.Fatalf("%s the last admin = %d, want 409: %s", c.name, rr.Code, rr.Body.String())
			}
			var r, susp sql.NullString
			_ = db.QueryRow(`SELECT instance_role, suspended_at FROM users WHERE id = 'ann'`).Scan(&r, &susp)
			if r.String != "ADMIN" || susp.Valid {
				t.Fatalf("ann = %q / suspended %v, want still an active admin", r.String, susp.Valid)
			}
		})
	}
}
