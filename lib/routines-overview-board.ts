// The top of the Routines overview (Harbor 2 RT-OV, routines.md R02): what
// needs a person and what starts in the next 24 hours, composed in the
// browser from lists the page already loads — the approval waitpoints, the
// live runs, the routines, the schedules and the accepted one-time starts.
//
// A waitpoint's token authorises its decision on its own, so it never leaves
// this module: an item carries the run id, and whoever decides looks the
// token up again (routines.md §5).

import { isAwaitingApproval } from "@/hooks/use-active-routine-runs"

export type NeedsKind = "decision" | "proposed" | "failed" | "breaker" | "off" | "not-run"

export interface OverviewNeed {
  key: string
  kind: NeedsKind
  slug: string
  name: string
  title: string
  detail: string
  /** The run to open for a decision. */
  runId?: string
  /** A plain approval (no decision form) can be answered in place. */
  approvable?: boolean
  /** When the decision times out. */
  expiresAt?: string
}

interface NeedRoutine {
  slug: string
  name?: string
  status?: string
  last_invocation_status?: string
  last_run_outcome?: string
}
interface NeedRun {
  id: string
  pipeline_slug: string
  pipeline_name?: string
  status: string
}
interface NeedWaitpoint {
  token: string
  pipeline_run_id: string
  step_id: string
  kind: string
  prompt?: string
  timeout_at?: string
  created_at?: string
  decision_form?: unknown
}
interface NeedSchedule {
  id: string
  target_pipeline_slug?: string
  enabled: boolean
  disabled_reason?: string
  consecutive_failures?: number
}

const firstLine = (s?: string) => (s ?? "").split(/\r?\n/)[0].trim()

export function overviewNeeds(input: {
  routines: readonly NeedRoutine[]
  activeRuns: readonly NeedRun[]
  waitpoints: readonly NeedWaitpoint[]
  schedules: readonly NeedSchedule[]
  /** Accepted one-time starts that did not run, per routine. */
  notRun?: readonly { pipeline_slug: string }[]
}): OverviewNeed[] {
  const nameOf = (slug: string, fallback?: string) => input.routines.find((r) => r.slug === slug)?.name || fallback || slug
  const out: OverviewNeed[] = []
  for (const run of input.activeRuns) {
    if (!isAwaitingApproval(run.status)) continue
    const wp = input.waitpoints.find((w) => w.pipeline_run_id === run.id && w.kind === "approval")
    const name = nameOf(run.pipeline_slug, run.pipeline_name)
    out.push({
      key: `decision-${run.id}`,
      kind: "decision",
      slug: run.pipeline_slug,
      name,
      title: `${name} is waiting for a decision`,
      detail: wp ? firstLine(wp.prompt) || "A person decides before it continues" : "Waiting for a person",
      runId: run.id,
      approvable: !!wp && !wp.decision_form,
      expiresAt: wp?.timeout_at,
    })
  }
  for (const r of input.routines)
    if (r.status === "proposed")
      out.push({ key: `p-${r.slug}`, kind: "proposed", slug: r.slug, name: nameOf(r.slug), title: `${nameOf(r.slug)} is proposed`, detail: "A Manager or above approves it before it can run" })
  for (const r of input.routines)
    if (r.status !== "disabled" && r.status !== "proposed" && (r.last_invocation_status?.toLowerCase() === "failed" || r.last_run_outcome === "FAILED"))
      out.push({ key: `f-${r.slug}`, kind: "failed", slug: r.slug, name: nameOf(r.slug), title: `${nameOf(r.slug)} could not finish`, detail: "Its last run ended before the work was done" })
  const breaker = new Set<string>()
  for (const s of input.schedules) {
    const slug = s.target_pipeline_slug
    if (!slug || s.enabled || s.disabled_reason !== "circuit_breaker" || breaker.has(slug)) continue
    breaker.add(slug)
    out.push({
      key: `b-${slug}`,
      kind: "breaker",
      slug,
      name: nameOf(slug),
      title: `${nameOf(slug)} was turned off`,
      detail: `${s.consecutive_failures ?? "Several"} failures in a row turned its schedule off (circuit breaker)`,
    })
  }
  for (const r of input.routines)
    if (r.status === "disabled" && !breaker.has(r.slug))
      out.push({ key: `d-${r.slug}`, kind: "off", slug: r.slug, name: nameOf(r.slug), title: `${nameOf(r.slug)} is turned off`, detail: "Nothing starts it until it is turned on" })
  const notRun = new Map<string, number>()
  for (const p of input.notRun ?? []) notRun.set(p.pipeline_slug, (notRun.get(p.pipeline_slug) ?? 0) + 1)
  for (const [slug, n] of notRun)
    out.push({ key: `n-${slug}`, kind: "not-run", slug, name: nameOf(slug), title: `${nameOf(slug)}: ${n} accepted ${n === 1 ? "start" : "starts"} did not run`, detail: "Open the routine's triggers to see why" })
  return out
}

export interface NextStart {
  key: string
  slug: string
  name: string
  at: string
  kind: "schedule" | "once"
}

/** Enabled schedules and planned one-time starts due within 24 hours, soonest first. */
export function next24Hours(input: {
  routines: readonly NeedRoutine[]
  schedules: readonly (NeedSchedule & { next_run_at?: string })[]
  starts: readonly { id: string; pipeline_slug: string; fire_at: string; status?: string }[]
  now?: Date
}): NextStart[] {
  const now = (input.now ?? new Date()).getTime()
  const until = now + 24 * 3_600_000
  const within = (iso?: string) => {
    const t = iso ? Date.parse(iso) : NaN
    return Number.isFinite(t) && t >= now && t <= until
  }
  const nameOf = (slug: string) => input.routines.find((r) => r.slug === slug)?.name || slug
  const rows: NextStart[] = []
  for (const s of input.schedules)
    if (s.enabled && s.target_pipeline_slug && within(s.next_run_at))
      rows.push({ key: `s-${s.id}`, slug: s.target_pipeline_slug, name: nameOf(s.target_pipeline_slug), at: s.next_run_at!, kind: "schedule" })
  for (const p of input.starts)
    if ((p.status ?? "pending") === "pending" && within(p.fire_at)) rows.push({ key: `o-${p.id}`, slug: p.pipeline_slug, name: nameOf(p.pipeline_slug), at: p.fire_at, kind: "once" })
  return rows.sort((a, b) => Date.parse(a.at) - Date.parse(b.at))
}
