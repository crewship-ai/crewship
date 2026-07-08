package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// admin_authz_floor_test.go — the ADMIN+ floor for the admin console surface
// (#865). Before this, /admin/* GET routes and /system/keeper + /system/runtime
// registered as authed(wsCtx(...)) / authed(...) with no role, so any workspace
// MEMBER could read cross-user / instance-wide operational data while the
// destructive mutations behind the same console were already ADMIN+. These
// tests pin: MEMBER is 403'd on the floor, keeper counts are workspace-scoped,
// runtime detail is redacted (not 403'd) for non-admins, and a new admin read
// cannot ship without the floor.

// mintTokenFor inserts a CLI token for an existing user and returns the plaintext.
func mintTokenFor(t *testing.T, db *sql.DB, userID, suffix string) string {
	t.Helper()
	plaintext := "crewship_cli_" + suffix
	if _, err := db.Exec(
		`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?, datetime('now'))`,
		"clt-"+userID, userID, "t", sha256Hex(plaintext),
	); err != nil {
		t.Fatalf("mint token for %s: %v", userID, err)
	}
	return plaintext
}

// seedRoleMemberToken creates a user, adds it to wsID with the given role, and
// mints a CLI token — returning the plaintext token.
func seedRoleMemberToken(t *testing.T, db *sql.DB, wsID, userID, role, suffix string) string {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`, userID, userID+"@ex.com", userID); err != nil {
		t.Fatalf("seed user %s: %v", userID, err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, ?)`, "m-"+userID, wsID, userID, role); err != nil {
		t.Fatalf("seed member %s: %v", userID, err)
	}
	return mintTokenFor(t, db, userID, suffix)
}

func adminFloorGet(r *Router, token, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path+"?workspace_id=test-workspace-id", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// TestAdminFloor_MemberDeniedAdminSurface pins the acceptance: a MEMBER token is
// 403'd across the /admin/* read surface and /system/keeper, while an ADMIN
// token is not blocked by the floor.
func TestAdminFloor_MemberDeniedAdminSurface(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID) // ownerID is OWNER of test-workspace-id
	_ = wsID
	memberTok := seedRoleMemberToken(t, db, "test-workspace-id", "member-u", "MEMBER", "memberfloor0000000000000000")
	adminTok := seedRoleMemberToken(t, db, "test-workspace-id", "admin-u", "ADMIN", "adminfloor00000000000000000")

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	floored := []string{
		"/api/v1/admin/stats",
		"/api/v1/admin/users",
		"/api/v1/admin/workspaces",
		"/api/v1/admin/health",
		"/api/v1/admin/memory/stats",
		"/api/v1/admin/backups",
		"/api/v1/admin/keeper/requests",
		"/api/v1/system/keeper",
		"/api/v1/system/aux-status",
	}
	for _, p := range floored {
		if code := adminFloorGet(r, memberTok, p).Code; code != http.StatusForbidden {
			t.Errorf("MEMBER GET %s = %d, want 403 (admin floor)", p, code)
		}
		// ADMIN must clear the *floor* — the handler may still 200 or 5xx on
		// data, but it must never be the 403 the floor produces for a MEMBER.
		if code := adminFloorGet(r, adminTok, p).Code; code == http.StatusForbidden {
			t.Errorf("ADMIN GET %s = 403, want to clear the ADMIN+ floor", p)
		}
	}
}

// TestSystemRuntime_RedactedNot403ForMember pins the redaction decision (#865):
// /system/runtime is NOT floored — a MEMBER (or a caller with no workspace) gets
// 200, but only the bare availability flag, never host detail (socket/version).
func TestSystemRuntime_RedactedNot403ForMember(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	seedTestWorkspace(t, db, ownerID)
	memberTok := seedRoleMemberToken(t, db, "test-workspace-id", "member-rt", "MEMBER", "memberrt000000000000000000")

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	rr := adminFloorGet(r, memberTok, "/api/v1/system/runtime")
	if rr.Code != http.StatusOK {
		t.Fatalf("MEMBER GET /system/runtime = %d, want 200 (redacted, not floored)", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["available"]; !ok {
		t.Errorf("redacted runtime response must carry `available`; got %v", body)
	}
	// The leak the redaction closes: when a runtime IS detected, a non-admin
	// must not see socket paths / versions / the runtimes array. (In a runner
	// without a container runtime this is vacuously satisfied — the no-runtime
	// branch exposes only nils — so guard on availability.)
	if body["available"] == true {
		for _, sensitive := range []string{"socket", "runtimes"} {
			if _, leaked := body[sensitive]; leaked {
				t.Errorf("non-admin runtime response leaked %q: %v", sensitive, body)
			}
		}
	}
}

// TestKeeperStatus_WorkspaceScopedCounts pins that the keeper request counts are
// scoped to the caller's workspace — the old global COUNT(*) leaked cross-tenant
// volume to anyone who could reach the endpoint.
func TestKeeperStatus_WorkspaceScopedCounts(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	seedTestWorkspace(t, db, ownerID) // OWNER of test-workspace-id (workspace A)
	ownerTok := mintTokenFor(t, db, ownerID, "ownerkeeper00000000000000")

	// Workspace B with its own agent + a keeper request that must NOT be counted.
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws-b', 'B', 'wsb')`)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES ('ag-a', 'test-workspace-id', 'A', 'aa')`)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES ('ag-b', 'ws-b', 'B', 'bb')`)
	mustExec(t, db, `INSERT INTO keeper_requests (id, requesting_agent_id, intent, decision) VALUES
		('kr1','ag-a','x','ALLOW'), ('kr2','ag-a','x','DENY'), ('kr3','ag-b','x','ESCALATE'), ('kr4','ag-b','x','ALLOW')`)

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	rr := adminFloorGet(r, ownerTok, "/api/v1/system/keeper")
	if rr.Code != http.StatusOK {
		t.Fatalf("OWNER GET /system/keeper = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp keeperStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Workspace A has 2 requests (1 ALLOW, 1 DENY); B's 2 must be excluded.
	if resp.TotalRequests != 2 || resp.AllowCount != 1 || resp.DenyCount != 1 || resp.EscalateCount != 0 {
		t.Errorf("counts not workspace-scoped: got total=%d allow=%d deny=%d escalate=%d, want 2/1/1/0 (workspace B excluded)",
			resp.TotalRequests, resp.AllowCount, resp.DenyCount, resp.EscalateCount)
	}
}

// TestEveryAdminReadDeclaresFloor is the floor invariant: every admin-console
// read must register through authedAdmin (recorded in r.adminRoutes), and the
// source must not register any /api/v1/admin/ GET through the raw r.mux.Handle
// chain — a new admin read that forgets the floor fails the build.
func TestEveryAdminReadDeclaresFloor(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if len(r.adminRoutes) < 15 {
		t.Errorf("only %d admin read routes recorded through authedAdmin; expected the full /admin/* GET surface — some were not migrated off authed(wsCtx(...))", len(r.adminRoutes))
	}

	// Source guard: no /admin/ or /system/keeper GET may be registered via the
	// raw r.mux.Handle chain — those must be authedAdmin.
	files, err := filepath.Glob("router_*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	offender := regexp.MustCompile(`r\.mux\.Handle(Func)?\("GET /api/v1/(admin/|system/keeper)`)
	var offenders []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if offender.MatchString(line) {
				offenders = append(offenders, formatOffender(f, i+1, line))
			}
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("%d admin read route(s) registered via the raw chain instead of authedAdmin — every admin read must carry the ADMIN+ floor:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
