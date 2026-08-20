package api

// RemoveMember × crew memberships (issue #1976, defect 1).
//
// Removing a member deleted the workspace_members row and nothing else, so
// every crew_members row the departing user held in that workspace survived
// the departure. Those rows are load-bearing, not cosmetic:
//
//   - CrewRoleFromDB (rbac.go) LEFT JOINs crew_members and returns
//     effectiveRole(workspaceRole, crewOverride) — a stale crew role ELEVATES
//     a user who is later re-added at a lower workspace role.
//   - crew membership alone grants access to crew-owned pages
//     (pages_authz.go) and to crew credentials (credentials_loaders.go).
//
// crew_members has no workspace_id of its own (prisma/schema.prisma), so the
// purge is scoped through crews — and it must run AFTER
// transferDepartingUserPages, whose rule 2 ("the crew the departing user
// belonged to") reads exactly the rows being purged.
//
// The rig (wmRemoveMemberRig, wmSeedCrew, wmSeedCrewMember, wmSeedPage) is
// shared with workspaces_membership_pages_transfer_test.go.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func wmCountCrewMemberships(t *testing.T, r *wmRemoveMemberRig, userID string) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM crew_members WHERE user_id = ?`, userID).Scan(&n); err != nil {
		t.Fatalf("count crew_members for %s: %v", userID, err)
	}
	return n
}

// ── The defect itself ───────────────────────────────────────────────────

// A member removed from the workspace keeps no crew membership behind them.
// Before the fix this returned 1: the workspace_members row went, the
// crew_members row stayed, and the departed user still passed every
// crew-scoped gate that only asks "is there a crew_members row?".
func TestRemoveMember_PurgesCrewMemberships(t *testing.T) {
	r := wmRemoveMemberSetup(t)
	wmSeedCrew(t, r.db, r.wsID, "crewA", "Crew A", "crew-a")
	wmSeedCrew(t, r.db, r.wsID, "crewB", "Crew B", "crew-b")
	wmSeedCrewMember(t, r.db, "crewA", r.memberUser)
	wmSeedCrewMember(t, r.db, "crewB", r.memberUser)
	// The remaining OWNER is in crewA too — the purge must be scoped to the
	// departing user, not to the crew.
	wmSeedCrewMember(t, r.db, "crewA", r.ownerID)

	rr := r.removeReq(t)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	if n := wmCountCrewMemberships(t, r, r.memberUser); n != 0 {
		t.Errorf("crew_members rows for the departed user = %d, want 0", n)
	}
	if n := wmCountCrewMemberships(t, r, r.ownerID); n != 1 {
		t.Errorf("crew_members rows for the staying OWNER = %d, want 1 — the purge hit the wrong user", n)
	}
}

// The guard against an unscoped `DELETE FROM crew_members WHERE user_id = ?`.
// crew_members carries no workspace_id, so a purge that forgets to scope
// through crews silently evicts the same person from every crew they hold in
// every OTHER workspace on the instance — a cross-tenant data loss triggered
// by one tenant's routine offboarding.
func TestRemoveMember_LeavesCrewMembershipsInOtherWorkspaces(t *testing.T) {
	r := wmRemoveMemberSetup(t)
	wmSeedCrew(t, r.db, r.wsID, "crewA", "Crew A", "crew-a")
	wmSeedCrewMember(t, r.db, "crewA", r.memberUser)

	// A second workspace the same user belongs to, with a crew of its own.
	const wsB = "ws-other"
	seedOtherWorkspace(t, r.h, wsB, "ws-other", r.memberUser)
	wmSeedCrew(t, r.db, wsB, "crewOther", "Crew Other", "crew-other")
	wmSeedCrewMember(t, r.db, "crewOther", r.memberUser)

	rr := r.removeReq(t)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var n int
	if err := r.db.QueryRow(
		`SELECT COUNT(*) FROM crew_members WHERE user_id = ? AND crew_id = 'crewOther'`,
		r.memberUser).Scan(&n); err != nil {
		t.Fatalf("count crew_members in the other workspace: %v", err)
	}
	if n != 1 {
		t.Fatalf("the user's crew membership in workspace %s was destroyed by a removal from another workspace: count = %d, want 1", wsB, n)
	}

	// And their membership in the workspace they actually left is gone.
	if err := r.db.QueryRow(
		`SELECT COUNT(*) FROM crew_members WHERE user_id = ? AND crew_id = 'crewA'`,
		r.memberUser).Scan(&n); err != nil {
		t.Fatalf("count crew_members in the departed workspace: %v", err)
	}
	if n != 0 {
		t.Errorf("crew_members row in the departed workspace = %d, want 0", n)
	}
}

// ── Ordering: purge AFTER the page transfer resolves ────────────────────

