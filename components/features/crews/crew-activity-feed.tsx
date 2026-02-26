"use client"

import { Fragment, useCallback, useEffect, useState } from "react"
import { RefreshCw, Activity, ClipboardList, MessageSquare, AlertTriangle } from "lucide-react"
import { Button } from "@/components/ui/button"
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
import { activityItemSchema, type ActivityItem } from "@/lib/types/activity"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { z } from "zod"

interface CrewActivityFeedProps {
  workspaceId: string
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

export function CrewActivityFeed({ workspaceId }: CrewActivityFeedProps) {
  const [items, setItems] = useState<ActivityItem[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [expandedId, setExpandedId] = useState<string | null>(null)

  const fetchActivity = useCallback(async (showRefresh = false, silent = false) => {
    if (silent) { /* no loading state change */ }
    else if (showRefresh) setRefreshing(true)
    else setLoading(true)
    try {
      const res = await fetch(
        `/api/v1/activity?workspace_id=${workspaceId}&limit=30`
      )
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
      setLoading(false)
      setRefreshing(false)
    }
  }, [workspaceId])

  useEffect(() => {
    fetchActivity()
  }, [fetchActivity])

  // Real-time: refetch when assignment or escalation events arrive
  useRealtimeEvent("assignment.updated", useCallback(() => { fetchActivity(false, true) }, [fetchActivity]))
  useRealtimeEvent("escalation.created", useCallback(() => { fetchActivity(false, true) }, [fetchActivity]))

  if (loading) {
    return (
      <div>
        <h2 className="text-base font-semibold mb-3">Recent Activity</h2>
        <div className="text-sm text-muted-foreground">Loading activity...</div>
      </div>
    )
  }

  if (items.length === 0) {
    return (
      <div>
        <h2 className="text-base font-semibold mb-3">Recent Activity</h2>
        <div className="flex flex-col items-center gap-3 py-8 text-center">
          <Activity className="h-8 w-8 text-muted-foreground/50" />
          <div>
            <p className="text-sm text-muted-foreground">No activity yet.</p>
            <p className="text-xs text-muted-foreground/70 mt-1">
              Activity appears when agents work on assignments, query peers, or raise escalations.
            </p>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <h2 className="text-base font-semibold">Recent Activity</h2>
        <Button
          variant="outline"
          size="sm"
          className="gap-2"
          onClick={() => fetchActivity(true)}
          disabled={refreshing}
        >
          <RefreshCw className={`h-3.5 w-3.5 ${refreshing ? "animate-spin" : ""}`} />
          Refresh
        </Button>
      </div>

      <Card>
        <CardContent className="p-0">
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
                              <span className="text-sm line-clamp-1">{item.summary}</span>
                            </TooltipTrigger>
                            <TooltipContent className="max-w-sm">
                              <p className="whitespace-pre-wrap">{item.summary}</p>
                            </TooltipContent>
                          </Tooltip>
                        </TableCell>
                        <TableCell className="text-sm text-muted-foreground">
                          @{item.from_slug}
                        </TableCell>
                        <TableCell className="text-sm text-muted-foreground">
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
                            <span className="text-sm text-muted-foreground truncate">
                              {item.crew_name}
                            </span>
                          </div>
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {formatRelativeTime(item.created_at)}
                        </TableCell>
                      </TableRow>
                      {isExpanded && hasDetail && (
                        <TableRow id={detailId}>
                          <TableCell colSpan={6} className="bg-muted/30">
                            <div className="text-sm whitespace-pre-wrap max-h-60 overflow-y-auto p-2">
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
        </CardContent>
      </Card>
    </div>
  )
}
