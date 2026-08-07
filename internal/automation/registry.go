package automation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// Enqueuer is the sink: everything an automation can do is park a deferred
// run. *pipeline.PendingRunStore satisfies it.
//
// Declared as an interface rather than taking the concrete store so a test
// can observe exactly how many enqueues one burst produced — which is the
// property the debounce design exists to hold, and the one a concrete
// dependency would make unobservable.
type Enqueuer interface {
	Enqueue(ctx context.Context, pr pipeline.PendingRun) (string, bool, error)
}

// loader supplies the rule set. The Registry only ever reads, and only off
// the write path.
type loader interface {
	ListActive(ctx context.Context) ([]Resolved, error)
}

// Options tunes a Registry. Zero values pick the documented defaults.
type Options struct {
	// RefreshInterval is how often the rule set is reloaded from the DB in
	// the background. Writes refresh immediately (the API handler calls
	// Refresh), so this tick is the backstop for a rule changed by another
	// replica, a restore, or a direct DB edit. Default 60s.
	RefreshInterval time.Duration
	// FlushInterval is how often coalesced intents are drained into
	// Enqueue. It is the width of the in-memory coalescing window: every
	// match landing inside one interval for the same debounce key becomes
	// one enqueue. Default 250ms.
	FlushInterval time.Duration
	// Journal receives the automation.throttled notices. Emitted from the
	// flusher goroutine, never from Observer — emitting a journal entry
	// from inside a journal commit observer is a re-entrant write on the
	// path we were told not to block.
	Journal journal.Emitter
	Logger  *slog.Logger
	// Now overrides the clock. Tests drive the hour window through it.
	Now func() time.Time
}

// Registry holds the enabled automations in memory and matches journal
// entries against them.
//
// One mutex guards everything. The critical section is a map lookup plus a
// few string comparisons per (entry × automation of that type), with no I/O
// inside it — an RWMutex would buy nothing because Observer mutates (the
// rate counters and the pending map) on the very path that would want the
// read lock.
type Registry struct {
	store   loader
	enq     Enqueuer
	journal journal.Emitter
	logger  *slog.Logger
	now     func() time.Time

	refreshEvery time.Duration
	flushEvery   time.Duration

	mu sync.Mutex
	// byEvent indexes the rule set by (workspace, event type) — the exact
	// question Observer asks, so the common case (an entry whose type no
	// automation subscribes to) costs one map lookup and nothing else.
	byEvent map[eventKey][]Resolved
	// rate holds the rolling hour window per automation id. It SURVIVES a
	// refresh: rebuilding it every 60s would reset the hourly counter every
	// 60s, and the cap would never be reached.
	rate map[string]*window
	// pending holds coalesced intents by debounce key, awaiting a flush.
	// Also survives a refresh.
	pending map[string]*intent
	// charged records, per debounce key, the fire time of the run that key
	// has ALREADY paid a rate-limit unit for. It is what makes the budget
	// count runs rather than flush rounds: `pending` is emptied every flush,
	// so without this a storm lasting ten seconds would be charged forty
	// times for the one run it actually produces. Pruned in Flush once the
	// run it refers to is due.
	charged map[string]time.Time
	// notices are automation.throttled entries the flusher will emit.
	notices []journal.Entry

	wake   chan struct{}
	stopMu sync.Mutex
	stop   chan struct{}
	wg     sync.WaitGroup
}

type eventKey struct {
	workspaceID string
	eventType   string
}

// window is one automation's rolling hour budget.
type window struct {
	start time.Time
	count int
	// noticed records that this window already wrote its ONE
	// automation.throttled entry. Without it a storm that trips the cap
	// 10,000 times writes 10,000 rows saying it was throttled, which is
	// the same flood the cap exists to stop, relocated.
	noticed bool
}

// intent is a run this registry has decided to enqueue, held in memory until
// the next flush so a burst collapses into one INSERT.
type intent struct {
	automationID   string
	automationName string
	eventType      string
	workspaceID    string
	pipelineID     string
	pipelineSlug   string
	debounceKey    string
	inputsJSON     string
	fireAt         time.Time
	debounceMaxAt  time.Time
	// coalesced counts how many matched entries folded into this one run.
	// Carried into the run metadata so "why did this fire once for 200
	// events" is answerable from the run, not from a log grep.
	coalesced int
}

