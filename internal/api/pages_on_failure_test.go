package api

// Pages — `on_failure` and the sweeper that notices (docs/prd/pages.md §4).
//
// "A page that quietly stops updating must generate work for a human." Every
// test here is a way that sentence fails: nobody notices, everybody notices
// every minute, or the wrong crew notices.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// newLapseRig builds a page whose single panel has a 30s SLA and the given
// on_failure block, plus the crew (with its LEAD agent) that block names.
func newLapseRig(t *testing.T, onFailure string) *wakeRig {
	t.Helper()
	block := ""
	if onFailure != "" {
		block = `, "on_failure": {"issue": "crew/` + onFailure + `"}`
	}
	return newWakeRig(t, `[{
		"id": "sluzby",
		"schema": "status.v1",
		"owner": "crew/lookout",
		"producer": "script/watch.sh",
		"sla_seconds": 30`+block+`
	}]`)
}

// A lapsed SLA opens exactly ONE issue on the crew on_failure names — and the
// next sweep, with the panel still quiet, opens nothing.
func TestLapsedSLAOpensExactlyOneIssue(t *testing.T) {
	rig := newLapseRig(t, "ops")
	rig.push(t, "ok")

	// Inside the SLA: nothing has lapsed.
	rig.clock.advance(20 * time.Second)
	res, err := rig.h.SweepPanelFreshness(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.Lapsed != 0 || rig.issuesOn(t, rig.opsID) != 0 {
		t.Fatalf("a panel inside its SLA lapsed: %+v", res)
	}

	// Past it.
	rig.clock.advance(20 * time.Second)
	res, err = rig.h.SweepPanelFreshness(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.Lapsed != 1 || res.Issues != 1 {
		t.Fatalf("sweep after the SLA passed = %+v, want one lapse and one issue", res)
	}
	if got := rig.issuesOn(t, rig.opsID); got != 1 {
		t.Fatalf("issues on the on_failure crew = %d, want 1", got)
	}
	if got := rig.issuesOn(t, rig.devopsID); got != 0 {
		t.Fatalf("crew/devops was given %d issues by a panel that does not name it", got)
	}

	// The next sweep — and the one after — must not open a second. This is
	// the property the alert row exists for: a panel dead for a week produces
	// one issue, not one per minute.
	for i := 0; i < 3; i++ {
		rig.clock.advance(time.Minute)
		res, err = rig.h.SweepPanelFreshness(context.Background())
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if res.Issues != 0 {
			t.Fatalf("sweep %d opened another issue for the same lapse", i+2)
		}
	}
	if got := rig.issuesOn(t, rig.opsID); got != 1 {
		t.Fatalf("issues after four sweeps = %d, want 1", got)
	}

	// The lapse is journalled once, and the entry is what notifyroute turns
	// into the pages.stale notification.
	stale := 0
	for _, e := range rig.spy.entries {
		if e.Type == journal.EntryPagePanelStale {
			stale++
		}
	}
	if stale != 1 {
		t.Fatalf("page.panel.stale entries = %d, want exactly 1", stale)
	}
	e := rig.spy.firstOfType(journal.EntryPagePanelStale)
	if e.Payload["verdict"] != "stale" || e.Payload["issue_identifier"] == "" {
		t.Errorf("page.panel.stale payload = %v, want the verdict and the issue it opened", e.Payload)
	}
}

// A panel with no on_failure still reports its lapse — the owner is owed the
// notification (§10b.6) even when no crew was given the work.
func TestALapseWithNoOnFailureIsReportedButOpensNoIssue(t *testing.T) {
	rig := newLapseRig(t, "")
	rig.push(t, "ok")
	rig.clock.advance(time.Minute)

	res, err := rig.h.SweepPanelFreshness(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.Lapsed != 1 {
		t.Fatalf("lapsed = %d, want 1", res.Lapsed)
	}
	if res.Issues != 0 {
		t.Fatalf("issues = %d; a panel with no on_failure gives nobody work", res.Issues)
	}
	if rig.spy.firstOfType(journal.EntryPagePanelStale) == nil {
		t.Error("the lapse was not journalled, so pages.stale has nothing to notify on")
	}

	// Still once, not once per tick.
	rig.clock.advance(time.Minute)
	if _, err := rig.h.SweepPanelFreshness(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	stale := 0
	for _, e := range rig.spy.entries {
		if e.Type == journal.EntryPagePanelStale {
			stale++
		}
	}
	if stale != 1 {
		t.Fatalf("page.panel.stale entries = %d after two sweeps, want 1", stale)
	}
}

// Recovery clears the alert, and a LATER lapse is a new edge with a new issue.
// Without this a panel that flaps would report its first outage and no other.
func TestRecoveryClearsTheAlertAndTheNextLapseOpensAgain(t *testing.T) {
	rig := newLapseRig(t, "ops")
	rig.push(t, "ok")
	rig.clock.advance(time.Minute)

	if _, err := rig.h.SweepPanelFreshness(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := rig.issuesOn(t, rig.opsID); got != 1 {
		t.Fatalf("issues = %d, want 1", got)
	}

	// Data arrives again.
	rig.push(t, "ok")
	res, err := rig.h.SweepPanelFreshness(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.Recovered != 1 {
		t.Fatalf("recovered = %d, want 1", res.Recovered)
	}
	if rig.spy.firstOfType(journal.EntryPagePanelRecovered) == nil {
		t.Error("the recovery was not journalled; 'it fixed itself at 03:12' has to be answerable")
	}
	var alerts int
	if err := rig.h.db.QueryRow(`SELECT COUNT(*) FROM page_panel_alerts WHERE gate_key = 'sla'`).Scan(&alerts); err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if alerts != 0 {
		t.Fatal("the alert survived the recovery, so the next outage would be silent")
	}

	// And it goes quiet again.
	rig.clock.advance(time.Minute)
	if _, err := rig.h.SweepPanelFreshness(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := rig.issuesOn(t, rig.opsID); got != 2 {
		t.Fatalf("issues after a second outage = %d, want 2", got)
	}
}

// An explicit failure push is a lapse immediately — §4 rule 2: a producer that
// ran and failed is a different fact from one that went quiet, and it does not
// have to wait out its SLA to be reported.
func TestAFailedPushLapsesWithoutWaitingForTheSLA(t *testing.T) {
	rig := newLapseRig(t, "ops")

	req := pagesRequest(t, "PUT", "/api/v1/pages/flotila/panels/sluzby/data?state=failed",
		rig.wsID, rig.userID, "OWNER", `{"items":[{"name":"api","state":"ok"}]}`)
	req.SetPathValue("slug", "flotila")
	req.SetPathValue("panelId", "sluzby")
	rr := httptest.NewRecorder()
	rig.h.PushData(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("push: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	res, err := rig.h.SweepPanelFreshness(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.Issues != 1 {
		t.Fatalf("issues = %d immediately after a failure push, want 1", res.Issues)
	}
	e := rig.spy.firstOfType(journal.EntryPagePanelStale)
	if e == nil || e.Payload["verdict"] != "failed" {
		t.Fatalf("entry = %v, want a lapse recording the failed verdict", e)
	}
}

// A panel that has NEVER been pushed is not an incident on the day it is
// authored — and is one when it has been alive for longer than its own SLA and
// still has nothing.
func TestNeverProducedLapsesOnlyOnceThePanelIsOlderThanItsSLA(t *testing.T) {
	rig := newLapseRig(t, "ops")

	res, err := rig.h.SweepPanelFreshness(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.Lapsed != 0 {
		t.Fatalf("a panel authored a moment ago lapsed: %+v", res)
	}

	rig.clock.advance(time.Minute)
	res, err = rig.h.SweepPanelFreshness(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.Issues != 1 {
		t.Fatalf("issues = %d for a panel that never reported inside its SLA, want 1", res.Issues)
	}
	e := rig.spy.firstOfType(journal.EntryPagePanelStale)
	if e == nil || !strings.Contains(e.Summary, "never_produced") {
		t.Errorf("summary = %q, want it to say the panel never produced", e.Summary)
	}
}

// The sweeper must not open an issue on a crew that has no LEAD agent — it
// cannot, insertIssueTx refuses — and must not swallow the failure either: the
// alert rolls back with the issue so the next tick tries again.
func TestALapseOnACrewWithNoLeadAgentRetriesRatherThanGoingSilent(t *testing.T) {
	rig := newLapseRig(t, "ops")
	execOrFatal(t, rig.h.db, `DELETE FROM agents WHERE id = 'agent-ops-lead'`)
	rig.push(t, "ok")
	rig.clock.advance(time.Minute)

	res, err := rig.h.SweepPanelFreshness(context.Background())
	if err != nil {
		t.Fatalf("sweep returned an error rather than logging one panel's failure: %v", err)
	}
	if res.Issues != 0 {
		t.Fatalf("issues = %d, want 0 — there is no LEAD agent to hand it to", res.Issues)
	}
	var alerts int
	if err := rig.h.db.QueryRow(`SELECT COUNT(*) FROM page_panel_alerts`).Scan(&alerts); err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if alerts != 0 {
		t.Fatal("an alert row survived a failed issue insert; the lapse would never be reported again")
	}

	// Hire one, and the next sweep reports the lapse that was never dropped.
	seedAgentRow(t, rig.h.db, "agent-ops-lead-2", rig.wsID, rig.opsID, "Ops Lead", "ops-lead-2", "LEAD")
	rig.clock.advance(time.Minute)
	res, err = rig.h.SweepPanelFreshness(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.Issues != 1 {
		t.Fatalf("issues = %d once the crew had a lead, want 1", res.Issues)
	}
}
