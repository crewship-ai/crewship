package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
)

// The panel icon (internal/pages/icons.go) across the wire.
//
// The Go validator is tested in its own package; what is tested HERE is the
// part with machinery in it: `icon` has no column on page_panels — it is
// presentation, and the table carries the contract — so the read path attaches
// it from the parsed spec, next to the wake gates. That attachment is the hop
// where a field silently disappears, and a page whose icons come back as the
// schema's defaults looks exactly like a page whose author never set one.

// pagesIconBody is the standard create body with an icon on its panel.
func pagesIconBody(slug, icon string) string {
	return strings.Replace(pagesSpecBody(slug),
		`"title": "Jede to?",`,
		`"title": "Jede to?", "icon": "`+icon+`",`, 1)
}

func TestPagesCreate_IconRoundTripsToTheReadPath(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)

	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesIconBody("fleet-201", "container"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("create: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	// On the create response…
	var created map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("create response is not JSON: %v", err)
	}
	if got, _ := pagesPanel(t, created, "sluzby")["icon"].(string); got != "container" {
		t.Errorf("create echoed icon = %q, want %q", got, "container")
	}

	// …and, the half that has machinery behind it, on a later GET. The icon is
	// read back out of spec_json, because page_panels has no column for it.
	doc := pagesGet(t, h, wsID, userID, "OWNER", "fleet-201")
	if got, _ := pagesPanel(t, doc, "sluzby")["icon"].(string); got != "container" {
		t.Errorf("get returned icon = %q, want %q — the icon did not survive the read path",
			got, "container")
	}
}

func TestPagesCreate_RefusesAnIconOutsideTheClosedSet(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)

	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesIconBody("fleet-203", "MemoryStick"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want a refusal for an icon the client cannot draw; body: %s",
			rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, "MemoryStick") {
		t.Errorf("the refusal does not quote the icon it rejected: %s", body)
	}
	// The whole point of a closed set is that its refusal is actionable. An
	// author who has to go and find the list will guess again instead.
	for _, allowed := range pages.PanelIcons {
		if !strings.Contains(body, string(allowed)) {
			t.Fatalf("the refusal does not name %q as an allowed value: %s", allowed, body)
		}
	}
}

func TestPagesCreate_APanelWithNoIconSendsNoIcon(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-204")

	panel := pagesPanel(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-204"), "sluzby")
	if _, present := panel["icon"]; present {
		// Absent means "the icon this panel's schema implies". An empty string
		// would be a second way of saying the same thing, and the client would
		// have to know that both mean default.
		t.Errorf("a panel that declared no icon carries one on the wire: %s", mustPagesJSON(t, panel))
	}
}
