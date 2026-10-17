"use client"

// The top of the Routines overview (Harbor 2 RT-OV, routines.md R02): four
// figures — Needs you, Running now, Runs · 7 days, Spend this month — then
// Needs you (a plain approval is answered in place by a Manager or above),
// Running now (Open run, Stop for Owner and Admin) and Next 24 hours.
//
// Checkup across routines and the Health table of v67 are not here: the
// checkup has no endpoint and the per-routine health aggregate does not
// exist (routines.md ODCH-RT-06/07).

import * as React from "react"
import Link from "next/link"
import { CalendarClock, Hand, Play, Square } from "lucide-react"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { isAwaitingApproval } from "@/hooks/use-active-routine-runs"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"
import type { BudgetSummary } from "@/hooks/use-routine-budget"
import type { PendingWaitpoint } from "@/hooks/use-pending-approval"
import { didNotRun, isWaitingForCapacity, type PendingStart } from "@/lib/routine-pending-starts"
import { canApproveRoutine, canKillRoutine } from "@/lib/routine-governance"
import { formatAgo, formatUntil } from "@/lib/routine-run-presentation"
import { next24Hours, overviewNeeds, type OverviewNeed } from "@/lib/routines-overview-board"
import { RoutineGlyph } from "./routine-glyph"

const TONE: Record<OverviewNeed["kind"], { tone: "warn" | "purple" | "danger" | "muted"; label: string }> = {
  decision: { tone: "warn", label: "Waiting" },
  proposed: { tone: "purple", label: "Proposed" },
  failed: { tone: "danger", label: "Failed" },
  breaker: { tone: "danger", label: "Off" },
  off: { tone: "muted", label: "Off" },
  "not-run": { tone: "danger", label: "Did not run" },
}

export interface RoutinesOverviewBoardProps {
  routines: Pipeline[]
  activeRuns: PipelineRun[]
  waitpoints: PendingWaitpoint[]
  schedules: PipelineSchedule[]
  pendingStarts: PendingStart[]
  /** Runs of the last 7 days and the share that completed (routines-dashboard outcomeKpis). */
  kpis: { total: number; successPct: number | null }
  /** Set when the run feed stops inside the window (windowCoverage). */
  coveredSince: string | null
  spend: BudgetSummary | null
  role: string | null | undefined
  now?: Date
  /** Approve or reject the decision a run waits on; resolves true once accepted. */
  onDecide: (runId: string, approved: boolean) => Promise<boolean>
  onStop: (runId: string) => Promise<boolean>
  onOpenRun: (slug: string, runId: string) => void
  onSelect: (slug: string) => void
}

const money = (n: number) => `$${n.toFixed(2)}`
const time = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })

export function RoutinesOverviewBoard(p: RoutinesOverviewBoardProps) {
  const now = p.now ?? new Date()
  const bySlug = new Map(p.routines.map((r) => [r.slug, r]))
  const weekAgo = now.getTime() - 7 * 86_400_000
  const needs = overviewNeeds({
    routines: p.routines,
    activeRuns: p.activeRuns,
    waitpoints: p.waitpoints,
    schedules: p.schedules,
    notRun: p.pendingStarts.filter((s) => didNotRun(s) && Date.parse(s.fire_at) >= weekAgo),
  })
  const capacityWaits = p.pendingStarts.filter(isWaitingForCapacity).length
  const live = p.activeRuns.filter((r) => !isAwaitingApproval(r.status))
  const next = next24Hours({ routines: p.routines, schedules: p.schedules, starts: p.pendingStarts, now })
  const mayDecide = canApproveRoutine(p.role)
  const mayStop = canKillRoutine(p.role)
  const [deciding, setDeciding] = React.useState<string | null>(null)
  const [stopping, setStopping] = React.useState<PipelineRun | null>(null)
  const month = p.spend?.month ? new Date(`${p.spend.month}-01T12:00:00Z`).toLocaleString("en-GB", { month: "long" }) : "this month"

  const glyph = (slug: string) => <RoutineGlyph routine={bySlug.get(slug) ?? { slug }} size="sm" />

  return (
    <div className="flex flex-col gap-3" data-testid="routines-overview-board">
      <ul aria-label="Routine figures" className="grid grid-cols-2 gap-2 lg:grid-cols-4">
        <Kpi label="Needs you" value={String(needs.length)} />
        <Kpi label="Running now" value={String(live.length)} />
        <Kpi
          label="Runs · 7 days"
          value={String(p.kpis.total)}
          note={[p.kpis.successPct != null ? `${p.kpis.successPct} % completed` : null, p.coveredSince ? `newest runs since ${formatAgo(p.coveredSince, now.getTime())}` : null].filter(Boolean).join(" · ") || undefined}
        />
        <Kpi
          label={`Spend · ${month}`}
          value={p.spend ? money(p.spend.total_spent_usd) : "—"}
          note={p.spend ? (p.spend.total_budget_usd > 0 ? `of ${money(p.spend.total_budget_usd)} budget` : "no budget set") : undefined}
        />
      </ul>

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        <section aria-label="Needs you" className="lg:col-span-2">
          <DashboardCard title="Needs you" icon={Hand} hint={needs.length ? String(needs.length) : "nothing"} className="h-full">
            {needs.length === 0 ? (
              <p className="px-2 py-3 text-label text-muted-foreground">No decision is waiting, nothing is up for review and every routine finished its last run.</p>
            ) : (
              <ul className="flex flex-col">
                {needs.map((n) => (
                  <li key={n.key} className="flex flex-wrap items-center gap-2.5 border-b border-hairline px-2 py-2 last:border-0 coarse:min-h-[44px]">
                    {glyph(n.slug)}
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-body font-medium text-foreground">{n.title}</span>
                      <span className="block truncate text-label text-muted-foreground">
                        {n.detail}
                        {n.expiresAt ? ` · times out in ${formatUntil(n.expiresAt, now.getTime())}` : ""}
                      </span>
                    </span>
                    <StatusPill tone={TONE[n.kind].tone} label={TONE[n.kind].label} />
                    {n.kind === "decision" && n.runId ? (
                      <>
                        <Button type="button" size="sm" variant="outline" className="coarse:h-[44px]" onClick={() => p.onOpenRun(n.slug, n.runId!)}>
                          Open run
                        </Button>
                        {n.approvable && mayDecide && (
                          <Button
                            type="button"
                            size="sm"
                            className="coarse:h-[44px]"
                            disabled={deciding === n.runId}
                            onClick={async () => {
                              setDeciding(n.runId!)
                              try {
                                await p.onDecide(n.runId!, true)
                              } finally {
                                setDeciding(null)
                              }
                            }}
                          >
                            {deciding === n.runId ? "Approving…" : "Approve"}
                          </Button>
                        )}
                      </>
                    ) : (
                      <Button type="button" size="sm" variant="outline" className="coarse:h-[44px]" onClick={() => p.onSelect(n.slug)}>
                        {n.kind === "proposed" ? "Review" : "Open"}
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </DashboardCard>
        </section>

        <div className="flex min-w-0 flex-col gap-3">
          <section aria-label="Running now">
            <DashboardCard title="Running now" icon={Play} hint={live.length ? `${live.length} running` : "none"}>
              {live.length === 0 ? (
                <p className="px-2 py-3 text-label text-muted-foreground">Nothing is running right now.</p>
              ) : (
                <ul className="flex flex-col">
                  {live.slice(0, 5).map((r) => (
                    <li key={r.id} className="flex flex-wrap items-center gap-2 border-b border-hairline px-2 py-2 last:border-0">
                      {glyph(r.pipeline_slug)}
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-body font-medium text-foreground">{r.pipeline_name || bySlug.get(r.pipeline_slug)?.name || r.pipeline_slug}</span>
                        <span className="block truncate text-label text-muted-foreground">
                          {r.status === "queued" ? "Waiting in line" : `started ${formatAgo(r.started_at, now.getTime())}`}
                          {r.current_step_id ? ` · ${r.current_step_id.replaceAll("_", " ")}` : ""}
                        </span>
                      </span>
                      <Button type="button" size="sm" variant="outline" className="coarse:h-[44px]" onClick={() => p.onOpenRun(r.pipeline_slug, r.id)}>
                        Open run
                      </Button>
                      {mayStop && (
                        <Button type="button" size="sm" variant="ghost" className="gap-1 coarse:h-[44px]" onClick={() => setStopping(r)}>
                          <Square className="h-3 w-3" aria-hidden />
                          Stop
                        </Button>
                      )}
                    </li>
                  ))}
                </ul>
              )}
              {capacityWaits > 0 && (
                <p className="mt-2 rounded-lg bg-warn/10 px-3 py-2 text-label text-warn">
                  {capacityWaits} {capacityWaits === 1 ? "start waits" : "starts wait"} for a free slot (one at a time)
                </p>
              )}
            </DashboardCard>
          </section>

          <section aria-label="Next 24 hours">
            <DashboardCard
              title="Next 24 hours"
              icon={CalendarClock}
              hint={String(next.length)}
              action={
                <Link href="/routines?tab=calendar" className="text-primary-hover hover:underline">
                  Open calendar
                </Link>
              }
            >
              {next.length === 0 ? (
                <p className="px-2 py-3 text-label text-muted-foreground">Nothing is planned for the next 24 hours.</p>
              ) : (
                <ul className="flex flex-col">
                  {next.map((s) => (
                    <li key={s.key} className="flex items-center gap-3 border-b border-hairline px-2 py-1.5 last:border-0">
                      <span className="w-12 shrink-0 font-mono text-label tabular-nums text-muted-foreground">{time(s.at)}</span>
                      <button type="button" className="min-w-0 flex-1 truncate text-left text-body text-foreground hover:underline" onClick={() => p.onSelect(s.slug)}>
                        {s.name}
                      </button>
                      <span className="shrink-0 text-label text-muted-foreground">{s.kind === "schedule" ? "Schedule" : "One-time"}</span>
                    </li>
                  ))}
                </ul>
              )}
            </DashboardCard>
          </section>
        </div>
      </div>

      <ConfirmDialog
        open={stopping !== null}
        onOpenChange={(open) => !open && setStopping(null)}
        title="Stop this run?"
        description="The current step is stopped. Finished steps and what they produced stay. The run ends as Stopped."
        cancelLabel="Keep running"
        confirmLabel="Stop run"
        destructive
        onConfirm={async () => {
          if (stopping && !(await p.onStop(stopping.id))) throw new Error("cancel refused")
        }}
      />
    </div>
  )
}

function Kpi({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <li className="flex flex-col gap-0.5 rounded-lg border border-hairline bg-card px-3 py-2">
      <span className="text-label text-muted-foreground">{label}</span>
      <span className="font-mono text-title font-semibold tabular-nums text-foreground">{value}</span>
      {note && <span className="text-label text-muted-foreground-soft">{note}</span>}
    </li>
  )
}
