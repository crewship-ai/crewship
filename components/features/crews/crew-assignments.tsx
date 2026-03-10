"use client"

import { Fragment, useCallback, useEffect, useRef, useState } from "react"
import { CheckCircle2, Loader2, Clock, XCircle, ClipboardList } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent } from "@/components/ui/card"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import type { Assignment } from "@/lib/types/assignment"

interface CrewAssignmentsProps {
  crewId: string
  workspaceId: string
}

const STATUS_CONFIG: Record<Assignment["status"], {
  label: string
  className: string
  icon: React.ComponentType<{ className?: string }>
}> = {
  COMPLETED: {
    label: "Completed",
    className: "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-300",
    icon: CheckCircle2,
  },
  RUNNING: {
    label: "Running",
    className: "bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300",
    icon: Loader2,
  },
  PENDING: {
    label: "Pending",
    className: "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300",
    icon: Clock,
  },
  FAILED: {
    label: "Failed",
    className: "bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300",
    icon: XCircle,
  },
}

function formatRelativeTime(dateStr: string): string {
  const now = Date.now()
  const date = new Date(dateStr).getTime()
  const diffMs = now - date

  const seconds = Math.floor(diffMs / 1000)
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return `${days}d ago`
}

function formatDuration(startedAt: string | null, finishedAt: string | null): string {
  if (!startedAt) return "—"
  const start = new Date(startedAt).getTime()
  const end = finishedAt ? new Date(finishedAt).getTime() : Date.now()
  const diffMs = end - start

  const seconds = Math.floor(diffMs / 1000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  const remainSecs = seconds % 60
  if (minutes < 60) return `${minutes}m ${remainSecs}s`
  const hours = Math.floor(minutes / 60)
  return `${hours}h ${minutes % 60}m`
}

function LiveDuration({ startedAt }: { startedAt: string }) {
  const [, setTick] = useState(0)
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), 1000)
    return () => clearInterval(id)
  }, [])
  return <>{formatDuration(startedAt, null)}</>
}

