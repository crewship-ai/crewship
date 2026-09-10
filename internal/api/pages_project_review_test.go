package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
)

// reviewFixture is a Page with source storage, a build worker and a runtime
// origin, i.e. everything a publication needs, so `storage_unavailable` shows
// up only where a test removes one of them on purpose.
//
// storageDir, never t.TempDir: the build worker writes from a detached
// goroutine and t.TempDir's cleanup races it (router_test.go).
func reviewFixture(t *testing.T) (*PageHandler, *testPageBuilder, string, string) {
	t.Helper()
	h, _, _, ws, user := newPagesFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: storageDir(t)})
	builder := &testPageBuilder{}
	h.SetBuildWorker(builder, &pagebuild.Store{Directory: storageDir(t)})
	h.pageRuntimeOrigin = "https://pages.example.net"
	h.pageStudioOrigin = "https://studio.example.com"
	pagesCreate(t, h, ws, user, "health")
	return h, builder, ws, user
}

func reviewCall(t *testing.T, h *PageHandler, ws, actor, role, slug string) (*httptest.ResponseRecorder, reviewSnapshotWire) {
	t.Helper()
	return reviewCallTarget(t, h, ws, actor, role, slug, "/")
}

// reviewCallTarget is reviewCall with a chosen query string, for ?publication=N.
func reviewCallTarget(t *testing.T, h *PageHandler, ws, actor, role, slug, target string) (*httptest.ResponseRecorder, reviewSnapshotWire) {
	t.Helper()
	r := pagesRequest(t, "GET", target, ws, actor, role, "")
	r.SetPathValue("slug", slug)
	w := httptest.NewRecorder()
	h.ReviewProject(w, r)
	var snapshot reviewSnapshotWire
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
			t.Fatalf("review response is not JSON: %v — %s", err, w.Body.String())
		}
	}
	return w, snapshot
}

func reviewBlockers(snapshot reviewSnapshotWire) map[string]string {
	out := map[string]string{}
	for _, b := range snapshot.Blockers {
		out[b.Code] = b.Message
	}
	return out
}

func reviewRoutineRow(t *testing.T, snapshot reviewSnapshotWire, name string) reviewRoutineWire {
	t.Helper()
	for _, row := range snapshot.Routines {
		if row.Routine == name {
			return row
		}
	}
	t.Fatalf("routine %q missing from the snapshot: %+v", name, snapshot.Routines)
	return reviewRoutineWire{}
}

func reviewLiveDigest(t *testing.T, h *PageHandler, slug string) string {
	t.Helper()
	var spec string
	if err := h.db.QueryRow(`SELECT spec_json FROM pages WHERE slug=?`, slug).Scan(&spec); err != nil {
		t.Fatalf("read live definition: %v", err)
	}
	sum := sha256.Sum256([]byte(spec))
	return hex.EncodeToString(sum[:])
}

// reviewBuild drives one preview build of the current draft to completion and
// returns its id.
func reviewBuildRevision(t *testing.T, h *PageHandler, ws, user string, revision int64) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"expected_revision": revision})
	w := buildRequest(t, h, "POST", "/", ws, user, "OWNER", string(body))
	if w.Code != 202 {
		t.Fatalf("build revision %d: %d %s", revision, w.Code, w.Body.String())
	}
	var job pageBuildRecord
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if !waitForBackgroundWork(5e9) {
		t.Fatal("build did not finish")
	}
	return job.ID
}

// publishCall sends a publication. An empty ExpectedDefinitionDigest is filled
// from the current review snapshot, which is what the CLI and the editor do;
// tests that are ABOUT the fence pass their own value.
func publishCall(t *testing.T, h *PageHandler, ws, actor, role, slug string, req pageProjectPublishRequest) *httptest.ResponseRecorder {
	t.Helper()
	if req.ExpectedDefinitionDigest == "" {
		req.ExpectedDefinitionDigest = reviewLiveDigest(t, h, slug)
	}
	if req.ExpectedRoutineDigests == nil {
		req.ExpectedRoutineDigests = map[string]string{}
	}
	body, _ := json.Marshal(req)
	r := pagesRequest(t, "POST", "/", ws, actor, role, string(body))
	r.SetPathValue("slug", slug)
	w := httptest.NewRecorder()
	h.PublishProject(w, r)
	return w
}

