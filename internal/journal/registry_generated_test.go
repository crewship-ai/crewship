package journal

import (
	"sort"
	"testing"

	"github.com/crewship-ai/crewship/internal/journalgen"
)

// TestAllEntryTypesMatchesSource re-scans types.go with the exact logic
// cmd/gen-journal-registry uses and asserts AllEntryTypes is precisely that
// scan's output. It is the enforcement half of A3's closed registry
// (PRD-ISSUES-AND-ROUTINES-2026 §17): the generator makes drift unlikely,
// this test makes it a build failure instead of a silent gap.
//
// Concretely: this FAILS whenever someone adds (or renames, or removes) an
// EntryType constant in types.go and does not re-run
// `go generate ./internal/journal/...` before committing —
// registry_generated.go is then stale, and this is the check that says so
// instead of the mismatch surviving all the way to an automation that saves
// but never fires.
func TestAllEntryTypesMatchesSource(t *testing.T) {
	scanned, err := journalgen.Scan("types.go")
	if err != nil {
		t.Fatalf("scan types.go: %v", err)
	}
	if len(scanned) == 0 {
		t.Fatal("scanned zero EntryType constants from types.go — the scanner is broken, not the journal")
	}

	wantValues := make([]string, 0, len(scanned))
	for _, c := range scanned {
		wantValues = append(wantValues, c.Value)
	}
	sort.Strings(wantValues)

	gotValues := make([]string, 0, len(AllEntryTypes))
	for _, t := range AllEntryTypes {
		gotValues = append(gotValues, string(t))
	}
	sort.Strings(gotValues)

	if len(wantValues) != len(gotValues) {
		t.Fatalf("types.go declares %d EntryType constants; registry_generated.go's AllEntryTypes has %d — "+
			"run `go generate ./internal/journal/...` and commit the result", len(wantValues), len(gotValues))
	}

	missing := diff(wantValues, gotValues) // in source, not in registry
	extra := diff(gotValues, wantValues)   // in registry, not in source
	if len(missing) > 0 {
		t.Errorf("EntryType values declared in types.go but absent from AllEntryTypes (registry is stale): %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("EntryType values in AllEntryTypes but no longer declared in types.go (registry is stale): %v", extra)
	}
	if len(missing) > 0 || len(extra) > 0 {
		t.Error("run `go generate ./internal/journal/...` (or `go run ./cmd/gen-journal-registry` " +
			"from the repo root) and commit registry_generated.go")
	}
}

// diff returns the elements of a not present in b. Both must already be
// sorted; a and b may contain duplicate values (types.go does not, but the
// helper does not need to assume that to be correct).
func diff(a, b []string) []string {
	bSet := make(map[string]int, len(b))
	for _, v := range b {
		bSet[v]++
	}
	var out []string
	for _, v := range a {
		if bSet[v] > 0 {
			bSet[v]--
			continue
		}
		out = append(out, v)
	}
	return out
}

// TestRegistered pins Registered's contract directly: every declared
// constant is registered, and an unrelated, well-shaped string is not.
func TestRegistered(t *testing.T) {
	if len(AllEntryTypes) == 0 {
		t.Fatal("AllEntryTypes is empty")
	}
	for _, et := range AllEntryTypes {
		if !Registered(et) {
			t.Errorf("Registered(%q) = false, want true (it is in AllEntryTypes)", et)
		}
	}
	for _, bogus := range []EntryType{
		"mission.no_such_thing",
		"totally.made_up",
		"",
	} {
		if Registered(bogus) {
			t.Errorf("Registered(%q) = true, want false", bogus)
		}
	}
}
