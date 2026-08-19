package api

// Public pages — the six rules, one test each (docs/prd/pages.md §7.3).
//
// §7.3 opens by calling this "the highest-risk surface in the feature", and the
// tests are written to that brief: every one of them is an assertion about what
// an outsider CANNOT reach, not about what the happy path renders. Where a rule
// can be broken in two different ways — a field added to a struct, a value
// copied into a payload — it gets two tests, because the two failures look
// nothing alike in a diff.
//
//	rule 1  read-only            TestPublicWire_CarriesNoActionField
//	                             TestPublicView_StripsActionsFromPayload
//	rule 2  opt-in per panel     TestPublicView_ServesOnlyPanelsMarkedPublic
//	rule 3  only a human         TestPublish_RefusesAnAgent
//	                             TestPublicView_IgnoresAPanelAnAgentMarkedPublic
//	rule 4  every link expires   TestPublish_ExpiryDefaultsTo30DaysAndCapsAt1Year
//	                             TestPublicView_RefusesAnExpiredLink
//	                             TestRevoke_TakesOneLinkWithoutBreakingTheOther
//	rule 5  provenance stripped  TestPublicView_StripsProvenanceByDefault
//	rule 6  noindex / limited /  TestPublicView_SetsNoindexAndNoReferrer
//	        logged               TestPublicView_IsRateLimitedPerToken
//	                             TestPublicView_JournalsFirstViewOfTheDayOnce
//
//	§7.3.2b  TestPublicView_FailedPanelShowsTheAgeAndNotTheReason
//	§7.3.3   TestPublicUnlock_* (four)

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/ratelimitcfg"
)

// ── Fixture ────────────────────────────────────────────────────────────────

// pagesPublicSpecBody is the spec every test in this file publishes.
//
// TWO panels, and only one of them is marked public. That asymmetry is the
// fixture's whole job: §7.3.2 rule 2 is default-deny, and a fixture where
// everything is public could not tell "the filter works" from "there was
// nothing to filter".
func pagesPublicSpecBody(slug string) string {
	return `{
		"slug": "` + slug + `",
		"name": "Uzávěrka",
		"panels": [
			{
				"id": "verejny",
				"schema": "status.v1",
				"title": "Stav služeb",
				"owner": "crew/lookout",
				"producer": "script/watch-services.sh",
				"sla_seconds": 3600,
				"span": 6,
				"public": true
			},
			{
				"id": "interni",
				"schema": "status.v1",
				"title": "Interní fronta",
				"owner": "crew/lookout",
				"producer": "script/watch-queue.sh",
				"sla_seconds": 3600,
				"span": 6
			}
		]
	}`
}

const (
	pagesPublicPayload   = `{"items":[{"name":"api","state":"ok","label":"200 OK"}]}`
	pagesInternalPayload = `{"items":[{"name":"fronta","state":"warning","label":"SECRET-QUEUE-DEPTH-91"}]}`
)

// newPagesPublicFixture builds a page with one public and one private panel,
// both fed, and returns the public handler over it.
func newPagesPublicFixture(t *testing.T, slug string) (*PagePublicHandler, *pagesJournalSpy, *pagesFakeClock, string, string) {
	t.Helper()
	h, spy, clock, wsID, userID := newPagesFixture(t)
	pagesCreateFrom(t, h, wsID, userID, pagesPublicSpecBody(slug))
	if rr := pagesPush(t, h, wsID, userID, "OWNER", slug, "verejny", pagesPublicPayload); rr.Code != http.StatusOK {
		t.Fatalf("push public panel: %d %s", rr.Code, rr.Body.String())
	}
	clock.advance(time.Minute)
	if rr := pagesPush(t, h, wsID, userID, "OWNER", slug, "interni", pagesInternalPayload); rr.Code != http.StatusOK {
		t.Fatalf("push private panel: %d %s", rr.Code, rr.Body.String())
	}
	return NewPagePublicHandler(h), spy, clock, wsID, userID
}

// pagesCreateFrom creates a page from an explicit body (pagesCreate in
// pages_handler_test.go hardcodes its own single-panel spec).
func pagesCreateFrom(t *testing.T, h *PageHandler, wsID, userID, body string) {
	t.Helper()
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create page: %d %s", rr.Code, rr.Body.String())
	}
}

// pagesHumanRequest is pagesRequest plus the credential kind RequireAuth
// records for a human. pageGrantCallerIsAgent is a POSITIVE test for humanity —
// an empty auth kind denies — so a publish request built without this is
// refused, which is the correct default and the wrong fixture.
func pagesHumanRequest(t *testing.T, method, target, wsID, userID, role, body string) *http.Request {
	t.Helper()
	req := pagesRequest(t, method, target, wsID, userID, role, body)
	return req.WithContext(context.WithValue(req.Context(), ctxAuthKind, AuthKindSession))
}

