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
