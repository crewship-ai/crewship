"use client"

import * as React from "react"

import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"

/** Pure: "Good morning, Pavel" by the person's local hour; no name, no comma. */
export function greeting(now: Date, name?: string | null): string {
  const hour = now.getHours()
  const part = hour < 12 ? "morning" : hour < 18 ? "afternoon" : "evening"
  const first = (name ?? "").trim().split(/\s+/)[0]
  return first ? `Good ${part}, ${first}` : `Good ${part}`
}

/** Pure: a short distance to a future instant — "18 min", "3 h", "2 d". */
export function formatUntil(ms: number): string {
  const min = Math.max(1, Math.round(ms / 60_000))
  if (min < 60) return `${min} min`
  const h = Math.round(min / 60)
  if (h < 48) return `${h} h`
  return `${Math.round(h / 24)} d`
}

/** Pure: the soonest enabled schedule still ahead of `now`, or null. */
export function nextScheduledRun(schedules: PipelineSchedule[], now: number): number | null {
  let best: number | null = null
  for (const s of schedules) {
    if (!s.enabled || !s.next_run_at) continue
    const at = new Date(s.next_run_at).getTime()
    if (!Number.isFinite(at) || at <= now) continue
    if (best == null || at < best) best = at
  }
  return best
}

/**
 * Pure: the line under the greeting. Parts that would say zero are left out;
 * "Nothing needs you" is said only when the inbox actually answered, because
 * it is a claim about the workspace, not the absence of one.
 */
export function dashboardMeta({
  now,
  attentionCount,
  attentionKnown,
  runningAgents,
  nextRunAt,
}: {
  now: Date
  attentionCount: number
  attentionKnown: boolean
  runningAgents: number
  nextRunAt: number | null
}): string {
  const parts = [now.toLocaleDateString("en-GB", { weekday: "long", day: "numeric", month: "long" })]
  if (attentionCount > 0) parts.push(`${attentionCount} ${attentionCount === 1 ? "thing needs" : "things need"} you`)
  else if (attentionKnown) parts.push("Nothing needs you")
  if (runningAgents > 0) parts.push(`${runningAgents} ${runningAgents === 1 ? "agent" : "agents"} running`)
  if (nextRunAt != null && nextRunAt > now.getTime()) parts.push(`next routine in ${formatUntil(nextRunAt - now.getTime())}`)
  return parts.join(" · ")
}

/** A clock that ticks once a minute, so the greeting and "in 18 min" stay true. */
export function useMinuteClock(): Date {
  const [now, setNow] = React.useState(() => new Date())
  React.useEffect(() => {
    const t = window.setInterval(() => setNow(new Date()), 60_000)
    return () => window.clearInterval(t)
  }, [])
  return now
}

/** The page head: the greeting as the page's visible heading, the live line under it. */
export function DashboardHead({ title, meta }: { title: string; meta: string }) {
  return (
    <div className="mb-4 min-w-0 md:mb-5">
      {/* The page's one h1: the top bar's SubBar renders its title without one. */}
      <h1 data-testid="dashboard-greeting" className="text-heading font-semibold tracking-tight text-foreground text-balance md:text-title">
        {title}
      </h1>
      <p className="mt-0.5 text-control text-muted-foreground">{meta}</p>
    </div>
  )
}
