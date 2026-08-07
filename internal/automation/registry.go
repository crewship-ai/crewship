package automation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// ChainPos is re-exported from internal/pipeline, which owns both the columns
// (pipeline_runs.chain_depth / chain_origin) and the single GuardChainDepth
// that spends against them. Declaring a second one here would be a second
// answer to "how deep are we", and this package already imports pipeline, so
// the dependency costs nothing.
type ChainPos = pipeline.ChainPos

// ChainSource answers where the run that produced a triggering entry already
// sat. Read in Flush, never in Observer: Observer is on the journal write path.
//
// A seam rather than a *sql.DB so the registry stays testable without one,
// and so the query lives with the rows it reads.
type ChainSource interface {
	ChainOf(ctx context.Context, workspaceID, runID string) (ChainPos, bool, error)
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
	chains  ChainSource
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
	// ceiling holds, per debounce key, the debounce_max_at of the run that
	// key is currently accumulating into. It MUST survive a flush, for the
	// same reason `charged` does and with more at stake: `pending` is emptied
	// every 250ms, so an intent rebuilt from scratch recomputes its ceiling
	// relative to the new `now` four times a second, and a ceiling that moves
	// with the storm is not a ceiling. Without it a never-ending storm defers
	// its run forever AND — because `charged` is pruned against a fireAt that
	// slides the same way — never pays a second budget unit, which makes
	// max_per_hour unreachable by exactly the traffic it exists to bound.
	// Dropped in Flush alongside `charged`, once the run they describe is due.
	ceiling map[string]time.Time
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
	// originRunID is the run whose work produced the triggering entry, taken
	// from the entry at match time — NOT looked up, because Observer runs on
	// the journal write path. Flush prices the hop from it, off that path.
	originRunID string
	// chainDepth is what the enqueued run will start at: the origin run's
	// depth + 1, or 1 when a human caused the entry.
	chainDepth int
	// coalesced counts how many matched entries folded into this one run.
	// Carried into the run metadata so "why did this fire once for 200
	// events" is answerable from the run, not from a log grep.
	coalesced int
}

// NewRegistry builds a Registry. store may be nil for a registry that is
// loaded directly with Load (tests, and the proof that Observer never needs
// a database).
// SetChainSource injects the composition-budget reader. Optional: without one
// every automation hop starts at depth 1 and roots its own chain, which is the
// pre-existing behaviour and is stated as such rather than silently assumed
// safe.
func (r *Registry) SetChainSource(c ChainSource) { r.chains = c }

// emitDepthExceeded records a refused hop. Loud on purpose: a cap that
// refuses silently is a cap nobody can debug, and this entry is how an
// operator finds out a loop exists at all.
func (r *Registry) emitDepthExceeded(ctx context.Context, it *intent, depth int) {
	r.logger.Warn("automation: composed chain refused at the depth cap",
		"automation_id", it.automationID, "routine", it.pipelineSlug,
		"chain_depth", depth, "origin_run_id", it.originRunID)
	if r.journal == nil {
		return
	}
	_, _ = r.journal.Emit(ctx, journal.Entry{
		WorkspaceID: it.workspaceID,
		Type:        journal.EntryAutomationDepthExceeded,
		Severity:    journal.SeverityError,
		ActorType:   journal.ActorSystem,
		ActorID:     "automation",
		Summary: fmt.Sprintf("automation %q refused: composed chain would reach depth %d",
			it.automationName, depth),
		Payload: map[string]any{
			"automation_id":   it.automationID,
			"automation_name": it.automationName,
			"routine_slug":    it.pipelineSlug,
			"chain_depth":     depth,
			"max_chain_depth": pipeline.MaxChainDepth,
			"origin_run_id":   it.originRunID,
		},
	})
}

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
		ceiling:      map[string]time.Time{},
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
			key := debounceKey(rule.ID, e)
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
				// The unit just charged buys a NEW run: either this key had
				// none in flight, or the one it had is due. Retire its
				// ceiling here rather than leaving it to Flush — Observer and
				// Flush both notice "the run is due", and if only one of them
				// started the next window the boundary would be charged
				// twice, once by each.
				delete(r.ceiling, key)
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

