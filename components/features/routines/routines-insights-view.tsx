"use client"

import { useMemo } from "react"
import { AlertCircle, CheckCircle2, Sparkles, TrendingUp } from "lucide-react"
import { cn } from "@/lib/utils"
import type { Pipeline } from "@/hooks/use-pipelines"

// RoutinesInsightsView — quick-glance health snapshot for the routine
// catalog. Surfaces three simple numbers (total runs, recent success
// rate, recent failures) plus a "top routines by usage" leaderboard.
//
// We intentionally compute everything from the cached pipeline list
// rather than firing a dedicated /insights endpoint. The list already
// carries invocation_count + last_invocation_status, which is enough
// for an MVP. A richer per-pipeline runs aggregate can replace this
// when the backend surface lands.

interface RoutinesInsightsViewProps {
  routines: Pipeline[]
  onSelect: (slug: string) => void
}

function statusBadge(status: string | undefined) {
  const s = status?.toLowerCase()
  if (s === "succeeded" || s === "success") {
    return <CheckCircle2 className="h-3 w-3 text-emerald-400" />
  }
  if (s === "failed" || s === "error") {
    return <AlertCircle className="h-3 w-3 text-rose-400" />
  }
  return <Sparkles className="h-3 w-3 text-muted-foreground" />
}

export function RoutinesInsightsView({
  routines,
  onSelect,
}: RoutinesInsightsViewProps) {
  const stats = useMemo(() => {
    const totalRuns = routines.reduce((sum, r) => sum + (r.invocation_count ?? 0), 0)
    const everRun = routines.filter((r) => (r.invocation_count ?? 0) > 0)
    const succeeded = routines.filter(
      (r) =>
        r.last_invocation_status?.toLowerCase() === "succeeded" ||
        r.last_invocation_status?.toLowerCase() === "success",
    ).length
    const failed = routines.filter(
      (r) =>
        r.last_invocation_status?.toLowerCase() === "failed" ||
        r.last_invocation_status?.toLowerCase() === "error",
    ).length
    const passRate =
      everRun.length > 0 ? Math.round((succeeded / everRun.length) * 100) : null
    const top = [...routines]
      .filter((r) => (r.invocation_count ?? 0) > 0)
      .sort((a, b) => (b.invocation_count ?? 0) - (a.invocation_count ?? 0))
      .slice(0, 5)
    // Sort by last_invoked_at desc so the panel shows the freshest
    // failures, not the first 5 in workspace iteration order. Routines
    // missing a timestamp sort to the bottom.
    const recentFailures = routines
      .filter(
        (r) =>
          r.last_invocation_status?.toLowerCase() === "failed" ||
          r.last_invocation_status?.toLowerCase() === "error",
      )
      .sort((a, b) => {
        const ta = a.last_invoked_at ? Date.parse(a.last_invoked_at) : 0
        const tb = b.last_invoked_at ? Date.parse(b.last_invoked_at) : 0
        return tb - ta
      })
      .slice(0, 5)
    return { totalRuns, everRun, succeeded, failed, passRate, top, recentFailures }
  }, [routines])

  return (
    <div className="h-full overflow-auto p-6">
      <div className="mb-4">
        <div className="text-base font-semibold">Insights</div>
        <div className="text-xs text-muted-foreground">
          Health snapshot for the {routines.length} routine
          {routines.length === 1 ? "" : "s"} in this workspace
        </div>
      </div>

      <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
        <StatCard
          icon={<TrendingUp className="h-4 w-4 text-blue-400" />}
          label="Total runs"
          value={stats.totalRuns.toLocaleString()}
          sub={`${stats.everRun.length} of ${routines.length} routines invoked`}
        />
        <StatCard
          icon={<CheckCircle2 className="h-4 w-4 text-emerald-400" />}
          label="Last-run success"
          value={stats.passRate !== null ? `${stats.passRate}%` : "—"}
          sub={`${stats.succeeded} succeeded, ${stats.failed} failed`}
        />
        <StatCard
          icon={<AlertCircle className="h-4 w-4 text-rose-400" />}
          label="Recent failures"
          value={stats.recentFailures.length.toString()}
          sub={
            stats.recentFailures.length === 0
              ? "Nothing failing right now"
              : "Click a row below to investigate"
          }
        />
      </div>

      <div className="mt-6 grid grid-cols-1 gap-4 md:grid-cols-2">
        <Panel title="Top routines by usage">
          {stats.top.length === 0 ? (
            <div className="px-3 py-6 text-center text-xs text-muted-foreground">
              No invocations yet — once routines start running, the most-used
              ones will surface here.
            </div>
          ) : (
            <ul className="divide-y divide-white/[0.04]">
              {stats.top.map((r) => (
                <RoutineRow
                  key={r.id}
                  onActivate={() => onSelect(r.slug)}
                  ariaLabel={`Open routine ${r.name}`}
                >
                  {statusBadge(r.last_invocation_status)}
                  <span className="flex-1 truncate text-xs">{r.name}</span>
                  <span className="text-[11px] tabular-nums text-muted-foreground">
                    {r.invocation_count} runs
                  </span>
                </RoutineRow>
              ))}
            </ul>
          )}
        </Panel>

        <Panel title="Recent failures">
          {stats.recentFailures.length === 0 ? (
            <div className="px-3 py-6 text-center text-xs text-muted-foreground">
              No failures recorded — last run of every routine is either
              clean or hasn&apos;t run yet.
            </div>
          ) : (
            <ul className="divide-y divide-white/[0.04]">
              {stats.recentFailures.map((r) => (
                <RoutineRow
                  key={r.id}
                  onActivate={() => onSelect(r.slug)}
                  ariaLabel={`Open routine ${r.name}`}
                >
                  <AlertCircle className="h-3 w-3 text-rose-400" />
                  <span className="flex-1 truncate text-xs">{r.name}</span>
                  <span className="text-[11px] tabular-nums text-muted-foreground">
                    {r.last_invoked_at ? new Date(r.last_invoked_at).toLocaleDateString() : "—"}
                  </span>
                </RoutineRow>
              ))}
            </ul>
          )}
        </Panel>
      </div>
    </div>
  )
}

