package api

// Pages — what the `read` verb decides (docs/prd/pages.md §7.1 rules 2–3,
// §7.1b's verb table).
//
// `read` was accepted, stored, journalled and listed for a while without any
// authorization decision consulting it. These tests are the other half of
// closing that: they drive the two endpoints whose answer a grant now changes
// (PageHandler.List and PageHandler.Get) and pin the three things that were
// easy to get wrong while making it decide something.
//
//  1. A grant reaches the PAGE and never a crew's DATA (§7.1 rule 3). The
//     grantee of a page whose panels all belong to crews they are not in gets
//     the page and a grid of sealed placeholders. That is the correct outcome.
//  2. The reach narrows with the ISSUER at use time (§7.1b), through the same
//     single reader that narrows `produce` and `write`.
//  3. `read` is implied by `produce` and by `write` — a principal who may
//     rewrite a page can open it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── Local helpers ──────────────────────────────────────────────────────────

// pagesGetStatus is pagesGet without the "must be 200" assumption: these tests
// are about the refusal as much as about the success.
func pagesGetStatus(t *testing.T, h *PageHandler, wsID, userID, role, slug string) *httptest.ResponseRecorder {
	t.Helper()
	req := pagesRequest(t, "GET", "/api/v1/pages/"+slug, wsID, userID, role, "")
	req.SetPathValue("slug", slug)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	return rr
}

// pagesListSlugs returns the slugs one caller is shown by the index.
func pagesListSlugs(t *testing.T, h *PageHandler, wsID, userID, role string) []string {
	t.Helper()
	req := pagesRequest(t, "GET", "/api/v1/pages", wsID, userID, role, "")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list pages: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("list is not a JSON array: %v (%s)", err, rr.Body.String())
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		slug, _ := row["slug"].(string)
		out = append(out, slug)
	}
	return out
}

func pagesSlugListed(slugs []string, want string) bool {
	for _, s := range slugs {
		if s == want {
			return true
		}
	}
	return false
}

// ── 1. The boundary: the page, never the crew's data ───────────────────────

// TestPageGrants_ReadGrantReachesThePageAndNotTheData is the test the whole
// verb turns on, and it asserts BOTH halves in one caller's lifetime:
//
//	before the grant — the page does not exist as far as this member is
//	  concerned, because they own nothing on it and belong to neither crew;
//	after the grant  — the page opens, keeps its shape, and every panel on it
//	  is still sealed, because a grant widens access to the page and never to a
//	  crew's data (§7.1 rule 3).
//
// The second half is the one somebody will be tempted to "fix": a page of
// sealed placeholders looks like a broken grant. It is not. The grantee was
// given the board, not the numbers on it, and the sentence in §7.1b that
// describes a viewer "seeing the sealed placeholder for a panel it cannot
// read" is exactly this state.
func TestPageGrants_ReadGrantReachesThePageAndNotTheData(t *testing.T) {
	h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "outsider", "outsider@example.com", "MEMBER")

	// Real data on both panels, so there is something to leak.
	if rr := pagesPush(t, h, wsID, ownerID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("seed push sluzby: %d %s", rr.Code, rr.Body.String())
	}
	if rr := pagesPush(t, h, wsID, ownerID, "OWNER", "fleet-201", "zatizeni", pagesMetricPayload); rr.Code != http.StatusOK {
		t.Fatalf("seed push zatizeni: %d %s", rr.Code, rr.Body.String())
	}

	if rr := pagesGetStatus(t, h, wsID, "outsider", "MEMBER", "fleet-201"); rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d before any grant, want 404 — a workspace member who owns nothing on the "+
			"page and belongs to none of its panels' crews has no reach to it, and if they did the "+
			"`read` verb would have nothing left to widen; body: %s", rr.Code, rr.Body.String())
	}

	pagesGrant(t, h, wsID, ownerID, "fleet-201",
		`{"subject_type":"user","subject":"outsider@example.com","level":"read"}`)

	rr := pagesGetStatus(t, h, wsID, "outsider", "MEMBER", "fleet-201")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d after a read grant, want 200 — the verb decides page reach; body: %s",
			rr.Code, rr.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("get response is not JSON: %v", err)
	}

	panels, _ := doc["panels"].([]any)
	if len(panels) != 2 {
		t.Fatalf("the page has %d panels for a read grantee, want both slots — the grid must have the "+
			"same shape for everyone (§2.3): %s", len(panels), mustPagesJSON(t, doc))
	}
	for _, panelID := range []string{"sluzby", "zatizeni"} {
		panel := pagesPanel(t, doc, panelID)
		if sealed, _ := panel["sealed"].(bool); !sealed {
			t.Fatalf("panel %q was unsealed by a `read` grant: a grant widens access to the PAGE and "+
				"never to a crew's data (§7.1 rule 3): %s", panelID, mustPagesJSON(t, panel))
		}
		for _, leaked := range []string{"data", "schema", "producer", "sla_seconds", "state", "provenance", "owner"} {
			if _, present := panel[leaked]; present {
				t.Errorf("the sealed placeholder for %q carries %q: %s", panelID, leaked, mustPagesJSON(t, panel))
			}
		}
	}
	raw := mustPagesJSON(t, doc)
	if strings.Contains(raw, "200 OK") || strings.Contains(raw, "42") {
		t.Error("a payload from a crew the read grantee is not in reached them through the page")
	}
}