func publishConflict(t *testing.T, w *httptest.ResponseRecorder) (string, []string) {
	t.Helper()
	var body struct {
		Error    string   `json:"error"`
		Conflict string   `json:"conflict"`
		Routines []string `json:"routines"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("conflict body is not JSON: %v — %s", err, w.Body.String())
	}
	if body.Error == "" {
		t.Fatalf("conflict body has no error sentence: %s", w.Body.String())
	}
	return body.Conflict, body.Routines
}

// TestPageProjectReviewCandidateBaselineAndBlockers walks one Page through the
// states a reviewer actually meets, in order, asserting the snapshot tells the
// truth about each. It is one test rather than six because the states are
// reached by doing the real work — saving, building, publishing — and a
// per-state fixture would assert on a database nobody could arrive at.
func TestPageProjectReviewCandidateBaselineAndBlockers(t *testing.T) {
	h, builder, ws, user := reviewFixture(t)

	t.Run("no draft", func(t *testing.T) {
		w, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
		if w.Code != 200 {
			t.Fatalf("review: %d %s", w.Code, w.Body.String())
		}
		if snapshot.Candidate != nil {
			t.Fatalf("candidate offered without a draft: %+v", snapshot.Candidate)
		}
		blockers := reviewBlockers(snapshot)
		if _, ok := blockers[reviewBlockerNoCandidate]; !ok {
			t.Fatalf("missing no_candidate: %+v", snapshot.Blockers)
		}
		if !snapshot.InitialPublication || snapshot.Baseline.PublicationVersion != 0 || snapshot.Baseline.Published {
			t.Fatalf("first review is not an initial publication: %+v", snapshot.Baseline)
		}
		// An initial publication is allowed. "No prior source" is a stated
		// fact, not a refusal.
		if snapshot.Baseline.SourceAvailable {
			t.Fatal("claimed a retained baseline before any publication")
		}
		if snapshot.Baseline.SourceUnavailableReason == nil || !strings.Contains(*snapshot.Baseline.SourceUnavailableReason, "never published") {
			t.Fatalf("no concrete reason for the missing baseline: %+v", snapshot.Baseline.SourceUnavailableReason)
		}
		if _, ok := blockers[reviewBlockerBaselineMissing]; ok {
			t.Fatal("initial publication refused as baseline_unavailable")
		}
		if snapshot.Baseline.DefinitionDigest != reviewLiveDigest(t, h, "health") {
			t.Fatal("definition digest is not sha256 of the live spec_json")
		}
		if !snapshot.Capabilities.MayEditSpec || !snapshot.Capabilities.MayPublish {
			t.Fatalf("owner capabilities: %+v", snapshot.Capabilities)
		}
		if snapshot.IssuedAt == "" {
			t.Fatal("snapshot has no issue time")
		}
	})

	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}

	t.Run("draft without a build", func(t *testing.T) {
		_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
		if snapshot.Candidate == nil || snapshot.Candidate.Revision != 1 {
			t.Fatalf("candidate: %+v", snapshot.Candidate)
		}
		if snapshot.Candidate.Build != nil {
			t.Fatalf("build reported without one: %+v", snapshot.Candidate.Build)
		}
		if snapshot.Candidate.Actor.Kind != "user" || snapshot.Candidate.Actor.ID != user {
			t.Fatalf("actor: %+v", snapshot.Candidate.Actor)
		}
		if snapshot.Candidate.Actor.Label != "test@example.com" {
			t.Fatalf("actor label was not resolved from the current directory: %q", snapshot.Candidate.Actor.Label)
		}
		if _, ok := reviewBlockers(snapshot)[reviewBlockerBuildMissing]; !ok {
			t.Fatalf("missing build_missing: %+v", snapshot.Blockers)
		}
	})

	t.Run("failed build", func(t *testing.T) {
		builder.fail = true
		defer func() { builder.fail = false }()
		reviewBuildRevision(t, h, ws, user, 1)
		_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
		if snapshot.Candidate == nil || snapshot.Candidate.Build == nil || snapshot.Candidate.Build.State != "failed" {
			t.Fatalf("build state: %+v", snapshot.Candidate)
		}
		if snapshot.Candidate.Build.Error == "" {
			t.Fatal("failed build reported without its diagnostic")
		}
		blockers := reviewBlockers(snapshot)
		if _, ok := blockers[reviewBlockerBuildFailed]; !ok {
			t.Fatalf("missing build_failed: %+v", snapshot.Blockers)
		}
		if _, ok := blockers[reviewBlockerBuildMissing]; ok {
			t.Fatal("a failed build also reported as missing")
		}
	})

	build := reviewBuildRevision(t, h, ws, user, 1)

	t.Run("ready build", func(t *testing.T) {
		_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
		if snapshot.Candidate == nil || snapshot.Candidate.Build == nil || snapshot.Candidate.Build.State != "ready" {
			t.Fatalf("build state: %+v", snapshot.Candidate)
		}
		if snapshot.Candidate.Build.ID != build || snapshot.Candidate.Build.ArtifactDigest == "" {
			t.Fatalf("ready build without its artifact: %+v", snapshot.Candidate.Build)
		}
		if len(snapshot.Blockers) != 0 {
			t.Fatalf("publishable candidate still blocked: %+v", snapshot.Blockers)
		}
		if snapshot.Candidate.GitCommit == "" || snapshot.Candidate.SourceDigest == "" || snapshot.Candidate.CreatedAt == "" {
			t.Fatalf("candidate identity incomplete: %+v", snapshot.Candidate)
		}
	})

	zero := int64(0)
	if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{BuildID: build, ExpectedRevision: 1, ExpectedPublication: &zero, ReviewedCode: true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}

	t.Run("draft identical to live", func(t *testing.T) {
		_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
		if snapshot.Candidate != nil {
			t.Fatalf("already-live draft offered as a candidate: %+v", snapshot.Candidate)
		}
		blockers := reviewBlockers(snapshot)
		if _, ok := blockers[reviewBlockerMatchesLive]; !ok {
			t.Fatalf("missing candidate_matches_live: %+v", snapshot.Blockers)
		}
		if _, ok := blockers[reviewBlockerNoCandidate]; ok {
			t.Fatal("an identical draft reported as no draft at all")
		}
		if snapshot.InitialPublication || snapshot.Baseline.PublicationVersion != 1 || !snapshot.Baseline.Published {
			t.Fatalf("baseline after publication: %+v %v", snapshot.Baseline, snapshot.InitialPublication)
		}
		if !snapshot.Baseline.SourceAvailable || snapshot.Baseline.SourceUnavailableReason != nil {
			t.Fatalf("retained baseline unreadable right after publishing: %+v", snapshot.Baseline)
		}
		if snapshot.Baseline.SourceRevision == nil || *snapshot.Baseline.SourceRevision != 1 || snapshot.Baseline.GitCommit == nil {
			t.Fatalf("baseline source identity: %+v", snapshot.Baseline)
		}
		if _, ok := blockers[reviewBlockerDefinitionMoved]; ok {
			t.Fatal("definition reported as moved immediately after it was published")
		}
	})

	t.Run("ready build of an earlier revision", func(t *testing.T) {
		changed := projectTestSource()
		changed.Files[3].Content = "// second revision\n"
		if w := projectPut(t, h, ws, user, "OWNER", "health", 1, changed); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
		if snapshot.Candidate == nil || snapshot.Candidate.Revision != 2 || snapshot.Candidate.Build != nil {
			t.Fatalf("candidate: %+v", snapshot.Candidate)
		}
		blockers := reviewBlockers(snapshot)
		if _, ok := blockers[reviewBlockerBuildStale]; !ok {
			t.Fatalf("missing build_stale: %+v", snapshot.Blockers)
		}
		if _, ok := blockers[reviewBlockerBuildMissing]; ok {
			t.Fatal("a stale ready build also reported as no build")
		}
	})
}

// TestPageProjectReviewBaselineUnavailableIsNotAgreement — V05. Losing the
// retained source removes the ability to compare, and the response has to say
// so out loud rather than serve an empty comparison.
func TestPageProjectReviewBaselineUnavailableIsNotAgreement(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	build := reviewBuildRevision(t, h, ws, user, 1)
	zero := int64(0)
	if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{BuildID: build, ExpectedRevision: 1, ExpectedPublication: &zero, ReviewedCode: true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// A NEW candidate, so the snapshot has something to compare: the point is
	// that the comparison basis is gone, not that there is nothing to compare.
	changed := projectTestSource()
	changed.Files[3].Content = "// awaiting review\n"
	if w := projectPut(t, h, ws, user, "OWNER", "health", 1, changed); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var commit string
	if err := h.db.QueryRow(`SELECT git_commit FROM page_project_publications WHERE version=1`).Scan(&commit); err != nil {
		t.Fatal(err)
	}
	// The retained checkpoint goes away under the publication.
	h.SetProjectStore(&pages.ProjectStore{Directory: storageDir(t)})

	_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	if snapshot.Baseline.SourceAvailable {
		t.Fatal("unreadable retained source reported as available")
	}
	reason := snapshot.Baseline.SourceUnavailableReason
	if reason == nil || !strings.Contains(*reason, commit) {
		t.Fatalf("reason does not name the missing checkpoint: %v", reason)
	}
	blockers := reviewBlockers(snapshot)
	if _, ok := blockers[reviewBlockerBaselineMissing]; !ok {
		t.Fatalf("missing baseline_unavailable: %+v", snapshot.Blockers)
	}
	// The three ways an empty comparison could be mistaken for agreement.
	if snapshot.InitialPublication {
		t.Fatal("a Page with a publication reported as an initial publication")
	}
	if snapshot.Baseline.PublicationVersion != 1 || snapshot.Baseline.GitCommit == nil || *snapshot.Baseline.GitCommit != commit {
		t.Fatalf("baseline stopped naming what it cannot read: %+v", snapshot.Baseline)
	}
	if _, ok := blockers[reviewBlockerMatchesLive]; ok {
		t.Fatal("an unreadable baseline reported as 'nothing changed'")
	}
}

// TestPageProjectReviewRoutineStates — V06. `unknown` is a state of its own.
func TestPageProjectReviewRoutineStates(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	if _, err := h.db.Exec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-restart', ?, 'ops-restart', 'Restart', '{"steps":[]}', 'hash')`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-drain', ?, 'ops-drain', 'Drain', '{"steps":[1]}', 'hash2')`, ws); err != nil {
		t.Fatal(err)
	}
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// Three routines on the candidate, and a publication that recorded a
	// digest for exactly one of them.
	spec := reviewDraftWithRoutines(t, h, "health", "ops-restart", "ops-drain", "ops-vanished")
	if _, err := h.db.Exec(`UPDATE page_project_drafts SET spec_json=?`, spec); err != nil {
		t.Fatal(err)
	}
	stale := strings.Repeat("0", 64)
	checks, _ := json.Marshal(map[string]any{"routine_definitions": map[string]string{"ops-restart": stale, "ops-vanished": stale}})
	reviewSeedPublication(t, h, "health", string(checks))

	_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	for _, tc := range []struct {
		routine   string
		state     string
		published bool
		current   bool
	}{
		{"ops-restart", "changed", true, true},
		{"ops-drain", "unknown", false, true},
		{"ops-vanished", "unknown", true, false},
	} {
		row := reviewRoutineRow(t, snapshot, tc.routine)
		if row.State != tc.state {
			t.Errorf("%s: state = %q, want %q (%+v)", tc.routine, row.State, tc.state, row)
		}
		if (row.PublishedDigest != nil) != tc.published {
			t.Errorf("%s: published digest present = %v, want %v", tc.routine, row.PublishedDigest != nil, tc.published)
		}
		if (row.CurrentDigest != nil) != tc.current {
			t.Errorf("%s: current digest present = %v, want %v", tc.routine, row.CurrentDigest != nil, tc.current)
		}
	}
	// And the agreeing case, so `unchanged` is still reachable.
	agreed, _ := json.Marshal(map[string]any{"routine_definitions": map[string]string{"ops-restart": pageRoutineDigest(`{"steps":[]}`)}})
	if _, err := h.db.Exec(`UPDATE page_project_publications SET checks_json=? WHERE version=1`, string(agreed)); err != nil {
		t.Fatal(err)
	}
	_, snapshot = reviewCall(t, h, ws, user, "OWNER", "health")
	if row := reviewRoutineRow(t, snapshot, "ops-restart"); row.State != "unchanged" {
		t.Fatalf("identical digests: state = %q", row.State)
	}
}