// pagesPublish mints a link and returns the decoded 201.
func pagesPublish(t *testing.T, h *PagePublicHandler, wsID, userID, slug, body string) map[string]any {
	t.Helper()
	rr := pagesPublishRaw(t, h, wsID, userID, slug, body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("publish page: %d %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("publish response is not JSON: %v", err)
	}
	return out
}

func pagesPublishRaw(t *testing.T, h *PagePublicHandler, wsID, userID, slug, body string) *httptest.ResponseRecorder {
	t.Helper()
	return pagesPublishAs(t, h, wsID, userID, "OWNER", slug, body)
}

func pagesPublishAs(t *testing.T, h *PagePublicHandler, wsID, userID, role, slug, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := pagesHumanRequest(t, "POST", "/api/v1/pages/"+slug+"/public", wsID, userID, role, body)
	req.SetPathValue("slug", slug)
	rr := httptest.NewRecorder()
	h.Publish(rr, req)
	return rr
}

// pagesPublicGet drives the unauthenticated view path. No user, no workspace,
// no role in the context — §7.3.1: this surface shares none of them.
func pagesPublicGet(t *testing.T, h *PagePublicHandler, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/public/pages/"+token, nil)
	req.RemoteAddr = "203.0.113.44:51244"
	req.SetPathValue("token", token)
	rr := httptest.NewRecorder()
	h.View(rr, req)
	return rr
}

func pagesPublicUnlock(t *testing.T, h *PagePublicHandler, token, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	if target == "" {
		target = "/api/v1/public/pages/" + token + "/unlock"
	}
	req := httptest.NewRequest("POST", target, strings.NewReader(body))
	req.RemoteAddr = "203.0.113.44:51244"
	req.SetPathValue("token", token)
	rr := httptest.NewRecorder()
	h.Unlock(rr, req)
	return rr
}

func pagesTokenOf(t *testing.T, publish map[string]any) string {
	t.Helper()
	tok, _ := publish["token"].(string)
	if tok == "" {
		t.Fatalf("publish returned no token: %v", publish)
	}
	return tok
}

// pagesPublicPanels decodes the panel list from a public document.
func pagesPublicPanels(t *testing.T, rr *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("public view: %d %s", rr.Code, rr.Body.String())
	}
	var doc struct {
		Panels []map[string]any `json:"panels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("public view is not JSON: %v", err)
	}
	return doc.Panels
}

// ── Rule 1 — read-only. No actions, ever. ──────────────────────────────────

// TestPublicWire_CarriesNoActionField is the structural half of rule 1.
//
// "A public page renders no buttons. A button behind a public link is remote
// code execution with a URL for a credential. `PageAction` is stripped
// server-side before serialisation, not hidden in CSS."
//
// The strongest possible reading of "stripped before serialisation" is that
// there is nothing to strip: the public wire is built additively, field by
// field, so an action cannot be forgotten on the way out. This test pins the
// field set, so the day §8b lands and pagePanelWire grows an `actions` field,
// somebody has to come here and delete a line of a test that explains why they
// should not — rather than shipping a public page with buttons on it because a
// struct they were editing happened to be embedded somewhere.
func TestPublicWire_CarriesNoActionField(t *testing.T) {
	cases := []struct {
		typ   reflect.Type
		allow []string
	}{
		{
			reflect.TypeOf(pagePublicPanelWire{}),
			[]string{"ID", "Schema", "Title", "Span", "State", "ProducedAt", "Data", "Provenance"},
		},
		{
			reflect.TypeOf(pagePublicWire{}),
			[]string{"Slug", "Name", "Description", "Panels", "GeneratedAt", "ShowProvenance", "ExpiresAt"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.typ.Name(), func(t *testing.T) {
			allowed := map[string]bool{}
			for _, f := range tc.allow {
				allowed[f] = true
			}
			for i := 0; i < tc.typ.NumField(); i++ {
				name := tc.typ.Field(i).Name
				if !allowed[name] {
					t.Errorf("%s grew a field %q that the public wire has never carried — "+
						"§7.3.2 rule 1 and rule 5 make this type an ALLOW-LIST, not a copy of the internal one. "+
						"If the field is genuinely safe for a reader outside the workspace, add it to this test's list "+
						"and say in the PR why an outsider may see it",
						tc.typ.Name(), name)
				}
				delete(allowed, name)
			}
			for name := range allowed {
				t.Errorf("%s lost field %q, which the public renderer reads", tc.typ.Name(), name)
			}
		})
	}

	// The internal panel wire carries three fields the public one deliberately
	// does not. Naming them here means "we removed provenance" cannot silently
	// become "we removed provenance and also the reason came back".
	internal := reflect.TypeOf(pagePanelWire{})
	public := reflect.TypeOf(pagePublicPanelWire{})
	for _, forbidden := range []string{"Owner", "Producer", "Reason", "SLASeconds"} {
		if _, ok := internal.FieldByName(forbidden); !ok {
			t.Fatalf("pagePanelWire no longer has %q — this test is comparing against a type that moved", forbidden)
		}
		if _, ok := public.FieldByName(forbidden); ok {
			t.Errorf("pagePublicPanelWire carries %q: crew slugs, producer names and failure text are internal "+
				"vocabulary (§7.3.2 rule 5, §7.3.2b)", forbidden)
		}
	}
}

// TestPublicView_StripsActionsFromPayload is the payload half of rule 1.
//
// The spec cannot carry an action today, but a PAYLOAD is producer-supplied
// JSON, and the reserved schemas (narrative.v1, embed.v1) are exactly where one
// would arrive first. The row is written directly rather than pushed, because
// status.v1 is `additionalProperties: false` and the push path would refuse it —
// which is the point: this test covers the door the push validator does not.
func TestPublicView_StripsActionsFromPayload(t *testing.T) {
	t.Run("unit", func(t *testing.T) {
		cases := []struct {
			name string
			in   string
			want []string // substrings that must NOT survive
			keep []string // substrings that must survive
		}{
			{"actions array", `{"value":3,"actions":[{"id":"rm","routine":"delete-everything"}]}`,
				[]string{"delete-everything", "actions"}, []string{`"value"`}},
			{"single action", `{"value":3,"action":{"routine":"deploy"}}`,
				[]string{"deploy", `"action"`}, []string{`"value"`}},
			{"mixed case", `{"value":3,"Actions":[{"routine":"deploy"}]}`,
				[]string{"deploy"}, []string{`"value"`}},
			{"no actions is untouched", `{"items":[{"name":"api","state":"ok"}]}`,
				nil, []string{"api"}},
			{"a non-object payload is left alone", `[1,2,3]`, nil, []string{"1,2,3"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := string(stripPublicPayloadActions(json.RawMessage(tc.in)))
				for _, bad := range tc.want {
					if strings.Contains(got, bad) {
						t.Errorf("%q survived the strip: %s", bad, got)
					}
				}
				for _, good := range tc.keep {
					if !strings.Contains(got, good) {
						t.Errorf("%q was lost: %s", good, got)
					}
				}
			})
		}
	})

	t.Run("end to end", func(t *testing.T) {
		h, _, _, wsID, userID := newPagesPublicFixture(t, "akce")
		// Overwrite the stored payload with one carrying an action. A producer
		// on a future schema could push exactly this.
		if _, err := h.db.Exec(`
			UPDATE page_panel_data
			   SET payload_json = ?
			 WHERE panel_id = (SELECT id FROM page_panels WHERE panel_id = 'verejny')`,
			`{"items":[{"name":"api","state":"ok"}],"actions":[{"id":"wipe","kind":"call","routine":"drop-database","label":"Run"}]}`); err != nil {
			t.Fatalf("plant action payload: %v", err)
		}

		token := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "akce", ""))
		rr := pagesPublicGet(t, h, token)
		body := rr.Body.String()
		for _, leak := range []string{"actions", "drop-database", "wipe"} {
			if strings.Contains(body, leak) {
				t.Fatalf("§7.3.2 rule 1: a public page served %q — a button behind a public link is "+
					"remote code execution with a URL for a credential. body=%s", leak, body)
			}
		}
		if !strings.Contains(body, `"api"`) {
			t.Fatalf("the strip took the data with it: %s", body)
		}
	})
}

// ── Rule 2 — opt-in per panel, not per page. ───────────────────────────────

// TestPublicView_ServesOnlyPanelsMarkedPublic covers rule 2: "Publishing a page
// publishes only the panels explicitly marked `public: true`. Default deny.
// Publishing must never be a bulk action over panels the author has not looked
// at."
//
// The assertion is on the RAW BODY and not on the decoded panel list, because
// the failure this guards against is a field carrying the private panel's
// payload somewhere the decoder was not looking.
func TestPublicView_ServesOnlyPanelsMarkedPublic(t *testing.T) {
	h, _, _, wsID, userID := newPagesPublicFixture(t, "uzaverka")
	token := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "uzaverka", ""))

	rr := pagesPublicGet(t, h, token)
	panels := pagesPublicPanels(t, rr)
	if len(panels) != 1 {
		t.Fatalf("public page served %d panels, want 1 (only `verejny` is marked public): %s",
			len(panels), rr.Body.String())
	}
	if id, _ := panels[0]["id"].(string); id != "verejny" {
		t.Fatalf("public page served panel %q, want verejny", id)
	}
	body := rr.Body.String()
	for _, leak := range []string{"interni", "Interní fronta", "SECRET-QUEUE-DEPTH-91"} {
		if strings.Contains(body, leak) {
			t.Fatalf("§7.3.2 rule 2: the unmarked panel leaked %q into a public page. body=%s", leak, body)
		}
	}

	// A public document must never be round-trippable back into a page spec.
	// If it were, somebody would eventually PATCH one back — and since it
	// carries only the PUBLIC panels, that write would delete every panel the
	// link was filtering out. The manifest surface refuses to export a page
	// holding a sealed panel for exactly this reason; here the property is
	// structural rather than a check, because the public wire simply does not
	// carry the fields a spec requires.
	var spec struct {
		Name   string          `json:"name"`
		Panels []pagePanelWire `json:"panels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &spec); err != nil {
		t.Fatalf("public view is not JSON: %v", err)
	}
	specs, ok := panelSpecsFrom(httptest.NewRecorder(), spec.Panels)
	if !ok {
		return // refused before validation; that is also a refusal
	}
	doc := &pages.Document{
		APIVersion: pages.DocumentAPIVersion,
		Kind:       pages.DocumentKind,
		Metadata:   pages.Metadata{Name: "x", Slug: "x"},
		Spec:       pages.Spec{Panels: specs},
	}
	if err := doc.Validate(); err == nil {
		t.Fatal("a public document validates as a page spec — it would be PATCH-able back, " +
			"and since it carries only the public panels that write would delete the rest")
	}
}

