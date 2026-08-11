package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The journal entry an automation matches must name the RUN that caused it.
//
// This is the pointer the composition depth cap is built on. Registry.Flush
// resolves the triggering entry's run, reads its chain_depth, and spends
// depth+1 — so a chain that leaves the process through the journal and comes
// back keeps one budget. With no pointer, every hop resolves nothing, defaults
// to depth 1, and roots a fresh chain: the cap becomes unreachable and a
// two-rule cycle runs forever.
//
// It shipped unreachable. `crewshipBody` sends author_run_id, and the internal
// route decoded it into the update struct — and then the journal entry was
// emitted with no TraceID at all. Measured on a live instance: two rules
// ping-ponging one issue through a crewship issue.update step ran 28 status
// changes and climbing, every run recording chain_depth 1 and its own
// chain_origin, with zero automation.depth_exceeded entries.
//
// The unit test that was supposed to cover this
// (automation.TestObserver_ClosedLoopStopsAtMaxChainDepth) sets
// `next.TraceID = parent.ID` by hand. It models the world this test now
// enforces, and passed for as long as that world did not exist — which is why
// this one drives the REAL route rather than constructing an issueEvent.
func TestInternalIssue_UpdateStatus_StampsTheCausingRunOnTheJournalEntry(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	rec := &recordingEmitter{}
	h.SetJournal(rec)

	const runID = "run_the_one_that_did_it"
	body := bytes.NewBufferString(`{"workspace_id":"` + wsID +
		`","status":"TODO","author_run_id":"` + runID + `"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var found bool
	for _, e := range rec.entries {
		if e.Type != "mission.status_change" {
			continue
		}
		found = true
		if e.TraceID != runID {
			t.Errorf("trace_id = %q, want %q — without it Registry.Flush cannot resolve the "+
				"parent run, every automation hop restarts the budget at depth 1, and the "+
				"MaxChainDepth cap can never be reached", e.TraceID, runID)
		}
	}
	if !found {
		t.Fatalf("no mission.status_change entry was emitted; got %d entries", len(rec.entries))
	}
}

// A change with no run behind it — a person moving a card — must NOT invent
// one. An entry claiming a run that never ran would give the walk a parent to
// follow into nothing, and would make a human action read as a composed hop.
func TestInternalIssue_UpdateStatus_LeavesTraceEmptyWhenNoRunCausedIt(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	rec := &recordingEmitter{}
	h.SetJournal(rec)

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"TODO"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	for _, e := range rec.entries {
		if e.Type == "mission.status_change" && e.TraceID != "" {
			t.Errorf("trace_id = %q, want empty — nothing ran, so there is no run to name", e.TraceID)
		}
	}
}
