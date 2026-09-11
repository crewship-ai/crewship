package api

// Pages — the truthfulness of `reviewed_code: true` (docs/prd/pages.md §7.1b,
// §11).
//
// Withholding a panel from the review comparison stops the leak and stops the
// phantom addition, but it does not make the consent underneath true: the
// publisher still attested that the whole change had been reviewed. That gap
// is reachable rather than theoretical, and the reachability is the first
// thing these tests pin down:
//
//	publishing  → mayAdministerGrants = manage role OR isPageOwner
//	seeing a panel → canSeePanel      = manage role OR membership of that
//	                                    panel's owning crew
//
// Neither asks the other's question, so a Page owner who is not a workspace
// administrator may publish a Page while reading only their own crews' panels.
//
// Nothing here is a disclosed exploit and nothing leaked: publishing already
// required ownership or administration, and the operation was always this
// caller's to perform. What was wrong is the CLAIM the request carries about
// what was reviewed. Permission to perform an operation and truthfulness of
// the attestation about it are separate, and only the second is fixed here.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
)

// withheldCreateBody is the Page as the create API takes it (SLA as an
// integer, §11b.3): one panel owned by crew/lookout and one by crew/engine,
// because one caller's standing differing per panel is the whole subject.
const withheldCreateBody = `{
	"slug": "health",
	"name": "Flotila .201",
	"panels": [
		{"id": "sluzby", "schema": "status.v1", "title": "Jede to?",
		 "owner": "crew/lookout", "producer": "script/watch-services.sh", "sla_seconds": 30, "span": 8},
		{"id": "zatizeni", "schema": "metric.v1", "title": "Zatizeni",
		 "owner": "crew/engine", "producer": "script/load.sh", "sla_seconds": 60, "span": 4}
	]
}`

// withheldDefinition is the same Page as a draft DOCUMENT, with the two knobs
// these tests turn: the title of the panel the publisher cannot read, and the
// owning crew of the one they can.
func withheldDefinition(withheldTitle, visibleOwner string) string {
	return `{
		"apiVersion": "crewship/v1",
		"kind": "Page",
		"metadata": {"name": "Flotila .201", "slug": "health"},
		"spec": {"panels": [
			{"id": "sluzby", "schema": "status.v1", "title": "` + withheldTitle + `",
			 "owner": "crew/lookout", "producer": "script/watch-services.sh", "sla": "30s", "span": 8},
			{"id": "zatizeni", "schema": "metric.v1", "title": "Zatizeni",
			 "owner": "` + visibleOwner + `", "producer": "script/load.sh", "sla": "60s", "span": 4}
		]}
	}`
}

// withheldFixture is a publishable Page "health" OWNED BY a workspace MEMBER
// who belongs to crew/engine and not to crew/lookout.
//
// That is the standing the whole file is about, and the fixture asserts it
// rather than assuming it: the publisher must come out able to publish and
// unable to read one of the panels they would be publishing.
func withheldFixture(t *testing.T) (*PageHandler, string, string, string) {
	t.Helper()
	h, _, _, ws, admin := newPagesFixture(t)
	if _, err := h.db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-engine', ?, 'Engine', 'engine')`, ws); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	pagesSeedUser(t, h, ws, "publisher", "publisher@example.com", "MEMBER")
	if _, err := h.db.Exec(`INSERT INTO crew_members (id, crew_id, user_id, role) VALUES ('cm-publisher', 'crew-engine', 'publisher', 'MEMBER')`); err != nil {
		t.Fatalf("add publisher to crew/engine: %v", err)
	}
	// `page.create` is the capability layer an admin uses to trust a MEMBER
	// with page authoring without promoting them (capabilities.go), and it is
	// how this standing arises in a real workspace: the page is created BY the
	// publisher, so owner_user_id is theirs. Creation owns; handing a page to
	// another user is a transfer, which has its own rules.
	if _, err := h.db.Exec(`UPDATE workspace_members SET capabilities = ? WHERE workspace_id = ? AND user_id = ?`,
		`["`+CapabilityPageCreate+`"]`, ws, "publisher"); err != nil {
		t.Fatalf("grant page.create: %v", err)
	}
	req := pagesRequest(t, "POST", "/api/v1/pages", ws, "publisher", "MEMBER", withheldCreateBody)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create page as a member: %d %s", rr.Code, rr.Body.String())
	}
	h.SetProjectStore(&pages.ProjectStore{Directory: storageDir(t)})
	h.SetBuildWorker(&testPageBuilder{}, &pagebuild.Store{Directory: storageDir(t)})
	h.pageRuntimeOrigin = "https://pages.example.net"
	h.pageStudioOrigin = "https://studio.example.com"

	// The reachability this file depends on, checked rather than assumed.
	_, snapshot := reviewCall(t, h, ws, "publisher", "MEMBER", "health")
	if !snapshot.Capabilities.MayPublish {
		t.Fatalf("the page owner cannot publish, so the case under test is unreachable: %+v", snapshot.Capabilities)
	}
	if snapshot.Baseline.ExcludedPanels != 1 {
		t.Fatalf("excluded_panels = %d, want 1 — the publisher must be unable to read crew/lookout's panel "+
			"or there is no attestation gap to test: %+v", snapshot.Baseline.ExcludedPanels, snapshot.Baseline)
	}
	return h, ws, admin, "publisher"
}

