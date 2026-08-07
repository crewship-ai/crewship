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

func entry(workspace, eventType, mission string) journal.Entry {
	return journal.Entry{
		WorkspaceID: workspace,
		Type:        journal.EntryType(eventType),
		MissionID:   mission,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		Summary:     "test",
		Payload:     map[string]any{"to": "DONE"},
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
	if pr.DebounceKey != "auto:a1:m_1" {
		t.Errorf("debounce key = %q, want auto:a1:m_1", pr.DebounceKey)
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