// TestPublish_RefusesAPageWithNothingMarkedPublic is the other half of default
// deny: publishing a page nobody has opted a panel into is refused rather than
// producing an empty link somebody believes is working.
func TestPublish_RefusesAPageWithNothingMarkedPublic(t *testing.T) {
	base, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, base, wsID, userID, "nic")
	h := NewPagePublicHandler(base)

	rr := pagesPublishRaw(t, h, wsID, userID, "nic", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("publish with no public panel: %d %s, want 400", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "public") {
		t.Errorf("the refusal does not name the knob that is missing: %s", rr.Body.String())
	}
}

// ── Rule 3 — only a human publishes. ───────────────────────────────────────

// TestPublish_RefusesAnAgent covers the first half of rule 3: "An agent can
// build the page; it cannot make it public."
//
// Every marker an agent-originated request carries is tried, plus the default
// (no recorded credential kind at all), because the check is a positive test
// for humanity and the failure mode worth guarding is somebody replacing it
// with a blocklist that goes out of date.
func TestPublish_RefusesAnAgent(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(*http.Request) *http.Request
	}{
		{"no recorded credential kind", func(r *http.Request) *http.Request {
			return r.WithContext(context.WithValue(r.Context(), ctxAuthKind, ""))
		}},
		{"internal token header", func(r *http.Request) *http.Request {
			r.Header.Set("X-Internal-Token", "internal-secret")
			return r
		}},
		{"acting agent header", func(r *http.Request) *http.Request {
			r.Header.Set(actingAgentSlugHeader, "watcher")
			return r
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _, wsID, userID := newPagesPublicFixture(t, "agent-"+strings.ReplaceAll(tc.name, " ", "-"))
			slug := "agent-" + strings.ReplaceAll(tc.name, " ", "-")
			req := tc.prepare(pagesHumanRequest(t, "POST", "/api/v1/pages/"+slug+"/public", wsID, userID, "OWNER", ""))
			req.SetPathValue("slug", slug)
			rr := httptest.NewRecorder()
			h.Publish(rr, req)

			if rr.Code != http.StatusForbidden {
				t.Fatalf("§7.3.2 rule 3: an agent-originated publish returned %d, want 403. body=%s",
					rr.Code, rr.Body.String())
			}
			var count int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_public_tokens`).Scan(&count); err != nil {
				t.Fatalf("count tokens: %v", err)
			}
			if count != 0 {
				t.Fatalf("a refused publish still minted %d token(s)", count)
			}
		})
	}
}

// TestPublicView_IgnoresAPanelAnAgentMarkedPublic covers the second half of
// rule 3: an agent "cannot add a panel to an already-public page without that
// panel being separately marked by a human".
//
// The mechanism is publicPanelIDs intersecting the current spec with the newest
// HUMAN-authored version of it. Here the page is edited so `interni` is public
// too, and the accompanying version row is authored by an agent — which is what
// an agent rebuilding a page through §7.1b's `write` grant produces. The
// internal page changes; the public one does not, until a human saves.
func TestPublicView_IgnoresAPanelAnAgentMarkedPublic(t *testing.T) {
	h, _, _, wsID, userID := newPagesPublicFixture(t, "rozsireni")
	token := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "rozsireni", ""))

	var pageID string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug = 'rozsireni'`).Scan(&pageID); err != nil {
		t.Fatalf("load page id: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO agents (id, workspace_id, name, slug) VALUES ('ag-1', ?, 'Watcher', 'watcher')`, wsID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	// The agent's rewrite: both panels public in the live spec, and a version
	// row that records an agent — never a human — as its author.
	widened := strings.Replace(pagesPublicSpecBody("rozsireni"),
		`"span": 6
			}
		]`, `"span": 6,
				"public": true
			}
		]`, 1)
	if !strings.Contains(widened, `"public": true`) {
		t.Fatal("fixture rewrite did not take")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(widened), &doc); err != nil {
		t.Fatalf("rewrite is not JSON: %v", err)
	}
	spec := `{"apiVersion":"crewship/v1","kind":"Page","metadata":{"name":"Uzávěrka","slug":"rozsireni"},"spec":{"panels":[` +
		`{"id":"verejny","schema":"status.v1","title":"Stav služeb","owner":"crew/lookout","producer":"script/watch-services.sh","sla":"3600s","span":6,"public":true},` +
		`{"id":"interni","schema":"status.v1","title":"Interní fronta","owner":"crew/lookout","producer":"script/watch-queue.sh","sla":"3600s","span":6,"public":true}]}}`
	if _, err := h.db.Exec(`UPDATE pages SET spec_json = ? WHERE id = ?`, spec, pageID); err != nil {
		t.Fatalf("apply agent rewrite: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO page_versions (page_id, seq, spec_json, author_user_id, author_agent_id, created_at)
		VALUES (?, 99, ?, NULL, 'ag-1', '2026-08-12T10:00:00Z')`, pageID, spec); err != nil {
		t.Fatalf("insert agent-authored version: %v", err)
	}

	panels := pagesPublicPanels(t, pagesPublicGet(t, h, token))
	if len(panels) != 1 {
		t.Fatalf("§7.3.2 rule 3: an agent widened a public page from %d to %d panels without a human "+
			"marking the new one — an agent may not widen reach, and public is the widest reach there is",
			1, len(panels))
	}

	// A human saving the same spec is the separate marking rule 3 asks for, and
	// then the panel appears.
	if _, err := h.db.Exec(`
		INSERT INTO page_versions (page_id, seq, spec_json, author_user_id, created_at)
		VALUES (?, 100, ?, ?, '2026-08-12T11:00:00Z')`, pageID, spec, userID); err != nil {
		t.Fatalf("insert human-authored version: %v", err)
	}
	if panels := pagesPublicPanels(t, pagesPublicGet(t, h, token)); len(panels) != 2 {
		t.Fatalf("after a human marked the panel the public page still shows %d panel(s), want 2", len(panels))
	}
}

// ── Rule 4 — every link expires. ───────────────────────────────────────────

// TestPublish_ExpiryDefaultsTo30DaysAndCapsAt1Year pins the two constants rule
// 4 names, and the two refusals that keep "never" from being expressible.
func TestPublish_ExpiryDefaultsTo30DaysAndCapsAt1Year(t *testing.T) {
	if PagePublicTokenDefaultTTL != 30*24*time.Hour {
		t.Errorf("PagePublicTokenDefaultTTL = %v, want 30 days (§7.3.2 rule 4)", PagePublicTokenDefaultTTL)
	}
	if PagePublicTokenMaxTTL != 365*24*time.Hour {
		t.Errorf("PagePublicTokenMaxTTL = %v, want 1 year (§7.3.2 rule 4)", PagePublicTokenMaxTTL)
	}

	h, _, clock, wsID, userID := newPagesPublicFixture(t, "platnost")

	t.Run("default is 30 days", func(t *testing.T) {
		out := pagesPublish(t, h, wsID, userID, "platnost", "")
		got, _ := out["expires_at"].(string)
		want := clock.now.Add(PagePublicTokenDefaultTTL).UTC().Format(time.RFC3339)
		if got != want {
			t.Fatalf("expires_at = %q, want %q", got, want)
		}
	})

	t.Run("an explicit expiry inside the ceiling is honoured", func(t *testing.T) {
		out := pagesPublish(t, h, wsID, userID, "platnost", `{"expires_in_days": 7}`)
		got, _ := out["expires_at"].(string)
		want := clock.now.Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)
		if got != want {
			t.Fatalf("expires_at = %q, want %q", got, want)
		}
	})

	for _, tc := range []struct{ name, body string }{
		{"a year and a day is refused", `{"expires_in_days": 366}`},
		{"zero is refused rather than read as never", `{"expires_in_days": 0}`},
		{"negative is refused", `{"expires_in_days": -1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := pagesPublishRaw(t, h, wsID, userID, "platnost", tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("%s: %d %s, want 400", tc.name, rr.Code, rr.Body.String())
			}
		})
	}

	// Every row that reached the database carries an expiry inside the ceiling.
	rows, err := h.db.Query(`SELECT expires_at FROM page_public_tokens`)
	if err != nil {
		t.Fatalf("read expiries: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var at string
		if err := rows.Scan(&at); err != nil {
			t.Fatalf("scan expiry: %v", err)
		}
		n++
		if parsed := parsePageTime(at); parsed.IsZero() || parsed.After(clock.now.Add(PagePublicTokenMaxTTL)) {
			t.Errorf("a stored token expires at %q, which is outside the one-year ceiling", at)
		}
	}
	if n == 0 {
		t.Fatal("no tokens were stored, so the expiry assertion proved nothing")
	}
}

// TestPublicView_RefusesAnExpiredLink is rule 4 at read time: the expiry is not
// decoration on a list screen, it is checked on every view.
func TestPublicView_RefusesAnExpiredLink(t *testing.T) {
	h, _, clock, wsID, userID := newPagesPublicFixture(t, "prosla")
	token := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "prosla", `{"expires_in_days": 1}`))

	if rr := pagesPublicGet(t, h, token); rr.Code != http.StatusOK {
		t.Fatalf("a live link returned %d %s", rr.Code, rr.Body.String())
	}
	clock.advance(24*time.Hour + time.Second)
	rr := pagesPublicGet(t, h, token)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("an expired link returned %d, want 404. body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), pagePublicUnavailable) {
		t.Errorf("an expired link answered with something other than the one generic refusal: %s", rr.Body.String())
	}
}

// TestPublicToken_IsHighEntropyAndStoredHashed covers the rest of rule 4's
// sentence: "Tokens are high-entropy, revocable individually".
func TestPublicToken_IsHighEntropyAndStoredHashed(t *testing.T) {
	h, _, _, wsID, userID := newPagesPublicFixture(t, "entropie")
	first := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "entropie", ""))
	second := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "entropie", ""))

	if first == second {
		t.Fatal("two publishes minted the same token")
	}
	// 32 random bytes as unpadded base64url is 43 characters.
	if len(first) < 43 {
		t.Errorf("token is %d characters; 32 bytes of entropy is 43", len(first))
	}
	var stored int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_public_tokens WHERE token_hash = ?`, first).Scan(&stored); err != nil {
		t.Fatalf("query by raw token: %v", err)
	}
	if stored != 0 {
		t.Fatal("the token itself is stored in token_hash — holding the token IS the authorisation, so a readable column is a credential store")
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_public_tokens WHERE token_hash = ?`,
		hashPagePublicToken(first)).Scan(&stored); err != nil {
		t.Fatalf("query by hash: %v", err)
	}
	if stored != 1 {
		t.Fatalf("the token hash does not identify exactly one row (got %d)", stored)
	}
}

