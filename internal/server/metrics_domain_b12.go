package server

// B12 instrumentation (PRD-ISSUES-AND-ROUTINES-2026 §17 "B12 · Instrumentation",
// §19.3, finding F39): the §19.3 service-level series, computed from real
// write-path timestamps and states rather than declared as targets. See
// metrics_domain.go's own header for the shared conventions (bounded label
// sets, foldStatus, writePromMetric) this file reuses, and
// metrics_percentile.go for the percentile capability F39 says does not
// exist anywhere in the codebase.
//
// §24.1 names what this PRD actually measures: "delivery, continuation,
// duplication, and human comprehension". The collectors below are grouped
// under those four headings rather than transcribing every row of the
// §19.3 table verbatim — a few of that table's rows (scheduled fire
// punctuality, the routine/webhook SLOs) belong to B8/B9's own scheduling
// substrate, not to the mention/delivery/session tables B1/B2/B4/B5/B6
// shipped, and forcing them in here would mean inventing a join these
// tables cannot honestly support.
//
// Every series is computed, never asserted:
//   - a latency/size percentile is emitted only when at least one real
//     sample exists (writeQuantileMetric); with zero samples the quantile
//     labels are absent — never a fabricated 0 — while the family's own
//     HELP/TYPE header, and a separate real sample-count series, are
//     still emitted so dashboards and absent() alerts have a stable name
//     to point at from minute one (matching this file's zero-filled-set
//     convention for closed label sets).
//   - a count (lost deliveries, duplicate active runs, outcome counts,
//     routing violations) is always emitted, because 0 is itself a real,
//     computed answer for a COUNT(*) query — nothing here is a
//     placeholder standing in for "not measured".

import (
	"context"
	"strings"
	"time"
)

// percentileWindowRows bounds every percentile-over-samples query in this
// file to its most recent N rows, ordered by rowid (SQLite's implicit,
// monotonically-increasing insert order — cheap to scan backward without
// a secondary index, unlike created_at which has none on these tables).
// This is the "bounded window" B12 asks for: a percentile's cost must not
// grow with the lifetime of the deployment, and 500 recent deliveries/runs
// is already generous for a 15s-cached scrape (domainMetricsTTL).
const percentileWindowRows = 500

// deliveriesLostAfter is §19.3's "lost deliveries" threshold. A delivery
// still 'pending' this long after being raised has missed its own claim
// window — B4's lease sweep only reaps 'claimed' rows (see
// issue_deliveries.go's abandonPendingDelivery doc comment: nothing scans
// for stuck 'pending' rows today), so "pending older than 5 minutes" is
// the honest, and currently the ONLY, definition of "lost" this schema can
// answer.
const deliveriesLostAfter = 5 * time.Minute

// nonNeedsHumanOutcomes is every outcome the §9.6 routing table says must
// NOT create an inbox item (internal/database/migrations/20260904233804_outcome_contract.sql's
// CHECK, minus 'NEEDS_HUMAN'). Used by collectOutcomeRoutingMetrics to spot
// a run that resolved one of these and still raised a 'run_needs_human'
// inbox item — the one routing outcome B6's own accept line says must
// never happen.
var nonNeedsHumanOutcomes = []string{"NO_CHANGE", "SUCCEEDED", "WORK_CREATED", "PARTIAL", "FAILED", "CANCELLED"}

// assignmentOutcomeSet is the closed label set for crewshipd_assignment_outcomes,
// lower-cased to match this file's foldStatus convention. Matches the
// CHECK constraint on assignments.outcome / pipeline_runs.outcome exactly.
var assignmentOutcomeSet = []string{"no_change", "succeeded", "work_created", "partial", "needs_human", "failed", "cancelled", "other"}

// contextPackCompactionSet is the closed label set for
// crewshipd_context_pack_compaction, matching assignments.context_pack_compaction's
// CHECK constraint (fit|summarized|truncated). NULL rows (no session, no
// pack assembled) are excluded from the query entirely rather than folded
// into "other" — NULL means "the compaction question was never asked",
// which is a different fact from "asked and produced an unrecognized
// answer".
var contextPackCompactionSet = []string{"fit", "summarized", "truncated", "other"}

