// Multi-dimensional run filtering + grouping for the /activity left
// rail. Pure functions, no React; the rail component composes these
// over the data it already has so we don't add new endpoints.
//
// Why split filter + group:
//   - filtering is "show me X" (returns a smaller list)
//   - grouping is "show me X organised by Y" (returns a tree)
// Linear keeps these as separate UX gestures and the user clearly
// asked for both — a routine that fires every minute should
// disappear into a single "cron · daily-etl · 234 runs" node, not
// flood the rail with 234 rows.

import type { PipelineRun } from "@/hooks/use-pipeline-runs"

// ── Filter ──────────────────────────────────────────────────────────

export type StatusBucket = "all" | "active" | "completed" | "failed"
export type DateRangeKey = "1h" | "24h" | "7d" | "all"
export type TriggerSource =
  | "manual"
  | "schedule"
  | "webhook"
  | "issue"
  | "call_pipeline"

export interface RunFilter {
  status?: StatusBucket
  search?: string
  crews?: string[] // crew_id list
  agents?: string[] // agent slug list
  routines?: string[] // pipeline_slug list
  sources?: TriggerSource[]
  issueIdentifiers?: string[]
  dateRange?: DateRangeKey
  costMin?: number
  costMax?: number
  durationMinMs?: number
  durationMaxMs?: number
  hasWaitpoint?: boolean
}

const ACTIVE_STATUSES = new Set(["running", "queued", "paused"])
const FAILED_STATUSES = new Set(["failed", "cancelled", "interrupted"])

// applyFilters narrows a list of runs by every active filter
// dimension. Each predicate is independent so future dimensions only
// need a new branch + a public field on RunFilter.
export function applyFilters(
  runs: PipelineRun[],
  f: RunFilter,
  // Optional: set of run ids that currently have a pending waitpoint.
  // The rail passes this in; if absent, hasWaitpoint filter is a no-op.
  runsWithWaitpoint?: ReadonlySet<string>,
): PipelineRun[] {
  const cutoffMs = dateRangeCutoff(f.dateRange)
  const search = (f.search ?? "").trim().toLowerCase()

  return runs.filter((r) => {
    if (f.status === "active" && !ACTIVE_STATUSES.has(r.status)) return false
    if (f.status === "completed" && r.status !== "completed") return false
    if (f.status === "failed" && !FAILED_STATUSES.has(r.status)) return false

    if (f.crews?.length && !f.crews.includes(r.invoking_crew_id)) return false
    if (f.agents?.length && !f.agents.includes(r.invoking_agent_id)) return false
    if (f.routines?.length && !f.routines.includes(r.pipeline_slug)) return false
    if (f.sources?.length && !f.sources.includes(r.triggered_via as TriggerSource))
      return false
    if (
      f.issueIdentifiers?.length &&
      (!r.issue_identifier || !f.issueIdentifiers.includes(r.issue_identifier))
    )
      return false

    if (cutoffMs !== null) {
      const ts = parseTs(r.started_at)
      if (ts === null || ts < cutoffMs) return false
    }

    if (f.costMin !== undefined && r.cost_usd < f.costMin) return false
    if (f.costMax !== undefined && r.cost_usd > f.costMax) return false
    if (f.durationMinMs !== undefined && r.duration_ms < f.durationMinMs) return false
    if (f.durationMaxMs !== undefined && r.duration_ms > f.durationMaxMs) return false

    if (f.hasWaitpoint && (!runsWithWaitpoint || !runsWithWaitpoint.has(r.id)))
      return false

    if (search) {
      // Match against id, slug, name, issue identifier — same fields
      // the user can see in the rail row, so the match feels
      // predictable.
      const hay =
        `${r.id} ${r.pipeline_slug} ${r.pipeline_name ?? ""} ${r.issue_identifier ?? ""}`.toLowerCase()
      if (!hay.includes(search)) return false
    }

    return true
  })
}

function dateRangeCutoff(range?: DateRangeKey): number | null {
  if (!range || range === "all") return null
  const now = Date.now()
  switch (range) {
    case "1h":
      return now - 60 * 60 * 1000
    case "24h":
      return now - 24 * 60 * 60 * 1000
    case "7d":
      return now - 7 * 24 * 60 * 60 * 1000
  }
}

function parseTs(iso?: string): number | null {
  if (!iso) return null
  const t = new Date(iso).getTime()
  return Number.isNaN(t) ? null : t
}

// activeFilterCount — used by the toolbar to badge the [Filter • N]
// button. Excludes status (always-visible segmented control) and
// search (its own box).
export function activeFilterCount(f: RunFilter): number {
  let n = 0
  if (f.crews?.length) n++
  if (f.agents?.length) n++
  if (f.routines?.length) n++
  if (f.sources?.length) n++
  if (f.issueIdentifiers?.length) n++
  if (f.dateRange && f.dateRange !== "all") n++
  if (f.costMin !== undefined || f.costMax !== undefined) n++
  if (f.durationMinMs !== undefined || f.durationMaxMs !== undefined) n++
  if (f.hasWaitpoint) n++
  return n
}

