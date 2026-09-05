package server

// B16 instrumentation (PRD-ISSUES-AND-ROUTINES-2026 §19.3, #2396): the two
// service-level rows B12 (metrics_domain_b12.go) left without a series,
// because the schema could not honestly support them at the time.
//
//   - "Scheduled fire punctuality, p95 < 60 s of due time (30 s poll
//     floor, F24)". A scheduled run carried only started_at; the
//     occurrence it fired FOR survived only as a SHA-256 inside the
//     idempotency key. v20260905172327 adds pipeline_runs.due_at, stamped
//     by the schedule fire path (fireSingleOccurrence → RunInput.DueAt →
//     RunRecord.DueAt) and by nothing else, and the collector below is
//     started_at − due_at over the same bounded window the B12
//     percentiles use.
//   - "Inbox items per successful run, 0 (outcome routing)". §12's hard
//     rule — "NO_CHANGE and SUCCEEDED never create an item" — was enforced
//     only by the producers' own tests. The collector below joins
//     inbox_items to every run (assignments AND pipeline_runs) whose
//     outcome is one of those two, and reports the ratio.
//
// Same conventions as metrics_domain_b12.go: a quantile is emitted only
// when at least one real sample exists; a ratio only when its denominator
// is non-zero; a COUNT(*) always, because 0 is a real, computed answer.

import (
	"context"
	"strings"
)

// successfulRunOutcomes is §12's "never create an item" set — the two
// outcomes of the §9.6 routing table (internal/orchestrator/outcome.go)
// that mean "done, nothing for a human". Matches the CHECK constraint on
// assignments.outcome / pipeline_runs.outcome
// (20260904233804_outcome_contract.sql) by spelling.
var successfulRunOutcomes = []string{"SUCCEEDED", "NO_CHANGE"}

// collectB16InstrumentationMetrics is the B16 fan-out, called from
// collectDomainMetrics (metrics_domain.go) right after the B12 one.
func (s *Server) collectB16InstrumentationMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	s.collectScheduleFirePunctualityMetrics(ctx, b, hostname)
	s.collectInboxItemsPerSuccessfulRunMetrics(ctx, b, hostname)
}

// ── Scheduled fire punctuality ───────────────────────────────────────────
//
// §19.3: "Scheduled fire punctuality, p95 < 60 s of due time, note the
// 30 s poll floor (F24)". due_at is the cron occurrence the scheduler took
// (scheduleDueAt: the row's next_run_at, fixed across a mid-run restart);
// started_at is when the executor opened the run. The gap is the whole
// path — the poll tick noticing the row, the wake probe on a gated
// schedule, the executor's own setup — which is exactly what "of due
// time" asks about. A catch-up fire after downtime keeps its original
// occurrence, so it reports the delta it really had; the window is
// bounded, so a restart's backlog ages out of the percentile as normal
// fires land.

// collectScheduleFirePunctualityMetrics computes p50/p95 of
// (started_at - due_at) over the most recent percentileWindowRows
// scheduled runs that carry a due_at. Runs from before the column existed
// have no due_at and are not samples — never a fabricated 0 s.
func (s *Server) collectScheduleFirePunctualityMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	var samples []float64
	if s.db != nil {
		rows, err := s.db.QueryContext(ctx, `
			SELECT started_at, due_at
			  FROM pipeline_runs
			 WHERE triggered_via = 'schedule' AND due_at IS NOT NULL
			 ORDER BY rowid DESC
			 LIMIT ?`, percentileWindowRows)
		if err != nil {
			s.logger.Warn("metrics: schedule fire punctuality failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var startedAtRaw, dueAtRaw string
				if err := rows.Scan(&startedAtRaw, &dueAtRaw); err != nil {
					s.logger.Warn("metrics: schedule fire punctuality scan failed", "error", err)
					break
				}
				startedAt, ok1 := parseWriteTimestamp(startedAtRaw)
				dueAt, ok2 := parseWriteTimestamp(dueAtRaw)
				if !ok1 || !ok2 {
					continue
				}
				if d := startedAt.Sub(dueAt).Seconds(); d >= 0 {
					samples = append(samples, d)
				}
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: schedule fire punctuality rows failed", "error", err)
			}
		}
	}
	p50, p95, n := percentiles50And95(samples)
	writeQuantileMetric(b, "crewshipd_schedule_fire_punctuality_seconds",
		"Seconds from a scheduled run's due occurrence (due_at) to the run starting (started_at) (§19.3 scheduled fire punctuality, target p95 < 60s over a 30s poll floor)",
		hostname, p50, p95, n)
	writePromMetric(b, "crewshipd_schedule_fire_punctuality_sample_count",
		"Samples backing crewshipd_schedule_fire_punctuality_seconds in the current window", "gauge", hostname,
		[]promSample{{value: float64(n)}})
}

