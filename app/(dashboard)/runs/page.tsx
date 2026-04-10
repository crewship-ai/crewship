"use client"

import { useCallback, useEffect, useState } from "react"
import {
  Activity,
  AlertTriangle,
  CheckCircle,
  ExternalLink,
  ListOrdered,
  Play,
  XCircle,
} from "lucide-react"
import { RocketIcon as AnimatedRocket } from "@/components/ui/rocket"
import { Card, CardContent } from "@/components/ui/card"
import { PageShell } from "@/components/layout/page-shell"
import { EmptyState } from "@/components/layout/empty-state"
import { StatusBadge, StatusDot } from "@/components/ui/status-badge"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import Link from "next/link"

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

// Map run API status → canonical status used in lib/colors STATUS_BADGE_CLASSES.
function toCanonicalStatus(status: string): string {
  switch (status) {
    case "RUNNING":
      return "IN_PROGRESS"
    case "TIMEOUT":
      return "FAILED"
    default:
      return status
  }
}

function statusLabel(status: string): string {
  switch (status) {
    case "PENDING":
      return "Pending"
    case "RUNNING":
      return "Running"
    case "COMPLETED":
      return "Completed"
    case "FAILED":
      return "Failed"
    case "CANCELLED":
      return "Cancelled"
    case "TIMEOUT":
      return "Timeout"
    default:
      return status
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

export default function RunsPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [data, setData] = useState<RunsResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [statusFilter, setStatusFilter] = useState("all")
  const [triggerFilter, setTriggerFilter] = useState("all")

  const fetchRuns = useCallback(async (showLoading = true) => {
    if (!workspaceId) return
    if (showLoading) {
      setLoading(true)
      setError(null)
    }
    try {
      const params = new URLSearchParams({ workspace_id: workspaceId })
      if (statusFilter !== "all") params.set("status", statusFilter)
      if (triggerFilter !== "all") params.set("trigger", triggerFilter)

      const res = await fetch(`/api/v1/runs?${params}`)
      if (!res.ok) {
        if (showLoading) setError("Failed to load runs")
        return
      }
      const result = (await res.json()) as RunsResponse
      setData(result)
    } catch {
      if (showLoading) setError("Failed to load runs")
    } finally {
      if (showLoading) setLoading(false)
    }
  }, [workspaceId, statusFilter, triggerFilter])

  useEffect(() => {
    if (!workspaceId) {
      if (!wsLoading) setLoading(false)
      return
    }
    fetchRuns()
  }, [workspaceId, wsLoading, fetchRuns])

  // Real-time: refetch runs when run events arrive
  useRealtimeEvent("run.started", useCallback(() => { fetchRuns(false) }, [fetchRuns]))
  useRealtimeEvent("run.completed", useCallback(() => { fetchRuns(false) }, [fetchRuns]))
  useRealtimeEvent("run.failed", useCallback(() => { fetchRuns(false) }, [fetchRuns]))

  const isLoading = wsLoading || loading

  const stats = data
    ? [
        {
          title: "Running Now",
          value: data.stats.running,
          subtitle: "live executions",
          icon: Play,
          iconClassName: "bg-primary/10 text-primary",
        },
        {
          title: "Today's Runs",
          value: data.stats.today,
          subtitle: "last 24 hours",
          icon: Activity,
          iconClassName: "bg-muted text-muted-foreground",
        },
        {
          title: "Failed",
          value: data.stats.failed,
          subtitle: "needs attention",
          icon: XCircle,
          iconClassName: "bg-destructive/10 text-destructive",
        },
        {
          title: "Total",
          value: data.pagination.total,
          subtitle: "across workspace",
          icon: ListOrdered,
          iconClassName: "bg-muted text-muted-foreground",
        },
      ]
    : undefined

  const filters = (
    <div className="flex items-center gap-3 flex-wrap">
      <Select value={statusFilter} onValueChange={setStatusFilter}>
        <SelectTrigger className="w-[140px]">
          <SelectValue placeholder="All Status" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all">All Status</SelectItem>
          <SelectItem value="RUNNING">Running</SelectItem>
          <SelectItem value="COMPLETED">Completed</SelectItem>
          <SelectItem value="FAILED">Failed</SelectItem>
          <SelectItem value="CANCELLED">Cancelled</SelectItem>
          <SelectItem value="TIMEOUT">Timeout</SelectItem>
          <SelectItem value="PENDING">Pending</SelectItem>
        </SelectContent>
      </Select>
      <Select value={triggerFilter} onValueChange={setTriggerFilter}>
        <SelectTrigger className="w-[140px]">
          <SelectValue placeholder="All Triggers" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all">All Triggers</SelectItem>
          <SelectItem value="USER">User</SelectItem>
          <SelectItem value="WEBHOOK">Webhook</SelectItem>
          <SelectItem value="CRON">Schedule</SelectItem>
          <SelectItem value="AGENT">Agent</SelectItem>
          <SelectItem value="SYSTEM">System</SelectItem>
        </SelectContent>
      </Select>
    </div>
  )

  return (
    <PageShell
      title="Runs"
      description="Cross-agent run activity across your workspace"
      stats={stats}
      toolbar={filters}
    >
      {error && <p className="text-body text-destructive">{error}</p>}

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-12 rounded-md" />
          ))}
        </div>
      ) : !data || data.data.length === 0 ? (
        <EmptyState
          icon={Activity}
          title="No runs yet"
          description="Agent runs will appear here once agents start executing tasks."
        />
      ) : (
        <Card>
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Run</TableHead>
                  <TableHead>Agent</TableHead>
                  <TableHead>Team</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Trigger</TableHead>
                  <TableHead>Duration</TableHead>
                  <TableHead>Started</TableHead>
                  <TableHead className="w-10" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.data.map((run) => {
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

                  return (
                    <TableRow key={run.id}>
                      <TableCell className="font-mono text-micro text-muted-foreground">
                        #{run.id.slice(0, 8)}
                      </TableCell>
                      <TableCell className="text-body font-medium">
                        <Link href={`/agents/${run.agent_id}`} className="hover:underline">
                          {run.agent_name ?? "Unknown"}
                        </Link>
                      </TableCell>
                      <TableCell>
                        {run.crew_name ? (
                          <span className="text-body">{run.crew_name}</span>
                        ) : (
                          <span className="text-label text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <StatusBadge
                          status={canonicalStatus}
                          label={
                            <span className="inline-flex items-center gap-1.5">
                              {isRunning ? (
                                <StatusDot status="IN_PROGRESS" live className="h-1.5 w-1.5" />
                              ) : (
                                <StatusIcon className="h-3 w-3" />
                              )}
                              {statusLabel(run.status)}
                            </span>
                          }
                          className="text-micro"
                        />
                      </TableCell>
                      <TableCell className="text-label text-muted-foreground">
                        <span className="flex items-center gap-1.5">
                          {run.trigger_type === "WEBHOOK" && <AnimatedRocket size={12} />}
                          {run.trigger_type}
                        </span>
                      </TableCell>
                      <TableCell className="font-mono text-micro tabular-nums">
                        {isRunning && run.started_at
                          ? <LiveRunDuration startedAt={run.started_at} />
                          : formatDuration(run.started_at, run.finished_at)}
                      </TableCell>
                      <TableCell className="text-label text-muted-foreground">
                        {run.started_at
                          ? new Date(run.started_at).toLocaleString()
                          : new Date(run.created_at).toLocaleString()}
                      </TableCell>
                      <TableCell>
                        <Link
                          href={`/agents/${run.agent_id}`}
                          className="text-muted-foreground hover:text-foreground"
                        >
                          <ExternalLink className="h-3.5 w-3.5" />
                        </Link>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}
    </PageShell>
  )
}
