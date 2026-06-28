"use client"

import { Fragment, useCallback, useEffect, useState } from "react"
import { Activity, ClipboardList, MessageSquare, AlertTriangle } from "lucide-react"
import { Badge } from "@/components/ui/badge"
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
import { activityItemSchema, type ActivityItem } from "@/lib/types/activity"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { useTick } from "@/hooks/use-tick"
import { formatRelativeTime } from "@/lib/time"
import { apiFetch } from "@/lib/api-fetch"
import { z } from "zod"

interface CrewActivityFeedProps {
  workspaceId: string
  /** Optional entity scope. Server filters the merged feed when set. */
  agentId?: string
  crewId?: string
}

const TYPE_CONFIG: Record<ActivityItem["type"], {
  label: string
  className: string
  icon: React.ComponentType<{ className?: string }>
}> = {
  assignment: {
    label: "Task",
    className: "bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300",
    icon: ClipboardList,
  },
  peer_conversation: {
    label: "Query",
    className: "bg-violet-100 text-violet-800 dark:bg-violet-900/40 dark:text-violet-300",
    icon: MessageSquare,
  },
  escalation: {
    label: "Escalation",
    className: "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300",
    icon: AlertTriangle,
  },
}

export function CrewActivityFeed({ workspaceId, agentId, crewId }: CrewActivityFeedProps) {
  const [items, setItems] = useState<ActivityItem[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  useTick(60_000) // re-render every 60s to keep relative times fresh
  const [expandedId, setExpandedId] = useState<string | null>(null)

  const fetchActivity = useCallback(async (showRefresh = false, silent = false) => {
    if (!silent) {
      if (showRefresh) setRefreshing(true)
      else setLoading(true)
    }
    try {
      const params = new URLSearchParams({
        workspace_id: workspaceId,
        limit: "30",
      })
      if (agentId) params.set("agent_id", agentId)
      if (crewId) params.set("crew_id", crewId)
      const res = await apiFetch(`/api/v1/activity?${params.toString()}`)
      if (res.ok) {
        const json = await res.json()
        const parsed = z.array(activityItemSchema).safeParse(json)
        if (parsed.success) {
          setItems(parsed.data)
        }
      }
    } catch {
      // Silently fail — component shows empty state
    } finally {
      if (!silent) {
        setLoading(false)
        setRefreshing(false)
      }
    }
  }, [workspaceId, agentId, crewId])

  useEffect(() => {
    fetchActivity()
  }, [fetchActivity])

  // Real-time: refetch when assignment or escalation events arrive
  useRealtimeEvent("assignment.updated", useCallback(() => { fetchActivity(false, true) }, [fetchActivity]))
  useRealtimeEvent("escalation.created", useCallback(() => { fetchActivity(false, true) }, [fetchActivity]))

  // The parent (agent-canvas / crew-canvas) already renders the section
  // heading with View-all + Live indicator; this component just renders
  // the body so the styling matches the Runtime / System Prompt cards.

  if (loading) {
    return (
      <div className="flex items-center justify-center py-12">
        <div className="text-body text-muted-foreground">Loading activity…</div>
      </div>
    )
  }

  if (items.length === 0) {
    return (
      <div className="flex flex-col items-center gap-3 py-12 text-center">
        <Activity className="h-8 w-8 text-muted-foreground-soft" />
        <div>
          <p className="text-body text-muted-foreground">No activity yet.</p>
          <p className="text-label text-muted-foreground mt-1">
            Activity appears when agents work on assignments, query peers, or raise escalations.
          </p>
        </div>
      </div>
    )
  }

  return (
    <div className="relative">
      {/* Tiny live-update indicator pinned top-right; replaces the
          old "Recent Activity · Live" heading row. The outer card chrome
          comes from the parent (agent-canvas / crew-canvas), so this
          component only renders the table body. */}
      <div className="absolute right-3 top-3 z-10 flex items-center gap-1.5 text-[10px] text-muted-foreground">
        <span className="relative flex h-1.5 w-1.5">
          <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
          <span className="relative inline-flex rounded-full h-1.5 w-1.5 bg-emerald-500" />
        </span>
        {refreshing ? "Updating…" : "Live"}
      </div>
        <TooltipProvider>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-28">Type</TableHead>
                  <TableHead>Summary</TableHead>
                  <TableHead className="w-28">From</TableHead>
                  <TableHead className="w-28">To</TableHead>
                  <TableHead className="w-28">Crew</TableHead>
                  <TableHead className="w-24">When</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((item) => {
                  const config = TYPE_CONFIG[item.type]
                  const TypeIcon = config.icon
                  const isExpanded = expandedId === item.id
                  const hasDetail = item.detail
                  const detailId = `activity-detail-${item.type}-${item.id}`

                  return (
                    <Fragment key={`${item.type}-${item.id}`}>
                      <TableRow
                        className={hasDetail ? "cursor-pointer" : ""}
                        role={hasDetail ? "button" : undefined}
                        tabIndex={hasDetail ? 0 : -1}
                        aria-expanded={hasDetail ? isExpanded : undefined}
                        aria-controls={hasDetail ? detailId : undefined}
                        onClick={() => {
                          if (hasDetail) setExpandedId(isExpanded ? null : item.id)
                        }}
                        onKeyDown={(e) => {
                          if (!hasDetail) return
                          if (e.key === "Enter" || e.key === " ") {
                            e.preventDefault()
                            setExpandedId(isExpanded ? null : item.id)
                          }
                        }}
                      >
                        <TableCell>
                          <Badge
                            variant="outline"
                            className={`gap-1 border-0 ${config.className}`}
                          >
                            <TypeIcon className="h-3 w-3" />
                            {config.label}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <span className="text-body line-clamp-1">{item.summary}</span>
                            </TooltipTrigger>
                            <TooltipContent className="max-w-sm">
                              <p className="whitespace-pre-wrap">{item.summary}</p>
                            </TooltipContent>
                          </Tooltip>
                        </TableCell>
                        <TableCell className="text-body text-muted-foreground">
                          @{item.from_slug}
                        </TableCell>
                        <TableCell className="text-body text-muted-foreground">
                          {item.to_slug ? `@${item.to_slug}` : "—"}
                        </TableCell>
                        <TableCell>
                          <div
                            className="flex items-center gap-1.5"
                            style={item.crew_color ? { '--crew-color': item.crew_color } as React.CSSProperties : undefined}
                          >
                            {item.crew_color && (
                              <span
                                className="inline-block h-2 w-2 rounded-full shrink-0 bg-[var(--crew-color)]"
                              />
                            )}
                            <span className="text-body text-muted-foreground truncate">
                              {item.crew_name}
                            </span>
                          </div>
                        </TableCell>
                        <TableCell className="text-label text-muted-foreground">
                          {formatRelativeTime(item.created_at)}
                        </TableCell>
                      </TableRow>
                      {isExpanded && hasDetail && (
                        <TableRow id={detailId}>
                          <TableCell colSpan={6} className="bg-muted/30">
                            <div className="text-body whitespace-pre-wrap max-h-60 overflow-y-auto p-2">
                              {item.detail}
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
    </div>
  )
}