export function CrewAssignments({ crewId, workspaceId }: CrewAssignmentsProps) {
  const [assignments, setAssignments] = useState<Assignment[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const requestIdRef = useRef(0)
  const loadingOwnerRef = useRef<number | null>(null)
  const refreshingOwnerRef = useRef<number | null>(null)

  const fetchAssignments = useCallback(async (showRefresh = false, silent = false) => {
    const requestId = silent ? requestIdRef.current : ++requestIdRef.current
    const ownsLoading = !silent && !showRefresh
    const ownsRefresh = !silent && showRefresh

    if (ownsRefresh) {
      refreshingOwnerRef.current = requestId
      setRefreshing(true)
    } else if (ownsLoading) {
      loadingOwnerRef.current = requestId
      setLoading(true)
    }
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/assignments?workspace_id=${workspaceId}&limit=50`
      )
      if (!res.ok) return
      const data = (await res.json()) as Assignment[]
      if (requestId === requestIdRef.current) {
        setAssignments(data)
      }
    } catch {
      // Silently fail — component shows empty state
    } finally {
      if (ownsLoading && loadingOwnerRef.current === requestId) setLoading(false)
      if (ownsRefresh && refreshingOwnerRef.current === requestId) setRefreshing(false)
    }
  }, [crewId, workspaceId])

  useEffect(() => {
    fetchAssignments()
  }, [fetchAssignments])

  // Real-time: refetch when assignment status changes
  useRealtimeEvent("assignment.updated", useCallback(() => { fetchAssignments(false, true) }, [fetchAssignments]))

  if (loading) {
    return (
      <div>
        <h2 className="text-base font-semibold mb-3">Assignments</h2>
        <div className="text-sm text-muted-foreground">Loading assignments...</div>
      </div>
    )
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <h2 className="text-base font-semibold">Assignments</h2>
          {assignments.some((a) => a.status === "RUNNING") && (
            <span aria-hidden="true" className="relative flex h-2 w-2">
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
              <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
            </span>
          )}
        </div>
        <span role="status" aria-live="polite" className="text-xs text-muted-foreground">
          {refreshing ? "Updating..." : "Live"}
        </span>
      </div>

      {assignments.length === 0 ? (
        <div className="flex flex-col items-center gap-3 py-8 text-center">
          <ClipboardList className="h-8 w-8 text-muted-foreground/50" />
          <div>
            <p className="text-sm text-muted-foreground">No assignments yet.</p>
            <p className="text-xs text-muted-foreground/70 mt-1">
              Assignments appear when a lead agent delegates tasks to crew members.
            </p>
          </div>
        </div>
      ) : (
        <Card>
          <CardContent className="p-0">
            <TooltipProvider>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-28">Status</TableHead>
                    <TableHead>Task</TableHead>
                    <TableHead className="w-28">From</TableHead>
                    <TableHead className="w-28">To</TableHead>
                    <TableHead className="w-24">When</TableHead>
                    <TableHead className="w-24">Duration</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {assignments.map((a) => {
                    const config = STATUS_CONFIG[a.status]
                    const StatusIcon = config.icon
                    const isExpanded = expandedId === a.id
                    const hasDetail = a.result_summary || a.error_message

                    return (
                      <Fragment key={a.id}>
                        <TableRow
                          className={hasDetail ? "cursor-pointer" : ""}
                          role={hasDetail ? "button" : undefined}
                          tabIndex={hasDetail ? 0 : -1}
                          aria-expanded={hasDetail ? isExpanded : undefined}
                          onClick={() => {
                            if (hasDetail) setExpandedId(isExpanded ? null : a.id)
                          }}
                          onKeyDown={(e) => {
                            if (!hasDetail) return
                            if (e.key === "Enter" || e.key === " ") {
                              e.preventDefault()
                              setExpandedId(isExpanded ? null : a.id)
                            }
                          }}
                        >
                          <TableCell>
                            <Badge
                              variant="outline"
                              className={`gap-1.5 border-0 ${config.className}`}
                            >
                              {a.status === "RUNNING" ? (
                                <span aria-hidden="true" className="relative flex h-2 w-2 shrink-0">
                                  <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
                                  <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
                                </span>
                              ) : (
                                <StatusIcon className="h-3 w-3" />
                              )}
                              {config.label}
                            </Badge>
                          </TableCell>
                          <TableCell>
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <span className="text-sm line-clamp-1">{a.task}</span>
                              </TooltipTrigger>
                              <TooltipContent className="max-w-sm">
                                <p className="whitespace-pre-wrap">{a.task}</p>
                              </TooltipContent>
                            </Tooltip>
                          </TableCell>
                          <TableCell className="text-sm text-muted-foreground">
                            @{a.assigned_by_slug}
                          </TableCell>
                          <TableCell className="text-sm text-muted-foreground">
                            @{a.assigned_to_slug}
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            {formatRelativeTime(a.created_at)}
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground tabular-nums">
                            {a.status === "RUNNING" && a.started_at
                              ? <LiveDuration startedAt={a.started_at} />
                              : formatDuration(a.started_at, a.finished_at)}
                          </TableCell>
                        </TableRow>
                        {isExpanded && hasDetail && (
                          <TableRow>
                            <TableCell colSpan={6} className="bg-muted/30">
                              <div className="text-sm whitespace-pre-wrap max-h-60 overflow-y-auto p-2">
                                {a.error_message && (
                                  <div className="text-destructive mb-2">
                                    <span className="font-medium">Error: </span>
                                    {a.error_message}
                                  </div>
                                )}
                                {a.result_summary && (
                                  <div>
                                    <span className="font-medium text-muted-foreground">Result: </span>
                                    {a.result_summary}
                                  </div>
                                )}
                              </div>
                            </TableCell>
                          </TableRow>
                        )}
                      </Fragment>
                    )
                  })}
                </TableBody>
              </Table>
            </TooltipProvider>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