// TestRevoke_TakesOneLinkWithoutBreakingTheOther is the last clause of rule 4:
// "one page may have several so revoking the accountant's link does not break
// the client's".
func TestRevoke_TakesOneLinkWithoutBreakingTheOther(t *testing.T) {
	h, _, _, wsID, userID := newPagesPublicFixture(t, "dva")
	accountant := pagesPublish(t, h, wsID, userID, "dva", "")
	client := pagesPublish(t, h, wsID, userID, "dva", "")

	req := pagesHumanRequest(t, "DELETE",
		fmt.Sprintf("/api/v1/pages/dva/public/%s", accountant["id"]), wsID, userID, "OWNER", "")
	req.SetPathValue("slug", "dva")
	req.SetPathValue("tokenId", accountant["id"].(string))
	rr := httptest.NewRecorder()
	h.RevokePublicLink(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
	}

	if got := pagesPublicGet(t, h, pagesTokenOf(t, accountant)); got.Code != http.StatusNotFound {
		t.Fatalf("the revoked link still answers %d", got.Code)
	}
	if got := pagesPublicGet(t, h, pagesTokenOf(t, client)); got.Code != http.StatusOK {
		t.Fatalf("revoking one link broke the other: %d %s", got.Code, got.Body.String())
	}
}

// ── Rule 5 — provenance stripped by default. ───────────────────────────────

