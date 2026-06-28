"use client"

import { Fragment, useCallback, useEffect, useState } from "react"
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
import { formatRelativeTime, formatDurationBetween } from "@/lib/time"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import type { Assignment } from "@/lib/types/assignment"
import { STATUS_STYLES, type StatusConfigEntryWithIcon } from "@/lib/status-config"
import { useApiResource } from "@/hooks/use-api-resource"

interface CrewAssignmentsProps {
  crewId: string
  workspaceId: string
}

const STATUS_CONFIG: Record<Assignment["status"], StatusConfigEntryWithIcon> = {
  COMPLETED: { label: "Completed", className: STATUS_STYLES.emerald, icon: CheckCircle2 },
  RUNNING:   { label: "Running",   className: STATUS_STYLES.blue,    icon: Loader2 },
  PENDING:   { label: "Pending",   className: STATUS_STYLES.amber,   icon: Clock },
  FAILED:    { label: "Failed",    className: STATUS_STYLES.red,     icon: XCircle },
}

function LiveDuration({ startedAt }: { startedAt: string }) {
  const [, setTick] = useState(0)
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), 1000)
    return () => clearInterval(id)
  }, [])
  return <>{formatDurationBetween(startedAt, null)}</>
}

export function CrewAssignments({ crewId, workspaceId }: CrewAssignmentsProps) {
  const [expandedId, setExpandedId] = useState<string | null>(null)
  // keepDataOnError: a transient backend hiccup keeps the last good list
  // (component shows empty state only when nothing has loaded yet).
  const { data, loading, reload } = useApiResource<Assignment[]>(
    `/api/v1/crews/${crewId}/assignments?workspace_id=${workspaceId}&limit=50`,
    { keepDataOnError: true },
  )
  const assignments = data ?? []

  // Real-time: refetch (silently, no spinner flash) when status changes.
  useRealtimeEvent("assignment.updated", useCallback(() => { reload({ silent: true }) }, [reload]))

  if (loading) {
    return (
      <div>
        <h2 className="text-default font-semibold mb-3">Assignments</h2>
        <div className="text-body text-muted-foreground">Loading assignments...</div>
      </div>
    )
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <h2 className="text-default font-semibold">Assignments</h2>
          {assignments.some((a) => a.status === "RUNNING") && (
            <span aria-hidden="true" className="relative flex h-2 w-2">
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
              <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
            </span>
          )}
        </div>
        <span role="status" aria-live="polite" className="text-label text-muted-foreground">
          Live
        </span>
      </div>

      {assignments.length === 0 ? (
        <div className="flex flex-col items-center gap-3 py-8 text-center">
          <ClipboardList className="h-8 w-8 text-muted-foreground/50" />
          <div>
            <p className="text-body text-muted-foreground">No assignments yet.</p>
            <p className="text-label text-muted-foreground/70 mt-1">
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
                          onClick={() => {
                            if (hasDetail) setExpandedId(isExpanded ? null : a.id)
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
                            {hasDetail ? (
                              <button
                                type="button"
                                className="text-left w-full"
                                aria-expanded={isExpanded}
                                aria-controls={`assign-detail-${a.id}`}
                                onClick={(e) => {
                                  e.stopPropagation()
                                  setExpandedId(isExpanded ? null : a.id)
                                }}
                              >
                                <Tooltip>
                                  <TooltipTrigger asChild>
                                    <span className="text-body line-clamp-1">{a.task}</span>
                                  </TooltipTrigger>
                                  <TooltipContent className="max-w-sm">
                                    <p className="whitespace-pre-wrap">{a.task}</p>
                                  </TooltipContent>
                                </Tooltip>
                              </button>
                            ) : (
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <span className="text-body line-clamp-1">{a.task}</span>
                                </TooltipTrigger>
                                <TooltipContent className="max-w-sm">
                                  <p className="whitespace-pre-wrap">{a.task}</p>
                                </TooltipContent>
                              </Tooltip>
                            )}
                          </TableCell>
                          <TableCell className="text-body text-muted-foreground">
                            @{a.assigned_by_slug}
                          </TableCell>
                          <TableCell className="text-body text-muted-foreground">
                            @{a.assigned_to_slug}
                          </TableCell>
                          <TableCell className="text-label text-muted-foreground">
                            {formatRelativeTime(a.created_at)}
                          </TableCell>
                          <TableCell className="text-label text-muted-foreground tabular-nums">
                            {a.status === "RUNNING" && a.started_at
                              ? <LiveDuration startedAt={a.started_at} />
                              : formatDurationBetween(a.started_at, a.finished_at)}
                          </TableCell>
                        </TableRow>
                        {isExpanded && hasDetail && (
                          <TableRow id={`assign-detail-${a.id}`}>
                            <TableCell colSpan={6} className="bg-muted/30">
                              <div className="text-body whitespace-pre-wrap max-h-60 overflow-y-auto p-2">
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