// reviewDraftWithRoutines returns the Page's current draft definition with
// exactly one `call` action per named routine on its first panel. It replaces
// rather than appends, so successive saves declare the set the caller asked
// for and not the union with whatever the previous revision declared.
func reviewDraftWithRoutines(t *testing.T, h *PageHandler, slug string, routines ...string) string {
	t.Helper()
	var raw string
	if err := h.db.QueryRow(`SELECT d.spec_json FROM page_project_drafts d JOIN pages p ON p.id=d.page_id WHERE p.slug=?`, slug).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var doc pages.Document
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Spec.Panels) == 0 {
		t.Fatal("draft definition has no panel to attach actions to")
	}
	for i := range doc.Spec.Panels {
		kept := doc.Spec.Panels[i].Actions[:0]
		for _, action := range doc.Spec.Panels[i].Actions {
			if action.Kind != pages.ActionCall {
				kept = append(kept, action)
			}
		}
		doc.Spec.Panels[i].Actions = kept
	}
	for i, routine := range routines {
		doc.Spec.Panels[0].Actions = append(doc.Spec.Panels[0].Actions, pages.PanelAction{ID: fmt.Sprintf("act-%d", i), Kind: pages.ActionCall, Label: "Run", Routine: routine})
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// reviewSeedPublication records a publication with a chosen checks_json,
// against the draft's own build. Publishing through the handler cannot produce
// the gaps this test is about — a real publication records every routine it
// resolved — and the gaps are exactly what a retained record can have.
//
// It records the LIVE Page definition rather than the draft's, so the draft
// stays a candidate: a publication carrying the draft's own declaration and
// digest is what "already live" means, and then there is nothing to fence.
func reviewSeedPublication(t *testing.T, h *PageHandler, slug, checks string) {
	t.Helper()
	var page, digest, commit, spec string
	var revision int64
	if err := h.db.QueryRow(`SELECT p.id,d.revision,d.source_digest,d.git_commit,p.spec_json FROM page_project_drafts d JOIN pages p ON p.id=d.page_id WHERE p.slug=?`, slug).Scan(&page, &revision, &digest, &commit, &spec); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO page_project_builds(id,page_id,source_revision,source_digest,state,artifact_digest,created_at) VALUES('bld-review',?,?,?,'ready',?, '2026-09-10T00:00:00Z')`, page, revision, digest, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO page_project_publications(page_id,version,build_id,source_revision,source_digest,git_commit,artifact_digest,spec_json,checks_json,created_at) VALUES(?,1,'bld-review',?,?,?,?,?,?, '2026-09-10T00:00:00Z')`, page, revision, digest, commit, digest, spec, checks); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO page_project_live(page_id,version) VALUES(?,1)`, page); err != nil {
		t.Fatal(err)
	}
}

// TestPageProjectReviewAuthorization — one section's refusal must not hide the
// other three, and someone with no edit standing at all sees nothing.
func TestPageProjectReviewAuthorization(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('editor-user','editor@example.com','Editor')`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-editor', ?, 'editor-user', 'MEMBER')`, ws); err != nil {
		t.Fatal(err)
	}
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='health'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO page_grants (page_id, subject_type, subject_id, level, granted_by_user_id) VALUES (?, 'user', 'editor-user', 'write', ?)`, page, user); err != nil {
		t.Fatal(err)
	}

	w, snapshot := reviewCall(t, h, ws, "editor-user", "MEMBER", "health")
	if w.Code != 200 {
		t.Fatalf("write grantee refused the review: %d %s", w.Code, w.Body.String())
	}
	if !snapshot.Capabilities.MayEditSpec || snapshot.Capabilities.MayPublish {
		t.Fatalf("capabilities: %+v", snapshot.Capabilities)
	}
	if _, ok := reviewBlockers(snapshot)[reviewBlockerNotPermitted]; !ok {
		t.Fatalf("missing not_permitted: %+v", snapshot.Blockers)
	}
	// The refusal must not swallow the snapshot.
	if snapshot.Candidate == nil || snapshot.Candidate.Revision != 1 || snapshot.Baseline.DefinitionDigest == "" {
		t.Fatalf("not_permitted erased the rest of the review: %+v", snapshot)
	}

	if w, _ := reviewCall(t, h, ws, "stranger", "MEMBER", "health"); w.Code != 403 {
		t.Fatalf("stranger: %d", w.Code)
	}
	if w, _ := reviewCall(t, h, "other-workspace", user, "OWNER", "health"); w.Code != 404 {
		t.Fatalf("cross workspace: %d", w.Code)
	}
}

// TestPageProjectReviewStorageUnavailableStillServesTheSnapshot — a 503 here
// would hide the candidate, the baseline and the routines from a reviewer who
// can still read all three.
func TestPageProjectReviewStorageUnavailableStillServesTheSnapshot(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	h.pageRuntimeOrigin = ""
	w, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	if w.Code != 200 {
		t.Fatalf("review refused instead of blocking: %d %s", w.Code, w.Body.String())
	}
	if _, ok := reviewBlockers(snapshot)[reviewBlockerStorageUnavailable]; !ok {
		t.Fatalf("missing storage_unavailable: %+v", snapshot.Blockers)
	}
	if snapshot.Candidate == nil || snapshot.Baseline.DefinitionDigest == "" {
		t.Fatalf("storage_unavailable erased the rest of the review: %+v", snapshot)
	}
}

// TestPageProjectHistoryCarriesTheActorKind — the flattened `actor` string
// cannot say whether a person or a container saved a revision.
func TestPageProjectHistoryCarriesTheActorKind(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err := h.db.Exec(`INSERT INTO page_project_revisions(page_id,revision,source_digest,created_at,git_commit,spec_json,actor_json) SELECT page_id,2,source_digest,'2026-09-10T00:00:00Z',git_commit,spec_json,'{"workspace_id":"` + ws + `","crew_id":"crew-lookout","agent_id":"agent-writer"}' FROM page_project_revisions WHERE revision=1`); err != nil {
		t.Fatal(err)
	}
	r := pagesRequest(t, "GET", "/", ws, user, "OWNER", "")
	r.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	h.ProjectHistory(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var history struct {
		Revisions []pageProjectRevision `json:"revisions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil || len(history.Revisions) != 2 {
		t.Fatalf("history: %v %s", err, w.Body.String())
	}
	byRevision := map[int64]pageProjectRevision{}
	for _, row := range history.Revisions {
		byRevision[row.Revision] = row
	}
	if byRevision[1].ActorKind != "user" || byRevision[1].Actor != user {
		t.Fatalf("human revision: %+v", byRevision[1])
	}
	if byRevision[2].ActorKind != "agent" || byRevision[2].Actor != "agent-writer" {
		t.Fatalf("agent revision: %+v", byRevision[2])
	}
}