// sessionRunTerminalStatuses is "this run is done", the same set
// idx_assignments_one_active_per_session's own WHERE clause treats as
// exempt (20260904172200_assignments_one_active_per_session.sql) — a run
// leaves that partial unique index's grip in exactly these three statuses.
var sessionRunTerminalStatuses = []string{"COMPLETED", "FAILED", "CANCELLED"}

// collectB12InstrumentationMetrics is the B12 fan-out, called from
// collectDomainMetrics (metrics_domain.go) alongside the pre-existing
// collectors. Kept as one grouped call so metrics_domain.go's own
// fan-out reads as "the domain families, then the §19.3 SLO families"
// rather than eight more individual lines mixed into the original list.
func (s *Server) collectB12InstrumentationMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	s.collectDeliveryAckLatencyMetrics(ctx, b, hostname)
	s.collectDeliveryClaimLatencyMetrics(ctx, b, hostname)
	s.collectLostDeliveriesMetric(ctx, b, hostname)
	s.collectDuplicateRunMetrics(ctx, b, hostname)
	s.collectContextPackMetrics(ctx, b, hostname)
	s.collectCheckpointComplianceMetrics(ctx, b, hostname)
	s.collectOutcomeRoutingMetrics(ctx, b, hostname)
}

// writeQuantileMetric renders a p50/p95 gauge family. When n is 0 the
// family's HELP/TYPE header is still written (writePromMetric's existing
// behaviour for an empty samples slice) but NO quantile lines are —
// F39's "no metric may claim a number it cannot compute", applied at the
// smallest unit: an absent series, never a fabricated 0-second/0-token
// value.
func writeQuantileMetric(b *strings.Builder, name, help, hostname string, p50, p95 float64, n int) {
	var samples []promSample
	if n > 0 {
		samples = []promSample{
			{labels: map[string]string{"quantile": "0.5"}, value: p50},
			{labels: map[string]string{"quantile": "0.95"}, value: p95},
		}
	}
	writePromMetric(b, name, help, "gauge", hostname, samples)
}

// parseWriteTimestamp parses a timestamp written by this codebase's own
// write paths. missionactivity.Emit, createDelivery and
// insertCappedAssignment's callers (dispatchOne/DispatchMention) all write
// time.Now().UTC().Format(time.RFC3339) — no fractional seconds, so a
// plain time.RFC3339 parse (never RFC3339Nano — see lint-tsformat's own
// proximity rule) round-trips every row these collectors read. A row that
// fails to parse is skipped by the caller, not zero-filled: this is a
// defensive read against legacy/malformed data, not a shape the happy
// path produces.
func parseWriteTimestamp(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ── Delivery ────────────────────────────────────────────────────────────
//
// §19.3: "Comment persisted → acknowledgement visible, p95 < 500ms,
// server timestamp → WS emit". mentionRecorder.record (issue_mentions.go)
// logs the 'mentioned' mission_activity event, then — in the same request,
// before any model call — createDelivery writes the pending delivery row
// and deliverAndDispatch broadcasts issue.delivery.acked on the row it
// just created. The gap between those two real timestamps IS the ack
// latency §19.3 asks for; nothing else in the schema records when the WS
// frame itself left the process, so this is the closest honest proxy —
// stated here rather than left to be discovered as an approximation.

// collectDeliveryAckLatencyMetrics computes p50/p95 of
// (delivery.created_at - event.created_at) over the most recent
// percentileWindowRows deliveries that reference a real event.
func (s *Server) collectDeliveryAckLatencyMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	var samples []float64
	if s.db != nil {
		rows, err := s.db.QueryContext(ctx, `
			SELECT m.created_at, e.created_at
			  FROM mission_comment_mentions m
			  JOIN mission_activity e ON e.id = m.event_id
			 WHERE m.event_id IS NOT NULL
			 ORDER BY m.rowid DESC
			 LIMIT ?`, percentileWindowRows)
		if err != nil {
			s.logger.Warn("metrics: delivery ack latency failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var deliveredAtRaw, eventAtRaw string
				if err := rows.Scan(&deliveredAtRaw, &eventAtRaw); err != nil {
					s.logger.Warn("metrics: delivery ack latency scan failed", "error", err)
					break
				}
				deliveredAt, ok1 := parseWriteTimestamp(deliveredAtRaw)
				eventAt, ok2 := parseWriteTimestamp(eventAtRaw)
				if !ok1 || !ok2 {
					continue
				}
				if d := deliveredAt.Sub(eventAt).Seconds(); d >= 0 {
					samples = append(samples, d)
				}
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: delivery ack latency rows failed", "error", err)
			}
		}
	}
	p50, p95, n := percentiles50And95(samples)
	writeQuantileMetric(b, "crewshipd_delivery_ack_latency_seconds",
		"Seconds from the mentioned event to the delivery ack (comment persisted -> acknowledgement visible, §19.3)",
		hostname, p50, p95, n)
	writePromMetric(b, "crewshipd_delivery_ack_latency_sample_count",
		"Samples backing crewshipd_delivery_ack_latency_seconds in the current window", "gauge", hostname,
		[]promSample{{value: float64(n)}})
}

