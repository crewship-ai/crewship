package api

import (
	"encoding/json"
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