// withheldPublishableDraft saves a draft carrying `definition` and builds it,
// returning the build id and the revision. Authored by the ADMIN: who wrote
// the draft is not the subject, who publishes it is.
func withheldPublishableDraft(t *testing.T, h *PageHandler, ws, admin, definition string, expected int64, marker string) (string, int64) {
	t.Helper()
	source := projectTestSource()
	source.Files[3].Content = "// " + marker + "\n"
	reviewSaveDefinition(t, h, ws, admin, "health", expected, source, definition)
	revision := expected + 1
	return reviewBuildRevision(t, h, ws, admin, revision), revision
}

// withheldAssertNeutral holds the refusal to what it is allowed to say.
func withheldAssertNeutral(t *testing.T, where, message string) {
	t.Helper()
	if message == "" {
		t.Fatalf("%s: the refusal carries no sentence", where)
	}
	for _, secret := range []string{"sluzby", "lookout", "Jede to?", "Bezi to?", "watch-services", "status.v1"} {
		if strings.Contains(message, secret) {
			t.Errorf("%s names %q; the refusal may say that a withheld part changed and how many panels are "+
				"withheld, never what it is, whose crew it belongs to, or its name: %s", where, secret, message)
		}
	}
}

func withheldPublishError(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("refusal body is not JSON: %v — %s", err, w.Body.String())
	}
	if _, ok := body["conflict"]; ok {
		t.Errorf("the refusal carries a `conflict` kind: this is not a state conflict, there is no value to "+
			"re-review and no refetch that changes the answer — %s", w.Body.String())
	}
	message, _ := body["error"].(string)
	return message
}

