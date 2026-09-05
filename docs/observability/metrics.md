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
- The `404` is the answer for **every** method, not just `GET` — an
  unauthorized `POST /metrics` looks exactly like an unauthorized `GET`, so a
  scanner cannot use a method probe to confirm the endpoint exists. Authorized
  callers still get an honest `405 Method Not Allowed` (with `Allow: GET, HEAD`)
  for anything other than `GET`/`HEAD`.

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

### Issue sessions and deliveries — the §19.3 service levels

These series answer the four questions the Issues & Routines PRD (§19.3,
§24.1) says are actually measured for the mention → wake → reply loop:
**delivery**, **continuation**, **duplication**, and **human comprehension**.
Before this section existed, none of them could be computed at all — there
was no percentile capability anywhere in `crewshipd` (no Prometheus client,
no histograms, SQLite has no `PERCENTILE_CONT`) — so every percentile below
is computed in Go, from real write-path timestamps, over a bounded window of
the most recent 500 rows. A quantile series is **absent** when its window has
zero samples — never a fabricated `0` — while its own sample-count series is
always present, because a count of zero is a real, computed answer.

#### Delivery — "did the mention reach an agent, and how fast"

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `crewshipd_delivery_ack_latency_seconds` | gauge | `quantile` (`0.5`, `0.95`) | Seconds from the `mentioned` event being logged to its delivery row being created and acked (`issue.delivery.acked`) — answers "comment persisted → acknowledgement visible" |
| `crewshipd_delivery_ack_latency_sample_count` | gauge | — | Deliveries backing the above, in the current window |
| `crewshipd_delivery_claim_latency_seconds` | gauge | `quantile` | Seconds from a delivery being raised to the run that claimed it being created — answers "first agent acknowledgement (delivery → run claim)" |
| `crewshipd_delivery_claim_latency_sample_count` | gauge | — | Deliveries backing the above, in the current window |
| `crewshipd_deliveries_lost` | gauge | — | Deliveries still `pending` more than 5 minutes after being raised. Target `0`; today nothing sweeps a stuck `pending` row (only `claimed` rows are reaped, by B4's lease sweeper), so a nonzero value here means an operator, not a background job, has to look. |

#### Duplication — "did one event ever produce two runs"

| Metric | Type | Description |
| --- | --- | --- |
| `crewshipd_duplicate_active_runs` | gauge | Sessions currently holding more than one non-terminal (`PENDING`/`QUEUED`/`RUNNING`) assignment. `idx_assignments_one_active_per_session` (invariant I2) makes this a canary rather than a routine count: the schema itself is supposed to make the value 0 always — a raw per-event duplicate count cannot be observed directly, because `UNIQUE(event_id, agent_id)` collapses a second identical delivery into a no-op INSERT rather than a second row. |

#### Continuation — "does the agent pick up where it left off, in a bounded pack"

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `crewshipd_context_pack_tokens` | gauge | `quantile` | Assembled context-pack size in tokens at dispatch time, over the most recent session-scoped runs — answers "is the pack capped, not growing with thread length" (§11.4 row 3) |
| `crewshipd_context_pack_compaction` | gauge | `compaction` (`fit`, `summarized`, `truncated`, `other`) | Session runs by unread-delta compaction outcome recorded at dispatch — answers "share of runs whose context was truncated" (§11.4 row 4). Only counts runs that actually had a compaction decision to record; a run with no session yet is absent from this set entirely, not folded into any bucket. |
| `crewshipd_session_runs_finished_total` | gauge | — | Session-scoped runs that reached a terminal status — the checkpoint-compliance denominator |
| `crewshipd_session_runs_checkpointed_total` | gauge | — | Finished session runs with at least one checkpoint whose body parsed (`Parsed=true`) — the checkpoint-compliance numerator. Divide the two in your dashboard/alert query rather than here: a pre-divided ratio has no honest value while the denominator is 0, and this endpoint has no way to mark a series "not applicable" instead of `0`. |

```promql
crewshipd_session_runs_checkpointed_total / crewshipd_session_runs_finished_total > 0.95
```

#### Human comprehension — "how often does a run need a human, and does routing hold"

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `crewshipd_assignment_outcomes` | gauge | `outcome` (`no_change`, `succeeded`, `work_created`, `partial`, `needs_human`, `failed`, `cancelled`, `other`) | Terminal assignments by outcome contract result (§9.6). The `needs_human` share is the direct "how often does a human have to look" signal. |
| `crewshipd_outcome_routing_violations` | gauge | — | Runs whose outcome was **not** `NEEDS_HUMAN` but which raised a `run_needs_human` inbox item anyway. Target `0` — the §9.6 routing table says this must never happen; a nonzero value means the outcome contract and the inbox disagree. |

## Freshness and cost

The DB-derived block is computed from indexed counts and cached for **15
seconds** — scraping more often than that returns the same snapshot. At
typical 15–60s scrape intervals this is invisible; it exists so a scraper
retry storm (or an abusive client that got hold of the token) cannot turn
`/metrics` into a query amplifier.

For traces and OTLP export, see [OTLP setup](/observability/otlp-setup).
