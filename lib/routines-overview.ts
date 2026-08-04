// The arithmetic behind the routines overview.
//
// The page it replaces spent its main pane on a table of every routine
// in the workspace — 38 rows, 37 of which read "never invoked · 0 · —".
// The sidebar beside it was already the catalog, searchable and
// filtered, so the table was the same list rendered twice, and the
// second copy was the one carrying no information.
//
// What went in its place answers what an operator actually asks on
// arrival: did anything run today, is it healthy, what fires next,
// what needs me. Those are the functions below.
//
// Framework-free on purpose (mirrors lib/routines-insights.ts): these
// are the numbers a reader will argue with, and they should be
// arguable without rendering anything. Inputs are structural — the
// minimum field set each function reads — rather than the full wire
// types, so a change to an unrelated column cannot break them.

export interface OverviewRun {
  id: string
  pipeline_slug: string
  pipeline_name?: string
  status: string
  started_at: string
  ended_at?: string
  cost_usd?: number | null
  duration_ms?: number | null
  triggered_via?: string
  current_step_id?: string
}

export interface OverviewRoutine {
  slug: string
  name?: string
  icon?: string
  color?: string
  invocation_count?: number
  last_invocation_status?: string
  status?: string
}

export interface OverviewSchedule {
  id: string
  name: string
  enabled: boolean
  cron_expr: string
  timezone?: string
  next_run_at?: string
  target_pipeline_slug?: string
  wake_pipeline_slug?: string
}

/** Statuses that mean the run is still happening. */
const LIVE = new Set(["running", "queued", "paused", "waiting"])
const FAILED = new Set(["failed", "error"])
const SUCCEEDED = new Set(["completed", "succeeded", "success"])

export function isLiveStatus(status: string | undefined): boolean {
  return LIVE.has((status ?? "").toLowerCase())
}

function sameLocalDay(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  )
}

function parsed(iso: string | undefined): Date | null {
  if (!iso) return null
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? null : d
}

/**
 * How much ran today, and how much of it broke.
 *
 * Replaces a lifetime invocation counter. "1 run, ever" is trivia; "1
 * run today, 0 failed" is a shift report. The day is the LOCAL
 * calendar day, because that is the day the person reading it is
 * having.
 */
export function runsToday(runs: OverviewRun[], now: Date): { total: number; failed: number } {
  let total = 0
  let failed = 0
  for (const r of runs) {
    const started = parsed(r.started_at)
    if (!started || !sameLocalDay(started, now)) continue
    total++
    if (FAILED.has((r.status ?? "").toLowerCase())) failed++
  }
  return { total, failed }
}

/**
 * Success rate over a window, WITH its denominator.
 *
 * The tile this replaces read "100%" off a single run. A rate with no
 * sample size is unweighable, and at n=1 it is close to meaningless —
 * so the caller renders "100% · 1 of 1" and lets the reader discount
 * it themselves.
 *
 * Only terminal verdicts count. A cancelled or interrupted run is a
 * human changing their mind or a process restart, not the routine
 * failing; counting either as a failure would make the health number
 * punish an operator for intervening.
 */
export function successRate(
  runs: OverviewRun[],
  now: Date,
  days: number,
): { pct: number | null; ok: number; total: number } {
  const since = now.getTime() - days * 24 * 60 * 60 * 1000
  let ok = 0
  let total = 0
  for (const r of runs) {
    const started = parsed(r.started_at)
    if (!started || started.getTime() < since) continue
    const s = (r.status ?? "").toLowerCase()
    if (SUCCEEDED.has(s)) {
      ok++
      total++
    } else if (FAILED.has(s)) {
      total++
    }
  }
  return { pct: total > 0 ? Math.round((ok / total) * 100) : null, ok, total }
}

/**
 * The next schedule that will actually fire.
 *
 * A `next_run_at` in the past means the scheduler has not caught up.
 * That is not "the next run" — surfacing it as one reads as a stopped
 * clock and sends people looking for a bug in the wrong place.
 */
export function nextScheduled(schedules: OverviewSchedule[], now: Date): OverviewSchedule | null {
  let best: OverviewSchedule | null = null
  let bestAt = Infinity
  for (const s of schedules) {
    if (!s.enabled) continue
    const at = parsed(s.next_run_at)
    if (!at || at.getTime() <= now.getTime()) continue
    if (at.getTime() < bestAt) {
      bestAt = at.getTime()
      best = s
    }
  }
  return best
}

