package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Handler-level coverage for internal/api/workspaces_membership.go.
//
// The membership surface is paid-customer trust territory — any leak of one
// tenant's roster into another tenant's view, or any privilege-escalation
// path that lets a MEMBER mutate their own role, is a business-grade
// security incident. These tests pin the wire contract so a refactor of
// the SQL or the role gating cannot silently degrade tenant isolation.
//
// NOTE: workspaces_membership.go exposes ListMembers, AddMember,
// RemoveMember, ListInvitations, CreateInvitation. There is no
// UpdateMemberRole handler — see the file-level TODO at the bottom of this
// file for the gap analysis. The "Update role" scenario from the test
// brief is therefore covered indirectly by the AddMember role-whitelist
// tests (the role parameter on insert) plus a documented absence.

// membershipRig spins up an isolated SQLite DB, seeds one user + one
// workspace where the user is OWNER, and returns a configured
// WorkspaceHandler. seedTestWorkspace already inserts the OWNER row
// (member id "m1"), which we lean on for tenant-isolation tests.
func membershipRig(t *testing.T) (*WorkspaceHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewWorkspaceHandler(db, logger)
	return h, userID, wsID
}

// seedOtherUser inserts a second user we can invite / add / try to leak.
// We always use a distinct email so the users.email UNIQUE constraint
// stays happy.
func seedOtherUser(t *testing.T, h *WorkspaceHandler, id, email string) {
	t.Helper()
	if _, err := h.db.Exec(
		`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`,
		id, email, "Other "+id,
	); err != nil {
		t.Fatalf("seed other user %s: %v", id, err)
	}
}