interface StatCardProps {
  icon: React.ReactNode
  label: string
  value: string
  sub: string
}

function StatCard({ icon, label, value, sub }: StatCardProps) {
  return (
    <div className={cn("rounded-md border border-white/[0.06] bg-card/30 p-4")}>
      <div className="flex items-center gap-2 text-[11px] uppercase tracking-wider text-muted-foreground">
        {icon}
        <span>{label}</span>
      </div>
      <div className="mt-1 text-2xl font-semibold tabular-nums">{value}</div>
      <div className="mt-1 text-[11px] text-muted-foreground">{sub}</div>
    </div>
  )
}

interface PanelProps {
  title: string
  children: React.ReactNode
}

function Panel({ title, children }: PanelProps) {
  return (
    <div className="overflow-hidden rounded-md border border-white/[0.06] bg-card/30">
      <div className="border-b border-white/[0.06] px-3 py-2 text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
        {title}
      </div>
      {children}
    </div>
  )
}

interface RoutineRowProps {
  onActivate: () => void
  ariaLabel: string
  children: React.ReactNode
}

// RoutineRow wraps a list item so the row is keyboard-activatable
// (Enter / Space) in addition to clickable. Both insights panels reuse
// it; the children compose the row content while the wrapper handles
// role / tabIndex / onKeyDown wiring.
function RoutineRow({ onActivate, ariaLabel, children }: RoutineRowProps) {
  return (
    <li
      role="button"
      tabIndex={0}
      aria-label={ariaLabel}
      onClick={onActivate}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault()
          onActivate()
        }
      }}
      className="flex cursor-pointer items-center gap-2 px-3 py-2 hover:bg-card/40 focus:bg-card/40 focus:outline-none focus-visible:ring-1 focus-visible:ring-blue-400"
    >
      {children}
    </li>
  )
}
