package api

// RemoveMember × page-owner transfer (docs/prd/pages.md §7.1 rule 1b,
// issue #1952).
//
// A page owner leaves the workspace through exactly one door in this
// package: WorkspaceHandler.RemoveMember (there is no self-service "leave
// workspace" endpoint — see the comment on RemoveMember in
// workspaces_membership.go). Before this fix, RemoveMember deleted the
// workspace_members row and called nothing else, so a departed member kept
// isPageOwner()==true and mayEditSpec()==true forever: worse than the
// orphan §7.1 rule 1b forbids, because the page LOOKED owned and nobody was
// notified.
//
// These tests mirror TestPagesOwnerTransfer_* in pages_transfer_owner_test.go
// — same ordered rule (most panels, else member's crew, else refuse), same
// journal + notification contract — but drive them through RemoveMember
// instead of the GDPR erasure cascade.

import (
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"
)

// wmSeedCrew, wmSeedCrewMember, wmSeedPage and wmSeedPanel mirror the
// pagesTransferRig helpers of the same shape (pages_transfer_owner_test.go)
// but operate directly on a *sql.DB, since WorkspaceHandler's test rig
// (membershipRig) has no equivalent seeding surface of its own.

func wmSeedCrew(t *testing.T, db *sql.DB, wsID, id, name, slug string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, allowed_domains)
		VALUES (?, ?, ?, ?, 'free', '[]')`, id, wsID, name, slug); err != nil {
		t.Fatalf("seed crew %s: %v", id, err)
	}
}

func wmSeedCrewMember(t *testing.T, db *sql.DB, crewID, userID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO crew_members (id, crew_id, user_id) VALUES (?, ?, ?)`,
		"cm-"+crewID+"-"+userID, crewID, userID); err != nil {
		t.Fatalf("seed crew_members %s/%s: %v", crewID, userID, err)
	}
}

// wmSeedPage inserts a page owned by exactly one of ownerUserID /
// ownerCrewID (the schema's XOR) — pass "" for whichever does not apply.
func wmSeedPage(t *testing.T, db *sql.DB, wsID, id, slug, name, ownerUserID, ownerCrewID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO pages (id, workspace_id, slug, name, owner_user_id, owner_crew_id, spec_json)
		VALUES (?, ?, ?, ?, ?, ?, '{}')`,
		id, wsID, slug, name, nilIfEmpty(ownerUserID), nilIfEmpty(ownerCrewID)); err != nil {
		t.Fatalf("seed page %s: %v", id, err)
	}
}

func wmSeedPanel(t *testing.T, db *sql.DB, id, pageID, ownerCrewID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO page_panels
		(id, page_id, panel_id, schema, owner_crew_id, producer_kind, producer_ref, sla_seconds)
		VALUES (?, ?, ?, 'status.v1', ?, 'script', 'script/watch.sh', 60)`,
		id, pageID, id, ownerCrewID); err != nil {
		t.Fatalf("seed page_panels %s: %v", id, err)
	}
}

// wmRemoveMemberRig wires a WorkspaceHandler over a real sqlite, seeded (via
// membershipRig) with one OWNER who will act as the remover, plus one
// departing MEMBER whose workspace_members row this test removes.
type wmRemoveMemberRig struct {
	h          *WorkspaceHandler
	db         *sql.DB
	spy        *pagesJournalSpy
	wsID       string
	ownerID    string // the actor performing the removal
	memberID   string // the workspace_members row id being deleted
	memberUser string // the departing member's user_id
}

