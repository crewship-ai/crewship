package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// approvals_read_authz_test.go — #2233.
//
// GET /api/v1/approvals and GET /api/v1/approvals/{id} used to register as
// authed(wsCtx(...)) — authentication + workspace membership only, no role —
// while the decide/cancel routes right next to them in
// router_orchestration.go were already roleManage. Both GETs return the
// full row including payload, so any authenticated workspace member,
// including tiers that can never decide anything, could read every
// approval ever created. These tests pin the fix at the real router
// (ApprovalsHandler.List/Get carry no inline role check of their own — the
// gate is entirely the requireRoleScopeMW middleware authedMut wires up —
// so a test calling the handler methods directly would not exercise it;
// this has to go through r.ServeHTTP like admin_authz_floor_test.go does).

func approvalsReadReq(method, path, token string) *http.Request {
	req := httptest.NewRequest(method, path+"?workspace_id=test-workspace-id", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

// TestApprovalsRead_MemberDenied403_AdminAllowed pins the role floor on
// both GET routes: MEMBER (and MANAGER, which can create/update but still
// cannot decide) get 403; OWNER/ADMIN clear the floor.
func TestApprovalsRead_MemberDenied403_AdminAllowed(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	seedTestWorkspace(t, db, ownerID) // owns "test-workspace-id"
	memberTok := seedRoleMemberToken(t, db, "test-workspace-id", "member-ar", "MEMBER", "memberapprovread00000000000")
	managerTok := seedRoleMemberToken(t, db, "test-workspace-id", "manager-ar", "MANAGER", "managerapprovread0000000000")
	adminTok := seedRoleMemberToken(t, db, "test-workspace-id", "admin-ar", "ADMIN", "adminapprovread000000000000")

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	// A real row so Get has something to 404-not-on: without one, a member
	// blocked by the role floor and a member blocked by a 404 would look
	// identical in this test, and the whole point is proving it's the
	// floor.
	if _, err := db.Exec(`INSERT INTO approvals_queue
		(id, workspace_id, requested_by, kind, reason, status)
		VALUES ('ap-read-authz', 'test-workspace-id', 'someone', 'tool_call', 'because', 'pending')`,
	); err != nil {
		t.Fatalf("seed approval row: %v", err)
	}

	routes := []string{
		"/api/v1/approvals",
		"/api/v1/approvals/ap-read-authz",
	}
	for _, path := range routes {
		for _, deniedTok := range []struct {
			role  string
			token string
		}{{"MEMBER", memberTok}, {"MANAGER", managerTok}} {
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, approvalsReadReq(http.MethodGet, path, deniedTok.token))
			if rr.Code != http.StatusForbidden {
				t.Errorf("%s GET %s = %d, want 403 (approvals read floor is roleManage)",
					deniedTok.role, path, rr.Code)
			}
		}

		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, approvalsReadReq(http.MethodGet, path, adminTok))
		if rr.Code == http.StatusForbidden {
			t.Errorf("ADMIN GET %s = 403, want to clear the roleManage floor", path)
		}
	}
}

// TestApprovalsRead_NoRoleFloorOnMain documents (via the manifest check
// already enforced by TestMutationRouteRolesMatchManifest) that this was a
// real gap on main, not a pre-existing gate this PR merely tests. Kept as a
// narrow, human-readable statement of the regression alongside the
// behavioural test above.
func TestApprovalsRead_DecideAndCancelWereAlreadyGated(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	seedTestWorkspace(t, db, ownerID)
	memberTok := seedRoleMemberToken(t, db, "test-workspace-id", "member-dc", "MEMBER", "memberdecidecancel000000000")

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	for _, path := range []string{
		"/api/v1/approvals/nope/decide",
		"/api/v1/approvals/nope/cancel",
	} {
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, approvalsReadReq(http.MethodPost, path, memberTok))
		if rr.Code != http.StatusForbidden {
			t.Errorf("MEMBER POST %s = %d, want 403 (unchanged by this PR)", path, rr.Code)
		}
	}
}