// TestPagePublishRefusesAnAttestationTheCallerCannotMake is the policy.
func TestPagePublishRefusesAnAttestationTheCallerCannotMake(t *testing.T) {
	t.Run("an unchanged withheld panel still publishes", func(t *testing.T) {
		h, ws, admin, publisher := withheldFixture(t)
		build, revision := withheldPublishableDraft(t, h, ws, admin, withheldDefinition("Jede to?", "crew/engine"), 0, "first")

		_, snapshot := reviewCall(t, h, ws, publisher, "MEMBER", "health")
		if snapshot.Baseline.WithheldChanged {
			t.Fatalf("withheld_changed is true for a candidate that leaves the withheld panel alone: %+v", snapshot.Baseline)
		}
		if reviewBlockers(snapshot)[reviewBlockerWithheldChange] != "" {
			t.Errorf("withheld_change blocker raised with nothing withheld changing: %+v", snapshot.Blockers)
		}
		if snapshot.Baseline.ExcludedPanels != 1 {
			t.Errorf("excluded_panels = %d, want 1 — the panel is still withheld, it just did not move",
				snapshot.Baseline.ExcludedPanels)
		}
		zero := int64(0)
		if w := publishCall(t, h, ws, publisher, "MEMBER", "health", pageProjectPublishRequest{
			BuildID: build, ExpectedRevision: revision, ExpectedPublication: &zero, ReviewedCode: true}); w.Code != 200 {
			t.Fatalf("publication refused although the reader's comparison covers everything that moves: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("a changed withheld panel blocks the review and refuses the publication", func(t *testing.T) {
		h, ws, admin, publisher := withheldFixture(t)
		build, revision := withheldPublishableDraft(t, h, ws, admin, withheldDefinition("Bezi to?", "crew/engine"), 0, "first")

		_, snapshot := reviewCall(t, h, ws, publisher, "MEMBER", "health")
		if !snapshot.Baseline.WithheldChanged {
			t.Fatalf("withheld_changed is false although the candidate changes a panel this publisher cannot read: %+v", snapshot.Baseline)
		}
		blocker := reviewBlockers(snapshot)[reviewBlockerWithheldChange]
		if blocker == "" {
			t.Fatalf("no withheld_change blocker: %+v", snapshot.Blockers)
		}
		withheldAssertNeutral(t, "the review blocker", blocker)

		zero := int64(0)
		w := publishCall(t, h, ws, publisher, "MEMBER", "health", pageProjectPublishRequest{
			BuildID: build, ExpectedRevision: revision, ExpectedPublication: &zero, ReviewedCode: true})
		if w.Code != http.StatusForbidden {
			t.Fatalf("publish status = %d, want 403 — the blocker alone is a hidden button, and "+
				"reviewed_code:true is an attestation this caller cannot honestly make: %s", w.Code, w.Body.String())
		}
		withheldAssertNeutral(t, "the publish refusal", withheldPublishError(t, w))
		var published int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_project_publications`).Scan(&published); err != nil {
			t.Fatal(err)
		}
		if published != 0 {
			t.Errorf("%d publications were written by a refused request", published)
		}
	})

	// Nobody may later "fix" this into an obstacle for administrators: an
	// admin sees every panel, so there is never a part of the change they did
	// not review and the check must never fire for them.
	t.Run("a workspace administrator is unaffected", func(t *testing.T) {
		h, ws, admin, _ := withheldFixture(t)
		build, revision := withheldPublishableDraft(t, h, ws, admin, withheldDefinition("Bezi to?", "crew/engine"), 0, "first")

		_, snapshot := reviewCall(t, h, ws, admin, "OWNER", "health")
		if snapshot.Baseline.WithheldChanged || snapshot.Baseline.ExcludedPanels != 0 {
			t.Fatalf("an administrator was told part of the Page is withheld from them: %+v", snapshot.Baseline)
		}
		if reviewBlockers(snapshot)[reviewBlockerWithheldChange] != "" {
			t.Errorf("withheld_change blocker raised for an administrator: %+v", snapshot.Blockers)
		}
		zero := int64(0)
		if w := publishCall(t, h, ws, admin, "OWNER", "health", pageProjectPublishRequest{
			BuildID: build, ExpectedRevision: revision, ExpectedPublication: &zero, ReviewedCode: true}); w.Code != 200 {
			t.Fatalf("an administrator, who can read every panel, was refused: %d %s", w.Code, w.Body.String())
		}
	})
}

// TestPagePublishWithheldChangeCoversTheRollbackPath — restoring a retained
// version replaces the live definition too, so the same attestation is being
// made about it. The comparison is against that version's archived
// declaration, which is what the rollback would make live.
func TestPagePublishWithheldChangeCoversTheRollbackPath(t *testing.T) {
	h, ws, admin, publisher := withheldFixture(t)
	first, firstRevision := withheldPublishableDraft(t, h, ws, admin, withheldDefinition("Jede to?", "crew/engine"), 0, "first")
	zero, one := int64(0), int64(1)
	if w := publishCall(t, h, ws, admin, "OWNER", "health", pageProjectPublishRequest{
		BuildID: first, ExpectedRevision: firstRevision, ExpectedPublication: &zero, ReviewedCode: true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}

	t.Run("a rollback that leaves the withheld panel alone is allowed", func(t *testing.T) {
		// Publication 2 moves only the panel the publisher CAN read, so
		// restoring publication 1 restores nothing they cannot see.
		second, secondRevision := withheldPublishableDraft(t, h, ws, admin, strings.Replace(withheldDefinition("Jede to?", "crew/engine"), "Zatizeni", "Zatizeni II", 1), 1, "second")
		if w := publishCall(t, h, ws, admin, "OWNER", "health", pageProjectPublishRequest{
			BuildID: second, ExpectedRevision: secondRevision, ExpectedPublication: &one, ReviewedCode: true}); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		_, snapshot := reviewCallTarget(t, h, ws, publisher, "MEMBER", "health", "/?publication=1")
		if snapshot.Baseline.WithheldChanged {
			t.Fatalf("withheld_changed is true for a rollback that touches nothing withheld: %+v", snapshot.Baseline)
		}
		two := int64(2)
		if w := publishCall(t, h, ws, publisher, "MEMBER", "health", pageProjectPublishRequest{
			RollbackVersion: 1, ExpectedPublication: &two, ReviewedCode: true}); w.Code != 200 {
			t.Fatalf("rollback refused although nothing withheld moves: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("a rollback that would restore a different withheld panel is refused", func(t *testing.T) {
		// The live definition is publication 1's again after the rollback
		// above. Publication 4 changes the WITHHELD panel, published by an
		// administrator who may attest to it; the publisher then tries to
		// restore publication 1 over it.
		// Draft revisions and publication versions are different counters: a
		// rollback publishes a version without authoring a revision, so the
		// draft is at 2 while the publication counter is at 3.
		fourth, fourthRevision := withheldPublishableDraft(t, h, ws, admin, withheldDefinition("Bezi to?", "crew/engine"), 2, "fourth")
		three := int64(3)
		if w := publishCall(t, h, ws, admin, "OWNER", "health", pageProjectPublishRequest{
			BuildID: fourth, ExpectedRevision: fourthRevision, ExpectedPublication: &three, ReviewedCode: true}); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		_, snapshot := reviewCallTarget(t, h, ws, publisher, "MEMBER", "health", "/?publication=1")
		if !snapshot.Baseline.WithheldChanged {
			t.Fatalf("withheld_changed is false for a rollback that would rewrite a withheld panel: %+v", snapshot.Baseline)
		}
		if blocker := reviewBlockers(snapshot)[reviewBlockerWithheldChange]; blocker == "" {
			t.Errorf("no withheld_change blocker on the rollback review: %+v", snapshot.Blockers)
		} else {
			withheldAssertNeutral(t, "the rollback review blocker", blocker)
		}
		four := int64(4)
		w := publishCall(t, h, ws, publisher, "MEMBER", "health", pageProjectPublishRequest{
			RollbackVersion: 1, ExpectedPublication: &four, ReviewedCode: true})
		if w.Code != http.StatusForbidden {
			t.Fatalf("rollback status = %d, want 403 — a rollback replaces the live definition too: %s", w.Code, w.Body.String())
		}
		withheldAssertNeutral(t, "the rollback refusal", withheldPublishError(t, w))
	})
}

// TestPageReviewWithheldChangeCatchesARepointedPanel — the case that vanishes
// completely without this answer.
//
// A candidate that re-points a panel from a crew the publisher can see to one
// they cannot puts that panel in the withheld union, which removes it from
// BOTH rendered documents. The comparison on screen then shows nothing at all
// about a panel whose ACL the publication rewrites.
func TestPageReviewWithheldChangeCatchesARepointedPanel(t *testing.T) {
	h, ws, admin, publisher := withheldFixture(t)
	build, revision := withheldPublishableDraft(t, h, ws, admin, withheldDefinition("Jede to?", "crew/lookout"), 0, "first")

	_, snapshot := reviewCall(t, h, ws, publisher, "MEMBER", "health")
	if snapshot.Baseline.ExcludedPanels != 2 {
		t.Errorf("excluded_panels = %d, want 2 — the re-pointed panel is withheld on the candidate side and "+
			"therefore out of the comparison on both", snapshot.Baseline.ExcludedPanels)
	}
	if !snapshot.Baseline.WithheldChanged {
		t.Fatalf("withheld_changed is false although the candidate moves a panel out of this publisher's "+
			"reach — the one change the rendered comparison cannot show at all: %+v", snapshot.Baseline)
	}
	if snapshot.Candidate == nil {
		t.Fatal("no candidate")
	}
	for _, definition := range []json.RawMessage{snapshot.Baseline.Definition, snapshot.Candidate.Definition} {
		if strings.Contains(string(definition), "zatizeni") {
			t.Errorf("the re-pointed panel is still rendered, so it was not withheld after all: %s", definition)
		}
	}
	zero := int64(0)
	w := publishCall(t, h, ws, publisher, "MEMBER", "health", pageProjectPublishRequest{
		BuildID: build, ExpectedRevision: revision, ExpectedPublication: &zero, ReviewedCode: true})
	if w.Code != http.StatusForbidden {
		t.Fatalf("publish status = %d, want 403 for a re-pointed panel: %s", w.Code, w.Body.String())
	}
}

// TestPagePublishReplayIsNotRefusedByTheWithheldCheck — the idempotent retry
// stays intact.
//
// A replay is re-delivery of a publication that already committed. Refusing it
// because the world moved on afterwards would turn a delivered success into a
// phantom failure, and invite a second publication of the same code. The check
// sits after the short-circuit for exactly that reason; this pins it there.
func TestPagePublishReplayIsNotRefusedByTheWithheldCheck(t *testing.T) {
	h, ws, admin, publisher := withheldFixture(t)
	build, revision := withheldPublishableDraft(t, h, ws, admin, withheldDefinition("Jede to?", "crew/engine"), 0, "first")
	digest := reviewLiveDigest(t, h, "health")
	zero := int64(0)
	request := pageProjectPublishRequest{
		BuildID: build, ExpectedRevision: revision, ExpectedPublication: &zero, ReviewedCode: true,
		ExpectedDefinitionDigest: digest, ExpectedRoutineDigests: map[string]string{},
	}
	if w := publishCall(t, h, ws, publisher, "MEMBER", "health", request); w.Code != 200 {
		t.Fatalf("first publication: %d %s", w.Code, w.Body.String())
	}

	// Somebody with the standing to do it now changes the withheld panel on
	// the live Page. The publisher's already-committed request is re-delivered.
	body := `{"slug": "health", "name": "Flotila .201", "panels": [
		{"id": "sluzby", "schema": "status.v1", "title": "Bezi to?", "owner": "crew/lookout",
		 "producer": "script/watch-services.sh", "sla_seconds": 30, "span": 8},
		{"id": "zatizeni", "schema": "metric.v1", "title": "Zatizeni", "owner": "crew/engine",
		 "producer": "script/load.sh", "sla_seconds": 60, "span": 4}]}`
	req := pagesRequest(t, http.MethodPatch, "/api/v1/pages/health", ws, admin, "OWNER", body)
	req.SetPathValue("slug", "health")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("change the live withheld panel: %d %s", rr.Code, rr.Body.String())
	}

	w := publishCall(t, h, ws, publisher, "MEMBER", "health", request)
	if w.Code != 200 {
		t.Fatalf("replay status = %d, want 200 with the original receipt — a delivered success must not "+
			"become a phantom failure: %s", w.Code, w.Body.String())
	}
	var receipt map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatalf("receipt is not JSON: %v — %s", err, w.Body.String())
	}
	if receipt["replayed"] != true {
		t.Errorf("the replay did not return the recorded receipt: %s", w.Body.String())
	}
	var published int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_project_publications`).Scan(&published); err != nil {
		t.Fatal(err)
	}
	if published != 1 {
		t.Errorf("%d publications exist after a replay, want 1", published)
	}
}
