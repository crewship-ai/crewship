package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #5260 refinement: a panel whose agent producer the owner did not authorize
// says so in plain words, says who can fix it, and the OWNER has one direct
// call that authorizes the producer explicitly. Nobody else can make the
// panel green: not an admin, not an Edit grantee, not an agent.

func pagesAuthorizeProducer(t *testing.T, h *PageHandler, wsID, userID, role, panelID string, agent bool) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/v1/pages/fleet-201/panels/" + panelID + "/producer/authorize"
	req := pagesGrantRequest(t, "POST", target, wsID, userID, role, "")
	if agent {
		req.Header.Set(actingAgentSlugHeader, pagesProducerAgentSlug)
	}
	req.SetPathValue("slug", "fleet-201")
	req.SetPathValue("panelId", panelID)
	rr := httptest.NewRecorder()
	h.AuthorizePanelProducer(rr, req)
	return rr
}

// pagesPanelWire reads one panel off the page document as a given viewer.
func pagesPanelWire(t *testing.T, h *PageHandler, wsID, userID, role, panelID string) map[string]any {
	t.Helper()
	req := pagesGrantRequest(t, "GET", "/api/v1/pages/fleet-201", wsID, userID, role, "")
	req.SetPathValue("slug", "fleet-201")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get page as %s: %d %s", userID, rr.Code, rr.Body.String())
	}
	var doc struct {
		Panels []map[string]any `json:"panels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	for _, p := range doc.Panels {
		if p["id"] == panelID {
			return p
		}
	}
	t.Fatalf("panel %s missing from the page", panelID)
	return nil
}

func TestOwnerAuthorizesAnUnauthorizedAgentProducer(t *testing.T) {
	h, wsID, ownerID, pageID := pagesProducerFixture(t)
	if rr := pagesPatchAs(t, h, wsID, ownerID, "OWNER", pagesPanelsWithProducer("agent/"+pagesProducerAgentSlug, "Jede to?")); rr.Code != http.StatusOK {
		t.Fatalf("owner names the producer: %d %s", rr.Code, rr.Body.String())
	}
	// As the migration leaves a designation the owner did not make.
	if _, err := h.db.Exec(`UPDATE page_panels SET producer_designated_by_user_id='admin' WHERE page_id=? AND panel_id='sluzby'`, pageID); err != nil {
		t.Fatalf("re-attribute the designation: %v", err)
	}
	if !pagesProducerRetired(t, h, pageID) {
		t.Fatal("precondition: the designation is retired")
	}

	// The panel explains itself, to the owner and to everyone else.
	for _, tc := range []struct {
		who, role    string
		canAuthorize bool
	}{{ownerID, "OWNER", true}, {"editor", "MEMBER", false}, {"admin", "ADMIN", false}} {
		p := pagesPanelWire(t, h, wsID, tc.who, tc.role, "sluzby")
		reason, _ := p["reason"].(string)
		if !strings.HasPrefix(reason, "Producer not authorized by the owner") || !strings.Contains(reason, "Only the page's owner") {
			t.Errorf("%s reads reason %q, want the plain-words state and who can fix it", tc.who, reason)
		}
		auth, _ := p["producer_authorization"].(map[string]any)
		if auth == nil {
			t.Fatalf("%s: no producer_authorization on the panel: %+v", tc.who, p)
		}
		if auth["producer"] != "agent/"+pagesProducerAgentSlug {
			t.Errorf("%s: producer_authorization.producer = %v", tc.who, auth["producer"])
		}
		if got, _ := auth["can_authorize"].(bool); got != tc.canAuthorize {
			t.Errorf("%s: can_authorize = %v, want %v", tc.who, got, tc.canAuthorize)
		}
	}
	if p := pagesPanelWire(t, h, wsID, ownerID, "OWNER", "zatizeni"); p["producer_authorization"] != nil {
		t.Errorf("a routine-produced panel carries producer_authorization: %+v", p)
	}

	// Nobody but the owner, and no agent, authorizes it.
	for _, tc := range []struct {
		name, who, role string
		agent           bool
	}{
		{"workspace admin", "admin", "ADMIN", false},
		{"Edit grantee", "editor", "MEMBER", false},
		{"an agent acting for the owner", ownerID, "OWNER", true},
	} {
		rr := pagesAuthorizeProducer(t, h, wsID, tc.who, tc.role, "sluzby", tc.agent)
		if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "owner") {
			t.Fatalf("%s authorizing: %d %s, want 403 naming the owner", tc.name, rr.Code, rr.Body.String())
		}
		if !pagesProducerRetired(t, h, pageID) || pagesAgentPush(t, h, wsID) != http.StatusForbidden {
			t.Fatalf("%s's refused authorization changed the designation", tc.name)
		}
	}
	if rr := pagesAuthorizeProducer(t, h, wsID, ownerID, "OWNER", "zatizeni", false); rr.Code != http.StatusConflict {
		t.Fatalf("authorizing a routine producer: %d %s, want 409", rr.Code, rr.Body.String())
	}
	if rr := pagesAuthorizeProducer(t, h, wsID, ownerID, "OWNER", "nope", false); rr.Code != http.StatusNotFound {
		t.Fatalf("authorizing a missing panel: %d %s, want 404", rr.Code, rr.Body.String())
	}

	// The owner's explicit authorization: a new designation, audited.
	rr := pagesAuthorizeProducer(t, h, wsID, ownerID, "OWNER", "sluzby", false)
	if rr.Code != http.StatusOK {
		t.Fatalf("owner authorizes: %d %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out["producer"] != "agent/"+pagesProducerAgentSlug || out["authorized"] != true {
		t.Fatalf("authorize response = %s (%v)", rr.Body.String(), err)
	}
	var designator string
	if err := h.db.QueryRow(`SELECT COALESCE(producer_designated_by_user_id,'') FROM page_panels WHERE page_id=? AND panel_id='sluzby'`, pageID).Scan(&designator); err != nil {
		t.Fatal(err)
	}
	if designator != ownerID || pagesProducerRetired(t, h, pageID) {
		t.Fatalf("after authorization: designator %q, retired %v", designator, pagesProducerRetired(t, h, pageID))
	}
	if n := pagesAuditCount(t, h, "page.agent_producer.authorized", pageID); n != 1 {
		t.Fatalf("authorization audits = %d, want 1", n)
	}
	if code := pagesAgentPush(t, h, wsID); code != http.StatusOK {
		t.Fatalf("the owner-authorized producer cannot push: %d", code)
	}
	p := pagesPanelWire(t, h, wsID, ownerID, "OWNER", "sluzby")
	if reason, _ := p["reason"].(string); p["producer_authorization"] != nil || strings.Contains(reason, "not authorized") {
		t.Fatalf("the panel still reads unauthorized after the owner authorized it: %+v", p)
	}
	// Idempotent: a second call changes and audits nothing.
	if rr := pagesAuthorizeProducer(t, h, wsID, ownerID, "OWNER", "sluzby", false); rr.Code != http.StatusOK {
		t.Fatalf("second authorize: %d %s", rr.Code, rr.Body.String())
	}
	if n := pagesAuditCount(t, h, "page.agent_producer.authorized", pageID); n != 1 {
		t.Fatalf("a no-op authorization audited again: %d", n)
	}
	// The authorization is the owner's: it ends with their ownership.
	if _, err := h.db.Exec(`UPDATE pages SET owner_user_id='editor' WHERE id=?`, pageID); err != nil {
		t.Fatal(err)
	}
	if !pagesProducerRetired(t, h, pageID) || pagesAgentPush(t, h, wsID) != http.StatusForbidden {
		t.Fatal("the previous owner's authorization survived the transfer")
	}
}
