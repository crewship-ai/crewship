package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Tabs across the wire (internal/pages/tabs.go).
//
// The rule itself is tested in the pages package, where it is pure. What is
// tested HERE is the part with machinery in it, and it is the same hop the icon
// had to be tested at: `tab` has no column on page_panels — it is layout, and
// the table carries the contract — so the read path attaches it from the parsed
// spec. That attachment is where a field silently disappears, and a page whose
// tabs came back empty is indistinguishable from a page whose author never
// declared any: one long scroll, no bar, nothing in a log.
//
// The sealed case is the one with a rule behind it rather than a mechanism. A
// tab is a property of the PAGE, so a tab whose panels are all foreign to this
// reader still has to appear on their bar — otherwise the bar reflows per
// viewer (which §2.3 spends its length arguing against) and the tab's absence
// discloses that everything on it belongs to a crew they are not in.

// pagesTabbedBody is a two-panel page: one panel on each of two tabs.
func pagesTabbedBody(slug string) string {
	return `{
		"slug": "` + slug + `",
		"name": "Síť",
		"panels": [
			{"id": "dosah", "schema": "status.v1", "title": "Dosažitelnost", "tab": "Síť",
			 "owner": "crew/lookout", "producer": "script/ping-go", "sla_seconds": 30, "span": 6},
			{"id": "latence", "schema": "metric.v1", "title": "Odezva", "tab": "Odezva",
			 "owner": "crew/lookout", "producer": "script/ping-go", "sla_seconds": 30, "span": 6}
		]
	}`
}

func TestPagesCreate_TabRoundTripsToTheReadPath(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)

	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesTabbedBody("sit"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("create: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var created map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("create response is not JSON: %v", err)
	}
	if got, _ := pagesPanel(t, created, "dosah")["tab"].(string); got != "Síť" {
		t.Errorf("create echoed tab = %q, want %q", got, "Síť")
	}

	// …and on a later GET, which reads it back out of spec_json.
	doc := pagesGet(t, h, wsID, userID, "OWNER", "sit")
	for panelID, want := range map[string]string{"dosah": "Síť", "latence": "Odezva"} {
		if got, _ := pagesPanel(t, doc, panelID)["tab"].(string); got != want {
			t.Errorf("get returned panel %q on tab %q, want %q — the tab did not survive the "+
				"read path, and the page renders as one scroll", panelID, got, want)
		}
	}
}

func TestPagesCreate_APanelWithNoTabSendsNoTab(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")

	panel := pagesPanel(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201"), "sluzby")
	if _, present := panel["tab"]; present {
		// A page with no tabs renders exactly as it did before tabs existed:
		// no bar. An empty string would be a second way of saying that, and
		// the client would have to know both mean the same thing.
		t.Errorf("a panel that declared no tab carries one on the wire: %s", mustPagesJSON(t, panel))
	}
}

func TestPagesCreate_RefusesATabNobodyCanRead(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)

	body := strings.Replace(pagesTabbedBody("sit-2"), `"tab": "Síť"`, `"tab": "   "`, 1)
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want a refusal for a blank tab name — it draws nothing on the bar "+
			"and still hides the panels under it; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "blank tab") {
		t.Errorf("the refusal does not say what was wrong: %s", rr.Body.String())
	}
}

func TestPagesGet_SealedPlaceholderKeepsItsTab(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)

	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesTabbedBody("sit-3"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("create: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	db := h.db
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('outsider', 'outsider@example.com', 'Outsider')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm-outsider', ?, 'outsider', 'MEMBER')`, wsID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	doc := pagesGet(t, h, wsID, "outsider", "MEMBER", "sit-3")
	for panelID, want := range map[string]string{"dosah": "Síť", "latence": "Odezva"} {
		panel := pagesPanel(t, doc, panelID)
		if sealed, _ := panel["sealed"].(bool); !sealed {
			t.Fatalf("panel %q is not sealed for a viewer outside its crew: %s",
				panelID, mustPagesJSON(t, panel))
		}
		if got, _ := panel["tab"].(string); got != want {
			t.Errorf("sealed panel %q reports tab %q, want %q — a tab of only sealed panels must "+
				"still appear, or the bar reflows per viewer and its absence says whose data it "+
				"held", panelID, got, want)
		}
		// And it still withholds everything the placeholder is for.
		for _, forbidden := range []string{"schema", "producer", "sla_seconds", "data", "provenance", "state", "owner"} {
			if _, present := panel[forbidden]; present {
				t.Errorf("the sealed placeholder leaks %q: %s", forbidden, mustPagesJSON(t, panel))
			}
		}
	}
}