// TestPublicView_StripsProvenanceByDefault covers rule 5: "Run ids, agent
// slugs, crew slugs and producer names are internal vocabulary. A public panel
// shows the value, the unit and the age — nothing that maps our org chart for a
// reader outside it. An author may opt provenance back in per page."
func TestPublicView_StripsProvenanceByDefault(t *testing.T) {
	t.Run("off by default", func(t *testing.T) {
		h, _, _, wsID, userID := newPagesPublicFixture(t, "puvod")
		token := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "puvod", ""))
		rr := pagesPublicGet(t, h, token)
		body := rr.Body.String()

		// The internal vocabulary rule 5 names, checked against the RAW body so
		// a value carried in a field the decoder was not looking at is still
		// caught. `show_provenance: false` is the token's own setting and is the
		// only occurrence of the word the response is allowed to contain.
		for _, leak := range []string{"producer", "run_id", "watch-services.sh", "crew/", "lookout", "push:"} {
			if strings.Contains(body, leak) {
				t.Errorf("§7.3.2 rule 5: %q reached a public page with provenance off. body=%s", leak, body)
			}
		}
		panels := pagesPublicPanels(t, rr)
		if _, ok := panels[0]["provenance"]; ok {
			t.Errorf("§7.3.2 rule 5: the panel carries a provenance object by default: %v", panels[0])
		}
		// The age is NOT provenance and must survive the strip (§7.3.2b).
		if at, _ := panels[0]["produced_at"].(string); at == "" {
			t.Fatalf("the panel lost its produced_at: §7.3.2b says a public panel ALWAYS carries when its data was produced. %s", body)
		}
	})

	t.Run("opted back in per token", func(t *testing.T) {
		h, _, _, wsID, userID := newPagesPublicFixture(t, "puvod-on")
		token := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "puvod-on", `{"show_provenance": true}`))
		panels := pagesPublicPanels(t, pagesPublicGet(t, h, token))
		prov, ok := panels[0]["provenance"].(map[string]any)
		if !ok {
			t.Fatalf("show_provenance did not turn provenance on: %v", panels[0])
		}
		if producer, _ := prov["producer"].(string); producer == "" {
			t.Errorf("provenance is present but empty: %v", prov)
		}
	})

	t.Run("the column defaults to off", func(t *testing.T) {
		h, _, _, wsID, userID := newPagesPublicFixture(t, "puvod-col")
		pagesPublish(t, h, wsID, userID, "puvod-col", "")
		var showProvenance int
		if err := h.db.QueryRow(`SELECT show_provenance FROM page_public_tokens`).Scan(&showProvenance); err != nil {
			t.Fatalf("read show_provenance: %v", err)
		}
		if showProvenance != 0 {
			t.Error("publishing without saying anything about provenance stored show_provenance = 1; " +
				"the default has to be the value that leaks nothing, so forgetting cannot be the disclosure")
		}
	})
}

// ── Rule 6 — not indexable, rate limited, logged. ──────────────────────────

