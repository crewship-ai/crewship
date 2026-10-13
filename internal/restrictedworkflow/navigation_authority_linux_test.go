//go:build linux

package restrictedworkflow

import (
	"errors"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestNavigationAuthoritySurvivesMissingExecutorButNotRevocation(t *testing.T) {
	s, _ := graphFixture(t)
	expected, err := s.Catalog(t.Context(), "h1", "w")
	if err != nil || len(expected) == 0 {
		t.Fatalf("positive catalog=%v %v", expected, err)
	}
	s.executor = nil
	if _, err = s.Catalog(t.Context(), "h1", "w"); !errors.Is(err, ErrDenied) {
		t.Fatalf("execution catalog unexpectedly ready: %v", err)
	}
	actual, err := AuthorizedCatalog(t.Context(), s.db, "h1", "w")
	if err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("runtime outage changed navigation authority: %v %v", actual, err)
	}
	store := access.Store{DB: s.db}
	member, err := store.Membership(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), "owner", "h1", "w", "restricted", member, nil); err != nil {
		t.Fatal(err)
	}
	actual, err = AuthorizedCatalog(t.Context(), s.db, "h1", "w")
	if err != nil || len(actual) != 0 {
		t.Fatalf("revocation retained navigation: %v %v", actual, err)
	}
}

func TestPageNavigationAuthoritySurvivesMissingExecutorButNotRevocation(t *testing.T) {
	s, _ := fixture(t)
	action := seedPage(t, s)
	expected, err := s.PageActionFingerprint(t.Context(), "h1", "w", action)
	if err != nil || expected == "" {
		t.Fatalf("positive Page=%q %v", expected, err)
	}
	s.executor = nil
	actual, err := AuthorizedPageActionFingerprint(t.Context(), s.db, "h1", "w", action)
	if err != nil || actual != expected {
		t.Fatalf("runtime outage changed Page authority: %q %v", actual, err)
	}
	store := access.Store{DB: s.db}
	member, err := store.Membership(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), "owner", "h1", "w", "restricted", member, nil); err != nil {
		t.Fatal(err)
	}
	if actual, err = AuthorizedPageActionFingerprint(t.Context(), s.db, "h1", "w", action); actual != "" || err == nil {
		t.Fatalf("revocation retained Page authority: %q %v", actual, err)
	}
}

func TestNavigationZeroRunFastPathIsOnlyNecessaryAuthority(t *testing.T) {
	s, _ := fixture(t)
	store := access.Store{DB: s.db}
	check := func(want bool) {
		t.Helper()
		got, err := HasAnyWorkflowRunAuthority(t.Context(), s.db, "h1", "w")
		if err != nil || got != want {
			t.Fatalf("necessary run authority=%t %v;want%t", got, err, want)
		}
	}
	replace := func(rights []access.Right) {
		t.Helper()
		m, err := store.Membership(t.Context(), "h1", "w")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), "owner", "h1", "w", "restricted", m, rights); err != nil {
			t.Fatal(err)
		}
	}
	check(true)
	replace(nil)
	check(false) // h2's independent run grant cannot qualify h1.
	replace([]access.Right{{Kind: "agent", ID: "agent", Operation: "discover"}})
	check(false)
	replace([]access.Right{{Kind: "agent", ID: "agent", Operation: "run"}})
	if _, err := s.db.Exec(`UPDATE agents SET deleted_at=datetime('now') WHERE id='agent'`); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := s.db.Exec(`UPDATE agents SET deleted_at=NULL WHERE id='agent'`); err != nil {
		t.Fatal(err)
	}
	check(false) // Undeleting does not resurrect grants removed by the scope trigger.
	replace([]access.Right{{Kind: "agent", ID: "agent", Operation: "run"}})
	check(true)
	// A valid run grant to another local agent satisfies only the cheap filter;
	// it must never authorize a recipe using the original ungranted agent.
	if _, err := s.db.Exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES('different','w','crew','Different','different','AGENT')`); err != nil {
		t.Fatal(err)
	}
	replace([]access.Right{{Kind: "agent", ID: "different", Operation: "run"}})
	check(true)
	if got, err := HasAuthorizedRoutine(t.Context(), s.db, "h1", "w", nil); err != nil || got {
		t.Fatalf("necessary filter bypassed actual graph: %t %v", got, err)
	}
	// Malformed cross-workspace grant rows also cannot satisfy the filter.
	if _, err := s.db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('foreign','Foreign','foreign')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE agents SET workspace_id='foreign' WHERE id='different'`); err != nil {
		t.Fatal(err)
	}
	// The API refuses this malformed binding; seed it directly to prove the
	// filter itself still binds the agent to the membership's workspace.
	if _, err := s.db.Exec(`INSERT INTO access_grants(id,member_id,resource_kind,agent_id,operation,created_by,created_at) VALUES('foreign-malformed','m1','agent','different','run','owner',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	check(false)
}