func wmRemoveMemberSetup(t *testing.T) *wmRemoveMemberRig {
	t.Helper()
	h, ownerID, wsID := membershipRig(t)
	spy := &pagesJournalSpy{}
	h.SetJournal(spy)

	const memberUser = "wm-departing"
	seedOtherUser(t, h, memberUser, "wm-departing@example.com")
	const memberRowID = "wm-member-row"
	if _, err := h.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'MEMBER')`,
		memberRowID, wsID, memberUser,
	); err != nil {
		t.Fatalf("seed departing member: %v", err)
	}

	return &wmRemoveMemberRig{
		h: h, db: h.db, spy: spy, wsID: wsID,
		ownerID: ownerID, memberID: memberRowID, memberUser: memberUser,
	}
}

func (r *wmRemoveMemberRig) removeReq(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/workspaces/"+r.wsID+"/members/"+r.memberID, nil),
		r.ownerID, r.wsID, "OWNER",
	)
	req.SetPathValue("memberId", r.memberID)
	rr := httptest.NewRecorder()
	r.h.RemoveMember(rr, req)
	return rr
}

// ── The ordered rule itself (§7.1 rule 1b), reached through RemoveMember ──

func TestWorkspaceMembership_RemoveMember_TransfersPage_MostPanelsWins(t *testing.T) {
	r := wmRemoveMemberSetup(t)
	wmSeedCrew(t, r.db, r.wsID, "crewA", "Crew A", "crew-a")
	wmSeedCrew(t, r.db, r.wsID, "crewB", "Crew B", "crew-b")
	wmSeedCrewMember(t, r.db, "crewA", r.memberUser) // the departing user's own crew
	wmSeedPage(t, r.db, r.wsID, "page1", "flotila", "Flotila .201", r.memberUser, "")
	wmSeedPanel(t, r.db, "p1", "page1", "crewB")
	wmSeedPanel(t, r.db, "p2", "page1", "crewB")
	wmSeedPanel(t, r.db, "p3", "page1", "crewA")

	rr := r.removeReq(t)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var ownerUserID sql.NullString
	var ownerCrewID sql.NullString
	if err := r.db.QueryRow(`SELECT owner_user_id, owner_crew_id FROM pages WHERE id = 'page1'`).
		Scan(&ownerUserID, &ownerCrewID); err != nil {
		t.Fatalf("load page: %v", err)
	}
	if ownerUserID.Valid {
		t.Errorf("owner_user_id still set after transfer: %v", ownerUserID.String)
	}
	if !ownerCrewID.Valid || ownerCrewID.String != "crewB" {
		t.Errorf("owner_crew_id = %v, want crewB (most panels)", ownerCrewID)
	}

	entry := r.spy.firstOfType(entryPageOwnerTransferred)
	if entry == nil {
		t.Fatal("no page.owner_transferred journal entry was emitted")
	}
	if got, _ := entry.Payload["reason"].(string); got != "most_panels" {
		t.Errorf("journal reason = %q, want most_panels", got)
	}

	// The membership row is actually gone — the removal itself must still
	// take effect once the page is safely resolved.
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE id = ?`, r.memberID).Scan(&n); err != nil {
		t.Fatalf("count member row: %v", err)
	}
	if n != 0 {
		t.Errorf("workspace_members row survived a successful removal: count = %d", n)
	}
}

func TestWorkspaceMembership_RemoveMember_TransfersPage_FallsBackToMemberCrew(t *testing.T) {
	r := wmRemoveMemberSetup(t)
	wmSeedCrew(t, r.db, r.wsID, "crewA", "Crew A", "crew-a")
	wmSeedCrewMember(t, r.db, "crewA", r.memberUser)
	wmSeedPage(t, r.db, r.wsID, "page1", "flotila", "Flotila .201", r.memberUser, "")
	// No panels at all — rule 1 misses, rule 2 must resolve to crewA.

	rr := r.removeReq(t)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
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
}

// ── The refusal: no crew resolves ───────────────────────────────────────

// A member who owns a page cannot be removed if no crew can be resolved to
// take over ownership — the removal must be refused outright, not partially
// applied. This is issue #1952's core assertion: no departed member may keep
// owner authority over a page, so when the transfer can't happen, NEITHER
// can the removal.
func TestWorkspaceMembership_RemoveMember_RefusesWhenNoCrewResolves(t *testing.T) {
	r := wmRemoveMemberSetup(t)
	// No crews seeded at all: rule 1 (panels) and rule 2 (member's crew)
	// both miss.
	wmSeedPage(t, r.db, r.wsID, "page1", "orphan-risk", "Orphan Risk", r.memberUser, "")

	rr := r.removeReq(t)
	if rr.Code != 409 {
		t.Fatalf("status = %d, want 409 Conflict; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "orphan-risk") {
		t.Errorf("refusal message does not name the page: %s", rr.Body.String())
	}

	// The member must still be present — a refused transfer must not
	// partially apply the removal.
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE id = ?`, r.memberID).Scan(&n); err != nil {
		t.Fatalf("count member row: %v", err)
	}
	if n != 1 {
		t.Fatalf("member row was deleted despite the pages transfer refusing: count = %d, want 1", n)
	}

	// The page's ownership must be untouched — still the departed user,
	// never silently reassigned or orphaned.
	var ownerUserID sql.NullString
	var ownerCrewID sql.NullString
	if err := r.db.QueryRow(`SELECT owner_user_id, owner_crew_id FROM pages WHERE id = 'page1'`).
		Scan(&ownerUserID, &ownerCrewID); err != nil {
		t.Fatalf("load page: %v", err)
	}
	if !ownerUserID.Valid || ownerUserID.String != r.memberUser {
		t.Errorf("refusal must not change the owner: owner_user_id = %v", ownerUserID)
	}
	if ownerCrewID.Valid {
		t.Errorf("refusal must not invent a crew owner: owner_crew_id = %v", ownerCrewID)
	}
}

// ── A crew-owned page needs no transfer and must not be touched ────────

func TestWorkspaceMembership_RemoveMember_CrewOwnedPageUntouched(t *testing.T) {
	r := wmRemoveMemberSetup(t)
	wmSeedCrew(t, r.db, r.wsID, "crewA", "Crew A", "crew-a")
	wmSeedCrewMember(t, r.db, "crewA", r.memberUser)
	// Owned by a crew already — unrelated to the departing member.
	wmSeedPage(t, r.db, r.wsID, "pageCrew", "crew-board", "Crew Board", "", "crewA")

	rr := r.removeReq(t)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var ownerCrewID string
	var ownerUserID sql.NullString
	if err := r.db.QueryRow(`SELECT owner_crew_id, owner_user_id FROM pages WHERE id = 'pageCrew'`).
		Scan(&ownerCrewID, &ownerUserID); err != nil {
		t.Fatalf("load page: %v", err)
	}
	if ownerCrewID != "crewA" || ownerUserID.Valid {
		t.Errorf("crew-owned page was touched: owner_crew_id=%q owner_user_id=%v", ownerCrewID, ownerUserID)
	}
	if entry := r.spy.firstOfType(entryPageOwnerTransferred); entry != nil {
		t.Errorf("a journal entry was emitted for a page that needed no transfer: %+v", entry)
	}
	var notices int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id = 'pageCrew'`).Scan(&notices); err != nil {
		t.Fatalf("query inbox_items: %v", err)
	}
	if notices != 0 {
		t.Errorf("a notification was sent for a page that needed no transfer: %d", notices)
	}
}

