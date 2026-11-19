package orchestrator

import (
	"encoding/json"
	"testing"
	"time"
)

// #5511: once a detached hold confirms its runtime gone, the run must leave
// RUNNING everywhere it is read: the runtime record, the audit log and (via
// the durable stop outbox the server projects) the journal's run list.
func TestDetachedHoldConfirmedGoneRecordsTerminal(t *testing.T) {
	cases := []struct {
		name        string
		stored      string
		wantStatus  string
		wantPending bool
		wantAudit   int
	}{
		{name: "running record becomes failed and projected", stored: "running", wantStatus: "error", wantPending: true, wantAudit: 1},
		// A concurrent writer that already settled the run keeps its outcome.
		{name: "settled record keeps its outcome", stored: "completed", wantStatus: "completed", wantPending: false, wantAudit: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := New(covNewRunContainer(covRunOpts{}), newMemState(), covQuietLogger())
			rec := &recordingAudit{}
			o.SetAuditLog(rec)
			req := covRunReq()
			req.RunID = "run_detachedterminal"
			raw, _ := json.Marshal(RunState{ID: req.RunID, AgentID: req.AgentID, AgentSlug: req.AgentSlug, WorkspaceID: req.WorkspaceID, ContainerID: req.ContainerID, Status: tc.stored})
			if err := o.state.Set(t.Context(), "agent_runs", req.RunID, raw); err != nil {
				t.Fatal(err)
			}
			released := 0
			h := &detachedHold{runID: req.RunID, containerID: req.ContainerID, agentSlug: req.AgentSlug, execID: "detached", req: req,
				release: func() { released++ }, startedAt: time.Now()}
			o.detached[h.runID] = h
			o.closeDetachedHold(h, true)
			o.closeDetachedHold(h, true) // exactly once

			stored, err := o.state.Get(t.Context(), "agent_runs", req.RunID)
			if err != nil {
				t.Fatal(err)
			}
			var run RunState
			if err := json.Unmarshal(stored, &run); err != nil {
				t.Fatal(err)
			}
			if run.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", run.Status, tc.wantStatus)
			}
			if run.StopJournalPending != tc.wantPending {
				t.Fatalf("journal projection pending = %v, want %v", run.StopJournalPending, tc.wantPending)
			}
			if tc.wantPending && run.StopOrigin != DetachedConfirmedGoneOrigin {
				t.Fatalf("stop origin = %q, want %q", run.StopOrigin, DetachedConfirmedGoneOrigin)
			}
			failed := 0
			for _, c := range rec.snapshot() {
				if c.action == "agent.run.failed" && c.entityID == req.RunID {
					failed++
				}
			}
			if failed != tc.wantAudit {
				t.Fatalf("agent.run.failed audit rows = %d, want %d", failed, tc.wantAudit)
			}
			if released != 1 {
				t.Fatalf("capacity released %d times, want 1", released)
			}
		})
	}

	// An unconfirmed close must not write anything terminal.
	t.Run("unconfirmed close writes nothing", func(t *testing.T) {
		o := New(covNewRunContainer(covRunOpts{}), newMemState(), covQuietLogger())
		rec := &recordingAudit{}
		o.SetAuditLog(rec)
		req := covRunReq()
		raw, _ := json.Marshal(RunState{ID: req.RunID, AgentID: req.AgentID, Status: "running"})
		_ = o.state.Set(t.Context(), "agent_runs", req.RunID, raw)
		h := &detachedHold{runID: req.RunID, containerID: req.ContainerID, agentSlug: req.AgentSlug, req: req, release: func() {}, startedAt: time.Now()}
		o.detached[h.runID] = h
		o.closeDetachedHold(h, false)
		stored, _ := o.state.Get(t.Context(), "agent_runs", req.RunID)
		var run RunState
		_ = json.Unmarshal(stored, &run)
		if run.Status != "running" || run.StopJournalPending || len(rec.snapshot()) != 0 {
			t.Fatalf("unconfirmed close changed the run: %+v audit=%d", run, len(rec.snapshot()))
		}
	})
}
