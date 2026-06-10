package server

// Domain metrics for the /metrics endpoint (W10, RELEASE-1.0-HARDENING).
//
// handleMetrics historically exposed six process gauges only — enough to
// see that crewshipd is alive, useless for alerting on what it is doing.
// This file adds the series an operator actually pages on: assignment
// states and queue depth, pipeline runs by status, agent run lifecycle
// counters, LLM spend from the paymaster ledger, container health, and
// the applied DB migration version.
//
// Design constraints, in order:
//
//   - Hand-rolled Prometheus text format, exactly like the existing
//     process gauges. go.mod has no prometheus client dependency and we
//     are not adding one for a handful of counters.
//   - Bounded label cardinality. Status/event labels come from closed
//     sets (unknown values fold into "other"), queue depth is aggregated
//     across crews instead of emitting a per-crew label (crews are
//     user-created and therefore unbounded), and provider labels are
//     capped at llmProviderSeriesCap with an "other" overflow bucket.
//   - Cheap scrapes. All counts ride indexes (v113 adds the two missing
//     status indexes) and the whole DB-derived block is cached for
//     domainMetricsTTL so a scraper retry storm cannot turn /metrics
//     into a query amplifier.
//   - Zero-filled closed sets. A migrated-but-empty store emits every
//     bounded series with value 0, so dashboards and absent() alerts
//     have a stable series set from the first scrape.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// domainMetricsTTL bounds how often the DB-derived metrics block is
// recomputed. Prometheus scrape intervals are typically 15–60s, so a
// 15s snapshot is indistinguishable from live data to an alert rule
// while capping the endpoint at ~6 small indexed queries per window
// regardless of scrape (or attack) frequency.
const domainMetricsTTL = 15 * time.Second

// llmProviderSeriesCap caps the number of distinct provider label
// values emitted for the LLM counters. Providers come from the
// credentials enum and are few in practice; the cap is a backstop so a
// corrupted/abused ledger can never mint unbounded series. Overflow
// folds into provider="other".
const llmProviderSeriesCap = 24

// Closed label sets. DB values are normalized to lower case and folded
// into "other" when unrecognized, keeping cardinality fixed forever.
var (
	assignmentStatusSet  = []string{"pending", "queued", "running", "completed", "failed", "cancelled", "other"}
	pipelineRunStatusSet = []string{"queued", "running", "completed", "failed", "cancelled", "dry_run", "interrupted", "other"}
	runEventSet          = []string{"started", "completed", "failed", "cancelled", "timeout"}
)

// domainMetricsCache memoizes the rendered text block. Zero value is
// ready to use; the first scrape populates it.
type domainMetricsCache struct {
	mu   sync.Mutex
	at   time.Time
	text string
}

// domainMetricsBlock returns the rendered domain-metrics text,
// recomputing it at most once per domainMetricsTTL.
func (s *Server) domainMetricsBlock(ctx context.Context, hostname string) string {
	s.domainMetrics.mu.Lock()
	defer s.domainMetrics.mu.Unlock()
	if s.domainMetrics.text != "" && time.Since(s.domainMetrics.at) < domainMetricsTTL {
		return s.domainMetrics.text
	}
	var b strings.Builder
	s.collectDomainMetrics(ctx, &b, hostname)
	s.domainMetrics.text = b.String()
	s.domainMetrics.at = time.Now()
	return s.domainMetrics.text
}

// promSample is one series line: extra labels (hostname is added by the
// writer) plus a value.
type promSample struct {
	labels map[string]string
	value  float64
}

// writePromMetric renders one metric family: HELP/TYPE headers followed
// by one line per sample, labels sorted for deterministic output.
func writePromMetric(b *strings.Builder, name, help, mtype, hostname string, samples []promSample) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s %s\n", name, mtype)
	for _, smp := range samples {
		labels := make(map[string]string, len(smp.labels)+1)
		labels["hostname"] = hostname
		for k, v := range smp.labels {
			labels[k] = v
		}
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, fmt.Sprintf("%s=%q", k, labels[k]))
		}
		fmt.Fprintf(b, "%s{%s} %s\n", name, strings.Join(pairs, ","), formatPromValue(smp.value))
	}
}