// TestPageProjectPublishFence — P0/V04. The publication is refused when either
// base the human reviewed moved, and it names which one.
func TestPageProjectPublishFence(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	if _, err := h.db.Exec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-restart', ?, 'ops-restart', 'Restart', '{"steps":[]}', 'hash')`, ws); err != nil {
		t.Fatal(err)
	}
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// Give the candidate a `call` action so the routine half of the fence has
	// something to fence on. It goes through the ordinary save path, which
	// revalidates the reference.
	definition := reviewDraftWithRoutines(t, h, "health", "ops-restart")
	var doc map[string]any
	if err := json.Unmarshal([]byte(definition), &doc); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"expected_revision": 1, "project": projectTestSource(), "definition": doc})
	r := pagesRequest(t, "PUT", "/", ws, user, "OWNER", string(body))
	r.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	h.PutProject(w, r)
	if w.Code != 200 {
		t.Fatalf("save draft with a call action: %d %s", w.Code, w.Body.String())
	}
	build := reviewBuildRevision(t, h, ws, user, 2)
	routineDigest := pageRoutineDigest(`{"steps":[]}`)
	zero, one := int64(0), int64(1)

	base := func() pageProjectPublishRequest {
		return pageProjectPublishRequest{BuildID: build, ExpectedRevision: 2, ExpectedPublication: &zero, ReviewedCode: true,
			ExpectedDefinitionDigest: reviewLiveDigest(t, h, "health"),
			ExpectedRoutineDigests:   map[string]string{"ops-restart": routineDigest}}
	}

	t.Run("malformed fence is a 400", func(t *testing.T) {
		for name, mutate := range map[string]func(*pageProjectPublishRequest){
			"missing definition digest": func(req *pageProjectPublishRequest) { req.ExpectedDefinitionDigest = "" },
			"short definition digest":   func(req *pageProjectPublishRequest) { req.ExpectedDefinitionDigest = "abc123" },
			"uppercase digest":          func(req *pageProjectPublishRequest) { req.ExpectedDefinitionDigest = strings.ToUpper(routineDigest) },
			"missing routine map":       func(req *pageProjectPublishRequest) { req.ExpectedRoutineDigests = nil },
			"garbage routine digest":    func(req *pageProjectPublishRequest) { req.ExpectedRoutineDigests["ops-restart"] = "not-a-digest" },
		} {
			req := base()
			mutate(&req)
			raw, _ := json.Marshal(req)
			r := pagesRequest(t, "POST", "/", ws, user, "OWNER", string(raw))
			r.SetPathValue("slug", "health")
			w := httptest.NewRecorder()
			h.PublishProject(w, r)
			if w.Code != 400 {
				t.Errorf("%s: status = %d, want 400 (%s)", name, w.Code, w.Body.String())
			}
		}
	})

	t.Run("routine moved since review", func(t *testing.T) {
		req := base()
		req.ExpectedRoutineDigests = map[string]string{"ops-restart": strings.Repeat("a", 64)}
		w := publishCall(t, h, ws, user, "OWNER", "health", req)
		if w.Code != 409 {
			t.Fatalf("status = %d, want 409 (%s)", w.Code, w.Body.String())
		}
		kind, routines := publishConflict(t, w)
		if kind != "routines" || len(routines) != 1 || routines[0] != "ops-restart" {
			t.Fatalf("conflict did not name the routine: %s", w.Body.String())
		}
	})

	t.Run("definition moved since review", func(t *testing.T) {
		req := base()
		req.ExpectedDefinitionDigest = strings.Repeat("b", 64)
		w := publishCall(t, h, ws, user, "OWNER", "health", req)
		if w.Code != 409 {
			t.Fatalf("status = %d, want 409 (%s)", w.Code, w.Body.String())
		}
		if kind, _ := publishConflict(t, w); kind != "definition" {
			t.Fatalf("conflict = %q, want definition (%s)", kind, w.Body.String())
		}
	})

	t.Run("correct fence publishes", func(t *testing.T) {
		if w := publishCall(t, h, ws, user, "OWNER", "health", base()); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		// An exact retry still returns its receipt: the fence guards what is
		// about to happen, not what already did.
		w := publishCall(t, h, ws, user, "OWNER", "health", base())
		if w.Code != 200 {
			t.Fatalf("idempotent retry: %d %s", w.Code, w.Body.String())
		}
		var receipt struct {
			Replayed bool `json:"replayed"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil || !receipt.Replayed {
			t.Fatalf("retry was not a replay: %s", w.Body.String())
		}
	})

	t.Run("another writer moves the live definition", func(t *testing.T) {
		reviewed := reviewLiveDigest(t, h, "health")
		if _, err := h.db.Exec(`UPDATE pages SET spec_json=json_set(spec_json,'$.metadata.description','moved') WHERE slug='health'`); err != nil {
			t.Fatal(err)
		}
		if reviewLiveDigest(t, h, "health") == reviewed {
			t.Fatal("the panel rollback did not change the live definition")
		}
		// The review snapshot now says so too.
		_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
		if _, ok := reviewBlockers(snapshot)[reviewBlockerDefinitionMoved]; !ok {
			t.Fatalf("review did not report the drifted definition: %+v", snapshot.Blockers)
		}
		rollback := pageProjectPublishRequest{RollbackVersion: 1, ExpectedPublication: &one, ReviewedCode: true,
			ExpectedDefinitionDigest: reviewed, ExpectedRoutineDigests: map[string]string{"ops-restart": routineDigest}}
		w := publishCall(t, h, ws, user, "OWNER", "health", rollback)
		if w.Code != 409 {
			t.Fatalf("rollback published over a moved definition: %d %s", w.Code, w.Body.String())
		}
		if kind, _ := publishConflict(t, w); kind != "definition" {
			t.Fatalf("rollback conflict = %q, want definition (%s)", kind, w.Body.String())
		}
		// The same rollback against the CURRENT definition is allowed.
		rollback.ExpectedDefinitionDigest = reviewLiveDigest(t, h, "health")
		if w := publishCall(t, h, ws, user, "OWNER", "health", rollback); w.Code != 200 {
			t.Fatalf("fenced rollback: %d %s", w.Code, w.Body.String())
		}
	})
}

// pageFenceForTest returns the two fence values a reviewer would have been
// shown for this Page right now: sha256 of the live definition, and the
// current digest of every routine its live definition calls. Shared with the
// pre-existing publication tests, which are now fenced like any other caller.
func pageFenceForTest(t *testing.T, h *PageHandler, ws, slug string) (string, map[string]string) {
	t.Helper()
	var spec string
	if err := h.db.QueryRow(`SELECT spec_json FROM pages WHERE workspace_id=? AND slug=?`, ws, slug).Scan(&spec); err != nil {
		t.Fatalf("read live definition: %v", err)
	}
	var doc pages.Document
	if err := json.Unmarshal([]byte(spec), &doc); err != nil {
		t.Fatalf("parse live definition: %v", err)
	}
	routines := map[string]string{}
	for _, panel := range doc.Spec.Panels {
		for _, action := range panel.Actions {
			if action.Kind != pages.ActionCall || routines[action.Routine] != "" {
				continue
			}
			var definition string
			if err := h.db.QueryRow(`SELECT definition_json FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL`, ws, action.Routine).Scan(&definition); err != nil {
				t.Fatalf("read routine %q: %v", action.Routine, err)
			}
			routines[action.Routine] = pageRoutineDigest(definition)
		}
	}
	sum := sha256.Sum256([]byte(spec))
	return hex.EncodeToString(sum[:]), routines
}

// TestPageProjectReviewRetainedPublicationFencesItsOwnRoutines — the History
// section's "Publish this version" control sends `rollback_version`, and the
// fence recomputes the routine map from THAT publication's archived spec. The
// draft's routines are a different document's routines, so without this mode
// the browser has no authorized way to learn the key set and every retained
// publish would 400.
func TestPageProjectReviewRetainedPublicationFencesItsOwnRoutines(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	for _, q := range []string{
		`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-restart', ?, 'ops-restart', 'Restart', '{"steps":[]}', 'h1')`,
		`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-drain', ?, 'ops-drain', 'Drain', '{"steps":[1]}', 'h2')`,
	} {
		if _, err := h.db.Exec(q, ws); err != nil {
			t.Fatal(err)
		}
	}
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// Revision 2 declares ops-restart, and is the version we later restore.
	reviewSaveDefinition(t, h, ws, user, "health", 1, projectTestSource(), reviewDraftWithRoutines(t, h, "health", "ops-restart"))
	restored := reviewBuildRevision(t, h, ws, user, 2)
	zero, one := int64(0), int64(1)
	if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{BuildID: restored, ExpectedRevision: 2, ExpectedPublication: &zero, ReviewedCode: true,
		ExpectedRoutineDigests: map[string]string{"ops-restart": pageRoutineDigest(`{"steps":[]}`)}}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// Revision 3 drops ops-restart and calls ops-drain instead, and becomes
	// publication 2. The draft's routines are now nothing like publication 1's.
	source := projectTestSource()
	source.Files[3].Content = "// third revision\n"
	reviewSaveDefinition(t, h, ws, user, "health", 2, source, reviewDraftWithRoutines(t, h, "health", "ops-drain"))
	current := reviewBuildRevision(t, h, ws, user, 3)
	if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{BuildID: current, ExpectedRevision: 3, ExpectedPublication: &one, ReviewedCode: true,
		ExpectedRoutineDigests: map[string]string{"ops-drain": pageRoutineDigest(`{"steps":[1]}`)}}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}

	t.Run("retained snapshot reports the retained routines", func(t *testing.T) {
		w, snapshot := reviewCallTarget(t, h, ws, user, "OWNER", "health", "/?publication=1")
		if w.Code != 200 {
			t.Fatalf("review: %d %s", w.Code, w.Body.String())
		}
		if snapshot.Candidate == nil || snapshot.Candidate.Revision != 2 {
			t.Fatalf("candidate is not publication 1's archived source: %+v", snapshot.Candidate)
		}
		if snapshot.Candidate.Build == nil || snapshot.Candidate.Build.ID != restored || snapshot.Candidate.Build.State != "ready" {
			t.Fatalf("candidate build: %+v", snapshot.Candidate.Build)
		}
		if snapshot.Candidate.Actor.Kind != "user" || snapshot.Candidate.Actor.ID != user {
			t.Fatalf("candidate actor is not the recorded publisher: %+v", snapshot.Candidate.Actor)
		}
		if snapshot.Candidate.CreatedAt == "" || snapshot.Candidate.GitCommit == "" || snapshot.Candidate.SourceDigest == "" {
			t.Fatalf("candidate identity incomplete: %+v", snapshot.Candidate)
		}
		if len(snapshot.Routines) != 1 || snapshot.Routines[0].Routine != "ops-restart" {
			t.Fatalf("routines are the draft's, not publication 1's: %+v", snapshot.Routines)
		}
		row := snapshot.Routines[0]
		if row.State != "unchanged" || row.PublishedDigest == nil || row.CurrentDigest == nil {
			t.Fatalf("retained routine: %+v", row)
		}
		// The baseline is the CURRENT live definition either way.
		if snapshot.Baseline.PublicationVersion != 2 || snapshot.Baseline.DefinitionDigest != reviewLiveDigest(t, h, "health") {
			t.Fatalf("baseline moved with the candidate: %+v", snapshot.Baseline)
		}
		// And the draft mode still answers about the draft.
		_, draft := reviewCall(t, h, ws, user, "OWNER", "health")
		if len(draft.Routines) != 1 || draft.Routines[0].Routine != "ops-drain" {
			t.Fatalf("draft snapshot changed: %+v", draft.Routines)
		}
	})

	t.Run("retained publish fenced on those values succeeds", func(t *testing.T) {
		_, snapshot := reviewCallTarget(t, h, ws, user, "OWNER", "health", "/?publication=1")
		two := int64(2)
		req := pageProjectPublishRequest{RollbackVersion: 1, ExpectedPublication: &two, ReviewedCode: true,
			ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest,
			ExpectedRoutineDigests:   reviewFenceFromSnapshot(t, snapshot)}
		if w := publishCall(t, h, ws, user, "OWNER", "health", req); w.Code != 200 {
			t.Fatalf("retained publish: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("retained publish after its routine changed is 409", func(t *testing.T) {
		_, snapshot := reviewCallTarget(t, h, ws, user, "OWNER", "health", "/?publication=1")
		fence := reviewFenceFromSnapshot(t, snapshot)
		if _, err := h.db.Exec(`UPDATE pipelines SET definition_json='{"steps":[9]}' WHERE id='pl-restart'`); err != nil {
			t.Fatal(err)
		}
		three := int64(3)
		req := pageProjectPublishRequest{RollbackVersion: 1, ExpectedPublication: &three, ReviewedCode: true,
			ExpectedDefinitionDigest: reviewLiveDigest(t, h, "health"), ExpectedRoutineDigests: fence}
		w := publishCall(t, h, ws, user, "OWNER", "health", req)
		if w.Code != 409 {
			t.Fatalf("stale routine published: %d %s", w.Code, w.Body.String())
		}
		kind, routines := publishConflict(t, w)
		if kind != "routines" || len(routines) != 1 || routines[0] != "ops-restart" {
			t.Fatalf("conflict did not name the retained routine: %s", w.Body.String())
		}
		// The snapshot now reports the same drift.
		_, after := reviewCallTarget(t, h, ws, user, "OWNER", "health", "/?publication=1")
		if row := reviewRoutineRow(t, after, "ops-restart"); row.State != "changed" {
			t.Fatalf("retained routine state = %q, want changed", row.State)
		}
	})

	t.Run("unknown and already-live publication numbers", func(t *testing.T) {
		_, missing := reviewCallTarget(t, h, ws, user, "OWNER", "health", "/?publication=99")
		if missing.Candidate != nil {
			t.Fatalf("unretained publication offered a candidate: %+v", missing.Candidate)
		}
		if message, ok := reviewBlockers(missing)[reviewBlockerNoCandidate]; !ok || !strings.Contains(message, "99") {
			t.Fatalf("unretained publication: %+v", missing.Blockers)
		}
		if missing.Baseline.DefinitionDigest == "" {
			t.Fatal("an unretained publication erased the baseline")
		}
		var live int64
		if err := h.db.QueryRow(`SELECT version FROM page_project_live`).Scan(&live); err != nil {
			t.Fatal(err)
		}
		_, current := reviewCallTarget(t, h, ws, user, "OWNER", "health", fmt.Sprintf("/?publication=%d", live))
		if message, ok := reviewBlockers(current)[reviewBlockerMatchesLive]; !ok || !strings.Contains(message, "already the live") {
			t.Fatalf("already-live publication: %+v", current.Blockers)
		}
		// It still names the version it is refusing.
		if current.Candidate == nil {
			t.Fatal("already-live publication erased its own candidate")
		}
		for _, bad := range []string{"/?publication=0", "/?publication=-1", "/?publication=latest"} {
			if w, _ := reviewCallTarget(t, h, ws, user, "OWNER", "health", bad); w.Code != 400 {
				t.Errorf("%s: status = %d, want 400", bad, w.Code)
			}
		}
	})

	t.Run("retained publication whose build is no longer ready", func(t *testing.T) {
		if _, err := h.db.Exec(`UPDATE page_project_builds SET state='interrupted',error='server restarted' WHERE id=?`, restored); err != nil {
			t.Fatal(err)
		}
		_, snapshot := reviewCallTarget(t, h, ws, user, "OWNER", "health", "/?publication=1")
		if message, ok := reviewBlockers(snapshot)[reviewBlockerBuildFailed]; !ok || !strings.Contains(message, "interrupted") {
			t.Fatalf("unpublishable retained build: %+v", snapshot.Blockers)
		}
		if snapshot.Candidate == nil || snapshot.Candidate.Build == nil || snapshot.Candidate.Build.State != "interrupted" {
			t.Fatalf("candidate build state: %+v", snapshot.Candidate)
		}
	})
}

// reviewFenceFromSnapshot builds expected_routine_digests the way a client
// must: from the rows the server flagged `in_candidate`, and no others.
func reviewFenceFromSnapshot(t *testing.T, snapshot reviewSnapshotWire) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, row := range snapshot.Routines {
		if !row.InCandidate {
			continue
		}
		if row.CurrentDigest == nil {
			t.Fatalf("routine %q has no current definition to fence on", row.Routine)
		}
		out[row.Routine] = *row.CurrentDigest
	}
	return out
}

// reviewSaveDefinition saves a source project and an explicit draft definition
// through the ordinary PUT path, which revalidates every reference.
func reviewSaveDefinition(t *testing.T, h *PageHandler, ws, user, slug string, expected int64, source *pages.SourceProject, definition string) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(definition), &doc); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"expected_revision": expected, "project": source, "definition": doc})
	r := pagesRequest(t, "PUT", "/", ws, user, "OWNER", string(body))
	r.SetPathValue("slug", slug)
	w := httptest.NewRecorder()
	h.PutProject(w, r)
	if w.Code != 200 {
		t.Fatalf("save draft definition: %d %s", w.Code, w.Body.String())
	}
}

