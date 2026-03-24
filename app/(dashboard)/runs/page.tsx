"use client"

import { useCallback, useEffect, useState } from "react"
import { Activity, Clock, AlertTriangle, CheckCircle, XCircle, Play, ExternalLink } from "lucide-react"
import { RocketIcon as AnimatedRocket } from "@/components/ui/rocket"
import { AnimatedNumber } from "@/components/ui/animated-number"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
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

const statusConfig: Record<string, { label: string; variant: "default" | "secondary" | "destructive" | "outline"; icon: React.ElementType }> = {
  PENDING: { label: "Pending", variant: "outline", icon: Clock },
  RUNNING: { label: "Running", variant: "default", icon: Play },
  COMPLETED: { label: "Completed", variant: "secondary", icon: CheckCircle },
  FAILED: { label: "Failed", variant: "destructive", icon: XCircle },
  CANCELLED: { label: "Cancelled", variant: "outline", icon: XCircle },
  TIMEOUT: { label: "Timeout", variant: "destructive", icon: AlertTriangle },
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

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Runs" description="Cross-agent run activity across your workspace" />

      {error && <p className="text-body text-destructive">{error}</p>}

      {/* Stats */}
      {data && (
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
          <Card>
            <CardContent className="p-4">
              <div className="flex items-center justify-between">
                <div className="text-label text-muted-foreground uppercase tracking-wide font-medium">Running Now</div>
                {data.stats.running > 0 && (
                  <span className="relative flex h-2 w-2">
                    <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
                    <span className="relative inline-flex rounded-full h-2 w-2 bg-emerald-500" />
                  </span>
                )}
              </div>
              <div className="text-title font-bold mt-1 text-emerald-600"><AnimatedNumber value={data.stats.running} /></div>
            </CardContent>
          </Card>
          <Card>
            <CardContent className="p-4">
              <div className="text-label text-muted-foreground uppercase tracking-wide font-medium">Today&apos;s Runs</div>
              <div className="text-title font-bold mt-1"><AnimatedNumber value={data.stats.today} /></div>
            </CardContent>
          </Card>
          <Card>
            <CardContent className="p-4">
              <div className="text-label text-muted-foreground uppercase tracking-wide font-medium">Failed</div>
              <div className="text-title font-bold mt-1 text-destructive"><AnimatedNumber value={data.stats.failed} /></div>
            </CardContent>
          </Card>
          <Card>
            <CardContent className="p-4">
              <div className="text-label text-muted-foreground uppercase tracking-wide font-medium">Total</div>
              <div className="text-title font-bold mt-1"><AnimatedNumber value={data.pagination.total} /></div>
            </CardContent>
          </Card>
        </div>
      )}

      {/* Filters */}
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
                  const config = statusConfig[run.status] ?? statusConfig.PENDING
                  const StatusIcon = config.icon

                  return (
                    <TableRow key={run.id}>
                      <TableCell className="font-mono text-xs text-muted-foreground">
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
                        <Badge variant={config.variant} className="gap-1.5 text-micro">
                          {run.status === "RUNNING" ? (
                            <span className="relative flex h-2 w-2 shrink-0">
                              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
                              <span className="relative inline-flex rounded-full h-2 w-2 bg-emerald-500" />
                            </span>
                          ) : (
                            <StatusIcon className="h-3 w-3" />
                          )}
                          {config.label}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-label text-muted-foreground">
                        <span className="flex items-center gap-1.5">
                          {run.trigger_type === "WEBHOOK" && <AnimatedRocket size={12} />}
                          {run.trigger_type}
                        </span>
                      </TableCell>
                      <TableCell className="font-mono text-xs tabular-nums">
                        {run.status === "RUNNING" && run.started_at
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
    </div>
  )
}
