"use client"

import { useMemo } from "react"
import { Calendar, Pause, Play, Webhook, Clock, CheckCircle2, AlertTriangle, XCircle } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import type { Pipeline } from "@/hooks/use-pipelines"
import { Card, EmptyState, Pill } from "./_shared"

// RoutinesSchedulesView — workspace-wide cron schedule dashboard.
// Top: KPI strip (active / paused / firing-soon / last-failed).
// Middle: "Firing next" upcoming list (sorted by next_run_at).
// Bottom: Full schedules table inside a Card.

interface RoutinesSchedulesViewProps {
  workspaceId: string
  routines: Pipeline[]
  onSelect: (slug: string) => void
}

function statusTone(status: string | undefined): "emerald" | "rose" | "blue" | "default" {
  switch (status?.toLowerCase()) {
    case "succeeded":
    case "success":
    case "completed":
      return "emerald"
    case "failed":
    case "error":
      return "rose"
    case "running":
      return "blue"
    default:
      return "default"
  }
}

function relativeTime(iso?: string): string {
  if (!iso) return "—"
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return "—"
  const diff = Date.now() - d.getTime()
  if (Math.abs(diff) < 60_000) return "just now"
  const fwd = diff < 0
  const abs = Math.abs(diff)
  const mins = Math.round(abs / 60_000)
  if (mins < 60) return fwd ? `in ${mins}m` : `${mins}m ago`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return fwd ? `in ${hrs}h` : `${hrs}h ago`
  const days = Math.round(hrs / 24)
  return fwd ? `in ${days}d` : `${days}d ago`
}