// debounceKey is the coalescing identity: one automation, one SUBJECT.
//
// The subject has to be whatever the rendered inputs vary by, because
// coalescing keeps the LAST event's inputs and discards the rest — so two
// entries that share a key had better be about the same thing. mission_id
// alone covers that for the mission.* and issue.* types and for nothing else:
// run.failed, assignment.failed, guardrail.input_blocked, agent.mentioned and
// budget.warning all arrive with no mission and with the run, agent and crew
// the author's {{ event.* }} references actually name. Keyed on the mission
// alone, two unrelated failed runs became one incident about the second and
// the first was dropped with `coalesced_events: 2` recording it as intended.
//
// The ladder is by specificity, and it mirrors EventContext: mission, then run
// (trace_id, which is what EventContext exposes as run_id), then agent, then
// crew. The first one present wins — an entry that has a mission is about that
// mission whatever else it carries, and an entry with none of them really is
// workspace-scoped and collapses to the automation's own key, which is what
// the original comment described and the only case where it was right.
//
// The kind prefix is part of the key so an id that appears in two roles cannot
// alias, and so a key stays readable in a log line.
func debounceKey(automationID string, e journal.Entry) string {
	kind, subject := "ws", ""
	switch {
	case e.MissionID != "":
		kind, subject = "mission", e.MissionID
	case e.TraceID != "":
		kind, subject = "run", e.TraceID
	case e.AgentID != "":
		kind, subject = "agent", e.AgentID
	case e.CrewID != "":
		kind, subject = "crew", e.CrewID
	}
	return "auto:" + automationID + ":" + kind + ":" + subject
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
	// The ceiling on how long a never-ending storm may keep pushing the run
	// out. Without it a workspace that emits one matching event every
	// debounce_seconds-1 never fires the run at all, and the automation looks
	// broken rather than busy.
	//
	// It is read from `ceiling` rather than computed here because the intent
	// this method may be about to create is NOT necessarily the first for this
	// key: Flush empties `pending` every 250ms, so a storm rebuilds the intent
	// many times over one run's life. Anchoring the ceiling to the FIRST match
	// of the current run is what makes it fixed; computing it from `now` made
	// it move with the storm.
	maxAt, live := r.ceiling[key]
	if !live {
		maxAt = now.Add(time.Duration(rule.DebounceSeconds*10) * time.Second)
		r.ceiling[key] = maxAt
	}
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
			debounceMaxAt:  maxAt,
		}
		r.pending[key] = it
	}
	it.coalesced++
	it.fireAt = now.Add(time.Duration(rule.DebounceSeconds) * time.Second)
	if it.fireAt.After(it.debounceMaxAt) {
		it.fireAt = it.debounceMaxAt
	}
	it.inputsJSON = renderInputsJSON(rule.Action.Inputs, e)
	// Remember which run produced this entry so Flush can price the hop.
	// Read here, off the entry, because Observer runs on the journal write
	// path and must not touch the database; the LOOKUP happens in Flush.
	// Last writer wins: a coalesced burst fires once, and the deepest
	// contributor is the honest cost of that one run.
	if e.TraceID != "" {
		it.originRunID = e.TraceID
	}
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
	// on it belongs to a NEW run and must pay again. The ceiling goes with it:
	// the next run gets its own, measured from its own first match.
	//
	// `until` is bounded by the key's ceiling (coalesceLocked clamps fireAt to
	// it), so this prune actually arrives during a sustained storm. It did not
	// when the ceiling was recomputed per flush — fireAt outran `now` forever
	// and one budget unit covered an unbounded number of runs.
	for key, until := range r.charged {
		if !now.Before(until) {
			delete(r.charged, key)
			delete(r.ceiling, key)
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
		// Price the hop. The budget is ONE budget: pipeline.GuardChainDepth is
		// the same answer runCallPipelineStep spends from, so a cycle that leaves
		// the process through the journal and comes back cannot get a fresh
		// allowance by changing which door it uses.
		//
		// The lookup is here, in Flush, and never in Observer — Observer runs on
		// the journal write path and must not touch the database.
		depth := 1
		// Which chain this run joins. Empty means it roots its own, which is
		// correct for a human-caused entry and wrong for a composed one — see
		// the origin resolution below.
		origin := ""
		if it.originRunID != "" && r.chains != nil {
			if pos, ok, err := r.chains.ChainOf(ctx, it.workspaceID, it.originRunID); err != nil {
				// Fail CLOSED on an unreadable position. An unknown depth that
				// defaults to "shallow" is how a cycle buys itself a fresh
				// budget every lap.
				r.logger.Error("automation: chain position unreadable, refusing the hop",
					"err", err, "automation_id", it.automationID, "origin_run_id", it.originRunID)
				continue
			} else if ok {
				depth = pos.Depth + 1
				// A chain has ONE root. Inherit the parent's if it has one;
				// otherwise the parent IS the root, so name it. Taking the
				// immediate parent in both cases would renumber the chain
				// every hop, which is the bug this replaced.
				origin = pos.Origin
				if origin == "" {
					origin = it.originRunID
				}
			}
		}
		if err := pipeline.GuardChainDepth(depth); err != nil {
			r.emitDepthExceeded(ctx, it, depth)
			continue
		}
		it.chainDepth = depth

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
			// Say who fired this on the row. metadata_json still carries the
			// rule for readers that want its name, but provenance a UI has to
			// reverse-engineer out of a scratchpad is provenance that drifts.
			TriggeredVia:  pipeline.TriggeredViaAutomation,
			TriggeredByID: it.automationID,
			ChainDepth:    it.chainDepth,
			ChainOrigin:   origin,
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
