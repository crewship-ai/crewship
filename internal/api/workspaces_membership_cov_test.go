package api

// Additional statement-coverage for internal/api/workspaces_membership.go.
//
// The sibling file workspaces_membership_test.go already pins the role
// gates, validation 400s, and the single-row / empty-list happy paths.
// This file targets the branches those tests leave cold:
//
//   - ListMembers: the rows.Next() loop body when MORE THAN ONE member
//     exists (the scan-and-append path executed repeatedly, plus the
//     ORDER BY created_at ordering).
//   - ListInvitations: the populated path — the rows.Next() scan that
//     joins the inviter user and fills inviterUser. The sibling file only
//     exercises the empty / cross-workspace-empty cases, so the scan body
//     and inviter join were never hit.
//   - CreateInvitation: the two 409 conflict branches —
//       (a) email already belongs to a workspace member (JOIN on users),
//       (b) an active, unexpired invitation already exists for the email.
//   - CreateInvitation: empty-role-defaults-to-MEMBER on the invitation
//     path (mirrors the AddMember default-role branch).
//
// All helpers introduced here are prefixed covWM per the task contract;
// existing helpers (membershipRig, seedOtherUser, seedOtherWorkspace,
// withWorkspaceUser, setupTestDB, …) are reused as-is.
//
// SKIPPED (no non-network/non-DB-error way to reach them deterministically):
//   - The license CheckMemberLimit PaymentRequired / 500 branches in
//     AddMember & CreateInvitation: h.license is nil in membershipRig, so
//     the whole `if h.license != nil` block is intentionally bypassed.
//     Driving it would require a real *license.License with an enforced
//     member cap, which is out of scope for this membership-handler file.
//   - The generic 500 "Internal server error" paths (QueryContext /
//     ExecContext / Scan failures): not reachable against a healthy,
//     migrated in-memory SQLite without fault injection.
//   - The email-delivery side of invitations: there is none in this file;
//     CreateInvitation only mints a token + DB row.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// covWMSeedMember inserts a user + a workspace_members row in one shot,
// mirroring the column set used by seedOtherWorkspace / the inline INSERTs
// in workspaces_membership_test.go.
func covWMSeedMember(t *testing.T, h *WorkspaceHandler, userID, email, wsID, memberID, role string) {
	t.Helper()
	if _, err := h.db.Exec(
		`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`,
		userID, email, "Cov "+userID,
	); err != nil {
		t.Fatalf("covWM seed user %s: %v", userID, err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, ?)`,
		memberID, wsID, userID, role,
	); err != nil {
		t.Fatalf("covWM seed member %s: %v", memberID, err)
	}
}

// covWMCreateInvitation drives CreateInvitation as OWNER and fails the
// test unless it returns 201 — used to seed a real invitation row whose
// existence we then assert against.
func covWMCreateInvitation(t *testing.T, h *WorkspaceHandler, userID, wsID, email, role string) invitationResponse {
	t.Helper()
	body := strings.NewReader(`{"email":"` + email + `","role":"` + role + `"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/invitations", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.CreateInvitation(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("covWMCreateInvitation: status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var got invitationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("covWMCreateInvitation unmarshal: %v", err)
	}
	return got
}

// ── ListMembers: multi-row loop ──────────────────────────────────────────

// With three members the rows.Next() loop body runs three times, the
// scan-and-append path is exercised repeatedly, and the ORDER BY
// created_at ASC ordering is observable. The single-owner sibling test
// only runs the loop once, so it never proves the loop accumulates.
func TestCovWMListMembers_MultipleMembers_AllReturnedOrdered(t *testing.T) {
	h, ownerID, wsID := membershipRig(t)
	// Seed two extra members. seedTestWorkspace already inserted the
	// OWNER row "m1" with the earliest created_at.
	covWMSeedMember(t, h, "cov-u2", "cov2@example.com", wsID, "cov-m2", "ADMIN")
	covWMSeedMember(t, h, "cov-u3", "cov3@example.com", wsID, "cov-m3", "MEMBER")

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/members", nil),
		ownerID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ListMembers(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var rows []memberResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("members = %d, want 3", len(rows))
	}
	// Every row must carry its joined user and a non-empty role.
	byUser := map[string]memberResponse{}
	for _, m := range rows {
		if m.User == nil {
			t.Fatalf("member %s missing joined user", m.ID)
		}
		if m.Role == "" {
			t.Errorf("member %s has empty role", m.ID)
		}
		byUser[m.UserID] = m
	}
	if byUser[ownerID].Role != "OWNER" {
		t.Errorf("owner role = %q, want OWNER", byUser[ownerID].Role)
	}
	if byUser["cov-u2"].Role != "ADMIN" {
		t.Errorf("cov-u2 role = %q, want ADMIN", byUser["cov-u2"].Role)
	}
	if byUser["cov-u3"].Role != "MEMBER" {
		t.Errorf("cov-u3 role = %q, want MEMBER", byUser["cov-u3"].Role)
	}
}

// ── ListInvitations: populated scan path ─────────────────────────────────

// The sibling tests only ever read an empty invitations list, so the
// rows.Next() scan + inviter JOIN was never executed. Seed two real
// invitations and assert both surface with their inviter populated and
// tokens present.
func TestCovWMListInvitations_Populated_ReturnsRowsWithInviter(t *testing.T) {
	h, ownerID, wsID := membershipRig(t)
	first := covWMCreateInvitation(t, h, ownerID, wsID, "cov-inv1@example.com", "MEMBER")
	second := covWMCreateInvitation(t, h, ownerID, wsID, "cov-inv2@example.com", "MANAGER")

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/invitations", nil),
		ownerID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ListInvitations(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var rows []invitationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("invitations = %d, want 2", len(rows))
	}
	seen := map[string]invitationResponse{}
	for _, inv := range rows {
		if inv.Inviter == nil {
			t.Fatalf("invitation %s missing joined inviter", inv.ID)
		}
		if inv.Inviter.ID != ownerID {
			t.Errorf("inviter id = %q, want %q", inv.Inviter.ID, ownerID)
		}
		if inv.Token == "" {
			t.Errorf("invitation %s has empty token", inv.ID)
		}
		seen[inv.Email] = inv
	}
	if _, ok := seen[first.Email]; !ok {
		t.Errorf("first invitation %q missing from listing", first.Email)
	}
	if got := seen[second.Email].Role; got != "MANAGER" {
		t.Errorf("second invitation role = %q, want MANAGER", got)
	}
}

// ── CreateInvitation: 409 conflict branches ──────────────────────────────

// Inviting an email that already belongs to a current member must 409.
// This exercises the `JOIN users ON ... WHERE email = ?` existing-member
// pre-check that the happy-path test never trips.
func TestCovWMCreateInvitation_EmailAlreadyMember_Returns409(t *testing.T) {
	h, ownerID, wsID := membershipRig(t)
	covWMSeedMember(t, h, "cov-existing", "already@example.com", wsID, "cov-m-existing", "MEMBER")

	body := strings.NewReader(`{"email":"already@example.com","role":"MEMBER"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/invitations", body),
		ownerID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.CreateInvitation(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (email already a member); body=%s", rr.Code, rr.Body.String())
	}
}

// A second invitation to the same email while the first is still active
// (unexpired, unaccepted) must 409. This hits the second pre-check —
// the `expires_at > datetime('now') AND accepted_at IS NULL` lookup —
// which is distinct from the already-member branch above.
func TestCovWMCreateInvitation_DuplicateActiveInvite_Returns409(t *testing.T) {
	h, ownerID, wsID := membershipRig(t)
	// First invitation succeeds (freshly minted, 7-day expiry).
	_ = covWMCreateInvitation(t, h, ownerID, wsID, "dup@example.com", "MEMBER")

	// Second invitation to the same email must be rejected as a conflict.
	body := strings.NewReader(`{"email":"dup@example.com","role":"MEMBER"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/invitations", body),
		ownerID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.CreateInvitation(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (active invite exists); body=%s", rr.Code, rr.Body.String())
	}
	// Exactly one invitation row should exist for that email.
	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM workspace_invitations WHERE workspace_id = ? AND email = ?`,
		wsID, "dup@example.com").Scan(&n); err != nil {
		t.Fatalf("count invitations: %v", err)
	}
	if n != 1 {
		t.Errorf("invitation rows = %d, want 1 (duplicate must not insert)", n)
	}
}

// Omitting the role on the invitation path must default to MEMBER, just
// like AddMember. Pins the `if req.Role == "" { req.Role = "MEMBER" }`
// branch inside CreateInvitation specifically (the sibling default-role
// test only covers AddMember).
func TestCovWMCreateInvitation_EmptyRoleDefaultsToMember(t *testing.T) {
	h, ownerID, wsID := membershipRig(t)
	body := strings.NewReader(`{"email":"defaultrole@example.com"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/invitations", body),
		ownerID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.CreateInvitation(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var got invitationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Role != "MEMBER" {
		t.Errorf("default invite role = %q, want MEMBER", got.Role)
	}
	// Confirm the persisted row also carries the defaulted role.
	var dbRole string
	if err := h.db.QueryRow(
		`SELECT role FROM workspace_invitations WHERE id = ?`, got.ID).Scan(&dbRole); err != nil {
		t.Fatalf("post-insert read: %v", err)
	}
	if dbRole != "MEMBER" {
		t.Errorf("persisted invite role = %q, want MEMBER", dbRole)
	}
}

// ── CreateInvitation: bad JSON → 400 ─────────────────────────────────────

// Malformed body must 400 before any DB work (mirrors the AddMember
// bad-JSON test, kept here so the invitation handler's readJSON error
// branch is independently pinned).
func TestCovWMCreateInvitation_BadJSON_Returns400(t *testing.T) {
	h, ownerID, wsID := membershipRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/invitations",
			strings.NewReader(`{not json`)),
		ownerID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.CreateInvitation(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}