// ── Notification lands ──────────────────────────────────────────────────

func TestWorkspaceMembership_RemoveMember_NotifiesWorkspaceAdmin(t *testing.T) {
	r := wmRemoveMemberSetup(t)
	wmSeedCrew(t, r.db, r.wsID, "crewA", "Crew A", "crew-a")
	wmSeedCrewMember(t, r.db, "crewA", r.memberUser)
	wmSeedPage(t, r.db, r.wsID, "page1", "flotila", "Flotila .201", r.memberUser, "")

	rr := r.removeReq(t)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var count int
	var targetRole, kind string
	if err := r.db.QueryRow(`
		SELECT COUNT(*), MAX(target_role), MAX(kind)
		FROM inbox_items WHERE workspace_id = ? AND source_id = 'page1'`,
		r.wsID).Scan(&count, &targetRole, &kind); err != nil {
		t.Fatalf("query inbox_items: %v", err)
	}
	if count != 1 {
		t.Fatalf("inbox notification for the transfer = %d rows, want 1", count)
	}
	if targetRole != "ADMIN" {
		t.Errorf("target_role = %q, want ADMIN (visible to ADMIN and OWNER)", targetRole)
	}
	if kind != "message" {
		t.Errorf("kind = %q, want message", kind)
	}
}

// ── The negative issue #1952 calls out explicitly ───────────────────────

// No code path deletes a page as a side effect of removing a member — the
// page count before and after a successful removal must be identical,
// whether the page transferred, was crew-owned already, or needed no
// transfer at all.
func TestWorkspaceMembership_RemoveMember_NeverDeletesAPage(t *testing.T) {
	r := wmRemoveMemberSetup(t)
	wmSeedCrew(t, r.db, r.wsID, "crewA", "Crew A", "crew-a")
	wmSeedCrewMember(t, r.db, "crewA", r.memberUser)
	wmSeedPage(t, r.db, r.wsID, "page1", "flotila", "Flotila .201", r.memberUser, "")
	wmSeedPage(t, r.db, r.wsID, "pageCrew", "crew-board", "Crew Board", "", "crewA")

	var before int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id = ?`, r.wsID).Scan(&before); err != nil {
		t.Fatalf("count pages before: %v", err)
	}

	rr := r.removeReq(t)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var after int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id = ?`, r.wsID).Scan(&after); err != nil {
		t.Fatalf("count pages after: %v", err)
	}
	if after != before {
		t.Fatalf("page count changed from %d to %d — a page was deleted as a side effect of removing a member", before, after)
	}
}

// A member who owns NO page at all is removed exactly as before — the new
// pages-transfer step must be a no-op, not a new failure mode, for the
// overwhelmingly common case.
func TestWorkspaceMembership_RemoveMember_NoPagesOwned_StillWorks(t *testing.T) {
	r := wmRemoveMemberSetup(t)

	rr := r.removeReq(t)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE id = ?`, r.memberID).Scan(&n); err != nil {
		t.Fatalf("count member row: %v", err)
	}
	if n != 0 {
		t.Errorf("workspace_members row survived a successful removal: count = %d", n)
	}
}