func formatPromValue(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// statusSamples zero-fills the closed set so every label value is
// always present, then orders samples by the set's canonical order.
func statusSamples(set []string, labelKey string, counts map[string]float64) []promSample {
	out := make([]promSample, 0, len(set))
	for _, st := range set {
		out = append(out, promSample{labels: map[string]string{labelKey: st}, value: counts[st]})
	}
	return out
}

// foldStatus normalizes a raw DB status into the closed set, folding
// unknown values into "other" so label cardinality stays fixed.
func foldStatus(set []string, raw string) string {
	norm := strings.ToLower(strings.TrimSpace(raw))
	for _, st := range set {
		if st == norm {
			return st
		}
	}
	return "other"
}

// collectDomainMetrics renders every domain metric family into b.
// Individual collection failures are logged and degrade to zero-filled
// series — a broken query must not take the whole scrape down.
func (s *Server) collectDomainMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	s.collectAssignmentMetrics(ctx, b, hostname)
	s.collectQueueMetrics(ctx, b, hostname)
	s.collectPipelineRunMetrics(ctx, b, hostname)
	s.collectRunEventMetrics(ctx, b, hostname)
	s.collectLLMCostMetrics(ctx, b, hostname)
	s.collectContainerMetrics(b, hostname)
	s.collectMigrationVersionMetric(ctx, b, hostname)
}

func (s *Server) collectAssignmentMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	counts := map[string]float64{}
	if s.db != nil {
		rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM assignments GROUP BY status`)
		if err != nil {
			s.logger.Warn("metrics: assignments count failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var status string
				var n float64
				if err := rows.Scan(&status, &n); err != nil {
					s.logger.Warn("metrics: assignments scan failed", "error", err)
					break
				}
				counts[foldStatus(assignmentStatusSet, status)] += n
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: assignments rows failed", "error", err)
			}
		}
	}
	writePromMetric(b, "crewshipd_assignments",
		"Assignments currently in each status", "gauge", hostname,
		statusSamples(assignmentStatusSet, "status", counts))
}

func (s *Server) collectQueueMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	// Aggregated on purpose: crews are user-created and unbounded, so a
	// per-crew label would be an unbounded series set. Total depth,
	// number of crews with a backlog, and the deepest single-crew queue
	// cover the alerting cases ("queue growing", "one crew wedged")
	// with exactly three series.
	var depth, crews, maxDepth float64
	if s.db != nil {
		rows, err := s.db.QueryContext(ctx, `
			SELECT COUNT(*)
			  FROM assignments a
			  JOIN agents ag ON ag.id = a.assigned_to_id
			 WHERE a.status = 'QUEUED'
			 GROUP BY ag.crew_id`)
		if err != nil {
			s.logger.Warn("metrics: queue depth failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var n float64
				if err := rows.Scan(&n); err != nil {
					s.logger.Warn("metrics: queue depth scan failed", "error", err)
					break
				}
				depth += n
				crews++
				if n > maxDepth {
					maxDepth = n
				}
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: queue depth rows failed", "error", err)
			}
		}
	}
	writePromMetric(b, "crewshipd_assignment_queue_depth",
		"QUEUED assignments across all crews", "gauge", hostname,
		[]promSample{{value: depth}})
	writePromMetric(b, "crewshipd_assignment_queue_crews",
		"Crews with at least one QUEUED assignment", "gauge", hostname,
		[]promSample{{value: crews}})
	writePromMetric(b, "crewshipd_assignment_queue_depth_max",
		"QUEUED assignments in the most backlogged crew", "gauge", hostname,
		[]promSample{{value: maxDepth}})
}

func (s *Server) collectPipelineRunMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	counts := map[string]float64{}
	if s.db != nil {
		rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM pipeline_runs GROUP BY status`)
		if err != nil {
			s.logger.Warn("metrics: pipeline runs count failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var status string
				var n float64
				if err := rows.Scan(&status, &n); err != nil {
					s.logger.Warn("metrics: pipeline runs scan failed", "error", err)
					break
				}
				counts[foldStatus(pipelineRunStatusSet, status)] += n
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: pipeline runs rows failed", "error", err)
			}
		}
	}
	writePromMetric(b, "crewshipd_pipeline_runs",
		"Pipeline runs by status", "gauge", hostname,
		statusSamples(pipelineRunStatusSet, "status", counts))
}

