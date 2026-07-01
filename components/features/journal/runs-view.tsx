"use client"

// RunsView — the "Runs" tab inside /journal, reframed as a fleet
// operations overview rather than a flat run list. It's the only surface
// that spans ALL runs in the workspace (routine + ad-hoc agent/chat/user),
// so it leans into breakdowns the routine-scoped Routines→Insights view
// structurally can't show: by trigger, by crew, by model.
//
// Four sections:
//   1. Live pulse strip  — running executions across the whole fleet, live.
//   2. KPI row           — outcome split + success rate + duration percentiles.
//   3. Breakdown row     — by trigger / top crews / by model.
//   4. Recent runs table — demoted list; each row deep-links to the Activity
//                          trace. Adds the resolved Model column.
//
// Data:
//   - /api/v1/runs               — the recent-runs table + live strip (status=RUNNING).
//   - /api/v1/runs/insights      — sections 2 & 3 (windowed aggregates).
// Both refresh silently on the same run.* WebSocket events.

import { useCallback, useEffect, useState } from "react"
import {
  Activity,
  AlertTriangle,
  CheckCircle,
  ChevronLeft,
  ChevronRight,
  Clock,
  ExternalLink,
  Play,
  RefreshCw,
  Users,
  XCircle,
  Zap,
} from "lucide-react"
import Link from "next/link"
import { useRouter } from "next/navigation"

import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import { StatusBadge, StatusDot } from "@/components/ui/status-badge"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { SettingsCard } from "@/components/features/settings/shared"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import { statusLabel, toCanonicalStatus } from "@/lib/runs-format"
import { formatDurationBetween, formatRelativeShort, formatDurationMillis } from "@/lib/time"
import {
  type RunInsights,
  type RunWindow,
  RUN_WINDOWS,
  WINDOW_LABEL,
  successRate,
  successRateColor,
  barPercent,
  maxTotal,
  shortModel,
  failRate,
} from "@/lib/runs-insights"

// Selectors for elements that should swallow row-level click/keypress
// events. Hoisted so the per-render .map() doesn't reallocate them.
const INTERACTIVE_SELECTOR =
  'a,button,[role="button"],[role="link"],input,textarea,select'

function isFromInteractiveChild(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false
  const hit = target.closest(INTERACTIVE_SELECTOR)
  return hit !== null && hit !== target.closest("[data-row-root]")
}

interface Run {
  id: string
  agent_id: string
  status: string
  trigger_type: string
  started_at: string | null
  finished_at: string | null
  error_message: string | null
  exit_code: number | null
  created_at: string
  model?: string
  agent_name?: string
  agent_slug?: string
  crew_name?: string
  triggerer: { id: string; email: string; full_name: string | null } | null
}

interface RunsResponse {
  data: Run[]
  stats: { running: number; today: number; failed: number }
  pagination: { page: number; limit: number; total: number; total_pages: number }
}

const PAGE_SIZE = 25
// Cap the live-pulse fetch — a fleet with hundreds of concurrent runs still
// only needs a scannable strip, not the whole set.
const LIVE_LIMIT = 25

// Human labels for trigger keys surfaced by the aggregate. CRON reads as
// "Schedule" everywhere else in the product, so keep parity here.
const TRIGGER_LABEL: Record<string, string> = {
  USER: "User",
  WEBHOOK: "Webhook",
  CRON: "Schedule",
  SCHEDULE: "Schedule",
  AGENT: "Agent",
  SYSTEM: "System",
  unknown: "Other",
}

function triggerLabel(key: string): string {
  return TRIGGER_LABEL[key] ?? (key.charAt(0) + key.slice(1).toLowerCase())
}

function LiveRunDuration({ startedAt }: { startedAt: string }) {
  const [, setTick] = useState(0)
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), 1000)
    return () => clearInterval(id)
  }, [])
  return <>{formatDurationBetween(startedAt, null)}</>
}

interface RunsViewProps {
  /** Workspace ID — required; component returns null when unset. */
  workspaceId: string | null
  /** Whether the parent has finished loading workspace context. */
  workspaceLoading: boolean
}

