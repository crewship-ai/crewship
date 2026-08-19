package api

// Pages — the wake gate, end to end (docs/prd/pages.md §5).
//
// These tests drive the REAL path: a push through the handler emits the real
// journal entry, the real automation.Registry matches it against the rules the
// page save compiled, and the real opener writes a real issue. The one thing
// they fake is the clock, because "held for 5m" is only testable if the test
// owns time.
//
// What each of them is defending is a property that fails SILENTLY when it
// breaks: a gate that never fires, a gate that fires for the wrong crew, a
// gate that fires once per event instead of once per burst.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/automation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
)

// ── Fixture ────────────────────────────────────────────────────────────────

// wakeJournalSpy is journal.Writer's two relevant halves: it keeps the entries
// so a test can assert on them, and it hands each one to the commit observer
// the way the real writer does.
type wakeJournalSpy struct {
	entries  []journal.Entry
	observer func([]journal.Entry)
}

func (s *wakeJournalSpy) Emit(_ context.Context, e journal.Entry) (string, error) {
	s.entries = append(s.entries, e)
	if s.observer != nil {
		s.observer([]journal.Entry{e})
	}
	return "jrn_test", nil
}

func (s *wakeJournalSpy) Flush(_ context.Context) error { return nil }

func (s *wakeJournalSpy) firstOfType(t journal.EntryType) *journal.Entry {
	for i := range s.entries {
		if s.entries[i].Type == t {
			return &s.entries[i]
		}
	}
	return nil
}

// wakeRig is a page with one status.v1 sensor panel, a wired automation
// registry, and the two crews a "wakes the declared agent and only it" test
// needs.
type wakeRig struct {
	h        *PageHandler
	spy      *wakeJournalSpy
	clock    *pagesFakeClock
	reg      *automation.Registry
	wsID     string
	userID   string
	devopsID string
	opsID    string
}

