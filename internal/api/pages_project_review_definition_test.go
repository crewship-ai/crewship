package api

// Pages — the two definitions on the review snapshot (docs/prd/pages.md §11).
//
// The review screen used to derive the "Definition changes" list from the Page
// DETAIL query and the candidate's document from `GET .../project`, while the
// publish fence carried `definition_digest` from the REVIEW query. Three
// endpoints, three cache entries, three moments: another author saving the
// live definition could move the digest while the rendered document was still
// the old one, so a person read a comparison against definition A and sent a
// request attesting to digest B. The server accepts that, because B is
// genuinely current — it has no way to know what the screen was showing.
//
// The snapshot now carries BOTH documents, read in one handler, filtered by
// one rule, at one instant. These tests hold that down. The one that matters
// most is the first: `mayEditSpec` is all the route proved, and it does not
// imply the caller may read every panel.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
)

// reviewDefinitionPanelIDs is the panel ids of one document on the snapshot,
// in order. A nil document fails the test: "no document" and "a document with
// no panels" are different facts and only one of them is comparable.
func reviewDefinitionPanelIDs(t *testing.T, label string, definition json.RawMessage) []string {
	t.Helper()
	if definition == nil {
		t.Fatalf("%s is null; the reviewer has nothing to compare with", label)
	}
	var doc struct {
		Spec struct {
			Panels []map[string]any `json:"panels"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(definition, &doc); err != nil {
		t.Fatalf("%s is not a Page document: %v — %s", label, err, definition)
	}
	out := make([]string, 0, len(doc.Spec.Panels))
	for _, panel := range doc.Spec.Panels {
		id, _ := panel["id"].(string)
		out = append(out, id)
	}
	return out
}

func reviewDefinitionPanel(t *testing.T, label string, definition json.RawMessage, id string) map[string]any {
	t.Helper()
	var doc struct {
		Spec struct {
			Panels []map[string]any `json:"panels"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(definition, &doc); err != nil {
		t.Fatalf("%s is not a Page document: %v", label, err)
	}
	for _, panel := range doc.Spec.Panels {
		if panel["id"] == id {
			return panel
		}
	}
	t.Fatalf("panel %q is missing from %s: %s", id, label, definition)
	return nil
}

// reviewLivePanelIDs is what the stored Page document declares, whoever is
// asking — the set the authorized view is a subset of.
func reviewLivePanelIDs(t *testing.T, h *PageHandler, slug string) []string {
	t.Helper()
	var spec string
	if err := h.db.QueryRow(`SELECT spec_json FROM pages WHERE slug=?`, slug).Scan(&spec); err != nil {
		t.Fatalf("read live definition: %v", err)
	}
	var doc pages.Document
	if err := json.Unmarshal([]byte(spec), &doc); err != nil {
		t.Fatalf("stored definition is not a Page document: %v", err)
	}
	out := make([]string, 0, len(doc.Spec.Panels))
	for _, panel := range doc.Spec.Panels {
		out = append(out, panel.ID)
	}
	return out
}

// reviewDetailSealedIDs is the verdict the PAGE DETAIL route reaches for the
// same caller: the panels it replaces with `pageSealedPanelWire`. The review
// snapshot must withhold exactly these, or there are two panel rules.
func reviewDetailSealedIDs(t *testing.T, h *PageHandler, ws, user, role, slug string) []string {
	t.Helper()
	doc := pagesGet(t, h, ws, user, role, slug)
	panels, _ := doc["panels"].([]any)
	out := []string{}
	for _, raw := range panels {
		panel, ok := raw.(map[string]any)
		if !ok || panel["sealed"] != true {
			continue
		}
		id, _ := panel["panel_id"].(string)
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func reviewMissingFrom(all, present []string) []string {
	have := map[string]bool{}
	for _, id := range present {
		have[id] = true
	}
	out := []string{}
	for _, id := range all {
		if !have[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// reviewCandidateDefinitionBody is a draft document over the fixture page: its
// two live panels, plus a THIRD that exists only in the candidate and belongs
// to crew/lookout. The third panel is what makes the candidate side of the
// comparison a separate authorization question from the live side.
const reviewCandidateDefinitionBody = `{
	"apiVersion": "crewship/v1",
	"kind": "Page",
	"metadata": {"name": "Flotila .201", "slug": "fleet-201"},
	"spec": {"panels": [
		{"id": "sluzby", "schema": "status.v1", "title": "Jede to?",
		 "owner": "crew/lookout", "producer": "script/watch-services.sh", "sla": "30s", "span": 8,
		 "actions": [{"id": "a1", "kind": "call", "label": "Run", "routine": "ops-secret"}]},
		{"id": "zatizeni", "schema": "metric.v1", "title": "Zatizeni",
		 "owner": "crew/engine", "producer": "script/load.sh", "sla": "60s", "span": 4,
		 "actions": [{"id": "a2", "kind": "call", "label": "Run", "routine": "ops-open"}]},
		{"id": "posadka", "schema": "status.v1", "title": "Kdo ma sluzbu",
		 "owner": "crew/lookout", "producer": "script/roster.sh", "sla": "90s", "span": 12}
	]}
}`

// reviewWithheldRoutineCreateBody is the LIVE page: the same two panels, each
// with the `call` action its crew authored. The create API takes the parsed
// spec with SLA as an integer (§11b.3).
//
// A routine is named by a panel, and a panel has an owning crew. A viewer who
// may not read the panel has no business being told which routine it calls,
// nor whether that routine's definition has moved.
const reviewWithheldRoutineCreateBody = `{
	"slug": "fleet-201",
	"name": "Flotila .201",
	"panels": [
		{"id": "sluzby", "schema": "status.v1", "title": "Jede to?",
		 "owner": "crew/lookout", "producer": "script/watch-services.sh", "sla_seconds": 30, "span": 8,
		 "actions": [{"id": "a1", "kind": "call", "label": "Run", "routine": "ops-secret"}]},
		{"id": "zatizeni", "schema": "metric.v1", "title": "Zatizeni",
		 "owner": "crew/engine", "producer": "script/load.sh", "sla_seconds": 60, "span": 4,
		 "actions": [{"id": "a2", "kind": "call", "label": "Run", "routine": "ops-open"}]}
	]
}`

// reviewSealedDefinitionFixture is the two-crew page from the grants suite
// with project storage and a draft, so the review route serves both
// documents, plus the three standings the panel rule has to tell apart:
//
//	owner    — workspace OWNER, sees every panel;
//	editor   — a `write` grantee in neither crew: may edit the spec, may read
//	           no panel on it;
//	engineer — a `write` grantee in crew/engine only: one panel each way.
func reviewSealedDefinitionFixture(t *testing.T) (*PageHandler, string, string) {
	t.Helper()
	h, _, ws, owner, _ := pagesGrantFixture(t, "")
	h.SetProjectStore(&pages.ProjectStore{Directory: storageDir(t)})
	pagesSeedUser(t, h, ws, "spec-editor", "spec-editor@example.com", "MEMBER")
	pagesSeedUser(t, h, ws, "engineer", "engineer@example.com", "MEMBER")
	if _, err := h.db.Exec(`INSERT INTO crew_members (id, crew_id, user_id, role) VALUES ('cm-engineer', 'crew-engine', 'engineer', 'MEMBER')`); err != nil {
		t.Fatalf("add engineer to crew/engine: %v", err)
	}
	for _, subject := range []string{"spec-editor@example.com", "engineer@example.com"} {
		pagesGrant(t, h, ws, owner, "fleet-201", `{"subject_type":"user","subject":"`+subject+`","level":"write"}`)
	}
	// One routine per crew, and each panel calls its own. `ops-secret` is the
	// one a reader outside crew/lookout must never learn about from this
	// endpoint; `ops-open` is the control that proves the filter is not just
	// dropping every routine.
	for _, q := range []string{
		`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-secret', ?, 'ops-secret', 'Secret', '{"steps":[]}', 'h1')`,
		`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-open', ?, 'ops-open', 'Open', '{"steps":[1]}', 'h2')`,
	} {
		if _, err := h.db.Exec(q, ws); err != nil {
			t.Fatal(err)
		}
	}
	req := pagesRequest(t, http.MethodPatch, "/api/v1/pages/fleet-201", ws, owner, "OWNER", reviewWithheldRoutineCreateBody)
	req.SetPathValue("slug", "fleet-201")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("attach the call actions to the live page: %d %s", rr.Code, rr.Body.String())
	}
	reviewSaveDefinition(t, h, ws, owner, "fleet-201", 0, projectTestSource(), reviewCandidateDefinitionBody)
	return h, ws, owner
}

// reviewProtectedStrings is everything about crew/lookout's panel that a
// reader outside that crew must not be handed — including the slug of the
// routine only that panel calls, and the association between the two.
var reviewProtectedStrings = []string{
	"sluzby", "posadka", "crew/lookout", "Jede to?", "Kdo ma sluzbu",
	"status.v1", "watch-services.sh", "roster.sh", "ops-secret",
}

// reviewAssertBodyWithholds scans the WHOLE response body, not the fields the
// withholding happens to have been applied to.
//
// The narrower assertion is what let a leak through: `baseline.definition` and
// `candidate.definition` were clean while `routines[]` in the same body named
// the routine only the withheld panel calls, with its digests and its state.
// An endpoint that withholds must not disclose elsewhere in the same envelope.
func reviewAssertBodyWithholds(t *testing.T, where, body string, protected []string) {
	t.Helper()
	for _, secret := range protected {
		if strings.Contains(body, secret) {
			t.Errorf("%s: the response discloses %q to a reader it is withholding that panel from — "+
				"the whole body, not just the two definition fields: %s", where, secret, body)
		}
	}
}

// TestPageProjectReviewDefinitionsWithholdPanelsTheViewerMayNotSee is the test
// these fields turn on.
//
// A `write` grant is authority over ARRANGEMENT, never over content (§7.1b
// rule 2), and a grant widens access to the PAGE and never to a crew's data
// (§7.1 rule 3). So a spec editor outside both crews may edit the document and
// may read none of its panels — and serving them the stored `spec_json`
// because the route already proved `mayEditSpec` would disclose every panel's
// schema, producer, SLA and actions to somebody entitled to none of them. The
// same is true of the candidate document, which is not a row in anything and
// therefore cannot be authorized by looking one up.
//
// `definition_digest` is unchanged by any of this: it is the digest of the
// FULL stored bytes, because that is what the publish fence compares.
func TestPageProjectReviewDefinitionsWithholdPanelsTheViewerMayNotSee(t *testing.T) {
	h, ws, owner := reviewSealedDefinitionFixture(t)
	digest := reviewLiveDigest(t, h, "fleet-201")

	t.Run("a spec editor in neither crew gets no panel in either document", func(t *testing.T) {
		w, snapshot := reviewCall(t, h, ws, "spec-editor", "MEMBER", "fleet-201")
		if w.Code != http.StatusOK {
			t.Fatalf("review: %d %s", w.Code, w.Body.String())
		}
		if !snapshot.Capabilities.MayEditSpec {
			t.Fatalf("the write grantee is not reported as a spec editor, so this test is not exercising the case it is about: %+v", snapshot.Capabilities)
		}
		if snapshot.Candidate == nil {
			t.Fatalf("the fixture has a draft, so there must be a candidate to authorize: %s", w.Body.String())
		}
		if ids := reviewDefinitionPanelIDs(t, "baseline.definition", snapshot.Baseline.Definition); len(ids) != 0 {
			t.Errorf("baseline.definition still carries %v for a viewer in neither owning crew", ids)
		}
		if ids := reviewDefinitionPanelIDs(t, "candidate.definition", snapshot.Candidate.Definition); len(ids) != 0 {
			t.Errorf("candidate.definition still carries %v for a viewer in neither owning crew", ids)
		}
		// Two live panels plus the candidate's third, counted once each.
		if snapshot.Baseline.ExcludedPanels != 3 {
			t.Errorf("excluded_panels = %d, want 3 — two live panels and the candidate's own, all of "+
				"crews this viewer is not in, and the screen has to be able to say its comparison is partial",
				snapshot.Baseline.ExcludedPanels)
		}
		// The leak, named field by field rather than by shape, and checked
		// against the WHOLE body: this viewer may read no panel on the page,
		// so nothing about either crew's panels may appear anywhere in it.
		reviewAssertBodyWithholds(t, "a spec editor entitled to no panel on this page", w.Body.String(),
			append(append([]string{}, reviewProtectedStrings...),
				"metric.v1", "load.sh", "Zatizeni", "crew/engine", "ops-open"))
		if snapshot.Baseline.DefinitionDigest != digest {
			t.Errorf("definition_digest = %s, want the digest of the FULL stored document (%s) — the fence "+
				"compares what the server stores, not what this viewer was shown",
				snapshot.Baseline.DefinitionDigest, digest)
		}
	})

	t.Run("a spec editor in one crew gets that panel and only that panel", func(t *testing.T) {
		w, snapshot := reviewCall(t, h, ws, "engineer", "MEMBER", "fleet-201")
		// Everything about crew/lookout's panels is out of the body, the
		// routine only they call included; crew/engine's own is still there.
		reviewAssertBodyWithholds(t, "a spec editor in crew/engine only", w.Body.String(), reviewProtectedStrings)
		if !strings.Contains(w.Body.String(), "ops-open") {
			t.Errorf("the routine crew/engine's own panel calls is missing, so the filter is dropping "+
				"routines rather than withholding them: %s", w.Body.String())
		}
		if snapshot.Candidate == nil {
			t.Fatal("no candidate to authorize")
		}
		if ids := reviewDefinitionPanelIDs(t, "baseline.definition", snapshot.Baseline.Definition); len(ids) != 1 || ids[0] != "zatizeni" {
			t.Errorf("baseline.definition panels = %v, want only crew/engine's own", ids)
		}
		if ids := reviewDefinitionPanelIDs(t, "candidate.definition", snapshot.Candidate.Definition); len(ids) != 1 || ids[0] != "zatizeni" {
			t.Errorf("candidate.definition panels = %v, want only crew/engine's own", ids)
		}
		if snapshot.Baseline.ExcludedPanels != 2 {
			t.Errorf("excluded_panels = %d, want 2 — crew/lookout's live panel and the candidate's new one",
				snapshot.Baseline.ExcludedPanels)
		}
		mine := reviewDefinitionPanel(t, "candidate.definition", snapshot.Candidate.Definition, "zatizeni")
		if mine["schema"] != "metric.v1" || mine["producer"] != "script/load.sh" {
			t.Errorf("crew/engine's own panel came back lossy: %v", mine)
		}
		if snapshot.Baseline.DefinitionDigest != digest {
			t.Errorf("definition_digest moved with the viewer: %s, want %s", snapshot.Baseline.DefinitionDigest, digest)
		}
	})

	t.Run("the owner gets the stored document byte for byte", func(t *testing.T) {
		_, snapshot := reviewCall(t, h, ws, owner, "OWNER", "fleet-201")
		if snapshot.Baseline.ExcludedPanels != 0 {
			t.Errorf("excluded_panels = %d for a viewer entitled to every panel, want 0", snapshot.Baseline.ExcludedPanels)
		}
		sum := sha256.Sum256(snapshot.Baseline.Definition)
		if hex.EncodeToString(sum[:]) != snapshot.Baseline.DefinitionDigest {
			t.Errorf("the digest of the document served (%s) is not definition_digest (%s) — with nothing "+
				"withheld the two are the same bytes, and a client can check that for itself",
				hex.EncodeToString(sum[:]), snapshot.Baseline.DefinitionDigest)
		}
		if ids := reviewDefinitionPanelIDs(t, "baseline.definition", snapshot.Baseline.Definition); len(ids) != 2 {
			t.Errorf("baseline.definition panels = %v, want both live panels", ids)
		}
		if snapshot.Candidate == nil {
			t.Fatal("no candidate")
		}
		if ids := reviewDefinitionPanelIDs(t, "candidate.definition", snapshot.Candidate.Definition); len(ids) != 3 {
			t.Errorf("candidate.definition panels = %v, want all three the draft declares", ids)
		}
		if panel := reviewDefinitionPanel(t, "candidate.definition", snapshot.Candidate.Definition, "posadka"); panel["producer"] != "script/roster.sh" {
			t.Errorf("the candidate's new panel came back lossy: %v", panel)
		}
	})

	// One rule, two routes. A snapshot that withheld a different set from the
	// Page detail route would mean the panel decision had been written twice.
	t.Run("the withheld set is the page detail route's sealed set", func(t *testing.T) {
		live := reviewLivePanelIDs(t, h, "fleet-201")
		for _, viewer := range []struct{ user, role string }{
			{"spec-editor", "MEMBER"}, {"engineer", "MEMBER"}, {owner, "OWNER"},
		} {
			_, snapshot := reviewCall(t, h, ws, viewer.user, viewer.role, "fleet-201")
			withheld := reviewMissingFrom(live, reviewDefinitionPanelIDs(t, "baseline.definition", snapshot.Baseline.Definition))
			sealed := reviewDetailSealedIDs(t, h, ws, viewer.user, viewer.role, "fleet-201")
			if strings.Join(withheld, ",") != strings.Join(sealed, ",") {
				t.Errorf("%s: the review withholds %v but the Page detail route seals %v — one panel rule, two answers",
					viewer.user, withheld, sealed)
			}
		}
	})
}

// TestPageProjectReviewCandidateOnlyPanelIsNeverAnAddition — the failure mode
// that made a count on the baseline alone insufficient.
//
// Withhold a panel from the live document and leave the candidate's copy of it
// whole and the comparison reports it as ADDED by this publication: a false
// claim about a panel that already exists, on the one screen that exists to
// stop false claims. The withheld set is therefore the union across both
// documents and is applied to both, so a panel the reader cannot see is
// absent from the comparison rather than appearing on one side of it.
func TestPageProjectReviewCandidateOnlyPanelIsNeverAnAddition(t *testing.T) {
	h, ws, _ := reviewSealedDefinitionFixture(t)
	_, snapshot := reviewCall(t, h, ws, "engineer", "MEMBER", "fleet-201")
	if snapshot.Candidate == nil {
		t.Fatal("no candidate")
	}
	for _, side := range []struct {
		label      string
		definition json.RawMessage
	}{
		{"baseline.definition", snapshot.Baseline.Definition},
		{"candidate.definition", snapshot.Candidate.Definition},
	} {
		for _, id := range reviewDefinitionPanelIDs(t, side.label, side.definition) {
			if id == "posadka" {
				t.Errorf("%s carries the candidate's crew/lookout panel, so the comparison will report it "+
					"as an addition to a reader who may not see it: %s", side.label, side.definition)
			}
		}
		if strings.Contains(string(side.definition), "posadka") {
			t.Errorf("%s mentions the withheld panel at all: %s", side.label, side.definition)
		}
	}
	if snapshot.Baseline.ExcludedPanels != 2 {
		t.Errorf("excluded_panels = %d, want 2 — the live crew/lookout panel and the candidate's new one, "+
			"each counted once", snapshot.Baseline.ExcludedPanels)
	}
}

// TestPageProjectReviewDefinitionAndDigestComeFromOneRead is the
// correspondence these fields exist for: whatever moves the digest moves the
// document with it, in the same response, because they are one read of one
// row. The old arrangement could move one and not the other, and that is
// exactly the window a person could consent inside.
func TestPageProjectReviewDefinitionAndDigestComeFromOneRead(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	_, before := reviewCall(t, h, ws, user, "OWNER", "health")
	if title := reviewDefinitionPanel(t, "baseline.definition", before.Baseline.Definition, "sluzby")["title"]; title != "Jede to?" {
		t.Fatalf("baseline.definition does not carry the live panel title: %v", title)
	}

	// Another author edits the live Page. Nothing about the review request
	// changes; the row underneath it does.
	body := `{
		"slug": "health",
		"name": "Flotila .201",
		"panels": [{
			"id": "sluzby", "schema": "status.v1", "title": "Bezi to?",
			"owner": "crew/lookout", "producer": "script/watch-services.sh",
			"sla_seconds": 30, "span": 8
		}]
	}`
	req := pagesRequest(t, http.MethodPatch, "/api/v1/pages/health", ws, user, "OWNER", body)
	req.SetPathValue("slug", "health")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update the live definition: %d %s", rr.Code, rr.Body.String())
	}

	_, after := reviewCall(t, h, ws, user, "OWNER", "health")
	if after.Baseline.DefinitionDigest == before.Baseline.DefinitionDigest {
		t.Fatalf("definition_digest did not move when the live definition did: %s", after.Baseline.DefinitionDigest)
	}
	if title := reviewDefinitionPanel(t, "baseline.definition", after.Baseline.Definition, "sluzby")["title"]; title != "Bezi to?" {
		t.Errorf("baseline.definition still shows the OLD title (%v) beside a NEW digest — the screen "+
			"would be comparing against a document the fence no longer attests to", title)
	}
	if after.Baseline.DefinitionDigest != reviewLiveDigest(t, h, "health") {
		t.Errorf("definition_digest = %s, want the digest of the stored row", after.Baseline.DefinitionDigest)
	}
	sum := sha256.Sum256(after.Baseline.Definition)
	if hex.EncodeToString(sum[:]) != after.Baseline.DefinitionDigest {
		t.Errorf("document and digest disagree after the edit: %s vs %s", hex.EncodeToString(sum[:]), after.Baseline.DefinitionDigest)
	}
}

// TestPageProjectReviewDefinitionOnAnInitialPublication — a Page that has
// never published an application has no prior SOURCE to compare against, and
// says so. It does have a live panel definition, and that is precisely what
// the first publication replaces, so the reviewer must still be shown it.
func TestPageProjectReviewDefinitionOnAnInitialPublication(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	if !snapshot.InitialPublication {
		t.Fatalf("this Page has published before, so it is not the case under test: %+v", snapshot.Baseline)
	}
	if snapshot.Baseline.SourceAvailable {
		t.Fatalf("an initial publication has no retained source: %+v", snapshot.Baseline)
	}
	if snapshot.Baseline.ExcludedPanels != 0 {
		t.Errorf("excluded_panels = %d for the page owner, want 0", snapshot.Baseline.ExcludedPanels)
	}
	if panel := reviewDefinitionPanel(t, "baseline.definition", snapshot.Baseline.Definition, "sluzby"); panel["schema"] != "status.v1" {
		t.Errorf("the live panel definition the first publication will replace is missing: %v", panel)
	}
	if snapshot.Candidate == nil {
		t.Fatal("the draft is the candidate for the first publication")
	}
	if panel := reviewDefinitionPanel(t, "candidate.definition", snapshot.Candidate.Definition, "sluzby"); panel["schema"] != "status.v1" {
		t.Errorf("the candidate document is missing from a first publication's review: %v", panel)
	}
	if snapshot.Baseline.DefinitionDigest != reviewLiveDigest(t, h, "health") {
		t.Errorf("definition_digest = %s, want the live digest", snapshot.Baseline.DefinitionDigest)
	}
}

// TestPageProjectReviewNoCandidateHasNoCandidateDefinition — a Page with no
// draft has no candidate object at all, so there is no document to authorize
// and nothing for a client to mistake for one. The baseline still renders:
// there is a live Page whether or not anything is proposing to replace it.
func TestPageProjectReviewNoCandidateHasNoCandidateDefinition(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	if snapshot.Candidate != nil {
		t.Fatalf("a Page with no draft reported a candidate: %+v", snapshot.Candidate)
	}
	if reviewBlockers(snapshot)[reviewBlockerNoCandidate] == "" {
		t.Errorf("no_candidate blocker missing: %+v", snapshot.Blockers)
	}
	if panel := reviewDefinitionPanel(t, "baseline.definition", snapshot.Baseline.Definition, "sluzby"); panel["schema"] != "status.v1" {
		t.Errorf("the live definition is missing when there is no candidate: %v", panel)
	}
	if snapshot.Baseline.ExcludedPanels != 0 {
		t.Errorf("excluded_panels = %d, want 0", snapshot.Baseline.ExcludedPanels)
	}
}

// TestPageProjectReviewRetainedModeCarriesBothDefinitions — §11's rule that
// `baseline` is identical in both modes, now that it carries a document too.
// Restoring a retained version replaces the same live definition, so it is
// fenced on the same digest and compared against the same document; the
// candidate's document is that version's ARCHIVED declaration, which is the
// one a rollback would make live.
func TestPageProjectReviewRetainedModeCarriesBothDefinitions(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	build := reviewBuildRevision(t, h, ws, user, 1)
	zero := int64(0)
	if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
		BuildID: build, ExpectedRevision: 1, ExpectedPublication: &zero, ReviewedCode: true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}

	_, draft := reviewCall(t, h, ws, user, "OWNER", "health")
	w, retained := reviewCallTarget(t, h, ws, user, "OWNER", "health", "/?publication=1")
	if w.Code != http.StatusOK {
		t.Fatalf("review ?publication=1: %d %s", w.Code, w.Body.String())
	}
	if retained.Baseline.DefinitionDigest != draft.Baseline.DefinitionDigest {
		t.Errorf("definition_digest differs between the modes: %s vs %s", retained.Baseline.DefinitionDigest, draft.Baseline.DefinitionDigest)
	}
	if retained.Baseline.ExcludedPanels != draft.Baseline.ExcludedPanels {
		t.Errorf("excluded_panels differs between the modes: %d vs %d", retained.Baseline.ExcludedPanels, draft.Baseline.ExcludedPanels)
	}
	if string(retained.Baseline.Definition) != string(draft.Baseline.Definition) {
		t.Errorf("baseline.definition differs between the modes:\n draft:    %s\n retained: %s",
			draft.Baseline.Definition, retained.Baseline.Definition)
	}
	if retained.Baseline.Definition == nil {
		t.Error("baseline.definition is null in ?publication=N mode; a rollback replaces the live definition too")
	}
	if retained.Candidate == nil {
		t.Fatalf("publication 1 is retained, so it is the candidate: %s", w.Body.String())
	}
	if panel := reviewDefinitionPanel(t, "candidate.definition", retained.Candidate.Definition, "sluzby"); panel["schema"] != "status.v1" {
		t.Errorf("the archived declaration a rollback would restore is missing: %v", panel)
	}
}

// TestPageProjectReviewDefinitionsCarryTheAuthoredSLA — the smaller finding in
// the same counter-review, closed by the same change rather than by a
// client-side workaround.
//
// `sla` is an authored duration string on PanelSpec (`internal/pages/spec.go`),
// but the Page DETAIL wire reports it as the integer `sla_seconds`. A panel
// declaring "90.5s" therefore came back as 90 on the live side and "90.5s" on
// the candidate's, and the review reported an SLA change nobody had made. Now
// that both sides of the comparison are documents read here, the lossy integer
// is not on the path at all: the same authored string is on both.
func TestPageProjectReviewDefinitionsCarryTheAuthoredSLA(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	fractional := `{
		"apiVersion": "crewship/v1",
		"kind": "Page",
		"metadata": {"name": "Flotila .201", "slug": "health"},
		"spec": {"panels": [{"id": "sluzby", "schema": "status.v1", "title": "Jede to?",
		 "owner": "crew/lookout", "producer": "script/watch-services.sh", "sla": "90.5s", "span": 8}]}
	}`
	reviewSaveDefinition(t, h, ws, user, "health", 0, projectTestSource(), fractional)
	build := reviewBuildRevision(t, h, ws, user, 1)
	zero := int64(0)
	if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
		BuildID: build, ExpectedRevision: 1, ExpectedPublication: &zero, ReviewedCode: true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// A second draft, same fractional SLA, so the review has both documents.
	source := projectTestSource()
	source.Files[3].Content = "// second revision\n"
	reviewSaveDefinition(t, h, ws, user, "health", 1, source, fractional)

	_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	if snapshot.Candidate == nil {
		t.Fatal("no candidate")
	}
	live := reviewDefinitionPanel(t, "baseline.definition", snapshot.Baseline.Definition, "sluzby")["sla"]
	candidate := reviewDefinitionPanel(t, "candidate.definition", snapshot.Candidate.Definition, "sluzby")["sla"]
	if live != "90.5s" {
		t.Errorf("baseline.definition sla = %v, want the authored \"90.5s\" — an integer here is the "+
			"lossy `sla_seconds` the false SLA change came from", live)
	}
	if candidate != live {
		t.Errorf("the two sides of the comparison disagree about an SLA nobody changed: %v vs %v", live, candidate)
	}
	// And the Page detail wire still truncates, which is why it must not be
	// the comparison's source.
	detail := pagesGet(t, h, ws, user, "OWNER", "health")
	panels, _ := detail["panels"].([]any)
	if len(panels) != 1 {
		t.Fatalf("detail panels: %v", panels)
	}
	if seconds, _ := panels[0].(map[string]any)["sla_seconds"].(float64); seconds != 90 {
		t.Logf("the detail route reports sla_seconds = %v; the point of this test is only that the "+
			"review no longer reads it", seconds)
	}
}

// TestPageProjectReviewRoutinesAreWithheldWithTheirPanel — F2, the leak the
// withholding work introduced.
//
// `reviewRoutines` was fed the raw, unauthorized documents while only the two
// `definition` fields went through the authorizer. So the same response that
// said `excluded_panels: 1` and omitted the panel also carried the routine
// that panel calls, with its published digest, its current digest and whether
// it had changed — the association and a change oracle on another crew's
// routine, handed over by the response built to withhold it.
//
// The row set is now derived from the AUTHORIZED documents: a routine is
// served only when a panel that survived withholding declares it.
func TestPageProjectReviewRoutinesAreWithheldWithTheirPanel(t *testing.T) {
	h, ws, owner := reviewSealedDefinitionFixture(t)

	t.Run("a reader outside the crew gets neither the routine nor its state", func(t *testing.T) {
		w, snapshot := reviewCall(t, h, ws, "engineer", "MEMBER", "fleet-201")
		for _, row := range snapshot.Routines {
			if row.Routine == "ops-secret" {
				t.Errorf("routines[] carries the routine only crew/lookout's withheld panel calls, with "+
					"published=%v current=%v state=%q in_candidate=%v — the association and a change "+
					"oracle on that crew's routine", row.PublishedDigest, row.CurrentDigest, row.State, row.InCandidate)
			}
		}
		reviewAssertBodyWithholds(t, "the routine rows", w.Body.String(), reviewProtectedStrings)
		// The control: crew/engine's own routine is still fenced, still
		// carries its digests, and is still marked for the publish fence.
		row := reviewRoutineRow(t, snapshot, "ops-open")
		if !row.InCandidate || row.CurrentDigest == nil {
			t.Errorf("crew/engine's own routine came back unusable for the fence: %+v", row)
		}
	})

	t.Run("the page owner sees both routines", func(t *testing.T) {
		_, snapshot := reviewCall(t, h, ws, owner, "OWNER", "fleet-201")
		for _, name := range []string{"ops-secret", "ops-open"} {
			row := reviewRoutineRow(t, snapshot, name)
			if !row.InCandidate || row.CurrentDigest == nil {
				t.Errorf("routine %q is missing or unusable for a viewer entitled to every panel: %+v", name, row)
			}
		}
	})

	// The blocker sentence names the routine, so a routine that resolves for
	// nobody must not reach a reader who cannot see the panel calling it.
	t.Run("the routine_unresolved blocker does not name a withheld routine", func(t *testing.T) {
		if _, err := h.db.Exec(`UPDATE pipelines SET deleted_at='2026-09-11T00:00:00Z' WHERE id='pl-secret'`); err != nil {
			t.Fatal(err)
		}
		w, snapshot := reviewCall(t, h, ws, "engineer", "MEMBER", "fleet-201")
		if message, ok := reviewBlockers(snapshot)[reviewBlockerRoutineUnresolved]; ok {
			t.Errorf("routine_unresolved raised for a routine only a withheld panel calls, naming it: %q", message)
		}
		reviewAssertBodyWithholds(t, "the unresolved-routine path", w.Body.String(), reviewProtectedStrings)

		// The owner still learns about it, because it is genuinely their
		// problem: publishing this candidate is refused.
		_, ownerSnapshot := reviewCall(t, h, ws, owner, "OWNER", "fleet-201")
		if message := reviewBlockers(ownerSnapshot)[reviewBlockerRoutineUnresolved]; !strings.Contains(message, "ops-secret") {
			t.Errorf("the owner was not told which routine stopped resolving: %q", message)
		}
	})
}

// TestPageProjectReviewDivergedDefinitionIsAStatementNotARefusal — the dead
// end a live browser pass walked into three times.
//
// When the live Page definition drifts from the one the live publication
// shipped with, the review used to raise a `definition_moved` blocker. Any
// blocker disables consent, so publishing was refused — and publishing the
// candidate is exactly what brings the two documents back into agreement. The
// screen's advice, "refresh the review", could never help: a refetch reports
// the same drift. The only escape was the CLI.
func TestPageProjectReviewDivergedDefinitionIsAStatementNotARefusal(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	first := reviewBuildRevision(t, h, ws, user, 1)
	zero, one := int64(0), int64(1)
	if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
		BuildID: first, ExpectedRevision: 1, ExpectedPublication: &zero, ReviewedCode: true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, settled := reviewCall(t, h, ws, user, "OWNER", "health"); settled.Baseline.DefinitionDiverged {
		t.Fatalf("the definition diverged the moment it was published: %+v", settled.Baseline)
	}

	// Somebody edits the live Page outside the application flow.
	if _, err := h.db.Exec(`UPDATE pages SET spec_json=json_set(spec_json,'$.metadata.description','moved') WHERE slug='health'`); err != nil {
		t.Fatal(err)
	}
	// A candidate to publish over it.
	source := projectTestSource()
	source.Files[3].Content = "// second revision\n"
	if w := projectPut(t, h, ws, user, "OWNER", "health", 1, source); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	second := reviewBuildRevision(t, h, ws, user, 2)

	_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	if !snapshot.Baseline.DefinitionDiverged {
		t.Fatalf("the drift is not reported at all: %+v", snapshot.Baseline)
	}
	if message, ok := reviewBlockers(snapshot)[reviewBlockerDefinitionMoved]; ok {
		t.Fatalf("the drift is still a blocker, which is what disabled consent: %q", message)
	}
	if len(snapshot.Blockers) != 0 {
		t.Fatalf("a publishable candidate over a diverged definition still carries blockers, so consent stays "+
			"disabled and the dead end is unchanged: %+v", snapshot.Blockers)
	}

	// And the action that ends the divergence is available. The fence still
	// does its job: it checks the CURRENT live digest, which the snapshot
	// carries.
	if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
		BuildID: second, ExpectedRevision: 2, ExpectedPublication: &one, ReviewedCode: true,
		ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest}); w.Code != 200 {
		t.Fatalf("publishing over a diverged definition was refused: %d %s — publishing the candidate is "+
			"what ends the divergence, and refusing it leaves no way out of the editor", w.Code, w.Body.String())
	}
	if _, after := reviewCall(t, h, ws, user, "OWNER", "health"); after.Baseline.DefinitionDiverged {
		t.Errorf("the divergence survived the publication that should have ended it: %+v", after.Baseline)
	}
}

// TestPageProjectReviewStaleDefinitionStillTripsTheFence — the other fact,
// which keeps its name. A base that moves between the render and the click is
// a stale snapshot, refreshing IS the cure, and the publication is refused
// with `conflict: "definition"`.
func TestPageProjectReviewStaleDefinitionStillTripsTheFence(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	build := reviewBuildRevision(t, h, ws, user, 1)
	reviewed := reviewLiveDigest(t, h, "health")
	if _, err := h.db.Exec(`UPDATE pages SET spec_json=json_set(spec_json,'$.metadata.description','moved') WHERE slug='health'`); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
		BuildID: build, ExpectedRevision: 1, ExpectedPublication: &zero, ReviewedCode: true,
		ExpectedDefinitionDigest: reviewed})
	if w.Code != 409 {
		t.Fatalf("publish over a definition that moved since the render: %d %s", w.Code, w.Body.String())
	}
	if kind, _ := publishConflict(t, w); kind != "definition" {
		t.Fatalf("conflict = %q, want definition: %s", kind, w.Body.String())
	}
}