// TestPageGrants_ReadGrantOnACrewReachesItsMembers — the same widening through
// the other human subject type. It also pins the half that must NOT happen:
// the crew named in the grant is `engine`, so its members reach the page, and
// the `lookout` panel stays sealed to them because their own membership is
// still the only thing that opens a panel.
func TestPageGrants_ReadGrantOnACrewReachesItsMembers(t *testing.T) {
	h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "sailor", "sailor@example.com", "MEMBER")
	if _, err := h.db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-galley', ?, 'Galley', 'galley')`, wsID); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO crew_members (id, crew_id, user_id) VALUES ('cm-sailor', 'crew-galley', 'sailor')`); err != nil {
		t.Fatalf("add sailor to galley: %v", err)
	}

	if rr := pagesGetStatus(t, h, wsID, "sailor", "MEMBER", "fleet-201"); rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d before the grant, want 404: %s", rr.Code, rr.Body.String())
	}

	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"crew","subject":"galley","level":"read"}`)

	rr := pagesGetStatus(t, h, wsID, "sailor", "MEMBER", "fleet-201")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a read grant to a crew reaches that crew's members; body: %s",
			rr.Code, rr.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("get response is not JSON: %v", err)
	}
	for _, panelID := range []string{"sluzby", "zatizeni"} {
		if sealed, _ := pagesPanel(t, doc, panelID)["sealed"].(bool); !sealed {
			t.Errorf("panel %q is unsealed for a member of crew/galley — a grant to one crew must not "+
				"hand it another crew's data (§7.1 rule 3)", panelID)
		}
	}
}

// TestPagesGet_UnreachablePageIs404AndNotAnOracle: the refusal for a page
// outside the caller's reach is byte-identical to the refusal for a slug that
// was never authored. Anything else turns GET into an enumeration tool for the
// workspace's page names.
func TestPagesGet_UnreachablePageIs404AndNotAnOracle(t *testing.T) {
	h, _, wsID, _, _ := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "outsider", "outsider@example.com", "MEMBER")

	real := pagesGetStatus(t, h, wsID, "outsider", "MEMBER", "fleet-201")
	invented := pagesGetStatus(t, h, wsID, "outsider", "MEMBER", "no-such-page")

	if real.Code != http.StatusNotFound {
		t.Fatalf("an unreachable page answered %d, want 404 — 403 would confirm it exists: %s",
			real.Code, real.Body.String())
	}
	if invented.Code != real.Code {
		t.Fatalf("an invented slug answered %d and a real-but-unreachable one %d", invented.Code, real.Code)
	}
	if a, b := strings.TrimSpace(real.Body.String()), strings.TrimSpace(invented.Body.String()); a == b {
		return
	}
	// The bodies name their own slug, so they cannot be identical strings. What
	// must be identical is the SHAPE: neither may say more than "not found".
	for _, body := range []string{real.Body.String(), invented.Body.String()} {
		if !strings.Contains(strings.ToLower(body), "not found") {
			t.Errorf("404 body %q does not read as a plain not-found", body)
		}
		for _, tell := range []string{"grant", "permission", "forbidden", "crew"} {
			if strings.Contains(strings.ToLower(body), tell) {
				t.Errorf("the 404 body leaks why (%q): %s", tell, body)
			}
		}
	}
}

// ── 2. The index ───────────────────────────────────────────────────────────

// TestPagesList_IncludesPagesReachableOnlyByGrant — §7.1 rule 3 calls the ACL
// "the 'who may look at this page' control", and an index that omitted a page
// the caller may open would make a grant something you have to be told about
// out of band before it is any use.
//
// The negative arm matters as much: before the grant the row is ABSENT, not
// present-and-locked. A locked row would disclose the page's name, its owner
// and its health to somebody with no reach to it — the same disclosure §11b
// decision 14 refuses one level down.
func TestPagesList_IncludesPagesReachableOnlyByGrant(t *testing.T) {
	h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "outsider", "outsider@example.com", "MEMBER")

	if slugs := pagesListSlugs(t, h, wsID, "outsider", "MEMBER"); pagesSlugListed(slugs, "fleet-201") {
		t.Fatalf("the index listed a page the caller cannot open: %v", slugs)
	}

	pagesGrant(t, h, wsID, ownerID, "fleet-201",
		`{"subject_type":"user","subject":"outsider@example.com","level":"read"}`)

	slugs := pagesListSlugs(t, h, wsID, "outsider", "MEMBER")
	if !pagesSlugListed(slugs, "fleet-201") {
		t.Fatalf("a page reachable only by grant is missing from the index: %v", slugs)
	}
}

// TestPagesList_RollupOnAGrantedPageCountsNothingSealed: the row a grantee is
// shown must not report the health of panels they may not read. panel_count is
// the grid's shape and is honest; every state count is zero because every panel
// is sealed to them.
func TestPagesList_RollupOnAGrantedPageCountsNothingSealed(t *testing.T) {
	h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "outsider", "outsider@example.com", "MEMBER")
	if rr := pagesPush(t, h, wsID, ownerID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("seed push: %d %s", rr.Code, rr.Body.String())
	}
	pagesGrant(t, h, wsID, ownerID, "fleet-201",
		`{"subject_type":"user","subject":"outsider@example.com","level":"read"}`)

	req := pagesRequest(t, "GET", "/api/v1/pages", wsID, "outsider", "MEMBER", "")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("list is not JSON: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the one granted page: %s", len(rows), rr.Body.String())
	}
	if got, _ := rows[0]["panel_count"].(float64); got != 2 {
		t.Errorf("panel_count = %v, want 2 — the grid's shape is the same for everyone (§2.3)", rows[0]["panel_count"])
	}
	states, _ := rows[0]["panel_states"].(map[string]any)
	for state, count := range states {
		if n, _ := count.(float64); n != 0 {
			t.Errorf("panel_states.%s = %v for a viewer every panel is sealed to; a state count is the "+
				"health of data they may not read", state, count)
		}
	}
}

// ── 3. Use-time narrowing, through the one reader ──────────────────────────

// TestPageGrants_ReadReachNarrowsWithItsIssuer applies §7.1b's invariant to the
// verb that now decides reach: the page opens while the issuer could still
// issue the grant, and stops opening the instant they could not — at the next
// request, not at the next sweep. The row survives; this is a narrowing.
//
// It is deliberately asserted through the SURFACE rather than through
// livePageGrants, because the property being tested is that reachability went
// through that single reader at all.
func TestPageGrants_ReadReachNarrowsWithItsIssuer(t *testing.T) {
	h, _, wsID, _, pageID := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "issuer", "issuer@example.com", "ADMIN")
	pagesSeedUser(t, h, wsID, "grantee", "grantee@example.com", "MEMBER")

	rr := pagesGrantCall(t, h, "PUT", "/api/v1/pages/fleet-201/grants", wsID, "issuer", "ADMIN", "fleet-201",
		`{"subject_type":"user","subject":"grantee@example.com","level":"read"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body.String())
	}
	if got := pagesGetStatus(t, h, wsID, "grantee", "MEMBER", "fleet-201"); got.Code != http.StatusOK {
		t.Fatalf("the read grant did not open the page before the issuer was demoted: %d %s",
			got.Code, got.Body.String())
	}
	if slugs := pagesListSlugs(t, h, wsID, "grantee", "MEMBER"); !pagesSlugListed(slugs, "fleet-201") {
		t.Fatalf("the index did not list the granted page: %v", slugs)
	}

	if _, err := h.db.Exec(`UPDATE workspace_members SET role = 'MEMBER' WHERE workspace_id = ? AND user_id = 'issuer'`, wsID); err != nil {
		t.Fatalf("demote issuer: %v", err)
	}

	if got := pagesGetStatus(t, h, wsID, "grantee", "MEMBER", "fleet-201"); got.Code != http.StatusNotFound {
		t.Errorf("status = %d after the issuer lost the standing to issue it, want 404 — a demoted "+
			"issuer cannot keep delegating reach they no longer hold (§7.1b); body: %s",
			got.Code, got.Body.String())
	}
	if slugs := pagesListSlugs(t, h, wsID, "grantee", "MEMBER"); pagesSlugListed(slugs, "fleet-201") {
		t.Errorf("the index still lists a page whose only grant went inert: %v — List and Get must "+
			"reach the same verdict through the same reader", slugs)
	}
	if n := pagesGrantRows(t, h, pageID); n != 1 {
		t.Errorf("grant rows = %d, want the row still present but inert", n)
	}
}