// newWakeRig builds the fixture. gateYAML is spliced into the panel spec, so
// each test states the gate it is about.
func newWakeRig(t *testing.T, panels string) *wakeRig {
	t.Helper()
	h, _, clock, wsID, userID := newPagesFixture(t)
	// The spy stands in for journal.Writer: it records what was emitted AND
	// forwards it to the registry's observer, which is what
	// AddCommitObserver does in the running daemon.
	spy := &wakeJournalSpy{}
	h.SetJournal(spy)

	rig := &wakeRig{h: h, spy: spy, clock: clock, wsID: wsID, userID: userID}
	rig.devopsID = "crew-devops"
	rig.opsID = "crew-ops"
	execOrFatal(t, h.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'DevOps', 'devops')`, rig.devopsID, wsID)
	execOrFatal(t, h.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Ops', 'ops')`, rig.opsID, wsID)
	// An issue needs its crew's LEAD agent: insertIssueTx sets lead_agent_id
	// from it, and the opener names it as the assignee so the issue arrives
	// ready to start.
	seedAgentRow(t, h.db, "agent-devops-lead", wsID, rig.devopsID, "DevOps Lead", "devops-lead", "LEAD")
	seedAgentRow(t, h.db, "agent-ops-lead", wsID, rig.opsID, "Ops Lead", "ops-lead", "LEAD")

	// The registry, wired exactly as cmd_start wires it, minus the routine
	// sink no wake gate uses.
	rig.reg = automation.NewRegistry(automation.NewStore(h.db), nil, automation.Options{
		Logger: newTestLogger(),
		Now:    func() time.Time { return clock.now },
	})
	rig.reg.SetIssueOpener(NewPagesWakeIssueOpener(h.db, nil, spy, newTestLogger()))
	spy.observer = rig.reg.Observer
	h.SetAutomationRefresh(func(ctx context.Context) {
		if err := rig.reg.Refresh(ctx); err != nil {
			t.Errorf("registry refresh: %v", err)
		}
	})

	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", `{
		"slug": "flotila",
		"name": "Flotila",
		"panels": `+panels+`
	}`)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create page: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	return rig
}

// wakePanelsWith returns the one-sensor page body, with the gate's `for` and
// the crew it wakes made explicit.
func wakePanelsWith(hold, crew string) string {
	forField := ""
	if hold != "" {
		forField = `, "for": "` + hold + `"`
	}
	return `[{
		"id": "sluzby",
		"schema": "status.v1",
		"owner": "crew/lookout",
		"producer": "script/watch.sh",
		"sla_seconds": 300,
		"wake": [{"when": "any(state == \"critical\")", "agent": "crew/` + crew + `"` + forField + `}]
	}]`
}

// push sends one payload through the real write path.
func (r *wakeRig) push(t *testing.T, states ...string) *httptest.ResponseRecorder {
	t.Helper()
	items := make([]string, 0, len(states))
	for i, s := range states {
		items = append(items, `{"name":"svc-`+string(rune('a'+i))+`","state":"`+s+`"}`)
	}
	body := `{"items":[` + strings.Join(items, ",") + `]}`
	req := pagesRequest(t, "PUT", "/api/v1/pages/flotila/panels/sluzby/data", r.wsID, r.userID, "OWNER", body)
	req.SetPathValue("slug", "flotila")
	req.SetPathValue("panelId", "sluzby")
	rr := httptest.NewRecorder()
	r.h.PushData(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("push: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	return rr
}

// issuesOn counts the issues opened on one crew.
func (r *wakeRig) issuesOn(t *testing.T, crewID string) int {
	t.Helper()
	var n int
	if err := r.h.db.QueryRow(
		`SELECT COUNT(*) FROM missions WHERE crew_id = ? AND mission_type = 'issue'`, crewID).Scan(&n); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	return n
}

// ── The gate compiles to a rule ────────────────────────────────────────────

func TestWakeGateCompilesToAnAutomationRow(t *testing.T) {
	rig := newWakeRig(t, wakePanelsWith("", "devops"))

	var (
		eventType  string
		actionKind string
		matcher    string
		action     string
		enabled    int
	)
	if err := rig.h.db.QueryRow(`
		SELECT event_type, action_kind, matcher_json, action_config_json, enabled
		  FROM automations WHERE workspace_id = ? AND id LIKE 'aut_pgw_%'`, rig.wsID).
		Scan(&eventType, &actionKind, &matcher, &action, &enabled); err != nil {
		t.Fatalf("the wake gate compiled to no automations row: %v", err)
	}
	if eventType != string(journal.EntryPagePanelUpdated) {
		t.Errorf("event_type = %q, want %q", eventType, journal.EntryPagePanelUpdated)
	}
	if actionKind != automation.ActionKindIssue {
		t.Errorf("action_kind = %q, want %q", actionKind, automation.ActionKindIssue)
	}
	if enabled != 1 {
		t.Error("the compiled rule is disabled")
	}

	var m automation.Matcher
	if err := json.Unmarshal([]byte(matcher), &m); err != nil {
		t.Fatalf("matcher_json: %v", err)
	}
	if m.PayloadEquals["panel"] != "sluzby" || m.PayloadEquals["wake_1"] != true {
		t.Errorf("matcher = %v, want it pinned to the panel and the gate's own key", m.PayloadEquals)
	}
	var a automation.Action
	if err := json.Unmarshal([]byte(action), &a); err != nil {
		t.Fatalf("action_config_json: %v", err)
	}
	if a.Issue == nil || a.Issue.CrewSlug != "devops" {
		t.Errorf("action = %+v, want an issue action on crew/devops", a.Issue)
	}
}

// Saving twice must not accumulate rules — the id is derived from the gate, so
// the second save is a rewrite. Nothing else in the system would notice a page
// that quietly grew a duplicate rule per edit.
func TestSavingAPageTwiceKeepsOneRulePerGate(t *testing.T) {
	rig := newWakeRig(t, wakePanelsWith("", "devops"))

	req := pagesRequest(t, "PATCH", "/api/v1/pages/flotila", rig.wsID, rig.userID, "OWNER", `{
		"name": "Flotila (renamed)",
		"panels": `+wakePanelsWith("", "devops")+`
	}`)
	req.SetPathValue("slug", "flotila")
	rr := httptest.NewRecorder()
	rig.h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var n int
	if err := rig.h.db.QueryRow(
		`SELECT COUNT(*) FROM automations WHERE workspace_id = ? AND id LIKE 'aut_pgw_%'`, rig.wsID).Scan(&n); err != nil {
		t.Fatalf("count rules: %v", err)
	}
	if n != 1 {
		t.Fatalf("after two saves there are %d rules for one gate, want 1", n)
	}
}

// A gate removed from the spec loses its rule. A rule that outlived its gate
// would keep waking a crew for a threshold nobody declares any more.
func TestRemovingAGateRemovesItsRule(t *testing.T) {
	rig := newWakeRig(t, wakePanelsWith("", "devops"))

	req := pagesRequest(t, "PATCH", "/api/v1/pages/flotila", rig.wsID, rig.userID, "OWNER", `{
		"panels": [{
			"id": "sluzby",
			"schema": "status.v1",
			"owner": "crew/lookout",
			"producer": "script/watch.sh",
			"sla_seconds": 300
		}]
	}`)
	req.SetPathValue("slug", "flotila")
	rr := httptest.NewRecorder()
	rig.h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var n int
	if err := rig.h.db.QueryRow(
		`SELECT COUNT(*) FROM automations WHERE workspace_id = ? AND id LIKE 'aut_pgw_%'`, rig.wsID).Scan(&n); err != nil {
		t.Fatalf("count rules: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d rules survived the gate that declared them, want 0", n)
	}
}

func TestDeletingThePageRemovesItsRules(t *testing.T) {
	rig := newWakeRig(t, wakePanelsWith("", "devops"))

	req := pagesRequest(t, "DELETE", "/api/v1/pages/flotila", rig.wsID, rig.userID, "OWNER", "")
	req.SetPathValue("slug", "flotila")
	rr := httptest.NewRecorder()
	rig.h.Delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var n int
	if err := rig.h.db.QueryRow(
		`SELECT COUNT(*) FROM automations WHERE workspace_id = ? AND id LIKE 'aut_pgw_%'`, rig.wsID).Scan(&n); err != nil {
		t.Fatalf("count rules: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d rules outlived their page, want 0", n)
	}
}

// ── The authoring gate ─────────────────────────────────────────────────────

func TestWakeGateAuthoringRefusals(t *testing.T) {
	tests := []struct {
		name    string
		panel   string
		wantErr string
	}{
		{
			name: "a predicate the panel's schema cannot satisfy",
			panel: `{"id":"sluzby","schema":"status.v1","owner":"crew/lookout","producer":"script/w.sh","sla_seconds":30,
				"wake":[{"when":"value > 90","agent":"crew/devops"}]}`,
			wantErr: "could never match",
		},
		{
			name: "a crew that does not exist",
			panel: `{"id":"sluzby","schema":"status.v1","owner":"crew/lookout","producer":"script/w.sh","sla_seconds":30,
				"wake":[{"when":"any(state == \"critical\")","agent":"crew/nosuch"}]}`,
			wantErr: "no such crew exists here",
		},
		{
			name: "writes naming a panel that is not on the page",
			panel: `{"id":"sluzby","schema":"status.v1","owner":"crew/lookout","producer":"script/w.sh","sla_seconds":30,
				"wake":[{"when":"any(state == \"critical\")","agent":"crew/devops","writes":"nowhere"}]}`,
			wantErr: "not a panel on this page",
		},
		{
			name: "on_failure naming something that is not a crew",
			panel: `{"id":"sluzby","schema":"status.v1","owner":"crew/lookout","producer":"script/w.sh","sla_seconds":30,
				"on_failure":{"issue":"user/pavel"}}`,
			wantErr: "must be crew/",
		},
		{
			name: "on_failure naming a crew that does not exist",
			panel: `{"id":"sluzby","schema":"status.v1","owner":"crew/lookout","producer":"script/w.sh","sla_seconds":30,
				"on_failure":{"issue":"crew/nosuch"}}`,
			wantErr: "no such crew exists here",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _, wsID, userID := newPagesFixture(t)
			execOrFatal(t, h.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-devops', ?, 'DevOps', 'devops')`, wsID)

			req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER",
				`{"slug":"flotila","name":"Flotila","panels":[`+tc.panel+`]}`)
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.wantErr) {
				t.Errorf("body = %s, want it to name %q", rr.Body.String(), tc.wantErr)
			}
			var n int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM pages`).Scan(&n); err != nil {
				t.Fatalf("count pages: %v", err)
			}
			if n != 0 {
				t.Error("the page was saved despite the refusal")
			}
		})
	}
}

// ── Firing ─────────────────────────────────────────────────────────────────

// The headline: a critical push wakes the declared crew, and ONLY it.
func TestCriticalPushWakesTheDeclaredCrewAndOnlyIt(t *testing.T) {
	rig := newWakeRig(t, wakePanelsWith("", "devops"))

	rig.push(t, "ok", "ok")
	rig.reg.Flush(context.Background())
	if got := rig.issuesOn(t, rig.devopsID); got != 0 {
		t.Fatalf("an all-ok push opened %d issues, want 0", got)
	}

	rig.clock.advance(time.Minute)
	rig.push(t, "ok", "critical")
	rig.reg.Flush(context.Background())

	if got := rig.issuesOn(t, rig.devopsID); got != 1 {
		t.Fatalf("a critical push opened %d issues on crew/devops, want 1", got)
	}
	if got := rig.issuesOn(t, rig.opsID); got != 0 {
		t.Fatalf("crew/ops was woken %d times by a gate that does not name it, want 0", got)
	}
	if got := rig.issuesOn(t, "crew-lookout"); got != 0 {
		t.Fatalf("the panel's OWNER crew was woken %d times; the gate names who to wake, not the owner", got)
	}

	// The issue arrives assigned to the crew's LEAD agent, so it can be
	// started without an edit first.
	var assigneeType, assigneeID string
	if err := rig.h.db.QueryRow(
		`SELECT COALESCE(assignee_type,''), COALESCE(assignee_id,'') FROM missions WHERE crew_id = ?`,
		rig.devopsID).Scan(&assigneeType, &assigneeID); err != nil {
		t.Fatalf("load issue: %v", err)
	}
	if assigneeType != "agent" || assigneeID != "agent-devops-lead" {
		t.Errorf("assignee = (%q, %q), want the crew's LEAD agent", assigneeType, assigneeID)
	}

	// And the fire is in the journal, which is where "why did this issue
	// appear" gets answered.
	if e := rig.spy.firstOfType(journal.EntryPageWakeFired); e == nil {
		t.Error("no page.wake.fired entry was written")
	} else if e.Payload["panel"] != "sluzby" || e.Payload["crew"] != "devops" {
		t.Errorf("page.wake.fired payload = %v, want it to name the panel and the crew", e.Payload)
	}
}

// The same condition twice is ONE issue, and there are TWO independent reasons
// for that. The test asserts both, because in production only the second one
// always applies:
//
//	coalescing   two matching entries sharing a debounce key fold into one
//	             intent. The key is (rule, subject) and the subject ladder ends
//	             at the entry's crew for a page push — but a push made through
//	             the HTTP API carries a per-request OTel trace id, which ranks
//	             ABOVE the crew, so two pushes from two requests may well land
//	             on two keys. Inside one commit batch, or from one routine run,
//	             they do not.
//	the alert    while an issue is open for a gate, the opener refuses to open
//	             a second. This holds across keys, across flushes, across
//	             replicas and across a restart, because it is a row.
//
// Between them, a wake gate is safe to point at a panel pushed every five
// seconds.
func TestTheSameConditionTwiceInsideTheDebounceWindowFiresOnce(t *testing.T) {
	rig := newWakeRig(t, wakePanelsWith("", "devops"))

	// Two pushes three seconds apart: inside the rule's ten-second debounce
	// window, and outside the per-panel push floor.
	rig.push(t, "critical")
	rig.clock.advance(3 * time.Second)
	rig.push(t, "critical")

	if got := rig.reg.PendingIntents(); got != 1 {
		t.Fatalf("two matching pushes coalesced into %d intents, want 1", got)
	}
	if opened := rig.reg.Flush(context.Background()); opened != 1 {
		t.Fatalf("Flush opened %d issues for two pushes inside the window, want 1", opened)
	}
	if got := rig.issuesOn(t, rig.devopsID); got != 1 {
		t.Fatalf("%d issues, want 1", got)
	}

	// Past the window, with the intent already flushed, coalescing cannot help
	// — and the ALERT is what stops the second issue. This is the case a real
	// deployment is in most of the time.
	rig.clock.advance(time.Hour)
	rig.push(t, "critical")
	rig.reg.Flush(context.Background())
	if got := rig.issuesOn(t, rig.devopsID); got != 1 {
		t.Fatalf("%d issues after the condition persisted into a second window, want 1", got)
	}
}

// `for: 5m` must not fire at 4m59s. The whole point of the window is that one
// bad scrape wakes nobody, and a boundary that is off by one push is a
// boundary nobody can reason about.
func TestForWindowDoesNotFireEarly(t *testing.T) {
	rig := newWakeRig(t, wakePanelsWith("5m", "devops"))

	// t=0 critical. One sample is never enough for a 5m window.
	rig.push(t, "critical")
	rig.reg.Flush(context.Background())
	if got := rig.issuesOn(t, rig.devopsID); got != 0 {
		t.Fatalf("the first critical push fired a 5m gate; issues = %d, want 0", got)
	}

	// t=4m59s, still critical. The condition has held for 4m59s.
	rig.clock.advance(4*time.Minute + 59*time.Second)
	rig.push(t, "critical")
	rig.reg.Flush(context.Background())
	if got := rig.issuesOn(t, rig.devopsID); got != 0 {
		t.Fatalf("a 5m gate fired at 4m59s; issues = %d, want 0", got)
	}

	// Past the window. (Two seconds, not one: the per-panel push floor is the
	// smallest interval a producer may push at, and this test is about the
	// gate rather than about the rate limiter. The nanosecond boundary itself
	// — 5m fires, 5m minus 1ns does not — is pinned in
	// internal/pages/wake_test.go, where there is no rate limiter in the way.)
	rig.clock.advance(2 * time.Second)
	rig.push(t, "critical")
	rig.reg.Flush(context.Background())
	if got := rig.issuesOn(t, rig.devopsID); got != 1 {
		t.Fatalf("a 5m gate did not fire once the condition had held past 5m; issues = %d, want 1", got)
	}
}

// One good scrape in the middle restarts the clock on the window.
func TestForWindowRestartsWhenTheConditionClears(t *testing.T) {
	rig := newWakeRig(t, wakePanelsWith("5m", "devops"))

	rig.push(t, "critical")
	rig.clock.advance(4 * time.Minute)
	rig.push(t, "ok")
	rig.clock.advance(4 * time.Minute)
	rig.push(t, "critical")
	rig.reg.Flush(context.Background())

	if got := rig.issuesOn(t, rig.devopsID); got != 0 {
		t.Fatalf("issues = %d; the window must restart from the good push, not span it", got)
	}
}

// A gate that fired and then cleared must be able to fire again. A monitor
// that stops monitoring after its first incident is worse than none.
func TestAGateRearmsAfterTheConditionClears(t *testing.T) {
	rig := newWakeRig(t, wakePanelsWith("", "devops"))

	rig.push(t, "critical")
	rig.reg.Flush(context.Background())
	if got := rig.issuesOn(t, rig.devopsID); got != 1 {
		t.Fatalf("issues = %d after the first crossing, want 1", got)
	}

	rig.clock.advance(time.Minute)
	rig.push(t, "ok")
	rig.reg.Flush(context.Background())
	var alerts int
	if err := rig.h.db.QueryRow(`SELECT COUNT(*) FROM page_panel_alerts WHERE gate_key = 'wake:1'`).Scan(&alerts); err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if alerts != 0 {
		t.Fatal("the alert survived the condition clearing; the gate can never fire again")
	}

	rig.clock.advance(time.Minute)
	rig.push(t, "critical")
	rig.reg.Flush(context.Background())
	if got := rig.issuesOn(t, rig.devopsID); got != 2 {
		t.Fatalf("issues = %d after a second crossing, want 2", got)
	}
}

// ── The cost of not using it ───────────────────────────────────────────────

// A page with no wake and no on_failure must cost nothing on the push path.
// The handler here has NO DATABASE: any query the gate path attempted would
// panic, which is the strongest available statement of "it did not run".
func TestAPanelWithNoGatesCostsNothingOnThePushPath(t *testing.T) {
	h := &PageHandler{}
	panel := &panelRecord{RowID: "pp_1", PanelID: "sluzby", Schema: string(pages.SchemaStatus)}
	payload := &pages.StatusPayload{Items: []pages.StatusItem{{Name: "api", State: pages.StatusCritical}}}

	if got := h.wakeSignals(context.Background(), "ws_1", panel, payload, time.Now()); got != nil {
		t.Fatalf("wakeSignals on a gateless panel = %v, want nil", got)
	}
}

// And the journal entry a gateless push writes carries no wake keys — the
// matcher must have nothing to match, rather than matching something that
// happens to be false.
func TestAGatelessPushWritesNoWakeKeys(t *testing.T) {
	h, spy, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "flotila")

	req := pagesRequest(t, "PUT", "/api/v1/pages/flotila/panels/sluzby/data", wsID, userID, "OWNER", pagesStatusPayload)
	req.SetPathValue("slug", "flotila")
	req.SetPathValue("panelId", "sluzby")
	rr := httptest.NewRecorder()
	h.PushData(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("push: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	e := spy.firstOfType(journal.EntryPagePanelUpdated)
	if e == nil {
		t.Fatal("no page.panel.updated entry")
	}
	for k := range e.Payload {
		if strings.HasPrefix(k, "wake_") {
			t.Errorf("a gateless push carried %q into the journal payload", k)
		}
	}
}
