package backupplan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A plan that covers several workspaces runs once per workspace for one due
// time (or one manual click). The heartbeat says "backups are healthy" and
// resolving the plan's "failed" incident says the same, so both wait for the
// whole group: workspace A finishing well must not hide workspace B failing,
// and must not ping before B has finished at all.
func TestAfterRun_FanOutGroupDecidesHeartbeatAndResolution(t *testing.T) {
	type finish struct {
		ws     string
		status string
	}
	cases := []struct {
		name string
		// manual runs share a click (no due time) instead of a due time.
		manual bool
		// the group's runs, all queued before any finishes.
		workspaces []string
		finishes   []finish
		wantPings  int32
		// the plan's failed incident: "open", "resolved" or "" (none).
		wantFailed string
	}{
		{
			name:       "A done while B still runs: nothing yet",
			workspaces: []string{"ws_a", "ws_b"},
			finishes:   []finish{{"ws_a", StatusDone}},
			wantPings:  0, wantFailed: "open",
		},
		{
			name:       "B fails after A succeeded: no ping, incident open",
			workspaces: []string{"ws_a", "ws_b"},
			finishes:   []finish{{"ws_a", StatusDone}, {"ws_b", StatusFailed}},
			wantPings:  0, wantFailed: "open",
		},
		{
			name:       "A succeeds after B failed: no ping, incident stays open",
			workspaces: []string{"ws_a", "ws_b"},
			finishes:   []finish{{"ws_b", StatusFailed}, {"ws_a", StatusDone}},
			wantPings:  0, wantFailed: "open",
		},
		{
			name:       "both succeed: one ping, incident resolved",
			workspaces: []string{"ws_a", "ws_b"},
			finishes:   []finish{{"ws_a", StatusDone}, {"ws_b", StatusDone}},
			wantPings:  1, wantFailed: "resolved",
		},
		{
			name:       "one workspace, done: one ping, resolved (unchanged)",
			workspaces: []string{"ws_a"},
			finishes:   []finish{{"ws_a", StatusDone}},
			wantPings:  1, wantFailed: "resolved",
		},
		{
			name:       "manual click over two workspaces, one fails: no ping",
			manual:     true,
			workspaces: []string{"ws_a", "ws_b"},
			finishes:   []finish{{"ws_b", StatusFailed}, {"ws_a", StatusDone}},
			wantPings:  0, wantFailed: "open",
		},
		{
			name:       "manual click over two workspaces, both done: one ping",
			manual:     true,
			workspaces: []string{"ws_a", "ws_b"},
			finishes:   []finish{{"ws_a", StatusDone}, {"ws_b", StatusDone}},
			wantPings:  1, wantFailed: "resolved",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var hits atomic.Int32
			srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			defer srv.Close()
			h := newHarness(t, "2026-10-01T02:00:00Z")
			h.svc.HeartbeatClient, h.svc.allowPrivateHeartbeat = srv.Client(), true
			set := DefaultSettings()
			u := srv.URL + "/ping"
			set.HeartbeatURL = &u
			if err := SaveSettings(ctx, h.db, set, "u1", h.clock.Now()); err != nil {
				t.Fatal(err)
			}
			p := h.plan(func(p *Plan) { p.WorkspaceIDs = []string{"ws_a", "ws_b"} })

			// An earlier failure leaves the plan's failed incident open.
			prevDue := mustTime(t, "2026-09-30T03:00:00Z")
			prev := h.queued(p, "ws_a", &prevDue, mustTime(t, "2026-09-30T03:00:20Z"))
			h.finish(prev, StatusFailed, p)
			hits.Store(0)

			now := mustTime(t, "2026-10-01T03:00:20Z")
			var due *time.Time
			if !tc.manual {
				d := mustTime(t, "2026-10-01T03:00:00Z")
				due = &d
			}
			runs := map[string]*Run{}
			for _, ws := range tc.workspaces {
				runs[ws] = h.queued(p, ws, due, now)
			}
			for _, f := range tc.finishes {
				h.finish(runs[f.ws], f.status, p)
			}
			h.svc.wg.Wait()

			if got := hits.Load(); got != tc.wantPings {
				t.Errorf("heartbeat pings = %d, want %d", got, tc.wantPings)
			}
			// An open failed incident wins over a resolved one: a reopened
			// incident is a new row beside the one an earlier run closed.
			state := ""
			for _, in := range incidentsOf(t, h) {
				if in.Kind == IncidentFailed && state != "open" {
					state = in.State
				}
			}
			if state != tc.wantFailed {
				t.Errorf("failed incident = %q, want %q", state, tc.wantFailed)
			}
		})
	}
}

// queued inserts a running run of plan p for ws; due nil makes it a manual run.
func (h *harness) queued(p *Plan, ws string, due *time.Time, started time.Time) *Run {
	h.t.Helper()
	trigger := TriggerSchedule
	if due == nil {
		trigger = TriggerManual
	}
	r := &Run{PlanID: p.ID, Trigger: trigger, Scope: p.Scope, WorkspaceID: ws, Status: StatusRunning,
		Phase: PhaseQueued, Phases: initialPhases(false), DueAt: due, NextAttemptAt: &started, StartedAt: started}
	if _, err := insertRun(context.Background(), h.db, r); err != nil {
		h.t.Fatal(err)
	}
	return r
}

// finish writes r's terminal state and runs the outcome hook, as the
// executor path does.
func (h *harness) finish(r *Run, status string, p *Plan) {
	h.t.Helper()
	end := h.clock.Now()
	r.Status, r.EndedAt = status, &end
	if status == StatusFailed {
		r.Error = "disk on fire"
	}
	if err := finishRun(context.Background(), h.db, r); err != nil {
		h.t.Fatal(err)
	}
	h.svc.afterRun(context.Background(), r, p)
}

// An interrupted run is retried once; the retry speaks for its workspace, so
// the group waits for it and judges by its result, not the interruption.
func TestAfterRun_RetryStandsInForTheInterruptedRun(t *testing.T) {
	ctx := context.Background()
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	h := newHarness(t, "2026-10-01T02:00:00Z")
	h.svc.HeartbeatClient, h.svc.allowPrivateHeartbeat = srv.Client(), true
	set := DefaultSettings()
	u := srv.URL + "/ping"
	set.HeartbeatURL = &u
	if err := SaveSettings(ctx, h.db, set, "u1", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	p := h.plan(func(p *Plan) { p.WorkspaceIDs = []string{"ws_a", "ws_b"} })
	due := mustTime(t, "2026-10-01T03:00:00Z")
	start := mustTime(t, "2026-10-01T03:00:20Z")
	a := h.queued(p, "ws_a", &due, start)
	b := h.queued(p, "ws_b", &due, start)
	// b is interrupted and retried (no outcome hook for a retried run).
	end := h.clock.Now()
	b.Status, b.EndedAt = StatusInterrupted, &end
	if err := finishRun(ctx, h.db, b); err != nil {
		t.Fatal(err)
	}
	retry := h.queued(p, "ws_b", &due, mustTime(t, "2026-10-01T03:10:00Z"))
	retry.RetryOf = b.ID
	if _, err := h.db.Exec(`UPDATE backup_runs SET retry_of = ? WHERE id = ?`, b.ID, retry.ID); err != nil {
		t.Fatal(err)
	}
	h.finish(a, StatusDone, p)
	h.svc.wg.Wait()
	if hits.Load() != 0 {
		t.Fatal("pinged while the retry still runs")
	}
	h.finish(retry, StatusDone, p)
	h.svc.wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("pings = %d, want 1 once the retry succeeded", hits.Load())
	}
}
