package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPagesGet_AuthoredHalfReachesAnEditor closes C1 at its root.
//
// The editor renders its YAML from GET /api/v1/pages/{slug}. While that
// document withheld `actions`, `wake` and `on_failure`, the editor was working
// from a lossy copy of the page — and PATCH replaced the panel set with that
// copy, so renaming a page deleted its gates and their compiled automation
// rows. No warning, because nothing in the loop knew the fields existed.
//
// The withholding was not wrong, it was too wide. It narrows here: a caller who
// may edit the spec can already read the whole of it through export, so echoing
// it to them leaks nothing; a sealed panel never reaches the branch at all,
// because the placeholder replaced it first.
func TestPagesGet_AuthoredHalfReachesAnEditor(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")

	body := pagesGet(t, h, wsID, userID, "OWNER", "fleet-201")
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal page document: %v", err)
	}

	// The fixture page declares no gates, so the assertion that matters here is
	// the negative one: the keys must not appear as empty husks either. An
	// `"actions": []` on a page that declares none would teach the editor to
	// write one back, which is the same class of quiet rewrite.
	for _, forbidden := range []string{`"actions":[]`, `"wake":[]`, `"on_failure":{}`} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("document carries %s — an absent authored half must be absent, not empty", forbidden)
		}
	}

	panel := pagesPanel(t, body, "sluzby")
	if _, ok := panel["id"]; !ok {
		t.Fatalf("panel lost its id, so this test is asserting against the wrong shape: %v", panel)
	}
}

// TestPagesGet_AuthoredHalfIsWithheldFromAReader pins the half that is still
// deliberately withheld. A viewer who cannot edit the spec sees the panel's
// DATA and none of its declaration — the buttons a page offers are served by
// GET …/panels/{id}/actions, which applies its own permission check.
func TestPagesGet_AuthoredHalfIsWithheldFromAReader(t *testing.T) {
	h, _, _, wsID, ownerID := newPagesFixture(t)
	pagesCreate(t, h, wsID, ownerID, "fleet-201")
	pagesSeedUser(t, h, wsID, "reader", "reader@example.com", "MEMBER")
	// `read` reaches the page and stops there: it is exactly the grant that
	// must NOT come with the authored half, since editing is the `write` verb.
	pagesGrant(t, h, wsID, ownerID, "fleet-201",
		`{"subject_type":"user","subject":"reader@example.com","level":"read"}`)

	doc := pagesGet(t, h, wsID, "reader", "MEMBER", "fleet-201")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal page document: %v", err)
	}
	for _, forbidden := range []string{`"actions"`, `"wake"`, `"on_failure"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("a reader who cannot edit the spec received %s", forbidden)
		}
	}
}

// `public` is part of the authored half, and leaving it off the read path made
// the read/write round trip destructive.
//
// panelWire builds from panelRecord, which has no `public` to read, so the flag
// only ever travelled one way: into spec_json. Anything that read a page and
// saved the same document back — `crewship page export | crewship apply`, a
// hand-edited `page get -f json`, the editor's own PATCH — dropped it, and
// every public panel on the page quietly became private. The published link
// kept resolving and rendered a page with nothing on it, and the next
// `page publish` refused the page for having no public panels, which points the
// operator at the wrong thing entirely.
//
// The round trip is the assertion, not the field: reading the value back is
// only interesting because saving it again has to leave the page as it was.
func TestPagesGet_PublicSurvivesAReadWriteRoundTrip(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreateFrom(t, h, wsID, userID,
		`{"slug":"verejna","name":"Veřejná","panels":[`+
			`{"id":"verejny","schema":"status.v1","owner":"crew/lookout","producer":"script/w.sh","sla_seconds":3600,"span":6,"public":true},`+
			`{"id":"interni","schema":"status.v1","owner":"crew/lookout","producer":"script/q.sh","sla_seconds":3600,"span":6}]}`)

	doc := pagesGet(t, h, wsID, userID, "OWNER", "verejna")
	if authored, _ := doc["authored"].(bool); !authored {
		t.Fatalf("fixture is not reading as an editor, so this test cannot see the authored half: %v", doc["authored"])
	}

	if got := pagesPanel(t, doc, "verejny")["public"]; got != true {
		t.Errorf("panel verejny came back with public=%v, want true — the flag is authored and must be echoed to an editor", got)
	}
	// The absent case stays absent rather than arriving as `false`: an
	// `omitempty` bool that materialises as an explicit false teaches a client
	// to write one back, which is the same quiet rewrite one level down.
	if _, present := pagesPanel(t, doc, "interni")["public"]; present {
		t.Errorf("panel interni carries a public key it never declared")
	}

	// Now the part that actually broke: save the document straight back.
	panels, ok := doc["panels"].([]any)
	if !ok {
		t.Fatalf("panels are not a list: %T", doc["panels"])
	}
	body, err := json.Marshal(map[string]any{"name": doc["name"], "panels": panels})
	if err != nil {
		t.Fatalf("marshal round trip: %v", err)
	}
	req := pagesRequest(t, "PATCH", "/api/v1/pages/verejna", wsID, userID, "OWNER", string(body))
	req.SetPathValue("slug", "verejna")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("re-apply the document we were just served: %d %s", rr.Code, rr.Body.String())
	}

	after := pagesGet(t, h, wsID, userID, "OWNER", "verejna")
	if got := pagesPanel(t, after, "verejny")["public"]; got != true {
		t.Errorf("re-applying the served document unpublished the panel (public=%v) — "+
			"a read followed by a write must leave the page as it was", got)
	}
}