// TestPublicView_SetsNoindexAndNoReferrer covers the first half of rule 6, on
// every response and not only the successful one: a 404 for a mistyped token is
// as indexable as a 200 if nobody says otherwise.
func TestPublicView_SetsNoindexAndNoReferrer(t *testing.T) {
	h, _, _, wsID, userID := newPagesPublicFixture(t, "roboti")
	token := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "roboti", ""))

	for _, tc := range []struct {
		name  string
		token string
	}{
		{"a live link", token},
		{"an unknown link", "definitely-not-a-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := pagesPublicGet(t, h, tc.token)
			if got := rr.Header().Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
				t.Errorf("X-Robots-Tag = %q, want it to carry noindex (§7.3.2 rule 6)", got)
			}
			if got := rr.Header().Get("Referrer-Policy"); got != "no-referrer" {
				t.Errorf("Referrer-Policy = %q, want no-referrer — the token IS the credential and it is in the URL", got)
			}
			if got := rr.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
				t.Errorf("Cache-Control = %q, want no-store: a cached render outlives the revocation", got)
			}
			if got := rr.Header().Get("X-Frame-Options"); got != "DENY" {
				t.Errorf("X-Frame-Options = %q, want DENY (§7.3.4 keeps third-party embedding out of 1.0)", got)
			}
			if cookies := rr.Result().Cookies(); len(cookies) > 0 {
				t.Errorf("the public surface set %d cookie(s); §7.3.1 says it shares no session and no cookie with the app", len(cookies))
			}
		})
	}
}

// TestPublicView_IsRateLimitedPerToken covers "a per-token request cap".
//
// The handler's own limiter is replaced with a two-request one so the boundary
// is exercised in a test that finishes, rather than by firing 601 requests. The
// SHIPPED number is asserted separately, against the registry that
// config/rate-limits.yml documents.
func TestPublicView_IsRateLimitedPerToken(t *testing.T) {
	if got := ratelimitcfg.Int(ratelimitcfg.KeyPagesPublicViewPerHr); got != 600 {
		t.Errorf("the shipped public-view cap is %d/h, want 600 (§7.3.2 rule 6, §10b.3)", got)
	}

	h, _, clock, wsID, userID := newPagesPublicFixture(t, "limit")
	first := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "limit", ""))
	second := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "limit", ""))
	h.views = newPagePublicLimiter(func() int { return 2 }, 2)

	for i := 1; i <= 2; i++ {
		if rr := pagesPublicGet(t, h, first); rr.Code != http.StatusOK {
			t.Fatalf("view %d returned %d, want 200", i, rr.Code)
		}
	}
	rr := pagesPublicGet(t, h, first)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("the third view returned %d, want 429", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("the 429 carries no Retry-After; that is the pattern the rest of the product uses")
	}

	// Per TOKEN, not per page: the accountant hammering their link must not
	// spend the client's budget.
	if rr := pagesPublicGet(t, h, second); rr.Code != http.StatusOK {
		t.Fatalf("the second link on the same page returned %d — the cap is per token", rr.Code)
	}

	// And the bucket refills.
	clock.advance(time.Hour)
	if ok, _ := h.views.Allow(clock.now, "anything"); !ok {
		t.Error("a fresh key was refused")
	}
}

// TestPublicView_JournalsFirstViewOfTheDayOnce covers the last clause of rule
// 6: "a journal entry for each token's first view per day so the owner can see
// the link is being used and by roughly whom".
//
// Both halves are load-bearing. ONE entry for many views is what keeps the
// audit trail readable — a page refreshed every thirty seconds would otherwise
// write 2 880 rows a day. AND an entry the next day, because a link that is
// still being read a month after everybody forgot about it is the exact thing
// the owner is meant to notice.
func TestPublicView_JournalsFirstViewOfTheDayOnce(t *testing.T) {
	h, spy, clock, wsID, userID := newPagesPublicFixture(t, "denik")
	token := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "denik", ""))

	count := func() int {
		n := 0
		for _, e := range spy.entries {
			if e.Type == journalPagePublicView {
				n++
			}
		}
		return n
	}

	for i := 0; i < 5; i++ {
		if rr := pagesPublicGet(t, h, token); rr.Code != http.StatusOK {
			t.Fatalf("view %d: %d", i, rr.Code)
		}
		clock.advance(time.Minute)
	}
	if got := count(); got != 1 {
		t.Fatalf("five views in one day wrote %d journal entries, want exactly 1", got)
	}

	entry := spy.firstOfType(journalPagePublicView)
	if entry == nil {
		t.Fatal("no page.public_view entry was written")
	}
	if entry.WorkspaceID != wsID {
		t.Errorf("the entry is filed under workspace %q, want %q", entry.WorkspaceID, wsID)
	}
	if entry.ActorType != journal.ActorSystem {
		t.Errorf("actor type = %q; a public reader has no account, and inventing one would be the first lie in the audit trail", entry.ActorType)
	}
	hint, _ := entry.Payload["viewer_hint"].(string)
	if hint != "203.0.113.0/24" {
		t.Errorf("viewer_hint = %q, want the /24 prefix — 'roughly whom', not 'exactly who'", hint)
	}
	if strings.Contains(fmt.Sprint(entry.Payload), "203.0.113.44") {
		t.Error("the full viewer address was recorded; the prefix answers the owner's question without identifying a person")
	}

	// Tomorrow the link is still being read, and the owner hears about it.
	clock.advance(24 * time.Hour)
	if rr := pagesPublicGet(t, h, token); rr.Code != http.StatusOK {
		t.Fatalf("view the next day: %d", rr.Code)
	}
	if got := count(); got != 2 {
		t.Fatalf("a view on the following day wrote %d entries in total, want 2", got)
	}
}

// TestPublish_IsJournalled — publishing is the widest reach the product has, so
// it is on the timeline before anybody has to ask who did it.
func TestPublish_IsJournalled(t *testing.T) {
	h, spy, _, wsID, userID := newPagesPublicFixture(t, "audit")
	pagesPublish(t, h, wsID, userID, "audit", "")

	entry := spy.firstOfType(journalPagePublished)
	if entry == nil {
		t.Fatal("publishing a page wrote no journal entry")
	}
	if entry.ActorID != userID {
		t.Errorf("actor = %q, want the publishing human %q", entry.ActorID, userID)
	}
	if entry.Severity != journal.SeverityNotice {
		t.Errorf("severity = %q, want notice", entry.Severity)
	}
	if _, ok := entry.Payload["expires_at"]; !ok {
		t.Error("the entry does not record when the link expires")
	}
}

