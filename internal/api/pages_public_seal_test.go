package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A panel may only be published by a human who can SEE it.
//
// publicPanelIDs calls itself "the only function that decides what a public
// link exposes", and it decided by intersecting the live spec with the newest
// human-authored version — never asking whether that human was allowed to look
// at the panel they were marking. mayEditSpec is page-level (pages_authz.go),
// so a MEMBER holding `write` on a page can edit a panel that is SEALED to
// them; marking it public then served its payload at /api/v1/public/pages/
// {token}, to anyone holding the link, with no auth at all.
//
// The seal and the publication gate were reading two different questions: "may
// you edit this page" and "may you see this panel". Publishing needs the second.
func TestPublicPanelIDs_APanelSealedToItsAttesterIsNotPublished(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	page := pagesCreate(t, h, wsID, userID, "fleet-201")
	pageID, _ := page["id"].(string)
	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("push: %d %s", rr.Code, rr.Body.String())
	}

	// A workspace MEMBER who belongs to no crew: crew/lookout's panel is sealed
	// to them, and a `write` grant lets them edit the page all the same.
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('outsider', 'outsider@example.com', 'Outsider')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm-outsider', ?, 'outsider', 'MEMBER')`, wsID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	pagesGrant(t, h, wsID, userID, "fleet-201",
		`{"subject_type":"user","subject":"outsider@example.com","level":"write"}`)

	// They mark the panel they cannot see as public.
	body := `{
		"slug": "fleet-201",
		"name": "Flotila .201",
		"panels": [{
			"id": "sluzby", "schema": "status.v1", "title": "Jede to?",
			"owner": "crew/lookout", "producer": "script/watch-services.sh",
			"sla_seconds": 30, "span": 8, "public": true
		}]
	}`
	req := pagesRequest(t, http.MethodPatch, "/api/v1/pages/fleet-201", wsID, "outsider", "MEMBER", body)
	req.SetPathValue("slug", "fleet-201")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update by the write grantee: %d %s", rr.Code, rr.Body.String())
	}

	ids, err := h.publicPanelIDs(context.Background(), pageID)
	if err != nil {
		t.Fatalf("publicPanelIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("published %v — a panel marked public by someone who cannot see it is "+
			"served to the internet with no auth", ids)
	}
}

// The rule it exists for still holds: a human who CAN see the panel publishes it.
func TestPublicPanelIDs_APanelItsAttesterCanSeeIsPublished(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	page := pagesCreate(t, h, wsID, userID, "fleet-201")
	pageID, _ := page["id"].(string)

	body := `{
		"slug": "fleet-201",
		"name": "Flotila .201",
		"panels": [{
			"id": "sluzby", "schema": "status.v1", "title": "Jede to?",
			"owner": "crew/lookout", "producer": "script/watch-services.sh",
			"sla_seconds": 30, "span": 8, "public": true
		}]
	}`
	req := pagesRequest(t, http.MethodPatch, "/api/v1/pages/fleet-201", wsID, userID, "OWNER", body)
	req.SetPathValue("slug", "fleet-201")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rr.Code, rr.Body.String())
	}

	ids, err := h.publicPanelIDs(context.Background(), pageID)
	if err != nil {
		t.Fatalf("publicPanelIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != "sluzby" {
		t.Errorf("published %v, want [sluzby] — an admin marking a panel public must still publish it", ids)
	}
}

// An unrelated edit by somebody who cannot see a published panel must not
// unpublish it.
//
// pagePatchBody drops `panels`, so renaming a page leaves every panel's
// `public: true` in place while making the renamer the page's newest human
// author. Attesting against that author — as the first version of this check
// did — silently took a live external link dark, weeks after an admin
// published it, with nothing said to anyone.
func TestPublicPanelIDs_ARenameByAnOutsiderDoesNotUnpublish(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	page := pagesCreate(t, h, wsID, userID, "fleet-201")
	pageID, _ := page["id"].(string)

	// The admin publishes crew/lookout's panel.
	published := `{
		"slug": "fleet-201", "name": "Flotila .201",
		"panels": [{
			"id": "sluzby", "schema": "status.v1", "title": "Jede to?",
			"owner": "crew/lookout", "producer": "script/watch-services.sh",
			"sla_seconds": 30, "span": 8, "public": true
		}]
	}`
	req := pagesRequest(t, http.MethodPatch, "/api/v1/pages/fleet-201", wsID, userID, "OWNER", published)
	req.SetPathValue("slug", "fleet-201")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", rr.Code, rr.Body.String())
	}
	if ids, err := h.publicPanelIDs(context.Background(), pageID); err != nil || len(ids) != 1 {
		t.Fatalf("precondition: publicPanelIDs = %v, %v; want [sluzby]", ids, err)
	}

	// A MEMBER with `write`, in no crew, renames the page. Same panels.
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('renamer', 'renamer@example.com', 'Renamer')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm-renamer', ?, 'renamer', 'MEMBER')`, wsID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	pagesGrant(t, h, wsID, userID, "fleet-201",
		`{"subject_type":"user","subject":"renamer@example.com","level":"write"}`)

	renamed := strings.Replace(published, `"name": "Flotila .201"`, `"name": "Flotila 201"`, 1)
	req2 := pagesRequest(t, http.MethodPatch, "/api/v1/pages/fleet-201", wsID, "renamer", "MEMBER", renamed)
	req2.SetPathValue("slug", "fleet-201")
	rr2 := httptest.NewRecorder()
	h.Update(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("rename by the write grantee: %d %s", rr2.Code, rr2.Body.String())
	}

	ids, err := h.publicPanelIDs(context.Background(), pageID)
	if err != nil {
		t.Fatalf("publicPanelIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != "sluzby" {
		t.Errorf("publicPanelIDs = %v after an unrelated rename, want [sluzby] — the link the admin "+
			"published went dark because somebody else touched the page", ids)
	}
}