// seedOtherWorkspace inserts a second workspace with the given user as
// OWNER. Used to assert that requests scoped to wsB cannot see wsA's
// rows.
func seedOtherWorkspace(t *testing.T, h *WorkspaceHandler, wsID, slug, ownerID string) {
	t.Helper()
	if _, err := h.db.Exec(
		`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`,
		wsID, "Other-"+slug, slug,
	); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'OWNER')`,
		"m-"+wsID, wsID, ownerID,
	); err != nil {
		t.Fatalf("seed other workspace owner: %v", err)
	}
}

// ── ListMembers ─────────────────────────────────────────────────────────

// Single-member workspace: the OWNER row seeded by seedTestWorkspace must
// surface. If it doesn't, every workspace-management UI breaks because
// the current user can never see their own role.
func TestWorkspaceMembership_ListMembers_SingleOwner_Returns200WithOwnerRow(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/members", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListMembers(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var rows []memberResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("members = %d, want exactly 1 (the seeded owner)", len(rows))
	}
	if rows[0].UserID != userID {
		t.Errorf("member.user_id = %q, want %q", rows[0].UserID, userID)
	}
	if rows[0].Role != "OWNER" {
		t.Errorf("member.role = %q, want OWNER", rows[0].Role)
	}
	if rows[0].User == nil || rows[0].User.Email != "test@example.com" {
		t.Errorf("expected joined user with seeded email, got %+v", rows[0].User)
	}
}

// Tenant-isolation gate: a caller authenticated against wsB MUST NOT see
// wsA's members. The handler scopes the SELECT by workspaceID; if that
// WHERE clause is ever dropped, this test fails immediately.
func TestWorkspaceMembership_ListMembers_CrossWorkspace_OnlyReturnsCallersWorkspace(t *testing.T) {
	h, userID, _ := membershipRig(t)
	// Create a second user + workspace with their own owner row.
	seedOtherUser(t, h, "user-B", "userb@example.com")
	wsB := "ws-B"
	seedOtherWorkspace(t, h, wsB, "wsb", "user-B")

	// Call from wsB context — should see only user-B as the owner, never
	// the original test-user-id from wsA.
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/workspaces/"+wsB+"/members", nil),
		"user-B", wsB, "OWNER")
	rr := httptest.NewRecorder()
	h.ListMembers(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var rows []memberResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("wsB members = %d, want exactly 1", len(rows))
	}
	if rows[0].UserID == userID {
		t.Fatalf("tenant isolation broken: wsB request surfaced wsA's user %q", userID)
	}
	if rows[0].UserID != "user-B" {
		t.Errorf("got user_id %q, want user-B", rows[0].UserID)
	}
}

// ── AddMember ───────────────────────────────────────────────────────────

// Forbidden gate: only OWNER/ADMIN ("manage") can add members. A
// MEMBER-level caller must be rejected before any DB work happens.
func TestWorkspaceMembership_AddMember_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	seedOtherUser(t, h, "user-target", "target@example.com")
	body := strings.NewReader(`{"user_id":"user-target","role":"MEMBER"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/members", body),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.AddMember(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

// VIEWER is below MEMBER and obviously cannot manage. Documents that the
// canRole("manage") gate uses an inclusive whitelist, not a blacklist.
func TestWorkspaceMembership_AddMember_ViewerRole_Returns403(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	body := strings.NewReader(`{"user_id":"someone","role":"MEMBER"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/members", body),
		userID, wsID, "VIEWER",
	)
	rr := httptest.NewRecorder()
	h.AddMember(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

// Privilege-escalation guard: an ADMIN may invite ordinary members but
// must not promote a new ADMIN — only OWNER can mint ADMINs. If this
// branch breaks, every workspace becomes one compromised ADMIN away from
// total takeover.
func TestWorkspaceMembership_AddMember_AdminAssigningAdmin_Returns403(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	seedOtherUser(t, h, "user-target", "target@example.com")
	body := strings.NewReader(`{"user_id":"user-target","role":"ADMIN"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/members", body),
		userID, wsID, "ADMIN",
	)
	rr := httptest.NewRecorder()
	h.AddMember(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

// Missing user_id is a 400. The endpoint cannot do anything useful
// without it and we want a deterministic error so the UI can surface a
// clean validation message instead of guessing from a 500.
func TestWorkspaceMembership_AddMember_MissingUserID_Returns400(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	body := strings.NewReader(`{"role":"MEMBER"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/members", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.AddMember(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// Garbage JSON body must also produce 400, not 500. Same reasoning as
// missing user_id — clients depend on the 400 to render validation UI.
func TestWorkspaceMembership_AddMember_BadJSON_Returns400(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/members",
			strings.NewReader(`{NOT_JSON`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.AddMember(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// Role whitelist: anything outside ADMIN/MANAGER/MEMBER/VIEWER must be
// rejected as 400. This is the "V-02" guard in the handler — a refactor
// could easily collapse it into a generic enum lookup and accidentally
// allow "OWNER" or "SUPERADMIN" through.
func TestWorkspaceMembership_AddMember_InvalidRole_Returns400(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	seedOtherUser(t, h, "user-target", "target@example.com")
	body := strings.NewReader(`{"user_id":"user-target","role":"SUPERADMIN"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/members", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.AddMember(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// The whitelist also blocks "OWNER" specifically — you cannot mint a
// second OWNER via the AddMember API. The handler returns 400 (not 403)
// because "OWNER" isn't in validAssignableRoles at all.
func TestWorkspaceMembership_AddMember_OwnerRoleAttempt_Returns400(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	seedOtherUser(t, h, "user-target", "target@example.com")
	body := strings.NewReader(`{"user_id":"user-target","role":"OWNER"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/members", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.AddMember(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (OWNER not in assignable role whitelist); body=%s",
			rr.Code, rr.Body.String())
	}
}

// Unknown user → 404. This is the existence check that runs AFTER the
// already-member check. We must avoid leaking "is X a member here?"
// information to unauthorized parties — but as OWNER the caller is
// authorized to ask, so 404 is the correct, informative response.
func TestWorkspaceMembership_AddMember_UnknownUser_Returns404(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	body := strings.NewReader(`{"user_id":"user-does-not-exist","role":"MEMBER"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/members", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.AddMember(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// Duplicate add → 409. Tries to insert a row that already exists; the
// handler's pre-check ought to catch it before the INSERT race-window
// even matters.
func TestWorkspaceMembership_AddMember_AlreadyMember_Returns409(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	// userID is already OWNER of wsID via seedTestWorkspace.
	body := strings.NewReader(`{"user_id":"` + userID + `","role":"MEMBER"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/members", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.AddMember(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
}

// Happy path: OWNER adds a fresh user with default MEMBER role. We
// verify both the HTTP response shape and that the row actually landed
// in workspace_members — a 201 with no DB write would be a silent
// regression.
func TestWorkspaceMembership_AddMember_HappyPath_Returns201AndPersists(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	seedOtherUser(t, h, "user-fresh", "fresh@example.com")
	body := strings.NewReader(`{"user_id":"user-fresh","role":"MEMBER"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/members", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.AddMember(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var got memberResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.UserID != "user-fresh" || got.Role != "MEMBER" || got.WorkspaceID != wsID {
		t.Errorf("response mismatch: %+v", got)
	}
	// Confirm the row landed.
	var role string
	if err := h.db.QueryRow(
		`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`,
		wsID, "user-fresh").Scan(&role); err != nil {
		t.Fatalf("post-insert read: %v", err)
	}
	if role != "MEMBER" {
		t.Errorf("persisted role = %q, want MEMBER", role)
	}
}

// Empty role string must default to MEMBER per the handler's
// `req.Role == ""` branch. Without this guard the V-02 whitelist
// validation would reject a request that omitted the field, which the
// UI sometimes does for the "invite as default member" case.
func TestWorkspaceMembership_AddMember_EmptyRoleDefaultsToMember(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	seedOtherUser(t, h, "user-default", "default@example.com")
	body := strings.NewReader(`{"user_id":"user-default"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/members", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.AddMember(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var got memberResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.Role != "MEMBER" {
		t.Errorf("default role = %q, want MEMBER", got.Role)
	}
}

// Tenant-isolation: an OWNER of wsB cannot add a member to wsA. The
// workspaceID comes from context — if the caller's context says wsB,
// the row lands in wsB regardless of what URL the caller scraped from
// somewhere. Verify that an OWNER of wsB context inserting userID will
// only affect wsB and never touch wsA's roster.
func TestWorkspaceMembership_AddMember_CrossWorkspace_DoesNotTouchOtherTenant(t *testing.T) {
	h, _, wsA := membershipRig(t)
	seedOtherUser(t, h, "user-B", "userb@example.com")
	wsB := "ws-B"
	seedOtherWorkspace(t, h, wsB, "wsb", "user-B")
	seedOtherUser(t, h, "user-victim", "victim@example.com")

	// user-B (OWNER of wsB) issues an AddMember request. Even though
	// the route looks like wsA's URL, context says wsB → row must land
	// in wsB, NOT wsA.
	body := strings.NewReader(`{"user_id":"user-victim","role":"MEMBER"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsA+"/members", body),
		"user-B", wsB, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.AddMember(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	// wsA must NOT now contain user-victim.
	var leaked int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ? AND user_id = ?`,
		wsA, "user-victim").Scan(&leaked); err != nil {
		t.Fatalf("isolation check: %v", err)
	}
	if leaked != 0 {
		t.Fatalf("tenant isolation broken: user-victim leaked into wsA (%d rows)", leaked)
	}
}

// ── RemoveMember ────────────────────────────────────────────────────────

// MEMBER role cannot delete anyone. The canRole("manage") gate runs
// before any path-value parsing, so the same 403 applies regardless of
// the target memberId.
func TestWorkspaceMembership_RemoveMember_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/members/m1", nil),
		userID, wsID, "MEMBER",
	)
	req.SetPathValue("memberId", "m1")
	rr := httptest.NewRecorder()
	h.RemoveMember(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

// Owner-protection guard: trying to delete a member whose role is OWNER
// returns 403, NOT 200/204. The handler currently blocks removal of ALL
// owners — including the user themselves when they are the sole owner.
// See file-bottom note for the "demote last owner" gap.
func TestWorkspaceMembership_RemoveMember_OwnerTarget_Returns403(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	// m1 is the OWNER row seeded by seedTestWorkspace.
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/members/m1", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("memberId", "m1")
	rr := httptest.NewRecorder()
	h.RemoveMember(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (cannot remove workspace owner); body=%s",
			rr.Code, rr.Body.String())
	}
}

// Unknown memberId → 404. Importantly, OWNER role with a bogus id still
// gets 404 (not 403) because the role check passes and only the
// SELECT-then-not-found path triggers.
func TestWorkspaceMembership_RemoveMember_UnknownMember_Returns404(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/members/nope", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("memberId", "nope")
	rr := httptest.NewRecorder()
	h.RemoveMember(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// Happy path: OWNER removes a non-owner. We verify the 200 response,
// success-payload shape, and that the row is actually gone from the
// table — a 200 with the row still present would be a silent leak.
func TestWorkspaceMembership_RemoveMember_OwnerRemovingMember_Returns200AndDeletes(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	// Insert a MEMBER row to delete.
	seedOtherUser(t, h, "user-doomed", "doomed@example.com")
	if _, err := h.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'MEMBER')`,
		"m-doomed", wsID, "user-doomed",
	); err != nil {
		t.Fatalf("seed doomed member: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/members/m-doomed", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("memberId", "m-doomed")
	rr := httptest.NewRecorder()
	h.RemoveMember(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]bool
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp["success"] {
		t.Errorf("response missing success:true, got %v", resp)
	}
	// Confirm the row is gone.
	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM workspace_members WHERE id = ?`,
		"m-doomed").Scan(&n); err != nil {
		t.Fatalf("post-delete count: %v", err)
	}
	if n != 0 {
		t.Errorf("row still present after 200: %d", n)
	}
}

// Tenant-isolation on the destructive path: caller authenticated against
// wsB tries to delete a wsA member by id. Handler SELECTs with
// `id = ? AND workspace_id = ?`, so the row from wsA looks like 404
// from wsB's perspective — NEVER 403 (which would confirm existence)
// and NEVER 200 (which would actually delete cross-tenant).
func TestWorkspaceMembership_RemoveMember_CrossWorkspace_Returns404(t *testing.T) {
	h, _, wsA := membershipRig(t)
	// Seed a member in wsA (besides the owner).
	seedOtherUser(t, h, "user-A2", "usera2@example.com")
	if _, err := h.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'MEMBER')`,
		"m-A2", wsA, "user-A2",
	); err != nil {
		t.Fatalf("seed wsA member: %v", err)
	}
	// Stand up wsB with its own owner.
	seedOtherUser(t, h, "user-B", "userb@example.com")
	wsB := "ws-B"
	seedOtherWorkspace(t, h, wsB, "wsb", "user-B")

	// From wsB's OWNER context, try to delete wsA's member by id.
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsB+"/members/m-A2", nil),
		"user-B", wsB, "OWNER",
	)
	req.SetPathValue("memberId", "m-A2")
	rr := httptest.NewRecorder()
	h.RemoveMember(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace delete leaked existence: status = %d, want 404", rr.Code)
	}
	// Row in wsA must still be present.
	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM workspace_members WHERE id = ?`,
		"m-A2").Scan(&n); err != nil {
		t.Fatalf("verify wsA row: %v", err)
	}
	if n != 1 {
		t.Fatalf("wsA member was deleted cross-tenant: count=%d", n)
	}
}

// ── CreateInvitation ────────────────────────────────────────────────────
//
// The invitation flow is the second half of "add member" — covered here
// for the same role-gate + tenant-isolation reasons.

// MEMBER cannot create invitations: the manage gate runs first.
func TestWorkspaceMembership_CreateInvitation_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	body := strings.NewReader(`{"email":"new@example.com","role":"MEMBER"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/invitations", body),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.CreateInvitation(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

// Missing email → 400. The handler short-circuits before any DB work.
func TestWorkspaceMembership_CreateInvitation_MissingEmail_Returns400(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	body := strings.NewReader(`{"role":"MEMBER"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/invitations", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.CreateInvitation(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// Same V-03 whitelist guard as AddMember — must reject "OWNER" /
// arbitrary strings as 400. This prevents an ADMIN from minting
// invitations that auto-promote to OWNER on acceptance.
func TestWorkspaceMembership_CreateInvitation_InvalidRole_Returns400(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	body := strings.NewReader(`{"email":"new@example.com","role":"SUPERADMIN"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/invitations", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.CreateInvitation(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// ADMIN cannot invite an ADMIN. Same privilege-escalation reasoning as
// AddMember; the guard lives in two places (add + invite) and we need to
// pin both.
func TestWorkspaceMembership_CreateInvitation_AdminInvitingAdmin_Returns403(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	body := strings.NewReader(`{"email":"newadmin@example.com","role":"ADMIN"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/invitations", body),
		userID, wsID, "ADMIN",
	)
	rr := httptest.NewRecorder()
	h.CreateInvitation(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

// Happy path: OWNER invites a brand-new email; row lands in
// workspace_invitations with a generated token. The response body
// includes the token (used in the magic-link URL).
func TestWorkspaceMembership_CreateInvitation_HappyPath_Returns201AndPersists(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	// UserFromContext is read inside the handler — withWorkspaceUser
	// already sets it (ID + Email).
	body := strings.NewReader(`{"email":"invitee@example.com","role":"MEMBER"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/invitations", body),
		userID, wsID, "OWNER",
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
	if got.Email != "invitee@example.com" || got.Role != "MEMBER" || got.WorkspaceID != wsID {
		t.Errorf("response mismatch: %+v", got)
	}
	if got.Token == "" {
		t.Error("expected non-empty invitation token")
	}
	// Persisted?
	var dbEmail, dbRole string
	if err := h.db.QueryRow(
		`SELECT email, role FROM workspace_invitations WHERE id = ?`, got.ID,
	).Scan(&dbEmail, &dbRole); err != nil {
		t.Fatalf("post-insert read: %v", err)
	}
	if dbEmail != "invitee@example.com" || dbRole != "MEMBER" {
		t.Errorf("DB row mismatch: email=%q role=%q", dbEmail, dbRole)
	}
}

// ── ListInvitations ─────────────────────────────────────────────────────

// Empty workspace returns 200 + []. The seeded workspace has zero
// pending invitations.
func TestWorkspaceMembership_ListInvitations_Empty_Returns200WithEmptySlice(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/invitations", nil),
		userID, wsID, "OWNER",
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
	if len(rows) != 0 {
		t.Errorf("rows = %d, want 0", len(rows))
	}
}

// Tenant-isolation gate: invitations created in wsA must not appear in
// wsB's listing. We create one in wsA, then read from wsB.
func TestWorkspaceMembership_ListInvitations_CrossWorkspace_OnlyReturnsCallersWorkspace(t *testing.T) {
	h, userID, wsA := membershipRig(t)
	// Seed an invitation in wsA.
	body := strings.NewReader(`{"email":"wsa-invitee@example.com","role":"MEMBER"}`)
	reqCreate := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsA+"/invitations", body),
		userID, wsA, "OWNER",
	)
	rrCreate := httptest.NewRecorder()
	h.CreateInvitation(rrCreate, reqCreate)
	if rrCreate.Code != http.StatusCreated {
		t.Fatalf("seed invitation: status = %d, body=%s", rrCreate.Code, rrCreate.Body.String())
	}

	// Spin up wsB.
	seedOtherUser(t, h, "user-B", "userb@example.com")
	wsB := "ws-B"
	seedOtherWorkspace(t, h, wsB, "wsb", "user-B")

	// Read wsB's invitations — must be empty even though wsA has one.
	reqList := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsB+"/invitations", nil),
		"user-B", wsB, "OWNER",
	)
	rrList := httptest.NewRecorder()
	h.ListInvitations(rrList, reqList)
	if rrList.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rrList.Code)
	}
	var rows []invitationResponse
	if err := json.Unmarshal(rrList.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("tenant isolation broken: wsB saw %d wsA invitation(s)", len(rows))
	}
}

// ── Notes / gaps discovered while writing this file ─────────────────────
//
// 1) There is NO UpdateMemberRole handler in workspaces_membership.go.
//    The "promote member to admin / demote admin to member" flow has no
//    HTTP surface today — the only way to change a member's role is to
//    DELETE + re-POST. Worth a TODO for the membership UI epic; we
//    deliberately did NOT add a test for an endpoint that doesn't exist.
//
// 2) RemoveMember rejects ANY removal whose target row has role=OWNER
//    with a 403 (see TestWorkspaceMembership_RemoveMember_OwnerTarget).
//    There is no "is this the LAST owner?" guard — but because every
//    OWNER is blocked, the failure mode is "stuck with too many owners"
//    rather than "workspace orphaned". Skipping the
//    "demote-last-owner" scenario from the test brief intentionally;
//    there is no demotion API to test.
//
// 3) AddMember has NO check that the target user is not already a
//    member of OTHER workspaces — by design, cross-workspace
//    membership is allowed. The cross-workspace isolation test above
//    verifies the row lands in the CALLER's workspace context, which
//    is the contract that matters.