// ── §7.3.2b — show the age, never the reason. ──────────────────────────────

// TestPublicView_FailedPanelShowsTheAgeAndNotTheReason covers §7.3.2b: "A
// public failed panel reads 'Data nejsou aktuální — poslední hodnota 12:40' and
// nothing more. The detail stays on the internal page for the people who can
// act."
//
// The internal page is asserted alongside, because the rule is not "delete the
// reason" — it is "the reason exists and stays inside".
func TestPublicView_FailedPanelShowsTheAgeAndNotTheReason(t *testing.T) {
	h, _, clock, wsID, userID := newPagesPublicFixture(t, "selhani")
	token := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "selhani", ""))

	clock.advance(time.Minute)
	req := pagesRequest(t, "PUT", "/api/v1/pages/selhani/panels/verejny/data?state=failed",
		wsID, userID, "OWNER", `{"items":[{"name":"api","state":"critical","label":"OOM in crew-container-3"}]}`)
	req.SetPathValue("slug", "selhani")
	req.SetPathValue("panelId", "verejny")
	rr := httptest.NewRecorder()
	h.PushData(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("push failure: %d %s", rr.Code, rr.Body.String())
	}

	view := pagesPublicGet(t, h, token)
	panels := pagesPublicPanels(t, view)
	if len(panels) != 1 {
		t.Fatalf("want 1 panel, got %d", len(panels))
	}
	panel := panels[0]

	if state, _ := panel["state"].(string); state != "failed" {
		t.Fatalf("state = %q, want failed", state)
	}
	// Show the age.
	if at, _ := panel["produced_at"].(string); at == "" {
		t.Error("§7.3.2b: a failed public panel still says WHEN its last value was produced — " +
			"an outsider acting on a stale number will invoice on it")
	}
	// Hide the reason, and the payload that carries it.
	if _, ok := panel["reason"]; ok {
		t.Error("§7.3.2b: the failure reason reached a public page")
	}
	if _, ok := panel["data"]; ok {
		t.Error("§7.3.2b: a failed public panel served its payload, which is where the producer's own " +
			"failure text lives")
	}
	if strings.Contains(view.Body.String(), "OOM in crew-container-3") {
		t.Fatalf("internal failure vocabulary reached a public page: %s", view.Body.String())
	}

	// The same panel, internally, still says why.
	internal := pagesGet(t, h.PageHandler, wsID, userID, "OWNER", "selhani")
	got := pagesPanel(t, internal, "verejny")
	if state, _ := got["state"].(string); state != "failed" {
		t.Fatalf("internal state = %q, want failed", state)
	}
	if reason, _ := got["reason"].(string); reason == "" {
		t.Error("the internal page lost the reason too; §7.3.2b keeps the detail for the people who can act")
	}
}

// ── §7.3.3 — the password. ─────────────────────────────────────────────────

