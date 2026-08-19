package api

// Pages — export/import (docs/prd/pages.md §10b.2).
//
// §10b.2 fixes exactly what "marketplace readiness in 1.0" means, and it is
// three testable claims. This file is those three, plus the two rules that
// only show up when a bundle crosses a workspace boundary:
//
//	1. the bundle carries no workspace ids;
//	2. every external reference is declared, so the importer can see what it
//	   must bind BEFORE installing;
//	3. import binds everything or refuses, naming what it could not resolve;
//	4. a bundle cannot publish a panel in the receiving workspace;
//	5. a bind that matches nothing in the bundle is a typo, not a no-op.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── Fixture ────────────────────────────────────────────────────────────────

const (
	// The receiving workspace. Its crew and routine are deliberately named
	// nothing like the source's: a bundle that happened to import because both
	// workspaces use the word "lookout" would prove nothing about binding.
	pagesImportWS    = "ws-import-target"
	pagesImportCrew  = "finance"
	pagesImportRtn   = "mesicni-uzaverka"
	pagesSourceRtn   = "nocni-uzaverka"
	pagesXferSlug    = "weekly-close"
	pagesXferPanelA  = "sluzby"
	pagesXferPanelB  = "vysledky"
	pagesXferProdual = "script/watch-services.sh"
)

// pagesTransferSpec is a two-panel page: one script-produced panel and one
// routine-produced one. The routine is what makes the bundle interesting —
// it is the reference that cannot travel.
func pagesTransferSpec(slug string) string {
	return `{
		"slug": "` + slug + `",
		"name": "Tydenni uzaverka",
		"description": "co se zavrelo",
		"panels": [
			{"id": "` + pagesXferPanelA + `", "schema": "status.v1", "title": "Jede to?",
			 "owner": "crew/lookout", "producer": "` + pagesXferProdual + `",
			 "sla_seconds": 30, "span": 8},
			{"id": "` + pagesXferPanelB + `", "schema": "metric.v1", "title": "Vysledek",
			 "owner": "crew/lookout", "producer": "routine/` + pagesSourceRtn + `",
			 "sla_seconds": 3600, "span": 4}
		]
	}`
}

// pagesSeedRoutine inserts a routine the panel spec can name.
func pagesSeedRoutine(t *testing.T, h *PageHandler, wsID, slug string) {
	t.Helper()
	if _, err := h.db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		VALUES (?, ?, ?, ?, '{}', 'hash')`, "pl-"+wsID+"-"+slug, wsID, slug, slug); err != nil {
		t.Fatalf("insert routine %s: %v", slug, err)
	}
}

// pagesSeedTargetWorkspace builds the OTHER workspace a bundle is imported
// into: its own id, its own crew, its own routine, same user.
func pagesSeedTargetWorkspace(t *testing.T, h *PageHandler, userID string) string {
	t.Helper()
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Target', 'target')`, []any{pagesImportWS}},
		{`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-target', ?, ?, 'OWNER')`,
			[]any{pagesImportWS, userID}},
		{`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-finance', ?, 'Finance', ?)`,
			[]any{pagesImportWS, pagesImportCrew}},
	}
	for _, s := range stmts {
		if _, err := h.db.Exec(s.q, s.args...); err != nil {
			t.Fatalf("seed target workspace: %v", err)
		}
	}
	pagesSeedRoutine(t, h, pagesImportWS, pagesImportRtn)
	return pagesImportWS
}