// ── Inbox items per successful run ──────────────────────────────────────
//
// §19.3: "Inbox items per successful run, 0, outcome routing". Two tables
// carry a §9.6 outcome — assignments (agent runs; the run_needs_human
// producer keys its item by assignment id) and pipeline_runs (routine
// runs; the failed_run producer keys its item by run id) — so both are
// denominators, and an inbox item of ANY kind whose source_id is one of
// those run ids is a numerator: §12 says a SUCCEEDED / NO_CHANGE run
// never creates an item, not merely never creates a particular kind. No
// other producer today keys an item by a run id, so a match here is a
// routing violation, not a coincidence.
//
// The ratio is emitted only when at least one successful run exists — a
// 0/0 has no honest value — while both raw counts are always emitted, so
// an alert can be written against the ratio and a dashboard can still
// show the denominator growing from minute one.

// collectInboxItemsPerSuccessfulRunMetrics counts successful runs across
// both run tables, the inbox items pointing at any of them, and their
// ratio.
func (s *Server) collectInboxItemsPerSuccessfulRunMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	var successful, items float64
	if s.db != nil {
		placeholders := make([]string, len(successfulRunOutcomes))
		args := make([]any, len(successfulRunOutcomes))
		for i, o := range successfulRunOutcomes {
			placeholders[i] = "?"
			args[i] = o
		}
		inClause := strings.Join(placeholders, ",")

		// Both tables in one statement each, so the two numbers come from
		// the same instant and cannot disagree about which runs exist.
		successfulArgs := append(append([]any{}, args...), args...)
		if err := s.db.QueryRowContext(ctx, `
			SELECT (SELECT COUNT(*) FROM assignments   WHERE outcome IN (`+inClause+`))
			     + (SELECT COUNT(*) FROM pipeline_runs WHERE outcome IN (`+inClause+`))`,
			successfulArgs...,
		).Scan(&successful); err != nil {
			s.logger.Warn("metrics: successful runs failed", "error", err)
		}

		itemArgs := append(append([]any{}, args...), args...)
		if err := s.db.QueryRowContext(ctx, `
			SELECT (SELECT COUNT(*) FROM inbox_items i
			          JOIN assignments a ON a.id = i.source_id
			         WHERE a.outcome IN (`+inClause+`))
			     + (SELECT COUNT(*) FROM inbox_items i
			          JOIN pipeline_runs r ON r.id = i.source_id
			         WHERE r.outcome IN (`+inClause+`))`,
			itemArgs...,
		).Scan(&items); err != nil {
			s.logger.Warn("metrics: inbox items on successful runs failed", "error", err)
		}
	}
	writePromMetric(b, "crewshipd_successful_runs_total",
		"Runs (assignments and pipeline_runs) whose §9.6 outcome is SUCCEEDED or NO_CHANGE (inbox-items-per-successful-run denominator, §19.3)",
		"gauge", hostname, []promSample{{value: successful}})
	writePromMetric(b, "crewshipd_inbox_items_on_successful_runs",
		"Inbox items of any kind whose source is a SUCCEEDED or NO_CHANGE run (§12 says this must never happen; inbox-items-per-successful-run numerator)",
		"gauge", hostname, []promSample{{value: items}})
	var ratio []promSample
	if successful > 0 {
		ratio = []promSample{{value: items / successful}}
	}
	writePromMetric(b, "crewshipd_inbox_items_per_successful_run",
		"Inbox items per SUCCEEDED/NO_CHANGE run (§19.3, target 0); absent until at least one successful run exists",
		"gauge", hostname, ratio)
}
