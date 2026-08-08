package automation

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// recordingEnqueuer counts calls and keeps every parked run so a test can
// assert on the exact number of enqueues, not merely on the final row count.
// The distinction is the whole point of the in-memory coalescing: pending_runs
// would have collapsed 200 INSERTs into one row and hidden 199 round-trips
// taken on the journal write path.
type recordingEnqueuer struct {
	mu    sync.Mutex
	calls []pipeline.PendingRun
	delay time.Duration
	err   error
}

func (e *recordingEnqueuer) Enqueue(_ context.Context, pr pipeline.PendingRun) (string, bool, error) {
	if e.delay > 0 {
		time.Sleep(e.delay)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return "", false, e.err
	}
	e.calls = append(e.calls, pr)
	return pr.ID, false, nil
}

func (e *recordingEnqueuer) n() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

func (e *recordingEnqueuer) at(i int) pipeline.PendingRun {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[i]
}

// recordingJournal captures throttle notices without a database.
type recordingJournal struct {
	mu      sync.Mutex
	entries []journal.Entry
}

func (j *recordingJournal) Emit(_ context.Context, e journal.Entry) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries = append(j.entries, e)
	return "j_test", nil
}

func (j *recordingJournal) Flush(context.Context) error { return nil }

func (j *recordingJournal) count(t journal.EntryType) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := 0
	for _, e := range j.entries {
		if e.Type == t {
			n++
		}
	}
	return n
}

func rule(id, workspace, eventType string) Resolved {
	return Resolved{
		Automation: Automation{
			ID:              id,
			WorkspaceID:     workspace,
			Name:            id,
			Enabled:         true,
			EventType:       eventType,
			ActionKind:      ActionKindRoutine,
			Action:          Action{RoutineSlug: "triage", Inputs: map[string]any{"issue": "{{ event.mission_id }}"}},
			DebounceSeconds: 10,
			MaxPerHour:      60,
		},
		PipelineID:   "pl_1",
		PipelineSlug: "triage",
	}
}

// entry builds a journal entry shaped like one the product really emits.
//
// The payload used to be {"to": "DONE"} — a shape no emitter in Crewship has
// ever produced. It was copied from the docs, and the docs had copied it from
// nowhere: internal/api's issueEvents.log writes mission.status_change with
// exactly {"action", "details"} (see TestIssueEvents_JournalPayloadIsWhatAutomationsMatchOn).
// So every matcher test in this package was passing against a fiction, which is
// why the documented first example — `--payload-equals to=DONE` — could ship in
// three places and match nothing.
//
// Keep this in step with the emitter. A helper that invents its own wire format
// tests the helper.
func entry(workspace, eventType, mission string) journal.Entry {
	return journal.Entry{
		WorkspaceID: workspace,
		Type:        journal.EntryType(eventType),
		MissionID:   mission,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		Summary:     "test",
		Payload:     map[string]any{"action": "status_changed", "details": "TODO → DONE"},
	}
}

// ---------------------------------------------------------------------------
// The write-path contract (internal/journal/emit.go:79-86)
// ---------------------------------------------------------------------------

// The observer runs inline on the journal write path. Two properties make
// that safe, and both are asserted here rather than assumed:
//
//   - it never reaches the database — the Registry is built with a NIL store,
//     so any lookup on this path would panic rather than quietly work;
//   - it never blocks — the Enqueuer parked behind it sleeps for a second per
//     call, and Observer must still return promptly, because it does not call
//     Enqueue at all.
func TestObserverNeverBlocksAndNeverTouchesTheDatabase(t *testing.T) {
	enq := &recordingEnqueuer{delay: time.Second}
	reg := NewRegistry(nil, enq, Options{})
	reg.Load([]Resolved{rule("a1", "ws_1", "mission.status_change")})

	entries := make([]journal.Entry, 0, 50)
	for i := 0; i < 50; i++ {
		entries = append(entries, entry("ws_1", "mission.status_change", "m_1"))
	}

	start := time.Now()
	reg.Observer(entries)
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Fatalf("Observer took %s on the journal write path; it must not block", elapsed)
	}
	if enq.n() != 0 {
		t.Fatalf("Observer performed %d enqueues inline; every I/O belongs in Flush", enq.n())
	}
	if reg.PendingIntents() != 1 {
		t.Fatalf("pending intents = %d, want 1", reg.PendingIntents())
	}
}