// NewRegistry builds a Registry. store may be nil for a registry that is
// loaded directly with Load (tests, and the proof that Observer never needs
// a database).
func NewRegistry(store loader, enq Enqueuer, opts Options) *Registry {
	if opts.RefreshInterval <= 0 {
		opts.RefreshInterval = 60 * time.Second
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = 250 * time.Millisecond
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Registry{
		store:        store,
		enq:          enq,
		journal:      opts.Journal,
		logger:       opts.Logger,
		now:          opts.Now,
		refreshEvery: opts.RefreshInterval,
		flushEvery:   opts.FlushInterval,
		byEvent:      map[eventKey][]Resolved{},
		rate:         map[string]*window{},
		pending:      map[string]*intent{},
		charged:      map[string]time.Time{},
		wake:         make(chan struct{}, 1),
		stop:         make(chan struct{}),
	}
}

// Load installs a rule set directly, without reading the database. Refresh
// funnels through it, and so do tests.
func (r *Registry) Load(rules []Resolved) {
	idx := make(map[eventKey][]Resolved, len(rules))
	live := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if !rule.Enabled || rule.WorkspaceID == "" || rule.EventType == "" {
			continue
		}
		k := eventKey{rule.WorkspaceID, rule.EventType}
		idx[k] = append(idx[k], rule)
		live[rule.ID] = struct{}{}
	}
	r.mu.Lock()
	r.byEvent = idx
	// Drop counters for rules that no longer exist so a workspace that
	// churns automations does not leak a map entry per deleted rule. A rule
	// that is still here keeps its window — see the field comment.
	for id := range r.rate {
		if _, ok := live[id]; !ok {
			delete(r.rate, id)
		}
	}
	r.mu.Unlock()
}

// Refresh reloads the rule set from the store. Called on every write (so a
// newly created automation fires on the next event, not up to a minute
// later) and on the background tick.
func (r *Registry) Refresh(ctx context.Context) error {
	if r.store == nil {
		return nil
	}
	rules, err := r.store.ListActive(ctx)
	if err != nil {
		return err
	}
	r.Load(rules)
	return nil
}

// Start runs the refresh tick and the flush loop until ctx is cancelled or
// Stop is called. Safe to omit entirely in tests, which drive Refresh and
// Flush directly.
func (r *Registry) Start(ctx context.Context) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		refresh := time.NewTicker(r.refreshEvery)
		defer refresh.Stop()
		flush := time.NewTicker(r.flushEvery)
		defer flush.Stop()
		for {
			select {
			case <-ctx.Done():
				r.Flush(context.WithoutCancel(ctx))
				return
			case <-r.stop:
				r.Flush(context.WithoutCancel(ctx))
				return
			case <-refresh.C:
				if err := r.Refresh(ctx); err != nil {
					r.logger.Error("automation: registry refresh failed", "err", err)
				}
			case <-flush.C:
				r.Flush(ctx)
			case <-r.wake:
				// A match just landed. Do not flush immediately: the whole
				// point is to let the rest of the burst arrive first. The
				// wake exists so a registry with a long FlushInterval still
				// has a live signal to shorten it later if we want one.
			}
		}
	}()
}

// Stop halts the background loops and flushes what is pending. Idempotent.
func (r *Registry) Stop() {
	r.stopMu.Lock()
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	r.stopMu.Unlock()
	r.wg.Wait()
}