// TestPageProjectReviewDroppedRoutineIsNotFenced — F1. A draft that removes a
// `call` action the live publication had still SHOWS that routine (a reviewer
// should see it being dropped) but must not offer it as a fence key: the
// server rebuilds the map from the candidate alone, so sending the dropped
// routine is a key it does not have, and the publication is refused naming a
// routine nobody moved. Refetching returns the same snapshot, so a client that
// cannot tell the two apart can never publish that draft.
//
// Against the pre-`in_candidate` code the fence built here carries two keys
// and the publish below returns 409.
func TestPageProjectReviewDroppedRoutineIsNotFenced(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	for _, q := range []string{
		`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-restart', ?, 'ops-restart', 'Restart', '{"steps":[]}', 'h1')`,
		`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-drain', ?, 'ops-drain', 'Drain', '{"steps":[1]}', 'h2')`,
	} {
		if _, err := h.db.Exec(q, ws); err != nil {
			t.Fatal(err)
		}
	}
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// Publication 1 calls both routines.
	reviewSaveDefinition(t, h, ws, user, "health", 1, projectTestSource(), reviewDraftWithRoutines(t, h, "health", "ops-restart", "ops-drain"))
	build := reviewBuildRevision(t, h, ws, user, 2)
	zero, one := int64(0), int64(1)
	if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{BuildID: build, ExpectedRevision: 2, ExpectedPublication: &zero, ReviewedCode: true,
		ExpectedRoutineDigests: map[string]string{"ops-restart": pageRoutineDigest(`{"steps":[]}`), "ops-drain": pageRoutineDigest(`{"steps":[1]}`)}}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// The draft drops ops-restart.
	source := projectTestSource()
	source.Files[3].Content = "// dropped the restart action\n"
	reviewSaveDefinition(t, h, ws, user, "health", 2, source, reviewDraftWithRoutines(t, h, "health", "ops-drain"))
	next := reviewBuildRevision(t, h, ws, user, 3)

	_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	dropped := reviewRoutineRow(t, snapshot, "ops-restart")
	if dropped.InCandidate {
		t.Fatalf("a routine the draft dropped is offered as a fence key: %+v", dropped)
	}
	if dropped.PublishedDigest == nil || dropped.CurrentDigest == nil || dropped.State != "unchanged" {
		t.Fatalf("the dropped routine stopped being visible to the reviewer: %+v", dropped)
	}
	kept := reviewRoutineRow(t, snapshot, "ops-drain")
	if !kept.InCandidate || kept.CurrentDigest == nil {
		t.Fatalf("the candidate's own routine is not in the fence: %+v", kept)
	}

	// The publication a browser would send: every in_candidate row, nothing else.
	fence := reviewFenceFromSnapshot(t, snapshot)
	if len(fence) != 1 {
		t.Fatalf("fence key set = %v, want only ops-drain", fence)
	}
	w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{BuildID: next, ExpectedRevision: 3, ExpectedPublication: &one, ReviewedCode: true,
		ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest, ExpectedRoutineDigests: fence})
	if w.Code != 200 {
		t.Fatalf("a draft that dropped a routine is unpublishable: %d %s", w.Code, w.Body.String())
	}
}

