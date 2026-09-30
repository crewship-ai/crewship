package backupplan

import (
	"context"
	"strings"
	"testing"
)

// A skipped night is visible — in Backup history and the nights strip — and
// pages nobody; the second skipped night in a row raises the plan's
// incident, naming why; the next good run resolves it.
func TestService_RepeatedSkipsRaiseAnIncident(t *testing.T) {
	t.Setenv(MinFreePercentEnv, "") // the default 10 % floor makes the skips
	h := newHarness(t, "2026-09-30T02:00:00Z")
	alerts := &fakeAlerter{}
	h.svc.Alerts = alerts
	p := h.plan(func(p *Plan) { p.Name = "Nightly" })
	full := true
	h.svc.Space = func(string) (uint64, uint64, error) {
		if full {
			return 50 << 30, 1000 << 30, nil // under the 10 % floor
		}
		return 900 << 30, 1000 << 30, nil
	}
	failedIncidents := func() []Incident {
		var out []Incident
		for _, in := range incidentsOf(t, h) {
			if in.Kind == IncidentFailed {
				out = append(out, in)
			}
		}
		return out
	}
	deliveredFailed := func() []alertCall {
		var out []alertCall
		for _, c := range alerts.all() {
			if c.kind == IncidentFailed {
				out = append(out, c)
			}
		}
		return out
	}

	// Night 1: skipped, visible, no incident.
	h.clock.Set(mustTime(t, "2026-09-30T03:00:20Z"))
	h.tick()
	rs := h.runs(p.ID)
	if len(rs) != 1 || rs[0].Status != StatusSkipped || !strings.Contains(rs[0].Error, "not enough disk space") {
		t.Fatalf("night 1 runs = %+v", rs)
	}
	if inc := failedIncidents(); len(inc) != 0 {
		t.Fatalf("a single skip raised an incident: %+v", inc)
	}
	if c := deliveredFailed(); len(c) != 0 {
		t.Fatalf("a single skip paged someone: %+v", c)
	}
	ov, err := BuildOverview(context.Background(), h.db, OverviewInput{Scope: ScopeWorkspaces, BackupsDir: t.TempDir(),
		Space: func() (uint64, uint64) { return 900 << 30, 1000 << 30 }}, h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n := ov.Nights[len(ov.Nights)-1]; n.Date != "2026-09-30" || n.Status != "skipped" {
		t.Fatalf("tonight in the nights strip = %+v, want skipped", n)
	}
	if !hasAttention(ov, "run:"+rs[0].ID) {
		t.Fatalf("needs attention lacks the skipped run: %+v", ov.NeedsAttention)
	}

	// Night 2: skipped again — the incident opens and is delivered once.
	h.clock.Set(mustTime(t, "2026-10-01T03:00:20Z"))
	h.tick()
	inc := failedIncidents()
	if len(inc) != 1 || inc[0].State != "open" {
		t.Fatalf("after two skips: %+v", inc)
	}
	if want := "Nightly backup skipped 2 times: not enough space. Last successful backup: never."; inc[0].Message != want {
		t.Fatalf("message = %q, want %q", inc[0].Message, want)
	}
	if c := deliveredFailed(); len(c) != 1 || !c[0].opened {
		t.Fatalf("delivered = %+v, want one opening", c)
	}

	// Night 3: room again — a good run resolves it.
	full = false
	h.clock.Set(mustTime(t, "2026-10-02T03:00:20Z"))
	h.tick()
	inc = failedIncidents()
	if len(inc) != 1 || inc[0].State != "resolved" {
		t.Fatalf("after a good run: %+v", inc)
	}
	if c := deliveredFailed(); len(c) != 2 || !c[1].resolved {
		t.Fatalf("delivered = %+v, want the opening then the resolution", c)
	}

	// Night 4: one skip after a good run starts the count over.
	full = true
	h.clock.Set(mustTime(t, "2026-10-03T03:00:20Z"))
	h.tick()
	for _, in := range failedIncidents() {
		if in.State == "open" {
			t.Fatalf("a first skip after a good run reopened the incident: %+v", in)
		}
	}
}

func TestService_SkipAlertAfterOverride(t *testing.T) {
	t.Setenv(SkipAlertAfterEnv, "1")
	t.Setenv(MinFreePercentEnv, "")
	h := newHarness(t, "2026-09-30T02:00:00Z")
	p := h.plan(nil)
	h.svc.Space = func(string) (uint64, uint64, error) { return 50 << 30, 1000 << 30, nil }
	h.clock.Set(mustTime(t, "2026-09-30T03:00:20Z"))
	h.tick()
	if rs := h.runs(p.ID); len(rs) != 1 || rs[0].Status != StatusSkipped {
		t.Fatalf("runs = %+v", rs)
	}
	inc := incidentsOf(t, h)
	if len(inc) != 1 || inc[0].Kind != IncidentFailed || !strings.Contains(inc[0].Message, "skipped 1 times: not enough space") {
		t.Fatalf("with %s=1 the first skip must raise: %+v", SkipAlertAfterEnv, inc)
	}
}

func TestSkipReason(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"skipped: still busy after 120 minutes (backup: instance backup skipped: work was still running: quiesce: writes did not drain: 1 still in flight after 1m0s)", "writes did not drain"},
		{"skipped: still busy after 120 minutes (2 agent(s) running)", "workspace busy"},
		{"not enough disk space: the run needs about 4.0 GiB …", "not enough space"},
		{"scheduled backups are held: instance restore", "backups are held"},
		{"", "workspace busy"},
	} {
		if got := SkipReason(c.in); got != c.want {
			t.Errorf("SkipReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestConsecutiveSkips_PerPlanAndWorkspace(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	ctx := context.Background()
	p := h.plan(func(p *Plan) { p.WorkspaceIDs = []string{"ws_a", "ws_b"} })
	add := func(ws, status, at string) {
		t.Helper()
		end := mustTime(t, at)
		if _, err := insertRun(ctx, h.db, &Run{PlanID: p.ID, Trigger: TriggerSchedule, Scope: ScopeWorkspaces, WorkspaceID: ws,
			Status: status, StartedAt: end, EndedAt: &end}); err != nil {
			t.Fatal(err)
		}
	}
	add("ws_a", StatusDone, "2026-09-26T03:00:00Z")
	add("ws_a", StatusSkipped, "2026-09-27T03:00:00Z")
	add("ws_a", StatusSkipped, "2026-09-28T03:00:00Z")
	add("ws_b", StatusSkipped, "2026-09-28T03:00:00Z")
	add("ws_b", StatusDone, "2026-09-29T03:00:00Z")
	if n := consecutiveSkips(ctx, h.db, p.ID, "ws_a"); n != 2 {
		t.Fatalf("ws_a = %d, want 2", n)
	}
	if n := consecutiveSkips(ctx, h.db, p.ID, "ws_b"); n != 0 {
		t.Fatalf("ws_b = %d, want 0 (its newest run was good)", n)
	}
	if n := consecutiveSkips(ctx, h.db, "", ""); n != 0 {
		t.Fatalf("no-plan runs = %d, want 0", n)
	}
}

func hasAttention(ov *OverviewResponse, id string) bool {
	for _, it := range ov.NeedsAttention {
		if it.ID == id {
			return true
		}
	}
	return false
}