// Observer is the journal commit observer. Register it with
// journal.Writer.AddCommitObserver.
//
// It runs on the journal write path, so it does three things and no others:
// look up the rules for this (workspace, event type), evaluate them in
// memory, and record an intent. No database, no network, no blocking call,
// and every value it keeps is copied out of the entry before it returns —
// the backing array belongs to the writer the instant this function does.
func (r *Registry) Observer(entries []journal.Entry) {
	if len(entries) == 0 {
		return
	}
	now := r.now()
	matched := false

	r.mu.Lock()
	for i := range entries {
		e := entries[i]
		rules := r.byEvent[eventKey{e.WorkspaceID, string(e.Type)}]
		if len(rules) == 0 {
			continue
		}
		for _, rule := range rules {
			if !rule.Matcher.Matches(e) {
				continue
			}
			key := debounceKey(rule.ID, e.MissionID)
			// The rate budget is spent on RUNS, not on matched events: a
			// burst that coalesces into one run costs one unit. Debounce
			// already governs the storm; making max_per_hour count events
			// too would mean a single 200-event burst exhausted a 60/hour
			// budget while producing one run, and the two controls would
			// be fighting over the same job.
			if until, paid := r.charged[key]; !paid || !now.Before(until) {
				if !r.admitLocked(rule, now) {
					continue
				}
			}
			r.coalesceLocked(rule, e, key, now)
			r.charged[key] = r.pending[key].fireAt
			matched = true
		}
	}
	r.mu.Unlock()

	if matched {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

// debounceKey is the coalescing identity: one automation, one mission. An
// entry with no mission (a budget warning, a container error) collapses to
// the automation's own key, which is the intended behaviour — those events
// are about the workspace, not about a row.
func debounceKey(automationID, missionID string) string {
	return "auto:" + automationID + ":" + missionID
}

// admitLocked spends one unit of the automation's hourly budget, or refuses
// and (at most once per window) queues the throttle notice. Caller holds mu.
func (r *Registry) admitLocked(rule Resolved, now time.Time) bool {
	w := r.rate[rule.ID]
	if w == nil || now.Sub(w.start) >= time.Hour {
		w = &window{start: now}
		r.rate[rule.ID] = w
	}
	if w.count >= rule.MaxPerHour {
		if !w.noticed {
			w.noticed = true
			r.notices = append(r.notices, throttleEntry(rule, w.start))
		}
		return false
	}
	w.count++
	return true
}

// coalesceLocked folds one matched entry into the pending intent for its
// debounce key. The LATEST event wins the rendered inputs, matching
// pending_runs' own coalesce semantics (a run fires with the payload of the
// event that most recently extended it). Caller holds mu.
func (r *Registry) coalesceLocked(rule Resolved, e journal.Entry, key string, now time.Time) {
	it, ok := r.pending[key]
	if !ok {
		it = &intent{
			automationID:   rule.ID,
			automationName: rule.Name,
			eventType:      rule.EventType,
			workspaceID:    rule.WorkspaceID,
			pipelineID:     rule.PipelineID,
			pipelineSlug:   rule.PipelineSlug,
			debounceKey:    key,
			// The ceiling on how long a never-ending storm may keep
			// pushing the run out. Without it a workspace that emits one
			// matching event every debounce_seconds-1 never fires the run
			// at all, and the automation looks broken rather than busy.
			debounceMaxAt: now.Add(time.Duration(rule.DebounceSeconds*10) * time.Second),
		}
		r.pending[key] = it
	}
	it.coalesced++
	it.fireAt = now.Add(time.Duration(rule.DebounceSeconds) * time.Second)
	if it.fireAt.After(it.debounceMaxAt) {
		it.fireAt = it.debounceMaxAt
	}
	it.inputsJSON = renderInputsJSON(rule.Action.Inputs, e)
}

// Flush drains the coalesced intents into the Enqueuer and emits any queued
// throttle notices. Returns the number of enqueues performed.
//
// This is where every I/O the trigger path performs happens, and it is
// deliberately NOT on the journal write path.
func (r *Registry) Flush(ctx context.Context) int {
	now := r.now()
	r.mu.Lock()
	pending := r.pending
	notices := r.notices
	r.pending = map[string]*intent{}
	r.notices = nil
	// A key whose run is now due has spent its budget unit; the next match
	// on it belongs to a NEW run and must pay again.
	for key, until := range r.charged {
		if !now.Before(until) {
			delete(r.charged, key)
		}
	}
	r.mu.Unlock()

	n := 0
	for _, it := range pending {
		if r.enq == nil {
			continue
		}
		meta, _ := json.Marshal(map[string]any{
			"automation_id":      it.automationID,
			"automation_name":    it.automationName,
			"trigger_event_type": it.eventType,
			"coalesced_events":   it.coalesced,
		})
		// TODO(chain-depth): an automation-fired run should inherit
		// chain_depth = (depth of the run that produced the triggering entry)
		// + 1, or 0 when a human caused it, and be REFUSED with
		// journal.EntryAutomationDepthExceeded past the cap. The counter is
		// deliberately not started here: composition makes cycles trivially
		// constructible (automation → routine → comment → automation), and
		// the only thing that keeps them bounded is that every composed edge
		// joins ONE budget. That budget lives next to runCallPipelineStep's
		// existing callPath guard in internal/pipeline/executor.go — a second
		// counter in this package would be a second answer to "how deep are
		// we", which is the failure mode the single guard exists to prevent.
		// pending_runs carries no depth column today; when executor.go's
		// chain_depth / chain_origin land, thread them through PendingRun
		// rather than reintroducing the count here.
		maxAt := it.debounceMaxAt
		if _, _, err := r.enq.Enqueue(ctx, pipeline.PendingRun{
			ID:            newPendingRunID(),
			WorkspaceID:   it.workspaceID,
			PipelineID:    it.pipelineID,
			PipelineSlug:  it.pipelineSlug,
			InputsJSON:    it.inputsJSON,
			MetadataJSON:  string(meta),
			DebounceKey:   it.debounceKey,
			FireAt:        it.fireAt,
			DebounceMaxAt: &maxAt,
		}); err != nil {
			r.logger.Error("automation: enqueue failed",
				"err", err, "automation_id", it.automationID,
				"routine", it.pipelineSlug, "workspace_id", it.workspaceID)
			continue
		}
		n++
	}

	for _, e := range notices {
		if r.journal == nil {
			break
		}
		if _, err := r.journal.Emit(ctx, e); err != nil {
			r.logger.Error("automation: throttle notice emit failed", "err", err)
		}
	}
	return n
}

// PendingIntents reports how many distinct runs are currently coalesced and
// awaiting a flush. Exported for the tests that assert the coalescing itself,
// and cheap enough to be useful from a debug endpoint later.
func (r *Registry) PendingIntents() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}

func throttleEntry(rule Resolved, windowStart time.Time) journal.Entry {
	return journal.Entry{
		WorkspaceID: rule.WorkspaceID,
		Type:        journal.EntryAutomationThrottled,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorSystem,
		ActorID:     "automation",
		Summary:     "Automation " + rule.Name + " hit its hourly limit and is dropping matches",
		Payload: map[string]any{
			"automation_id":     rule.ID,
			"automation_name":   rule.Name,
			"event_type":        rule.EventType,
			"max_per_hour":      rule.MaxPerHour,
			"window_started_at": windowStart.UTC().Format(time.RFC3339),
		},
	}
}

func newPendingRunID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "pr_auto_" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return "pr_auto_" + hex.EncodeToString(b)
}
