package api

// Pages — `refresh:`, end to end (docs/prd/pages.md §12 v1.1, and §6's worked
// example at line 422).
//
// The property every one of these defends is the same, and it is the reason
// the field exists at all: a `refresh:` the server stores and never acts on is
// worse than no field, because the author believes it works. So each test
// drives the REAL path — the page save compiles the rule, the real
// automation.Registry matches the real journal entry, and the assertion is on
// the row or the enqueued run rather than on an intention.

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
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// ── Fixture ────────────────────────────────────────────────────────────────

// pageRefreshEnqueueSpy is the only observable that says a refresh actually RAN
// something: automation.Registry parks a pipeline.PendingRun and nothing else.
type pageRefreshEnqueueSpy struct {
	runs []pipeline.PendingRun
}

func (s *pageRefreshEnqueueSpy) Enqueue(_ context.Context, pr pipeline.PendingRun) (string, bool, error) {
	s.runs = append(s.runs, pr)
	return pr.ID, true, nil
}

func (s *pageRefreshEnqueueSpy) slugs() []string {
	out := make([]string, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, r.PipelineSlug)
	}
	return out
}

// pageRefreshRig is the PRD's §6 example as a running page: a gated status panel
// produced by a script, and a narrative panel produced by a routine.
type pageRefreshRig struct {
	h      *PageHandler
	spy    *wakeJournalSpy
	reg    *automation.Registry
	enq    *pageRefreshEnqueueSpy
	wsID   string
	userID string
}

// prdRefreshPanels is docs/prd/pages.md:401-434 on the wire, with the
// narrative panel's `refresh:` made explicit by the case under test.
func prdRefreshPanels(refresh string) string {
	field := ""
	if refresh != "" {
		field = `, "refresh": "` + refresh + `"`
	}
	return `[{
		"id": "sluzby",
		"schema": "status.v1",
		"owner": "crew/lookout",
		"producer": "script/watch.sh",
		"sla_seconds": 300,
		"wake": [{"when": "any(state == \"critical\")", "agent": "crew/devops", "writes": "incident"}]
	}, {
		"id": "incident",
		"schema": "narrative.v1",
		"owner": "crew/devops",
		"producer": "routine/incident-rozbor",
		"sla_seconds": 3600` + field + `
	}]`
}