// §19.3: "First agent acknowledgement (capacity available), p95 < 10s,
// delivery -> run claim". A delivery is claimed and its claimed_by_run_id
// stamped in the same call that inserts the assignments row
// (insertCappedAssignment); the gap between the delivery's own created_at
// and the claiming run's created_at is that latency, exactly as written.

// collectDeliveryClaimLatencyMetrics computes p50/p95 of
// (assignment.created_at - delivery.created_at) over the most recent
// percentileWindowRows claimed deliveries.
func (s *Server) collectDeliveryClaimLatencyMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	var samples []float64
	if s.db != nil {
		rows, err := s.db.QueryContext(ctx, `
			SELECT m.created_at, a.created_at
			  FROM mission_comment_mentions m
			  JOIN assignments a ON a.id = m.claimed_by_run_id
			 WHERE m.claimed_by_run_id IS NOT NULL
			 ORDER BY m.rowid DESC
			 LIMIT ?`, percentileWindowRows)
		if err != nil {
			s.logger.Warn("metrics: delivery claim latency failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var deliveredAtRaw, claimedAtRaw string
				if err := rows.Scan(&deliveredAtRaw, &claimedAtRaw); err != nil {
					s.logger.Warn("metrics: delivery claim latency scan failed", "error", err)
					break
				}
				deliveredAt, ok1 := parseWriteTimestamp(deliveredAtRaw)
				claimedAt, ok2 := parseWriteTimestamp(claimedAtRaw)
				if !ok1 || !ok2 {
					continue
				}
				if d := claimedAt.Sub(deliveredAt).Seconds(); d >= 0 {
					samples = append(samples, d)
				}
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: delivery claim latency rows failed", "error", err)
			}
		}
	}
	p50, p95, n := percentiles50And95(samples)
	writeQuantileMetric(b, "crewshipd_delivery_claim_latency_seconds",
		"Seconds from delivery creation to the claiming run's own creation (delivery -> run claim, §19.3)",
		hostname, p50, p95, n)
	writePromMetric(b, "crewshipd_delivery_claim_latency_sample_count",
		"Samples backing crewshipd_delivery_claim_latency_seconds in the current window", "gauge", hostname,
		[]promSample{{value: float64(n)}})
}

// §19.3: "Lost deliveries, 0, pending older than 5 min, alarmed".