// The page-transfer fallback (pages_transfer_owner.go, §7.1 rule 1b rule 2)
// resolves "the crew the departing user belonged to" by reading crew_members
// for that user. Purging before the transfer makes rule 2 stop resolving, and
// a removal that used to succeed turns into a 409 refusal. This test is the
// ordering constraint, stated as behaviour: the page must transfer AND the
// crew rows must be gone, in that order, from the same removal.
func TestRemoveMember_PurgesAfterPageTransferResolves(t *testing.T) {
	r := wmRemoveMemberSetup(t)
	wmSeedCrew(t, r.db, r.wsID, "crewA", "Crew A", "crew-a")
	wmSeedCrewMember(t, r.db, "crewA", r.memberUser)
	// No panels anywhere: rule 1 (most panels) misses, so crew membership is
	// the ONLY thing that can resolve this page's new owner.
	wmSeedPage(t, r.db, r.wsID, "page1", "flotila", "Flotila .201", r.memberUser, "")

	rr := r.removeReq(t)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200 — the crew purge ran before the page transfer could resolve rule 2; body=%s",
			rr.Code, rr.Body.String())
	}

	var ownerCrewID string
	if err := r.db.QueryRow(`SELECT owner_crew_id FROM pages WHERE id = 'page1'`).Scan(&ownerCrewID); err != nil {
		t.Fatalf("load page: %v", err)
	}
	if ownerCrewID != "crewA" {
		t.Errorf("owner_crew_id = %q, want crewA (member_crew fallback)", ownerCrewID)
	}
	entry := r.spy.firstOfType(entryPageOwnerTransferred)
	if entry == nil {
		t.Fatal("no page.owner_transferred journal entry was emitted")
	}
	if got, _ := entry.Payload["reason"].(string); got != "member_crew" {
		t.Errorf("journal reason = %q, want member_crew", got)
	}

	if n := wmCountCrewMemberships(t, r, r.memberUser); n != 0 {
		t.Errorf("crew_members rows for the departed user = %d, want 0 — the transfer ran but the purge did not", n)
	}
}

// ── The security impact, stated sharply ─────────────────────────────────

// A user removed from the workspace while holding a per-crew ADMIN override,
// then re-added as a plain MEMBER, must come back as a MEMBER. Before the
// fix the stale crew_members.role survived the departure and
// effectiveRole(workspaceRole, crewOverride) handed the re-added user back
// their ADMIN authority inside that crew — an elevation nobody granted, on a
// door (AddMember) that never looks at crew_members at all.
func TestRemoveMember_ThenReAdd_DoesNotRestoreStaleCrewElevation(t *testing.T) {
	r := wmRemoveMemberSetup(t)
	wmSeedCrew(t, r.db, r.wsID, "crewA", "Crew A", "crew-a")
	wmSeedCrewMember(t, r.db, "crewA", r.memberUser)
	if _, err := r.db.Exec(
		`UPDATE crew_members SET role = 'ADMIN' WHERE crew_id = 'crewA' AND user_id = ?`,
		r.memberUser); err != nil {
		t.Fatalf("set per-crew ADMIN override: %v", err)
	}

	// Sanity: the elevation is real before the removal.
	before, err := CrewRoleFromDB(t.Context(), r.db, r.memberUser, "crewA")
	if err != nil {
		t.Fatalf("CrewRoleFromDB before removal: %v", err)
	}
	if before != "ADMIN" {
		t.Fatalf("precondition: effective crew role = %q, want ADMIN", before)
	}

	if rr := r.removeReq(t); rr.Code != 200 {
		t.Fatalf("remove status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	// Re-add at MEMBER through the real handler — AddMember inspects only
	// workspace_members, exactly as in production.
	body := strings.NewReader(`{"user_id":"` + r.memberUser + `","role":"MEMBER"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+r.wsID+"/members", body),
		r.ownerID, r.wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	r.h.AddMember(rr, req)
	if rr.Code != 201 {
		t.Fatalf("re-add status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var added memberResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &added); err != nil {
		t.Fatalf("unmarshal re-add response: %v", err)
	}
	if added.Role != "MEMBER" {
		t.Fatalf("re-added at role %q, want MEMBER", added.Role)
	}

	after, err := CrewRoleFromDB(t.Context(), r.db, r.memberUser, "crewA")
	if err != nil {
		t.Fatalf("CrewRoleFromDB after re-add: %v", err)
	}
	if after == "ADMIN" {
		t.Fatalf("a user re-added as MEMBER is still crew ADMIN — a stale crew_members.role survived the removal and re-elevated them")
	}
	if after != "MEMBER" {
		t.Errorf("effective crew role after re-add = %q, want MEMBER (the workspace role, no override)", after)
	}
}
