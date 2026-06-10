---
title: "Prometheus Metrics"
description: "Scrape crewshipd's /metrics endpoint for process and domain metrics"
icon: "chart-line"
---

`crewshipd` exposes a Prometheus text-format endpoint at `GET /metrics` on the
main HTTP port. It serves two groups of series: process gauges (uptime, memory,
goroutines, WebSocket connections) and domain metrics — the counters and gauges
an operator alerts on.

## Authorization

`/metrics` is not public:

- Requests from **loopback** (the true client IP, X-Forwarded-For aware) are
  always allowed — the typical node-local Prometheus or sidecar scrape.
- Remote scrapers must send `Authorization: Bearer <token>` matching the
  `CREWSHIP_METRICS_TOKEN` environment variable.
- With no token configured, non-loopback requests get a `404`.

```yaml
# prometheus.yml
scrape_configs:
  - job_name: crewshipd
    scrape_interval: 30s
    authorization:
      credentials: <CREWSHIP_METRICS_TOKEN>
    static_configs:
      - targets: ["crewship.example.com:8080"]
```

## Process metrics

| Metric | Type | Description |
| --- | --- | --- |
| `crewshipd_uptime_seconds` | gauge | Time since crewshipd started |
| `crewshipd_goroutines` | gauge | Number of goroutines |
| `crewshipd_memory_alloc_bytes` | gauge | Bytes of allocated heap |
| `crewshipd_memory_sys_bytes` | gauge | Total bytes obtained from the OS |
| `crewshipd_gc_runs_total` | counter | Total GC runs |
| `crewshipd_ws_connections` | gauge | Active WebSocket connections |

Every series carries a `hostname` label.

## Domain metrics

### Assignments and queue

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `crewshipd_assignments` | gauge | `status` | Assignments currently in each status. Statuses: `pending`, `queued`, `running`, `completed`, `failed`, `cancelled`; anything unrecognized folds into `other`. All label values are always emitted (zero-filled). |
| `crewshipd_assignment_queue_depth` | gauge | — | `QUEUED` assignments across all crews |
| `crewshipd_assignment_queue_crews` | gauge | — | Crews with at least one queued assignment |
| `crewshipd_assignment_queue_depth_max` | gauge | — | Queued assignments in the most backlogged crew |

Queue depth is deliberately **aggregated, not labeled per crew** — crews are
user-created and unbounded, and per-crew labels would grow the series set
without limit. The three aggregates cover the alerting cases: total backlog
growing, backlog spreading across crews, and a single crew wedged
(`depth_max` climbing while `depth` is flat).

### Pipeline runs

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `crewshipd_pipeline_runs` | gauge | `status` | Pipeline runs by status: `queued`, `running`, `completed`, `failed`, `cancelled`, `dry_run`, `interrupted` (+ `other`), zero-filled |

### Agent runs

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `crewshipd_agent_run_events_total` | counter | `event` | Agent run lifecycle events from the unified journal (live + archived rows): `started`, `completed`, `failed`, `cancelled`, `timeout` |

Alert on failure rate with the usual counter recipe:

```promql
sum(rate(crewshipd_agent_run_events_total{event="failed"}[10m]))
/
sum(rate(crewshipd_agent_run_events_total{event="started"}[10m])) > 0.2
```

Journal retention pruning can shrink these counters; Prometheus `rate()` /
`increase()` treat that as a normal counter reset.

### LLM cost (paymaster)

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `crewshipd_llm_calls_total` | counter | `provider` | LLM invocations recorded in the paymaster cost ledger |
| `crewshipd_llm_cost_usd_total` | counter | `provider` | Cumulative LLM spend in USD |

Provider label values are capped (overflow folds into `provider="other"`) so
the series set stays bounded. Spend-rate alert:

```promql
sum(increase(crewshipd_llm_cost_usd_total[1h])) > 5
```

### Containers

| Metric | Type | Description |
| --- | --- | --- |
| `crewshipd_containers_tracked` | gauge | Crew containers registered with the stats collector |
| `crewshipd_containers_reporting` | gauge | Tracked containers with a collected stats sample — a cheap health proxy; `tracked - reporting > 0` for more than a couple of poll intervals means a container is not answering stats |

### Database

| Metric | Type | Description |
| --- | --- | --- |
| `crewshipd_db_migration_version` | gauge | Highest applied schema migration version. Compare across a fleet to catch a node running old code against a newer schema. |

## Freshness and cost

The DB-derived block is computed from indexed counts and cached for **15
seconds** — scraping more often than that returns the same snapshot. At
typical 15–60s scrape intervals this is invisible; it exists so a scraper
retry storm (or an abusive client that got hold of the token) cannot turn
`/metrics` into a query amplifier.

For traces and OTLP export, see [OTLP setup](/observability/otlp-setup).