// TestPageGrants_ReadGrantIsNotReportedInert — the listing's honesty, which
// used to say the opposite. `read` reported an inert_reason unconditionally
// while the verb decided nothing; that sentence had to stop being true at the
// same moment the behaviour changed, or the ACL would tell an administrator
// their working grant does nothing.
func TestPageGrants_ReadGrantIsNotReportedInert(t *testing.T) {
	h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "outsider", "outsider@example.com", "MEMBER")
	pagesGrant(t, h, wsID, ownerID, "fleet-201",
		`{"subject_type":"user","subject":"outsider@example.com","level":"read"}`)

	grants := pagesGrantList(t, h, wsID, ownerID, "OWNER", "fleet-201")
	if len(grants) != 1 {
		t.Fatalf("grants = %+v, want the one read grant", grants)
	}
	if live, _ := grants[0]["live"].(bool); !live {
		t.Errorf("a read grant from a live issuer is reported not live: %+v", grants[0])
	}
	if reason, _ := grants[0]["inert_reason"].(string); strings.TrimSpace(reason) != "" {
		t.Errorf("inert_reason = %q on a read grant that opens the page; inertness is now only ever "+
			"the issuer's lost standing", reason)
	}
}

// ── 4. The verbs nest ──────────────────────────────────────────────────────