// TestPageProjectReviewUnresolvedRoutineBlocksPublication — F2. A candidate
// calling a soft-deleted routine used to report `unknown` with a ready build
// and no blocker, i.e. "publishing is fine", and publishing then failed 422.
func TestPageProjectReviewUnresolvedRoutineBlocksPublication(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	if _, err := h.db.Exec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-restart', ?, 'ops-restart', 'Restart', '{"steps":[]}', 'h1')`, ws); err != nil {
		t.Fatal(err)
	}
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	reviewSaveDefinition(t, h, ws, user, "health", 1, projectTestSource(), reviewDraftWithRoutines(t, h, "health", "ops-restart"))
	build := reviewBuildRevision(t, h, ws, user, 2)

	_, before := reviewCall(t, h, ws, user, "OWNER", "health")
	if _, ok := reviewBlockers(before)[reviewBlockerRoutineUnresolved]; ok {
		t.Fatalf("a resolvable routine reported as unresolved: %+v", before.Blockers)
	}
	if _, err := h.db.Exec(`UPDATE pipelines SET deleted_at='2026-09-10T00:00:00Z' WHERE id='pl-restart'`); err != nil {
		t.Fatal(err)
	}

	_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	row := reviewRoutineRow(t, snapshot, "ops-restart")
	if row.CurrentDigest != nil || row.State != "unknown" || !row.InCandidate {
		t.Fatalf("vanished routine: %+v", row)
	}
	message, ok := reviewBlockers(snapshot)[reviewBlockerRoutineUnresolved]
	if !ok {
		t.Fatalf("missing routine_unresolved: %+v", snapshot.Blockers)
	}
	if !strings.Contains(message, "ops-restart") {
		t.Fatalf("blocker does not name the routine: %q", message)
	}
	if snapshot.Candidate == nil || snapshot.Candidate.Build == nil || snapshot.Candidate.Build.State != "ready" {
		t.Fatalf("the build is still ready, and the snapshot must still say so: %+v", snapshot.Candidate)
	}
	// And publishing is in fact refused — the blocker was telling the truth —
	// with a sentence naming the routine and no driver text in it. The
	// reference check ahead of the digest pass is what answers here; the
	// digest pass answers when a routine vanishes after that check, and both
	// now name the routine instead of quoting the driver.
	zero := int64(0)
	w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{BuildID: build, ExpectedRevision: 2, ExpectedPublication: &zero, ReviewedCode: true})
	if w.Code < 400 || w.Code >= 500 {
		t.Fatalf("publish with a vanished routine: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body.Error, "sql:") || !strings.Contains(body.Error, "ops-restart") {
		t.Fatalf("refusal leaks driver text or omits the routine: %q", body.Error)
	}
}