export function RunsView({ workspaceId, workspaceLoading }: RunsViewProps) {
  const router = useRouter()
  const [data, setData] = useState<RunsResponse | null>(null)
  const [insights, setInsights] = useState<RunInsights | null>(null)
  const [liveRuns, setLiveRuns] = useState<Run[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [statusFilter, setStatusFilter] = useState("all")
  const [triggerFilter, setTriggerFilter] = useState("all")
  const [window, setWindow] = useState<RunWindow>("24h")
  const [page, setPage] = useState(1)

  const fetchRuns = useCallback(
    async (opts?: { silent?: boolean }) => {
      if (!workspaceId) return
      const silent = opts?.silent ?? false
      if (silent) setRefreshing(true)
      else {
        setLoading(true)
        setError(null)
      }
      try {
        const params = new URLSearchParams({
          workspace_id: workspaceId,
          page: String(page),
          limit: String(PAGE_SIZE),
        })
        if (statusFilter !== "all") params.set("status", statusFilter)
        if (triggerFilter !== "all") params.set("trigger", triggerFilter)

        const res = await apiFetch(`/api/v1/runs?${params}`)
        if (!res.ok) {
          setError("Failed to load runs")
          return
        }
        const result = (await res.json()) as RunsResponse
        setData(result)
      } catch {
        setError("Failed to load runs")
      } finally {
        setLoading(false)
        setRefreshing(false)
      }
    },
    [workspaceId, page, statusFilter, triggerFilter],
  )

  const fetchInsights = useCallback(async () => {
    if (!workspaceId) return
    try {
      const params = new URLSearchParams({ workspace_id: workspaceId, window })
      const res = await apiFetch(`/api/v1/runs/insights?${params}`)
      if (res.ok) setInsights((await res.json()) as RunInsights)
    } catch {
      /* insights are non-critical decoration; leave the last snapshot up */
    }
  }, [workspaceId, window])

  const fetchLive = useCallback(async () => {
    if (!workspaceId) return
    try {
      const params = new URLSearchParams({
        workspace_id: workspaceId,
        status: "RUNNING",
        limit: String(LIVE_LIMIT),
      })
      const res = await apiFetch(`/api/v1/runs?${params}`)
      if (res.ok) {
        const result = (await res.json()) as RunsResponse
        setLiveRuns(result.data ?? [])
      }
    } catch {
      /* non-critical */
    }
  }, [workspaceId])

  useEffect(() => {
    if (!workspaceId) {
      if (!workspaceLoading) setLoading(false)
      return
    }
    fetchRuns()
  }, [workspaceId, workspaceLoading, fetchRuns])

  useEffect(() => {
    fetchInsights()
  }, [fetchInsights])

  useEffect(() => {
    fetchLive()
  }, [fetchLive])

  // Reset page when filters change
  useEffect(() => {
    setPage(1)
  }, [statusFilter, triggerFilter])

  // Real-time refetch on run events. Backend collapses terminal statuses
  // into run.completed / run.failed; subscribing to these three covers the
  // lifecycle. Refresh table, live strip and aggregates together.
  const silentRefetch = useCallback(() => {
    fetchRuns({ silent: true })
    fetchInsights()
    fetchLive()
  }, [fetchRuns, fetchInsights, fetchLive])
  useRealtimeEvent("run.started", silentRefetch)
  useRealtimeEvent("run.completed", silentRefetch)
  useRealtimeEvent("run.failed", silentRefetch)

  const isLoading = workspaceLoading || loading

  const runs = data?.data ?? []
  const total = data?.pagination.total ?? 0
  const totalPages = data?.pagination.total_pages ?? 1

  const rangeStart = runs.length > 0 ? (page - 1) * PAGE_SIZE + 1 : 0
  const rangeEnd = Math.min(page * PAGE_SIZE, total)

  const t = insights?.totals
  const rate = t ? successRate(t.succeeded, t.failed) : null

  return (
    <div className="h-full overflow-y-auto">
      <div className="p-4 md:p-6 space-y-4">
        {/* ── Local header (window + refresh) ──────────────────────── */}
        <div className="flex items-center justify-between gap-3 flex-wrap">
          <div className="flex items-center gap-2">
            <Activity className="h-3.5 w-3.5 text-foreground/50" />
            <span className="text-[10px] font-mono text-muted-foreground/60">
              {t && t.total > 0
                ? `${t.total.toLocaleString()} runs · ${WINDOW_LABEL[window]}`
                : total > 0
                  ? `${total.toLocaleString()} total runs`
                  : "no runs yet"}
            </span>
          </div>
          <div className="flex items-center gap-2">
            <div className="inline-flex rounded border border-border/60 bg-card overflow-hidden">
              {RUN_WINDOWS.map((wv) => (
                <button
                  key={wv}
                  type="button"
                  onClick={() => setWindow(wv)}
                  aria-pressed={window === wv}
                  className={cn(
                    "h-7 px-2.5 text-[10px] font-mono uppercase tracking-wider transition-colors border-r border-border/60 last:border-r-0",
                    window === wv ? "bg-blue-500/15 text-blue-300" : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {wv}
                </button>
              ))}
            </div>
            <Button
              variant="outline"
              size="sm"
              className="h-7 px-2.5 text-xs"
              onClick={silentRefetch}
              disabled={isLoading || refreshing}
            >
              <RefreshCw className={cn("h-3 w-3 mr-1.5", refreshing && "animate-spin")} />
              Refresh
            </Button>
          </div>
        </div>

        {/* ── 1. Live pulse strip ──────────────────────────────────── */}
        <LivePulse runs={liveRuns} runningCount={insights?.totals.running ?? liveRuns.length} />

        {/* ── 2. KPI row ───────────────────────────────────────────── */}
        <div className="grid gap-4 grid-cols-2 md:grid-cols-4">
          <RunsKpiTile
            label={`Runs (${window})`}
            value={t ? t.total.toLocaleString() : "—"}
            icon={Zap}
            iconTone="bg-blue-500/20 text-blue-400"
            split={t ? { ok: t.succeeded, failed: t.failed } : undefined}
            sub={t ? `${t.succeeded.toLocaleString()} ok · ${t.failed.toLocaleString()} failed` : undefined}
          />
          <RunsKpiTile
            label="Success"
            value={rate === null ? "—" : `${rate}%`}
            valueColor={successRateColor(rate)}
            icon={CheckCircle}
            iconTone="bg-emerald-500/20 text-emerald-400"
            sub={t && t.running > 0 ? `${t.running} running now` : "of completed runs"}
          />
          <RunsKpiTile
            label="Failed"
            value={t ? t.failed.toLocaleString() : "—"}
            valueColor={t && t.failed > 0 ? "rgb(248, 113, 113)" : undefined}
            icon={XCircle}
            iconTone="bg-rose-500/20 text-rose-400"
            sub={t && t.failed > 0 ? "needs attention" : "all clean"}
          />
          <RunsKpiTile
            label="Median duration"
            value={insights && insights.duration.p50_ms > 0 ? formatDurationMillis(insights.duration.p50_ms) : "—"}
            icon={Clock}
            iconTone="bg-violet-500/20 text-violet-400"
            sub={
              insights && insights.duration.p95_ms > 0
                ? `p95 ${formatDurationMillis(insights.duration.p95_ms)}`
                : "no finished runs"
            }
          />
        </div>

        {/* ── 3. Breakdown row ─────────────────────────────────────── */}
        <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
          <BreakdownCard
            title="By trigger"
            icon={Zap}
            hint="where runs originate"
            rows={(insights?.by_trigger ?? []).map((c) => ({
              key: c.key,
              label: triggerLabel(c.key),
              total: c.total,
              failed: c.failed,
            }))}
            barClass="bg-cyan-400/70"
          />
          <TopCrewsCard crews={insights?.by_crew ?? []} />
          <BreakdownCard
            title="By model"
            icon={Activity}
            hint="plan / cost signal"
            rows={(insights?.by_model ?? []).map((c) => ({
              key: c.key,
              label: shortModel(c.key),
              total: c.total,
              failed: c.failed,
            }))}
            barClass="bg-indigo-400/70"
          />
        </div>

        {insights?.truncated && (
          <div className="text-[10px] text-muted-foreground/60 font-mono">
            aggregates cover the most recent runs in this window (cap reached)
          </div>
        )}

        {/* ── 4. Recent runs — filters ─────────────────────────────── */}
        <div className="flex items-center gap-2 flex-wrap">
          <div className="inline-flex rounded border border-border/60 bg-card overflow-hidden">
            {(["all", "RUNNING", "COMPLETED", "FAILED", "CANCELLED", "TIMEOUT"] as const).map((s) => {
              const active = statusFilter === s
              const label = s === "all" ? "All" : s.charAt(0) + s.slice(1).toLowerCase()
              return (
                <button
                  key={s}
                  type="button"
                  onClick={() => setStatusFilter(s)}
                  aria-pressed={active}
                  className={cn(
                    "h-7 px-2.5 text-[10px] font-mono uppercase tracking-wider flex items-center gap-1 transition-colors border-r border-border/60 last:border-r-0",
                    active
                      ? s === "FAILED"
                        ? "bg-red-500/15 text-red-300"
                        : s === "RUNNING"
                          ? "bg-emerald-500/15 text-emerald-300"
                          : s === "COMPLETED"
                            ? "bg-sky-500/15 text-sky-300"
                            : s === "TIMEOUT"
                              ? "bg-amber-500/15 text-amber-300"
                              : s === "CANCELLED"
                                ? "bg-muted text-foreground"
                                : "bg-primary/15 text-primary-hover"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {label}
                </button>
              )
            })}
          </div>
          <Select value={triggerFilter} onValueChange={setTriggerFilter}>
            <SelectTrigger className="h-7 w-[140px] text-xs">
              <SelectValue placeholder="All triggers" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all" className="text-xs">All triggers</SelectItem>
              <SelectItem value="USER" className="text-xs">User</SelectItem>
              <SelectItem value="WEBHOOK" className="text-xs">Webhook</SelectItem>
              <SelectItem value="CRON" className="text-xs">Schedule</SelectItem>
              <SelectItem value="AGENT" className="text-xs">Agent</SelectItem>
              <SelectItem value="SYSTEM" className="text-xs">System</SelectItem>
            </SelectContent>
          </Select>
          <span className="ml-auto text-[10px] font-mono text-muted-foreground/50 hidden sm:inline">
            click a row → open trace
          </span>
        </div>

        {error && (
          <div className="text-[11px] text-destructive px-3 py-2 rounded-md border border-destructive/30 bg-destructive/5">
            {error}
          </div>
        )}

        {/* ── Recent runs — table ──────────────────────────────────── */}
        {isLoading ? (
          <div className="flex flex-col gap-1.5">
            {Array.from({ length: 8 }).map((_, i) => (
              <Skeleton key={i} className="h-9 rounded-md" />
            ))}
          </div>
        ) : runs.length === 0 ? (
          <SettingsCard
            title="Runs"
            description={
              statusFilter !== "all" || triggerFilter !== "all"
                ? "No runs match the current filters"
                : "Agent runs will appear here once agents start executing tasks"
            }
          >
            <div className="flex flex-col items-center justify-center py-12 text-center">
              <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center mb-3">
                <Activity className="h-4 w-4 text-muted-foreground/60" />
              </div>
              <div className="text-sm font-medium text-foreground/80">No runs yet</div>
              <div className="text-[11px] text-muted-foreground mt-0.5 max-w-xs">
                Agent executions across all crews will show up here in real time.
              </div>
            </div>
          </SettingsCard>
        ) : (
          <>
            <div className="rounded-xl border border-border/60 bg-card overflow-hidden">
              {/* Desktop header */}
              <div
                className="hidden md:grid items-center gap-3 px-4 py-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/60 border-b border-border/60"
                style={{ gridTemplateColumns: RUN_GRID }}
              >
                <div>Run</div>
                <div>Agent</div>
                <div>Crew</div>
                <div>Status</div>
                <div>Trigger</div>
                <div>Model</div>
                <div className="text-right">Duration</div>
                <div>Started</div>
                <div />
              </div>

              {runs.map((run, idx) => {
                const canonicalStatus = toCanonicalStatus(run.status)
                const isRunning = run.status === "RUNNING"
                const StatusIcon =
                  run.status === "COMPLETED"
                    ? CheckCircle
                    : run.status === "FAILED"
                      ? XCircle
                      : run.status === "TIMEOUT"
                        ? AlertTriangle
                        : run.status === "CANCELLED"
                          ? XCircle
                          : Play
                const traceHref = `/journal?tab=timeline&trace_id=${encodeURIComponent(run.id)}`
                return (
                  <div
                    key={run.id}
                    role="button"
                    tabIndex={0}
                    data-row-root
                    onClick={(e) => {
                      if (isFromInteractiveChild(e.target)) return
                      router.push(traceHref)
                    }}
                    onKeyDown={(e) => {
                      if (e.key !== "Enter" && e.key !== " ") return
                      if (isFromInteractiveChild(e.target)) return
                      e.preventDefault()
                      router.push(traceHref)
                    }}
                    title={`Open trace ${run.id.slice(0, 8)} in Timeline`}
                    className={cn(
                      "grid items-center gap-3 px-4 py-2 hover:bg-white/[0.02] transition-colors cursor-pointer outline-none focus-visible:bg-white/[0.04] focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-emerald-500/40",
                      idx < runs.length - 1 && "border-b border-border/40",
                    )}
                    style={{ gridTemplateColumns: RUN_GRID }}
                  >
                    <span className="text-[10px] font-mono text-muted-foreground/60">
                      #{run.id.slice(0, 8)}
                    </span>
                    <Link
                      href={`/crews/agents/${run.agent_id}`}
                      onClick={(e) => e.stopPropagation()}
                      className="text-xs font-medium truncate hover:underline"
                    >
                      {run.agent_name ?? <span className="text-muted-foreground/60">Unknown</span>}
                    </Link>
                    <span className="text-[11px] text-muted-foreground truncate">
                      {run.crew_name ?? <span className="text-muted-foreground/40">—</span>}
                    </span>
                    <div>
                      <StatusBadge
                        status={canonicalStatus}
                        label={
                          <span className="inline-flex items-center gap-1">
                            {isRunning ? (
                              <StatusDot status="IN_PROGRESS" live className="h-1.5 w-1.5" />
                            ) : (
                              <StatusIcon className="h-2.5 w-2.5" />
                            )}
                            {statusLabel(run.status)}
                          </span>
                        }
                        className="text-[10px]"
                      />
                    </div>
                    <span className="text-[11px] text-muted-foreground truncate">
                      {triggerLabel(run.trigger_type)}
                    </span>
                    <span className="text-[10px] font-mono text-indigo-300/80 truncate">
                      {run.model ? shortModel(run.model) : <span className="text-muted-foreground/40">—</span>}
                    </span>
                    <span className="text-[11px] font-mono tabular-nums text-muted-foreground text-right">
                      {isRunning && run.started_at ? (
                        <LiveRunDuration startedAt={run.started_at} />
                      ) : (
                        formatDurationBetween(run.started_at, run.finished_at)
                      )}
                    </span>
                    <span className="text-[11px] text-muted-foreground truncate">
                      {formatRelativeShort(run.started_at ?? run.created_at)}
                    </span>
                    <Link
                      href={`/crews/agents/${run.agent_id}`}
                      onClick={(e) => e.stopPropagation()}
                      className="text-muted-foreground/60 hover:text-foreground transition-colors justify-self-end"
                      aria-label="Open agent"
                    >
                      <ExternalLink className="h-3 w-3" />
                    </Link>
                  </div>
                )
              })}
            </div>

            {/* Pagination */}
            {totalPages > 1 && (
              <div className="flex items-center justify-between gap-2 flex-wrap">
                <span className="text-[11px] text-muted-foreground font-mono tabular-nums">
                  Showing {rangeStart}–{rangeEnd} of {total.toLocaleString()}
                </span>
                <div className="flex items-center gap-1.5">
                  <Button
                    variant="outline"
                    size="sm"
                    className="h-7 px-2 text-xs"
                    onClick={() => setPage((p) => Math.max(1, p - 1))}
                    disabled={page === 1 || isLoading}
                  >
                    <ChevronLeft className="h-3 w-3 mr-1" />
                    Previous
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    className="h-7 px-2 text-xs"
                    onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
                    disabled={page >= totalPages || isLoading}
                  >
                    Next
                    <ChevronRight className="h-3 w-3 ml-1" />
                  </Button>
                </div>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}

// Shared grid template so header + rows stay aligned (adds a Model column
// vs the legacy list).
const RUN_GRID =
  "80px minmax(0,1.2fr) minmax(0,1fr) 108px 84px 84px 80px minmax(0,0.9fr) 20px"

/* ----------------------------------------------------------------- *
 *  Live pulse strip                                                  *
 * ----------------------------------------------------------------- */
function LivePulse({ runs, runningCount }: { runs: Run[]; runningCount: number }) {
  const router = useRouter()
  if (runningCount <= 0 && runs.length === 0) {
    return (
      <div className="rounded-xl border border-border/60 bg-card px-4 py-3 flex items-center gap-2">
        <span className="h-2 w-2 rounded-full bg-muted-foreground/30" />
        <span className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground/70">
          Running now
        </span>
        <span className="text-[10px] font-mono text-muted-foreground/50">fleet idle</span>
      </div>
    )
  }
  return (
    <div className="rounded-xl border border-emerald-500/25 bg-card overflow-hidden">
      <div className="flex items-center gap-2 border-b border-border/40 px-4 py-2">
        <StatusDot status="IN_PROGRESS" live className="h-2 w-2" />
        <span className="text-[11px] font-semibold uppercase tracking-wider text-foreground/85">
          Running now
        </span>
        <span className="text-[10px] font-mono text-muted-foreground/60">
          {runningCount} live {runningCount === 1 ? "execution" : "executions"}
        </span>
        <span className="ml-auto text-[10px] text-muted-foreground/40">auto-updating</span>
      </div>
      <ul className="divide-y divide-border/40">
        {runs.map((run) => {
          const traceHref = `/journal?tab=timeline&trace_id=${encodeURIComponent(run.id)}`
          return (
            <li
              key={run.id}
              role="button"
              tabIndex={0}
              onClick={() => router.push(traceHref)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault()
                  router.push(traceHref)
                }
              }}
              className="flex items-center gap-3 px-4 py-2 hover:bg-white/[0.02] cursor-pointer outline-none focus-visible:bg-white/[0.04]"
            >
              <StatusDot status="IN_PROGRESS" live className="h-1.5 w-1.5" />
              <span className="text-xs font-medium truncate w-40">
                {run.agent_name ?? "Unknown"}
              </span>
              <span className="text-[11px] text-muted-foreground truncate w-28 hidden sm:block">
                {run.crew_name ?? "—"}
              </span>
              <span className="text-[10px] font-mono text-cyan-300/80 hidden md:block">
                {triggerLabel(run.trigger_type)}
              </span>
              {run.model && (
                <span className="shrink-0 rounded border border-indigo-500/40 px-1 py-0 text-[9px] text-indigo-300 hidden md:block">
                  {shortModel(run.model)}
                </span>
              )}
              <span className="ml-auto font-mono tabular-nums text-[11px] text-emerald-300">
                {run.started_at ? <LiveRunDuration startedAt={run.started_at} /> : "running"}
              </span>
            </li>
          )
        })}
      </ul>
    </div>
  )
}

/* ----------------------------------------------------------------- *
 *  KPI tile (with optional outcome split-bar)                       *
 * ----------------------------------------------------------------- */
function RunsKpiTile({
  label,
  value,
  valueColor,
  sub,
  icon: Icon,
  iconTone,
  split,
}: {
  label: string
  value: string
  valueColor?: string
  sub?: string
  icon: typeof Zap
  iconTone: string
  split?: { ok: number; failed: number }
}) {
  const splitTotal = split ? split.ok + split.failed : 0
  const okPct = splitTotal > 0 ? (split!.ok / splitTotal) * 100 : 0
  return (
    <div className="flex flex-col gap-1 rounded-xl border border-border/60 bg-card px-4 py-4">
      <div className="flex items-center justify-between">
        <div className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">{label}</div>
        <div className={cn("flex h-6 w-6 items-center justify-center rounded-md", iconTone)}>
          <Icon className="h-3.5 w-3.5" />
        </div>
      </div>
      <div
        className="mt-1 text-[28px] sm:text-[32px] font-semibold leading-none tabular-nums"
        style={valueColor ? { color: valueColor } : undefined}
      >
        {value}
      </div>
      {split && splitTotal > 0 && (
        <div className="mt-2 flex h-1.5 w-full overflow-hidden rounded-full bg-white/[0.06]">
          <span className="bg-emerald-400/80" style={{ width: `${okPct}%` }} />
          <span className="bg-rose-400/80" style={{ width: `${100 - okPct}%` }} />
        </div>
      )}
      {sub && <div className="mt-1 text-[11px] text-muted-foreground">{sub}</div>}
    </div>
  )
}

/* ----------------------------------------------------------------- *
 *  Breakdown card (horizontal bars)                                 *
 * ----------------------------------------------------------------- */
interface BreakdownRow {
  key: string
  label: string
  total: number
  failed: number
}

function BreakdownCard({
  title,
  icon: Icon,
  hint,
  rows,
  barClass,
}: {
  title: string
  icon: typeof Zap
  hint: string
  rows: BreakdownRow[]
  barClass: string
}) {
  const max = maxTotal(rows)
  return (
    <div className="rounded-xl border border-border/60 bg-card overflow-hidden">
      <div className="flex items-center gap-2 border-b border-border/40 px-4 py-2.5">
        <Icon className="h-3.5 w-3.5 text-muted-foreground/60" />
        <span className="text-[11px] font-semibold uppercase tracking-wider">{title}</span>
        <span className="ml-auto font-mono text-[10px] text-muted-foreground/60">{hint}</span>
      </div>
      {rows.length === 0 ? (
        <div className="px-4 py-6 text-center text-[11px] text-muted-foreground/60">No data yet</div>
      ) : (
        <ul className="p-3 space-y-2.5">
          {rows.slice(0, 6).map((r) => (
            <li key={r.key}>
              <div className="flex items-center gap-2 text-[11px] mb-1">
                <span className="truncate">{r.label}</span>
                <span className="ml-auto font-mono tabular-nums text-muted-foreground">
                  {r.total.toLocaleString()}
                  {r.failed > 0 && <span className="text-rose-400/80"> · {r.failed} fail</span>}
                </span>
              </div>
              <div className="h-1.5 w-full overflow-hidden rounded-full bg-white/[0.06]">
                <span className={cn("block h-full rounded-full", barClass)} style={{ width: `${barPercent(r.total, max)}%` }} />
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/* ----------------------------------------------------------------- *
 *  Top crews card (volume + fail rate)                              *
 * ----------------------------------------------------------------- */
function TopCrewsCard({ crews }: { crews: RunInsights["by_crew"] }) {
  return (
    <div className="rounded-xl border border-border/60 bg-card overflow-hidden">
      <div className="flex items-center gap-2 border-b border-border/40 px-4 py-2.5">
        <Users className="h-3.5 w-3.5 text-muted-foreground/60" />
        <span className="text-[11px] font-semibold uppercase tracking-wider">Top crews</span>
        <span className="ml-auto font-mono text-[10px] text-muted-foreground/60">volume · fail%</span>
      </div>
      {crews.length === 0 ? (
        <div className="px-4 py-6 text-center text-[11px] text-muted-foreground/60">No data yet</div>
      ) : (
        <ul className="divide-y divide-border/40">
          {crews.slice(0, 6).map((c) => {
            const fr = failRate(c.total, c.failed)
            return (
              <li key={c.id || c.name} className="flex items-center gap-2.5 px-4 py-2.5">
                <span className={cn("h-1.5 w-1.5 rounded-full", fr >= 15 ? "bg-rose-500" : "bg-emerald-500")} />
                <span className="flex-1 truncate text-sm">{c.name}</span>
                <span className="font-mono tabular-nums text-[12px] text-foreground/80">{c.total.toLocaleString()}</span>
                <span className={cn("font-mono tabular-nums text-[11px] w-10 text-right", fr >= 15 ? "text-rose-400" : "text-emerald-400")}>
                  {fr}%
                </span>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
