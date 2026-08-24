package api

// The delegated-authorship gate (internal_delegated_crew.go), tested at the
// helper rather than through a handler: every rule here is about WHICH crew
// ends up owning the write, and driving that through save_routine's DSL
// validation or save_page's panel resolution would bury the one assertion
// that matters under a fixture for the other subsystem.
//
// The handler-level proof that these rules are actually wired lives in
// pipelines_crud_delegated_test.go and pages_internal_save_test.go.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

// seedCrewRowKind is seedCrewRow with the crews.kind column set — the column
// the whole gate turns on, and one seedCrewRow leaves at its default.
func seedCrewRowKind(t *testing.T, db *sql.DB, id, wsID, name, slug, kind string) string {
	t.Helper()
	_, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, kind, network_mode, container_memory_mb, container_cpus)
		VALUES (?, ?, ?, ?, ?, 'free', 4096, 2.0)`, id, wsID, name, slug, kind)
	if err != nil {
		t.Fatalf("seed crew %s (kind=%s): %v", id, kind, err)
	}
	return id
}

type delegateFixture struct {
	db      *sql.DB
	wsID    string
	setupID string
	realID  string
}

func newDelegateFixture(t *testing.T) delegateFixture {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return delegateFixture{
		db:      db,
		wsID:    wsID,
		setupID: seedCrewRowKind(t, db, "c-setup", wsID, "Crewship Guide", "_crewship-setup", setupCrewKindSetup),
		realID:  seedCrewRowKind(t, db, "c-real", wsID, "Uptime Watch", "uptime-watch", setupCrewKindStandard),
	}
}

// run drives the helper the way a handler does: authorCrewID already holds
// the caller's own crew, as assertBoundCrewWorkspaceDB leaves it.
func (f delegateFixture) run(t *testing.T, callerCrewID, targetSlug string) (*httptest.ResponseRecorder, string, bool) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", nil)
	rr := httptest.NewRecorder()
	author := callerCrewID
	ok := resolveDelegatedAuthorCrew(rr, req, f.db, newTestLogger(), f.wsID, targetSlug, &author)
	return rr, author, ok
}

// The bug this gate exists for. A routine the Guide built for "Uptime Watch"
// used to be owned by _crewship-setup, and author_crew_id is what the egress
// gate, the container resolver and the credential resolver all read.
func TestDelegatedCrew_SetupCrewAuthorsForTheNamedCrew(t *testing.T) {
	f := newDelegateFixture(t)
	rr, author, ok := f.run(t, f.setupID, "uptime-watch")
	if !ok {
		t.Fatalf("delegation refused: %d %s", rr.Code, rr.Body.String())
	}
	if author != f.realID {
		t.Errorf("author crew = %q, want %q (the named crew, not the caller)", author, f.realID)
	}
}

// The other half of the trade. Allowing the Guide to name a crew is only a
// fix if keeping the work is also impossible — otherwise it is an option the
// model forgets to take and the orphans come back.
func TestDelegatedCrew_SetupCrewCannotOwnAnything(t *testing.T) {
	f := newDelegateFixture(t)
	rr, _, ok := f.run(t, f.setupID, "")
	if ok {
		t.Fatal("the setup crew was allowed to own the write")
	}
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rr.Code)
	}
	// The message is the repair instruction the model reads out of its tool
	// result — if it stops naming the field, the model cannot self-correct
	// and every onboarding needs a human to notice.
	if body := rr.Body.String(); !contains(body, "target_crew_slug") {
		t.Errorf("error does not name the field to set: %s", body)
	}
}

// Naming the Guide explicitly is the same orphan by a longer route.
func TestDelegatedCrew_SetupCrewCannotNameItself(t *testing.T) {
	f := newDelegateFixture(t)
	rr, _, ok := f.run(t, f.setupID, "_crewship-setup")
	if ok {
		t.Fatal("the setup crew delegated to itself")
	}
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rr.Code)
	}
}

// The original cross-crew gate, unchanged. This is the property the sidecar's
// trust-model note promises ("Crew B's agent can never claim crew_a") and the
// narrow setup exception must not have widened it.
func TestDelegatedCrew_OrdinaryCrewMayNotNameAnother(t *testing.T) {
	f := newDelegateFixture(t)
	other := seedCrewRowKind(t, f.db, "c-other", f.wsID, "Other", "other-crew", setupCrewKindStandard)
	rr, author, ok := f.run(t, other, "uptime-watch")
	if ok {
		t.Fatal("an ordinary crew was allowed to author for another crew")
	}
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
	if author != other {
		t.Errorf("author crew was moved to %q on a refused request", author)
	}
}

// An ordinary crew authoring for ITSELF is the overwhelmingly common case and
// must stay untouched — it names no target and is not a setup crew.
func TestDelegatedCrew_OrdinaryCrewKeepsItsOwnWrites(t *testing.T) {
	f := newDelegateFixture(t)
	rr, author, ok := f.run(t, f.realID, "")
	if !ok {
		t.Fatalf("an ordinary self-authored write was refused: %d %s", rr.Code, rr.Body.String())
	}
	if author != f.realID {
		t.Errorf("author crew = %q, want it left at %q", author, f.realID)
	}
}

// The ordering rule is enforced by the slug simply not resolving, rather
// than by the prompt remembering — you cannot name a crew that does not
// exist. 422 and not 403: this is a request the agent can fix and retry, not
// a privilege it lacks, and the two statuses lead a model down different
// paths.
func TestDelegatedCrew_UnknownTargetIsRetryableNotForbidden(t *testing.T) {
	f := newDelegateFixture(t)
	rr, author, ok := f.run(t, f.setupID, "not-created-yet")
	if ok {
		t.Fatal("delegation to a nonexistent crew succeeded")
	}
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rr.Code)
	}
	if author != f.setupID {
		t.Errorf("author crew was moved to %q on a refused request", author)
	}
	// What to do next has to be IN the message — see
	// TestDelegatedCrew_UnknownSlugListsTheRealOnes and
	// TestDelegatedCrew_NoCrewsYetSaysSoRatherThanListingNothing for the two
	// branches it takes.
	if body := rr.Body.String(); !contains(body, "uptime-watch") {
		t.Errorf("error leaves the agent with no next move: %s", body)
	}
}

// Slugs are unique per workspace, not globally. The lookup is workspace-
// scoped so a setup crew cannot reach a same-named crew in another tenant.
func TestDelegatedCrew_TargetLookupIsWorkspaceScoped(t *testing.T) {
	f := newDelegateFixture(t)
	// Inserted directly rather than via seedTestWorkspace, which hardcodes
	// both the workspace id and the membership id and so cannot run twice.
	const otherWS = "ws-foreign"
	if _, err := f.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign')`, otherWS); err != nil {
		t.Fatalf("insert second workspace: %v", err)
	}
	seedCrewRowKind(t, f.db, "c-foreign", otherWS, "Foreign", "foreign-crew", setupCrewKindStandard)

	rr, author, ok := f.run(t, f.setupID, "foreign-crew")
	if ok {
		t.Fatal("resolved a crew slug from another workspace")
	}
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rr.Code)
	}
	if author == "c-foreign" {
		t.Error("author crew was pointed at a foreign tenant's crew")
	}
}