// pagesExport runs the export handler and returns the decoded bundle plus the
// raw bytes — the raw bytes matter, because "carries no workspace ids" is a
// claim about the DOCUMENT and not about a struct.
func pagesExport(t *testing.T, h *PageHandler, wsID, userID, slug string) (map[string]any, string) {
	t.Helper()
	req := pagesRequest(t, "GET", "/api/v1/pages/"+slug+"/export", wsID, userID, "OWNER", "")
	req.SetPathValue("slug", slug)
	rr := httptest.NewRecorder()
	h.Export(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("export: status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	var bundle map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &bundle); err != nil {
		t.Fatalf("export body is not JSON: %v", err)
	}
	return bundle, rr.Body.String()
}

// pagesImport posts a bundle with the given slug and binds.
func pagesImport(t *testing.T, h *PageHandler, wsID, userID string, bundle map[string]any, slug string, bind map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{
		"format":     bundle["format"],
		"page":       bundle["page"],
		"references": bundle["references"],
	}
	if slug != "" {
		body["slug"] = slug
	}
	if len(bind) > 0 {
		body["bind"] = bind
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal import body: %v", err)
	}
	req := pagesRequest(t, "POST", "/api/v1/pages/import", wsID, userID, "OWNER", string(raw))
	rr := httptest.NewRecorder()
	h.Import(rr, req)
	return rr
}

func pagesCountIn(t *testing.T, h *PageHandler, query string, args ...any) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// ── 1. The bundle ──────────────────────────────────────────────────────────

// TestPageExport_CarriesNoWorkspaceIdsAndDeclaresEveryReference is §10b.2's
// first two claims, and the second is the one an importer depends on: "every
// external reference the page needs is DECLARED (so the importer can see what
// it must bind before installing)".
func TestPageExport_CarriesNoWorkspaceIdsAndDeclaresEveryReference(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesSeedRoutine(t, h, wsID, pagesSourceRtn)

	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesTransferSpec(pagesXferSlug))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	bundle, raw := pagesExport(t, h, wsID, userID, pagesXferSlug)

	if got, _ := bundle["format"].(string); got != pageBundleFormat {
		t.Errorf("format = %q, want %q", got, pageBundleFormat)
	}
	// Claim 1, tested against the bytes: neither the workspace id nor the
	// owner's user id may appear ANYWHERE in the document. A bundle carrying
	// either is not portable, it is a copy of one installation.
	for what, id := range map[string]string{"workspace id": wsID, "owner user id": userID} {
		if strings.Contains(raw, id) {
			t.Errorf("the bundle contains the %s (%q) — §10b.2: the export bundle carries no workspace ids:\n%s", what, id, raw)
		}
	}
	// The page was created by a user, so it exports with no owner at all: the
	// importer becomes the owner of what they installed.
	page, _ := bundle["page"].(map[string]any)
	if page == nil {
		t.Fatalf("bundle has no page: %s", raw)
	}
	if owner, present := page["owner"]; present && owner != "" {
		t.Errorf("a user-owned page exported owner = %v; a user id is exactly the kind of id that cannot travel", owner)
	}

	// Claim 2: every external reference declared, with the panels that need it.
	refs, _ := bundle["references"].([]any)
	if len(refs) == 0 {
		t.Fatalf("the bundle declares no references; an importer cannot see what to bind:\n%s", raw)
	}
	declared := map[string]map[string]any{}
	for _, r := range refs {
		obj, _ := r.(map[string]any)
		if obj == nil {
			continue
		}
		ref, _ := obj["ref"].(string)
		declared[ref] = obj
	}
	for ref, want := range map[string]struct {
		kind     string
		bindable bool
		usedBy   string
	}{
		"crew/lookout":              {"crew", true, pagesXferPanelA},
		"routine/" + pagesSourceRtn: {"routine", true, pagesXferPanelB},
		pagesXferProdual:            {"script", false, pagesXferPanelA},
	} {
		got, ok := declared[ref]
		if !ok {
			t.Errorf("reference %q is not declared; the importer would discover it only after installing", ref)
			continue
		}
		if kind, _ := got["kind"].(string); kind != want.kind {
			t.Errorf("reference %q kind = %q, want %q", ref, kind, want.kind)
		}
		if bindable, _ := got["bindable"].(bool); bindable != want.bindable {
			t.Errorf("reference %q bindable = %v, want %v — a script producer is a name, not a principal, "+
				"and there is nothing local to bind it to", ref, bindable, want.bindable)
		}
		usedBy, _ := got["used_by"].([]any)
		found := false
		for _, u := range usedBy {
			if s, _ := u.(string); s == want.usedBy {
				found = true
			}
		}
		if !found {
			t.Errorf("reference %q used_by = %v, want it to name panel %q", ref, usedBy, want.usedBy)
		}
	}

	// A panel that travels carries no `public`: publication is a property of
	// the install (§7.3.2 rule 2), not of the document.
	if strings.Contains(raw, `"public"`) {
		t.Errorf("the bundle carries a `public` field; a bundle that could publish panels "+
			"would make publishing a bulk action over panels nobody in the receiving workspace has looked at:\n%s", raw)
	}
}

