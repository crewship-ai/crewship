package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A deleted crew owns nothing (#5259). Crew deletion through the API removes
// crew_members, but rows can outlive the tombstone (a restore, an older
// build), and every human owner check read crew_members alone: the deleted
// crew's members kept owner rights on its pages (edit the spec, administer
// grants, webhooks, folders, public tokens) and the human grants they issued
// stayed live. The fixture deletes the crew the way such a residue looks: a
// tombstone with its member rows still there.
func TestDeletedOwningCrewMembersAreNotPageOwners(t *testing.T) {
	h, _, wsID, ownerID, _ := pagesGrantFixture(t, "crew/engine")
	ctx := context.Background()
	pagesSeedUser(t, h, wsID, "crewmate", "crewmate@example.com", "MEMBER")
	pagesSeedUser(t, h, wsID, "othermate", "othermate@example.com", "MEMBER")
	pagesSeedUser(t, h, wsID, "reader", "reader@example.com", "MEMBER")
	pagesSeedUser(t, h, wsID, "reader2", "reader2@example.com", "MEMBER")
	pagesSeedCrewMember(t, h, "crew-engine", "crewmate")
	// An unrelated, live crew with its own page: it must keep everything.
	if _, err := h.db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-other', ?, 'Other', 'other')`, wsID); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	pagesSeedCrewMember(t, h, "crew-other", "othermate")
	rr := httptest.NewRecorder()
	h.Create(rr, pagesGrantRequest(t, "POST", "/api/v1/pages", wsID, ownerID, "OWNER", pagesTwoPanelBody("fleet-202", "crew/other")))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create other page: %d %s", rr.Code, rr.Body)
	}

	// While the crew lives, its member owns the page and issues grants on it.
	pagesGrantAs := func(user, slug, body string) int {
		t.Helper()
		return pagesGrantCall(t, h, "PUT", "/api/v1/pages/"+slug+"/grants", wsID, user, "MEMBER", slug, body).Code
	}
	if code := pagesGrantAs("crewmate", "fleet-201", `{"subject_type":"user","subject":"reader@example.com","level":"read"}`); code != http.StatusOK {
		t.Fatalf("a live crew's member could not grant on its page: %d", code)
	}
	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"user","subject":"reader2@example.com","level":"read"}`)
	if code := pagesGrantAs("othermate", "fleet-202", `{"subject_type":"user","subject":"reader@example.com","level":"read"}`); code != http.StatusOK {
		t.Fatalf("other crew's member could not grant on its page: %d", code)
	}

	if _, err := h.db.Exec(`UPDATE crews SET deleted_at='2026-11-19T00:00:00Z' WHERE id='crew-engine'`); err != nil {
		t.Fatalf("delete crew: %v", err)
	}
	load := func(slug string) *pageRecord {
		t.Helper()
		rec, err := h.loadPage(ctx, wsID, slug)
		if err != nil {
			t.Fatalf("load %s: %v", slug, err)
		}
		return rec
	}
	deleted, live := load("fleet-201"), load("fleet-202")

	for _, tc := range []struct {
		name  string
		user  string
		role  string
		rec   *pageRecord
		owner bool // isPageOwner, and with it mayAdministerGrants and mayEditSpec
	}{
		{"deleted crew's member is not the owner", "crewmate", "MEMBER", deleted, false},
		{"live crew's member stays the owner", "othermate", "MEMBER", live, true},
		{"a member of another crew never owned it", "othermate", "MEMBER", deleted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.isPageOwner(ctx, wsID, tc.user, tc.rec); got != tc.owner {
				t.Errorf("isPageOwner = %v, want %v", got, tc.owner)
			}
			if got := h.mayAdministerGrants(ctx, wsID, tc.user, tc.role, tc.rec); got != tc.owner {
				t.Errorf("mayAdministerGrants = %v, want %v", got, tc.owner)
			}
			if got := h.mayEditSpec(ctx, wsID, tc.user, tc.role, tc.rec); got != tc.owner {
				t.Errorf("mayEditSpec = %v, want %v", got, tc.owner)
			}
			err := h.pageGrantIssuerCurrent(ctx, wsID, tc.user, tc.rec)
			if tc.owner && err != nil {
				t.Errorf("pageGrantIssuerCurrent = %v, want nil", err)
			}
			if !tc.owner && !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("pageGrantIssuerCurrent = %v, want sql.ErrNoRows", err)
			}
		})
	}
	// The workspace OWNER's standing does not come from the crew.
	if !h.mayEditSpec(ctx, wsID, ownerID, "OWNER", deleted) || h.pageGrantIssuerCurrent(ctx, wsID, ownerID, deleted) != nil {
		t.Error("the workspace OWNER lost its rights on a deleted crew's page")
	}

	if code := pagesGrantAs("crewmate", "fleet-201", `{"subject_type":"user","subject":"reader2@example.com","level":"comment"}`); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("a deleted crew's member granted on its page: %d, want 403 or 404", code)
	}
	if code := pagesGrantAs("othermate", "fleet-202", `{"subject_type":"user","subject":"reader2@example.com","level":"read"}`); code != http.StatusOK {
		t.Errorf("a live crew's member could not grant on its page: %d", code)
	}

	retired := func(page, subject, issuer string) bool {
		t.Helper()
		var r bool
		if err := h.db.QueryRow(`SELECT g.retired_at IS NOT NULL FROM page_grants g JOIN pages p ON p.id=g.page_id
			WHERE p.slug=? AND g.subject_type='user' AND g.subject_id=? AND g.granted_by_user_id=?`, page, subject, issuer).Scan(&r); err != nil {
			t.Fatalf("read grant %s/%s by %s: %v", page, subject, issuer, err)
		}
		return r
	}
	if !retired("fleet-201", "reader", "crewmate") {
		t.Error("a grant issued by the deleted crew's member stayed live")
	}
	if retired("fleet-201", "reader2", ownerID) {
		t.Error("the workspace OWNER's grant on the deleted crew's page was retired")
	}
	if retired("fleet-202", "reader", "othermate") {
		t.Error("the live crew's member's grant was retired")
	}

	// Restoring the crew restores ownership, a live read, but not the
	// retired grant: a new grant is a new operation (#5069).
	if _, err := h.db.Exec(`UPDATE crews SET deleted_at=NULL WHERE id='crew-engine'`); err != nil {
		t.Fatalf("restore crew: %v", err)
	}
	if !h.isPageOwner(ctx, wsID, "crewmate", load("fleet-201")) {
		t.Error("the restored crew's member is not the owner again")
	}
	if !retired("fleet-201", "reader", "crewmate") {
		t.Error("restoring the crew revived its member's grant")
	}
}