/**
 * How many routines want a human.
 *
 * Deliberately a count of ROUTINES, not of problems: a routine that is
 * both failing and awaiting approval is one thing to go and look at,
 * and counting it twice would inflate the only number on this page
 * that is supposed to mean "your queue is this long".
 */
export function needsAttention(routines: OverviewRoutine[]): {
  total: number
  failing: number
  awaitingApproval: number
} {
  let failing = 0
  let awaitingApproval = 0
  let total = 0
  for (const r of routines) {
    const isFailing = FAILED.has((r.last_invocation_status ?? "").toLowerCase())
    const isProposed = r.status === "proposed"
    if (isFailing) failing++
    if (isProposed) awaitingApproval++
    if (isFailing || isProposed) total++
  }
  return { total, failing, awaitingApproval }
}

export interface CatalogBucket {
  key: "live" | "healthy" | "failing" | "awaiting" | "disabled" | "never"
  label: string
  count: number
  color: string
  /** Sidebar filter this arc corresponds to, for click-through. */
  filter: string
}

// Shared with the dashboard's mission donut so a green arc means the
// same thing on both pages.
const ARC = {
  live: "rgb(96, 165, 250)",
  healthy: "rgb(52, 211, 153)",
  failing: "rgb(248, 113, 113)",
  awaiting: "rgb(251, 191, 36)",
  disabled: "rgb(148, 163, 184)",
  never: "rgb(71, 85, 105)",
}

/**
 * The catalog as one shape.
 *
 * Every routine lands in exactly one arc, so the arcs sum to the
 * catalog — a donut whose slices do not add up to the number in its
 * centre is worse than no donut. Order is fixed and empty buckets are
 * kept: a legend that reshuffles as counts change cannot be read at a
 * glance, and "Failing 0" is a fact worth stating.
 *
 * Live wins over the last result, because a routine running right now
 * is a routine whose previous failure you are already past.
 */
export function catalogBuckets(routines: OverviewRoutine[], liveSlugs: Set<string>): CatalogBucket[] {
  const counts = { live: 0, healthy: 0, failing: 0, awaiting: 0, disabled: 0, never: 0 }
  for (const r of routines) {
    if (liveSlugs.has(r.slug)) counts.live++
    else if (r.status === "proposed") counts.awaiting++
    else if (r.status === "disabled") counts.disabled++
    else if (FAILED.has((r.last_invocation_status ?? "").toLowerCase())) counts.failing++
    else if ((r.invocation_count ?? 0) > 0) counts.healthy++
    else counts.never++
  }
  return [
    { key: "live", label: "Running now", count: counts.live, color: ARC.live, filter: "running" },
    { key: "healthy", label: "Healthy", count: counts.healthy, color: ARC.healthy, filter: "completed" },
    { key: "failing", label: "Failing", count: counts.failing, color: ARC.failing, filter: "failed" },
    { key: "awaiting", label: "Awaiting approval", count: counts.awaiting, color: ARC.awaiting, filter: "awaiting" },
    { key: "disabled", label: "Disabled", count: counts.disabled, color: ARC.disabled, filter: "all" },
    { key: "never", label: "Never invoked", count: counts.never, color: ARC.never, filter: "never" },
  ]
}

/** Enabled schedules with a future firing time, soonest first. */
export function upcomingSchedules(
  schedules: OverviewSchedule[],
  now: Date,
  limit: number,
): OverviewSchedule[] {
  return schedules
    .filter((s) => {
      const at = parsed(s.next_run_at)
      return s.enabled && at !== null && at.getTime() > now.getTime()
    })
    .sort((a, b) => Date.parse(a.next_run_at!) - Date.parse(b.next_run_at!))
    .slice(0, limit)
}

/**
 * The last N runs, live ones first.
 *
 * A run happening now is what someone opening this page is looking
 * for, even when it started before the most recent completed one —
 * so live is pinned rather than merely sorted.
 */
export function recentRuns(runs: OverviewRun[], limit: number): OverviewRun[] {
  const byNewest = (a: OverviewRun, b: OverviewRun) =>
    (parsed(b.started_at)?.getTime() ?? 0) - (parsed(a.started_at)?.getTime() ?? 0)
  const live = runs.filter((r) => isLiveStatus(r.status)).sort(byNewest)
  const done = runs.filter((r) => !isLiveStatus(r.status)).sort(byNewest)
  return [...live, ...done].slice(0, limit)
}

