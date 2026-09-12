package server

// §10 metrics for the durable work ledger (WEBHOOKS-AGENT-PARALLELISM-
// IMPLEMENTATION-1-0.md): "Počítadla: accept/reject/duplicate, queue
// length/oldest age podle třídy, attempts/retries, lease loss, reconciliation,
// memory conflict, WAL/checkpoint a cleanup failure" plus the release-target
// rows of the same section's table — acceptance latency, queue age, start
// latency, the capacity invariant, and the ledger balance.
//
// The label rule is the section's own sentence and it is a hard constraint,
// not a style note:
//
//	"Metriky exportovat bez raw payloadu, credentials a high-cardinality run
//	IDs v labels. IDs patří do strukturovaného auditu."
//
// So every label in this file comes from a closed set — a work state, a class,
// a bucket name, a reject reason — and no work id, run id, workspace id, agent
// id or session id ever appears in one. Those live in the journal entries
// internal/work/metrics.go emits (work.accepted / work.claimed /
// work.needs_reconciliation / work.lease_lost), which is the "strukturovaný
// audit" the sentence sends them to. TestCollectWorkMetrics_NoIDShapedLabels
// enforces the rule on the rendered text rather than on reviewer attention,
// because this is exactly the kind of constraint that rots one careless label
// at a time.
//
// Almost everything here is a query on scrape rather than a process counter,
// following metrics_domain.go's own convention: the ledger already holds the
// authoritative history, so a derived number survives a restart and cannot
// drift from the rows it describes. The four values that genuinely leave no
// row behind — duplicate deliveries, rejections, cleanup failures and
// acceptance latency — come from internal/work/metrics.go, which explains at
// length why each of them cannot be derived.
//
// Shared conventions with the rest of the block: writePromMetric for a family,
// writeQuantileMetric for a p50/p95 pair that is ABSENT rather than zero when
// there is no sample, percentileWindowRows for the bounded window, and
// zero-filled closed label sets so a dashboard has a stable series set from
// the first scrape against an empty database.

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/work"
)

// workStateSet is every state the ledger's CHECK constraint allows, in
// work.AllStates order. Taken from the work package rather than typed out
// again: a state added to the machine must not silently vanish from this
// gauge, and TestWorkLedgerBucketsCoverEveryState pins the coverage.
var workStateSet = func() []string {
	states := work.AllStates()
	out := make([]string, 0, len(states))
	for _, s := range states {
		out = append(out, string(s))
	}
	return out
}()

// workClassSet is §6's two capacity classes. Chat has a reservation background
// may not borrow, which is the entire reason queue depth and queue age are
// reported per class rather than as one number: a background backlog that
// hides a starved chat queue is the failure mode the split exists to expose.
var workClassSet = []string{string(work.ClassChat), string(work.ClassBackground)}

// workLedgerBuckets is §10's balance identity, spelled as the six terms the
// contract names: "Bilance accepted = queued/active/waiting/retry/
// reconciliation/terminal".
//
// The lists are explicit rather than derived from a predicate, because the
// whole point of the identity is to be an INDEPENDENT statement about the
// ledger. A bucket set generated from the same helper the sum is taken with
// could never disagree with itself, and a metric that cannot be wrong is not a
// check. TestWorkLedgerBucketsCoverEveryState asserts these six lists
// partition work.AllStates() exactly — no state missing, none counted twice.
var workLedgerBuckets = []struct {
	name   string
	states []string
}{
	{"queued", []string{"queued"}},
	{"active", []string{"starting", "running"}},
	{"waiting", []string{"waiting"}},
	{"retry", []string{"retry_wait"}},
	{"reconciliation", []string{"needs_reconciliation"}},
	{"terminal", []string{"succeeded", "failed", "expired", "cancelled"}},
}

// workDeliveryDecisionSet mirrors webhook_deliveries.filter_decision's CHECK.
// An ignored delivery is recorded rather than dropped (§5), so both values are
// real answers and both are always emitted.
var workDeliveryDecisionSet = []string{"accepted", "ignored"}