func newPageRefreshRig(t *testing.T, panels string) *pageRefreshRig {
	t.Helper()
	h, _, clock, wsID, userID := newPagesFixture(t)
	spy := &wakeJournalSpy{}
	h.SetJournal(spy)

	execOrFatal(t, h.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-devops', ?, 'DevOps', 'devops')`, wsID)
	seedAgentRow(t, h.db, "agent-devops-lead", wsID, "crew-devops", "DevOps Lead", "devops-lead", "LEAD")
	// The routine has to exist before a panel names it: resolveReferences is
	// the authoring gate and refuses a producer that does not resolve.
	execOrFatal(t, h.db, `INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		VALUES ('pl-rozbor', ?, 'incident-rozbor', 'Rozbor', '{}', 'hash')`, wsID)

	enq := &pageRefreshEnqueueSpy{}
	reg := automation.NewRegistry(automation.NewStore(h.db), enq, automation.Options{
		Logger: newTestLogger(),
		Now:    func() time.Time { return clock.now },
	})
	reg.SetIssueOpener(NewPagesWakeIssueOpener(h.db, nil, spy, newTestLogger()))
	spy.observer = reg.Observer
	h.SetAutomationRefresh(func(ctx context.Context) {
		if err := reg.Refresh(ctx); err != nil {
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
	return &pageRefreshRig{h: h, spy: spy, reg: reg, enq: enq, wsID: wsID, userID: userID}
}

func (r *pageRefreshRig) patch(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := pagesRequest(t, "PATCH", "/api/v1/pages/flotila", r.wsID, r.userID, "OWNER", body)
	req.SetPathValue("slug", "flotila")
	rr := httptest.NewRecorder()
	r.h.Update(rr, req)
	return rr
}

// push sends one status payload through the real write path.
func (r *pageRefreshRig) push(t *testing.T, state string) {
	t.Helper()
	req := pagesRequest(t, "PUT", "/api/v1/pages/flotila/panels/sluzby/data", r.wsID, r.userID, "OWNER",
		`{"items":[{"name":"api","state":"`+state+`"}]}`)
	req.SetPathValue("slug", "flotila")
	req.SetPathValue("panelId", "sluzby")
	rr := httptest.NewRecorder()
	r.h.PushData(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("push: status = %d, body: %s", rr.Code, rr.Body.String())
	}
}

// refreshRule reads the one compiled refresh rule, or fails.
func (r *pageRefreshRig) refreshRule(t *testing.T) automation.Automation {
	t.Helper()
	var (
		id, name, eventType, actionKind, matcher, action string
		debounce, maxPerHour, enabled                    int
	)
	err := r.h.db.QueryRow(`
		SELECT id, name, event_type, action_kind, matcher_json, action_config_json,
		       debounce_seconds, max_per_hour, enabled
		  FROM automations
		 WHERE workspace_id = ? AND action_kind = 'routine' AND id LIKE 'aut_pgw_%'`, r.wsID).
		Scan(&id, &name, &eventType, &actionKind, &matcher, &action, &debounce, &maxPerHour, &enabled)
	if err != nil {
		t.Fatalf("`refresh:` compiled to no automations row: %v", err)
	}
	out := automation.Automation{
		ID: id, Name: name, EventType: eventType, ActionKind: actionKind,
		DebounceSeconds: debounce, MaxPerHour: maxPerHour, Enabled: enabled == 1,
	}
	if err := json.Unmarshal([]byte(matcher), &out.Matcher); err != nil {
		t.Fatalf("matcher_json: %v", err)
	}
	if err := json.Unmarshal([]byte(action), &out.Action); err != nil {
		t.Fatalf("action_config_json: %v", err)
	}
	return out
}

func (r *pageRefreshRig) countRefreshRules(t *testing.T) int {
	t.Helper()
	var n int
	if err := r.h.db.QueryRow(
		`SELECT COUNT(*) FROM automations WHERE workspace_id = ? AND action_kind = 'routine' AND id LIKE 'aut_pgw_%'`,
		r.wsID).Scan(&n); err != nil {
		t.Fatalf("count refresh rules: %v", err)
	}
	return n
}

func (r *pageRefreshRig) countSpecChanged() int {
	n := 0
	for i := range r.spy.entries {
		if r.spy.entries[i].Type == journal.EntryPageSpecChanged {
			n++
		}
	}
	return n
}

// ── The declaration becomes a rule ─────────────────────────────────────────

// The whole feature in one assertion: `refresh:` is a TRIGGER, so it has to
// exist somewhere the server acts on — an `automations` row whose action runs
// the panel's own producer routine.
func TestRefreshOnWakeCompilesToARoutineAutomation(t *testing.T) {
	rig := newPageRefreshRig(t, prdRefreshPanels("on:wake"))

	rule := rig.refreshRule(t)
	if rule.EventType != string(journal.EntryPageWakeFired) {
		t.Errorf("event_type = %q, want %q — a refresh fires when a GATE fires, not on every push",
			rule.EventType, journal.EntryPageWakeFired)
	}
	if rule.ActionKind != automation.ActionKindRoutine {
		t.Errorf("action_kind = %q, want %q", rule.ActionKind, automation.ActionKindRoutine)
	}
	if rule.Action.RoutineSlug != "incident-rozbor" {
		t.Errorf("routine_slug = %q, want the PANEL'S OWN producer routine", rule.Action.RoutineSlug)
	}
	if !rule.Enabled {
		t.Error("the compiled refresh rule is disabled")
	}
	// Matched on the page and nothing else — which is what makes "any gate on
	// this page" ONE row rather than one per (panel × gate). The matcher is
	// exact-equality only, so the disjunction has to live in the EVENT.
	if len(rule.Matcher.PayloadEquals) != 1 || rule.Matcher.PayloadEquals["page_id"] == nil {
		t.Errorf("matcher = %v, want exactly {page_id}", rule.Matcher.PayloadEquals)
	}
	if rule.Action.Inputs["panel"] != "incident" {
		t.Errorf("inputs = %v, want the routine told which panel it is expected to write", rule.Action.Inputs)
	}
	// The burst controls are the substrate's, not new numbers: everything the
	// automations table already does about storms applies here for free.
	if rule.DebounceSeconds != automation.DefaultDebounceSeconds || rule.MaxPerHour != automation.DefaultMaxPerHour {
		t.Errorf("debounce/max_per_hour = %d/%d, want the substrate's defaults %d/%d",
			rule.DebounceSeconds, rule.MaxPerHour,
			automation.DefaultDebounceSeconds, automation.DefaultMaxPerHour)
	}
}

func TestRefreshOnPanelsChangedCompilesToTheSpecChangedEvent(t *testing.T) {
	rig := newPageRefreshRig(t, prdRefreshPanels("on:panels-changed"))

	rule := rig.refreshRule(t)
	if rule.EventType != string(journal.EntryPageSpecChanged) {
		t.Errorf("event_type = %q, want %q", rule.EventType, journal.EntryPageSpecChanged)
	}
	if rule.Action.RoutineSlug != "incident-rozbor" {
		t.Errorf("routine_slug = %q, want the panel's producer", rule.Action.RoutineSlug)
	}
}

// A page that declares no refresh must add no rules at all: the cost of this
// feature to a page that does not use it is zero rows.
func TestRefreshAbsentCompilesToNoRoutineRule(t *testing.T) {
	rig := newPageRefreshRig(t, prdRefreshPanels(""))

	if n := rig.countRefreshRules(t); n != 0 {
		t.Errorf("a page declaring no refresh compiled %d routine rules, want 0", n)
	}
}

// Removing `refresh:` has to remove the rule in the SAME save. A rule that
// outlives its declaration is a routine firing forever with nothing in the
// document to explain it.
func TestRemovingRefreshDeletesItsRule(t *testing.T) {
	rig := newPageRefreshRig(t, prdRefreshPanels("on:wake"))
	if n := rig.countRefreshRules(t); n != 1 {
		t.Fatalf("setup: %d refresh rules, want 1", n)
	}

	if rr := rig.patch(t, `{"panels": `+prdRefreshPanels("")+`}`); rr.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if n := rig.countRefreshRules(t); n != 0 {
		t.Errorf("%d refresh rules survived the declaration being removed, want 0", n)
	}
}

// Saving twice must not accumulate: the id is derived from the page and the
// panel, so the second save is a rewrite rather than an addition.
func TestSavingAPageTwiceKeepsOneRefreshRule(t *testing.T) {
	rig := newPageRefreshRig(t, prdRefreshPanels("on:wake"))

	if rr := rig.patch(t, `{"name": "Flotila (renamed)", "panels": `+prdRefreshPanels("on:wake")+`}`); rr.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if n := rig.countRefreshRules(t); n != 1 {
		t.Errorf("%d refresh rules after two saves, want 1", n)
	}
}

// A refresh rule and a wake gate on the same page must not collide on their
// derived id, or one of the two silently replaces the other.
func TestRefreshAndWakeRulesCoexistOnOnePage(t *testing.T) {
	rig := newPageRefreshRig(t, prdRefreshPanels("on:wake"))

	var n int
	if err := rig.h.db.QueryRow(
		`SELECT COUNT(*) FROM automations WHERE workspace_id = ? AND id LIKE 'aut_pgw_%'`, rig.wsID).Scan(&n); err != nil {
		t.Fatalf("count page rules: %v", err)
	}
	if n != 2 {
		t.Errorf("the page compiled %d rules, want 2 — one gate and one refresh", n)
	}
}

// A rollback is a SAVE (§10b.1 — it appends a version rather than truncating
// one), so it owes the same reconcile every other save does. Before this,
// rolling back to a version that declared a refresh restored the spec and left
// the page with no rule: the document said it refreshed and nothing did.
func TestRollingBackRestoresTheRefreshRule(t *testing.T) {
	rig := newPageRefreshRig(t, prdRefreshPanels("on:wake"))
	if n := rig.countRefreshRules(t); n != 1 {
		t.Fatalf("setup: %d refresh rules, want 1", n)
	}
	// v2 drops the trigger, and with it the rule.
	if rr := rig.patch(t, `{"panels": `+prdRefreshPanels("")+`}`); rr.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if n := rig.countRefreshRules(t); n != 0 {
		t.Fatalf("after dropping the trigger: %d refresh rules, want 0", n)
	}

	req := pagesRequest(t, "POST", "/api/v1/pages/flotila/rollback", rig.wsID, rig.userID, "OWNER", `{"to": 1}`)
	req.SetPathValue("slug", "flotila")
	rr := httptest.NewRecorder()
	rig.h.Rollback(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("rollback: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if n := rig.countRefreshRules(t); n != 1 {
		t.Errorf("%d refresh rules after rolling back to the version that declared one, want 1 — "+
			"the spec came back and the rule it compiles to did not", n)
	}
}

// ── The trigger actually fires ─────────────────────────────────────────────

// The end-to-end claim: a critical push arms the gate, the gate opens the issue
// and emits `page.wake.fired`, and THAT entry matches the refresh rule and
// enqueues the panel's producer. If any hop is wrong the page still renders and
// nothing says a routine did not run — which is why this asserts on the
// enqueued run and not on the rule.
func TestRefreshOnWakeEnqueuesTheProducerWhenAGateFires(t *testing.T) {
	rig := newPageRefreshRig(t, prdRefreshPanels("on:wake"))

	rig.push(t, "critical")
	// Two flushes, and the second is the point rather than an accident. The
	// first drains the GATE's intent, which opens the issue and emits
	// `page.wake.fired` — an entry that arrives while the first flush is
	// already draining, so it lands in the next batch. The running daemon
	// flushes on a 250 ms tick and pays the same one-tick latency.
	rig.reg.Flush(context.Background())
	rig.reg.Flush(context.Background())

	if len(rig.enq.runs) != 1 {
		t.Fatalf("the gate fired and %v producer runs were enqueued, want exactly 1", rig.enq.slugs())
	}
	if got := rig.enq.runs[0].PipelineSlug; got != "incident-rozbor" {
		t.Errorf("enqueued %q, want the refreshing panel's own producer", got)
	}
}

// A push that satisfies nothing fires no gate, so it must refresh nothing.
func TestRefreshOnWakeStaysQuietWhenNoGateFires(t *testing.T) {
	rig := newPageRefreshRig(t, prdRefreshPanels("on:wake"))

	rig.push(t, "ok")
	rig.reg.Flush(context.Background())
	rig.reg.Flush(context.Background())

	if len(rig.enq.runs) != 0 {
		t.Errorf("a healthy push enqueued %v; nothing fired", rig.enq.slugs())
	}
}

// ── The arrangement event ──────────────────────────────────────────────────

// `on:panels-changed` needs an event, and creating the page is the first
// arrangement change it ever has.
func TestPageCreateEmitsTheArrangementEvent(t *testing.T) {
	rig := newPageRefreshRig(t, prdRefreshPanels("on:panels-changed"))

	e := rig.spy.firstOfType(journal.EntryPageSpecChanged)
	if e == nil {
		t.Fatal("creating a page emitted no page.spec.changed entry; on:panels-changed could never fire")
	}
	if e.Payload["page_id"] == nil {
		t.Errorf("payload = %v, want the page_id the refresh rule matches on", e.Payload)
	}
	if e.Payload["created"] != true {
		t.Errorf("payload = %v, want created: true on the first arrangement", e.Payload)
	}
}

// A rename is not an arrangement change. Without this the trigger fires on
// every typo fix, which is the noise that makes a feature get turned off.
func TestRenamingAPageIsNotAnArrangementChange(t *testing.T) {
	rig := newPageRefreshRig(t, prdRefreshPanels("on:panels-changed"))
	before := rig.countSpecChanged()

	if rr := rig.patch(t, `{"name": "Flotila (renamed)"}`); rr.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if got := rig.countSpecChanged(); got != before {
		t.Errorf("a rename emitted %d arrangement entries, want none", got-before)
	}
}

// Re-sending the SAME panels is the shape a routine holding `page.write` would
// produce by re-applying its own manifest. It must emit nothing, or the refresh
// it triggers re-triggers itself. This is the on:panels-changed loop guard.
func TestReSavingTheSamePanelsIsNotAnArrangementChange(t *testing.T) {
	rig := newPageRefreshRig(t, prdRefreshPanels("on:panels-changed"))
	before := rig.countSpecChanged()

	if rr := rig.patch(t, `{"panels": `+prdRefreshPanels("on:panels-changed")+`}`); rr.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if got := rig.countSpecChanged(); got != before {
		t.Error("re-applying an identical panel list emitted an arrangement change; that is the loop")
	}
}

func TestEditingAPanelIsAnArrangementChange(t *testing.T) {
	rig := newPageRefreshRig(t, prdRefreshPanels("on:panels-changed"))
	before := rig.countSpecChanged()

	edited := strings.Replace(prdRefreshPanels("on:panels-changed"),
		`"id": "incident",`, `"id": "incident", "title": "Rozbor",`, 1)
	if rr := rig.patch(t, `{"panels": `+edited+`}`); rr.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if got := rig.countSpecChanged(); got <= before {
		t.Error("editing a panel emitted no arrangement change")
	}
}

// The arrangement event has to reach the rule, not merely be written. It is
// emitted after the registry reload for exactly this reason: a page created
// with a refresh on it would otherwise miss its own first arrangement.
func TestArrangementChangeEnqueuesTheProducer(t *testing.T) {
	rig := newPageRefreshRig(t, prdRefreshPanels("on:panels-changed"))
	rig.reg.Flush(context.Background())
	if len(rig.enq.runs) != 1 {
		t.Fatalf("creating the page enqueued %v, want the producer once", rig.enq.slugs())
	}

	edited := strings.Replace(prdRefreshPanels("on:panels-changed"),
		`"id": "incident",`, `"id": "incident", "title": "Rozbor",`, 1)
	if rr := rig.patch(t, `{"panels": `+edited+`}`); rr.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	rig.reg.Flush(context.Background())
	if len(rig.enq.runs) != 2 {
		t.Errorf("editing a panel enqueued %v, want the producer a second time", rig.enq.slugs())
	}
}

// ── The refusals, through the HTTP door ────────────────────────────────────

// internal/pages refuses these; this is the assertion that the HTTP door does
// too, with the reason in the body rather than a bare 400 — and that a refused
// save leaves no rule behind.
func TestRefreshRefusalsReachTheCaller(t *testing.T) {
	cases := []struct {
		name    string
		panels  string
		mustSay string
	}{
		{
			name:    "outside the closed set",
			panels:  prdRefreshPanels("on:push"),
			mustSay: "on:panels-changed",
		},
		{
			name: "a producer the server cannot run",
			panels: strings.Replace(prdRefreshPanels("on:wake"),
				`"producer": "routine/incident-rozbor"`, `"producer": "script/rozbor.sh"`, 1),
			mustSay: "routine/",
		},
		{
			name: "on:wake with no gate anywhere on the page",
			panels: strings.Replace(prdRefreshPanels("on:wake"),
				`"wake": [{"when": "any(state == \"critical\")", "agent": "crew/devops", "writes": "incident"}]`,
				`"title": "Jede to?"`, 1),
			mustSay: "wake",
		},
		{
			// The panel is status.v1 here so its own gate is well-formed —
			// otherwise ValidateGates refuses the predicate first and the loop
			// rule is never reached, which would make this test pass for the
			// wrong reason.
			name: "a panel that is its own sensor",
			panels: `[{
				"id": "sluzby",
				"schema": "status.v1",
				"owner": "crew/lookout",
				"producer": "routine/incident-rozbor",
				"sla_seconds": 300,
				"refresh": "on:wake",
				"wake": [{"when": "any(state == \"critical\")", "agent": "crew/devops"}]
			}]`,
			mustSay: "loop",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newPageRefreshRig(t, prdRefreshPanels(""))
			rr := rig.patch(t, `{"panels": `+tc.panels+`}`)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.mustSay) {
				t.Errorf("refusal does not name %q: %s", tc.mustSay, rr.Body.String())
			}
			if n := rig.countRefreshRules(t); n != 0 {
				t.Errorf("a refused save left %d refresh rules behind", n)
			}
		})
	}
}

// ── The round trip ─────────────────────────────────────────────────────────

// The bug this branch has already had four times: a field the read path does
// not echo is a field the editor deletes on the next save — and what is deleted
// here is the compiled rule, so the page goes on LOOKING like it refreshes.
func TestRefreshIsEchoedToACallerWhoMayEditTheSpec(t *testing.T) {
	rig := newPageRefreshRig(t, prdRefreshPanels("on:wake"))

	req := pagesRequest(t, "GET", "/api/v1/pages/flotila", rig.wsID, rig.userID, "OWNER", "")
	req.SetPathValue("slug", "flotila")
	rr := httptest.NewRecorder()
	rig.h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Panels []struct {
			ID      string `json:"id"`
			Refresh string `json:"refresh"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, p := range out.Panels {
		if p.ID == "incident" {
			if p.Refresh != "on:wake" {
				t.Errorf("refresh = %q on the read path, want on:wake — an editor saving this "+
					"document back would delete the trigger and its rule", p.Refresh)
			}
			return
		}
	}
	t.Fatal("the incident panel was not in the document")
}
