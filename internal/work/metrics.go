package work

// Instrumentation for the durable work ledger: the §10 counters that the
// ledger tables CANNOT answer, plus the operator-facing journal entries for
// the four transitions §10 says belong in the structured audit.
//
// Read this together with internal/server/metrics_domain_work.go, which is
// where most of §10's counters actually live. The split is deliberate and it
// has one rule:
//
//	If the value is derivable from work_items / work_attempts / work_events /
//	webhook_deliveries, it is a query on scrape and it is NOT here.
//
// That is the convention every other domain metric in this codebase follows
// (metrics_domain.go's own header), and it buys two things a process counter
// cannot: the number survives a restart, and it cannot drift from the rows it
// claims to describe. Queue depth, attempts, retries, lease losses,
// reconciliation entries and the ledger balance are all derivable, so none of
// them is a counter in this file.
//
// What is left here is exactly the set of events that leave NO row behind:
//
//   - a duplicate delivery. AcceptDeliveryTx answers a re-delivery with the
//     ORIGINAL receipt and writes nothing, which is the whole point (I2). The
//     second arrival is therefore unobservable in the schema.
//   - a rejected delivery. A bad signature, a blown acceptance budget or a
//     tripped ingress cap must not write a row — §5 forbids a `202` without a
//     durable commit, and a rejection that persisted state would be a half
//     acceptance. So the refusal exists only as an HTTP status.
//   - a cleanup failure after a run is already terminal. The work item is
//     finished and its row says so; the leaked tmux session or container does
//     not appear anywhere in this database.
//   - acceptance latency. §10 measures it "from the complete body to the HTTP
//     response", and neither end of that interval is a column: received_at is
//     stamped inside the transaction, and the response has no timestamp at
//     all. Only the handler holding the request can measure it.
//
// Everything in this file is process-local and resets on restart. That is
// correct for a counter whose alternative is not existing, and rate()/
// increase() absorb the reset the same way they absorb a scrape gap.

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// RejectReason is why an inbound unit of work was refused before anything was
// written. It is a CLOSED set on purpose: it becomes a Prometheus label, and a
// label fed from an error string is an unbounded series set waiting to happen.
// Anything unrecognized folds into RejectOther, matching the foldStatus
// convention the server's domain metrics already use.
type RejectReason string

const (
	// RejectSignature is a failed webhook signature, an unknown key id, or a
	// timestamp outside the accepted skew (§5, T01).
	RejectSignature RejectReason = "signature"
	// RejectBudget is ErrAcceptanceBudget: the acceptance transaction could
	// not commit inside its budget, so the sender was told to retry rather
	// than told `202` about work that does not exist (§5, T04).
	RejectBudget RejectReason = "budget"
	// RejectCapacity is an ingress limit — non-terminal work per endpoint or
	// per workspace, or the shared raw-input byte ceiling (§6).
	RejectCapacity RejectReason = "capacity"
	// RejectPayload is a malformed or oversized body (max 1 MiB, §6).
	RejectPayload RejectReason = "payload"
	// RejectUnauthorized is a permission or scope refusal at acceptance —
	// I8's "checked on accept AND on dispatch", failing at the first of the
	// two.
	RejectUnauthorized RejectReason = "unauthorized"
	// RejectOther is the overflow bucket that keeps the label set closed.
	RejectOther RejectReason = "other"
)

// RejectReasons is the canonical order the exposition emits, so every label
// value is always present (zero-filled) and a dashboard has a stable series
// set from the first scrape.
var RejectReasons = []RejectReason{
	RejectSignature, RejectBudget, RejectCapacity, RejectPayload, RejectUnauthorized, RejectOther,
}

// Fold normalizes r into the closed set.
func (r RejectReason) Fold() RejectReason {
	for _, known := range RejectReasons {
		if r == known {
			return r
		}
	}
	return RejectOther
}

// The counters themselves. Package-level and atomic, in the same shape as
// internal/api's credentialAuditDropped — the one existing end-to-end process
// counter in this codebase (exposed as crewshipd_credential_audit_dropped_total)
// — so there is one pattern to learn rather than two.
var (
	rejectedCounters   = newRejectCounters()
	duplicateCounter   atomic.Int64
	cleanupFailCounter atomic.Int64
	capacityViolations atomic.Int64
)

func newRejectCounters() map[RejectReason]*atomic.Int64 {
	m := make(map[RejectReason]*atomic.Int64, len(RejectReasons))
	for _, r := range RejectReasons {
		m[r] = new(atomic.Int64)
	}
	return m
}

