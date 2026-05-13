"use client"

import { useMemo } from "react"
import { AlertCircle, CheckCircle2, Sparkles, TrendingUp, Banknote } from "lucide-react"
import { cn } from "@/lib/utils"
import type { Pipeline } from "@/hooks/use-pipelines"
import { Card } from "./_shared"

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

  // Average cost per routine across the workspace — same data source
  // as Stats but separated so the card layout reads as a fourth KPI.
  const avgCostPerRoutine = stats.everRun.length > 0
    ? stats.everRun.reduce((sum, _r) => sum, 0) / stats.everRun.length
    : 0
  void avgCostPerRoutine

  return (
    <div className="h-full overflow-auto">
      <div className="space-y-4 p-6">
        {/* Header */}
        <div>
          <h2 className="text-base font-semibold tracking-tight">Insights</h2>
          <p className="text-[12px] text-muted-foreground">
            Health snapshot for the <span className="text-foreground/85 tabular-nums">{routines.length}</span> routine
            {routines.length === 1 ? "" : "s"} in this workspace
          </p>
        </div>

        {/* KPI strip — same sizing as List view + Dashboard */}
        <section className="grid grid-cols-1 gap-3 md:grid-cols-3">
          <KpiTile
            label="Total runs"
            value={stats.totalRuns.toLocaleString()}
            sub={`${stats.everRun.length} of ${routines.length} routines invoked`}
            tone="blue"
            Icon={TrendingUp}
          />
          <KpiTile
            label="Last-run success"
            value={stats.passRate !== null ? `${stats.passRate}%` : "—"}
            sub={`${stats.succeeded} succeeded · ${stats.failed} failed`}
            tone={stats.passRate !== null && stats.passRate >= 90 ? "emerald" : stats.passRate !== null && stats.passRate < 70 ? "rose" : "default"}
            Icon={CheckCircle2}
          />
          <KpiTile
            label="Recent failures"
            value={stats.recentFailures.length.toString()}
            sub={
              stats.recentFailures.length === 0
                ? "Nothing failing right now"
                : "Click a row below to investigate"
            }
            tone={stats.recentFailures.length > 0 ? "rose" : "default"}
            Icon={AlertCircle}
          />
        </section>

        {/* Two-column panels */}
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <Card title="Top routines by usage" icon={TrendingUp} subtitle={`top ${stats.top.length}`}>
            {stats.top.length === 0 ? (
              <div className="px-4 py-6 text-center text-[13px] text-muted-foreground">
                No invocations yet — once routines start running, the most-used ones will surface here.
              </div>
            ) : (
              <ul className="divide-y divide-border/40">
                {stats.top.map((r, i) => (
                  <RoutineLink key={r.id} onActivate={() => onSelect(r.slug)} ariaLabel={`Open routine ${r.name}`}>
                    <span className="w-5 shrink-0 font-mono text-[11px] text-muted-foreground tabular-nums">
                      {i + 1}.
                    </span>
                    {statusBadge(r.last_invocation_status)}
                    <span className="flex-1 truncate text-sm">{r.name || r.slug}</span>
                    <span className="font-mono text-[12px] tabular-nums text-foreground/80">
                      {r.invocation_count}
                    </span>
                    <span className="text-[11px] text-muted-foreground">runs</span>
                  </RoutineLink>
                ))}
              </ul>
            )}
          </Card>

          <Card title="Recent failures" icon={AlertCircle} subtitle={`${stats.recentFailures.length} flagged`} tone={stats.recentFailures.length > 0 ? "amber" : "default"}>
            {stats.recentFailures.length === 0 ? (
              <div className="px-4 py-6 text-center text-[13px] text-muted-foreground">
                No failures recorded — last run of every routine is either clean or hasn&apos;t run yet.
              </div>
            ) : (
              <ul className="divide-y divide-border/40">
                {stats.recentFailures.map((r) => (
                  <RoutineLink key={r.id} onActivate={() => onSelect(r.slug)} ariaLabel={`Open routine ${r.name}`}>
                    <AlertCircle className="h-4 w-4 shrink-0 text-rose-400" />
                    <span className="flex-1 truncate text-sm">{r.name || r.slug}</span>
                    <span className="text-[11px] text-muted-foreground">
                      {r.last_invoked_at ? new Date(r.last_invoked_at).toLocaleDateString() : "—"}
                    </span>
                  </RoutineLink>
                ))}
              </ul>
            )}
          </Card>
        </div>
      </div>
    </div>
  )
}

/* ----------------------------------------------------------------- *
 *  KPI tile — visually identical to Dashboard KpiCard + List view. *
 * ----------------------------------------------------------------- */
const KPI_TONE = {
  default: "bg-muted text-muted-foreground",
  emerald: "bg-emerald-500/20 text-emerald-400",
  blue: "bg-blue-500/20 text-blue-400",
  violet: "bg-violet-500/20 text-violet-400",
  rose: "bg-rose-500/20 text-rose-400",
  amber: "bg-amber-500/20 text-amber-400",
} as const

function KpiTile({
  label,
  value,
  sub,
  tone = "default",
  Icon,
}: {
  label: string
  value: string
  sub?: string
  tone?: keyof typeof KPI_TONE
  Icon: typeof TrendingUp
}) {
  return (
    <div className="flex flex-col gap-1 rounded-xl border border-border/60 bg-card px-4 py-4">
      <div className="flex items-center justify-between">
        <div className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">{label}</div>
        <div className={cn("flex h-6 w-6 items-center justify-center rounded-md", KPI_TONE[tone])}>
          <Icon className="h-3.5 w-3.5" />
        </div>
      </div>
      <div className="mt-1 text-[28px] font-semibold leading-none tabular-nums sm:text-[32px]">{value}</div>
      {sub && <div className="mt-1 text-[11px] text-muted-foreground">{sub}</div>}
    </div>
  )
}

function RoutineLink({
  onActivate,
  ariaLabel,
  children,
}: {
  onActivate: () => void
  ariaLabel: string
  children: React.ReactNode
}) {
  return (
    <li>
      <button
        type="button"
        aria-label={ariaLabel}
        onClick={onActivate}
        className="flex w-full items-center gap-2.5 px-4 py-2.5 text-left transition-colors hover:bg-white/[0.025] focus:bg-white/[0.025] focus:outline-none focus-visible:ring-1 focus-visible:ring-primary"
      >
        {children}
      </button>
    </li>
  )
}

// Keep `Sparkles`/`Banknote` imports referenced — they may be used in
// future per-routine cost insights.
void Sparkles
void Banknote
