"use client"

import { Fragment, useCallback, useState } from "react"
import { MessageSquare, AlertTriangle } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { StatusIconBadge } from "@/components/ui/status-icon-badge"
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
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { formatRelativeTime, formatDurationClock } from "@/lib/time"
import { z } from "zod"
import { RUN_STATUS_CONFIG } from "@/lib/status-config"
import { useApiResource } from "@/hooks/use-api-resource"

interface CrewPeerConversationsProps {
  crewId: string
  workspaceId: string
}

// Completed / Running / Failed entries are shared with crew assignments.
const STATUS_CONFIG = RUN_STATUS_CONFIG

export function CrewPeerConversations({ crewId, workspaceId }: CrewPeerConversationsProps) {
  const [expandedId, setExpandedId] = useState<string | null>(null)
  // keepDataOnError + schema: parse/transport failures keep the last good
  // list (component shows empty state only before the first load lands).
  const { data, loading, reload } = useApiResource<PeerConversation[]>(
    `/api/v1/crews/${crewId}/peer-conversations?workspace_id=${workspaceId}&limit=50`,
    { schema: z.array(peerConversationSchema), keepDataOnError: true },
  )
  const conversations = data ?? []

  // Real-time: refetch (silently, no spinner flash) when conversations finish.
  useRealtimeEvent("peer_conversation.updated", useCallback(() => { reload({ silent: true }) }, [reload]))

  if (loading) {
    return (
      <div>
        <h2 className="text-default font-semibold mb-3">Peer Conversations</h2>
        <div className="text-body text-muted-foreground">Loading peer conversations...</div>
      </div>
    )
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <h2 className="text-default font-semibold">Peer Conversations</h2>
          {conversations.some((c) => c.status === "RUNNING") && (
            <span className="relative flex h-2 w-2">
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
              <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
            </span>
          )}
        </div>
        <span className="text-label text-muted-foreground">
          Live
        </span>
      </div>

      {conversations.length === 0 ? (
        <div className="flex flex-col items-center gap-3 py-8 text-center">
          <MessageSquare className="h-8 w-8 text-muted-foreground/50" />
          <div>
            <p className="text-body text-muted-foreground">No peer conversations yet.</p>
            <p className="text-label text-muted-foreground/70 mt-1">
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
                            <StatusIconBadge
                              entry={config}
                              gap="gap-1.5"
                              icon={
                                c.status === "RUNNING" ? (
                                  <span className="relative flex h-2 w-2 shrink-0">
                                    <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
                                    <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
                                  </span>
                                ) : undefined
                              }
                            />
                          </TableCell>
                          <TableCell>
                            <div className="flex items-center gap-2">
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <span className="text-body line-clamp-1">{c.question}</span>
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
                          <TableCell className="text-body text-muted-foreground">
                            @{c.from_slug}
                          </TableCell>
                          <TableCell className="text-body text-muted-foreground">
                            @{c.to_slug}
                          </TableCell>
                          <TableCell className="text-label text-muted-foreground">
                            {formatRelativeTime(c.created_at)}
                          </TableCell>
                          <TableCell className="text-label text-muted-foreground">
                            {c.duration_ms === null ? "—" : formatDurationClock(c.duration_ms)}
                          </TableCell>
                        </TableRow>
                        {isExpanded && hasDetail && (
                          <TableRow id={detailId}>
                            <TableCell colSpan={6} className="bg-muted/30">
                              <div className="text-body whitespace-pre-wrap max-h-60 overflow-y-auto p-2">
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