// collectLostDeliveriesMetric counts deliveries still 'pending' more than
// deliveriesLostAfter after they were raised — always emitted (0 is a
// real, computed answer).
func (s *Server) collectLostDeliveriesMetric(ctx context.Context, b *strings.Builder, hostname string) {
	var lost float64
	if s.db != nil {
		cutoff := time.Now().UTC().Add(-deliveriesLostAfter).Format(time.RFC3339)
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM mission_comment_mentions
			 WHERE state = 'pending' AND created_at < ?`, cutoff,
		).Scan(&lost); err != nil {
			s.logger.Warn("metrics: lost deliveries failed", "error", err)
		}
	}
	writePromMetric(b, "crewshipd_deliveries_lost",
		"Deliveries still pending more than 5 minutes after being raised (§19.3, target 0)", "gauge", hostname,
		[]promSample{{value: lost}})
}

// ── Duplication ─────────────────────────────────────────────────────────
//
// §19.3: "Duplicate runs per event, 0, count runs per event_id". The raw
// per-event count cannot itself go above 1 in this schema —
// UNIQUE(event_id, agent_id) makes a second identical delivery an INSERT
// no-op (createDelivery), not a second row — so a violation of I1 would
// surface as a write-path error, never as a scannable duplicate row. The
// same property one layer up (B3, invariant I2:
// idx_assignments_one_active_per_session) DOES have an observable shape:
// two non-terminal assignments sharing one session_id. That is the
// concrete, queryable proxy for "duplicate runs" this collector measures,
// named as such rather than silently standing in for the raw-event
// question it cannot answer.

// collectDuplicateRunMetrics counts sessions currently holding more than
// one non-terminal (PENDING/QUEUED/RUNNING) assignment — the I2 violation
// idx_assignments_one_active_per_session exists to prevent. Always
// emitted; 0 is the expected, computed steady state.
func (s *Server) collectDuplicateRunMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	var dup float64
	if s.db != nil {
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM (
				SELECT session_id FROM assignments
				 WHERE session_id IS NOT NULL AND status IN ('PENDING','QUEUED','RUNNING')
				 GROUP BY session_id
				HAVING COUNT(*) > 1
			)`,
		).Scan(&dup); err != nil {
			s.logger.Warn("metrics: duplicate active runs failed", "error", err)
		}
	}
	writePromMetric(b, "crewshipd_duplicate_active_runs",
		"Sessions currently holding more than one non-terminal assignment (I2 violation canary, §19.3 'duplicate runs', target 0)",
		"gauge", hostname, []promSample{{value: dup}})
}

// ── Continuation ────────────────────────────────────────────────────────
//
// §11.4 / §19.3: "assembled pack size in tokens ... capped, not
// minimised" and "share of runs whose context was truncated ... reported,
// alarmed above a threshold". assignments.context_pack_tokens and
// context_pack_compaction (B5) are recorded once, at dispatch time, for
// every session-scoped run — exactly the two numbers those two SLO rows
// ask for.

// collectContextPackMetrics reports the token-size percentile and the
// compaction-outcome closed set together, both restricted to session runs
// that actually got a context pack (context_pack_compaction/tokens
// NOT NULL) — a NULL row is "the compaction question was never asked",
// not a sample of "compaction produced nothing to report".
func (s *Server) collectContextPackMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	var tokenSamples []float64
	compactionCounts := map[string]float64{}
	if s.db != nil {
		rows, err := s.db.QueryContext(ctx, `
			SELECT context_pack_compaction, context_pack_tokens
			  FROM assignments
			 WHERE session_id IS NOT NULL
			   AND (context_pack_compaction IS NOT NULL OR context_pack_tokens IS NOT NULL)
			 ORDER BY rowid DESC
			 LIMIT ?`, percentileWindowRows)
		if err != nil {
			s.logger.Warn("metrics: context pack metrics failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var compaction *string
				var tokens *int64
				if err := rows.Scan(&compaction, &tokens); err != nil {
					s.logger.Warn("metrics: context pack metrics scan failed", "error", err)
					break
				}
				if compaction != nil {
					compactionCounts[foldStatus(contextPackCompactionSet, *compaction)]++
				}
				if tokens != nil {
					tokenSamples = append(tokenSamples, float64(*tokens))
				}
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: context pack metrics rows failed", "error", err)
			}
		}
	}
	p50, p95, n := percentiles50And95(tokenSamples)
	writeQuantileMetric(b, "crewshipd_context_pack_tokens",
		"Assembled context-pack size in tokens at dispatch time (§11.4: capped, not minimised, over a bounded window)",
		hostname, p50, p95, n)
	writePromMetric(b, "crewshipd_context_pack_compaction",
		"Session runs by unread-delta compaction outcome recorded at dispatch (§11.4 row 4)", "gauge", hostname,
		statusSamples(contextPackCompactionSet, "compaction", compactionCounts))
}

// §19.3: "Checkpoint compliance, >95% of session runs, Parsed flag".
// Reported as the two raw counts a compliance ratio needs — finished
// session runs, and how many of those have a Parsed=true checkpoint on
// record — rather than a pre-divided ratio: a ratio with a zero
// denominator (no session runs have finished yet) has no honest value,
// and this hand-rolled format has no way to mark one series "not
// applicable" the way an operator's own query (numerator/denominator)
// can once both counts exist.