// memoryMutationStateSet mirrors memory_mutations.state's CHECK. `conflicted`
// is §10's "memory conflict" row; `intent` and `renamed` together are the
// unsettled recovery backlog, which is the number that says whether the
// recovery protocol in §8 is keeping up.
var memoryMutationStateSet = []string{"intent", "renamed", "confirmed", "conflicted"}

// workLeaseLossOutcomeSet is the two ways RecoverExpiredLeases can settle an
// expired lease. They are not the same event: `requeued` healed itself, while
// `reconciliation` parked the work with its execution slot still held.
var workLeaseLossOutcomeSet = []string{"requeued", "reconciliation"}

// sqlWorkStatesHoldingSlot is the quoted state list for "occupies an execution
// slot", built from work.StatesHoldingExecutionSlot so the capacity metrics
// count exactly what Claim counts — needs_reconciliation included.
var sqlWorkStatesHoldingSlot = func() string {
	states := work.StatesHoldingExecutionSlot()
	quoted := make([]string, 0, len(states))
	for _, s := range states {
		quoted = append(quoted, "'"+string(s)+"'")
	}
	return strings.Join(quoted, ",")
}()

// collectWorkLedgerMetrics is the §10 fan-out, called from
// collectDomainMetrics alongside the other domain collectors.
func (s *Server) collectWorkLedgerMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	s.collectWorkStateMetrics(ctx, b, hostname)
	s.collectWorkQueueMetrics(ctx, b, hostname)
	s.collectWorkAttemptMetrics(ctx, b, hostname)
	s.collectWorkDeliveryMetrics(ctx, b, hostname)
	s.collectWorkCapacityMetrics(ctx, b, hostname)
	s.collectWorkLedgerBalanceMetrics(ctx, b, hostname)
	s.collectWorkLatencyMetrics(ctx, b, hostname)
	s.collectMemoryMutationMetrics(ctx, b, hostname)
	s.collectWALMetrics(ctx, b, hostname)
	s.collectWorkProcessCounters(b, hostname)
}

// parseLedgerTimestamp parses a timestamp written by internal/work.
//
// Deliberately NOT parseWriteTimestamp: that helper documents itself as
// parsing the fractionless time.RFC3339 the mention/delivery write paths
// produce, whereas the work ledger writes tsformat.Layout — RFC 3339 with a
// fixed nine-digit fraction, so string order matches time order in the SQL
// comparisons the dispatch scan does. RFC3339Nano parses that form unchanged,
// and is what internal/work's own reader uses.
//
// A row that fails to parse is skipped by the caller, never zero-filled.
func parseLedgerTimestamp(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s) // tsformat:allow: parses an existing ledger timestamp; does not format a SQL parameter
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ── States, queue depth and queue age ───────────────────────────────────

