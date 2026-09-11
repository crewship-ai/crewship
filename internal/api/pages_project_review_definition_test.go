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
		 "owner": "crew/lookout", "producer": "script/watch-services.sh", "sla": "30s", "span": 8},
		{"id": "zatizeni", "schema": "metric.v1", "title": "Zatizeni",
		 "owner": "crew/engine", "producer": "script/load.sh", "sla": "60s", "span": 4},
		{"id": "posadka", "schema": "status.v1", "title": "Kdo ma sluzbu",
		 "owner": "crew/lookout", "producer": "script/roster.sh", "sla": "90s", "span": 12}
	]}
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
	reviewSaveDefinition(t, h, ws, owner, "fleet-201", 0, projectTestSource(), reviewCandidateDefinitionBody)
	return h, ws, owner
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
		// The leak, named field by field rather than by shape.
		both := string(snapshot.Baseline.Definition) + string(snapshot.Candidate.Definition)
		for _, secret := range []string{"status.v1", "metric.v1", "watch-services.sh", "load.sh", "roster.sh", "Jede to?", "Zatizeni", "Kdo ma sluzbu", "crew/lookout", "crew/engine"} {
			if strings.Contains(both, secret) {
				t.Errorf("the review snapshot discloses %q to a spec editor entitled to no panel on this page:\n baseline:  %s\n candidate: %s",
					secret, snapshot.Baseline.Definition, snapshot.Candidate.Definition)
			}
		}
		if snapshot.Baseline.DefinitionDigest != digest {
			t.Errorf("definition_digest = %s, want the digest of the FULL stored document (%s) — the fence "+
				"compares what the server stores, not what this viewer was shown",
				snapshot.Baseline.DefinitionDigest, digest)
		}
	})

	t.Run("a spec editor in one crew gets that panel and only that panel", func(t *testing.T) {
		_, snapshot := reviewCall(t, h, ws, "engineer", "MEMBER", "fleet-201")
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