export function RoutinesSchedulesView({
  workspaceId,
  routines,
  onSelect,
}: RoutinesSchedulesViewProps) {
  const { schedules, loading, error } = usePipelineSchedules(workspaceId)

  const slugByPipelineId = useMemo(() => new Map(routines.map((r) => [r.id, r.slug])), [routines])

  const stats = useMemo(() => {
    const active = schedules.filter((s) => s.enabled).length
    const paused = schedules.length - active
    const lastFailed = schedules.filter((s) => {
      const t = s.last_status?.toLowerCase()
      return t === "failed" || t === "error"
    }).length
    const now = Date.now()
    const hourMs = 60 * 60 * 1000
    const firingSoon = schedules.filter((s) => {
      if (!s.enabled || !s.next_run_at) return false
      const t = new Date(s.next_run_at).getTime()
      return Number.isFinite(t) && t - now < 6 * hourMs
    }).length
    // Upcoming firings, soonest first, top 6
    const upcoming = [...schedules]
      .filter((s) => s.enabled && s.next_run_at)
      .sort((a, b) => new Date(a.next_run_at!).getTime() - new Date(b.next_run_at!).getTime())
      .slice(0, 6)
    return { active, paused, lastFailed, firingSoon, upcoming }
  }, [schedules])

  if (loading) {
    return (
      <div className="space-y-3 p-6">
        <Skeleton className="h-24 w-full rounded-xl" />
        <Skeleton className="h-40 w-full rounded-xl" />
        <Skeleton className="h-64 w-full rounded-xl" />
      </div>
    )
  }

  if (error) {
    return (
      <div className="p-6">
        <Card tone="amber">
          <div className="px-4 py-3 text-sm text-amber-300">Schedules unavailable: {error}</div>
        </Card>
      </div>
    )
  }

  if (schedules.length === 0) {
    return (
      <div className="p-6">
        <Card>
          <EmptyState
            icon={Calendar}
            title="No schedules yet"
            description='Schedules fire a saved routine on a cron expression — perfect for recurring jobs like "every weekday at 8am, summarize new tickets." Open a routine in the List tab and use its Schedules sub-tab to add one.'
          />
        </Card>
      </div>
    )
  }

  return (
    <div className="h-full overflow-auto">
      <div className="space-y-4 p-6">
        {/* Header */}
        <div>
          <h2 className="text-base font-semibold tracking-tight">Schedules</h2>
          <p className="text-[12px] text-muted-foreground">
            <span className="tabular-nums text-foreground/85">{schedules.length}</span> cron triggers in this workspace · open a routine to add or edit
          </p>
        </div>

        {/* KPI strip */}
        <section className="grid grid-cols-2 gap-3 md:grid-cols-4">
          <KpiTile
            label="Active"
            value={stats.active.toString()}
            sub={stats.paused > 0 ? `${stats.paused} paused` : "all enabled"}
            tone="emerald"
            Icon={Play}
          />
          <KpiTile
            label="Firing soon"
            value={stats.firingSoon.toString()}
            sub="within the next 6 hours"
            tone="blue"
            Icon={Clock}
          />
          <KpiTile
            label="Last failed"
            value={stats.lastFailed.toString()}
            sub={stats.lastFailed === 0 ? "all healthy" : "schedules with failed last run"}
            tone={stats.lastFailed > 0 ? "rose" : "default"}
            Icon={AlertTriangle}
          />
          <KpiTile
            label="Webhook triggers"
            value="—"
            sub="configured per-routine in detail"
            tone="violet"
            Icon={Webhook}
          />
        </section>

        {/* Upcoming firings */}
        {stats.upcoming.length > 0 && (
          <Card title="Firing next" icon={Clock} subtitle={`${stats.upcoming.length} upcoming`}>
            <ul className="divide-y divide-border/40">
              {stats.upcoming.map((s) => {
                const slug = s.target_pipeline_slug ?? slugByPipelineId.get(s.target_pipeline_id) ?? ""
                return (
                  <li key={s.id}>
                    <button
                      onClick={() => slug && onSelect(slug)}
                      disabled={!slug}
                      className={cn(
                        "grid w-full grid-cols-[auto_1fr_auto] items-center gap-3 px-4 py-3 text-left transition-colors",
                        slug
                          ? "hover:bg-white/[0.025]"
                          : "cursor-default opacity-60",
                      )}
                    >
                      <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-blue-500/20 text-blue-400">
                        <Calendar className="h-4 w-4" />
                      </div>
                      <div className="min-w-0">
                        <div className="truncate text-sm font-medium">{s.name}</div>
                        <div className="font-mono text-[11px] text-muted-foreground">
                          {s.cron_expr} · {s.timezone}
                          {slug && <span className="ml-2 text-foreground/70">→ {slug}</span>}
                        </div>
                      </div>
                      <div className="text-right">
                        <div className="text-sm font-medium text-blue-400">{relativeTime(s.next_run_at)}</div>
                        <div className="text-[10px] text-muted-foreground">{new Date(s.next_run_at!).toLocaleString()}</div>
                      </div>
                    </button>
                  </li>
                )
              })}
            </ul>
          </Card>
        )}

        {/* Full table */}
        <Card title="All schedules" subtitle={`${schedules.length} total`}>
          <div className="overflow-x-auto">
            <table className="w-full text-[13px]">
              <thead className="border-b border-border/40 bg-card/40 text-[10px] uppercase tracking-wider text-muted-foreground">
                <tr className="text-left">
                  <th className="px-4 py-2.5 font-semibold">Name</th>
                  <th className="px-3 py-2.5 font-semibold">Routine</th>
                  <th className="px-3 py-2.5 font-semibold">Cron</th>
                  <th className="px-3 py-2.5 font-semibold">Last run</th>
                  <th className="px-3 py-2.5 font-semibold">Next run</th>
                  <th className="px-3 py-2.5 text-right font-semibold">Status</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border/40">
                {schedules.map((s) => {
                  const slug = s.target_pipeline_slug ?? slugByPipelineId.get(s.target_pipeline_id) ?? ""
                  const interactive = Boolean(slug)
                  const activate = () => slug && onSelect(slug)
                  return (
                    <tr
                      key={s.id}
                      {...(interactive
                        ? {
                            role: "button" as const,
                            tabIndex: 0,
                            "aria-label": `Open routine ${slug}`,
                            onClick: activate,
                            onKeyDown: (e: React.KeyboardEvent<HTMLTableRowElement>) => {
                              if (e.key === "Enter" || e.key === " ") {
                                e.preventDefault()
                                activate()
                              }
                            },
                          }
                        : {})}
                      className={cn(
                        "transition-colors",
                        interactive ? "cursor-pointer row-hover" : "opacity-70",
                      )}
                    >
                      <td className="px-4 py-3 text-sm font-medium">
                        <div className="flex items-center gap-2">
                          {s.enabled ? (
                            <Play className="h-3.5 w-3.5 text-emerald-400" aria-label="enabled" />
                          ) : (
                            <Pause className="h-3.5 w-3.5 text-muted-foreground" aria-label="paused" />
                          )}
                          <span>{s.name}</span>
                        </div>
                      </td>
                      <td className="px-3 py-3 font-mono text-[12px] text-muted-foreground">{slug || "—"}</td>
                      <td className="px-3 py-3 font-mono text-[12px] text-muted-foreground">{s.cron_expr}</td>
                      <td className="px-3 py-3 text-[12px] text-muted-foreground">{relativeTime(s.last_run_at)}</td>
                      <td className="px-3 py-3 text-[12px] text-muted-foreground">{relativeTime(s.next_run_at)}</td>
                      <td className="px-3 py-3 text-right">
                        {s.last_status ? (
                          <Pill tone={statusTone(s.last_status)} className="capitalize">
                            {s.last_status}
                          </Pill>
                        ) : (
                          <span className="text-[11px] text-muted-foreground/60">—</span>
                        )}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </Card>

        <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <Webhook className="h-3.5 w-3.5" />
          Webhook triggers are configured per-routine in its detail panel.
        </div>
      </div>
    </div>
  )
}

/* ----------------------------------------------------------------- *
 *  Internal KPI tile — same visual as List view's tiles.            *
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
  Icon: typeof Calendar
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

// Re-export reference so the parent module's import doesn't go stale.
void CheckCircle2
void XCircle