// TestPageRoutineDigestsInReportsTheMissingRoutine — the digest pass must hand
// its caller the routine's NAME, not a wrapped driver error. `sql: no rows in
// result set` was reaching the wire through err.Error().
func TestPageRoutineDigestsInReportsTheMissingRoutine(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	if _, err := h.db.Exec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-restart', ?, 'ops-restart', 'Restart', '{"steps":[]}', 'h1')`, ws); err != nil {
		t.Fatal(err)
	}
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var doc pages.Document
	if err := json.Unmarshal([]byte(reviewDraftWithRoutines(t, h, "health", "ops-restart")), &doc); err != nil {
		t.Fatal(err)
	}
	digests, unresolved, err := pageRoutineDigestsIn(context.Background(), h.db, ws, &doc)
	if err != nil || unresolved != "" || digests["ops-restart"] != pageRoutineDigest(`{"steps":[]}`) {
		t.Fatalf("resolvable routine: %v %q %v", err, unresolved, digests)
	}
	if _, err := h.db.Exec(`UPDATE pipelines SET deleted_at='2026-09-10T00:00:00Z' WHERE id='pl-restart'`); err != nil {
		t.Fatal(err)
	}
	digests, unresolved, err = pageRoutineDigestsIn(context.Background(), h.db, ws, &doc)
	if err != nil {
		t.Fatalf("a vanished routine is a reviewable fact, not an error: %v", err)
	}
	if unresolved != "ops-restart" || digests != nil {
		t.Fatalf("unresolved = %q, digests = %v", unresolved, digests)
	}
	if message := pageUnresolvedRoutineMessage(unresolved); !strings.Contains(message, "ops-restart") || strings.Contains(message, "sql:") {
		t.Fatalf("message: %q", message)
	}
}

// TestPageProjectPublishStaleRevisionReportsTheDraft — F4. Publishing a
// revision the draft has moved past must answer `conflict:"draft"`. The
// routine map was built for a different document, so checking routines first
// answered `conflict:"routines"` naming routines nobody touched.
func TestPageProjectPublishStaleRevisionReportsTheDraft(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	if _, err := h.db.Exec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-restart', ?, 'ops-restart', 'Restart', '{"steps":[]}', 'h1')`, ws); err != nil {
		t.Fatal(err)
	}
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	reviewSaveDefinition(t, h, ws, user, "health", 1, projectTestSource(), reviewDraftWithRoutines(t, h, "health", "ops-restart"))
	stale := reviewBuildRevision(t, h, ws, user, 2)
	// The draft moves on, and its routines move with it.
	source := projectTestSource()
	source.Files[3].Content = "// the draft moved on\n"
	reviewSaveDefinition(t, h, ws, user, "health", 2, source, reviewDraftWithRoutines(t, h, "health"))
	reviewBuildRevision(t, h, ws, user, 3)

	// The caller still holds revision 2's build and the current snapshot's
	// (now empty) routine map.
	_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	zero := int64(0)
	w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{BuildID: stale, ExpectedRevision: 2, ExpectedPublication: &zero, ReviewedCode: true,
		ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest, ExpectedRoutineDigests: reviewFenceFromSnapshot(t, snapshot)})
	if w.Code != 409 {
		t.Fatalf("stale revision published: %d %s", w.Code, w.Body.String())
	}
	kind, routines := publishConflict(t, w)
	if kind != "draft" {
		t.Fatalf("conflict = %q with routines %v, want draft (%s)", kind, routines, w.Body.String())
	}
}

