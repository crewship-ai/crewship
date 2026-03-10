"use client"

import { Fragment, useCallback, useEffect, useRef, useState } from "react"
import { CheckCircle2, AlertTriangle } from "lucide-react"
import { BadgeAlertIcon } from "@/components/ui/badge-alert"
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
import { escalationSchema, type Escalation } from "@/lib/types/escalation"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { z } from "zod"

interface CrewEscalationsProps {
  crewId: string
  workspaceId: string
}

function PendingIcon() {
  return <BadgeAlertIcon size={14} />
}

const STATUS_CONFIG: Record<Escalation["status"], {
  label: string
  className: string
  icon: React.ComponentType<{ className?: string }>
}> = {
  PENDING: {
    label: "Pending",
    className: "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300",
    icon: PendingIcon,
  },
  RESOLVED: {
    label: "Resolved",
    className: "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-300",
    icon: CheckCircle2,
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

export function CrewEscalations({ crewId, workspaceId }: CrewEscalationsProps) {
  const [escalations, setEscalations] = useState<Escalation[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const requestIdRef = useRef(0)
  const loadingOwnerRef = useRef<number | null>(null)
  const refreshingOwnerRef = useRef<number | null>(null)

  const fetchEscalations = useCallback(async (showRefresh = false, silent = false) => {
    const requestId = ++requestIdRef.current

    if (!silent && showRefresh) {
      refreshingOwnerRef.current = requestId
      setRefreshing(true)
    } else if (!silent) {
      loadingOwnerRef.current = requestId
      setLoading(true)
    }
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/escalations?workspace_id=${workspaceId}&limit=50`
      )
      if (!res.ok) return
      const json = await res.json()
      if (requestId !== requestIdRef.current) return
      const parsed = z.array(escalationSchema).safeParse(json)
      if (parsed.success) {
        setEscalations(parsed.data)
      }
    } catch {
      // Silently fail — component shows empty state
    } finally {
      if (loadingOwnerRef.current === requestId) setLoading(false)
      if (refreshingOwnerRef.current === requestId) setRefreshing(false)
    }
  }, [crewId, workspaceId])

  useEffect(() => {
    fetchEscalations()
  }, [fetchEscalations])

  // Real-time: refetch when escalations are created
  useRealtimeEvent("escalation.created", useCallback(() => { fetchEscalations(false, true) }, [fetchEscalations]))

  if (loading) {
    return (
      <div>
        <h2 className="text-base font-semibold mb-3">Escalations</h2>
        <div className="text-sm text-muted-foreground">Loading escalations...</div>
      </div>
    )
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <h2 className="text-base font-semibold">Escalations</h2>
          {escalations.some((e) => e.status === "PENDING") && (
            <span className="relative flex h-2 w-2">
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-amber-400 opacity-75" />
              <span className="relative inline-flex rounded-full h-2 w-2 bg-amber-500" />
            </span>
          )}
        </div>
        <span className="text-xs text-muted-foreground">
          {refreshing ? "Updating..." : "Live"}
        </span>
      </div>

      {escalations.length === 0 ? (
        <div className="flex flex-col items-center gap-3 py-8 text-center">
          <AlertTriangle className="h-8 w-8 text-muted-foreground/50" />
          <div>
            <p className="text-sm text-muted-foreground">No escalations yet.</p>
            <p className="text-xs text-muted-foreground/70 mt-1">
              Escalations appear when agents need human intervention or encounter blockers.
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
                    <TableHead>Reason</TableHead>
                    <TableHead className="w-28">From</TableHead>
                    <TableHead className="w-24">When</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {escalations.map((e) => {
                    const config = STATUS_CONFIG[e.status]
                    const StatusIcon = config.icon
                    const isExpanded = expandedId === e.id
                    const hasDetail = e.context || e.resolution
                    const detailId = `esc-detail-${e.id}`

                    return (
                      <Fragment key={e.id}>
                        <TableRow
                          className={hasDetail ? "cursor-pointer" : ""}
                          role={hasDetail ? "button" : undefined}
                          tabIndex={hasDetail ? 0 : -1}
                          aria-expanded={hasDetail ? isExpanded : undefined}
                          aria-controls={hasDetail ? detailId : undefined}
                          onClick={() => {
                            if (hasDetail) setExpandedId(isExpanded ? null : e.id)
                          }}
                          onKeyDown={(ev) => {
                            if (!hasDetail) return
                            if (ev.key === "Enter" || ev.key === " ") {
                              ev.preventDefault()
                              setExpandedId(isExpanded ? null : e.id)
                            }
                          }}
                        >
                          <TableCell>
                            <Badge
                              variant="outline"
                              className={`gap-1 border-0 ${config.className}`}
                            >
                              <StatusIcon className="h-3 w-3" />
                              {config.label}
                            </Badge>
                          </TableCell>
                          <TableCell>
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <span className="text-sm line-clamp-1">{e.reason}</span>
                              </TooltipTrigger>
                              <TooltipContent className="max-w-sm">
                                <p className="whitespace-pre-wrap">{e.reason}</p>
                              </TooltipContent>
                            </Tooltip>
                          </TableCell>
                          <TableCell className="text-sm text-muted-foreground">
                            @{e.from_slug}
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            {formatRelativeTime(e.created_at)}
                          </TableCell>
                        </TableRow>
                        {isExpanded && hasDetail && (
                          <TableRow id={detailId}>
                            <TableCell colSpan={4} className="bg-muted/30">
                              <div className="text-sm whitespace-pre-wrap max-h-60 overflow-y-auto p-2">
                                {e.context && (
                                  <div className="mb-2">
                                    <span className="font-medium text-muted-foreground">Context: </span>
                                    {e.context}
                                  </div>
                                )}
                                {e.resolution && (
                                  <div>
                                    <span className="font-medium text-muted-foreground">Resolution: </span>
                                    {e.resolution}
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