// collectWorkStateMetrics reports how many work items sit in each state.
// §10's "reconciliation" counter is the needs_reconciliation label here: a
// state that holds its execution slot until a human resolves it, so its
// steady-state value is 0 and any growth is capacity being lost.
func (s *Server) collectWorkStateMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	counts := map[string]float64{}
	if s.db != nil {
		rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM work_items GROUP BY state`)
		if err != nil {
			s.logger.Warn("metrics: work items count failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var state string
				var n float64
				if err := rows.Scan(&state, &n); err != nil {
					s.logger.Warn("metrics: work items scan failed", "error", err)
					break
				}
				// No "other" fold: the column has a CHECK constraint listing
				// exactly these ten values, so an unrecognized state is not a
				// cardinality risk — it is impossible, and silently bucketing
				// it would hide a schema change that must be noticed.
				counts[state] += n
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: work items rows failed", "error", err)
			}
		}
	}
	writePromMetric(b, "crewshipd_work_items",
		"Work items currently in each ledger state (§4 state machine)", "gauge", hostname,
		statusSamples(workStateSet, "state", counts))
}

// collectWorkQueueMetrics reports §10's "queue length / oldest age podle
// třídy" for the two capacity classes.
//
// Both queued and retry_wait count as queue depth: an item waiting out its
// backoff is work the server has accepted and not yet done, and leaving it out
// would make a retry storm look like an emptying queue.
//
// Age is measured from created_at, not from eligible_at, and that is the
// "celkový queue age" §10 asks to report separately from CLI preparation time:
// eligible_at is rewritten by every retry, so an age taken from it would reset
// the clock on exactly the items that have been waiting longest.
//
// An empty class emits age 0. That is a real answer — nothing is waiting — not
// a placeholder, and keeping the series present means an alert can compare
// against it from the first scrape.
func (s *Server) collectWorkQueueMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	depth := map[string]float64{}
	age := map[string]float64{}
	if s.db != nil {
		now := time.Now().UTC()
		rows, err := s.db.QueryContext(ctx, `
			SELECT class, COUNT(*), MIN(created_at)
			  FROM work_items
			 WHERE state IN ('queued','retry_wait')
			 GROUP BY class`)
		if err != nil {
			s.logger.Warn("metrics: work queue depth failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var class, oldest string
				var n float64
				if err := rows.Scan(&class, &n, &oldest); err != nil {
					s.logger.Warn("metrics: work queue depth scan failed", "error", err)
					break
				}
				depth[class] += n
				if t, ok := parseLedgerTimestamp(oldest); ok {
					if d := now.Sub(t).Seconds(); d > age[class] {
						age[class] = d
					}
				}
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: work queue depth rows failed", "error", err)
			}
		}
	}
	writePromMetric(b, "crewshipd_work_queue_depth",
		"Work items waiting to run (queued or in retry backoff) per capacity class", "gauge", hostname,
		statusSamples(workClassSet, "class", depth))
	writePromMetric(b, "crewshipd_work_queue_oldest_age_seconds",
		"Age of the oldest waiting work item per class, from acceptance (0 when the class queue is empty)",
		"gauge", hostname, statusSamples(workClassSet, "class", age))
}

// ── Attempts, retries, lease loss, reconciliation ───────────────────────

// collectWorkAttemptMetrics reports §10's attempts/retries, lease loss and
// reconciliation counters.
//
// All four are counted from work_events rather than from work_items.attempts,
// and from one source rather than two, so attempts and retries stay
// comparable: the event ledger is append-only inside the same transaction as
// the state change (§3), which makes it the only place where a retry that
// happened and was then overwritten still shows up.
//
// Retention hard-deletes terminal work and its events cascade, so these can
// decrease. That is a counter reset, which rate()/increase() absorb — the same
// property crewshipd_agent_run_events_total already documents for the journal.
func (s *Server) collectWorkAttemptMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	var attempts, retries, reconciliation float64
	leaseLoss := map[string]float64{}
	if s.db != nil {
		if err := s.db.QueryRowContext(ctx, `
			SELECT
				COALESCE(SUM(CASE WHEN to_state = 'starting' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN to_state = 'retry_wait' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN to_state = 'needs_reconciliation' THEN 1 ELSE 0 END), 0)
			  FROM work_events`,
		).Scan(&attempts, &retries, &reconciliation); err != nil {
			s.logger.Warn("metrics: work attempt counters failed", "error", err)
		}

		// A lease loss is an event whose reason RecoverExpiredLeases wrote.
		// Matching on the reason prefix is an interface between two files, so
		// the prefix is a named constant in internal/work and
		// TestCollectWorkMetrics_LeaseLoss drives the real recovery path — a
		// reworded reason reds that test instead of silently zeroing this
		// series.
		rows, err := s.db.QueryContext(ctx, `
			SELECT to_state, COUNT(*) FROM work_events
			 WHERE reason LIKE ? || '%'
			 GROUP BY to_state`, work.LeaseExpiredReasonPrefix)
		if err != nil {
			s.logger.Warn("metrics: work lease loss failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var toState string
				var n float64
				if err := rows.Scan(&toState, &n); err != nil {
					s.logger.Warn("metrics: work lease loss scan failed", "error", err)
					break
				}
				switch toState {
				case string(work.StateNeedsReconciliation):
					leaseLoss["reconciliation"] += n
				default:
					// Everything else recovery can reach from an expired lease
					// is a return to the queue.
					leaseLoss["requeued"] += n
				}
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: work lease loss rows failed", "error", err)
			}
		}
	}
	writePromMetric(b, "crewshipd_work_attempts_total",
		"Attempts started against work items (one per claim)", "counter", hostname,
		[]promSample{{value: attempts}})
	writePromMetric(b, "crewshipd_work_retries_total",
		"Work items sent back to retry_wait after a safely repeatable failure (§4, max 5 attempts)",
		"counter", hostname, []promSample{{value: retries}})
	writePromMetric(b, "crewshipd_work_reconciliation_total",
		"Times work entered needs_reconciliation — an unclear external effect or an unrecoverable runtime (§4)",
		"counter", hostname, []promSample{{value: reconciliation}})
	writePromMetric(b, "crewshipd_work_lease_losses_total",
		"Attempts whose lease expired without a heartbeat, by what recovery did with them",
		"counter", hostname, statusSamples(workLeaseLossOutcomeSet, "outcome", leaseLoss))
}

// ── Deliveries ──────────────────────────────────────────────────────────

// collectWorkDeliveryMetrics reports §10's "accept" counter from the delivery
// ledger. The reject and duplicate halves of that same row cannot come from
// here — neither writes a row — and are emitted by
// collectWorkProcessCounters from internal/work's process counters.
func (s *Server) collectWorkDeliveryMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	counts := map[string]float64{}
	if s.db != nil {
		rows, err := s.db.QueryContext(ctx, `
			SELECT filter_decision, COUNT(*) FROM webhook_deliveries GROUP BY filter_decision`)
		if err != nil {
			s.logger.Warn("metrics: webhook deliveries count failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var decision string
				var n float64
				if err := rows.Scan(&decision, &n); err != nil {
					s.logger.Warn("metrics: webhook deliveries scan failed", "error", err)
					break
				}
				counts[decision] += n
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: webhook deliveries rows failed", "error", err)
			}
		}
	}
	writePromMetric(b, "crewshipd_work_deliveries",
		"Recorded webhook deliveries by filter decision — an ignored delivery is audited, not dropped (§5)",
		"gauge", hostname, statusSamples(workDeliveryDecisionSet, "decision", counts))
}

// ── Capacity invariant ──────────────────────────────────────────────────

// collectWorkCapacityMetrics reports the live set per class and the number of
// capacity dimensions currently exceeding their cap.
//
// §10's "Kapacitní invariant" row targets no slot overrun at all under 50
// concurrent producers, so the violation gauge's alarm is on it being non-zero
// — there is no threshold to tune. A non-zero value does not mean the server
// is busy; it means the atomic admission in Claim (I3) did not hold, which is
// a correctness bug rather than a load signal.
//
// Caps come from work.DefaultLimits, the §6 reference profile. An adapter that
// has not passed T06/T07 runs under narrower per-agent limits
// (work.SerialAgentLimits), which this collector cannot see from the ledger —
// so it measures against the PERMISSIVE ceiling. A violation here is therefore
// unambiguous, and a violation of a narrower configured limit is not visible;
// that is the honest trade and it is stated rather than left to be discovered.
func (s *Server) collectWorkCapacityMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	limits := work.DefaultLimits()
	live := map[string]float64{}
	var violations float64

	if s.db != nil {
		var total, chat, background float64
		if err := s.db.QueryRowContext(ctx, `
			SELECT
				COUNT(*),
				COALESCE(SUM(CASE WHEN class = 'chat' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN class = 'background' THEN 1 ELSE 0 END), 0)
			  FROM work_items WHERE state IN (`+sqlWorkStatesHoldingSlot+`)`,
		).Scan(&total, &chat, &background); err != nil {
			s.logger.Warn("metrics: work live counts failed", "error", err)
		} else {
			live[string(work.ClassChat)] = chat
			live[string(work.ClassBackground)] = background
			if total > float64(limits.ServerTotal) {
				violations++
			}
			if background > float64(limits.ServerBackground) {
				violations++
			}
		}

		// Per-agent caps. Agents are user-created and unbounded, so they are
		// COUNTED, never labelled: the series says how many agents are over a
		// cap, and the journal says which — the §10 division of labour again.
		var overTotal, overChat, overBackground float64
		if err := s.db.QueryRowContext(ctx, `
			SELECT
				COALESCE(SUM(CASE WHEN n     > ? THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN nchat > ? THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN nbg   > ? THEN 1 ELSE 0 END), 0)
			  FROM (
				SELECT
					COUNT(*) AS n,
					SUM(CASE WHEN class = 'chat' THEN 1 ELSE 0 END) AS nchat,
					SUM(CASE WHEN class = 'background' THEN 1 ELSE 0 END) AS nbg
				  FROM work_items
				 WHERE agent_id != '' AND state IN (`+sqlWorkStatesHoldingSlot+`)
				 GROUP BY agent_id
			  )`, limits.AgentTotal, limits.AgentChat, limits.AgentBackground,
		).Scan(&overTotal, &overChat, &overBackground); err != nil {
			s.logger.Warn("metrics: work per-agent capacity failed", "error", err)
		} else {
			violations += overTotal + overChat + overBackground
		}
	}

	if violations > 0 {
		// Turn the observation into a monotonic count as well, so a violation
		// that self-heals between two scrapes still leaves a trace an alert can
		// fire on hours later. "Moments" here means scrape windows: the block
		// is cached for domainMetricsTTL, so a sustained violation increments
		// once per window rather than once per scraper.
		work.RecordCapacityViolation()
	}

	writePromMetric(b, "crewshipd_work_live",
		"Work items holding an execution slot per class (starting, running, or needs_reconciliation)",
		"gauge", hostname, statusSamples(workClassSet, "class", live))
	writePromMetric(b, "crewshipd_work_capacity_violations",
		"Capacity dimensions currently over their §6 cap — server total, server background, and the number of agents over each per-agent cap. Target 0; non-zero means admission (I3) did not hold",
		"gauge", hostname, []promSample{{value: violations}})
}

// ── Ledger balance ──────────────────────────────────────────────────────

// collectWorkLedgerBalanceMetrics reports §10's "Bilance accepted =
// queued/active/waiting/retry/reconciliation/terminal" as one signed
// difference plus both sides of it.
//
// The two sides are computed from DIFFERENT tables on purpose, and that is
// what makes the identity worth exposing. The accepted side counts acceptance
// events in work_events — the row AcceptTx writes in the same transaction as
// the item (I1), from_state ” and reason 'accepted'. The bucket side counts
// work_items by state. Summing work_items twice would be an identity that
// cannot fail.
//
// So a non-zero difference names a real defect:
//
//   - positive: acceptance events without a live item. Retention cascades
//     events with their item, so this is history outliving the work it
//     describes.
//   - negative: work items nobody accepted — a producer that reached the
//     ledger without going through the shared admission path, which is exactly
//     what I7 forbids. It is also what the number moving off zero is FOR: a
//     leak shows up as a value nobody has to compute by hand.
func (s *Server) collectWorkLedgerBalanceMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	var accepted float64
	buckets := map[string]float64{}
	var bucketSum float64

	if s.db != nil {
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM work_events
			 WHERE from_state = '' AND to_state = 'queued' AND reason = 'accepted'`,
		).Scan(&accepted); err != nil {
			s.logger.Warn("metrics: work accepted count failed", "error", err)
		}

		stateCounts := map[string]float64{}
		rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM work_items GROUP BY state`)
		if err != nil {
			s.logger.Warn("metrics: work ledger buckets failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var state string
				var n float64
				if err := rows.Scan(&state, &n); err != nil {
					s.logger.Warn("metrics: work ledger buckets scan failed", "error", err)
					break
				}
				stateCounts[state] = n
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: work ledger buckets rows failed", "error", err)
			}
		}
		for _, bucket := range workLedgerBuckets {
			var n float64
			for _, st := range bucket.states {
				n += stateCounts[st]
			}
			buckets[bucket.name] = n
			bucketSum += n
		}
	}

	names := make([]string, 0, len(workLedgerBuckets))
	for _, bucket := range workLedgerBuckets {
		names = append(names, bucket.name)
	}

	writePromMetric(b, "crewshipd_work_ledger_accepted",
		"Acceptance events in the ledger — one per work item that went through the shared admission path (I1)",
		"gauge", hostname, []promSample{{value: accepted}})
	writePromMetric(b, "crewshipd_work_ledger_bucket",
		"Work items per §10 balance term", "gauge", hostname,
		statusSamples(names, "bucket", buckets))
	writePromMetric(b, "crewshipd_work_ledger_balance_difference",
		"accepted minus the sum of every balance term (§10). Permanently 0; positive means history outliving its work, negative means work that bypassed admission (I7)",
		"gauge", hostname, []promSample{{value: accepted - bucketSum}})
}

// ── Latency ─────────────────────────────────────────────────────────────

// collectWorkLatencyMetrics reports the three §10 timing rows.
//
// They are three series and not one on purpose. §10: "Zvlášť reportovat
// celkový queue age a čas přípravy CLI; žádné skrytí čekání za tuto metriku" —
// report total queue age and runtime preparation separately, and hide no
// waiting behind either.
//
//   - acceptance: complete body to HTTP answer. Process samples; no column
//     records either end (internal/work/metrics.go).
//   - queue age: eligible_at to the claim that took the item.
//   - start latency: the claim to the runtime being confirmed live. This is
//     the "čas přípravy CLI" — container start, session setup — and keeping it
//     out of queue age is what stops a slow runtime from reading as a fast
//     queue.
//
// Each emits a quantile family only when a real sample exists, plus an
// always-present sample count. §10 requires absent functionality to be marked
// N/A rather than 0, and an omitted series is how this exposition format says
// N/A.
func (s *Server) collectWorkLatencyMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	acceptance := work.AcceptanceLatencySamples()
	p50, p95, n := percentiles50And95(acceptance)
	writeQuantileMetric(b, "crewshipd_work_acceptance_latency_seconds",
		"Seconds from the complete request body to the acceptance answer (§10 target p95 ≤0.5s, p99 ≤2s). In-process samples since start — no column records either end",
		hostname, p50, p95, n)
	writePromMetric(b, "crewshipd_work_acceptance_latency_sample_count",
		"Acceptance samples retained in the current in-process window", "gauge", hostname,
		[]promSample{{value: float64(n)}})

	// Queue age is restricted to the FIRST attempt. work_items.eligible_at is
	// rewritten by every retry's backoff, so on a later attempt the column no
	// longer holds the time that attempt actually waited from; a sample taken
	// there would measure the backoff, not the queue.
	queueAge := s.workLatencySamples(ctx, "work queue age", `
		SELECT w.eligible_at, a.started_at
		  FROM work_attempts a
		  JOIN work_items w ON w.id = a.work_id
		 WHERE a.attempt = 1
		 ORDER BY a.rowid DESC
		 LIMIT ?`)
	p50, p95, n = percentiles50And95(queueAge)
	writeQuantileMetric(b, "crewshipd_work_queue_age_seconds",
		"Seconds a work item waited from becoming eligible to being claimed (§10 dispatch overhead, target p95 ≤2s). First attempts only — a retry rewrites eligible_at",
		hostname, p50, p95, n)
	writePromMetric(b, "crewshipd_work_queue_age_sample_count",
		"Claims backing crewshipd_work_queue_age_seconds in the current window", "gauge", hostname,
		[]promSample{{value: float64(n)}})

	startLatency := s.workLatencySamples(ctx, "work start latency", `
		SELECT a.started_at, e.at
		  FROM work_attempts a
		  JOIN work_events e
		    ON e.work_id = a.work_id AND e.run_id = a.run_id AND e.to_state = 'running'
		 ORDER BY a.rowid DESC
		 LIMIT ?`)
	p50, p95, n = percentiles50And95(startLatency)
	writeQuantileMetric(b, "crewshipd_work_start_latency_seconds",
		"Seconds from claiming a work item to its runtime being confirmed live — the preparation time §10 requires be reported apart from queue age",
		hostname, p50, p95, n)
	writePromMetric(b, "crewshipd_work_start_latency_sample_count",
		"Attempts backing crewshipd_work_start_latency_seconds in the current window", "gauge", hostname,
		[]promSample{{value: float64(n)}})
}

// workLatencySamples runs a two-timestamp query over the bounded window and
// returns the positive deltas in seconds.
//
// A negative delta is dropped rather than clamped to zero: it means the two
// columns disagree about order (a retry that rewrote eligible_at past its own
// first attempt, a clock step), and a percentile is only worth reading if
// every sample in it was a real interval.
func (s *Server) workLatencySamples(ctx context.Context, label, query string) []float64 {
	if s.db == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, query, percentileWindowRows)
	if err != nil {
		s.logger.Warn("metrics: "+label+" failed", "error", err)
		return nil
	}
	defer rows.Close()
	var samples []float64
	for rows.Next() {
		var fromRaw, toRaw string
		if err := rows.Scan(&fromRaw, &toRaw); err != nil {
			s.logger.Warn("metrics: "+label+" scan failed", "error", err)
			break
		}
		from, ok1 := parseLedgerTimestamp(fromRaw)
		to, ok2 := parseLedgerTimestamp(toRaw)
		if !ok1 || !ok2 {
			continue
		}
		if d := to.Sub(from).Seconds(); d >= 0 {
			samples = append(samples, d)
		}
	}
	if err := rows.Err(); err != nil {
		s.logger.Warn("metrics: "+label+" rows failed", "error", err)
	}
	return samples
}

// ── Memory conflicts ────────────────────────────────────────────────────

// collectMemoryMutationMetrics reports §10's "memory conflict" counter from
// the mutation ledger.
//
// `conflicted` is the row that matters: §8's recovery protocol reaches it when
// the file on disk matches neither the original nor the target hash, so a third
// party owns those bytes and they are NOT overwritten. It settles the mutation
// while leaving the anchor stale, which means every later write of that key
// keeps conflicting until somebody imports it — a number that only ever grows
// until a human acts.
//
// intent and renamed are emitted beside it because they are the unsettled
// recovery backlog: a growing count there means the confirm step is not
// keeping up, which §10's memory row cares about for the same reason.
func (s *Server) collectMemoryMutationMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	counts := map[string]float64{}
	if s.db != nil {
		rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM memory_mutations GROUP BY state`)
		if err != nil {
			s.logger.Warn("metrics: memory mutations count failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var state string
				var n float64
				if err := rows.Scan(&state, &n); err != nil {
					s.logger.Warn("metrics: memory mutations scan failed", "error", err)
					break
				}
				counts[state] += n
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: memory mutations rows failed", "error", err)
			}
		}
	}
	writePromMetric(b, "crewshipd_memory_mutations",
		"Memory mutations by §8 state — `conflicted` is §10's memory-conflict counter, `intent`/`renamed` the unsettled recovery backlog",
		"gauge", hostname, statusSamples(memoryMutationStateSet, "state", counts))
}