// The writer reuses the backing array the moment the observer returns
// (emit.go:79-86). Anything the observer keeps must therefore be a copy. This
// mutates every entry after the call and asserts the enqueued run still
// carries the values that were present at match time.
func TestObserverDoesNotRetainTheEntrySlice(t *testing.T) {
	enq := &recordingEnqueuer{}
	reg := NewRegistry(nil, enq, Options{})
	reg.Load([]Resolved{rule("a1", "ws_1", "mission.status_change")})

	entries := []journal.Entry{entry("ws_1", "mission.status_change", "m_1")}
	reg.Observer(entries)

	// The writer's next batch lands in the same array.
	entries[0] = entry("ws_9", "run.failed", "m_9")

	reg.Flush(context.Background())
	if enq.n() != 1 {
		t.Fatalf("enqueues = %d, want 1", enq.n())
	}
	var inputs map[string]any
	if err := json.Unmarshal([]byte(enq.at(0).InputsJSON), &inputs); err != nil {
		t.Fatalf("inputs: %v", err)
	}
	if inputs["issue"] != "m_1" {
		t.Errorf("rendered issue = %v, want m_1 — the observer read the slice after it was reused", inputs["issue"])
	}
	if got := enq.at(0).WorkspaceID; got != "ws_1" {
		t.Errorf("workspace = %q, want ws_1", got)
	}
}

// ---------------------------------------------------------------------------
// Burst control
// ---------------------------------------------------------------------------

// A status-change storm must produce one run, not 200 — and, just as
// importantly, ONE enqueue, not 200. pending_runs would have coalesced 200
// INSERTs into a single row, so counting rows would have declared success
// while the journal write path paid for 200 database round-trips.
func TestBurstOfTwoHundredEntriesProducesOneEnqueue(t *testing.T) {
	enq := &recordingEnqueuer{}
	reg := NewRegistry(nil, enq, Options{})
	reg.Load([]Resolved{rule("a1", "ws_1", "mission.status_change")})

	for i := 0; i < 200; i++ {
		reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", "m_1")})
	}
	if got := reg.PendingIntents(); got != 1 {
		t.Fatalf("pending intents after 200 matches = %d, want 1", got)
	}

	reg.Flush(context.Background())
	if got := enq.n(); got != 1 {
		t.Fatalf("enqueues = %d, want exactly 1", got)
	}
	pr := enq.at(0)
	// auto:<rule>:<subject-kind>:<subject>. The kind is in the key because the
	// subject is the most specific identity the entry carries, not always a
	// mission — see debounceKey.
	if pr.DebounceKey != "auto:a1:mission:m_1" {
		t.Errorf("debounce key = %q, want auto:a1:mission:m_1", pr.DebounceKey)
	}
	if pr.DebounceMaxAt == nil {
		t.Error("debounce_max_at is nil; a never-ending storm would defer the run forever")
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(pr.MetadataJSON), &meta); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if meta["coalesced_events"] != float64(200) {
		t.Errorf("coalesced_events = %v, want 200 — the run must be able to explain itself", meta["coalesced_events"])
	}
}

// Different missions are different work. Coalescing must key on the mission,
// not collapse a workspace's whole event stream into one run.
func TestDistinctMissionsProduceDistinctRuns(t *testing.T) {
	enq := &recordingEnqueuer{}
	reg := NewRegistry(nil, enq, Options{})
	reg.Load([]Resolved{rule("a1", "ws_1", "mission.status_change")})

	for _, m := range []string{"m_1", "m_2", "m_3"} {
		reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", m)})
	}
	reg.Flush(context.Background())
	if got := enq.n(); got != 3 {
		t.Fatalf("enqueues = %d, want 3", got)
	}
}