// RecordRejected counts one unit of work refused before any durable write.
// Call it exactly once per refusal, at the point that decides the HTTP status
// — not once per handler layer, or the count stops meaning "requests refused".
func RecordRejected(reason RejectReason) { rejectedCounters[reason.Fold()].Add(1) }

// RecordDuplicate counts one re-delivery answered with the original receipt.
// This is a NORMAL event, not an error: a provider that did not hear the
// response resends, and suppressing the second one is the contract working.
// It is counted because §10 asks for "0 additional work items from an
// identical delivery in the dedup window" — a claim nobody can check without
// knowing how many duplicates were even seen.
func RecordDuplicate() { duplicateCounter.Add(1) }

// RecordCleanupFailure counts one failure to release a finished run's
// resources — a container that would not stop, a tmux session left behind, a
// workdir that could not be removed. §10's soak target is "no orphan
// processes, leaked slots or FD growth", and this is the only signal for the
// first of those three that does not require reading logs.
func RecordCleanupFailure() { cleanupFailCounter.Add(1) }

// RecordCapacityViolation counts one observation of the live set exceeding a
// cap it is supposed to make impossible.
//
// It should be permanently zero. A non-zero value is not a capacity problem to
// tune — it means the atomic admission in Claim (I3) did not hold, and the
// alarm is on the value being non-zero at all rather than on any threshold.
// The server's scrape-time collector calls this when it finds the ledger
// over-cap; a dispatcher that detects one directly should call it too.
func RecordCapacityViolation() { capacityViolations.Add(1) }

// Counters is one immutable read of every process counter in this file.
type Counters struct {
	Rejected           map[RejectReason]int64
	Duplicates         int64
	CleanupFailures    int64
	CapacityViolations int64
}

// ReadCounters snapshots the counters. Each field is read independently, so a
// snapshot taken during a burst can straddle an increment; that is fine for
// monotonic counters read by a scraper and is not worth a lock on the write
// path to avoid.
func ReadCounters() Counters {
	c := Counters{
		Rejected:           make(map[RejectReason]int64, len(RejectReasons)),
		Duplicates:         duplicateCounter.Load(),
		CleanupFailures:    cleanupFailCounter.Load(),
		CapacityViolations: capacityViolations.Load(),
	}
	for _, r := range RejectReasons {
		c.Rejected[r] = rejectedCounters[r].Load()
	}
	return c
}

// acceptanceLatencyWindow bounds the retained acceptance samples. §10 asks for
// p95/p99 of the acceptance path at 10 req/s; 1024 samples is ~100 s of that
// load, comfortably more than one scrape interval, and the whole buffer is
// 8 KiB. A bounded ring rather than a histogram because this codebase has no
// Prometheus client and therefore no histogram type — the same reason
// internal/server/metrics_percentile.go exists.
const acceptanceLatencyWindow = 1024

// acceptanceLatency is a fixed-size ring of the most recent acceptance
// durations, in seconds.
var acceptanceLatency struct {
	mu   sync.Mutex
	buf  [acceptanceLatencyWindow]float64
	next int
	n    int
}

// RecordAcceptanceLatency records one acceptance: the interval from the
// complete request body to the answer being ready, which is what §10's
// "Acceptance" row measures (p95 ≤500 ms, p99 ≤2 s).
//
// Record it for a REJECTION as well as for a `202`. The row is about the
// endpoint's answer time, and an acceptance path that answers 503 slowly is
// exactly the failure the target exists to catch; excluding refusals would
// make the metric flatter the worse things got.
//
// A negative duration (a clock stepping backwards mid-request) is dropped
// rather than recorded as a sample, because a percentile over impossible
// values is worse than one sample short.
func RecordAcceptanceLatency(d time.Duration) {
	if d < 0 {
		return
	}
	acceptanceLatency.mu.Lock()
	defer acceptanceLatency.mu.Unlock()
	acceptanceLatency.buf[acceptanceLatency.next] = d.Seconds()
	acceptanceLatency.next = (acceptanceLatency.next + 1) % acceptanceLatencyWindow
	if acceptanceLatency.n < acceptanceLatencyWindow {
		acceptanceLatency.n++
	}
}

// AcceptanceLatencySamples returns a copy of the retained samples, in no
// particular order — every consumer sorts anyway.
//
// An EMPTY result means "nothing has been accepted since this process
// started", and the caller must emit no quantile at all rather than a
// fabricated zero. §10 is explicit: absent functionality is marked N/A, not 0.
func AcceptanceLatencySamples() []float64 {
	acceptanceLatency.mu.Lock()
	defer acceptanceLatency.mu.Unlock()
	out := make([]float64, acceptanceLatency.n)
	copy(out, acceptanceLatency.buf[:acceptanceLatency.n])
	return out
}

