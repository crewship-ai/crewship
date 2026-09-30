package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// seedInstanceUser inserts a user and, when wsID is set, a membership.
func seedInstanceUser(t *testing.T, db *sql.DB, id, email, wsID, role string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`, id, email, id)
	if wsID != "" {
		mustExec(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, ?)`, "m-"+id+"-"+wsID, wsID, id, role)
	}
}

func seedInstanceWorkspace(t *testing.T, db *sql.DB, id, created string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug, created_at) VALUES (?, ?, ?, ?)`, id, id, id, created)
}

// TestInstanceAdminStatus pins who administers the instance, and why. The
// env owner always does; a named instance admin does; with neither, the
// one-time bootstrap names the OWNERs of the oldest workspace the first time
// anyone asks, so a fresh or seeded install is never left without one — and
// it does nothing on an install that already names someone
// (instance_admin_bootstrap_test.go pins that it never runs twice).
func TestInstanceAdminStatus(t *testing.T) {
	cases := []struct {
		name       string
		envOwner   string
		setup      func(t *testing.T, db *sql.DB)
		user       string
		want       bool
		wantSource string
	}{
		{
			name:     "env owner is an instance admin without being a member anywhere",
			envOwner: "boss@ex.com",
			setup: func(t *testing.T, db *sql.DB) {
				seedInstanceUser(t, db, "boss", "boss@ex.com", "", "")
			},
			user: "boss", want: true, wantSource: instanceAdminSourceEnv,
		},
		{
			name: "a named instance admin is one",
			setup: func(t *testing.T, db *sql.DB) {
				seedInstanceWorkspace(t, db, "old", "2026-01-01 00:00:00")
				seedInstanceUser(t, db, "ann", "ann@ex.com", "old", "MEMBER")
				mustExec(t, db, `UPDATE users SET instance_role = 'ADMIN' WHERE id = 'ann'`)
			},
			user: "ann", want: true, wantSource: instanceAdminSourceRole,
		},
		{
			name: "with nobody named, the bootstrap names the oldest workspace's owner",
			setup: func(t *testing.T, db *sql.DB) {
				seedInstanceWorkspace(t, db, "old", "2026-01-01 00:00:00")
				seedInstanceWorkspace(t, db, "new", "2026-06-01 00:00:00")
				seedInstanceUser(t, db, "first", "first@ex.com", "old", "OWNER")
				seedInstanceUser(t, db, "later", "later@ex.com", "new", "OWNER")
			},
			user: "first", want: true, wantSource: instanceAdminSourceRole,
		},
		{
			name: "owning a newer workspace is not enough",
			setup: func(t *testing.T, db *sql.DB) {
				seedInstanceWorkspace(t, db, "old", "2026-01-01 00:00:00")
				seedInstanceWorkspace(t, db, "new", "2026-06-01 00:00:00")
				seedInstanceUser(t, db, "first", "first@ex.com", "old", "OWNER")
				seedInstanceUser(t, db, "later", "later@ex.com", "new", "OWNER")
			},
			user: "later", want: false,
		},
		{
			name: "an ADMIN of the oldest workspace is not an owner",
			setup: func(t *testing.T, db *sql.DB) {
				seedInstanceWorkspace(t, db, "old", "2026-01-01 00:00:00")
				seedInstanceUser(t, db, "first", "first@ex.com", "old", "OWNER")
				seedInstanceUser(t, db, "adm", "adm@ex.com", "old", "ADMIN")
			},
			user: "adm", want: false,
		},
		{
			name: "an install that names someone is not bootstrapped",
			setup: func(t *testing.T, db *sql.DB) {
				seedInstanceWorkspace(t, db, "old", "2026-01-01 00:00:00")
				seedInstanceUser(t, db, "first", "first@ex.com", "old", "OWNER")
				seedInstanceUser(t, db, "ann", "ann@ex.com", "", "")
				mustExec(t, db, `UPDATE users SET instance_role = 'ADMIN' WHERE id = 'ann'`)
			},
			user: "first", want: false,
		},
		{
			name:     "an install with an env owner is not bootstrapped",
			envOwner: "boss@ex.com",
			setup: func(t *testing.T, db *sql.DB) {
				seedInstanceWorkspace(t, db, "old", "2026-01-01 00:00:00")
				seedInstanceUser(t, db, "first", "first@ex.com", "old", "OWNER")
			},
			user: "first", want: false,
		},
		{
			name: "the bootstrap skips a deleted workspace",
			setup: func(t *testing.T, db *sql.DB) {
				seedInstanceWorkspace(t, db, "gone", "2025-01-01 00:00:00")
				mustExec(t, db, `UPDATE workspaces SET deleted_at = '2026-01-01' WHERE id = 'gone'`)
				seedInstanceWorkspace(t, db, "old", "2026-01-01 00:00:00")
				seedInstanceUser(t, db, "first", "first@ex.com", "old", "OWNER")
			},
			user: "first", want: true, wantSource: instanceAdminSourceRole,
		},
		{
			name: "a suspended account administers nothing",
			setup: func(t *testing.T, db *sql.DB) {
				seedInstanceUser(t, db, "ann", "ann@ex.com", "", "")
				mustExec(t, db, `UPDATE users SET instance_role = 'ADMIN', suspended_at = '2026-09-29' WHERE id = 'ann'`)
			},
			user: "ann", want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(backup.InstanceOwnerEmailEnv, tc.envOwner)
			db := setupTestDB(t)
			tc.setup(t, db)
			var email string
			if err := db.QueryRow(`SELECT email FROM users WHERE id = ?`, tc.user).Scan(&email); err != nil {
				t.Fatalf("user: %v", err)
			}
			got, source, err := instanceAdminStatus(t.Context(), db, tc.user, email)
			if err != nil {
				t.Fatalf("instanceAdminStatus: %v", err)
			}
			if got != tc.want || (tc.want && source != tc.wantSource) {
				t.Errorf("instanceAdminStatus(%s) = %v (%q), want %v (%q)", tc.user, got, source, tc.want, tc.wantSource)
			}
		})
	}
}

// TestInstanceGate is the privilege fix: an instance-wide setting is changed
// by an instance admin, not by whoever is ADMIN of the workspace they name.
func TestInstanceGate(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	seedTestWorkspace(t, db, ownerID) // OWNER of the only workspace → fallback admin
	ownerTok := mintTokenFor(t, db, ownerID, "instgateowner00000000000000")
	adminTok := seedRoleMemberToken(t, db, "test-workspace-id", "ws-admin", "ADMIN", "instgateadmin00000000000000")

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	put := func(tok string) int {
		req := httptest.NewRequest("PUT", "/api/v1/admin/log-level?workspace_id=test-workspace-id", strings.NewReader(`{"level":"info","ttl_seconds":0}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr.Code
	}
	if code := put(adminTok); code != http.StatusForbidden {
		t.Errorf("workspace ADMIN PUT /admin/log-level = %d, want 403 (instance-wide)", code)
	}
	if code := put(ownerTok); code == http.StatusForbidden {
		t.Errorf("instance admin PUT /admin/log-level = 403, want to pass the gate")
	}

	// Every instance-gated mutation is recorded with the instance role and
	// scope, so the route invariants see it.
	var n int
	for _, m := range r.mutationRoutes {
		if m.Role == roleInstance {
			n++
			if m.Scope != scopeInstanceAdmin {
				t.Errorf("%s %s: instance route scope %q, want %q", m.Method, m.Pattern, m.Scope, scopeInstanceAdmin)
			}
		}
	}
	if n < 10 {
		t.Errorf("only %d mutation routes behind the instance gate; the instance-wide settings were not all moved", n)
	}
}