export interface SpendDay {
  /** Short weekday label for the axis. */
  label: string
  usd: number
  isToday: boolean
}

/**
 * Daily spend across the window, oldest first.
 *
 * Always returns one bucket per day, including empty ones: a chart
 * that omits quiet days compresses the gap and implies activity that
 * did not happen. Negative and non-finite costs are dropped rather
 * than summed — one malformed row must not be able to make the total
 * go backwards.
 */
export function spendByDay(runs: OverviewRun[], now: Date, days: number): SpendDay[] {
  const out: SpendDay[] = []
  for (let i = days - 1; i >= 0; i--) {
    const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() - i)
    out.push({
      label: d.toLocaleDateString(undefined, { weekday: "short" }),
      usd: 0,
      isToday: i === 0,
    })
  }
  for (const r of runs) {
    const started = parsed(r.started_at)
    if (!started) continue
    const dayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate())
    const diff = Math.floor((dayStart.getTime() - new Date(started.getFullYear(), started.getMonth(), started.getDate()).getTime()) / 86_400_000)
    if (diff < 0 || diff >= days) continue
    const c = r.cost_usd
    if (typeof c !== "number" || !Number.isFinite(c) || c <= 0) continue
    out[days - 1 - diff].usd += c
  }
  return out
}

export interface OverviewWaitpoint {
  token: string
  pipeline_run_id: string
  step_id: string
  kind: string
  prompt: string
  timeout_at?: string
  created_at?: string
}

export type PendingApproval =
  | {
      kind: "run"
      token: string
      runId: string
      stepId: string
      prompt: string
      routineSlug?: string
      routineName?: string
      expiresAt?: string
    }
  | { kind: "routine"; slug: string; name: string }

/**
 * Everything currently waiting on a human, as one queue.
 *
 * Two sources that feel like one job. A run parked on a `wait:
 * approval` step has stopped mid-flight and holds real state; a
 * routine saved as `proposed` cannot run at all until someone reviews
 * its definition. From the operator's side both are "something
 * stopped, and it is waiting for me" — so they belong on one card
 * rather than in two places neither of which is the whole answer.
 *
 * Parked runs sort first, and among themselves by how soon they
 * expire: a waitpoint has a timeout and a live process behind it,
 * while a proposal sits still indefinitely. A waitpoint with no
 * timeout sorts LAST among runs rather than first — parsing an absent
 * timestamp as zero would rank "never expires" as the most urgent
 * thing on the page and push a genuinely expiring approval below it.
 *
 * A waitpoint carries a run id, not a slug, so the routine name comes
 * from a run→slug lookup. When that lookup misses the row is still
 * listed, unnamed: dropping it would hide a blocked run, which is the
 * one thing this card exists to prevent.
 */
export function pendingApprovals(
  waitpoints: OverviewWaitpoint[],
  routines: OverviewRoutine[],
  slugByRunId: ReadonlyMap<string, string>,
): PendingApproval[] {
  const bySlug = new Map(routines.map((r) => [r.slug, r]))
  const runs: PendingApproval[] = waitpoints
    .map((w) => {
      const slug = slugByRunId.get(w.pipeline_run_id)
      const r = slug ? bySlug.get(slug) : undefined
      return {
        kind: "run" as const,
        token: w.token,
        runId: w.pipeline_run_id,
        stepId: w.step_id,
        prompt: w.prompt,
        routineSlug: slug,
        routineName: r?.name,
        expiresAt: w.timeout_at || undefined,
      }
    })
    .sort((a, b) => expiryRank(a.expiresAt) - expiryRank(b.expiresAt))

  const proposed: PendingApproval[] = routines
    .filter((r) => r.status === "proposed")
    .map((r) => ({ kind: "routine" as const, slug: r.slug, name: r.name || r.slug }))
    .sort((a, b) => a.name.localeCompare(b.name))

  return [...runs, ...proposed]
}

/** Missing or unparseable expiry sorts last, not first. */
function expiryRank(iso: string | undefined): number {
  const d = parsed(iso)
  return d ? d.getTime() : Number.POSITIVE_INFINITY
}