// ── Grouping ────────────────────────────────────────────────────────

export type GroupAxis = "source" | "routine" | "crew" | "issue" | "none"

// Two-level tree: top group → optional sub-groups → leaf runs.
// Built so the renderer can blindly iterate without knowing the axis.
export interface RunGroup {
  key: string
  label: string
  kind:
    | "cron"
    | "issue"
    | "webhook"
    | "manual"
    | "call_pipeline"
    | "crew"
    | "routine"
    | "all"
  // Aggregated status pip — picks the most "interesting" status the
  // tree contains (running > paused > failed > completed).
  status: "running" | "paused" | "failed" | "completed" | "mixed" | "queued"
  // Either subgroups (deeper tree) or runs (leaf), never both. Keeps
  // the renderer trivial.
  subgroups?: RunGroup[]
  runs?: PipelineRun[]
  // Counts roll up from subgroup runs.
  totalRuns: number
  // Optional metadata the row header renders. None of these are
  // structural, just hints for the label.
  metadata?: {
    cronExpr?: string
    issueIdentifier?: string
    crewName?: string
    routineSlug?: string
    routineName?: string
    failureCount?: number
  }
}

interface GroupContext {
  // pipeline_slug → cron expression. Only present for routines that
  // are bound to a schedule. Used to label cron parent rows.
  cronBySlug?: ReadonlyMap<string, string>
  // pipeline_slug → human pipeline name. We already have it on the
  // run row via pipeline_name, this is a fallback.
  routineNameBySlug?: ReadonlyMap<string, string>
  // crew_id → crew name. For crew grouping.
  crewNameById?: ReadonlyMap<string, string>
}

// groupRuns turns a flat list into a tree under one of five axes.
// The default axis ("source") is the one that solves the cron noise
// problem the user flagged: 234 cron runs of one routine collapse
// under a single "daily-etl · every 1h · 234" parent node.
export function groupRuns(
  runs: PipelineRun[],
  axis: GroupAxis,
  ctx: GroupContext = {},
): RunGroup[] {
  if (axis === "none") {
    return runs.length === 0
      ? []
      : [
          {
            key: "all",
            label: "All runs",
            kind: "all",
            status: rollupStatus(runs),
            runs: sortByStartedAtDesc(runs),
            totalRuns: runs.length,
          },
        ]
  }

  if (axis === "source") return groupBySource(runs, ctx)
  if (axis === "routine") return groupByRoutine(runs, ctx)
  if (axis === "crew") return groupByCrew(runs, ctx)
  if (axis === "issue") return groupByIssue(runs, ctx)
  return []
}

// Group by trigger source, then sub-group inside cron / issue /
// webhook by routine or issue identifier; manual + call_pipeline
// stay flat (sub-groups would just add ceremony).
function groupBySource(runs: PipelineRun[], ctx: GroupContext): RunGroup[] {
  const buckets: Record<TriggerSource, PipelineRun[]> = {
    manual: [],
    schedule: [],
    webhook: [],
    issue: [],
    call_pipeline: [],
  }
  for (const r of runs) {
    const s = (r.triggered_via as TriggerSource) || "manual"
    if (buckets[s]) buckets[s].push(r)
    else buckets.manual.push(r)
  }

  const out: RunGroup[] = []

  if (buckets.schedule.length) {
    out.push({
      key: "src:cron",
      label: "Cron",
      kind: "cron",
      status: rollupStatus(buckets.schedule),
      totalRuns: buckets.schedule.length,
      subgroups: groupByRoutine(buckets.schedule, ctx, "cron-"),
    })
  }
  if (buckets.issue.length) {
    out.push({
      key: "src:issue",
      label: "Issue",
      kind: "issue",
      status: rollupStatus(buckets.issue),
      totalRuns: buckets.issue.length,
      subgroups: groupByIssue(buckets.issue, ctx, "issue-"),
    })
  }
  if (buckets.webhook.length) {
    out.push({
      key: "src:webhook",
      label: "Webhook",
      kind: "webhook",
      status: rollupStatus(buckets.webhook),
      totalRuns: buckets.webhook.length,
      subgroups: groupByRoutine(buckets.webhook, ctx, "wh-"),
    })
  }
  if (buckets.manual.length) {
    out.push({
      key: "src:manual",
      label: "Manual",
      kind: "manual",
      status: rollupStatus(buckets.manual),
      totalRuns: buckets.manual.length,
      runs: sortByStartedAtDesc(buckets.manual),
    })
  }
  if (buckets.call_pipeline.length) {
    out.push({
      key: "src:call",
      label: "Sub-routine",
      kind: "call_pipeline",
      status: rollupStatus(buckets.call_pipeline),
      totalRuns: buckets.call_pipeline.length,
      runs: sortByStartedAtDesc(buckets.call_pipeline),
    })
  }
  return out
}

