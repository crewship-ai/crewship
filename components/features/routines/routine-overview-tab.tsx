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
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { JSONViewer } from "@/components/features/activity/json-viewer"
import { relTime, formatDuration } from "@/lib/activity/format-time"
import { cn } from "@/lib/utils"
import { usePipelineRunRecords, type PipelineRunRecord } from "@/hooks/use-pipeline-run-records"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import type { RoutineDetail } from "./routines-detail-panel"

// RoutineOverviewTab — operational dashboard for a single routine,
// modeled on Stripe/Vercel: KPI tiles with sparklines, runs chart,
// schedule card, I/O schema, prominent last-run card with trigger
// source + result, recent-runs feed, spend breakdown, compact
// metadata footer. The whole tab answers the implicit question
// "what does this routine do, when did it last run, what caused it
// to run, and was it healthy?" — in that order — without leaving the
// pane.

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
  const inputs = asArrayOfObjects(def?.["inputs"])
  const outputs = asArrayOfObjects(def?.["outputs"])
  const creds = asArrayOfObjects(def?.["credentials_required"])
  const steps = asArrayOfObjects(def?.["steps"])

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

  return (
    <div className="space-y-4">
      {/* ── KPI strip ──────────────────────────────────────────────── */}
      <section className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <KpiTile
          label="Total runs"
          value={stats.total.toString()}
          delta={runs.length > 0 ? `+${runs.length} last 30` : "no runs yet"}
          spark={buckets.bins.map((b) => b.total)}
          tone="blue"
        />
        <KpiTile
          label="Pass rate"
          value={stats.passRate !== null ? `${stats.passRate}%` : "—"}
          deltaTone={stats.passRate !== null && stats.passRate >= 90 ? "up" : stats.passRate !== null && stats.passRate < 70 ? "down" : undefined}
          delta={
            stats.passRate !== null
              ? `${stats.succeeded} ok · ${stats.failed} failed`
              : "no completed runs yet"
          }
          spark={buckets.bins.map((b) => (b.total > 0 ? (b.total - b.failed) / b.total : 1))}
          tone="green"
        />
        <KpiTile
          label="Avg duration"
          value={stats.avgDurMs > 0 ? formatDuration(stats.avgDurMs) : "—"}
          delta={lastRun && lastRun.duration_ms > 0 ? `last ${formatDuration(lastRun.duration_ms)}` : "—"}
          spark={buckets.bins.map((b) => (b.total > 0 ? b.dur / b.total : 0))}
          tone="violet"
        />
        <KpiTile
          label="Total spend"
          value={stats.costSum > 0 ? `$${stats.costSum.toFixed(4)}` : "—"}
          delta={runs.length > 0 ? `avg $${(stats.costSum / Math.max(runs.length, 1)).toFixed(4)} / run` : "—"}
          spark={buckets.bins.map((b) => b.cost)}
          tone="amber"
        />
      </section>

      {/* ── Two-column body ────────────────────────────────────────── */}
      <div className="grid gap-4 lg:grid-cols-[2fr_1fr]">
        {/* Main column */}
        <div className="space-y-4">
          {/* Runs over time chart */}
          <Card title="Runs over time" subtitle="last 7 days">
            <RunsChart bins={buckets.bins} />
          </Card>

          {/* I/O schema */}
          <Card title="I/O schema" subtitle={`${inputs.length} in · ${outputs.length} out`}>
            <div className="divide-y divide-white/[0.04]">
              {inputs.map((inp, i) => (
                <IORow
                  key={`in-${i}`}
                  direction="in"
                  name={String(inp["name"])}
                  type={String(inp["type"])}
                  required={inp["required"] === true}
                  description={typeof inp["description"] === "string" ? String(inp["description"]) : undefined}
                  defaultValue={"default" in inp ? inp["default"] : undefined}
                />
              ))}
              {outputs.map((out, i) => (
                <IORow
                  key={`out-${i}`}
                  direction="out"
                  name={String(out["name"])}
                  type={String(out["type"])}
                />
              ))}
              {inputs.length === 0 && outputs.length === 0 && (
                <div className="py-4 text-center text-xs text-muted-foreground/60">
                  No inputs / outputs declared.
                </div>
              )}
            </div>
          </Card>

          {/* Last run — the prominent "data flow" card */}
          <LastRunCard run={lastRun} workspaceId={workspaceId} />

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
              <div className="divide-y divide-white/[0.04]">
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
              <div className="px-3 py-4 text-center text-xs text-muted-foreground/60">
                No cron triggers wired.<br />
                <span className="text-muted-foreground/40">Add one in the Schedules tab to run this routine on a cadence.</span>
              </div>
            ) : (
              <div className="divide-y divide-white/[0.04]">
                {schedules.slice(0, 4).map((s) => (
                  <div key={s.id} className="px-3 py-2.5">
                    <div className="flex items-center gap-2">
                      <Calendar className={cn("h-3.5 w-3.5 shrink-0", s.enabled ? "text-violet-400" : "text-muted-foreground/40")} />
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
                      <div className="mt-1 ml-5.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[10px] text-muted-foreground/70">
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
              <div className="h-9 w-9 shrink-0 rounded-full bg-gradient-to-br from-blue-500/60 to-violet-500/60" aria-hidden />
              <div className="min-w-0">
                <div className="truncate text-sm font-medium">{routine.authored_via.replace(/_/g, " ")}</div>
                <div className="text-[10px] text-muted-foreground">
                  {new Date(routine.created_at).toLocaleString()}
                </div>
              </div>
            </div>
          </Card>

          {/* Credentials */}
          {creds.length > 0 && (
            <Card title="Credentials required">
              <div className="divide-y divide-white/[0.04]">
                {creds.map((c, i) => (
                  <div key={i} className="flex items-center gap-2.5 px-3 py-2">
                    <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-violet-500/12 font-mono text-[10px] font-bold text-violet-300">
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
 * ----------------------------------------------------------------- */

function Card({
  title,
  subtitle,
  action,
  tone,
  children,
}: {
  title: string
  subtitle?: string
  action?: React.ReactNode
  tone?: "default" | "violet"
  children: React.ReactNode
}) {
  return (
    <div
      className={cn(
        "overflow-hidden rounded-xl border bg-card",
        tone === "violet" ? "border-violet-500/15" : "border-white/[0.06]",
      )}
    >
      <div className="flex items-center gap-2 border-b border-white/[0.04] px-4 py-2.5">
        <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">{title}</span>
        {subtitle && <span className="text-[11px] text-muted-foreground/50">{subtitle}</span>}
        {action && <span className="ml-auto">{action}</span>}
      </div>
      <div>{children}</div>
    </div>
  )
}

const SPARK_TONES = {
  blue: "rgb(107 141 247)",
  green: "rgb(52 211 153)",
  violet: "rgb(167 139 250)",
  amber: "rgb(245 158 11)",
} as const

function KpiTile({
  label,
  value,
  delta,
  deltaTone,
  spark,
  tone,
}: {
  label: string
  value: string
  delta?: string
  deltaTone?: "up" | "down"
  spark: number[]
  tone: keyof typeof SPARK_TONES
}) {
  const color = SPARK_TONES[tone]
  const max = Math.max(1, ...spark)
  return (
    <div className="relative overflow-hidden rounded-xl border border-white/[0.06] bg-card p-4">
      <div
        aria-hidden
        className="pointer-events-none absolute -right-6 -top-6 h-24 w-24 rounded-full"
        style={{ background: `radial-gradient(circle, ${color}22, transparent 70%)` }}
      />
      <div className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">{label}</div>
      <div className="mt-2 text-3xl font-bold tabular-nums leading-none tracking-tight">{value}</div>
      {delta && (
        <div
          className={cn(
            "mt-2 text-[12px]",
            deltaTone === "up" ? "text-emerald-400" : deltaTone === "down" ? "text-rose-400" : "text-muted-foreground/70",
          )}
        >
          {delta}
        </div>
      )}
      <div className="mt-3 flex h-7 items-end gap-[3px]">
        {spark.map((v, i) => (
          <span
            key={i}
            className="flex-1 rounded-sm"
            style={{
              height: `${Math.max(6, (v / max) * 100)}%`,
              backgroundColor: color,
              opacity: 0.6,
            }}
          />
        ))}
      </div>
    </div>
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
      <div className="mt-1 flex justify-between text-[10px] text-muted-foreground/50">
        {bins.map((b, i) => (
          <span key={i}>{b.day}</span>
        ))}
      </div>
    </div>
  )
}

function IORow({
  direction,
  name,
  type,
  required,
  description,
  defaultValue,
}: {
  direction: "in" | "out"
  name: string
  type: string
  required?: boolean
  description?: string
  defaultValue?: unknown
}) {
  const isIn = direction === "in"
  // Long defaults (multi-line strings, big JSON) were rendering as a
  // wall-of-text in the meta line. Render short defaults inline; collapse
  // long ones into a <details> block so the row stays scannable.
  const defaultStr = defaultValue !== undefined ? JSON.stringify(defaultValue) : null
  const isLongDefault = defaultStr !== null && defaultStr.length > 60
  return (
    <div className="flex items-start gap-3 px-4 py-3">
      <span
        className={cn(
          "shrink-0 font-mono text-base leading-tight",
          isIn ? "text-blue-400" : "text-emerald-400",
        )}
        aria-hidden
      >
        {isIn ? "→" : "←"}
      </span>
      <div className="min-w-0 flex-1 space-y-1.5">
        {/* Name + type + flags on one line */}
        <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
          <span className="font-mono text-sm font-semibold text-foreground">{name}</span>
          <span className="rounded bg-white/[0.04] px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
            {type}
          </span>
          {required && (
            <span className="rounded bg-amber-500/10 px-1.5 py-0.5 text-[11px] text-amber-300">
              required
            </span>
          )}
          {defaultStr !== null && !isLongDefault && (
            <span className="font-mono text-[11px] text-muted-foreground/70">
              default {defaultStr}
            </span>
          )}
        </div>
        {/* Description */}
        {description && (
          <p className="text-[13px] leading-relaxed text-muted-foreground">{description}</p>
        )}
        {/* Long default — collapsible, preview clipped */}
        {isLongDefault && defaultStr !== null && (
          <details className="group rounded-md border border-white/[0.06] bg-black/20 text-[12px]">
            <summary className="cursor-pointer list-none px-2.5 py-1.5 font-mono text-muted-foreground hover:text-foreground">
              <span className="mr-1 inline-block transition-transform group-open:rotate-90">▸</span>
              default value <span className="text-faint">· {defaultStr.length} chars</span>
            </summary>
            <pre className="m-0 max-h-48 overflow-auto border-t border-white/[0.04] px-2.5 py-2 font-mono text-[12px] leading-relaxed text-foreground/85">
              {defaultStr}
            </pre>
          </details>
        )}
      </div>
    </div>
  )
}

function LastRunCard({
  run,
  workspaceId,
}: {
  run: PipelineRunRecord | null
  workspaceId: string | undefined
}) {
  if (!run) {
    return (
      <Card title="Last run">
        <div className="px-3 py-5 text-center text-xs text-muted-foreground/60">
          This routine hasn't been invoked yet.<br />
          <span className="text-muted-foreground/40">Click <b>Run</b> above to trigger a manual invocation.</span>
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
    ? { bg: "bg-emerald-500/12", text: "text-emerald-400", ring: "ring-emerald-500/30" }
    : isFailed
      ? { bg: "bg-rose-500/12", text: "text-rose-400", ring: "ring-rose-500/30" }
      : { bg: "bg-blue-500/12", text: "text-blue-400", ring: "ring-blue-500/30" }

  return (
    <div className="overflow-hidden rounded-lg border border-white/[0.06] bg-card">
      <div
        className={cn(
          "flex items-center gap-3 border-b border-white/[0.04] px-4 py-3",
          isCompleted && "bg-gradient-to-r from-emerald-500/[0.04] to-transparent",
          isFailed && "bg-gradient-to-r from-rose-500/[0.04] to-transparent",
        )}
      >
        <div
          className={cn(
            "flex h-9 w-9 shrink-0 items-center justify-center rounded-lg ring-1",
            statusTone.bg,
            statusTone.ring,
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
              <div className="text-foreground tabular-nums">{formatDuration(run.duration_ms)}</div>
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
        {run.output && (
          <div>
            <div className="mb-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Output</div>
            <div className="overflow-hidden rounded-md border border-white/[0.06] bg-black/30">
              <JSONViewer value={run.output} />
            </div>
          </div>
        )}
        {workspaceId && (
          <Link
            href={`/activity?run=${encodeURIComponent(run.id)}`}
            className="inline-flex items-center gap-1.5 rounded-md bg-blue-500/12 px-2.5 py-1.5 text-[11px] font-medium text-blue-300 transition-colors hover:bg-blue-500/20"
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
        {run.duration_ms > 0 ? formatDuration(run.duration_ms) : "—"}
      </span>
      <span className={cn("shrink-0 text-right text-muted-foreground/60", isFailed && "text-rose-400")}>
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
    <div className="border-b border-dashed border-white/[0.04] py-2 last:border-b-0">
      <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70">{k}</div>
      <div className={cn("mt-0.5 text-sm text-foreground/90", mono && "font-mono text-[12px]")} title={title}>
        {v}
      </div>
    </div>
  )
}

// `ChevronRight` is imported but only used in one place currently; keep
// the import in case future runs-row reintroduces a "view detail" arrow.
void ChevronRight
