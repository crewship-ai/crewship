package backupplan

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

func drillReminders(t *testing.T, h *harness) []Incident {
	t.Helper()
	var out []Incident
	for _, in := range incidentsOf(t, h) {
		if in.Kind == IncidentDrill && in.PlanID == nil && strings.HasPrefix(in.Message, "No test restore") {
			out = append(out, in)
		}
	}
	return out
}

// The drill reminder: the scheduler tick opens an instance-wide "drill"
// incident when the newest test restore is older than drill_reminder
// (monthly: 31 days), delivers it once, and resolves it when a drill is
// recorded — a catalog drill_at or a restore_reports row of kind drill.
func TestService_DrillReminderWithAFakeClock(t *testing.T) {
	h := newHarness(t, "2026-09-01T02:00:00Z")
	ctx := context.Background()
	alerts := &fakeAlerter{}
	h.svc.Alerts = alerts
	tick := func(at string) {
		t.Helper()
		h.clock.Set(mustTime(t, at))
		h.svc.lastDrill = time.Time{} // the check is throttled to every 5 minutes
		h.svc.checkDrillReminder(ctx)
	}

	// No backup at all: nothing to test, no reminder however long it waits.
	tick("2026-12-01T00:00:00Z")
	if got := drillReminders(t, h); len(got) != 0 {
		t.Fatalf("reminder without any backup: %+v", got)
	}

	// A bundle made on 1 September, never drilled.
	path := "/backups/crewship-instance-all-20260901T030000Z.tar.zst"
	if err := backup.UpsertCatalogEntry(ctx, h.db, backup.CatalogEntry{FilePath: path, Scope: "instance", Kind: backup.KindInstance,
		CreatedAt: mustTime(t, "2026-09-01T03:00:00Z"), SHA256: "x"}); err != nil {
		t.Fatal(err)
	}
	tick("2026-10-01T03:00:00Z") // 30 days: within a month
	if got := drillReminders(t, h); len(got) != 0 {
		t.Fatalf("reminder too early: %+v", got)
	}
	tick("2026-10-03T04:00:00Z") // 32 days
	got := drillReminders(t, h)
	if len(got) != 1 || got[0].State != "open" {
		t.Fatalf("reminder = %+v, want one open", got)
	}
	if !strings.Contains(got[0].Message, "No test restore yet, and the first backup is 32 days old") ||
		!strings.Contains(got[0].Message, "crewship backup drill --bundle "+path) {
		t.Fatalf("message = %q", got[0].Message)
	}
	tick("2026-10-03T05:00:00Z") // still overdue: not delivered again
	if n := len(drillReminders(t, h)); n != 1 {
		t.Fatalf("reminders = %d", n)
	}

	// A drill recorded on the bundle resolves it on the next tick.
	if err := backup.SetCatalogDrill(ctx, h.db, path, backup.RestoreResultOK, json.RawMessage(`{}`), mustTime(t, "2026-10-03T06:00:00Z")); err != nil {
		t.Fatal(err)
	}
	tick("2026-10-03T06:10:00Z")
	if got := drillReminders(t, h); len(got) != 1 || got[0].State != "resolved" {
		t.Fatalf("after the drill: %+v", got)
	}

	// 32 days after that drill, it opens again with the age of the drill.
	tick("2026-11-04T07:00:00Z")
	got = drillReminders(t, h)
	var open *Incident
	for i := range got {
		if got[i].State == "open" {
			open = &got[i]
		}
	}
	if open == nil || !strings.Contains(open.Message, "No test restore in 32 days — run `crewship backup drill") {
		t.Fatalf("second reminder = %+v", got)
	}

	// A drill report (the CLI's `backup drill`) answers it too, and so
	// does RecordDrillOutcome at once, without waiting for a tick.
	if _, err := backup.RecordRestoreReport(ctx, h.db, backup.RestoreReport{Kind: backup.RestoreKindDrill, BundlePath: path,
		Target: "isolated", Result: backup.RestoreResultPartial, CreatedAt: mustTime(t, "2026-11-04T08:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	h.svc.RecordDrillOutcome(ctx, "", backup.RestoreResultPartial, "one crew failed")
	for _, in := range drillReminders(t, h) {
		if in.State == "open" {
			t.Fatalf("reminder still open after a recorded drill: %+v", in)
		}
	}
	// The partial drill itself is its own incident (plan "" = manual), not
	// swallowed by the reminder's slot.
	var failure bool
	for _, in := range incidentsOf(t, h) {
		if in.Kind == IncidentDrill && in.State == "open" && strings.Contains(in.Message, "was partial") {
			failure = true
		}
	}
	if !failure {
		t.Fatal("the partial drill's incident is missing")
	}
	tick("2026-11-04T09:00:00Z")
	for _, in := range drillReminders(t, h) {
		if in.State == "open" {
			t.Fatalf("reminder reopened within the month after a drill report: %+v", in)
		}
	}

	// Off: an overdue reminder resolves and none opens.
	tick("2027-01-01T00:00:00Z")
	if len(openOf(drillReminders(t, h))) != 1 {
		t.Fatal("expected an overdue reminder before switching it off")
	}
	s := DefaultSettings()
	s.DrillReminder = DrillOff
	if err := SaveSettings(ctx, h.db, s, "u1", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	tick("2027-01-01T01:00:00Z")
	if n := len(openOf(drillReminders(t, h))); n != 0 {
		t.Fatalf("reminder open while off: %d", n)
	}

	var delivered int
	for _, c := range alerts.all() {
		if c.kind == IncidentDrill && c.opened && strings.HasPrefix(c.message, "No test restore") {
			delivered++
		}
	}
	if delivered != 3 {
		t.Fatalf("reminder delivered %d times, want 3 (once per overdue spell)", delivered)
	}
}

func openOf(in []Incident) []Incident {
	var out []Incident
	for _, i := range in {
		if i.State == "open" {
			out = append(out, i)
		}
	}
	return out
}

func TestDrillReminderPeriod(t *testing.T) {
	for in, want := range map[string]time.Duration{DrillWeekly: 7 * 24 * time.Hour, DrillMonthly: 31 * 24 * time.Hour, DrillOff: 0, "": 0} {
		if got := DrillReminderPeriod(in); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}
