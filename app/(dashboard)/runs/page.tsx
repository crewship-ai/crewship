"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  Activity, AlertTriangle, CheckCircle, ChevronLeft, ChevronRight, ExternalLink,
  Play, RefreshCw, XCircle,
} from "lucide-react"
import Link from "next/link"

import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import { StatusBadge, StatusDot } from "@/components/ui/status-badge"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { SettingsCard } from "@/components/features/settings/shared"
import { cn } from "@/lib/utils"

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

// Map run API status → canonical status used in STATUS_BADGE_CLASSES.
function toCanonicalStatus(status: string): string {
  switch (status) {
    case "RUNNING": return "IN_PROGRESS"
    case "TIMEOUT": return "FAILED"
    default:        return status
  }
}

function statusLabel(status: string): string {
  switch (status) {
    case "PENDING":   return "Pending"
    case "RUNNING":   return "Running"
    case "COMPLETED": return "Completed"
    case "FAILED":    return "Failed"
    case "CANCELLED": return "Cancelled"
    case "TIMEOUT":   return "Timeout"
    default:          return status
  }
}

function formatDuration(start: string | null, end: string | null): string {
  if (!start) return "—"
  const startDate = new Date(start)
  const endDate = end ? new Date(end) : new Date()
  const seconds = Math.floor((endDate.getTime() - startDate.getTime()) / 1000)
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
  return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`
}

function LiveRunDuration({ startedAt }: { startedAt: string }) {
  const [, setTick] = useState(0)
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), 1000)
    return () => clearInterval(id)
  }, [])
  return <>{formatDuration(startedAt, null)}</>
}

function formatRelativeShort(iso: string | null | undefined): string {
  if (!iso) return "—"
  const ts = new Date(iso).getTime()
  if (isNaN(ts)) return "—"
  const diffSec = Math.floor((Date.now() - ts) / 1000)
  if (diffSec < 60) return `${diffSec}s ago`
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`
  return `${Math.floor(diffSec / 86400)}d ago`
}

export default function RunsPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
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
      else { setLoading(true); setError(null) }
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
      if (!wsLoading) setLoading(false)
      return
    }
    fetchRuns()
  }, [workspaceId, wsLoading, fetchRuns])

  // Reset page when filters change
  useEffect(() => {
    setPage(1)
  }, [statusFilter, triggerFilter])

  // Real-time refetch on run events
  const silentRefetch = useCallback(() => { fetchRuns({ silent: true }) }, [fetchRuns])
  useRealtimeEvent("run.started", silentRefetch)
  useRealtimeEvent("run.completed", silentRefetch)
  useRealtimeEvent("run.failed", silentRefetch)

  const isLoading = wsLoading || loading

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
    <div className="p-4 md:p-6 pb-10 space-y-4 bg-background min-h-[calc(100vh-48px)]">
      {/* ── Header ─────────────────────────────────────────────── */}
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <div className="flex items-center gap-2">
          <Activity className="h-3.5 w-3.5 text-foreground/50" />
          <h1 className="text-body font-medium text-foreground/80">Runs</h1>
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
        <KpiCard
          label="Today"
          value={stats?.today ?? 0}
          subtitle="last 24 hours"
        />
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
            !stats || stats.today === 0 ? undefined
              : stats.failed === 0 ? "rgb(52, 211, 153)"
              : Math.round(((stats.today - stats.failed) / stats.today) * 100) >= 90 ? "rgb(52, 211, 153)"
              : "rgb(251, 191, 36)"
          }
          subtitle={stats && stats.today === 0 ? "no data" : "passing ratio"}
        />
      </div>

      {/* ── Filters ────────────────────────────────────────────── */}
      <div className="flex items-center gap-2 flex-wrap">
        <Select value={statusFilter} onValueChange={setStatusFilter}>
          <SelectTrigger className="h-7 w-[140px] text-xs">
            <SelectValue placeholder="All statuses" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all" className="text-xs">All statuses</SelectItem>
            <SelectItem value="RUNNING" className="text-xs">Running</SelectItem>
            <SelectItem value="COMPLETED" className="text-xs">Completed</SelectItem>
            <SelectItem value="FAILED" className="text-xs">Failed</SelectItem>
            <SelectItem value="CANCELLED" className="text-xs">Cancelled</SelectItem>
            <SelectItem value="TIMEOUT" className="text-xs">Timeout</SelectItem>
            <SelectItem value="PENDING" className="text-xs">Pending</SelectItem>
          </SelectContent>
        </Select>
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
          description={statusFilter !== "all" || triggerFilter !== "all" ? "No runs match the current filters" : "Agent runs will appear here once agents start executing tasks"}
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
              style={{ gridTemplateColumns: "80px minmax(0,1.2fr) minmax(0,1fr) 110px 90px 80px minmax(0,1fr) 20px" }}
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
                run.status === "COMPLETED" ? CheckCircle
                  : run.status === "FAILED" ? XCircle
                  : run.status === "TIMEOUT" ? AlertTriangle
                  : run.status === "CANCELLED" ? XCircle
                  : Play
              return (
                <div
                  key={run.id}
                  className={cn(
                    "grid items-center gap-3 px-4 py-2 hover:bg-white/[0.02] transition-colors",
                    idx < runs.length - 1 && "border-b border-border/40",
                  )}
                  style={{ gridTemplateColumns: "80px minmax(0,1.2fr) minmax(0,1fr) 110px 90px 80px minmax(0,1fr) 20px" }}
                >
                  <span className="text-[10px] font-mono text-muted-foreground/60">
                    #{run.id.slice(0, 8)}
                  </span>
                  <Link
                    href={`/fleet/agents/${run.agent_id}`}
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
                    {run.trigger_type}
                  </span>
                  <span className="text-[11px] font-mono tabular-nums text-muted-foreground text-right">
                    {isRunning && run.started_at
                      ? <LiveRunDuration startedAt={run.started_at} />
                      : formatDuration(run.started_at, run.finished_at)}
                  </span>
                  <span className="text-[11px] text-muted-foreground truncate">
                    {formatRelativeShort(run.started_at ?? run.created_at)}
                  </span>
                  <Link
                    href={`/fleet/agents/${run.agent_id}`}
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