// ---------------------------------------------------------------------------
// Fences
// ---------------------------------------------------------------------------

// A disabled automation is not a slow automation. It never enters the index,
// so it can never match — asserted through the same door production uses
// (Load, which Refresh funnels through) rather than by checking a flag.
func TestDisabledAutomationNeverFires(t *testing.T) {
	enq := &recordingEnqueuer{}
	reg := NewRegistry(nil, enq, Options{})
	off := rule("a1", "ws_1", "mission.status_change")
	off.Enabled = false
	reg.Load([]Resolved{off})

	reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", "m_1")})
	reg.Flush(context.Background())

	if got := enq.n(); got != 0 {
		t.Fatalf("a disabled automation enqueued %d runs, want 0", got)
	}
}

// The tenant fence. An automation names a routine and fires it with data
// lifted out of a journal entry, so a rule matching another workspace's
// events would be a cross-tenant execution primitive — not a display bug.
func TestAutomationNeverMatchesAnotherWorkspacesEntry(t *testing.T) {
	enq := &recordingEnqueuer{}
	reg := NewRegistry(nil, enq, Options{})
	reg.Load([]Resolved{rule("a1", "ws_A", "mission.status_change")})

	reg.Observer([]journal.Entry{entry("ws_B", "mission.status_change", "m_1")})
	reg.Flush(context.Background())
	if got := enq.n(); got != 0 {
		t.Fatalf("workspace A's automation fired on workspace B's entry (%d enqueues)", got)
	}

	// Control: the same entry in its own workspace does fire, so the test
	// above is proving isolation rather than a matcher that never matches.
	reg.Observer([]journal.Entry{entry("ws_A", "mission.status_change", "m_1")})
	reg.Flush(context.Background())
	if got := enq.n(); got != 1 {
		t.Fatalf("enqueues after the in-workspace entry = %d, want 1", got)
	}
}

