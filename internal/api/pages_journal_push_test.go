package api

import (
	"net/http"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// TestPagesPush_AcceptedPushIsJournalled closes the gap a conformance audit
// found: internal/journal/types.go declared EntryPagePanelUpdated as "emitted
// on every successful panel push (§5)" and nothing emitted it.
//
// This is not bookkeeping. §5 is the section that answers §2.1 — the documented
// reason the whole push-to-panel genre lost to query-based dashboards — and its
// answer for history is "internal/journal: every push emits an entry; the
// journal is the queryable record". With no entry, the only history Pages has
// is the 200-row ring §5 explicitly says is NOT the answer, and §5's alerting
// row has no event for an automation matcher to fire on.
func TestPagesPush_AcceptedPushIsJournalled(t *testing.T) {
	h, spy, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")

	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("push: status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}

	var entry *journal.Entry
	for i := range spy.entries {
		if spy.entries[i].Type == journal.EntryPagePanelUpdated {
			entry = &spy.entries[i]
			break
		}
	}
	if entry == nil {
		t.Fatalf("an accepted push wrote no %s entry; §5 makes the journal the history answer",
			journal.EntryPagePanelUpdated)
	}
	if entry.WorkspaceID != wsID {
		t.Errorf("entry workspace = %q, want %q", entry.WorkspaceID, wsID)
	}
	if entry.CrewID == "" {
		t.Error("entry carries no crew; the owning crew is how a wake gate scopes what it matches")
	}
	if got, _ := entry.Payload["panel"].(string); got != "sluzby" {
		t.Errorf("entry payload panel = %v, want the panel that was written", entry.Payload["panel"])
	}
	if got, _ := entry.Payload["page"].(string); got != "fleet-201" {
		t.Errorf("entry payload page = %v, want the page slug", entry.Payload["page"])
	}

	// The producer's own verdict, never the freshness state. fresh and stale
	// are functions of the clock and are never stored (§4), so an entry
	// claiming one would be wrong the moment anybody read it back.
	state, _ := entry.Payload["state"].(string)
	if state != "ok" && state != "failed" {
		t.Errorf("entry state = %q, want the producer verdict ok|failed, never a freshness word", state)
	}
}