// resetMetricsForTest zeroes every counter and sample in this file. Test-only,
// and unexported so it cannot be reached from a production call site.
func resetMetricsForTest() {
	for _, r := range RejectReasons {
		rejectedCounters[r].Store(0)
	}
	duplicateCounter.Store(0)
	cleanupFailCounter.Store(0)
	capacityViolations.Store(0)
	acceptanceLatency.mu.Lock()
	acceptanceLatency.next, acceptanceLatency.n = 0, 0
	acceptanceLatency.mu.Unlock()
}

// ── State sets, for the collectors that live outside this package ───────
//
// internal/server's /metrics collectors need the same state predicates the
// claim scan uses, and a second hand-written list of state strings over there
// would be a list that drifts. work.go already learned that lesson once — its
// SQL fragments are derived from HoldsExecutionSlot/OccupiesSession rather than
// typed out again, "because the first version of this package wrote the list
// inline in four queries and one of them disagreed with the other three".
// These accessors extend the same rule across the package boundary.

// AllStates returns every declared state in a fixed order. A caller that
// buckets states for reporting should assert its buckets cover this exactly,
// so adding a state to the machine reds that caller's test instead of quietly
// dropping the new state out of a total.
func AllStates() []State {
	out := make([]State, len(allStates))
	copy(out, allStates)
	return out
}

// StatesHoldingExecutionSlot returns the states that occupy one of the
// server's execution slots — the same set the capacity counting in Claim uses,
// needs_reconciliation included (see State.HoldsExecutionSlot for why).
func StatesHoldingExecutionSlot() []State {
	var out []State
	for _, s := range allStates {
		if s.HoldsExecutionSlot() {
			out = append(out, s)
		}
	}
	return out
}

// LeaseExpiredReasonPrefix is how RecoverExpiredLeases opens the reason it
// writes into work_events for BOTH of its outcomes. The /metrics lease-loss
// counter matches on it, which makes this an interface between the two files
// rather than an implementation detail of either — hence the named constant,
// and hence TestCollectWorkMetrics_LeaseLoss driving the real recovery path
// rather than inserting a hand-written reason string.
const LeaseExpiredReasonPrefix = "lease expired"

// ── Journal ─────────────────────────────────────────────────────────────
//
// §10: "IDs patří do strukturovaného auditu" — ids belong in the structured
// audit. These four entries are that audit. Every one carries the work id, and
// the three that have one carry the run id, in the PAYLOAD; none of them ever
// reaches a metric label.

// Emitter is the slice of *journal.Writer an Observer needs. Narrow on purpose:
// it keeps this package testable without a database, and it documents that
// nothing here reads the journal back.
type Emitter interface {
	Emit(ctx context.Context, e journal.Entry) (string, error)
}

// Observer writes the operator-facing journal entries for the ledger's
// dispatch decisions.
//
// It is best-effort by construction. A journal write that fails must never
// fail the dispatch decision it describes: the authoritative record is the
// work_events row, committed in the same transaction as the state change, and
// this stream is the human-readable view of it. A failure is logged and
// dropped, which is the same trade internal/api's credential audit makes — and
// like that one, the drop is visible, in the log rather than silently.
//
// A nil Observer is usable and does nothing, so a caller with no journal wired
// (a CLI, a test) needs no branch at every call site.
type Observer struct {
	emitter Emitter
	logger  *slog.Logger
}

// NewObserver returns an Observer emitting through e. A nil e yields a nil
// Observer, so "no journal configured" and "no observer" are the same thing to
// every caller.
func NewObserver(e Emitter, logger *slog.Logger) *Observer {
	if e == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Observer{emitter: e, logger: logger}
}

func (o *Observer) emit(ctx context.Context, e journal.Entry) {
	if o == nil || o.emitter == nil {
		return
	}
	if _, err := o.emitter.Emit(ctx, e); err != nil {
		o.logger.Warn("work: journal emit failed",
			"entry_type", e.Type, "workspace_id", e.WorkspaceID, "error", err)
	}
}

