package api

// Owner-departure transfer — tests for pages_transfer_owner.go and its
// wiring into the user-erasure handler (admin_gdpr.go DeleteUserData).
// docs/prd/pages.md §7.1 rule 1b, issue #1944.
//
// Table-driven core: TestPagesOwnerTransfer_TargetCrewResolution covers the
// ordered rule itself (most panels, else member crew, else refuse). The
// remaining tests cover what surrounds that rule: the page survives, the
// notification and journal entry land, a crew-owned page is never touched,
// and — the negative the issue calls out explicitly — no code path deletes
// a page as a side effect of erasing its owner.

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// pagesTransferRig wires an AdminGDPRHandler over a real sqlite, seeded
// with one workspace, one ADMIN and one departing MEMBER. Tests add crews,
// crew membership and pages on top as each case needs.
type pagesTransferRig struct {
	h       *AdminGDPRHandler
	db      *sql.DB
	spy     *pagesJournalSpy
	wsID    string
	adminID string
	userID  string // the departing user
}

func pagesTransferSetup(t *testing.T) *pagesTransferRig {
	t.Helper()
	dbh := testutil.MigratedDB(t)
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := dbh.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ptws1','PT','pt')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO users (id, email) VALUES ('ptadmin','ptadmin@x'),('ptdeparting','ptdeparting@x')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role)
		VALUES ('ptm1','ptws1','ptadmin','OWNER'),('ptm2','ptws1','ptdeparting','MEMBER')`); err != nil {
		t.Fatalf("seed members: %v", err)
	}
	h := NewAdminGDPRHandler(dbh.DB, silent, t.TempDir())
	spy := &pagesJournalSpy{}
	h.SetJournal(spy)
	return &pagesTransferRig{h: h, db: dbh.DB, spy: spy, wsID: "ptws1", adminID: "ptadmin", userID: "ptdeparting"}
}

func (r *pagesTransferRig) seedCrew(t *testing.T, id, name, slug string) {
	t.Helper()
	if _, err := r.db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, allowed_domains)
		VALUES (?, ?, ?, ?, 'free', '[]')`, id, r.wsID, name, slug); err != nil {
		t.Fatalf("seed crew %s: %v", id, err)
	}
}

func (r *pagesTransferRig) seedCrewMember(t *testing.T, crewID, userID string) {
	t.Helper()
	if _, err := r.db.Exec(`INSERT INTO crew_members (id, crew_id, user_id) VALUES (?, ?, ?)`,
		"cm-"+crewID+"-"+userID, crewID, userID); err != nil {
		t.Fatalf("seed crew_members %s/%s: %v", crewID, userID, err)
	}
}

// seedPage inserts a page owned by exactly one of ownerUserID / ownerCrewID
// (matching the XOR the schema enforces) — pass "" for whichever does not
// apply.
func (r *pagesTransferRig) seedPage(t *testing.T, id, slug, name, ownerUserID, ownerCrewID string) {
	t.Helper()
	if _, err := r.db.Exec(`INSERT INTO pages (id, workspace_id, slug, name, owner_user_id, owner_crew_id, spec_json)
		VALUES (?, ?, ?, ?, ?, ?, '{}')`,
		id, r.wsID, slug, name, nilIfEmpty(ownerUserID), nilIfEmpty(ownerCrewID)); err != nil {
		t.Fatalf("seed page %s: %v", id, err)
	}
}

func (r *pagesTransferRig) seedPanel(t *testing.T, id, pageID, ownerCrewID string) {
	t.Helper()
	if _, err := r.db.Exec(`INSERT INTO page_panels
		(id, page_id, panel_id, schema, owner_crew_id, producer_kind, producer_ref, sla_seconds)
		VALUES (?, ?, ?, 'status.v1', ?, 'script', 'script/watch.sh', 60)`,
		id, pageID, id, ownerCrewID); err != nil {
		t.Fatalf("seed page_panels %s: %v", id, err)
	}
}