// collectCheckpointComplianceMetrics counts finished session runs, and how
// many of those wrote at least one checkpoint whose JSON body parsed
// (orchestrator.CheckpointData.Parsed) successfully.
func (s *Server) collectCheckpointComplianceMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	var finished, compliant float64
	if s.db != nil {
		placeholders := make([]string, len(sessionRunTerminalStatuses))
		args := make([]any, len(sessionRunTerminalStatuses))
		for i, st := range sessionRunTerminalStatuses {
			placeholders[i] = "?"
			args[i] = st
		}
		inClause := strings.Join(placeholders, ",")

		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM assignments WHERE session_id IS NOT NULL AND status IN (`+inClause+`)`,
			args...,
		).Scan(&finished); err != nil {
			s.logger.Warn("metrics: session runs finished failed", "error", err)
		}

		compliantArgs := append([]any{}, args...)
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(DISTINCT a.id)
			  FROM assignments a
			  JOIN agent_session_checkpoints c ON c.run_id = a.id
			 WHERE a.session_id IS NOT NULL AND a.status IN (`+inClause+`)
			   AND json_extract(c.checkpoint_json, '$.parsed') = 1`,
			compliantArgs...,
		).Scan(&compliant); err != nil {
			s.logger.Warn("metrics: session runs checkpointed failed", "error", err)
		}
	}
	writePromMetric(b, "crewshipd_session_runs_finished_total",
		"Session-scoped runs that have reached a terminal status (checkpoint-compliance denominator, §19.3)",
		"gauge", hostname, []promSample{{value: finished}})
	writePromMetric(b, "crewshipd_session_runs_checkpointed_total",
		"Finished session runs with at least one checkpoint whose body parsed (Parsed=true, checkpoint-compliance numerator, §19.3)",
		"gauge", hostname, []promSample{{value: compliant}})
}

// ── Human comprehension ─────────────────────────────────────────────────
//
// §19.3: "Inbox items per successful run, 0, outcome routing". §9.6's
// routing table (internal/orchestrator/outcome.go, B6) says exactly one
// outcome reaches the inbox: NEEDS_HUMAN. The outcome distribution itself
// is the direct "how often does a human have to look" comprehension
// signal; the violation count is the canary for the routing table's own
// invariant — a non-NEEDS_HUMAN run that raised a 'run_needs_human' inbox
// item anyway is a routing bug, and this is the one query that would
// notice.

// collectOutcomeRoutingMetrics reports the outcome closed set and the
// routing-violation canary together.
func (s *Server) collectOutcomeRoutingMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	counts := map[string]float64{}
	var violations float64
	if s.db != nil {
		rows, err := s.db.QueryContext(ctx,
			`SELECT outcome, COUNT(*) FROM assignments WHERE outcome IS NOT NULL GROUP BY outcome`)
		if err != nil {
			s.logger.Warn("metrics: assignment outcomes failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var outcome string
				var n float64
				if err := rows.Scan(&outcome, &n); err != nil {
					s.logger.Warn("metrics: assignment outcomes scan failed", "error", err)
					break
				}
				counts[foldStatus(assignmentOutcomeSet, strings.ToLower(outcome))] += n
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: assignment outcomes rows failed", "error", err)
			}
		}

		placeholders := make([]string, len(nonNeedsHumanOutcomes))
		args := make([]any, len(nonNeedsHumanOutcomes))
		for i, o := range nonNeedsHumanOutcomes {
			placeholders[i] = "?"
			args[i] = o
		}
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(*)
			  FROM assignments a
			  JOIN inbox_items i ON i.source_id = a.id AND i.kind = 'run_needs_human'
			 WHERE a.outcome IN (`+strings.Join(placeholders, ",")+`)`,
			args...,
		).Scan(&violations); err != nil {
			s.logger.Warn("metrics: outcome routing violations failed", "error", err)
		}
	}
	writePromMetric(b, "crewshipd_assignment_outcomes",
		"Terminal assignments by §9.6 outcome (human-comprehension signal: how often a human has to look, §19.3)",
		"gauge", hostname, statusSamples(assignmentOutcomeSet, "outcome", counts))
	writePromMetric(b, "crewshipd_outcome_routing_violations",
		"Runs whose outcome was not NEEDS_HUMAN but which raised a run_needs_human inbox item anyway (§9.6 routing-table canary, target 0)",
		"gauge", hostname, []promSample{{value: violations}})
}