function groupByRoutine(
  runs: PipelineRun[],
  ctx: GroupContext,
  keyPrefix = "rt-",
): RunGroup[] {
  const buckets = new Map<string, PipelineRun[]>()
  for (const r of runs) {
    const k = r.pipeline_slug
    const arr = buckets.get(k) ?? []
    arr.push(r)
    buckets.set(k, arr)
  }
  return Array.from(buckets, ([slug, list]) => {
    const failureCount = list.filter((r) => FAILED_STATUSES.has(r.status)).length
    const sample = list[0]
    return {
      key: `${keyPrefix}${slug}`,
      label: sample.pipeline_name || slug,
      kind: "routine" as const,
      status: rollupStatus(list),
      totalRuns: list.length,
      runs: sortByStartedAtDesc(list),
      metadata: {
        routineSlug: slug,
        routineName: sample.pipeline_name || ctx.routineNameBySlug?.get(slug),
        cronExpr: ctx.cronBySlug?.get(slug),
        failureCount: failureCount > 0 ? failureCount : undefined,
      },
    }
  }).sort((a, b) => b.totalRuns - a.totalRuns)
}

function groupByIssue(
  runs: PipelineRun[],
  _ctx: GroupContext,
  keyPrefix = "iss-",
): RunGroup[] {
  const buckets = new Map<string, PipelineRun[]>()
  const noIssue: PipelineRun[] = []
  for (const r of runs) {
    if (r.issue_identifier) {
      const arr = buckets.get(r.issue_identifier) ?? []
      arr.push(r)
      buckets.set(r.issue_identifier, arr)
    } else {
      noIssue.push(r)
    }
  }
  const out: RunGroup[] = Array.from(buckets, ([id, list]) => {
    const sample = list[0]
    return {
      key: `${keyPrefix}${id}`,
      label: sample.pipeline_name || sample.pipeline_slug,
      kind: "issue" as const,
      status: rollupStatus(list),
      totalRuns: list.length,
      runs: sortByStartedAtDesc(list),
      metadata: {
        issueIdentifier: id,
        routineSlug: sample.pipeline_slug,
      },
    }
  })
  if (noIssue.length) {
    out.push({
      key: `${keyPrefix}_orphan`,
      label: "Without an issue",
      kind: "issue",
      status: rollupStatus(noIssue),
      totalRuns: noIssue.length,
      runs: sortByStartedAtDesc(noIssue),
    })
  }
  return out.sort((a, b) => b.totalRuns - a.totalRuns)
}

function groupByCrew(runs: PipelineRun[], ctx: GroupContext): RunGroup[] {
  const buckets = new Map<string, PipelineRun[]>()
  for (const r of runs) {
    const k = r.invoking_crew_id || "_no_crew"
    const arr = buckets.get(k) ?? []
    arr.push(r)
    buckets.set(k, arr)
  }
  return Array.from(buckets, ([crewId, list]) => ({
    key: `crew-${crewId}`,
    label: ctx.crewNameById?.get(crewId) || (crewId === "_no_crew" ? "No crew" : crewId),
    kind: "crew" as const,
    status: rollupStatus(list),
    totalRuns: list.length,
    runs: sortByStartedAtDesc(list),
    metadata: { crewName: ctx.crewNameById?.get(crewId) },
  })).sort((a, b) => b.totalRuns - a.totalRuns)
}

// Pick the most attention-grabbing status from a list of runs:
// running ≫ paused ≫ failed ≫ queued ≫ completed.
function rollupStatus(list: PipelineRun[]): RunGroup["status"] {
  let hasRunning = false
  let hasPaused = false
  let hasFailed = false
  let hasQueued = false
  let hasCompleted = false
  for (const r of list) {
    if (r.status === "running") hasRunning = true
    else if (r.status === "paused") hasPaused = true
    else if (FAILED_STATUSES.has(r.status)) hasFailed = true
    else if (r.status === "queued") hasQueued = true
    else if (r.status === "completed") hasCompleted = true
  }
  if (hasRunning) return "running"
  if (hasPaused) return "paused"
  if (hasFailed) return "failed"
  if (hasQueued) return "queued"
  if (hasCompleted) return "completed"
  return "mixed"
}

function sortByStartedAtDesc(runs: PipelineRun[]): PipelineRun[] {
  return [...runs].sort((a, b) => {
    const ta = parseTs(a.started_at) ?? 0
    const tb = parseTs(b.started_at) ?? 0
    return tb - ta
  })
}
