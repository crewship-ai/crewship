"use client"

// RunsView — the Runs tab inside /journal. Renders the same KPI strip,
// filters, table and pagination as the legacy /runs page. The legacy
// page is being replaced by a redirect to /journal?tab=runs (Phase F
// of unified-journal); the table itself is unchanged so users see
// identical content under the new home.
//
// Data source: /api/v1/runs (which now reads from journal_entries
// grouped by trace_id, see Phase E). Real-time updates piggy-back on
// the same run.* WebSocket events the legacy page listened to.

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  Activity,
  AlertTriangle,
  CheckCircle,
  ChevronLeft,
  ChevronRight,
  ExternalLink,
  Play,
  RefreshCw,
  XCircle,
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
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { SettingsCard } from "@/components/features/settings/shared"
import { cn } from "@/lib/utils"
import {
  formatDuration,
  formatRelativeShort,
  statusLabel,
  toCanonicalStatus,
} from "@/lib/runs-format"

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

function LiveRunDuration({ startedAt }: { startedAt: string }) {
  const [, setTick] = useState(0)
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), 1000)
    return () => clearInterval(id)
  }, [])
  return <>{formatDuration(startedAt, null)}</>
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
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [statusFilter, setStatusFilter] = useState("all")
  const [triggerFilter, setTriggerFilter] = useState("all")
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

        const res = await fetch(`/api/v1/runs?${params}`)
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

  useEffect(() => {
    if (!workspaceId) {
      if (!workspaceLoading) setLoading(false)
      return
    }
    fetchRuns()
  }, [workspaceId, workspaceLoading, fetchRuns])

  // Reset page when filters change
  useEffect(() => {
    setPage(1)
  }, [statusFilter, triggerFilter])

  // Real-time refetch on run events. Backend collapses every terminal
  // status into one of two WS event types: COMPLETED → "run.completed",
  // and FAILED / CANCELLED / TIMEOUT → "run.failed" (see
  // internal_runs.go broadcastWorkspaceEvent). So subscribing to these
  // three covers the full lifecycle — there's no separate
  // "run.cancelled" or "run.timeout" event to subscribe to today.
  const silentRefetch = useCallback(() => {
    fetchRuns({ silent: true })
  }, [fetchRuns])
  useRealtimeEvent("run.started", silentRefetch)
  useRealtimeEvent("run.completed", silentRefetch)
  useRealtimeEvent("run.failed", silentRefetch)

  const isLoading = workspaceLoading || loading

  const runs = data?.data ?? []
  const stats = data?.stats
  const total = data?.pagination.total ?? 0
  const totalPages = data?.pagination.total_pages ?? 1

  const rangeStart = runs.length > 0 ? (page - 1) * PAGE_SIZE + 1 : 0
  const rangeEnd = Math.min(page * PAGE_SIZE, total)

  const successRateDisplay = useMemo(() => {
    if (!stats || stats.today === 0) return "—"
    const pct = Math.round(((stats.today - stats.failed) / stats.today) * 100)
    return `${pct}%`
  }, [stats])

  return (
    <div className="space-y-4">
      {/* ── Local header (refresh + count) ───────────────────────── */}
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <div className="flex items-center gap-2">
          <Activity className="h-3.5 w-3.5 text-foreground/50" />
          <span className="text-[10px] font-mono text-muted-foreground/60">
            {total > 0 ? `${total.toLocaleString()} total` : "no runs yet"}
          </span>
        </div>
        <Button
          variant="outline"
          size="sm"
          className="h-7 px-2.5 text-xs"
          onClick={() => fetchRuns({ silent: true })}
          disabled={isLoading || refreshing}
        >
          <RefreshCw className={cn("h-3 w-3 mr-1.5", refreshing && "animate-spin")} />
          Refresh
        </Button>
      </div>

      {/* ── KPI strip ──────────────────────────────────────────── */}
      <div className="grid gap-4 grid-cols-2 sm:grid-cols-4">
        <KpiCard
          label="Running now"
          value={stats?.running ?? 0}
          valueColor={stats && stats.running > 0 ? "rgb(52, 211, 153)" : undefined}
          subtitle="live executions"
        />
        <KpiCard label="Today" value={stats?.today ?? 0} subtitle="last 24 hours" />
        <KpiCard
          label="Failed (24h)"
          value={stats?.failed ?? 0}
          valueColor={stats && stats.failed > 0 ? "rgb(248, 113, 113)" : undefined}
          subtitle={stats && stats.failed > 0 ? "needs attention" : "all clean"}
        />
        <KpiCard
          label="Success (24h)"
          value={successRateDisplay}
          valueColor={
            !stats || stats.today === 0
              ? undefined
              : stats.failed === 0
                ? "rgb(52, 211, 153)"
                : Math.round(((stats.today - stats.failed) / stats.today) * 100) >= 90
                  ? "rgb(52, 211, 153)"
                  : "rgb(251, 191, 36)"
          }
          subtitle={stats && stats.today === 0 ? "no data" : "passing ratio"}
        />
      </div>

      {/* ── Status chips ───────────────────────────────────────── */}
      {/* Replaces the legacy two-Select filter row with horizontal */}
      {/* chips that show the count next to each status. Visual */}
      {/* parity with the Timeline severity row keeps the two */}
      {/* surfaces feeling like one product. Trigger filter stays */}
      {/* as a Select — its values are noisier and rarely used. */}
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
            <SelectItem value="all" className="text-xs">
              All triggers
            </SelectItem>
            <SelectItem value="USER" className="text-xs">
              User
            </SelectItem>
            <SelectItem value="WEBHOOK" className="text-xs">
              Webhook
            </SelectItem>
            <SelectItem value="CRON" className="text-xs">
              Schedule
            </SelectItem>
            <SelectItem value="AGENT" className="text-xs">
              Agent
            </SelectItem>
            <SelectItem value="SYSTEM" className="text-xs">
              System
            </SelectItem>
          </SelectContent>
        </Select>
      </div>

      {error && (
        <div className="text-[11px] text-destructive px-3 py-2 rounded-md border border-destructive/30 bg-destructive/5">
          {error}
        </div>
      )}

      {/* ── Content ────────────────────────────────────────────── */}
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
              style={{
                gridTemplateColumns:
                  "80px minmax(0,1.2fr) minmax(0,1fr) 110px 90px 80px minmax(0,1fr) 20px",
              }}
            >
              <div>Run</div>
              <div>Agent</div>
              <div>Crew</div>
              <div>Status</div>
              <div>Trigger</div>
              <div className="text-right">Duration</div>
              <div>Started</div>
              <div />
            </div>

            {/* Rows */}
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
                  style={{
                    gridTemplateColumns:
                      "80px minmax(0,1.2fr) minmax(0,1fr) 110px 90px 80px minmax(0,1fr) 20px",
                  }}
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
                  <span className="text-[11px] text-muted-foreground truncate">{run.trigger_type}</span>
                  <span className="text-[11px] font-mono tabular-nums text-muted-foreground text-right">
                    {isRunning && run.started_at ? (
                      <LiveRunDuration startedAt={run.started_at} />
                    ) : (
                      formatDuration(run.started_at, run.finished_at)
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
  )
}
