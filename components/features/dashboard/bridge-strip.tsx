"use client"

import * as React from "react"
import Link from "next/link"
import { motion, useReducedMotion } from "motion/react"
import { ArrowRight, Radio } from "lucide-react"

import type { AgentSummary, CrewSummary, DashboardWindow } from "@/app/(dashboard)/dashboard-types"
import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"
import { AnimatedNumber } from "@/components/ui/animated-number"
import { crewColor, formatCost } from "@/app/(dashboard)/dashboard-helpers"
import { formatDuration, formatRelativeTime } from "@/lib/time"
import { cn } from "@/lib/utils"
import { entityHref } from "@/lib/entity-links"
import type { OutcomeKpiData } from "./dashboard-overview"

export interface BridgeData {
  workingAgents: number
  idleAgents: number
  errorAgents: number
  crews: CrewSummary[]
  /** Metered spend for the window, or null when paymaster has no ledger rows
   *  (not configured, or a flat-rate subscription that meters nothing). */
  spendUsd: number | null
  runsTotal: number
  successPct: number | null
  failed: number
  p95Ms: number
  attentionCount: number
  attentionKnown: boolean
  nextSchedule: PipelineSchedule | null
}

/**
 * The bridge: one row that answers "how is the ship doing" before the eye
 * goes anywhere else. Pure so the page can compute it once and the test can
 * pin what each cell says.
 */
export function deriveBridge({
  agents,
  crews,
  spendRows,
  kpis,
  attentionCount,
  attentionKnown,
  schedules,
  now = Date.now(),
}: {
  agents: AgentSummary[]
  crews: CrewSummary[]
  spendRows: Array<{ cost_usd: number }> | null
  kpis: OutcomeKpiData
  attentionCount: number
  attentionKnown: boolean
  schedules: PipelineSchedule[]
  now?: number
}): BridgeData {
  const working = agents.filter((a) => a.status === "RUNNING").length
  const errors = agents.filter((a) => a.status === "ERROR").length
  const spend = spendRows && spendRows.length > 0 ? spendRows.reduce((sum, r) => sum + (r.cost_usd || 0), 0) : null
  const next = schedules
    .filter((s) => s.enabled && s.next_run_at && new Date(s.next_run_at).getTime() > now)
    .sort((a, b) => new Date(a.next_run_at!).getTime() - new Date(b.next_run_at!).getTime())[0] ?? null
  return {
    workingAgents: working,
    idleAgents: Math.max(0, agents.length - working - errors),
    errorAgents: errors,
    crews,
    spendUsd: spend,
    runsTotal: kpis.successTotal,
    successPct: kpis.successPct,
    failed: Math.max(0, kpis.successTotal - kpis.successOk),
    p95Ms: kpis.p95Ms,
    attentionCount,
    attentionKnown,
    nextSchedule: next,
  }
}

function Cell({ label, children, className }: { label: string; children: React.ReactNode; className?: string }) {
  return (
    <div className={cn("flex min-w-0 flex-col gap-1", className)}>
      <span className="text-label font-medium text-muted-foreground">{label}</span>
      {children}
    </div>
  )
}