// deleteReq builds the DELETE /api/v1/admin/users/{userId}/data request the
// handler expects, with workspace + ADMIN role context already plumbed —
// mirrors gdprTestRig.adminReq in admin_gdpr_test.go.
func (r *pagesTransferRig) deleteReq(t *testing.T, reason string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/", strings.NewReader(`{"reason":"`+reason+`"}`))
	req.SetPathValue("userId", r.userID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, r.wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: r.adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	return req.WithContext(ctx)
}

// ── The ordered rule itself (§7.1 rule 1b) ──────────────────────────────

func TestPagesOwnerTransfer_TargetCrewResolution(t *testing.T) {
	type panelSeed struct{ id, ownerCrewID string }
	tests := []struct {
		name        string
		panels      []panelSeed
		memberOf    []string // crew ids the departing user belongs to
		wantCrew    string
		wantReason  string
		wantRefusal bool
	}{
		{
			name:       "crew owning the most panels wins, even over the user's own crew",
			panels:     []panelSeed{{"p1", "crewB"}, {"p2", "crewB"}, {"p3", "crewA"}},
			memberOf:   []string{"crewA"},
			wantCrew:   "crewB",
			wantReason: "most_panels",
		},
		{
			name:       "no panel-owning crew falls back to the departing user's crew",
			panels:     nil,
			memberOf:   []string{"crewA"},
			wantCrew:   "crewA",
			wantReason: "member_crew",
		},
		{
			name:        "neither resolves: refuse, do not invent a fallback",
			panels:      nil,
			memberOf:    nil,
			wantRefusal: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := pagesTransferSetup(t)
			r.seedCrew(t, "crewA", "Crew A", "crew-a")
			r.seedCrew(t, "crewB", "Crew B", "crew-b")
			for _, c := range tc.memberOf {
				r.seedCrewMember(t, c, r.userID)
			}
			const pageID = "page1"
			r.seedPage(t, pageID, "flotila", "Flotila .201", r.userID, "")
			for _, p := range tc.panels {
				r.seedPanel(t, p.id, pageID, p.ownerCrewID)
			}

			rec := httptest.NewRecorder()
			r.h.DeleteUserData(rec, r.deleteReq(t, "sar-"+tc.name))

			if tc.wantRefusal {
				if rec.Code != http.StatusConflict {
					t.Fatalf("status = %d, want 409 Conflict; body=%s", rec.Code, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), "flotila") {
					t.Errorf("refusal message does not name the page: %s", rec.Body.String())
				}
				var ownerUserID sql.NullString
				var ownerCrewID sql.NullString
				if err := r.db.QueryRow(`SELECT owner_user_id, owner_crew_id FROM pages WHERE id = ?`, pageID).
					Scan(&ownerUserID, &ownerCrewID); err != nil {
					t.Fatalf("load page: %v", err)
				}
				if !ownerUserID.Valid || ownerUserID.String != r.userID {
					t.Errorf("refusal must not change the owner: owner_user_id = %v", ownerUserID)
				}
				if ownerCrewID.Valid {
					t.Errorf("refusal must not invent a crew owner: owner_crew_id = %v", ownerCrewID)
				}
				return
			}

			if rec.Code >= 400 {
				t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
			}
			var ownerUserID sql.NullString
			var ownerCrewID sql.NullString
			if err := r.db.QueryRow(`SELECT owner_user_id, owner_crew_id FROM pages WHERE id = ?`, pageID).
				Scan(&ownerUserID, &ownerCrewID); err != nil {
				t.Fatalf("load page: %v", err)
			}
			if ownerUserID.Valid {
				t.Errorf("owner_user_id still set after transfer: %v", ownerUserID.String)
			}
			if !ownerCrewID.Valid || ownerCrewID.String != tc.wantCrew {
				t.Errorf("owner_crew_id = %v, want %s", ownerCrewID, tc.wantCrew)
			}

			entry := r.spy.firstOfType(entryPageOwnerTransferred)
			if entry == nil {
				t.Fatal("no page.owner_transferred journal entry was emitted")
			}
			if got, _ := entry.Payload["reason"].(string); got != tc.wantReason {
				t.Errorf("journal reason = %q, want %q", got, tc.wantReason)
			}
			if got, _ := entry.Payload["to_crew_id"].(string); got != tc.wantCrew {
				t.Errorf("journal to_crew_id = %q, want %q", got, tc.wantCrew)
			}
		})
	}
}

// ── The page survives (issue #1944's literal ask) ───────────────────────