// A different event type is a different rule. One automation per event type
// is the schema's whole position on wildcards.
func TestAutomationNeverMatchesAnotherEventType(t *testing.T) {
	enq := &recordingEnqueuer{}
	reg := NewRegistry(nil, enq, Options{})
	reg.Load([]Resolved{rule("a1", "ws_1", "mission.status_change")})

	reg.Observer([]journal.Entry{entry("ws_1", "run.failed", "m_1")})
	reg.Flush(context.Background())
	if got := enq.n(); got != 0 {
		t.Fatalf("enqueues = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

// Over the cap the match is dropped, and the fact is recorded ONCE for the
// window. The second assertion is the load-bearing one: a storm that trips a
// 3/hour cap two hundred times must not write two hundred rows saying it was
// throttled, which is the same flood the cap exists to stop, relocated into
// the audit log.
func TestRateLimitDropsOverCapAndNoticesExactlyOncePerHour(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	enq := &recordingEnqueuer{}
	jrn := &recordingJournal{}
	reg := NewRegistry(nil, enq, Options{Journal: jrn, Now: func() time.Time { return now }})
	capped := rule("a1", "ws_1", "mission.status_change")
	capped.MaxPerHour = 3
	capped.DebounceSeconds = 1
	reg.Load([]Resolved{capped})

	// 200 events, each on its own mission so every one wants its own run.
	// The clock is frozen, so nothing ages out of the window.
	for i := 0; i < 200; i++ {
		reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", missionID(i))})
	}
	reg.Flush(context.Background())

	if got := enq.n(); got != 3 {
		t.Fatalf("enqueues = %d, want 3 (max_per_hour)", got)
	}
	if got := jrn.count(journal.EntryAutomationThrottled); got != 1 {
		t.Fatalf("automation.throttled entries = %d, want exactly 1 per automation per hour", got)
	}

	// Next hour: the budget resets and the notice is allowed to fire again.
	now = now.Add(time.Hour + time.Minute)
	reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", "m_next")})
	reg.Flush(context.Background())
	if got := enq.n(); got != 4 {
		t.Fatalf("enqueues after the window rolled = %d, want 4", got)
	}
}

// The budget is held in memory, per Registry — therefore per PROCESS, and
// therefore reset by a restart.
//
// This test PINS A LIMITATION rather than a guarantee, and is meant to fail if
// someone makes the counter durable. Read that failure as "update this test",
// not as a regression.
//
// It exists because `max_per_hour` reads like a promise the system cannot keep
// in the general case, and an unwritten limitation becomes an assumed feature.
// The scope is honest for the deployment that exists: Crewship's own store is
// SQLite, which is single-writer, so two daemons sharing one database is not a
// supported topology and "per process" and "per instance" are the same
// sentence today. They stop being the same sentence the moment a shared-store
// backend lands, and the cap would then be N× looser than configured with
// nothing reporting it.
//
// The restart half is the one that bites now: a daemon restarted every ten
// minutes has no effective hourly cap at all. The cap is a burst brake, not an
// accounting control, and the field doc says so.
func TestHourlyBudgetIsPerProcessAndDoesNotSurviveARestart(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	capped := rule("a1", "ws_1", "mission.status_change")
	capped.MaxPerHour = 2
	capped.DebounceSeconds = 1

	spend := func(reg *Registry, enq *recordingEnqueuer) int {
		for i := 0; i < 10; i++ {
			reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", missionID(i))})
		}
		reg.Flush(context.Background())
		return enq.n()
	}

	firstEnq := &recordingEnqueuer{}
	first := NewRegistry(nil, firstEnq, Options{Journal: &recordingJournal{}, Now: clock})
	first.Load([]Resolved{capped})
	if got := spend(first, firstEnq); got != 2 {
		t.Fatalf("first process enqueued %d, want 2 (max_per_hour)", got)
	}

	// A second Registry over the same rule and the same frozen hour. This is a
	// restarted daemon, and — were the topology supported — a second replica.
	secondEnq := &recordingEnqueuer{}
	second := NewRegistry(nil, secondEnq, Options{Journal: &recordingJournal{}, Now: clock})
	second.Load([]Resolved{capped})
	got := spend(second, secondEnq)

	if got == 0 {
		t.Fatalf("the budget survived the process boundary — max_per_hour is now durable; " +
			"that is an improvement, so update this test and the field doc on Automation.MaxPerHour")
	}
	if got != 2 {
		t.Fatalf("second process enqueued %d, want 2 — the budget is per process, so a fresh "+
			"process gets a fresh cap", got)
	}
}

// A refresh reloads the rules; it must NOT reload the counters. Rebuilding
// the rate map every 60s would reset the hourly budget every 60s, and
// max_per_hour would be unreachable by construction — a cap that is green in
// every unit test and absent in production.
func TestRefreshDoesNotResetTheHourlyBudget(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	enq := &recordingEnqueuer{}
	jrn := &recordingJournal{}
	reg := NewRegistry(nil, enq, Options{Journal: jrn, Now: func() time.Time { return now }})
	capped := rule("a1", "ws_1", "mission.status_change")
	capped.MaxPerHour = 2
	reg.Load([]Resolved{capped})

	reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", "m_1")})
	reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", "m_2")})
	reg.Flush(context.Background())

	// The 60s tick, or any write to any automation in any workspace.
	reg.Load([]Resolved{capped})

	reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", "m_3")})
	reg.Flush(context.Background())

	if got := enq.n(); got != 2 {
		t.Fatalf("enqueues = %d, want 2 — the refresh handed the automation a fresh budget", got)
	}
}

// A rule that is deleted must stop consuming memory. Its window is dropped on
// the refresh that no longer lists it.
func TestRefreshDropsCountersForRemovedRules(t *testing.T) {
	reg := NewRegistry(nil, &recordingEnqueuer{}, Options{})
	reg.Load([]Resolved{rule("a1", "ws_1", "mission.status_change")})
	reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", "m_1")})

	reg.Load(nil)

	reg.mu.Lock()
	n := len(reg.rate)
	reg.mu.Unlock()
	if n != 0 {
		t.Fatalf("rate windows after the rule was deleted = %d, want 0", n)
	}
}

// A storm that lasts across several flush rounds is still ONE run, so it must
// cost ONE unit of budget. Charging per flush round would burn a 60/hour
// budget in fifteen seconds of sustained activity.
func TestSustainedStormCostsOneBudgetUnit(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	enq := &recordingEnqueuer{}
	jrn := &recordingJournal{}
	reg := NewRegistry(nil, enq, Options{Journal: jrn, Now: func() time.Time { return now }})
	capped := rule("a1", "ws_1", "mission.status_change")
	capped.MaxPerHour = 2
	capped.DebounceSeconds = 10
	reg.Load([]Resolved{capped})

	// Twenty flush rounds, each 250ms apart, all inside the 10s debounce.
	for i := 0; i < 20; i++ {
		reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", "m_1")})
		reg.Flush(context.Background())
		now = now.Add(250 * time.Millisecond)
	}
	if got := jrn.count(journal.EntryAutomationThrottled); got != 0 {
		t.Fatalf("throttle notices = %d, want 0 — one run was charged %d times", got, got+1)
	}
}

// ---------------------------------------------------------------------------
// Enqueue shape
// ---------------------------------------------------------------------------

func TestFlushBuildsTheParkedRun(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	enq := &recordingEnqueuer{}
	reg := NewRegistry(nil, enq, Options{Now: func() time.Time { return now }})
	reg.Load([]Resolved{rule("a1", "ws_1", "mission.status_change")})

	reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", "m_1")})
	reg.Flush(context.Background())

	pr := enq.at(0)
	if pr.PipelineID != "pl_1" || pr.PipelineSlug != "triage" {
		t.Errorf("target = %q/%q, want pl_1/triage", pr.PipelineID, pr.PipelineSlug)
	}
	if want := now.Add(10 * time.Second); !pr.FireAt.Equal(want) {
		t.Errorf("fire_at = %s, want %s (now + debounce_seconds)", pr.FireAt, want)
	}
	if pr.ID == "" {
		t.Error("pending run id is empty; Enqueue rejects that")
	}
}

// An enqueue failure must not take the registry down or wedge the pending
// map — the next event has to be able to try again.
func TestFlushSurvivesAnEnqueueError(t *testing.T) {
	enq := &recordingEnqueuer{err: errors.New("db is locked")}
	reg := NewRegistry(nil, enq, Options{})
	reg.Load([]Resolved{rule("a1", "ws_1", "mission.status_change")})

	reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", "m_1")})
	if n := reg.Flush(context.Background()); n != 0 {
		t.Fatalf("Flush reported %d successful enqueues, want 0", n)
	}
	if reg.PendingIntents() != 0 {
		t.Error("a failed enqueue left the intent pending; it would be retried forever")
	}
}

func missionID(i int) string {
	return "m_" + string(rune('a'+i%26)) + "_" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// An automation-fired run must say an automation fired it.
//
// Before pending_runs carried attribution, PendingRunDispatcher labelled
// every deferred run `schedule` and pointed triggered_by_id at the pending
// row itself, so a rule was indistinguishable from a cron on every surface
// that reads the enum. The rule survived only as a shape inside
// metadata_json, which each reader had to reverse-engineer.
func TestFlush_StampsTheAutomationAsTheTrigger(t *testing.T) {
	enq := &recordingEnqueuer{}
	reg := NewRegistry(nil, enq, Options{})
	reg.Load([]Resolved{rule("a1", "ws_1", "mission.status_change")})

	reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", "m_1")})
	reg.Flush(context.Background())

	if got := enq.n(); got != 1 {
		t.Fatalf("enqueues = %d, want 1", got)
	}
	pr := enq.at(0)
	if pr.TriggeredVia != pipeline.TriggeredViaAutomation {
		t.Errorf("TriggeredVia = %q, want %q — a rule must not read as a cron",
			pr.TriggeredVia, pipeline.TriggeredViaAutomation)
	}
	if pr.TriggeredByID != "a1" {
		t.Errorf("TriggeredByID = %q, want the automation id a1 — pointing at the pending row "+
			"is what made every rule-fired run indistinguishable from a schedule", pr.TriggeredByID)
	}
}

// ---------------------------------------------------------------------------
// The composition budget across the automation edge
// ---------------------------------------------------------------------------

// A closed composition loop must stop at pipeline.MaxChainDepth.
//
// This is the shape MaxChainDepth exists for, spelled out in its own doc
// comment: "automation -> routine -> issue change -> automation". The chain
// leaves the process through the journal and comes back, so callPath cannot
// see it and the in-process nesting ceiling never fires. GuardChainDepth is
// meant to be the ONE budget both composed edges spend from.
//
// Today only the call_pipeline edge spends from it. The automation edge parks
// a PendingRun that carries no depth at all, so every hop is dispatched as a
// fresh chain root at chain_depth 0 and the cap is unreachable across the very
// boundary it was written for.
//
// Verified on a live instance on 2026-08-07: two rules ping-ponging one issue
// between TODO and IN_PROGRESS through a crewship issue.update step ran 59
// hops in five minutes, every run recording chain_depth 0 and its own
// chain_origin, and zero automation.depth_exceeded entries. The only thing
// that ever stops such a loop is max_per_hour — a throttle, not a cap, and one
// whose default (60/hour) a user is free to raise.
//
// The loop below is that instance run, in memory: each enqueued run is fed
// back in as the event it would have caused, with TraceID naming the parent
// run exactly as journal.prepareEntry sets it. EventContext already reads
// e.TraceID as "run_id", so the parent pointer this guard needs is present at
// the match site today — merely unused.
//
// MaxPerHour is set far above the hop budget on purpose: the rate limiter must
// not be what makes this test pass. A green run here has to mean the depth cap
// held.

// enqueuedDepths answers ChainOf out of what the recording enqueuer already
// holds. In production the position is read from pipeline_runs, written there
// when the deferred row fired; here the enqueued row IS the record, so this
// models the same fact without a database.
type enqueuedDepths struct{ enq *recordingEnqueuer }

func (d enqueuedDepths) ChainOf(_ context.Context, _, runID string) (ChainPos, bool, error) {
	for i := 0; i < d.enq.n(); i++ {
		if pr := d.enq.at(i); pr.ID == runID {
			return ChainPos{Depth: pr.ChainDepth, Origin: pr.ChainOrigin}, true, nil
		}
	}
	return ChainPos{}, false, nil
}

// Depth bounds a chain; origin is what makes it READ as one chain. A composed
// run that re-roots at itself turns eight linked hops into eight unrelated
// runs — unreadable, and the exact shape a loop would prefer to present.
//
// Three cases, and the middle one is the one that is easy to get wrong: when
// the parent is itself the root it has no origin of its own, so the child's
// origin is the PARENT'S ID, not the parent's (empty) origin field.
func TestFlush_ComposedRunsInheritTheChainOrigin(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	enq := &recordingEnqueuer{}
	reg := NewRegistry(nil, enq, Options{Journal: &recordingJournal{}, Now: func() time.Time { return now }})
	reg.SetChainSource(enqueuedDepths{enq})

	r := rule("a1", "ws_1", "mission.status_change")
	r.MaxPerHour = 10000
	r.DebounceSeconds = 1
	reg.Load([]Resolved{r})

	// Hop 0 — a human changed the issue. No parent run, so this run starts its
	// own chain and claims no origin.
	reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", "m_1")})
	reg.Flush(context.Background())
	if enq.n() != 1 {
		t.Fatalf("enqueues = %d, want 1", enq.n())
	}
	root := enq.at(0)
	if root.ChainOrigin != "" {
		t.Errorf("a human-caused run claimed origin %q; it roots its own chain", root.ChainOrigin)
	}

	// Hop 1 — the run above emits the status change its crewship step made.
	// The parent is the root, so the child's origin is the parent's ID.
	now = now.Add(2 * time.Second)
	e1 := entry("ws_1", "mission.status_change", "m_1")
	e1.TraceID = root.ID
	reg.Observer([]journal.Entry{e1})
	reg.Flush(context.Background())
	if enq.n() != 2 {
		t.Fatalf("enqueues = %d, want 2", enq.n())
	}
	hop1 := enq.at(1)
	if hop1.ChainOrigin != root.ID {
		t.Fatalf("ChainOrigin = %q, want %q — the first composed hop is rooted at the run "+
			"that caused it", hop1.ChainOrigin, root.ID)
	}

	// Hop 2 — now the parent HAS an origin, and the grandchild must inherit
	// that root rather than re-rooting at its immediate parent.
	now = now.Add(2 * time.Second)
	e2 := entry("ws_1", "mission.status_change", "m_1")
	e2.TraceID = hop1.ID
	reg.Observer([]journal.Entry{e2})
	reg.Flush(context.Background())
	if enq.n() != 3 {
		t.Fatalf("enqueues = %d, want 3", enq.n())
	}
	if got := enq.at(2).ChainOrigin; got != root.ID {
		t.Fatalf("ChainOrigin = %q, want %q — a chain has ONE root; inheriting the immediate "+
			"parent instead renumbers the chain every hop", got, root.ID)
	}
}

func TestObserver_ClosedLoopStopsAtMaxChainDepth(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	enq := &recordingEnqueuer{}
	jrn := &recordingJournal{}
	reg := NewRegistry(nil, enq, Options{Journal: jrn, Now: func() time.Time { return now }})
	reg.SetChainSource(enqueuedDepths{enq})

	r := rule("a1", "ws_1", "mission.status_change")
	r.MaxPerHour = 10000
	r.DebounceSeconds = 1
	reg.Load([]Resolved{r})

	// Hop 0: a human changes the issue. No parent run, so this edge is depth 1.
	reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", "m_1")})
	reg.Flush(context.Background())

	// Every subsequent hop is the run enqueued by the previous one emitting the
	// status change its crewship step performed, which re-matches the same rule.
	const hops = 30
	for i := 0; i < hops; i++ {
		if enq.n() == 0 {
			break
		}
		parent := enq.at(enq.n() - 1)
		now = now.Add(2 * time.Second) // past fire_at, so the key is charged afresh
		next := entry("ws_1", "mission.status_change", "m_1")
		next.TraceID = parent.ID // journal.prepareEntry: trace_id IS the originating run id
		reg.Observer([]journal.Entry{next})
		reg.Flush(context.Background())
	}

	// One enqueue for the human's change, then at most MaxChainDepth composed
	// hops on top of it.
	if want := pipeline.MaxChainDepth + 1; enq.n() > want {
		t.Fatalf("closed loop produced %d enqueues, want at most %d "+
			"(1 human-caused + %d composed hops): the automation edge does not spend "+
			"from the GuardChainDepth budget, so a composition cycle is bounded only "+
			"by max_per_hour", enq.n(), want, pipeline.MaxChainDepth)
	}
	if got := jrn.count(journal.EntryAutomationDepthExceeded); got == 0 {
		t.Errorf("no %s entry emitted; a refused edge that leaves no trace is "+
			"indistinguishable from a rule that silently stopped matching",
			journal.EntryAutomationDepthExceeded)
	}
}