// TestPageGrants_ProduceAndWriteImplyRead. A `write` grantee may add, remove
// and re-arrange the page's panels; a `produce` grantee pushes into panels by
// name. Refusing either of them the page is two paths disagreeing about the
// same principal, not a narrower permission.
func TestPageGrants_ProduceAndWriteImplyRead(t *testing.T) {
	for _, tc := range []struct{ name, level, body string }{
		{"write", pageGrantWrite, `{"subject_type":"user","subject":"grantee@example.com","level":"write"}`},
		{"produce", pageGrantProduce, `{"subject_type":"user","subject":"grantee@example.com","level":"produce","panels":["sluzby"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
			pagesSeedUser(t, h, wsID, "grantee", "grantee@example.com", "MEMBER")
			pagesGrant(t, h, wsID, ownerID, "fleet-201", tc.body)

			if rr := pagesGetStatus(t, h, wsID, "grantee", "MEMBER", "fleet-201"); rr.Code != http.StatusOK {
				t.Fatalf("a %s grantee got %d opening the page they hold %s on; body: %s",
					tc.level, rr.Code, tc.level, rr.Body.String())
			}
			if slugs := pagesListSlugs(t, h, wsID, "grantee", "MEMBER"); !pagesSlugListed(slugs, "fleet-201") {
				t.Errorf("a %s grantee cannot find the page in the index: %v", tc.level, slugs)
			}
		})
	}
}

// ── 5. What reaches a page WITHOUT any grant ───────────────────────────────

// TestPagesGet_CrewMemberReachesThePageThroughItsPanel pins the arm that keeps
// the model usable: nobody needs a grant to see a page their own crew has a
// panel on. The member of crew/engine opens the page, reads `zatizeni`, and is
// still sealed out of crew/lookout's `sluzby` — one caller, both verdicts,
// which is what stops "can open the page" and "can read the panel" from being
// confused for each other.
func TestPagesGet_CrewMemberReachesThePageThroughItsPanel(t *testing.T) {
	h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "stoker", "stoker@example.com", "MEMBER")
	if _, err := h.db.Exec(`INSERT INTO crew_members (id, crew_id, user_id) VALUES ('cm-stoker', 'crew-engine', 'stoker')`); err != nil {
		t.Fatalf("add stoker to engine: %v", err)
	}
	if rr := pagesPush(t, h, wsID, ownerID, "OWNER", "fleet-201", "zatizeni", pagesMetricPayload); rr.Code != http.StatusOK {
		t.Fatalf("seed push: %d %s", rr.Code, rr.Body.String())
	}

	rr := pagesGetStatus(t, h, wsID, "stoker", "MEMBER", "fleet-201")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d — a crew that owns a panel on the page reaches the page, no grant "+
			"required; body: %s", rr.Code, rr.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("get response is not JSON: %v", err)
	}
	if sealed, _ := pagesPanel(t, doc, "zatizeni")["sealed"].(bool); sealed {
		t.Error("crew/engine's own panel is sealed to a member of crew/engine")
	}
	if sealed, _ := pagesPanel(t, doc, "sluzby")["sealed"].(bool); !sealed {
		t.Error("reaching the page unsealed crew/lookout's panel — reach and readability are separate")
	}
	if slugs := pagesListSlugs(t, h, wsID, "stoker", "MEMBER"); !pagesSlugListed(slugs, "fleet-201") {
		t.Errorf("the index hid a page the caller's crew owns a panel on: %v", slugs)
	}
}

// TestPagesGet_OwnerAndAdminReachTheirPagesWithoutAGrant is the floor. A
// user-owned page with no panel the owner's crews cover must still open for its
// owner, and a workspace ADMIN must reach everything — effectiveRole already
// takes the max of workspace and crew role, so denying ADMIN here would only
// make two paths disagree.
func TestPagesGet_OwnerAndAdminReachTheirPagesWithoutAGrant(t *testing.T) {
	h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "boss", "boss@example.com", "ADMIN")

	if rr := pagesGetStatus(t, h, wsID, ownerID, "OWNER", "fleet-201"); rr.Code != http.StatusOK {
		t.Errorf("the page's own owner got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := pagesGetStatus(t, h, wsID, "boss", "ADMIN", "fleet-201"); rr.Code != http.StatusOK {
		t.Errorf("a workspace ADMIN got %d: %s", rr.Code, rr.Body.String())
	}
	if slugs := pagesListSlugs(t, h, wsID, "boss", "ADMIN"); !pagesSlugListed(slugs, "fleet-201") {
		t.Errorf("the index hid a page from a workspace ADMIN: %v", slugs)
	}
}