// Accepted records that a producer took durable responsibility for it. Call it
// AFTER the accepting transaction commits: an entry describing work that was
// rolled back is worse than no entry.
func (o *Observer) Accepted(ctx context.Context, it *Item) {
	if o == nil || it == nil {
		return
	}
	o.emit(ctx, journal.Entry{
		WorkspaceID: it.WorkspaceID,
		CrewID:      it.CrewID,
		AgentID:     it.AgentID,
		Type:        journal.EntryWorkAccepted,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		ActorID:     string(it.Source),
		Summary:     "work accepted from " + string(it.Source),
		Payload: map[string]any{
			"work_id":     it.ID,
			"source":      string(it.Source),
			"source_ref":  it.SourceRef,
			"class":       string(it.Class),
			"session_id":  it.SessionID,
			"priority":    it.Priority,
			"eligible_at": tsformat.Format(it.EligibleAt),
			"replay_of":   it.ReplayOf,
		},
		Refs: map[string]any{"work_id": it.ID},
	})
}

// Claimed records that capacity was reserved and one attempt started.
//
// TraceID is the run id, not a fresh value: work_attempts.run_id is the same
// namespace as agent_runs.id (see this package's doc comment), so setting it
// makes this entry land on the run's existing trace instead of starting a
// second one beside it.
func (o *Observer) Claimed(ctx context.Context, c *Claimed) {
	if o == nil || c == nil || c.Item == nil {
		return
	}
	it := c.Item
	o.emit(ctx, journal.Entry{
		WorkspaceID: it.WorkspaceID,
		CrewID:      it.CrewID,
		AgentID:     it.AgentID,
		Type:        journal.EntryWorkClaimed,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		ActorID:     "work-dispatcher",
		Summary:     "work claimed for attempt " + itoa(c.Attempt),
		TraceID:     c.RunID,
		Payload: map[string]any{
			"work_id":          it.ID,
			"run_id":           c.RunID,
			"generation":       c.Generation,
			"attempt":          c.Attempt,
			"class":            string(it.Class),
			"session_id":       it.SessionID,
			"lease_expires_at": tsformat.Format(c.LeaseUntil),
		},
		Refs: map[string]any{"work_id": it.ID, "run_id": c.RunID},
	})
}

// NeedsReconciliation records work parked because an external effect or a live
// runtime could not be safely resolved (§4).
//
// Severity is warn, and that is not decoration: this state keeps holding its
// execution slot until a human or a reconciler resolves it, so an operator who
// never sees it loses capacity permanently and silently.
func (o *Observer) NeedsReconciliation(ctx context.Context, it *Item, runID, locator, reason string) {
	if o == nil || it == nil {
		return
	}
	o.emit(ctx, journal.Entry{
		WorkspaceID: it.WorkspaceID,
		CrewID:      it.CrewID,
		AgentID:     it.AgentID,
		Type:        journal.EntryWorkNeedsReconciliation,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorSystem,
		ActorID:     "work-dispatcher",
		Summary:     "work needs reconciliation: " + reason,
		TraceID:     runID,
		Payload: map[string]any{
			"work_id":         it.ID,
			"run_id":          runID,
			"generation":      it.Generation,
			"attempt":         it.Attempts,
			"reason":          reason,
			"runtime_locator": locator,
		},
		Refs: map[string]any{"work_id": it.ID, "run_id": runID},
	})
}

// LeaseLost records an attempt whose lease expired without a heartbeat.
//
// requeued distinguishes the two outcomes RecoverExpiredLeases can reach, and
// they are not the same event to an operator: a requeue is self-healing, while
// the other branch has just parked the work with its capacity still held.
func (o *Observer) LeaseLost(ctx context.Context, it *Item, runID, locator string, requeued bool) {
	if o == nil || it == nil {
		return
	}
	severity := journal.SeverityWarn
	if requeued {
		// Recovery did its job and nothing is stuck; still worth an entry,
		// because a lease lost every few minutes is a sick worker whether or
		// not each individual loss healed.
		severity = journal.SeverityNotice
	}
	o.emit(ctx, journal.Entry{
		WorkspaceID: it.WorkspaceID,
		CrewID:      it.CrewID,
		AgentID:     it.AgentID,
		Type:        journal.EntryWorkLeaseLost,
		Severity:    severity,
		ActorType:   journal.ActorSystem,
		ActorID:     "work-recovery",
		Summary:     "work lease expired without a heartbeat",
		TraceID:     runID,
		Payload: map[string]any{
			"work_id":         it.ID,
			"run_id":          runID,
			"generation":      it.Generation,
			"attempt":         it.Attempts,
			"requeued":        requeued,
			"runtime_locator": locator,
		},
		Refs: map[string]any{"work_id": it.ID, "run_id": runID},
	})
}

// itoa avoids pulling strconv in for one summary string.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
