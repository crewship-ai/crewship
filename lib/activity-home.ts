// The Activity home — what ran, what needs you, what broke (#2979).
//
// The overview it replaces was built from journal EVENTS: "139 events", a
// latest-events list of "Pipeline demo-marketing-check step notify completed",
// and a failures chart that was blank on a quiet day. A person opening Activity
// asks about RUNS. So this page is built from the runs themselves, and the
// rules that decide what it says live here, where a test can hold them.

import { formatDurationMs } from "@/lib/activity-stream"
import { runTone, type RunTone } from "@/lib/activity-run"

export interface HomeRun {
  id: string
  pipeline_slug: string
  pipeline_name: string
  status: string
  started_at: string
  duration_ms: number
  error_message?: string
  failed_at_step?: string
  current_step_id?: string
  triggered_via?: string
  cost_usd?: number
}

/* ------------------------------------------------------------------ *
 *  The header sentence
 * ------------------------------------------------------------------ */

export interface HeadlinePart {
  text: string
  tone: "default" | "primary" | "warn" | "destructive"
}

/**
 * "14 runs · 2 running · 1 needs you · 3 could not finish · $0.42" — one
 * sentence instead of four equal tiles. A zero is left out, except "nothing
 * needs you", which is the one zero a reader came to hear.
 */
export function headlineParts(c: {
  runs: number
  running: number
  waiting: number
  failed: number
  cost: number | null
}): HeadlinePart[] {
  const parts: HeadlinePart[] = [{ text: `${c.runs} ${c.runs === 1 ? "run" : "runs"}`, tone: "default" }]
  if (c.running > 0) parts.push({ text: `${c.running} running`, tone: "primary" })
  parts.push(c.waiting > 0 ? { text: `${c.waiting} needs you`, tone: "warn" } : { text: "nothing needs you", tone: "default" })
  if (c.failed > 0) parts.push({ text: `${c.failed} could not finish`, tone: "destructive" })
  if (c.cost != null && c.cost > 0) parts.push({ text: `$${c.cost.toFixed(2)}`, tone: "default" })
  return parts
}

/* ------------------------------------------------------------------ *
 *  What ran — one lane per routine
 * ------------------------------------------------------------------ */

export interface LaneBar {
  id: string
  tone: RunTone
  /** Percent of the window from its start. */
  left: number
  /** Percent of the window, never below a visible sliver. */
  width: number
  startedAt: string
  durationMs: number
}

export interface Lane {
  slug: string
  name: string
  bars: LaneBar[]
  summary: { runs: number; failed: number; active: number }
}

const MIN_BAR = 0.6

/**
 * Every run of the window as a bar on its routine's lane — the GitHub Actions /
 * Temporal picture: what runs on a beat, what failed, what is live. Lanes with
 * live or failing runs come first; the quiet tail folds into a count.
 */
export function runLanes(
  runs: HomeRun[],
  window: { from: number; to: number },
  maxLanes = 6,
): { lanes: Lane[]; hidden: number } {
  const span = Math.max(1, window.to - window.from)
  const bySlug = new Map<string, Lane>()
  for (const r of runs) {
    const start = Date.parse(r.started_at)
    if (!Number.isFinite(start) || start < window.from || start > window.to) continue
    const key = r.pipeline_slug || r.pipeline_name || "unknown"
    const lane = bySlug.get(key) ?? {
      slug: key,
      name: r.pipeline_name || r.pipeline_slug || "Routine",
      bars: [],
      summary: { runs: 0, failed: 0, active: 0 },
    }
    const tone = runTone(r.status)
    const left = ((start - window.from) / span) * 100
    const width = Math.min(100 - left, Math.max(MIN_BAR, ((r.duration_ms || 0) / span) * 100))
    lane.bars.push({
      id: r.id,
      tone,
      left: Math.min(left, 100 - MIN_BAR),
      width: Math.max(MIN_BAR, width),
      startedAt: r.started_at,
      durationMs: r.duration_ms || 0,
    })
    lane.summary.runs++
    if (tone === "failed") lane.summary.failed++
    if (tone === "running" || tone === "waiting") lane.summary.active++
    bySlug.set(key, lane)
  }
  const lanes = [...bySlug.values()]
  for (const l of lanes) l.bars.sort((a, b) => a.startedAt.localeCompare(b.startedAt))
  lanes.sort(
    (a, b) =>
      Number(b.summary.active > 0) - Number(a.summary.active > 0) ||
      Number(b.summary.failed > 0) - Number(a.summary.failed > 0) ||
      b.summary.runs - a.summary.runs ||
      a.name.localeCompare(b.name),
  )
  return { lanes: lanes.slice(0, maxLanes), hidden: Math.max(0, lanes.length - maxLanes) }
}

/* ------------------------------------------------------------------ *
 *  Outcomes per day
 * ------------------------------------------------------------------ */

export interface DayOutcome {
  key: string
  label: string
  today: boolean
  done: number
  failed: number
  other: number
}

