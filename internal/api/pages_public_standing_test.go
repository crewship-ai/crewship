package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Publishing reaches the internet, so on a crew-owned Page it takes the crew's
// manage authority: a MANAGER (or higher, workspace or crew override) who
// belongs to the owning crew, an explicit crew Manage grant, or a workspace
// admin. Plain crew membership is not enough.
func TestPublish_CrewOwnedPageNeedsTheCrewsManager(t *testing.T) {
	cases := []struct {
		name          string
		workspaceRole string
		crewRole      any // nil = inherit the workspace role
		inCrew        bool
		wantPublish   bool
		manageGrant   bool
		crewDeleted   bool
	}{
		{name: "crew viewer", workspaceRole: "VIEWER", inCrew: true},
		{name: "crew member", workspaceRole: "MEMBER", inCrew: true},
		{name: "member with crew VIEWER override", workspaceRole: "MEMBER", crewRole: "VIEWER", inCrew: true},
		{name: "workspace manager outside the crew", workspaceRole: "MANAGER"},
		{name: "workspace manager in the crew", workspaceRole: "MANAGER", inCrew: true, wantPublish: true},
		{name: "manager with a crew VIEWER override keeps the higher role", workspaceRole: "MANAGER", crewRole: "VIEWER", inCrew: true, wantPublish: true},
		{name: "member promoted to crew MANAGER", workspaceRole: "MEMBER", crewRole: "MANAGER", inCrew: true, wantPublish: true},
		{name: "crew Manage grant without membership", workspaceRole: "MEMBER", manageGrant: true, wantPublish: true},
		{name: "crew Manage grant in the crew", workspaceRole: "MEMBER", inCrew: true, manageGrant: true, wantPublish: true},
		{name: "workspace admin outside the crew", workspaceRole: "ADMIN", wantPublish: true},
		{name: "crew manager of a deleted crew", workspaceRole: "MANAGER", inCrew: true, crewDeleted: true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slug := "crew-public-" + string(rune('a'+i))
			h, _, _, wsID, _ := newPagesPublicFixture(t, slug)
			mustExec(t, h.db, `UPDATE pages SET owner_user_id=NULL, owner_crew_id='crew-lookout' WHERE slug=?`, slug)
			const actor = "user-actor"
			mustExec(t, h.db, `INSERT INTO users (id, email, full_name) VALUES (?, 'actor@example.com', 'Actor')`, actor)
			mustExec(t, h.db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm-actor', ?, ?, ?)`, wsID, actor, tc.workspaceRole)
			if tc.inCrew {
				mustExec(t, h.db, `INSERT INTO crew_members (id, crew_id, user_id, role) VALUES ('cm-actor', 'crew-lookout', ?, ?)`, actor, tc.crewRole)
			}
			if tc.manageGrant {
				var owner string
				if err := h.db.QueryRow(`SELECT user_id FROM workspace_members WHERE workspace_id=? AND role='OWNER'`, wsID).Scan(&owner); err != nil {
					t.Fatal(err)
				}
				mustExec(t, h.db, `INSERT INTO access_grants(id,member_id,resource_kind,crew_id,operation,created_by,created_at) VALUES('grant-actor','wm-actor','crew','crew-lookout','manage',?,'fixture')`, owner)
			}
			if tc.crewDeleted {
				mustExec(t, h.db, `UPDATE crews SET deleted_at='2026-11-13T10:00:00Z' WHERE id='crew-lookout'`)
			}
			rr := pagesPublishAs(t, h, wsID, actor, tc.workspaceRole, slug, `{}`)
			var links int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_public_tokens WHERE created_by_user_id=?`, actor).Scan(&links); err != nil {
				t.Fatal(err)
			}
			if tc.wantPublish && (rr.Code != http.StatusCreated || links != 1) {
				t.Fatalf("want a link: %d %s (links=%d)", rr.Code, rr.Body.String(), links)
			}
			if !tc.wantPublish && (rr.Code == http.StatusCreated || links != 0) {
				t.Fatalf("published without the crew's manage authority: %d %s (links=%d)", rr.Code, rr.Body.String(), links)
			}
		})
	}
}

// A link opens only while its creator still stands: not suspended, still a
// member of the workspace. It is withheld, never revoked, so it works again
// when the standing returns (permanent expiry is a separate product decision).
func TestPublicView_WithheldWhileTheCreatorLacksStanding(t *testing.T) {
	h, _, _, wsID, userID := newPagesPublicFixture(t, "standing")
	link := pagesPublish(t, h, wsID, userID, "standing", `{}`)
	token := link["token"].(string)
	open := func(want int, step string) {
		t.Helper()
		if rr := pagesPublicGet(t, h, token); rr.Code != want {
			t.Fatalf("%s: got %d, want %d: %s", step, rr.Code, want, rr.Body.String())
		}
	}
	open(http.StatusOK, "baseline")

	mustExec(t, h.db, `UPDATE users SET suspended_at='2026-11-13T10:00:00Z' WHERE id=?`, userID)
	open(http.StatusNotFound, "creator suspended")
	if withheld, unknown := pagesPublicGet(t, h, token), pagesPublicGet(t, h, "unknown-token-for-comparison"); withheld.Body.String() != unknown.Body.String() {
		t.Fatalf("a withheld link answers differently from an unknown one: %q vs %q", withheld.Body.String(), unknown.Body.String())
	}
	list := httptest.NewRecorder()
	listReq := pagesHumanRequest(t, "GET", "/api/v1/pages/standing/public", wsID, userID, "OWNER", "")
	listReq.SetPathValue("slug", "standing")
	h.ListPublicLinks(list, listReq)
	if !strings.Contains(list.Body.String(), `"withheld":true`) || strings.Contains(list.Body.String(), `"live":true`) {
		t.Fatalf("the owner's list must show the link withheld, not live: %s", list.Body.String())
	}
	mustExec(t, h.db, `UPDATE users SET suspended_at=NULL WHERE id=?`, userID)
	open(http.StatusOK, "suspension lifted")

	var memberID, role string
	if err := h.db.QueryRow(`SELECT id, role FROM workspace_members WHERE workspace_id=? AND user_id=?`, wsID, userID).Scan(&memberID, &role); err != nil {
		t.Fatal(err)
	}
	mustExec(t, h.db, `DELETE FROM workspace_members WHERE id=?`, memberID)
	open(http.StatusNotFound, "creator no longer a member")
	mustExec(t, h.db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, ?)`, memberID, wsID, userID, role)
	open(http.StatusOK, "membership restored")

	var revoked sql.NullString
	if err := h.db.QueryRow(`SELECT revoked_at FROM page_public_tokens WHERE id=?`, link["id"]).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if revoked.Valid {
		t.Fatal("withholding a link must not revoke it")
	}
}

// The unlock path resolves the same token and must withhold it the same way,
// with the same answer as an unknown token.
func TestPublicUnlock_WithheldWhileTheCreatorIsSuspended(t *testing.T) {
	h, _, _, wsID, userID := newPagesPublicFixture(t, "standing-pw")
	link := pagesPublish(t, h, wsID, userID, "standing-pw", `{"password":"correct horse battery staple"}`)
	token := link["token"].(string)
	mustExec(t, h.db, `UPDATE users SET suspended_at='2026-11-13T10:00:00Z' WHERE id=?`, userID)
	suspended := pagesPublicUnlock(t, h, token, "/api/v1/public/pages/"+token+"/unlock", `{"password":"correct horse battery staple"}`)
	unknown := pagesPublicUnlock(t, h, "unknown-token-for-comparison", "/api/v1/public/pages/unknown-token-for-comparison/unlock", `{"password":"correct horse battery staple"}`)
	if suspended.Code == http.StatusOK || suspended.Code != unknown.Code {
		t.Fatalf("unlock of a suspended creator's link: %d (unknown token: %d) %s", suspended.Code, unknown.Code, suspended.Body.String())
	}
}

// Listing a page's links shows who created them, and revoking takes one back.
// Both take the same authority as publishing: a crew VIEWER or MEMBER may read
// the page but neither see the creators nor revoke a manager's link. A link's
// own creator may always revoke it (narrowing reach is always safe).
func TestPublicLinks_ListAndRevokeTakeThePublishAuthority(t *testing.T) {
	cases := []struct {
		name, workspaceRole string
		crewRole            any
		ownLink             bool
		agentHeader         bool // a human session that only claims to be an agent
		want                bool
	}{
		{name: "crew viewer", workspaceRole: "VIEWER"},
		{name: "crew member", workspaceRole: "MEMBER"},
		{name: "member with crew VIEWER override", workspaceRole: "MEMBER", crewRole: "VIEWER"},
		{name: "crew member revokes the link they created", workspaceRole: "MEMBER", ownLink: true, want: true},
		{name: "crew member claiming to be an agent by header", workspaceRole: "MEMBER", agentHeader: true},
		{name: "crew manager", workspaceRole: "MANAGER", want: true},
		{name: "member promoted to crew MANAGER", workspaceRole: "MEMBER", crewRole: "MANAGER", want: true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slug := "crew-links-" + string(rune('a'+i))
			h, _, _, wsID, ownerID := newPagesPublicFixture(t, slug)
			link := pagesPublish(t, h, wsID, ownerID, slug, `{}`)
			mustExec(t, h.db, `UPDATE pages SET owner_user_id=NULL, owner_crew_id='crew-lookout' WHERE slug=?`, slug)
			var ownerEmail string
			if err := h.db.QueryRow(`SELECT email FROM users WHERE id=?`, ownerID).Scan(&ownerEmail); err != nil {
				t.Fatal(err)
			}
			const actor = "user-links-actor"
			mustExec(t, h.db, `INSERT INTO users (id, email, full_name) VALUES (?, 'links-actor@example.com', 'Actor')`, actor)
			mustExec(t, h.db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm-links-actor', ?, ?, ?)`, wsID, actor, tc.workspaceRole)
			mustExec(t, h.db, `INSERT INTO crew_members (id, crew_id, user_id, role) VALUES ('cm-links-actor', 'crew-lookout', ?, ?)`, actor, tc.crewRole)
			target := link["id"].(string)
			if tc.ownLink {
				mustExec(t, h.db, `UPDATE page_public_tokens SET created_by_user_id=? WHERE id=?`, actor, target)
			}

			list := httptest.NewRecorder()
			listReq := pagesHumanRequest(t, "GET", "/api/v1/pages/"+slug+"/public", wsID, actor, tc.workspaceRole, "")
			listReq.SetPathValue("slug", slug)
			h.ListPublicLinks(list, listReq)
			wantList := tc.want && !tc.ownLink
			if wantList != (list.Code == http.StatusOK) {
				t.Errorf("list: got %d, want ok=%v: %s", list.Code, wantList, list.Body.String())
			}
			if !wantList && strings.Contains(list.Body.String(), ownerEmail) {
				t.Errorf("a refused list leaked the creator's email: %s", list.Body.String())
			}

			rev := httptest.NewRecorder()
			revReq := pagesHumanRequest(t, "DELETE", "/api/v1/pages/"+slug+"/public/"+target, wsID, actor, tc.workspaceRole, "")
			revReq.SetPathValue("slug", slug)
			revReq.SetPathValue("tokenId", target)
			if tc.agentHeader {
				revReq.Header.Set(actingAgentSlugHeader, "spoofed-agent")
			}
			h.RevokePublicLink(rev, revReq)
			var revoked sql.NullString
			if err := h.db.QueryRow(`SELECT revoked_at FROM page_public_tokens WHERE id=?`, target).Scan(&revoked); err != nil {
				t.Fatal(err)
			}
			if tc.want != (rev.Code == http.StatusOK && revoked.Valid) {
				t.Fatalf("revoke: got %d (revoked=%v), want %v: %s", rev.Code, revoked.Valid, tc.want, rev.Body.String())
			}
		})
	}
}

// An expired link is expired, whatever its creator's standing.
func TestPublicLinks_ExpiredLinkIsNotReportedWithheld(t *testing.T) {
	h, _, _, wsID, userID := newPagesPublicFixture(t, "expired-held")
	link := pagesPublish(t, h, wsID, userID, "expired-held", `{}`)
	mustExec(t, h.db, `UPDATE page_public_tokens SET expires_at='2020-01-01T00:00:00Z' WHERE id=?`, link["id"])
	mustExec(t, h.db, `UPDATE users SET suspended_at='2026-11-13T10:00:00Z' WHERE id=?`, userID)
	list := httptest.NewRecorder()
	listReq := pagesHumanRequest(t, "GET", "/api/v1/pages/expired-held/public", wsID, userID, "OWNER", "")
	listReq.SetPathValue("slug", "expired-held")
	h.ListPublicLinks(list, listReq)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), `"withheld":true`) || strings.Contains(list.Body.String(), `"live":true`) {
		t.Fatalf("an expired link was reported withheld: %d %s", list.Code, list.Body.String())
	}
}
