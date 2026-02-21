"use client"

import { Fragment, useEffect, useState } from "react"
import { RefreshCw, CheckCircle2, Loader2, XCircle, MessageSquare, AlertTriangle } from "lucide-react"
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
import { peerConversationSchema, type PeerConversation } from "@/lib/types/peer-conversation"
import { z } from "zod"

interface CrewPeerConversationsProps {
  crewId: string
  workspaceId: string
}

const STATUS_CONFIG: Record<PeerConversation["status"], {
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

function formatDurationMs(ms: number | null): string {
  if (ms === null) return "—"
  const seconds = Math.floor(ms / 1000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  const remainSecs = seconds % 60
  if (minutes < 60) return `${minutes}m ${remainSecs}s`
  const hours = Math.floor(minutes / 60)
  return `${hours}h ${minutes % 60}m`
}

export function CrewPeerConversations({ crewId, workspaceId }: CrewPeerConversationsProps) {
  const [conversations, setConversations] = useState<PeerConversation[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [expandedId, setExpandedId] = useState<string | null>(null)

  async function fetchConversations(showRefresh = false) {
    if (showRefresh) setRefreshing(true)
    else setLoading(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/peer-conversations?workspace_id=${workspaceId}&limit=50`
      )
      if (res.ok) {
        const json = await res.json()
        const parsed = z.array(peerConversationSchema).safeParse(json)
        if (parsed.success) {
          setConversations(parsed.data)
        }
      }
    } catch {
      // Silently fail — component shows empty state
    } finally {
      setLoading(false)
      setRefreshing(false)
    }
  }

  useEffect(() => {
    fetchConversations()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [crewId, workspaceId])

  if (loading) {
    return (
      <div>
        <h2 className="text-base font-semibold mb-3">Peer Conversations</h2>
        <div className="text-sm text-muted-foreground">Loading peer conversations...</div>
      </div>
    )
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <h2 className="text-base font-semibold">Peer Conversations</h2>
        <Button
          variant="outline"
          size="sm"
          className="gap-2"
          onClick={() => fetchConversations(true)}
          disabled={refreshing}
        >
          <RefreshCw className={`h-3.5 w-3.5 ${refreshing ? "animate-spin" : ""}`} />
          Refresh
        </Button>
      </div>

      {conversations.length === 0 ? (
        <div className="flex flex-col items-center gap-3 py-8 text-center">
          <MessageSquare className="h-8 w-8 text-muted-foreground/50" />
          <div>
            <p className="text-sm text-muted-foreground">No peer conversations yet.</p>
            <p className="text-xs text-muted-foreground/70 mt-1">
              Peer conversations appear when agents communicate with each other.
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
                    <TableHead>Question</TableHead>
                    <TableHead className="w-28">From</TableHead>
                    <TableHead className="w-28">To</TableHead>
                    <TableHead className="w-24">When</TableHead>
                    <TableHead className="w-24">Duration</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {conversations.map((c) => {
                    const config = STATUS_CONFIG[c.status]
                    const StatusIcon = config.icon
                    const isExpanded = expandedId === c.id
                    const hasDetail = c.response
                    const detailId = `peer-detail-${c.id}`

                    return (
                      <Fragment key={c.id}>
                        <TableRow
                          className={hasDetail ? "cursor-pointer" : ""}
                          role={hasDetail ? "button" : undefined}
                          tabIndex={hasDetail ? 0 : -1}
                          aria-expanded={hasDetail ? isExpanded : undefined}
                          aria-controls={hasDetail ? detailId : undefined}
                          onClick={() => {
                            if (hasDetail) setExpandedId(isExpanded ? null : c.id)
                          }}
                          onKeyDown={(e) => {
                            if (!hasDetail) return
                            if (e.key === "Enter" || e.key === " ") {
                              e.preventDefault()
                              setExpandedId(isExpanded ? null : c.id)
                            }
                          }}
                        >
                          <TableCell>
                            <Badge
                              variant="outline"
                              className={`gap-1 border-0 ${config.className}`}
                            >
                              <StatusIcon className={`h-3 w-3 ${c.status === "RUNNING" ? "animate-spin" : ""}`} />
                              {config.label}
                            </Badge>
                          </TableCell>
                          <TableCell>
                            <div className="flex items-center gap-2">
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <span className="text-sm line-clamp-1">{c.question}</span>
                                </TooltipTrigger>
                                <TooltipContent className="max-w-sm">
                                  <p className="whitespace-pre-wrap">{c.question}</p>
                                </TooltipContent>
                              </Tooltip>
                              {c.escalated && (
                                <Badge variant="outline" className="gap-1 border-0 bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300 shrink-0">
                                  <AlertTriangle className="h-3 w-3" />
                                  Escalated
                                </Badge>
                              )}
                            </div>
                          </TableCell>
                          <TableCell className="text-sm text-muted-foreground">
                            @{c.from_slug}
                          </TableCell>
                          <TableCell className="text-sm text-muted-foreground">
                            @{c.to_slug}
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            {formatRelativeTime(c.created_at)}
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            {formatDurationMs(c.duration_ms)}
                          </TableCell>
                        </TableRow>
                        {isExpanded && hasDetail && (
                          <TableRow id={detailId}>
                            <TableCell colSpan={6} className="bg-muted/30">
                              <div className="text-sm whitespace-pre-wrap max-h-60 overflow-y-auto p-2">
                                <span className="font-medium text-muted-foreground">Response: </span>
                                {c.response}
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
