package main

import (
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