// ── WAL ─────────────────────────────────────────────────────────────────

// collectWALMetrics reports §10's WAL row: the size of the -wal sidecar and
// the autocheckpoint setting that says who is responsible for shrinking it.
//
// Deliberately read-only. `PRAGMA wal_checkpoint` — the only pragma that would
// answer "how many frames are pending" — performs a checkpoint, which is a
// WRITE; running it on every scrape would hand a scraper the ability to take
// the write lock, and would make the metric change the thing it measures.
// So the size comes from stat(2) on the sidecar, the same way
// internal/database's own checkpoint policy measures it.
//
// autocheckpoint 0 means the file is managed by the background checkpointer
// (database.WithManagedWAL); any other value means SQLite is folding frames
// back inline, on whichever request happens to cross the threshold. Emitting
// it turns "why did one agent's write take 30ms" into a question with a
// visible answer.
//
// A checkpoint COUNTER — how many ran, how many were busy, how long they took
// — is not here: that lives in internal/database's checkpoint loop, and
// instrumenting it is a change to that package.
func (s *Server) collectWALMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	var walBytes, autoCheckpoint float64
	if s.db != nil {
		var seq int
		var name, file string
		if err := s.db.QueryRowContext(ctx, `PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
			s.logger.Warn("metrics: database_list failed", "error", err)
		} else if file != "" {
			// An in-memory database answers with an empty file path and has no
			// sidecar; a missing -wal file is 0 bytes, which is what a freshly
			// truncated WAL genuinely is.
			if fi, err := os.Stat(file + "-wal"); err == nil {
				walBytes = float64(fi.Size())
			}
		}
		if err := s.db.QueryRowContext(ctx, `PRAGMA wal_autocheckpoint`).Scan(&autoCheckpoint); err != nil {
			s.logger.Warn("metrics: wal_autocheckpoint failed", "error", err)
		}
	}
	writePromMetric(b, "crewshipd_wal_bytes",
		"Size of the SQLite -wal sidecar. Growth without bound means nothing is checkpointing (§10 DB/WAL growth)",
		"gauge", hostname, []promSample{{value: walBytes}})
	writePromMetric(b, "crewshipd_wal_autocheckpoint_frames",
		"SQLite's inline autocheckpoint threshold in frames. 0 means the background checkpointer owns the WAL; any other value means request-serving writes pay the fold-back",
		"gauge", hostname, []promSample{{value: autoCheckpoint}})
}

// ── Process counters ────────────────────────────────────────────────────

// collectWorkProcessCounters renders the four §10 counters that no table can
// answer. internal/work/metrics.go documents, per counter, why the value
// leaves no row behind; the short version is that a duplicate and a rejection
// both exist precisely BECAUSE nothing was written.
//
// These reset when the process restarts, unlike every DB-derived series above.
// That is stated in the HELP text so a dashboard author does not read a
// restart as a fix.
func (s *Server) collectWorkProcessCounters(b *strings.Builder, hostname string) {
	c := work.ReadCounters()

	rejected := make([]promSample, 0, len(work.RejectReasons))
	for _, reason := range work.RejectReasons {
		rejected = append(rejected, promSample{
			labels: map[string]string{"reason": string(reason)},
			value:  float64(c.Rejected[reason]),
		})
	}
	writePromMetric(b, "crewshipd_work_rejected_total",
		"Inbound work refused before any durable write, by reason. Process-local: resets on restart",
		"counter", hostname, rejected)
	writePromMetric(b, "crewshipd_work_duplicate_deliveries_total",
		"Re-deliveries answered with the original receipt and no second work item (I2). Process-local: resets on restart",
		"counter", hostname, []promSample{{value: float64(c.Duplicates)}})
	writePromMetric(b, "crewshipd_work_cleanup_failures_total",
		"Failures to release a finished run's resources — the orphan-process signal for §10's soak target. Process-local: resets on restart",
		"counter", hostname, []promSample{{value: float64(c.CleanupFailures)}})
	writePromMetric(b, "crewshipd_work_capacity_violations_total",
		"Scrape windows in which the live set exceeded a §6 cap. Target 0; process-local, resets on restart",
		"counter", hostname, []promSample{{value: float64(c.CapacityViolations)}})
}
