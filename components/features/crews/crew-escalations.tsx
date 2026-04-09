"use client"

import { Fragment, useCallback, useEffect, useRef, useState } from "react"
import { CheckCircle2, AlertTriangle, Send, ExternalLink, KeyRound } from "lucide-react"
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
import { useTick } from "@/hooks/use-tick"
import { EscalationResponseCard, ActionBadge } from "@/components/features/escalations/escalation-response-card"
import { formatRelativeTime } from "@/lib/time"
import { z } from "zod"
import { STATUS_STYLES, type StatusConfigEntryWithIcon } from "@/lib/status-config"

interface CrewEscalationsProps {
  crewId: string
  workspaceId: string
}

function PendingIcon({ className }: { className?: string }) {
  return <BadgeAlertIcon size={14} className={className} />
}


const STATUS_CONFIG: Record<Escalation["status"], StatusConfigEntryWithIcon> = {
  PENDING:  { label: "Pending",  className: STATUS_STYLES.amber,   icon: PendingIcon },
  RESOLVED: { label: "Resolved", className: STATUS_STYLES.emerald, icon: CheckCircle2 },
}

const TYPE_LABELS: Record<string, { label: string; icon: React.ComponentType<{ className?: string }> }> = {
  TEXT: { label: "Text", icon: Send },
  CREDENTIAL: { label: "Credential", icon: KeyRound },
  LINK: { label: "Link", icon: ExternalLink },
}

export function CrewEscalations({ crewId, workspaceId }: CrewEscalationsProps) {
  const [escalations, setEscalations] = useState<Escalation[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const requestIdRef = useRef(0)
  useTick(60_000) // re-render every 60s to keep relative times fresh
  const loadingOwnerRef = useRef<number | null>(null)
  const refreshingOwnerRef = useRef<number | null>(null)

  const fetchEscalations = useCallback(async (showRefresh = false, silent = false) => {
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
      // Silently fail
    } finally {
      if (ownsLoading && loadingOwnerRef.current === requestId) setLoading(false)
      if (ownsRefresh && refreshingOwnerRef.current === requestId) setRefreshing(false)
    }
  }, [crewId, workspaceId])

  useEffect(() => {
    fetchEscalations()
  }, [fetchEscalations])

  useRealtimeEvent("escalation.created", useCallback(() => { fetchEscalations(false, true) }, [fetchEscalations]))
  useRealtimeEvent("escalation.resolved", useCallback(() => { fetchEscalations(false, true) }, [fetchEscalations]))

  if (loading) {
    return (
      <div>
        <h2 className="text-default font-semibold mb-3">Escalations</h2>
        <div className="text-body text-muted-foreground">Loading escalations...</div>
      </div>
    )
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <h2 className="text-default font-semibold">Escalations</h2>
          {escalations.some((e) => e.status === "PENDING") && (
            <span aria-hidden="true" className="relative flex h-2 w-2">
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-amber-400 opacity-75" />
              <span className="relative inline-flex rounded-full h-2 w-2 bg-amber-500" />
            </span>
          )}
        </div>
        <span role="status" aria-live="polite" className="text-label text-muted-foreground">
          {refreshing ? "Updating..." : "Live"}
        </span>
      </div>

      {escalations.length === 0 ? (
        <div className="flex flex-col items-center gap-3 py-8 text-center">
          <AlertTriangle className="h-8 w-8 text-muted-foreground/50" />
          <div>
            <p className="text-body text-muted-foreground">No escalations yet.</p>
            <p className="text-label text-muted-foreground/70 mt-1">
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
                    const isPending = e.status === "PENDING"
                    const hasDetail = isPending || e.context || e.resolution
                    const detailId = `esc-detail-${e.id}`
                    const typeInfo = TYPE_LABELS[e.type] || TYPE_LABELS.TEXT

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
                            <div className="flex flex-col gap-1">
                              <Badge
                                variant="outline"
                                className={`gap-1 border-0 ${config.className}`}
                              >
                                <StatusIcon className="h-3 w-3" />
                                {config.label}
                              </Badge>
                              {e.type !== "TEXT" && (
                                <span className="text-[10px] text-muted-foreground flex items-center gap-0.5">
                                  <typeInfo.icon className="h-2.5 w-2.5" />
                                  {typeInfo.label}
                                </span>
                              )}
                            </div>
                          </TableCell>
                          <TableCell>
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <span className="text-body line-clamp-1">{e.reason}</span>
                              </TooltipTrigger>
                              <TooltipContent className="max-w-sm">
                                <p className="whitespace-pre-wrap">{e.reason}</p>
                              </TooltipContent>
                            </Tooltip>
                          </TableCell>
                          <TableCell className="text-body text-muted-foreground">
                            @{e.from_slug}
                          </TableCell>
                          <TableCell className="text-label text-muted-foreground">
                            {formatRelativeTime(e.created_at)}
                          </TableCell>
                        </TableRow>
                        {isExpanded && hasDetail && (
                          <TableRow id={detailId}>
                            <TableCell colSpan={4} className="bg-muted/30 p-0">
                              {isPending ? (
                                <EscalationResponseCard
                                  escalation={e}
                                  workspaceId={workspaceId}
                                  crewId={crewId}
                                  onResolved={() => fetchEscalations(false, true)}
                                />
                              ) : (
                                <div className="text-body whitespace-pre-wrap max-h-60 overflow-y-auto p-3 space-y-2">
                                  {e.action && (
                                    <div>
                                      <ActionBadge action={e.action} redirectTo={e.redirect_to} />
                                    </div>
                                  )}
                                  {e.context && (
                                    <div>
                                      <span className="font-medium text-muted-foreground">Context: </span>
                                      {e.context}
                                    </div>
                                  )}
                                  {e.resolution && (
                                    <div>
                                      <span className="font-medium text-muted-foreground">Resolution: </span>
                                      {e.type === "CREDENTIAL" ? "Credential submitted" : e.resolution}
                                    </div>
                                  )}
                                </div>
                              )}
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