// TestPageProjectReviewWithdrawnPublicationIsNotAlreadyLive — a withdrawn
// publication means nothing is serving, so a draft identical to it is a real
// candidate and republishing it is the recovery path. Calling that
// `candidate_matches_live` nulls the candidate, empties the fence, and the
// republication is then refused as a routines conflict.
func TestPageProjectReviewWithdrawnPublicationIsNotAlreadyLive(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	if _, err := h.db.Exec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-restart', ?, 'ops-restart', 'Restart', '{"steps":[]}', 'h1')`, ws); err != nil {
		t.Fatal(err)
	}
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	reviewSaveDefinition(t, h, ws, user, "health", 1, projectTestSource(), reviewDraftWithRoutines(t, h, "health", "ops-restart"))
	build := reviewBuildRevision(t, h, ws, user, 2)
	zero, one := int64(0), int64(1)
	if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{BuildID: build, ExpectedRevision: 2, ExpectedPublication: &zero, ReviewedCode: true,
		ExpectedRoutineDigests: map[string]string{"ops-restart": pageRoutineDigest(`{"steps":[]}`)}}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// While it is running, the identical draft is correctly "already live".
	_, live := reviewCall(t, h, ws, user, "OWNER", "health")
	if _, ok := reviewBlockers(live)[reviewBlockerMatchesLive]; !ok || live.Candidate != nil {
		t.Fatalf("a live identical draft: %+v %+v", live.Candidate, live.Blockers)
	}

	body, _ := json.Marshal(map[string]any{"expected_publication": 1})
	r := pagesRequest(t, "POST", "/", ws, user, "OWNER", string(body))
	r.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	h.UnpublishProject(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}

	_, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
	if snapshot.Baseline.Published {
		t.Fatal("baseline still reports the withdrawn application as published")
	}
	if _, ok := reviewBlockers(snapshot)[reviewBlockerMatchesLive]; ok {
		t.Fatalf("a withdrawn publication reported as already live: %+v", snapshot.Blockers)
	}
	if snapshot.Candidate == nil || snapshot.Candidate.Revision != 2 {
		t.Fatalf("no candidate to republish after withdrawal: %+v", snapshot.Candidate)
	}
	row := reviewRoutineRow(t, snapshot, "ops-restart")
	if !row.InCandidate {
		t.Fatalf("the candidate's routine is missing from the fence: %+v", row)
	}
	fence := reviewFenceFromSnapshot(t, snapshot)
	if len(fence) != 1 {
		t.Fatalf("fence = %v, want ops-restart", fence)
	}
	if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{BuildID: build, ExpectedRevision: 2, ExpectedPublication: &one, ReviewedCode: true,
		ExpectedDefinitionDigest: snapshot.Baseline.DefinitionDigest, ExpectedRoutineDigests: fence}); w.Code != 200 {
		t.Fatalf("republication after withdrawal: %d %s", w.Code, w.Body.String())
	}
}

// TestPageWireHasProjectIsNotHasApplication — `has_application` means "a
// publication is running". A Page whose application source has never shipped
// reports it false, which is exactly the first-publication case the review
// screen exists for, so a client keying off it alone never opens the review.
// `has_project` answers the other question, on both the detail and the list.
func TestPageWireHasProjectIsNotHasApplication(t *testing.T) {
	h, _, ws, user := reviewFixture(t)
	pagesCreate(t, h, ws, user, "plain")

	detail := func(slug string) (bool, bool) {
		t.Helper()
		doc := pagesGet(t, h, ws, user, "OWNER", slug)
		project, okProject := doc["has_project"].(bool)
		application, okApplication := doc["has_application"].(bool)
		if !okProject || !okApplication {
			t.Fatalf("%s: has_project/has_application missing from the page wire: %v", slug, doc)
		}
		return project, application
	}
	listed := func(slug string) (bool, bool) {
		t.Helper()
		r := pagesRequest(t, "GET", "/api/v1/pages", ws, user, "OWNER", "")
		w := httptest.NewRecorder()
		h.List(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var rows []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatalf("list is not JSON: %v — %s", err, w.Body.String())
		}
		for _, row := range rows {
			if row["slug"] != slug {
				continue
			}
			project, okProject := row["has_project"].(bool)
			application, okApplication := row["has_application"].(bool)
			if !okProject || !okApplication {
				t.Fatalf("%s: has_project/has_application missing from the list row: %v", slug, row)
			}
			return project, application
		}
		t.Fatalf("%s missing from the list", slug)
		return false, false
	}

	for _, stage := range []struct {
		name                     string
		slug                     string
		wantProject, wantAppLive bool
	}{
		{"no source at all", "plain", false, false},
		{"draft awaiting its first publication", "health", true, false},
	} {
		if stage.slug == "health" {
			if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
		}
		project, application := detail(stage.slug)
		if project != stage.wantProject || application != stage.wantAppLive {
			t.Errorf("%s: detail has_project=%v has_application=%v, want %v/%v", stage.name, project, application, stage.wantProject, stage.wantAppLive)
		}
		if listProject, listApplication := listed(stage.slug); listProject != project || listApplication != application {
			t.Errorf("%s: list says %v/%v, detail says %v/%v", stage.name, listProject, listApplication, project, application)
		}
	}

	// Published: both true.
	build := reviewBuildRevision(t, h, ws, user, 1)
	zero := int64(0)
	if w := publishCall(t, h, ws, user, "OWNER", "health", pageProjectPublishRequest{BuildID: build, ExpectedRevision: 1, ExpectedPublication: &zero, ReviewedCode: true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if project, application := detail("health"); !project || !application {
		t.Errorf("published: has_project=%v has_application=%v, want both true", project, application)
	}
	if project, application := listed("health"); !project || !application {
		t.Errorf("published, listed: has_project=%v has_application=%v, want both true", project, application)
	}

	// Withdrawn: the source is still there, the application is not running.
	body, _ := json.Marshal(map[string]any{"expected_publication": 1})
	r := pagesRequest(t, "POST", "/", ws, user, "OWNER", string(body))
	r.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	h.UnpublishProject(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if project, application := detail("health"); !project || application {
		t.Errorf("withdrawn: has_project=%v has_application=%v, want true/false", project, application)
	}
	if project, application := listed("health"); !project || application {
		t.Errorf("withdrawn, listed: has_project=%v has_application=%v, want true/false", project, application)
	}
}
