"use client"

import { Fragment, useCallback, useMemo, useState } from "react"
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
import { useJournalList } from "@/hooks/use-journal-list"
import {
  useJournalLookup,
  type CrewLookup,
  type AgentLookup,
} from "@/hooks/use-journal-lookup"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { useTick } from "@/hooks/use-tick"
import { formatRelativeTime } from "@/lib/time"
import type { JournalEntry } from "@/lib/types/journal"

interface CrewActivityFeedProps {
  workspaceId: string
  /** Optional entity scope. The journal query narrows server-side when set. */
  agentId?: string
  crewId?: string
}

// The journal entry types that make up the "activity" view: peer queries,
// escalations, and the full assignment lifecycle (created → running →
// completed/failed). Mirrors the CLI's activityEntryTypes
// (cmd/crewship/cmd_activity.go). The terminal rows matter — without
// assignment.completed / assignment.failed a user watching the feed never
// saw an assignment finish or fail. The parallel run.completed/run.failed
// entries (keyed by trace_id) are the run-tracking view and are excluded
// here so the feed stays scoped to assignments, not every routine run.
const ACTIVITY_ENTRY_TYPES = "peer.conversation,peer.escalation,assignment.created,assignment.running,assignment.completed,assignment.failed"

type FeedType = "assignment" | "peer_conversation" | "escalation"

/**
 * A journal entry projected into the row shape the feed renders. Replaces
 * the old server-joined ActivityItem — participant slugs and crew
 * name/color are resolved client-side from useJournalLookup, since the
 * journal stores ids + payload slugs, never the joined display fields the
 * retired /api/v1/activity endpoint synthesised.
 */
export interface ActivityFeedRow {
  id: string
  type: FeedType
  summary: string
  detail: string | null
  from_slug: string | null
  to_slug: string | null
  crew_name: string | null
  crew_color: string | null
  created_at: string
}

const TYPE_CONFIG: Record<FeedType, {
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

function classifyEntryType(entryType: string): FeedType | null {
  if (entryType.startsWith("assignment.")) return "assignment"
  if (entryType === "peer.conversation") return "peer_conversation"
  if (entryType === "peer.escalation") return "escalation"
  return null
}

function payloadStr(p: JournalEntry["payload"], key: string): string | null {
  const v = p?.[key]
  return typeof v === "string" && v !== "" ? v : null
}

function agentSlug(agents: Map<string, AgentLookup>, id: string | null | undefined): string | null {
  if (!id) return null
  return agents.get(id)?.slug ?? null
}

/**
 * Projects raw journal entries into feed rows, enriching ids with the
 * lookup maps. Pure + framework-free so it unit-tests without React.
 * Non-activity entry types are dropped.
 */
export function journalEntriesToFeedRows(
  entries: JournalEntry[],
  crews: Map<string, CrewLookup>,
  agents: Map<string, AgentLookup>,
): ActivityFeedRow[] {
  const rows: ActivityFeedRow[] = []
  // Dedupe key per entry. A peer query lands as TWO peer.conversation
  // entries (question then answer) sharing one thread_id — rendering both
  // reads as the same conversation twice, so collapse them to the first
  // seen (entries arrive newest-first, so that is the answer once it
  // exists). Everything else keys on its own id, which drops a stray
  // repeated row (e.g. a live-prepend racing a refetch) while keeping the
  // distinct assignment lifecycle rows (created/running/completed).
  const seen = new Set<string>()
  for (const e of entries) {
    const type = classifyEntryType(e.entry_type)
    if (!type) continue
    const p = e.payload
    const dedupKey =
      e.entry_type === "peer.conversation" && typeof p?.thread_id === "string" && p.thread_id !== ""
        ? `peer.conversation:${p.thread_id}`
        : `id:${e.id}`
    if (seen.has(dedupKey)) continue
    seen.add(dedupKey)
    const crew = e.crew_id ? crews.get(e.crew_id) : undefined
    // FROM: peer/escalation payloads carry from_slug; assignments carry the
    // assigner only as actor_id, so fall back to the lookup.
    const fromSlug = payloadStr(p, "from_slug") ?? agentSlug(agents, e.actor_id) ?? agentSlug(agents, e.agent_id)
    // TO: peer/assignment carry target_slug (+ target_id); escalations have none.
    const toSlug = payloadStr(p, "target_slug") ?? agentSlug(agents, payloadStr(p, "target_id"))
    // DETAIL: the full human body distinct from the truncated summary.
    const detail =
      payloadStr(p, "task") ??
      payloadStr(p, "question") ??
      payloadStr(p, "response") ??
      payloadStr(p, "reason") ??
      payloadStr(p, "context")
    rows.push({
      id: e.id,
      type,
      summary: e.summary,
      detail,
      from_slug: fromSlug,
      to_slug: toSlug,
      crew_name: crew?.name ?? null,
      crew_color: crew?.color ?? null,
      created_at: e.ts,
    })
  }
  return rows
}

export function CrewActivityFeed({ workspaceId, agentId, crewId }: CrewActivityFeedProps) {
  useTick(60_000) // re-render every 60s to keep relative times fresh
  const [expandedId, setExpandedId] = useState<string | null>(null)

  const params = useMemo(
    () => ({ entry_type: ACTIVITY_ENTRY_TYPES, crew_id: crewId, agent_id: agentId }),
    [crewId, agentId],
  )

  const { entries, loading, error, refresh } = useJournalList({
    workspaceId,
    params,
    limit: 30,
  })

  const { crews, agents } = useJournalLookup()
  const items = useMemo(
    () => journalEntriesToFeedRows(entries, crews, agents),
    [entries, crews, agents],
  )

  // Real-time: refetch when assignment or escalation events arrive. (The
  // journal is the source, but these WS events are the cheap "something
  // changed" signal — a full refetch keeps the enriched view correct
  // without wiring a second stream here.)
  useRealtimeEvent("assignment.updated", useCallback(() => { refresh() }, [refresh]))
  useRealtimeEvent("escalation.created", useCallback(() => { refresh() }, [refresh]))

  // The parent (agent-canvas / crew-canvas) already renders the section
  // heading with View-all + Live indicator; this component just renders
  // the body so the styling matches the Runtime / System Prompt cards.

  if (loading && items.length === 0) {
    return (
      <div className="flex items-center justify-center py-12">
        <div className="text-body text-muted-foreground">Loading activity…</div>
      </div>
    )
  }

  // Surface load failures instead of falling through to the empty state,
  // which would misreport a broken fetch as "no activity yet" (#1408).
  if (error && items.length === 0) {
    return (
      <div className="flex flex-col items-center gap-3 py-12 text-center">
        <AlertTriangle className="h-8 w-8 text-amber-500" />
        <div>
          <p className="text-body text-muted-foreground">Couldn&apos;t load activity.</p>
          <p className="text-label text-muted-foreground mt-1">{error}</p>
        </div>
        <button
          type="button"
          onClick={() => { refresh() }}
          className="text-label text-primary underline underline-offset-2 hover:no-underline"
        >
          Retry
        </button>
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
        {loading ? "Updating…" : "Live"}
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
                  const hasDetail = !!item.detail
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
                          {item.from_slug ? `@${item.from_slug}` : "—"}
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
                              {item.crew_name ?? "—"}
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