func (s *Server) collectRunEventMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	// Agent run lifecycle lives in the unified journal (agent_runs is
	// deprecated — see internal/api/metrics_fillers_runs_missions.go).
	// Live + archived rows are summed so journal archiving doesn't read
	// as a counter reset; retention pruning of the archive still can,
	// which rate()/increase() absorb as a normal reset.
	counts := map[string]float64{}
	if s.db != nil {
		const q = `
			SELECT entry_type, SUM(n) FROM (
				SELECT entry_type, COUNT(*) AS n FROM journal_entries
				 WHERE entry_type IN ('run.started','run.completed','run.failed','run.cancelled','run.timeout')
				 GROUP BY entry_type
				UNION ALL
				SELECT entry_type, COUNT(*) AS n FROM journal_entries_archived
				 WHERE entry_type IN ('run.started','run.completed','run.failed','run.cancelled','run.timeout')
				 GROUP BY entry_type
			) GROUP BY entry_type`
		rows, err := s.db.QueryContext(ctx, q)
		if err != nil {
			s.logger.Warn("metrics: run events count failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var entryType string
				var n float64
				if err := rows.Scan(&entryType, &n); err != nil {
					s.logger.Warn("metrics: run events scan failed", "error", err)
					break
				}
				counts[foldStatus(runEventSet, strings.TrimPrefix(entryType, "run."))] += n
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: run events rows failed", "error", err)
			}
		}
	}
	writePromMetric(b, "crewshipd_agent_run_events_total",
		"Agent run lifecycle events recorded in the journal (live + archived)", "counter", hostname,
		statusSamples(runEventSet, "event", counts))
}

func (s *Server) collectLLMCostMetrics(ctx context.Context, b *strings.Builder, hostname string) {
	type providerAgg struct {
		provider string
		calls    float64
		cost     float64
	}
	var aggs []providerAgg
	if s.db != nil {
		rows, err := s.db.QueryContext(ctx, `
			SELECT provider, COUNT(*), COALESCE(SUM(cost_usd), 0)
			  FROM cost_ledger
			 GROUP BY provider
			 ORDER BY COUNT(*) DESC, provider`)
		if err != nil {
			s.logger.Warn("metrics: llm cost failed", "error", err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var a providerAgg
				if err := rows.Scan(&a.provider, &a.calls, &a.cost); err != nil {
					s.logger.Warn("metrics: llm cost scan failed", "error", err)
					break
				}
				aggs = append(aggs, a)
			}
			if err := rows.Err(); err != nil {
				s.logger.Warn("metrics: llm cost rows failed", "error", err)
			}
		}
	}
	// Cardinality backstop: fold everything past the cap into "other".
	if len(aggs) > llmProviderSeriesCap {
		other := providerAgg{provider: "other"}
		for _, a := range aggs[llmProviderSeriesCap:] {
			other.calls += a.calls
			other.cost += a.cost
		}
		aggs = append(aggs[:llmProviderSeriesCap], other)
	}
	calls := make([]promSample, 0, len(aggs))
	costs := make([]promSample, 0, len(aggs))
	for _, a := range aggs {
		calls = append(calls, promSample{labels: map[string]string{"provider": a.provider}, value: a.calls})
		costs = append(costs, promSample{labels: map[string]string{"provider": a.provider}, value: a.cost})
	}
	writePromMetric(b, "crewshipd_llm_calls_total",
		"LLM invocations recorded in the paymaster cost ledger", "counter", hostname, calls)
	writePromMetric(b, "crewshipd_llm_cost_usd_total",
		"Cumulative LLM spend in USD from the paymaster cost ledger", "counter", hostname, costs)
}

func (s *Server) collectContainerMetrics(b *strings.Builder, hostname string) {
	var tracked, reporting float64
	if s.statsCollector != nil {
		tracked = float64(len(s.statsCollector.Tracked()))
		reporting = float64(s.statsCollector.ReportingCount())
	}
	writePromMetric(b, "crewshipd_containers_tracked",
		"Crew containers registered with the stats collector", "gauge", hostname,
		[]promSample{{value: tracked}})
	writePromMetric(b, "crewshipd_containers_reporting",
		"Tracked crew containers with a collected stats sample (health proxy)", "gauge", hostname,
		[]promSample{{value: reporting}})
}

func (s *Server) collectMigrationVersionMetric(ctx context.Context, b *strings.Builder, hostname string) {
	var version float64
	if s.db != nil {
		if err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(version), 0) FROM _migrations`).Scan(&version); err != nil {
			s.logger.Warn("metrics: migration version failed", "error", err)
		}
	}
	writePromMetric(b, "crewshipd_db_migration_version",
		"Highest applied database schema migration version", "gauge", hostname,
		[]promSample{{value: version}})
}