func TestPagesOwnerTransfer_UserWithPageIsDeletable(t *testing.T) {
	r := pagesTransferSetup(t)
	r.seedCrew(t, "crewA", "Crew A", "crew-a")
	r.seedCrewMember(t, "crewA", r.userID)
	r.seedPage(t, "page1", "flotila", "Flotila .201", r.userID, "")

	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.deleteReq(t, "sar-happy-path"))
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusMultiStatus {
		t.Fatalf("erasure of a user who owns a page must succeed: got %d body=%s", rec.Code, rec.Body.String())
	}

	var count int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE id = 'page1'`).Scan(&count); err != nil {
		t.Fatalf("count pages: %v", err)
	}
	if count != 1 {
		t.Fatalf("page did not survive the owner's erasure: count = %d, want 1", count)
	}
	var ownerCrewID string
	if err := r.db.QueryRow(`SELECT owner_crew_id FROM pages WHERE id = 'page1'`).Scan(&ownerCrewID); err != nil {
		t.Fatalf("load transferred page: %v", err)
	}
	if ownerCrewID != "crewA" {
		t.Errorf("owner_crew_id = %q, want crewA", ownerCrewID)
	}
}

// ── Notification + journal (§7.1 rule 1b: "notifies workspace ADMIN/OWNER
// through the existing notification path") ──────────────────────────────

func TestPagesOwnerTransfer_NotifiesWorkspaceAdmin(t *testing.T) {
	r := pagesTransferSetup(t)
	r.seedCrew(t, "crewA", "Crew A", "crew-a")
	r.seedCrewMember(t, "crewA", r.userID)
	r.seedPage(t, "page1", "flotila", "Flotila .201", r.userID, "")

	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.deleteReq(t, "sar-notify"))
	if rec.Code >= 400 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
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
	// target_role='ADMIN' is visible to ADMIN and OWNER — inboxVisibilityClause
	// (inbox_handler.go) ranks roles hierarchically, so this single row reaches
	// both without a second row per role.
	if targetRole != "ADMIN" {
		t.Errorf("target_role = %q, want ADMIN (visible to ADMIN and OWNER)", targetRole)
	}
	if kind != "message" {
		t.Errorf("kind = %q, want message (the existing inbox kind this reuses)", kind)
	}

	entry := r.spy.firstOfType(entryPageOwnerTransferred)
	if entry == nil {
		t.Fatal("no page.owner_transferred journal entry was emitted")
	}
	if entry.WorkspaceID != r.wsID {
		t.Errorf("journal entry workspace_id = %q, want %q", entry.WorkspaceID, r.wsID)
	}
	if pageID, _ := entry.Payload["page_id"].(string); pageID != "page1" {
		t.Errorf("journal payload page_id = %q, want page1", pageID)
	}
}

// ── A crew-owned page needs no transfer and must not be touched ────────

func TestPagesOwnerTransfer_CrewOwnedPageUntouched(t *testing.T) {
	r := pagesTransferSetup(t)
	r.seedCrew(t, "crewA", "Crew A", "crew-a")
	r.seedCrewMember(t, "crewA", r.userID)
	// Owned by a crew already — unrelated to the departing user's erasure.
	r.seedPage(t, "pageCrew", "crew-board", "Crew Board", "", "crewA")

	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.deleteReq(t, "sar-crew-owned"))
	if rec.Code >= 400 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
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

// ── The negative the issue calls out explicitly ─────────────────────────

func TestPagesOwnerTransfer_NeverDeletesAPage(t *testing.T) {
	r := pagesTransferSetup(t)
	r.seedCrew(t, "crewA", "Crew A", "crew-a")
	r.seedCrewMember(t, "crewA", r.userID)
	r.seedPage(t, "page1", "flotila", "Flotila .201", r.userID, "")
	r.seedPage(t, "pageCrew", "crew-board", "Crew Board", "", "crewA")

	var before int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id = ?`, r.wsID).Scan(&before); err != nil {
		t.Fatalf("count pages before: %v", err)
	}

	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.deleteReq(t, "sar-no-drop"))
	if rec.Code >= 400 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	var after int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id = ?`, r.wsID).Scan(&after); err != nil {
		t.Fatalf("count pages after: %v", err)
	}
	if after != before {
		t.Fatalf("page count changed from %d to %d — a page was deleted as a side effect of erasing its owner", before, after)
	}
}

// A refusal must be all-or-nothing for the WHOLE erasure, not just the
// pages step — otherwise a user ends up half-erased with an unresolved
// page still blocking the parts of the cascade that already ran.
func TestPagesOwnerTransfer_RefusalBlocksRestOfErasure(t *testing.T) {
	r := pagesTransferSetup(t)
	// No crews at all: the panel rule and the membership rule both miss.
	r.seedPage(t, "page1", "orphan-risk", "Orphan Risk", r.userID, "")
	if _, err := r.db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, data_subject_id)
		VALUES ('ib1', ?, 'message', 'msg1', 'about the departing user', ?)`, r.wsID, r.userID); err != nil {
		t.Fatalf("seed inbox_items: %v", err)
	}

	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.deleteReq(t, "sar-refuse"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 Conflict; body=%s", rec.Code, rec.Body.String())
	}

	var cnt int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE id = 'ib1'`).Scan(&cnt); err != nil {
		t.Fatalf("query inbox_items: %v", err)
	}
	if cnt != 1 {
		t.Errorf("the rest of the cascade ran despite the pages transfer refusing: inbox_items = %d, want 1", cnt)
	}

	var status string
	if err := r.db.QueryRow(`SELECT status FROM gdpr_actions WHERE workspace_id = ? AND data_subject_id = ? AND action = 'delete'`,
		r.wsID, r.userID).Scan(&status); err != nil {
		t.Fatalf("query gdpr_actions: %v", err)
	}
	if status != "failed" {
		t.Errorf("gdpr_actions status = %q, want failed (the audit trail must record the refusal)", status)
	}
}