// TestPublicUnlock_PasswordIsHashedAndNeverInTheURL covers the two storage
// clauses: "stored hashed with the same primitives the auth layer already uses
// — never reversible, never in the URL".
func TestPublicUnlock_PasswordIsHashedAndNeverInTheURL(t *testing.T) {
	h, _, _, wsID, userID := newPagesPublicFixture(t, "heslo")
	const password = "uzaverka-2026"
	token := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "heslo", `{"password":"`+password+`"}`))

	var stored string
	if err := h.db.QueryRow(`SELECT password_hash FROM page_public_tokens`).Scan(&stored); err != nil {
		t.Fatalf("read password_hash: %v", err)
	}
	if stored == password || strings.Contains(stored, password) {
		t.Fatal("the password is recoverable from the column")
	}
	if !strings.HasPrefix(stored, "$2") {
		t.Errorf("password_hash = %q, want a bcrypt hash — §7.3.3 says the primitives the auth layer already uses", stored)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)); err != nil {
		t.Errorf("the stored hash does not verify the password: %v", err)
	}

	// The GET says a password is needed and serves nothing.
	rr := pagesPublicGet(t, h, token)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("a protected link answered %d without a password, want 401", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "Stav služeb") || strings.Contains(rr.Body.String(), "api") {
		t.Fatalf("a protected link served content before the password: %s", rr.Body.String())
	}

	// A password on the QUERY STRING is not a password. If this ever passed, the
	// secret would be in every proxy log between here and the reader.
	viaURL := pagesPublicUnlock(t, h, token,
		"/api/v1/public/pages/"+token+"/unlock?password="+password, "")
	if viaURL.Code != http.StatusUnauthorized {
		t.Fatalf("a password in the URL unlocked the page (%d) — §7.3.3: never in the URL", viaURL.Code)
	}

	// In the body, it works.
	ok := pagesPublicUnlock(t, h, token, "", `{"password":"`+password+`"}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("the correct password returned %d %s", ok.Code, ok.Body.String())
	}
	if !strings.Contains(ok.Body.String(), "Stav služeb") {
		t.Errorf("unlocking did not serve the page: %s", ok.Body.String())
	}
	if cookies := ok.Result().Cookies(); len(cookies) > 0 {
		t.Errorf("unlocking set %d cookie(s); §7.3.1 says this surface shares no session and no cookie", len(cookies))
	}
}

// TestPublicUnlock_WrongPasswordIsIndistinguishableFromUnknownToken is the
// clause §7.3.3 spends its last sentence on: "the failure must not distinguish
// 'wrong password' from 'unknown token'".
//
// Byte-for-byte, status and body, for four different failures — including the
// two an attacker would use to tell a real token from a guess.
func TestPublicUnlock_WrongPasswordIsIndistinguishableFromUnknownToken(t *testing.T) {
	h, _, _, wsID, userID := newPagesPublicFixture(t, "shoda")
	real := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "shoda", `{"password":"spravne-heslo"}`))

	cases := map[string]struct{ token, body string }{
		"wrong password on a real token": {real, `{"password":"spatne-heslo"}`},
		"any password on an unknown token": {"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			`{"password":"spravne-heslo"}`},
		"no password at all on a real token": {real, `{"password":""}`},
		"the right password on an unknown token": {"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			`{"password":"spravne-heslo"}`},
	}
	var status int
	var body string
	first := true
	for name, tc := range cases {
		rr := pagesPublicUnlock(t, h, tc.token, "", tc.body)
		if first {
			status, body, first = rr.Code, rr.Body.String(), false
			if status != http.StatusUnauthorized {
				t.Fatalf("a failure returned %d, want 401", status)
			}
			continue
		}
		if rr.Code != status {
			t.Errorf("%s returned %d; another failure returned %d — the two are distinguishable", name, rr.Code, status)
		}
		if rr.Body.String() != body {
			t.Errorf("%s answered %q; another failure answered %q — §7.3.3 requires one refusal, not two",
				name, rr.Body.String(), body)
		}
	}
	if !strings.Contains(body, pagePublicPasswordRefusal) {
		t.Errorf("the refusal is not the single shared sentence: %s", body)
	}
}

// TestPublicUnlock_IsRateLimitedPerToken covers "a wrong password must be rate
// limited per token".
func TestPublicUnlock_IsRateLimitedPerToken(t *testing.T) {
	h, _, _, wsID, userID := newPagesPublicFixture(t, "brute")
	first := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "brute", `{"password":"spravne-heslo"}`))
	second := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "brute", `{"password":"jine-heslo"}`))
	h.passwords = newPagePublicLimiter(func() int { return 3 }, 3)

	for i := 1; i <= 3; i++ {
		if rr := pagesPublicUnlock(t, h, first, "", `{"password":"x"}`); rr.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d returned %d, want 401", i, rr.Code)
		}
	}
	if rr := pagesPublicUnlock(t, h, first, "", `{"password":"x"}`); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("the fourth guess returned %d, want 429", rr.Code)
	}
	// The correct password is refused too while the bucket is empty: a limiter
	// that steps aside for the right answer is a limiter that tells an attacker
	// when they have found it.
	if rr := pagesPublicUnlock(t, h, first, "", `{"password":"spravne-heslo"}`); rr.Code != http.StatusTooManyRequests {
		t.Errorf("the correct password bypassed the exhausted bucket (%d)", rr.Code)
	}
	// Per token: another link's budget is its own.
	if rr := pagesPublicUnlock(t, h, second, "", `{"password":"jine-heslo"}`); rr.Code != http.StatusOK {
		t.Errorf("guessing at one link spent another link's budget (%d)", rr.Code)
	}

	if pagePublicPasswordAttemptsPerHour > 60 {
		t.Errorf("the password bucket allows %d guesses an hour, which is a brute-force allowance rather than a limit",
			pagePublicPasswordAttemptsPerHour)
	}
}

// TestPublicUnlock_GuessingAnUnknownTokenIsAlsoLimited closes the gap a
// per-row-id limiter would leave: the caller who does not have a real token is
// exactly the caller worth limiting.
func TestPublicUnlock_GuessingAnUnknownTokenIsAlsoLimited(t *testing.T) {
	h, _, _, wsID, userID := newPagesPublicFixture(t, "hadani")
	pagesPublish(t, h, wsID, userID, "hadani", `{"password":"spravne-heslo"}`)
	h.passwords = newPagePublicLimiter(func() int { return 2 }, 2)

	const guess = "ccccccccccccccccccccccccccccccccccccccccccc"
	for i := 1; i <= 2; i++ {
		if rr := pagesPublicUnlock(t, h, guess, "", `{"password":"x"}`); rr.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d returned %d, want 401", i, rr.Code)
		}
	}
	if rr := pagesPublicUnlock(t, h, guess, "", `{"password":"x"}`); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("an unknown token was not rate limited: %d", rr.Code)
	}
}

// TestPublish_RefusesAWeakOrOversizedPassword — bcrypt truncates past 72 bytes,
// so accepting a longer password would store one the holder does not have.
func TestPublish_RefusesAWeakOrOversizedPassword(t *testing.T) {
	h, _, _, wsID, userID := newPagesPublicFixture(t, "sila")
	for _, tc := range []struct{ name, body string }{
		{"too short", `{"password":"kratke"}`},
		{"past bcrypt's truncation point", `{"password":"` + strings.Repeat("a", 73) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rr := pagesPublishRaw(t, h, wsID, userID, "sila", tc.body); rr.Code != http.StatusBadRequest {
				t.Fatalf("%s: %d %s, want 400", tc.name, rr.Code, rr.Body.String())
			}
		})
	}
}

// ── The surface itself ─────────────────────────────────────────────────────

// TestPublicView_ReadsNoWorkspaceContext is §7.3.1 as an assertion: "a separate
// URL space that shares no session, no cookie and no workspace context with the
// app". The request carries none of them and the page still renders — which is
// only possible if the handler never looks.
func TestPublicView_ReadsNoWorkspaceContext(t *testing.T) {
	h, _, _, wsID, userID := newPagesPublicFixture(t, "bezkontextu")
	token := pagesTokenOf(t, pagesPublish(t, h, wsID, userID, "bezkontextu", ""))

	req := httptest.NewRequest("GET", "/api/v1/public/pages/"+token, nil)
	req.SetPathValue("token", token)
	rr := httptest.NewRecorder()
	h.View(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("the public view needed a session, a workspace or a role: %d %s", rr.Code, rr.Body.String())
	}
	if UserFromContext(req.Context()) != nil || WorkspaceIDFromContext(req.Context()) != "" {
		t.Fatal("the fixture leaked a user or a workspace into the request; the test proves nothing")
	}
}

// TestPublish_RefusesANonOwner keeps the publish verb where §7.3 leaves it: the
// widest reach the product has is not something any workspace member may hand
// out.
func TestPublish_RefusesANonOwner(t *testing.T) {
	h, _, _, wsID, _ := newPagesPublicFixture(t, "cizi")
	const other = "user-other"
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'other@example.com', 'Other')`, other); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm-other', ?, ?, 'MEMBER')`,
		wsID, other); err != nil {
		t.Fatalf("add member: %v", err)
	}
	rr := pagesPublishAs(t, h, wsID, other, "MEMBER", "cizi", "")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("a non-owner MEMBER published the page: %d %s", rr.Code, rr.Body.String())
	}
}
