"use client"

import { useParams } from "next/navigation"
import { useState, useEffect, useCallback } from "react"
import { AlertCircle, Activity } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { StatusBadge } from "@/components/ui/status-badge"
import { Skeleton } from "@/components/ui/skeleton"
import { EmptyState } from "@/components/layout/empty-state"
import { formatRelativeTime } from "@/lib/time"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import type { AgentRun } from "@/lib/types/agent"

// Map run status → canonical status id (shared palette) + live flag.
function runStatusId(status: string): { id: string; live: boolean } {
  switch (status) {
    case "PENDING": return { id: "BLOCKED", live: false }
    case "RUNNING": return { id: "IN_PROGRESS", live: true }
    case "COMPLETED": return { id: "COMPLETED", live: false }
    case "FAILED": return { id: "FAILED", live: false }
    case "CANCELLED": return { id: "CANCELLED", live: false }
    case "TIMEOUT": return { id: "BLOCKED", live: false }
    default: return { id: "PENDING", live: false }
  }
}

function formatDuration(start: string | null, end: string | null): string {
  if (!start) return "—"
  const startDate = new Date(start)
  const endDate = end ? new Date(end) : new Date()
  const diffMs = endDate.getTime() - startDate.getTime()
  const totalSeconds = Math.floor(diffMs / 1000)
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  if (minutes >= 60) {
    const hours = Math.floor(minutes / 60)
    const remaining = minutes % 60
    return `${hours}h ${remaining}m`
  }
  return `${minutes}m ${seconds.toString().padStart(2, "0")}s`
}

function LiveDuration({ startedAt }: { startedAt: string }) {
  const [, setTick] = useState(0)
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), 1000)
    return () => clearInterval(id)
  }, [])
  return <>{formatDuration(startedAt, null)}</>
}

export function RunsPageClient() {
  const { agentId } = useParams<{ agentId: string }>()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [runs, setRuns] = useState<AgentRun[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const fetchRuns = useCallback(async (silent = false) => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/runs?workspace_id=${workspaceId}`)
      if (!res.ok) {
        if (!silent) setError("Failed to load runs")
        return
      }
      const data = await res.json()
      setRuns(Array.isArray(data) ? data : [])
    } catch {
      if (!silent) setError("Network error. Please try again.")
    } finally {
      if (!silent) setLoading(false)
    }
  }, [agentId, workspaceId])

  useEffect(() => { fetchRuns() }, [fetchRuns])

  // Real-time: refetch runs on run events
  useRealtimeEvent("run.started", useCallback(() => { fetchRuns(true) }, [fetchRuns]))
  useRealtimeEvent("run.completed", useCallback(() => { fetchRuns(true) }, [fetchRuns]))
  useRealtimeEvent("run.failed", useCallback(() => { fetchRuns(true) }, [fetchRuns]))

  if (wsLoading || loading) {
    return <RunsSkeleton />
  }

  if (error) {
    return (
      <div className="p-4 sm:p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-body">{error}</p>
        </div>
      </div>
    )
  }

  const completedCount = runs.filter((r) => r.status === "COMPLETED").length
  const runningCount = runs.filter((r) => r.status === "RUNNING").length
  const failedCount = runs.filter((r) => r.status === "FAILED").length

  return (
    <div className="p-4 sm:p-6 space-y-6">
      <div>
        <h2 className="text-title font-semibold">Runs</h2>
        <p className="text-body text-muted-foreground">
          {runs.length} run{runs.length !== 1 ? "s" : ""} total
          {completedCount > 0 && ` · ${completedCount} completed`}
          {runningCount > 0 && ` · ${runningCount} running`}
          {failedCount > 0 && ` · ${failedCount} failed`}
        </p>
      </div>

      {runs.length === 0 ? (
        <EmptyState
          icon={Activity}
          title="No runs yet"
          description="Runs will appear here when the agent is triggered."
        />
      ) : (
        <div className="border border-border rounded-lg overflow-x-auto bg-card">
          <table className="w-full text-body">
            <thead>
              <tr className="border-b border-border bg-muted/50 text-label text-muted-foreground uppercase tracking-wide">
                <th className="text-left px-4 sm:px-6 py-3 font-medium">Status</th>
                <th className="text-left px-4 sm:px-6 py-3 font-medium">Trigger</th>
                <th className="text-left px-4 sm:px-6 py-3 font-medium">Duration</th>
                <th className="text-left px-4 sm:px-6 py-3 font-medium hidden sm:table-cell">Started</th>
                <th className="text-left px-4 sm:px-6 py-3 font-medium hidden md:table-cell">Error</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {runs.map((r) => {
                const { id: statusId, live } = runStatusId(r.status)
                return (
                  <tr key={r.id} className="hover:bg-muted/50">
                    <td className="px-4 sm:px-6 py-3">
                      <StatusBadge
                        status={statusId}
                        withDot={live}
                        label={r.status}
                        className="text-label"
                      />
                    </td>
                    <td className="px-4 sm:px-6 py-3">
                      <Badge variant="outline" className="text-label border-border bg-muted/40 text-muted-foreground">
                        {r.trigger_type}
                      </Badge>
                    </td>
                    <td className="px-4 sm:px-6 py-3 font-mono text-label tabular-nums">
                      {r.status === "RUNNING" && r.started_at
                        ? <LiveDuration startedAt={r.started_at} />
                        : formatDuration(r.started_at, r.finished_at)}
                    </td>
                    <td className="px-4 sm:px-6 py-3 text-label text-muted-foreground hidden sm:table-cell">
                      {r.started_at ? formatRelativeTime(r.started_at) : "—"}
                    </td>
                    <td className="px-4 sm:px-6 py-3 hidden md:table-cell">
                      {r.status === "FAILED" && r.error_message && (
                        <span className="text-label text-destructive truncate block max-w-[200px]" title={r.error_message}>
                          {r.error_message}
                        </span>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function RunsSkeleton() {
  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <div className="border rounded-lg">
        <div className="border-b bg-muted/50 px-4 sm:px-6 py-3">
          <Skeleton className="h-4 w-full max-w-md" />
        </div>
        {Array.from({ length: 5 }).map((_, i) => (
          <div key={i} className="px-4 sm:px-6 py-3 border-b last:border-b-0">
            <Skeleton className="h-5 w-full" />
          </div>
        ))}
      </div>
    </div>
  )
}