// TestPageProjectReviewDivergenceAfterAWithdrawalAssertsNothingRunning — the
// sentence that was false in one state.
//
// The blocker used to say the live definition no longer matched "the one
// published with the RUNNING application". This branch is also reached after a
// withdrawal, where the same snapshot reports `published: false` — nothing is
// running — so the client had to compose a truthful replacement for that case.
// Nothing on the wire asserts it any more.
func TestPageProjectReviewDivergenceAfterAWithdrawalAssertsNothingRunning(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	build := reviewBuildRevision(t, h, ws, user, 1)
	zero := int64(0)
	if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{
		BuildID: build, ExpectedRevision: 1, ExpectedPublication: &zero, ReviewedCode: true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err := h.db.Exec(`UPDATE page_project_live SET published=0 WHERE page_id=(SELECT id FROM pages WHERE slug='health')`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE pages SET spec_json=json_set(spec_json,'$.metadata.description','moved') WHERE slug='health'`); err != nil {
		t.Fatal(err)
	}

	w, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	if snapshot.Baseline.Published {
		t.Fatalf("the publication is still running, so this is not the state under test: %+v", snapshot.Baseline)
	}
	if !snapshot.Baseline.DefinitionDiverged {
		t.Fatalf("the drift is not reported after a withdrawal: %+v", snapshot.Baseline)
	}
	for _, blocker := range snapshot.Blockers {
		if strings.Contains(blocker.Message, "running application") {
			t.Errorf("blocker %q asserts a running application in a snapshot that says published:false — %q",
				blocker.Code, blocker.Message)
		}
	}
	if strings.Contains(w.Body.String(), "running application") {
		t.Errorf("the response asserts a running application while reporting published:false: %s", w.Body.String())
	}
}