// A soft-deleted crew is not a home for new work.
func TestDelegatedCrew_DeletedTargetIsNotResolvable(t *testing.T) {
	f := newDelegateFixture(t)
	if _, err := f.db.Exec(`UPDATE crews SET deleted_at = '2026-01-01T00:00:00Z' WHERE id = ?`, f.realID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if _, _, ok := f.run(t, f.setupID, "uptime-watch"); ok {
		t.Fatal("delegated to a soft-deleted crew")
	}
}

// A crew's slug is DERIVED from its name server-side. The Guide proposes
// "Hlídač dostupnosti" and is never told the slug became
// "hlidac-dostupnosti", so the likeliest failure is not a missing crew but a
// guessed slug. A bare "no such crew" sends the model hunting for something
// that is sitting right there; the list makes the retry correct.
func TestDelegatedCrew_UnknownSlugListsTheRealOnes(t *testing.T) {
	f := newDelegateFixture(t)
	seedCrewRowKind(t, f.db, "c-second", f.wsID, "News", "sberac-novinek", setupCrewKindStandard)

	rr, _, ok := f.run(t, f.setupID, "hlidac-dostupnost")
	if ok {
		t.Fatal("a misspelled slug resolved")
	}
	body := rr.Body.String()
	for _, want := range []string{"uptime-watch", "sberac-novinek"} {
		if !contains(body, want) {
			t.Errorf("error does not offer %q as a candidate: %s", want, body)
		}
	}
	// The Guide's own crew is never a candidate — suggesting it would be
	// suggesting the very orphan this gate exists to prevent.
	if contains(body, "_crewship-setup") {
		t.Errorf("the setup crew was offered as a delegation target: %s", body)
	}
}

// The genuinely-empty case has to read differently, because the right next
// move is different: propose a crew, do not retry with another slug.
func TestDelegatedCrew_NoCrewsYetSaysSoRatherThanListingNothing(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	setupID := seedCrewRowKind(t, db, "c-setup", wsID, "Guide", "_crewship-setup", setupCrewKindSetup)
	f := delegateFixture{db: db, wsID: wsID, setupID: setupID}

	rr, _, ok := f.run(t, setupID, "anything")
	if ok {
		t.Fatal("delegation succeeded with no crews in the workspace")
	}
	if !contains(rr.Body.String(), "no crews yet") {
		t.Errorf("empty workspace should say so plainly: %s", rr.Body.String())
	}
}