/** The last `days` local days, oldest first, each split by how its runs ended. */
export function outcomesByDay(runs: HomeRun[], days: number, now = Date.now()): DayOutcome[] {
  const today = new Date(now)
  today.setHours(0, 0, 0, 0)
  const out: DayOutcome[] = []
  for (let i = days - 1; i >= 0; i--) {
    const d = new Date(today)
    d.setDate(d.getDate() - i)
    out.push({
      key: d.toDateString(),
      label: i === 0 ? "today" : d.toLocaleDateString(undefined, { weekday: "short" }),
      today: i === 0,
      done: 0,
      failed: 0,
      other: 0,
    })
  }
  const byKey = new Map(out.map((d) => [d.key, d]))
  for (const r of runs) {
    const t = Date.parse(r.started_at)
    if (!Number.isFinite(t)) continue
    const day = byKey.get(new Date(t).toDateString())
    if (!day) continue
    const tone = runTone(r.status)
    if (tone === "done") day.done++
    else if (tone === "failed") day.failed++
    else day.other++
  }
  return out
}

/* ------------------------------------------------------------------ *
 *  Up next
 * ------------------------------------------------------------------ */

export interface CalendarEvent {
  kind: string
  at: string
  slug: string
  name: string
}

/** The next planned runs after now, soonest first. */
export function upNext(events: CalendarEvent[], now = Date.now(), limit = 3): CalendarEvent[] {
  return events
    .filter((e) => e.kind === "planned" && Date.parse(e.at) > now)
    .sort((a, b) => a.at.localeCompare(b.at))
    .slice(0, limit)
}

/* ------------------------------------------------------------------ *
 *  One line per run
 * ------------------------------------------------------------------ */

/** What a run came to, in one line — the row text in "Latest runs". */
export function runLine(r: HomeRun): string {
  const tone = runTone(r.status)
  if (tone === "failed") {
    const first = (r.error_message ?? "").split(/\r?\n/)[0]?.trim()
    if (first) return first.length > 140 ? `${first.slice(0, 139)}…` : first
    return r.failed_at_step ? `Stopped at step “${r.failed_at_step}”` : "Could not finish"
  }
  if (tone === "waiting") return "Waiting for a decision"
  if (tone === "running") return r.current_step_id ? `Running · step “${r.current_step_id}”` : "Running"
  if (tone === "done") return r.duration_ms > 0 ? `Finished in ${formatDurationMs(r.duration_ms)}` : "Finished"
  if ((r.status ?? "").toLowerCase() === "interrupted") return "Interrupted — the process running it stopped"
  return "Cancelled before it finished"
}

/* ------------------------------------------------------------------ *
 *  What changed
 * ------------------------------------------------------------------ */

export interface EffectIssue {
  id: string
  identifier?: string
  title?: string
  created?: boolean
}

/** Issues the window's runs created, and the ones they only touched — once each. */
export function issueEffects(chains: { issues?: EffectIssue[] }[]): { created: EffectIssue[]; touched: EffectIssue[] } {
  const created = new Map<string, EffectIssue>()
  const touched = new Map<string, EffectIssue>()
  for (const c of chains) {
    for (const i of c.issues ?? []) {
      if (i.created) {
        created.set(i.id, i)
        touched.delete(i.id)
      } else if (!created.has(i.id)) {
        touched.set(i.id, i)
      }
    }
  }
  return { created: [...created.values()], touched: [...touched.values()] }
}

/* ------------------------------------------------------------------ *
 *  Problems
 * ------------------------------------------------------------------ */

export interface FailureGroup {
  fingerprint: string
  count: number
  pipeline_slug: string
  failed_at_step?: string
  sample_error?: string
  run_ids?: string[]
}

export interface Problem {
  fingerprint: string
  name: string
  step: string
  error: string
  /** Failures of this cause among the window's runs. */
  recent: number
  /** Failures of this cause ever recorded. */
  total: number
  latestRunId: string
}

/**
 * The server groups every failure ever recorded by cause. A problem is one that
 * is still happening, so a group counts only when one of its runs is in the
 * window — otherwise a cause fixed months ago would head this card forever.
 */
export function activeProblems(groups: FailureGroup[], runs: HomeRun[]): Problem[] {
  const byId = new Map(runs.map((r) => [r.id, r]))
  const out: Problem[] = []
  for (const g of groups) {
    const recent = (g.run_ids ?? []).filter((id) => byId.has(id))
    if (recent.length === 0) continue
    const first = byId.get(recent[0])!
    out.push({
      fingerprint: g.fingerprint,
      name: first.pipeline_name || g.pipeline_slug,
      step: g.failed_at_step ?? "",
      error: (g.sample_error ?? "").split(/\r?\n/)[0]?.trim() ?? "",
      recent: recent.length,
      total: g.count,
      latestRunId: recent[0],
    })
  }
  return out.sort((a, b) => b.recent - a.recent)
}
