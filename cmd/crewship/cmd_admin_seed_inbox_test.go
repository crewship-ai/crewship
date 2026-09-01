package main

import (
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
)

// The point of the command is "one row of every kind" — a reviewer looking at
// the inbox surface has to be able to see each one render. A kind added to
// inbox.AllKinds without a seed row here means the next person reviewing the
// inbox never sees it, and concludes it works.
func TestSeedInboxRows_CoverEveryInboxKind(t *testing.T) {
	seen := map[string]int{}
	for _, row := range seedInboxRows(time.Now()) {
		seen[row.kind]++
	}
	for _, kind := range inbox.AllKinds {
		if seen[kind] == 0 {
			t.Errorf("no seed row for kind %q — add one to seedInboxRows", kind)
		}
	}
	for kind := range seen {
		found := false
		for _, k := range inbox.AllKinds {
			if k == kind {
				found = true
			}
		}
		if !found {
			t.Errorf("seed row uses kind %q, which the inbox_items CHECK constraint rejects", kind)
		}
	}
}

// History is only worth looking at if it holds both shapes: a real decision
// and archived noise. That distinction is what the surface now splits on.
func TestSeedInboxRows_GiveHistoryBothADecisionAndArchivedNoise(t *testing.T) {
	var decided, archived int
	for _, row := range seedInboxRows(time.Now()) {
		switch row.resolve {
		case "":
		case "archived", "dismissed":
			archived++
		default:
			decided++
		}
	}
	if decided == 0 {
		t.Error("no seeded row resolves to a decision — History would show no receipts")
	}
	if archived == 0 {
		t.Error("no seeded row is archived — the decisions/archived split would never be exercised")
	}
}

// A blocking row with a deadline is what "Needs action" is for, and the
// deadline facet has nothing to bucket without one.
func TestSeedInboxRows_IncludeADeadlineAndADestructiveGate(t *testing.T) {
	var withDeadline, destructive int
	for _, row := range seedInboxRows(time.Now()) {
		if row.payload["timeout_at"] != nil {
			withDeadline++
		}
		if row.payload["risk_level"] == "destructive" {
			destructive++
		}
	}
	if withDeadline == 0 {
		t.Error("no seeded row carries timeout_at — the deadline facet has nothing to bucket")
	}
	if destructive == 0 {
		t.Error("no seeded row is destructive — the loudest row the inbox renders is never seen")
	}
}

// Two invocations in the same second must not mint the same identifiers.
// `time.Now().Unix()` did: the second run then failed on the pipeline_runs
// primary key, or — with no pipeline to hang the run on — got as far as
// re-using every source id, where inbox.Insert dedupes silently while the
// success line still claims sixteen rows were written. Seeding twice to get
// more variety is the obvious thing to try, so it has to work.
//
// Two FIXED instants inside one second, not two calls to time.Now(). The
// first draft did the latter and asserted they differed, which tests that the
// host's clock ticks between two adjacent statements rather than anything
// about this function — a bet on timer granularity that has no reason to hold
// on every runner, and it does not: it went red on macos-arm64 while passing
// everywhere else. What the command actually needs is sub-second resolution,
// and that is what this states.
func TestSeedSuffix_SeparatesTwoInstantsInsideOneSecond(t *testing.T) {
	within := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	later := within.Add(time.Millisecond)
	if within.Unix() != later.Unix() {
		t.Fatalf("test is wrong: %v and %v are not in the same second", within, later)
	}

	if first, second := seedSuffix(within), seedSuffix(later); first == second {
		t.Errorf("seedSuffix(%v) and seedSuffix(%v) both = %q; two seed runs in "+
			"one second collide", within, later, first)
	}
}

// …and the same instant always mints the same suffix, so the run id and the
// source ids written by one invocation agree with each other.
func TestSeedSuffix_IsStableForOneInstant(t *testing.T) {
	at := time.Date(2026, 8, 31, 12, 0, 0, 123456789, time.UTC)

	if first, second := seedSuffix(at), seedSuffix(at); first != second {
		t.Errorf("seedSuffix(%v) = %q then %q; one invocation would write a run "+
			"id its source ids do not match", at, first, second)
	}
}

// The prefixes are the command's cleanup contract: --clear finds what it wrote
// with LIKE 'run_seed_%' on pipeline_runs and LIKE 'seed_%' on inbox_items and
// pipeline_waitpoints. A uniqueness scheme that moved either prefix would leave
// every seeded row unremovable, which is worse than the collision it fixes.
func TestSeedSuffix_KeepsThePrefixesClearMatchesOn(t *testing.T) {
	suffix := seedSuffix(time.Now())

	runID := "run_seed_" + suffix
	sourceID := "seed_" + suffix + "_0"

	if !strings.HasPrefix(runID, "run_seed_") {
		t.Errorf("run id %q no longer matches LIKE 'run_seed_%%'", runID)
	}
	if !strings.HasPrefix(sourceID, "seed_") {
		t.Errorf("source id %q no longer matches LIKE 'seed_%%'", sourceID)
	}
	// A suffix carrying `_` would still match the LIKE, but would make the
	// `seed_<suffix>_<n>` shape ambiguous to read. Base 36 has no separator.
	if strings.ContainsAny(suffix, "_%") {
		t.Errorf("suffix %q contains a separator or a LIKE wildcard", suffix)
	}
}
