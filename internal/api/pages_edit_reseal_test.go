package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A spec edit must not carry a panel's payload across a crew boundary, and must
// not carry it across a change of schema either.
//
// reconcilePanels UPDATEs the surviving row in place — schema, owner_crew_id,
// producer — and used to leave page_panel_data untouched. owner_crew_id IS the
// ACL (pages_authz.go), so re-pointing it re-points who may read every payload
// already in the ring. The rollback path has always known this and dims exactly
// these panels (panelsToDim, pages_versions.go); the ordinary edit path did
// not, which is the whole of this bug: two ways to change a panel's shape, one
// of them honest about what that costs.
func TestPagesUpdate_ChangingAPanelsCrewClearsItsPayload(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")
	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("push: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := h.db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-galley', ?, 'Galley', 'galley')`, wsID); err != nil {
		t.Fatalf("insert crew: %v", err)
	}

	// The page's owner re-points the panel at a different crew. Everything else
	// about the panel is unchanged, so the row survives the reconcile.
	body := `{
		"slug": "fleet-201",
		"name": "Flotila .201",
		"panels": [{
			"id": "sluzby", "schema": "status.v1", "title": "Jede to?",
			"owner": "crew/galley", "producer": "script/watch-services.sh",
			"sla_seconds": 30, "span": 8
		}]
	}`
	req := pagesRequest(t, http.MethodPatch, "/api/v1/pages/fleet-201", wsID, userID, "OWNER", body)
	req.SetPathValue("slug", "fleet-201")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rr.Code, rr.Body.String())
	}

	// The payload produced while crew/lookout owned the panel must not still be
	// there now that crew/galley does.
	var n int
	if err := h.db.QueryRow(`
		SELECT COUNT(*) FROM page_panel_data
		 WHERE panel_id IN (SELECT id FROM page_panels WHERE panel_id = 'sluzby')`).Scan(&n); err != nil {
		t.Fatalf("count payloads: %v", err)
	}
	if n != 0 {
		t.Errorf("%d payload rows survived a change of owning crew — the new crew can now read "+
			"what the old crew produced", n)
	}
}

// The same rule for a change of SCHEMA, which is a correctness bug rather than
// a permission one: a status.v1 payload rendered by a metric.v1 panel is a
// panel showing something its own contract does not describe.
func TestPagesUpdate_ChangingAPanelsSchemaClearsItsPayload(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")
	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("push: %d %s", rr.Code, rr.Body.String())
	}

	body := `{
		"slug": "fleet-201",
		"name": "Flotila .201",
		"panels": [{
			"id": "sluzby", "schema": "metric.v1", "title": "Jede to?",
			"owner": "crew/lookout", "producer": "script/watch-services.sh",
			"sla_seconds": 30, "span": 8
		}]
	}`
	req := pagesRequest(t, http.MethodPatch, "/api/v1/pages/fleet-201", wsID, userID, "OWNER", body)
	req.SetPathValue("slug", "fleet-201")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rr.Code, rr.Body.String())
	}

	var n int
	if err := h.db.QueryRow(`
		SELECT COUNT(*) FROM page_panel_data
		 WHERE panel_id IN (SELECT id FROM page_panels WHERE panel_id = 'sluzby')`).Scan(&n); err != nil {
		t.Fatalf("count payloads: %v", err)
	}
	if n != 0 {
		t.Errorf("%d payload rows of the old shape survived a schema change", n)
	}
}

// An edit that leaves a panel's shape alone keeps its data. Without this the
// fix above would be indistinguishable from "every save wipes the page".
func TestPagesUpdate_AnOrdinaryEditKeepsThePayload(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")
	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("push: %d %s", rr.Code, rr.Body.String())
	}

	// Only the title and the span move.
	body := `{
		"slug": "fleet-201",
		"name": "Flotila .201",
		"panels": [{
			"id": "sluzby", "schema": "status.v1", "title": "Bezi to?",
			"owner": "crew/lookout", "producer": "script/watch-services.sh",
			"sla_seconds": 30, "span": 12
		}]
	}`
	req := pagesRequest(t, http.MethodPatch, "/api/v1/pages/fleet-201", wsID, userID, "OWNER", body)
	req.SetPathValue("slug", "fleet-201")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rr.Code, rr.Body.String())
	}

	var n int
	if err := h.db.QueryRow(`
		SELECT COUNT(*) FROM page_panel_data
		 WHERE panel_id IN (SELECT id FROM page_panels WHERE panel_id = 'sluzby')`).Scan(&n); err != nil {
		t.Fatalf("count payloads: %v", err)
	}
	if n == 0 {
		t.Error("renaming a panel threw its data away")
	}
}
