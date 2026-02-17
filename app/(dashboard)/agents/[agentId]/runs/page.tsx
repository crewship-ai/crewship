"use client"

import { use, useState, useEffect } from "react"
import { AlertCircle, Inbox } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"

interface AgentRun {
  id: string
  status: string
  trigger_type: string
  started_at: string | null
  finished_at: string | null
  error_message: string | null
}

const STATUS_STYLES: Record<string, { class: string; pulse: boolean }> = {
  PENDING: { class: "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-400", pulse: false },
  RUNNING: { class: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400", pulse: true },
  COMPLETED: { class: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400", pulse: false },
  FAILED: { class: "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-400", pulse: false },
  CANCELLED: { class: "bg-neutral-100 text-neutral-600 dark:bg-neutral-900 dark:text-neutral-400", pulse: false },
  TIMEOUT: { class: "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-400", pulse: false },
}

const TRIGGER_STYLES: Record<string, string> = {
  USER: "bg-blue-50 text-blue-700 dark:bg-blue-950 dark:text-blue-400",
  WEBHOOK: "bg-violet-50 text-violet-700 dark:bg-violet-950 dark:text-violet-400",
  CRON: "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-400",
  AGENT: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400",
  SYSTEM: "bg-neutral-100 text-neutral-600 dark:bg-neutral-900 dark:text-neutral-400",
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

function formatRelativeTime(dateStr: string | null): string {
  if (!dateStr) return "—"
  const diff = Date.now() - new Date(dateStr).getTime()
  const minutes = Math.floor(diff / 60000)
  if (minutes < 1) return "Just now"
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return days === 1 ? "Yesterday" : `${days}d ago`
}

export default function RunsPage({ params }: { params: Promise<{ agentId: string }> }) {
  const { agentId } = use(params)
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [runs, setRuns] = useState<AgentRun[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!workspaceId) return

    let cancelled = false

    async function fetchRuns() {
      try {
        const res = await fetch(`/api/v1/agents/${agentId}/runs?workspace_id=${workspaceId}`)
        if (!res.ok) {
          if (!cancelled) setError("Failed to load runs")
          return
        }
        const data: AgentRun[] = await res.json()
        if (!cancelled) setRuns(data)
      } catch {
        if (!cancelled) setError("Network error. Please try again.")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchRuns()
    return () => { cancelled = true }
  }, [agentId, workspaceId])

  if (wsLoading || loading) {
    return <RunsSkeleton />
  }

  if (error) {
    return (
      <div className="p-4 sm:p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-sm">{error}</p>
        </div>
      </div>
    )
  }

  const completedCount = runs.filter((r) => r.status === "COMPLETED").length
  const runningCount = runs.filter((r) => r.status === "RUNNING").length
  const failedCount = runs.filter((r) => r.status === "FAILED").length

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {runs.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <Inbox className="h-10 w-10 text-muted-foreground/50 mb-3" />
          <p className="text-sm font-medium text-muted-foreground">No runs yet</p>
          <p className="text-xs text-muted-foreground mt-1">Runs will appear here when the agent is triggered.</p>
        </div>
      ) : (
        <>
          <div className="border rounded-lg overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b bg-muted/50 text-xs text-muted-foreground uppercase tracking-wide">
                  <th className="text-left px-4 sm:px-6 py-3 font-medium">Status</th>
                  <th className="text-left px-4 sm:px-6 py-3 font-medium">Trigger</th>
                  <th className="text-left px-4 sm:px-6 py-3 font-medium">Duration</th>
                  <th className="text-left px-4 sm:px-6 py-3 font-medium hidden sm:table-cell">Started</th>
                  <th className="text-left px-4 sm:px-6 py-3 font-medium hidden md:table-cell">Error</th>
                </tr>
              </thead>
              <tbody className="divide-y">
                {runs.map((r) => {
                  const statusStyle = STATUS_STYLES[r.status] ?? STATUS_STYLES.PENDING
                  return (
                    <tr key={r.id} className="hover:bg-muted/50">
                      <td className="px-4 sm:px-6 py-3">
                        <Badge variant="secondary" className={`${statusStyle.class} text-xs gap-1`}>
                          {statusStyle.pulse && <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />}
                          {r.status}
                        </Badge>
                      </td>
                      <td className="px-4 sm:px-6 py-3">
                        <Badge variant="secondary" className={`${TRIGGER_STYLES[r.trigger_type] ?? ""} text-xs`}>
                          {r.trigger_type}
                        </Badge>
                      </td>
                      <td className="px-4 sm:px-6 py-3 font-mono text-xs">
                        {formatDuration(r.started_at, r.finished_at)}
                      </td>
                      <td className="px-4 sm:px-6 py-3 text-xs text-muted-foreground hidden sm:table-cell">
                        {formatRelativeTime(r.started_at)}
                      </td>
                      <td className="px-4 sm:px-6 py-3 hidden md:table-cell">
                        {r.status === "FAILED" && r.error_message && (
                          <span className="text-xs text-red-600 dark:text-red-400 truncate block max-w-[200px]" title={r.error_message}>
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

          {/* Footer */}
          <p className="text-xs text-muted-foreground">
            {runs.length} run{runs.length !== 1 ? "s" : ""} total
            {completedCount > 0 && ` · ${completedCount} completed`}
            {runningCount > 0 && ` · ${runningCount} running`}
            {failedCount > 0 && ` · ${failedCount} failed`}
          </p>
        </>
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
