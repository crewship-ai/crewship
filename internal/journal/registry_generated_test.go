package journal

import (
	"sort"
	"testing"

	"github.com/crewship-ai/crewship/internal/journalgen"
)

// TestAllEntryTypesMatchesSource re-scans the whole module with the exact
// logic cmd/gen-journal-registry uses and asserts AllEntryTypes is precisely
// that scan's output. It is the enforcement half of A3's closed registry
// (PRD-ISSUES-AND-ROUTINES-2026 §17): the generator makes drift unlikely,
// this test makes it a build failure instead of a silent gap.
//
// This scans internal/ and cmd/ in full, not just types.go — A3's first cut
// only re-scanned types.go, which meant an ad hoc `journal.EntryType("…")`
// declared anywhere else (internal/api/pages_public_tokens.go,
// internal/harbormaster/reward.go, and nine more — see journalgen.ScanTree's
// doc comment) could drift from the registry with NO test catching it,
// because the drift test and the thing it was checking against shared the
// same blind spot. That is the drift that matters for automations: a rule on
// one of those types looked accepted and could never fire. This test would
// have failed on every one of them before this fix.
//
// Concretely: this FAILS whenever someone adds, renames, or removes a
// journal.EntryType value anywhere under internal/ or cmd/ (in types.go or
// ad hoc) and does not re-run `go generate ./internal/journal/...` before
// committing — registry_generated.go is then stale, and this is the check
// that says so instead of the mismatch surviving all the way to an
// automation that saves but never fires.
func TestAllEntryTypesMatchesSource(t *testing.T) {
	root, err := journalgen.RepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	scanned, err := journalgen.ScanTree(root, "internal", "cmd")
	if err != nil {
		t.Fatalf("scan internal/ and cmd/: %v", err)
	}
	if len(scanned) == 0 {
		t.Fatal("scanned zero EntryType values from internal/ and cmd/ — the scanner is broken, not the journal")
	}

	// AllEntryTypes' own doc comment promises "sorted by value" — assert
	// that directly, on its own order, rather than only ever comparing
	// against a re-sorted copy. Re-sorting before every comparison would
	// make a real ordering bug in the generated file invisible to this test.
	gotValues := make([]string, 0, len(AllEntryTypes))
	for _, et := range AllEntryTypes {
		gotValues = append(gotValues, string(et))
	}
	if !sort.StringsAreSorted(gotValues) {
		t.Error("AllEntryTypes is not sorted by value — its doc comment promises it is; " +
			"run `go generate ./internal/journal/...` and commit the result")
	}

	wantValues := make([]string, 0, len(scanned))
	for _, c := range scanned {
		wantValues = append(wantValues, c.Value)
	}
	sortedWant := append([]string(nil), wantValues...)
	sortedGot := append([]string(nil), gotValues...)
	sort.Strings(sortedWant)
	sort.Strings(sortedGot)

	if len(sortedWant) != len(sortedGot) {
		t.Fatalf("the module declares/uses %d EntryType values; registry_generated.go's AllEntryTypes has %d — "+
			"run `go generate ./internal/journal/...` and commit the result", len(sortedWant), len(sortedGot))
	}

	missing := diff(sortedWant, sortedGot) // in source, not in registry
	extra := diff(sortedGot, sortedWant)   // in registry, not in source
	if len(missing) > 0 {
		t.Errorf("EntryType values used in the module but absent from AllEntryTypes (registry is stale): %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("EntryType values in AllEntryTypes but no longer used anywhere in the module (registry is stale): %v", extra)
	}
	if len(missing) > 0 || len(extra) > 0 {
		t.Error("run `go generate ./internal/journal/...` (or `go run ./cmd/gen-journal-registry` " +
			"from the repo root) and commit registry_generated.go")
	}
}

// diff returns the elements of a not present in b. Both must already be
// sorted; a and b may contain duplicate values (the source scan does not
// produce any after its own dedupe, but the helper does not need to assume
// that to be correct).
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

// TestAllEntryTypesCoversAdHocDeclarations pins, by name, that the ad hoc
// entry types found during the A3 registry-completeness fix are actually in
// the registry — a regression test more specific than the general drift
// scan above, so a future refactor that happens to keep the scan passing
// (e.g. by deleting one of these emit sites' *string value*, not just
// moving it) still has a named test calling out exactly which type broke.
func TestAllEntryTypesCoversAdHocDeclarations(t *testing.T) {
	adHoc := []EntryType{
		"keeper.rule_auto_tuned",
		"page.link_revoked",
		"page.public_view",
		"page.published",
		"page.webhook_issued",
		"page.webhook_revoked",
		"queue.sweeper_pumped",
		"page.owner_transferred",
		"onboarding.proposal_applied",
		"policy.changed",
		"approval.auto_tuning_reset",
	}
	for _, et := range adHoc {
		if !Registered(et) {
			t.Errorf("Registered(%q) = false, want true — this type is declared and emitted outside types.go; "+
				"see internal/journalgen.ScanTree", et)
		}
	}
}