// TestPageExport_NeedsTheWriteAuthority — an export carries the whole
// arrangement, including panels an ordinary reader receives sealed. Reader
// authority must therefore not be enough.
func TestPageExport_NeedsTheWriteAuthority(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesSeedRoutine(t, h, wsID, pagesSourceRtn)
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesTransferSpec(pagesXferSlug))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	// A workspace MEMBER who neither owns the page nor holds a write grant.
	if _, err := h.db.Exec(
		`INSERT INTO users (id, email, full_name) VALUES ('outsider', 'out@example.com', 'Out')`); err != nil {
		t.Fatalf("insert outsider: %v", err)
	}
	req = pagesRequest(t, "GET", "/api/v1/pages/"+pagesXferSlug+"/export", wsID, "outsider", "MEMBER", "")
	req.SetPathValue("slug", pagesXferSlug)
	rr = httptest.NewRecorder()
	h.Export(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("export by a plain member: status = %d, want 403 — an export carries panels that reader "+
			"would receive sealed, body: %s", rr.Code, rr.Body.String())
	}
}

// ── 2. The round trip ──────────────────────────────────────────────────────

// TestPageBundle_RoundTripsIntoADifferentWorkspace is the whole point: the
// same document is a page here and a template there. Nothing about the source
// workspace survives except what the bindings replaced it with.
func TestPageBundle_RoundTripsIntoADifferentWorkspace(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesSeedRoutine(t, h, wsID, pagesSourceRtn)
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesTransferSpec(pagesXferSlug))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	bundle, _ := pagesExport(t, h, wsID, userID, pagesXferSlug)

	target := pagesSeedTargetWorkspace(t, h, userID)
	rr = pagesImport(t, h, target, userID, bundle, "uzaverka", map[string]string{
		"crew/lookout":              "crew/" + pagesImportCrew,
		"routine/" + pagesSourceRtn: "routine/" + pagesImportRtn,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("import: status = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}

	// Read it back through the ordinary path in the RECEIVING workspace.
	req = pagesRequest(t, "GET", "/api/v1/pages/uzaverka", target, userID, "OWNER", "")
	req.SetPathValue("slug", "uzaverka")
	rr = httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get imported page: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var doc struct {
		Slug   string `json:"slug"`
		Name   string `json:"name"`
		Panels []struct {
			ID         string `json:"id"`
			Schema     string `json:"schema"`
			Title      string `json:"title"`
			Owner      string `json:"owner"`
			Producer   string `json:"producer"`
			SLASeconds int    `json:"sla_seconds"`
			Span       int    `json:"span"`
			State      string `json:"state"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode imported page: %v", err)
	}
	if doc.Slug != "uzaverka" {
		t.Errorf("slug = %q, want the --slug the importer chose", doc.Slug)
	}
	if doc.Name != "Tydenni uzaverka" {
		t.Errorf("name = %q, want the bundle's", doc.Name)
	}
	if len(doc.Panels) != 2 {
		t.Fatalf("imported %d panels, want 2: %s", len(doc.Panels), rr.Body.String())
	}
	byID := map[string]int{}
	for i, p := range doc.Panels {
		byID[p.ID] = i
	}
	a, b := doc.Panels[byID[pagesXferPanelA]], doc.Panels[byID[pagesXferPanelB]]

	// Everything that is not a reference survives byte for byte.
	if a.Schema != "status.v1" || a.SLASeconds != 30 || a.Span != 8 || a.Title != "Jede to?" {
		t.Errorf("panel %s did not round-trip: %+v", pagesXferPanelA, a)
	}
	if b.Schema != "metric.v1" || b.SLASeconds != 3600 || b.Span != 4 {
		t.Errorf("panel %s did not round-trip: %+v", pagesXferPanelB, b)
	}
	// Everything that IS a reference is the LOCAL one.
	if a.Owner != "crew/"+pagesImportCrew || b.Owner != "crew/"+pagesImportCrew {
		t.Errorf("owners = %q / %q, want the bound crew/%s", a.Owner, b.Owner, pagesImportCrew)
	}
	if b.Producer != "routine/"+pagesImportRtn {
		t.Errorf("producer = %q, want the bound routine/%s", b.Producer, pagesImportRtn)
	}
	// The script producer had no binding and is not bindable: it travels
	// unchanged, and is checked at push time instead.
	if a.Producer != pagesXferProdual {
		t.Errorf("script producer = %q, want it carried through unchanged", a.Producer)
	}
	// An imported page has no data. Nothing was resurrected, and no panel
	// pretends otherwise (§11b decision 8: the fourth state is the server's).
	for _, p := range doc.Panels {
		if p.State != "never_produced" {
			t.Errorf("panel %s imported in state %q, want never_produced", p.ID, p.State)
		}
	}

	// And the round trip closes: exporting from the receiving workspace
	// declares the LOCAL references, so the page is a template again.
	bundle2, raw2 := pagesExport(t, h, target, userID, "uzaverka")
	if !strings.Contains(raw2, "crew/"+pagesImportCrew) || !strings.Contains(raw2, "routine/"+pagesImportRtn) {
		t.Errorf("re-export does not declare the local references:\n%s", raw2)
	}
	if strings.Contains(raw2, "crew/lookout") || strings.Contains(raw2, pagesSourceRtn) {
		t.Errorf("re-export still names the SOURCE workspace's references:\n%s", raw2)
	}
	if refs, _ := bundle2["references"].([]any); len(refs) != 3 {
		t.Errorf("re-export declares %d references, want 3 (crew, routine, script)", len(refs))
	}
}

// ── 3. Refusal ─────────────────────────────────────────────────────────────

// TestPageImport_UnresolvableReferenceRefusesTheWholeImport is §10b.2's third
// claim and the failure mode it was written against: "importing a page whose
// producer routine does not exist locally must not create a page full of dead
// panels. Import either binds it, or refuses and says which reference it could
// not resolve."
//
// Two references are left unbound on purpose. The refusal must name BOTH —
// which is also what proves this is not the authoring gate (resolveReferences)
// wearing a different hat: that one answers with the first panel that fails,
// which for an importer means one round trip per broken reference.
func TestPageImport_UnresolvableReferenceRefusesTheWholeImport(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesSeedRoutine(t, h, wsID, pagesSourceRtn)
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesTransferSpec(pagesXferSlug))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	bundle, _ := pagesExport(t, h, wsID, userID, pagesXferSlug)

	target := pagesSeedTargetWorkspace(t, h, userID)
	// Neither crew/lookout nor the source routine exists in the target, and
	// the importer bound neither.
	rr = pagesImport(t, h, target, userID, bundle, "uzaverka", nil)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("import with unresolvable references: status = %d, want 422, body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	var refused struct {
		Error      string `json:"error"`
		Unresolved []struct {
			Ref    string   `json:"ref"`
			UsedBy []string `json:"used_by"`
			Reason string   `json:"reason"`
		} `json:"unresolved"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &refused); err != nil {
		t.Fatalf("refusal is not JSON: %v\n%s", err, body)
	}
	for _, want := range []string{"crew/lookout", "routine/" + pagesSourceRtn} {
		if !strings.Contains(refused.Error, want) {
			t.Errorf("the refusal message does not name %q — §10b.2 requires it to say which reference "+
				"it could not resolve:\n%s", want, refused.Error)
		}
	}
	if len(refused.Unresolved) != 2 {
		t.Errorf("the refusal lists %d unresolved references, want both — an import that reported one "+
			"per attempt would take a round trip per panel: %s", len(refused.Unresolved), body)
	}
	for _, u := range refused.Unresolved {
		if len(u.UsedBy) == 0 {
			t.Errorf("unresolved %q does not say which panels need it: %s", u.Ref, body)
		}
		if strings.TrimSpace(u.Reason) == "" {
			t.Errorf("unresolved %q carries no reason: %s", u.Ref, body)
		}
	}

	// Nothing was created. Not the page, not one panel of it — "a page full of
	// dead panels is the failure mode to design against".
	if n := pagesCountIn(t, h, `SELECT COUNT(*) FROM pages WHERE workspace_id = ?`, target); n != 0 {
		t.Errorf("%d pages exist in the target workspace after a refused import, want 0", n)
	}
	if n := pagesCountIn(t, h, `SELECT COUNT(*) FROM page_panels`); n != 2 {
		t.Errorf("page_panels holds %d rows, want only the source page's 2 — a refused import wrote panels", n)
	}
}

// TestPageImport_PartialBindingIsStillARefusal — binding one of the two
// references does not get you half a page.
func TestPageImport_PartialBindingIsStillARefusal(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesSeedRoutine(t, h, wsID, pagesSourceRtn)
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesTransferSpec(pagesXferSlug))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	bundle, _ := pagesExport(t, h, wsID, userID, pagesXferSlug)
	target := pagesSeedTargetWorkspace(t, h, userID)

	rr = pagesImport(t, h, target, userID, bundle, "uzaverka", map[string]string{
		"crew/lookout": "crew/" + pagesImportCrew,
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), pagesSourceRtn) {
		t.Errorf("the refusal does not name the routine that is still unbound: %s", rr.Body.String())
	}
	if n := pagesCountIn(t, h, `SELECT COUNT(*) FROM pages WHERE workspace_id = ?`, target); n != 0 {
		t.Errorf("%d pages created by a partially-bound import, want 0", n)
	}
}

// TestPageImport_ABindThatMatchesNothingIsATypo — a binding the bundle does
// not declare is refused rather than ignored. Ignoring it leaves the operator
// believing they rebound a reference that is still pointing somewhere else.
func TestPageImport_ABindThatMatchesNothingIsATypo(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesSeedRoutine(t, h, wsID, pagesSourceRtn)
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesTransferSpec(pagesXferSlug))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	bundle, _ := pagesExport(t, h, wsID, userID, pagesXferSlug)
	target := pagesSeedTargetWorkspace(t, h, userID)

	rr = pagesImport(t, h, target, userID, bundle, "uzaverka", map[string]string{
		"crew/lokout": "crew/" + pagesImportCrew, // one letter short
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a binding that names nothing, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "crew/lokout") {
		t.Errorf("the refusal does not quote the binding that matched nothing: %s", rr.Body.String())
	}

	// And a binding that changes what KIND of thing a panel names is refused
	// too: a binding replaces a reference, it does not reinterpret it.
	rr = pagesImport(t, h, target, userID, bundle, "uzaverka", map[string]string{
		"routine/" + pagesSourceRtn: "crew/" + pagesImportCrew,
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("rebinding a routine to a crew: status = %d, want 400, body: %s", rr.Code, rr.Body.String())
	}
}

// ── 4. What a bundle may not do ────────────────────────────────────────────

// TestPageImport_CannotPublishAPanel — §7.3.2 rule 2 makes publication
// default-deny, per panel, human-only. A bundle that could carry `public:
// true` would publish panels in the receiving workspace that nobody there has
// ever looked at.
func TestPageImport_CannotPublishAPanel(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	target := pagesSeedTargetWorkspace(t, h, userID)
	_ = wsID

	// A hand-made bundle that tries it on.
	bundle := map[string]any{
		"format": pageBundleFormat,
		"page": map[string]any{
			"name":  "Nase cisla",
			"slug":  "nase-cisla",
			"owner": "",
			"panels": []any{map[string]any{
				"id": "verejny", "schema": "status.v1", "owner": "crew/" + pagesImportCrew,
				"producer": pagesXferProdual, "sla_seconds": 60, "span": 12, "public": true,
			}},
		},
	}
	rr := pagesImport(t, h, target, userID, bundle, "nase-cisla", nil)
	if rr.Code != http.StatusCreated {
		t.Fatalf("import: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var specJSON string
	if err := h.db.QueryRow(
		`SELECT spec_json FROM pages WHERE workspace_id = ? AND slug = 'nase-cisla'`, target).Scan(&specJSON); err != nil {
		t.Fatalf("read imported spec: %v", err)
	}
	if strings.Contains(specJSON, `"public":true`) {
		t.Errorf("the imported spec publishes a panel the importer never looked at:\n%s", specJSON)
	}
}

// TestPageImport_RefusesAnUnknownBundleFormat — a bundle from a future build
// is refused with its format named, not silently half-read.
func TestPageImport_RefusesAnUnknownBundleFormat(t *testing.T) {
	h, _, _, _, userID := newPagesFixture(t)
	target := pagesSeedTargetWorkspace(t, h, userID)

	bundle := map[string]any{
		"format": "crewship-page-bundle/v9",
		"page":   map[string]any{"name": "x", "slug": "x", "panels": []any{}},
	}
	rr := pagesImport(t, h, target, userID, bundle, "x", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "crewship-page-bundle/v9") {
		t.Errorf("the refusal does not name the format it could not read: %s", rr.Body.String())
	}
}

// TestPageImport_SlugConflictIsA409 — importing twice under one slug is a
// conflict with a way out, not a 500.
func TestPageImport_SlugConflictIsA409(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesSeedRoutine(t, h, wsID, pagesSourceRtn)
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesTransferSpec(pagesXferSlug))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	bundle, _ := pagesExport(t, h, wsID, userID, pagesXferSlug)
	target := pagesSeedTargetWorkspace(t, h, userID)
	binds := map[string]string{
		"crew/lookout":              "crew/" + pagesImportCrew,
		"routine/" + pagesSourceRtn: "routine/" + pagesImportRtn,
	}
	if rr := pagesImport(t, h, target, userID, bundle, "uzaverka", binds); rr.Code != http.StatusCreated {
		t.Fatalf("first import: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	rr = pagesImport(t, h, target, userID, bundle, "uzaverka", binds)
	if rr.Code != http.StatusConflict {
		t.Fatalf("second import: status = %d, want 409, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "--slug") {
		t.Errorf("the conflict does not say how to get past it: %s", rr.Body.String())
	}
}

// TestPageImport_IsTheFirstVersion — an import is a save, so the imported page
// starts with a version a rollback can name (§10b.1).
func TestPageImport_IsTheFirstVersion(t *testing.T) {
	h, _, _, _, userID := newPagesFixture(t)
	target := pagesSeedTargetWorkspace(t, h, userID)

	bundle := map[string]any{
		"format": pageBundleFormat,
		"page": map[string]any{
			"name": "Nase cisla", "slug": "nase-cisla",
			"panels": []any{map[string]any{
				"id": "stav", "schema": "status.v1", "owner": "crew/" + pagesImportCrew,
				"producer": pagesXferProdual, "sla_seconds": 60,
			}},
		},
	}
	if rr := pagesImport(t, h, target, userID, bundle, "", nil); rr.Code != http.StatusCreated {
		t.Fatalf("import: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	n := pagesCountIn(t, h, `
		SELECT COUNT(*) FROM page_versions v
		JOIN pages p ON p.id = v.page_id
		WHERE p.workspace_id = ? AND p.slug = 'nase-cisla'`, target)
	if n != 1 {
		t.Errorf("the imported page has %d versions, want 1 — every save is a version", n)
	}
}