export function BridgeStrip({
  data,
  window,
  realtimeStatus,
}: {
  data: BridgeData
  window: DashboardWindow
  realtimeStatus: "connected" | "connecting" | string
}) {
  const reduce = useReducedMotion()
  const live = realtimeStatus === "connected"
  return (
    <div
      data-testid="dashboard-bridge"
      className="relative overflow-hidden rounded-xl border border-border/60 bg-card px-5 py-4"
    >
      {/* One quiet highlight along the top edge — the surface is lit, not
          decorated. Same gesture the onboarding pane uses. */}
      <div className="pointer-events-none absolute inset-x-0 top-0 h-px bg-gradient-to-r from-transparent via-primary/40 to-transparent" />
      <div className="grid grid-cols-2 gap-x-6 gap-y-4 md:grid-cols-3 xl:grid-cols-5">
        <Cell label="Fleet">
          <span className="flex items-center gap-2 text-[15px] font-semibold tracking-tight">
            <span className={cn("relative flex h-2.5 w-2.5", !live && "opacity-60")} aria-hidden>
              {live && !reduce && (
                <motion.span
                  className="absolute inset-0 rounded-full bg-success"
                  animate={{ opacity: [0.5, 0], scale: [1, 2.2] }}
                  transition={{ duration: 1.65, repeat: Infinity, ease: "easeOut" }}
                />
              )}
              <span className={cn("relative m-auto h-2 w-2 rounded-full", live ? "bg-success" : "bg-warn")} />
            </span>
            {data.workingAgents > 0 ? (
              <><MetricNumber value={data.workingAgents} /> working · {data.idleAgents} idle</>
            ) : (
              <>{data.idleAgents + data.errorAgents} agents ready</>
            )}
            {data.errorAgents > 0 && <span className="text-label font-medium text-destructive">· {data.errorAgents} error</span>}
          </span>
          <span className="flex items-center gap-1.5 text-label text-muted-foreground">
            <span className="flex items-center gap-1">
              {data.crews.slice(0, 6).map((crew) => (
                <span key={crew.id} className="h-2 w-2 rounded-full" style={{ backgroundColor: crewColor(crew.color) }} aria-hidden />
              ))}
            </span>
            <span className="truncate">{data.crews.map((c) => c.name).slice(0, 3).join(" · ")}{data.crews.length > 3 ? ` · +${data.crews.length - 3}` : ""}</span>
          </span>
        </Cell>

        <Cell label={`Spend · ${window}`}>
          {data.spendUsd == null ? (
            <>
              <span className="text-[15px] font-semibold tracking-tight text-muted-foreground">not metered</span>
              <Link href="/paymaster" className="text-label text-primary-hover hover:underline">Paymaster →</Link>
            </>
          ) : (
            <>
              <span className="text-[22px] font-semibold leading-none tracking-tight tabular-nums">{formatCost(data.spendUsd)}</span>
              <span className="text-label text-muted-foreground">metered ledger · <Link href="/paymaster" className="text-primary-hover hover:underline">breakdown</Link></span>
            </>
          )}
        </Cell>

        <Cell label={`Runs · ${window}`}>
          <span className="flex items-baseline gap-2">
            <span className="text-[22px] font-semibold leading-none tracking-tight tabular-nums"><MetricNumber value={data.runsTotal} /></span>
            {data.successPct != null && (
              <span className={cn("rounded-full border px-2 py-0.5 text-micro font-semibold", data.successPct >= 90 ? "border-success/25 bg-success/10 text-success" : data.successPct >= 70 ? "border-warn/25 bg-warn/10 text-warn" : "border-destructive/25 bg-destructive/10 text-destructive")}>
                {data.successPct}% ok
              </span>
            )}
          </span>
          <span className="text-label text-muted-foreground">
            {data.runsTotal === 0 ? "no finished runs yet" : `${data.failed} failed${data.p95Ms > 0 ? ` · p95 ${formatDuration(data.p95Ms)}` : ""}`}
          </span>
        </Cell>

        <Cell label="Waiting on you">
          <span className="flex items-baseline gap-2">
            <span className={cn("text-[22px] font-semibold leading-none tracking-tight tabular-nums", data.attentionCount > 0 ? "text-warn" : "text-success")}>
              {data.attentionKnown || data.attentionCount > 0 ? <MetricNumber value={data.attentionCount} /> : "—"}
            </span>
            <span className="text-label text-muted-foreground">{data.attentionCount === 0 && data.attentionKnown ? "nothing blocked" : data.attentionKnown ? (data.attentionCount === 1 ? "item below" : "items below") : "checking…"}</span>
          </span>
          <Link href={entityHref({ kind: "inbox" })} className="inline-flex items-center gap-1 text-label text-primary-hover hover:underline">Open inbox <ArrowRight className="h-3 w-3" /></Link>
        </Cell>

        <Cell label="Next run" className="col-span-2 md:col-span-1">
          {data.nextSchedule ? (
            <>
              <span className="text-[15px] font-semibold tracking-tight">{formatRelativeTime(data.nextSchedule.next_run_at!)}</span>
              <span className="truncate text-label text-muted-foreground">{data.nextSchedule.target_pipeline_slug || data.nextSchedule.name}</span>
            </>
          ) : (
            <>
              <span className="text-[15px] font-semibold tracking-tight text-muted-foreground">nothing scheduled</span>
              <Link href="/routines" className="text-label text-primary-hover hover:underline">Add a trigger →</Link>
            </>
          )}
        </Cell>
      </div>
      <span className="sr-only"><Radio className="h-3 w-3" />{live ? "live" : realtimeStatus}</span>
    </div>
  )
}

function MetricNumber({ value }: { value: number }) {
  const reduce = useReducedMotion()
  return reduce ? <>{Math.round(value)}</> : <AnimatedNumber value={value} duration={0.72} />
}
