"use client"

/**
 * The work ledger as a list
 * (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §9: "the UI shows
 * source, agent/session, queued reason, eligible time, attempt, state, cost
 * and result").
 *
 * One row per work item, and a row is a work item — never an agent and never a
 * conversation. Grouping by either is the merge §9 forbids: one agent can hold
 * several concurrent items and one session can hold a queue of turns, and a
 * list keyed on those shows a reader one row where there are three pieces of
 * work.
 *
 * Layout keys on width (`md:`), touch sizing on the pointer (`coarse:`) —
 * `--spacing` is 0.23rem here, so `h-12` is the 44px target.
 */

import * as React from "react"
import { Webhook, MessageSquare, ClipboardList, CalendarClock, GitBranch, Hand } from "lucide-react"

import { EmptyState } from "@/components/ui/detail"
import { cn } from "@/lib/utils"
import { relTime } from "@/lib/time"
import {
  attemptCount,
  queuedReason,
  workOutcome,
  type WorkItem,
  type WorkSource,
} from "@/hooks/use-work-items"
import { WorkStatePill } from "./work-state-pill"

const SOURCE_ICON: Record<WorkSource, React.ComponentType<{ className?: string }>> = {
  webhook: Webhook,
  chat: MessageSquare,
  assignment: ClipboardList,
  schedule: CalendarClock,
  pipeline_step: GitBranch,
  manual: Hand,
}

const SOURCE_LABEL: Record<WorkSource, string> = {
  webhook: "Webhook",
  chat: "Chat",
  assignment: "Assignment",
  schedule: "Schedule",
  pipeline_step: "Pipeline step",
  manual: "Manual",
}

/** An id shortened for a table cell, with the whole value on the title so it
 *  is still copyable and still checkable. */
function shortId(id: string): string {
  return id.length > 10 ? `${id.slice(0, 10)}…` : id
}

export function formatCost(usd: number): string {
  if (!Number.isFinite(usd) || usd === 0) return "—"
  return usd < 0.01 ? `<$0.01` : `$${usd.toFixed(2)}`
}

export interface WorkItemsListProps {
  items: readonly WorkItem[]
  selectedId?: string | null
  onSelect: (item: WorkItem) => void
  loading?: boolean
  emptyTitle?: string
  emptyDescription?: string
}

export function WorkItemsList({
  items,
  selectedId,
  onSelect,
  loading = false,
  emptyTitle = "No work in the ledger",
  emptyDescription = "Nothing has been accepted for this workspace yet. A webhook, a chat turn, a schedule or an assignment all land here the moment they are accepted.",
}: WorkItemsListProps) {
  if (!loading && items.length === 0) {
    return <EmptyState icon={ClipboardList} title={emptyTitle} description={emptyDescription} />
  }
  return (
    <div role="list" aria-label="Work items" className="divide-y divide-hairline">
      {items.map((item) => (
        <WorkItemRow
          key={item.id}
          item={item}
          selected={item.id === selectedId}
          onSelect={() => onSelect(item)}
        />
      ))}
    </div>
  )
}

function WorkItemRow({
  item,
  selected,
  onSelect,
}: {
  item: WorkItem
  selected: boolean
  onSelect: () => void
}) {
  const SourceIcon = SOURCE_ICON[item.source] ?? Hand
  const reason = queuedReason(item)
  const outcome = workOutcome(item.state)
  const attempts = attemptCount(item)

  return (
    <div role="listitem">
      <button
        type="button"
        onClick={onSelect}
        aria-current={selected ? "true" : undefined}
        data-work-item-id={item.id}
        className={cn(
          "flex w-full flex-col gap-2 px-3 py-2.5 text-left transition-colors",
          "coarse:min-h-12 hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40",
          "md:grid md:grid-cols-[minmax(0,1.4fr)_minmax(0,1.6fr)_minmax(0,1.2fr)_auto] md:items-center md:gap-3",
          selected && "bg-primary/[0.07]",
        )}
      >
        {/* Source + class */}
        <span className="flex min-w-0 items-center gap-2">
          <SourceIcon className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
          <span className="min-w-0">
            <span className="block truncate text-[13px] text-foreground/90">
              {SOURCE_LABEL[item.source] ?? item.source}
            </span>
            <span className="block truncate font-mono text-[10px] text-muted-foreground/70" title={item.id}>
              {shortId(item.id)}
              {item.replay_of ? " · replay" : ""}
            </span>
          </span>
        </span>

        {/* Agent / session — separate values, never collapsed into one */}
        <span className="min-w-0 text-[11px] text-muted-foreground">
          <span className="block truncate" title={item.agent_id || undefined}>
            {item.agent_id ? (
              <>agent <span className="font-mono">{shortId(item.agent_id)}</span></>
            ) : (
              <span className="text-muted-foreground/60">no agent</span>
            )}
          </span>
          <span className="block truncate" title={item.session_id || undefined}>
            {item.session_id ? (
              <>session <span className="font-mono">{shortId(item.session_id)}</span></>
            ) : (
              <span className="text-muted-foreground/60">{item.class === "chat" ? "no session" : "background"}</span>
            )}
          </span>
        </span>

        {/* Why it is not running, and when it may */}
        <span className="min-w-0 text-[11px] text-muted-foreground">
          {reason ? <span className="block truncate" title={reason}>{reason}</span> : null}
          <span className="block truncate text-muted-foreground/70">
            {outcome === "waiting" ? `eligible ${relTime(item.eligible_at)}` : `updated ${relTime(item.updated_at)}`}
            {attempts > 0 ? ` · attempt ${attempts}` : ""}
          </span>
        </span>

        <span className="flex shrink-0 items-center gap-2 md:justify-end">
          <WorkStatePill state={item.state} />
        </span>
      </button>
    </div>
  )
}
