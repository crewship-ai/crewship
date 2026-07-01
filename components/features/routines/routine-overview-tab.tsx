"use client"

import { useMemo } from "react"
import Link from "next/link"
import {
  ExternalLink,
  Play,
  Calendar,
  Webhook,
  Link2,
  CircleDot,
  Bot,
  CheckCircle2,
  XCircle,
  ChevronRight,
  Activity,
  Puzzle,
  ListChecks,
  ShieldAlert,
  Clock,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { relTime, formatDurationDecimal } from "@/lib/time"
import { cn } from "@/lib/utils"
import { integrationLabel } from "@/lib/integration-labels"
import { usePipelineRunRecords, type PipelineRunRecord } from "@/hooks/use-pipeline-run-records"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import { useRunSubSpans } from "@/hooks/use-run-sub-spans"
import type { RoutineDetail } from "./routines-detail-panel"
import { Card } from "./_shared"
import { RoutineTouches } from "./routine-touches"
import { RoutineMiniTrace } from "./routine-mini-trace"
import { buildPlainSteps, type PlainStep } from "@/lib/routine-flow"
import { buildMiniTrace } from "@/lib/routine-mini-trace"

// RoutineOverviewTab — the approachable, non-technical summary of a single
// routine. Leads with the essentials a human asks first: a thin stat strip,
// "What it does" (plain-language steps with deterministic/AI tags), "What it
// touches" (brand-logo chips = blast radius), and the prominent last-run card.
// Heavier/operational detail (runs-over-time chart, recent-runs feed,
// schedules, metadata, integrations, credentials) is demoted below the fold.
// The data-flow diagram lives in its own "Preview" tab; the raw I/O schema is
// intentionally gone (it lived in the Editor/Advanced surfaces instead).

// Defensive DSL field extraction so a malformed routine doesn't crash
// the tab (the Editor tab still surfaces the raw JSON).
function asArrayOfObjects(v: unknown): Array<Record<string, unknown>> {
  if (!Array.isArray(v)) return []
  return v.filter(
    (item): item is Record<string, unknown> =>
      !!item && typeof item === "object" && !Array.isArray(item),
  )
}

// Trigger source taxonomy. The backend stores the literal string in
// pipeline_runs.triggered_via; map it to an icon + human label here.
// 'agent_tool_call' and 'issue' aren't in the canonical enum yet but
// the API surface accepts them, so we handle them defensively.
type TriggerKey = PipelineRunRecord["triggered_via"] | "agent_tool_call" | "issue" | "unknown"
const TRIGGER_META: Record<TriggerKey, { label: string; Icon: typeof Play; tone: string }> = {
  manual: { label: "Manual", Icon: Play, tone: "text-accent-foreground" },
  schedule: { label: "Schedule", Icon: Calendar, tone: "text-violet-400" },
  webhook: { label: "Webhook", Icon: Webhook, tone: "text-amber-400" },
  call_pipeline: { label: "Called by routine", Icon: Link2, tone: "text-cyan-400" },
  agent_tool_call: { label: "Agent tool", Icon: Bot, tone: "text-pink-400" },
  issue: { label: "From issue", Icon: CircleDot, tone: "text-blue-400" },
  unknown: { label: "Unknown", Icon: Play, tone: "text-muted-foreground" },
}

function triggerMeta(via: string | undefined) {
  if (!via) return TRIGGER_META.unknown
  return TRIGGER_META[(via as TriggerKey)] ?? TRIGGER_META.unknown
}

export function RoutineOverviewTab({
  routine,
  workspaceId,
}: {
  routine: RoutineDetail
  workspaceId?: string
}) {
  const def = routine.definition as Record<string, unknown> | undefined
  const creds = asArrayOfObjects(def?.["credentials_required"])
  const steps = asArrayOfObjects(def?.["steps"])
  // Required third-party integrations (Composio connector slugs). Filter
  // to non-empty strings so a malformed entry can't render a blank chip.
  const integrations = Array.isArray(routine.integrations_required)
    ? routine.integrations_required.filter((s): s is string => typeof s === "string" && s.trim().length > 0)
    : []

  const { records: runs } = usePipelineRunRecords(workspaceId ?? null, routine.slug)
  const { schedules: allSchedules } = usePipelineSchedules(workspaceId ?? null)
  const schedules = useMemo(
    () =>
      allSchedules.filter(
        (s) => s.target_pipeline_id === routine.id || s.target_pipeline_slug === routine.slug,
      ),
    [allSchedules, routine.id, routine.slug],
  )

  // KPI aggregates. Computed against the 30 most recent runs the hook
  // returns by default — enough to power a sparkline + averages while
  // staying cheap. Total runs comes from routine.invocation_count
  // (workspace-canonical counter) rather than recordsCount because the
  // hook is capped at 30.
  const stats = useMemo(() => {
    const total = routine.invocation_count ?? 0
    const finished = runs.filter((r) => r.status === "completed" || r.status === "failed")
    const succeeded = runs.filter((r) => r.status === "completed").length
    const failed = runs.filter((r) => r.status === "failed").length
    const passRate = finished.length > 0 ? Math.round((succeeded / finished.length) * 100) : null
    const durations = runs.filter((r) => r.duration_ms > 0).map((r) => r.duration_ms)
    const avgDurMs = durations.length > 0 ? durations.reduce((a, b) => a + b, 0) / durations.length : 0
    const costSum = runs.reduce((a, b) => a + (b.cost_usd ?? 0), 0)
    return { total, succeeded, failed, passRate, avgDurMs, costSum }
  }, [runs, routine.invocation_count])

  // Daily buckets for the runs-over-time chart + sparklines. 7 days
  // back from today (inclusive) at one-day resolution. We render the
  // chart as a single area curve of total runs/day and a smaller red
  // overlay of failed runs/day for the "incidents" line.
  const buckets = useMemo(() => {
    const days = 7
    const now = Date.now()
    const dayMs = 24 * 60 * 60 * 1000
    const bins: Array<{ day: string; total: number; failed: number; cost: number; dur: number }> = []
    for (let i = days - 1; i >= 0; i--) {
      const d = new Date(now - i * dayMs)
      bins.push({
        day: d.toLocaleDateString(undefined, { month: "short", day: "numeric" }),
        total: 0,
        failed: 0,
        cost: 0,
        dur: 0,
      })
    }
    for (const r of runs) {
      const t = Date.parse(r.started_at)
      if (Number.isNaN(t)) continue
      const idx = days - 1 - Math.floor((now - t) / dayMs)
      if (idx >= 0 && idx < days) {
        bins[idx].total += 1
        if (r.status === "failed") bins[idx].failed += 1
        bins[idx].cost += r.cost_usd ?? 0
        if (r.duration_ms > 0) bins[idx].dur += r.duration_ms
      }
    }
    const maxTotal = Math.max(1, ...bins.map((b) => b.total))
    return { bins, maxTotal }
  }, [runs])

  const lastRun = runs[0] ?? null

  // Plain-language step list + AI/script tags for the "What it does" panel,
  // derived purely from the DSL (lib/routine-flow). Memoized on the
  // definition object identity so we don't re-walk on unrelated re-renders.
  const plainSteps = useMemo(
    () => buildPlainSteps(routine.definition),
    [routine.definition],
  )
  const manifest = routine.manifest

  return (
    <div className="space-y-4">
      {/* ── Compact stat strip (DEMOTED from 4 big KPI cards) ───────── */}
      <section className="flex flex-wrap overflow-hidden rounded-lg border border-border/60">
        <StatCell label="Runs" value={stats.total.toString()} />
        <StatCell
          label="Pass"
          value={stats.passRate !== null ? `${stats.passRate}%` : "—"}
          tone={stats.passRate !== null && stats.passRate >= 90 ? "green" : undefined}
        />
        <StatCell label="Avg" value={stats.avgDurMs > 0 ? formatDurationDecimal(stats.avgDurMs) : "—"} />
        <StatCell
          label="Spend / run"
          value={runs.length > 0 ? `$${(stats.costSum / Math.max(runs.length, 1)).toFixed(4)}` : "—"}
        />
        <StatCell
          label="Schedule"
          value={schedules.length > 0 ? schedules[0].cron_expr : "manual"}
          mono={schedules.length > 0}
        />
      </section>

      {/* ── ★ Essentials — what it does + what it touches ──────────── */}
      <div className="grid gap-4 lg:grid-cols-[2fr_1fr]">
        {/* What it does — plain-language steps + deterministic/AI tags so
            users aren't reading raw DSL JSON to understand the routine. */}
        <Card title="What it does" subtitle="step by step" icon={ListChecks}>
          <ol className="px-4 py-2">
            {plainSteps.map((s) => (
              <PlainStepRow key={s.id} step={s} />
            ))}
          </ol>
        </Card>

        {/* What it touches — capability manifest as brand-logo chips. */}
        <Card title="What it touches" subtitle="blast radius" icon={ShieldAlert}>
          <div className="px-3 py-2">
            <RoutineTouches manifest={manifest} />
          </div>
        </Card>
      </div>

      {/* ── ★ Last run — the prominent result card ─────────────────── */}
      <LastRunCard run={lastRun} workspaceId={workspaceId} definition={def} />

      {/* ── Operational detail — demoted below the essentials ──────── */}
      <div className="grid gap-4 lg:grid-cols-[2fr_1fr]">
        {/* Main column */}
        <div className="space-y-4">
          {/* Runs over time chart */}
          <Card title="Runs over time" subtitle="last 7 days">
            <RunsChart bins={buckets.bins} />
          </Card>

          {/* Recent runs feed */}
          {runs.length > 1 && (
            <Card
              title="Recent runs"
              subtitle={`${Math.min(runs.length, 5)} of ${runs.length}`}
              action={
                <Link
                  href={`/activity?pipeline=${encodeURIComponent(routine.slug)}`}
                  className="text-[11px] text-muted-foreground hover:text-foreground"
                >
                  view all →
                </Link>
              }
            >
              <div className="divide-y divide-border/40">
                {runs.slice(0, 5).map((r) => (
                  <RunFeedRow key={r.id} run={r} />
                ))}
              </div>
            </Card>
          )}
        </div>

        {/* Right column */}
        <div className="space-y-4">
          {/* Schedules card */}
          <Card
            title="Schedules"
            subtitle={schedules.length > 0 ? `${schedules.length} active` : "none"}
            tone={schedules.length > 0 ? "violet" : "default"}
          >
            {schedules.length === 0 ? (
              <div className="px-3 py-4 text-center text-xs text-muted-foreground">
                No cron triggers wired.<br />
                <span className="text-muted-foreground-soft">Add one in the Schedules tab to run this routine on a cadence.</span>
              </div>
            ) : (
              <div className="divide-y divide-border/40">
                {schedules.slice(0, 4).map((s) => (
                  <div key={s.id} className="px-3 py-2.5">
                    <div className="flex items-center gap-2">
                      <Calendar className={cn("h-3.5 w-3.5 shrink-0", s.enabled ? "text-violet-400" : "text-muted-foreground-soft")} />
                      <span className="truncate text-xs font-medium">{s.name}</span>
                      {!s.enabled && (
                        <Badge variant="outline" className="px-1 py-0 text-[9px]">paused</Badge>
                      )}
                    </div>
                    <div className="mt-1 ml-5.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 font-mono text-[10px] text-muted-foreground">
                      <span>{s.cron_expr}</span>
                      <span className="opacity-60">·</span>
                      <span>{s.timezone}</span>
                    </div>
                    {(s.next_run_at || s.last_run_at) && (
                      <div className="mt-1 ml-5.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[10px] text-muted-foreground">
                        {s.next_run_at && <span>next <span className="text-foreground/80">{relTime(s.next_run_at)}</span></span>}
                        {s.last_run_at && <span>last <span className="text-foreground/80">{relTime(s.last_run_at)}</span></span>}
                      </div>
                    )}
                  </div>
                ))}
              </div>
            )}
          </Card>

          {/* Metadata */}
          <Card title="Metadata">
            <div className="grid grid-cols-2 gap-x-3 px-3 py-2">
              <MetaCell k="DSL version" v={routine.dsl_version} mono />
              <MetaCell k="Visibility" v={routine.workspace_visible ? "workspace" : "private"} />
              <MetaCell k="Hash" v={routine.definition_hash.slice(0, 10) + "…"} mono title={routine.definition_hash} />
              <MetaCell k="Steps" v={String(steps.length)} mono />
              <MetaCell k="Created" v={new Date(routine.created_at).toLocaleDateString()} />
              <MetaCell k="Updated" v={new Date(routine.updated_at).toLocaleDateString()} />
              {routine.ephemeral && <MetaCell k="Type" v="ephemeral" />}
              {routine.head_version != null && <MetaCell k="Head" v={`v${routine.head_version}`} mono />}
            </div>
          </Card>

          {/* Authored */}
          <Card title="Authored">
            <div className="flex items-center gap-3 px-3 py-3">
              <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-primary/20 text-primary" aria-hidden>
                <span className="text-sm font-semibold">
                  {routine.authored_via.charAt(0).toUpperCase()}
                </span>
              </div>
              <div className="min-w-0">
                <div className="truncate text-sm font-medium">{routine.authored_via.replace(/_/g, " ")}</div>
                <div className="text-[11px] text-muted-foreground">
                  {new Date(routine.created_at).toLocaleString()}
                </div>
              </div>
            </div>
          </Card>

          {/* Integrations — third-party connectors the executing crew must
              have connected for a run to succeed. Empty/absent → no card. */}
          {integrations.length > 0 && (
            <Card title="Integrations" subtitle={`${integrations.length} required`}>
              <div className="flex flex-wrap gap-2 px-3 py-3">
                {integrations.map((slug) => (
                  <span
                    key={slug}
                    className="inline-flex items-center gap-1.5 rounded-full border border-border/60 bg-white/[0.04] px-2.5 py-1 text-xs font-medium text-foreground/90"
                    title={slug}
                  >
                    <Puzzle className="h-3 w-3 text-cyan-400" aria-hidden />
                    {integrationLabel(slug)}
                  </span>
                ))}
              </div>
            </Card>
          )}

          {/* Credentials */}
          {creds.length > 0 && (
            <Card title="Credentials required">
              <div className="divide-y divide-border/40">
                {creds.map((c, i) => (
                  <div key={i} className="flex items-center gap-2.5 px-3 py-2">
                    <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-violet-500/20 font-mono text-[10px] font-bold text-violet-400">
                      {String(c["type"]).slice(0, 2).toUpperCase()}
                    </div>
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-xs font-medium">{String(c["type"])}</div>
                      {typeof c["scope"] === "string" && (
                        <div className="font-mono text-[10px] text-muted-foreground">scope: {String(c["scope"])}</div>
                      )}
                    </div>
                  </div>
                ))}
              </div>
            </Card>
          )}
        </div>
      </div>
    </div>
  )
}

/* ----------------------------------------------------------------- *
 *  Sub-components                                                   *
 *  Card primitive lives in ./_shared.tsx — single source of truth   *
 *  shared by every routine sub-tab.                                 *
 * ----------------------------------------------------------------- */

// StatCell — one segment of the thin demoted stat strip. Replaces the four
// large KPI tiles the redesign collapses into a single compact row.
function StatCell({
  label,
  value,
  tone,
  mono,
}: {
  label: string
  value: string
  tone?: "green"
  mono?: boolean
}) {
  return (
    <div className="min-w-[96px] flex-1 border-r border-border/60 px-3.5 py-2 last:border-r-0">
      <div className="text-[10px] uppercase tracking-wide text-muted-foreground-soft">{label}</div>
      <div
        className={cn(
          "mt-0.5 truncate text-[13px] font-semibold tabular-nums",
          tone === "green" && "text-emerald-400",
          mono && "font-mono text-[11px] font-medium",
        )}
        title={value}
      >
        {value}
      </div>
    </div>
  )
}

// PlainStepRow — one line of the "What it does" list: a numbered (or
// trigger) marker, a human title with a det-vs-AI tag, and an optional
// detail line. The AI tag (indigo) flags non-deterministic agent steps; the
// script tag (violet) marks deterministic ones.
function PlainStepRow({ step }: { step: PlainStep }) {
  const isTrigger = step.determinism === "trigger"
  return (
    <li className="flex gap-3 border-t border-white/[0.04] py-2.5 first:border-t-0">
      <span
        className={cn(
          "mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-md text-[10px] font-medium",
          isTrigger ? "bg-amber-500/15 text-amber-400" : "bg-white/[0.06] text-muted-foreground",
        )}
      >
        {isTrigger ? <Clock className="h-3 w-3" aria-hidden /> : step.index}
      </span>
      <div className="min-w-0">
        <div className="text-[12.5px] leading-snug text-foreground/90">
          {step.title}
          {!isTrigger && (
            <span
              className={cn(
                "ml-1.5 rounded px-1.5 py-px align-middle text-[9.5px] font-medium",
                step.determinism === "ai"
                  ? "bg-indigo-500/15 text-indigo-300"
                  : "bg-violet-500/15 text-violet-300",
              )}
            >
              {step.determinism === "ai" ? "AI" : "script"}
            </span>
          )}
        </div>
        {step.detail && (
          <div className="mt-0.5 truncate font-mono text-[10.5px] text-muted-foreground-soft" title={step.detail}>
            {step.detail}
          </div>
        )}
      </div>
    </li>
  )
}

function RunsChart({ bins }: { bins: Array<{ day: string; total: number; failed: number }> }) {
  // Lightweight inline SVG; preserveAspectRatio="none" lets us draw in
  // a virtual 600×100 grid and stretch to whatever width the card has.
  const W = 600
  const H = 100
  const max = Math.max(1, ...bins.map((b) => b.total))
  const step = bins.length > 1 ? W / (bins.length - 1) : W
  const points = bins.map((b, i) => {
    const x = i * step
    const y = H - (b.total / max) * (H - 12) - 6
    return { x, y, ...b }
  })
  const linePath = points.map((p, i) => `${i === 0 ? "M" : "L"}${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(" ")
  const areaPath = `${linePath} L${W},${H} L0,${H} Z`

  return (
    <div className="px-3 py-3">
      <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="h-32 w-full">
        <defs>
          <linearGradient id="runsArea" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="rgb(107 141 247)" stopOpacity="0.45" />
            <stop offset="100%" stopColor="rgb(107 141 247)" stopOpacity="0" />
          </linearGradient>
        </defs>
        <path d={areaPath} fill="url(#runsArea)" />
        <path d={linePath} fill="none" stroke="rgb(107 141 247)" strokeWidth="1.5" />
        {points.map((p, i) => (
          <g key={i}>
            <circle cx={p.x} cy={p.y} r="2.5" fill="rgb(107 141 247)" />
            {p.failed > 0 && <circle cx={p.x} cy={p.y} r="4" fill="none" stroke="rgb(244 63 94)" strokeWidth="1.5" />}
          </g>
        ))}
      </svg>
      <div className="mt-1 flex justify-between text-[10px] text-muted-foreground-soft">
        {bins.map((b, i) => (
          <span key={i}>{b.day}</span>
        ))}
      </div>
    </div>
  )
}

function LastRunCard({
  run,
  workspaceId,
  definition,
}: {
  run: PipelineRunRecord | null
  workspaceId: string | undefined
  definition: Record<string, unknown> | undefined
}) {
  // Best-effort fetch of the run detail (carries sub_spans + step_outputs
  // the list row omits) to power the mini-trace's tool calls. Hooks must
  // run unconditionally, so this sits above the early return; it no-ops
  // when there's no run.
  const { run: runDetail } = useRunSubSpans(workspaceId, run?.id ?? null)

  // Compact "how did it flow + what did it call" projection. Prefer the
  // detail (sub_spans + per-step status); fall back to the list row so the
  // flow still renders before/without the detail fetch. Memoized on the
  // inputs so we don't re-walk the DSL on unrelated re-renders.
  const miniTrace = useMemo(
    () => buildMiniTrace(definition, runDetail ?? run),
    [definition, runDetail, run],
  )

  if (!run) {
    return (
      <Card title="Last run">
        <div className="px-3 py-5 text-center text-xs text-muted-foreground">
          This routine hasn't been invoked yet.<br />
          <span className="text-muted-foreground-soft">Click <b>Run</b> above to trigger a manual invocation.</span>
        </div>
      </Card>
    )
  }

  const meta = triggerMeta(run.triggered_via)
  const Icon = meta.Icon
  const isCompleted = run.status === "completed"
  const isFailed = run.status === "failed"
  const StatusIcon = isCompleted ? CheckCircle2 : isFailed ? XCircle : Activity
  const statusTone = isCompleted
    ? { bg: "bg-emerald-500/20", text: "text-emerald-400" }
    : isFailed
      ? { bg: "bg-rose-500/20", text: "text-rose-400" }
      : { bg: "bg-blue-500/20", text: "text-blue-400" }

  return (
    <div className="overflow-hidden rounded-lg border border-border/60 bg-card">
      <div
        className={cn(
          "flex items-center gap-3 border-b border-border/40 px-4 py-3",
          isCompleted && "bg-gradient-to-r from-emerald-500/[0.04] to-transparent",
          isFailed && "bg-gradient-to-r from-rose-500/[0.04] to-transparent",
        )}
      >
        <div
          className={cn(
            "flex h-9 w-9 shrink-0 items-center justify-center rounded-lg",
            statusTone.bg,
          )}
        >
          <StatusIcon className={cn("h-4 w-4", statusTone.text)} />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="text-sm font-semibold capitalize">Last run · {run.status}</span>
            <span
              className={cn(
                "inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 text-[9px] font-medium ring-1",
                "ring-white/[0.08] bg-white/[0.04] text-muted-foreground",
              )}
            >
              <Icon className={cn("h-2.5 w-2.5", meta.tone)} />
              {meta.label}
              {run.triggered_by_id && (run.triggered_via as string) === "issue" && (
                <span className="font-mono text-foreground/80">·&nbsp;{run.triggered_by_id}</span>
              )}
            </span>
          </div>
          <div className="mt-0.5 font-mono text-[10px] text-muted-foreground">{run.id}</div>
        </div>
        <div className="flex shrink-0 items-center gap-4 text-right text-[10px] text-muted-foreground">
          <div>
            <div className="text-foreground tabular-nums">{relTime(run.started_at)}</div>
            <div>started</div>
          </div>
          {run.duration_ms > 0 && (
            <div>
              <div className="text-foreground tabular-nums">{formatDurationDecimal(run.duration_ms)}</div>
              <div>duration</div>
            </div>
          )}
          {run.cost_usd > 0 && (
            <div>
              <div className="text-foreground tabular-nums">${run.cost_usd.toFixed(4)}</div>
              <div>cost</div>
            </div>
          )}
        </div>
      </div>

      <div className="space-y-3 p-4">
        {run.error_message && (
          <div className="rounded-md border border-rose-500/30 bg-rose-500/[0.06] px-3 py-2 font-mono text-[11px] text-rose-300">
            {run.failed_at_step && <span className="opacity-70">{run.failed_at_step}: </span>}
            {run.error_message}
          </div>
        )}
        {/* Mini-trace — HOW the run flowed + what it called, not its output
            (the output lives in Activity → Output). A compact, read-only
            projection of the step flow with each agent step's tool calls. */}
        <div>
          <div className="mb-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
            How it ran
          </div>
          <RoutineMiniTrace nodes={miniTrace} />
        </div>
        {workspaceId && (
          <Link
            href={`/activity?run=${encodeURIComponent(run.id)}`}
            className="inline-flex items-center gap-1.5 rounded-md bg-blue-500/20 px-2.5 py-1.5 text-[11px] font-medium text-blue-400 transition-colors hover:bg-blue-500/30"
          >
            <ExternalLink className="h-3 w-3" />
            Open full trace in Activity
          </Link>
        )}
      </div>
    </div>
  )
}

function RunFeedRow({ run }: { run: PipelineRunRecord }) {
  const meta = triggerMeta(run.triggered_via)
  const Icon = meta.Icon
  const isFailed = run.status === "failed"
  return (
    <div className="grid grid-cols-[14px_minmax(0,1fr)_auto_auto_auto] items-center gap-3 px-4 py-2.5 text-xs">
      <span
        aria-hidden
        title={run.status}
        className={cn(
          "h-2.5 w-2.5 shrink-0 rounded-full",
          run.status === "completed" && "bg-emerald-500",
          run.status === "failed" && "bg-rose-500",
          run.status === "running" && "bg-blue-500",
          run.status === "queued" && "bg-amber-500",
          run.status === "cancelled" && "bg-muted-foreground/40",
          run.status === "dry_run" && "bg-violet-500",
          run.status === "interrupted" && "bg-amber-500",
        )}
      />
      <span className="truncate font-mono text-muted-foreground">{run.id.slice(0, 16)}…</span>
      <span className={cn("inline-flex shrink-0 items-center gap-1 text-[11px]", meta.tone)} title={meta.label}>
        <Icon className="h-3 w-3" />
        {meta.label}
      </span>
      <span className="shrink-0 text-right tabular-nums text-muted-foreground">
        {run.duration_ms > 0 ? formatDurationDecimal(run.duration_ms) : "—"}
      </span>
      <span className={cn("shrink-0 text-right text-muted-foreground", isFailed && "text-rose-400")}>
        {relTime(run.started_at)}
      </span>
    </div>
  )
}

function MetaCell({
  k,
  v,
  mono,
  title,
}: {
  k: string
  v: string
  mono?: boolean
  title?: string
}) {
  return (
    <div className="border-b border-dashed border-border/40 py-2 last:border-b-0">
      <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">{k}</div>
      <div className={cn("mt-0.5 text-sm text-foreground/90", mono && "font-mono text-[12px]")} title={title}>
        {v}
      </div>
    </div>
  )
}

// `ChevronRight` is imported but only used in one place currently; keep
// the import in case future runs-row reintroduces a "view detail" arrow.
void ChevronRight
