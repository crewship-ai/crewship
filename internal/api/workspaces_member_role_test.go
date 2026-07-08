package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// Tests for PATCH /api/v1/workspaces/{id}/members/{memberId} (#867.2).
//
// The role-change ladder is the whole security contract:
//   - a caller may only GRANT a role strictly below their own
//     (roleRank[new] >= roleRank[caller] → 403);
//   - a caller may not modify a member ranked ABOVE their own
//     (roleRank[target] > roleRank[caller] → 403);
//   - the last OWNER cannot be demoted (409).
// RED-first — every rung is pinned before the UPDATE is trusted.

func roleRig(t *testing.T) (*WorkspaceHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db) // "test-user-id", OWNER, member id "m1"
	wsID := seedTestWorkspace(t, db, ownerID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewWorkspaceHandler(db, logger)
	return h, ownerID, wsID
}

// seedMember inserts a user + a workspace_members row with the given role,
// returning the member row id.
func seedMember(t *testing.T, h *WorkspaceHandler, wsID, memberID, userID, email, role string) {
	t.Helper()
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`, userID, email, "U "+userID); err != nil {
		t.Fatalf("seed user %s: %v", userID, err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, ?)`,
		memberID, wsID, userID, role,
	); err != nil {
		t.Fatalf("seed member %s: %v", memberID, err)
	}
}

func roleReq(t *testing.T, callerID, wsID, callerRole, memberID, newRole string) *http.Request {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"role": newRole})
	req := httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/members/"+memberID, bytes.NewReader(body))
	req.SetPathValue("workspaceId", wsID)
	req.SetPathValue("memberId", memberID)
	return withWorkspaceUser(req, callerID, wsID, callerRole)
}

func memberRole(t *testing.T, h *WorkspaceHandler, memberID string) string {
	t.Helper()
	var role string
	if err := h.db.QueryRow(`SELECT role FROM workspace_members WHERE id = ?`, memberID).Scan(&role); err != nil {
		t.Fatalf("read member role: %v", err)
	}
	return role
}

// Grant ceiling: an ADMIN cannot mint another ADMIN (equal rank).
func TestMemberRole_GrantEqualRank_Forbidden(t *testing.T) {
	h, _, wsID := roleRig(t)
	seedMember(t, h, wsID, "m-admin", "u-admin", "admin@x.io", "ADMIN")
	seedMember(t, h, wsID, "m-target", "u-target", "target@x.io", "MEMBER")

	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, roleReq(t, "u-admin", wsID, "ADMIN", "m-target", "ADMIN"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if got := memberRole(t, h, "m-target"); got != "MEMBER" {
		t.Fatalf("role changed to %q despite 403", got)
	}
}

// Grant below own rank succeeds: ADMIN promotes MEMBER → MANAGER.
func TestMemberRole_GrantBelowOwn_OK(t *testing.T) {
	h, _, wsID := roleRig(t)
	seedMember(t, h, wsID, "m-admin", "u-admin", "admin@x.io", "ADMIN")
	seedMember(t, h, wsID, "m-target", "u-target", "target@x.io", "MEMBER")

	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, roleReq(t, "u-admin", wsID, "ADMIN", "m-target", "MANAGER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := memberRole(t, h, "m-target"); got != "MANAGER" {
		t.Fatalf("role = %q, want MANAGER", got)
	}
}

// Cannot modify a member ranked above you: a MANAGER may not touch an ADMIN.
func TestMemberRole_ModifySuperior_Forbidden(t *testing.T) {
	h, _, wsID := roleRig(t)
	seedMember(t, h, wsID, "m-mgr", "u-mgr", "mgr@x.io", "MANAGER")
	seedMember(t, h, wsID, "m-admin", "u-admin", "admin@x.io", "ADMIN")

	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, roleReq(t, "u-mgr", wsID, "MANAGER", "m-admin", "MEMBER"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if got := memberRole(t, h, "m-admin"); got != "ADMIN" {
		t.Fatalf("superior role changed to %q despite 403", got)
	}
}

// Last-owner guard: the sole OWNER cannot demote themselves.
func TestMemberRole_DemoteLastOwner_Conflict(t *testing.T) {
	h, ownerID, wsID := roleRig(t)
	// ownerID is member "m1" (the only OWNER), demoting self to ADMIN.
	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, roleReq(t, ownerID, wsID, "OWNER", "m1", "ADMIN"))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if got := memberRole(t, h, "m1"); got != "OWNER" {
		t.Fatalf("last owner demoted to %q", got)
	}
}

// With a co-owner present, an OWNER may demote the other OWNER.
func TestMemberRole_DemoteCoOwner_OK(t *testing.T) {
	h, ownerID, wsID := roleRig(t)
	seedMember(t, h, wsID, "m-owner2", "u-owner2", "owner2@x.io", "OWNER")

	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, roleReq(t, ownerID, wsID, "OWNER", "m-owner2", "ADMIN"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := memberRole(t, h, "m-owner2"); got != "ADMIN" {
		t.Fatalf("co-owner role = %q, want ADMIN", got)
	}
}

// Invalid role string is a 400.
func TestMemberRole_InvalidRole_BadRequest(t *testing.T) {
	h, ownerID, wsID := roleRig(t)
	seedMember(t, h, wsID, "m-target", "u-target", "target@x.io", "MEMBER")

	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, roleReq(t, ownerID, wsID, "OWNER", "m-target", "SUPERUSER"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// A member id not in this workspace is a 404.
func TestMemberRole_NotFound(t *testing.T) {
	h, ownerID, wsID := roleRig(t)
	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, roleReq(t, ownerID, wsID, "OWNER", "m-nope", "MEMBER"))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// Error responses are RFC 7807 problem+json (detail/status/title), not
// the legacy {error} shape (#883 review).
func TestMemberRole_ErrorsAreProblemJSON(t *testing.T) {
	h, _, wsID := roleRig(t)
	seedMember(t, h, wsID, "m-admin", "u-admin", "admin@x.io", "ADMIN")
	seedMember(t, h, wsID, "m-target", "u-target", "target@x.io", "MEMBER")

	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, roleReq(t, "u-admin", wsID, "ADMIN", "m-target", "ADMIN")) // grant ceiling → 403
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	var prob struct {
		Status int    `json:"status"`
		Detail string `json:"detail"`
		Title  string `json:"title"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &prob); err != nil {
		t.Fatalf("unmarshal problem: %v; body=%s", err, rr.Body.String())
	}
	if prob.Status != http.StatusForbidden {
		t.Fatalf("problem.status = %d, want 403", prob.Status)
	}
	if prob.Detail == "" {
		t.Fatalf("problem.detail empty; body=%s", rr.Body.String())
	}
}

// A non-privileged caller (below MANAGER) is refused defensively even if
// the route gate were misconfigured.
func TestMemberRole_CallerBelowManager_Forbidden(t *testing.T) {
	h, _, wsID := roleRig(t)
	seedMember(t, h, wsID, "m-caller", "u-caller", "caller@x.io", "MEMBER")
	seedMember(t, h, wsID, "m-target", "u-target", "target@x.io", "VIEWER")

	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, roleReq(t, "u-caller", wsID, "MEMBER", "m-target", "VIEWER"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}
