"use client"

// One line inside an activity card.
//
// Shaped like the "Recent runs" row on the Routines overview — square icon
// tile with a status dot, title, one muted sub-line, then meta pinned right
// — rather than the seven-column log grid this page had first. A card holds
// a handful of rows, so each one can afford to be legible; density belongs
// on /journal, which is the surface for reading everything.

import * as React from "react"
import type { LucideIcon } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { cn } from "@/lib/utils"
import { relTime } from "@/lib/time"
import {
  activitySource,
  buildSpine,
  entryCostUSD,
  entryDurationMs,
  formatDurationMs,
  scopeOf,
  sourceMeta,
  type SpineLabels,
  type SpineLink,
} from "@/lib/activity-stream"
import type { JournalEntry } from "@/lib/types/journal"

const SCOPE_DOT: Record<string, string> = {
  active: "bg-primary animate-pulse",
  waiting: "bg-warn animate-pulse",
  failed: "bg-destructive",
  done: "bg-success",
}

export interface FeedRowProps {
  entry: JournalEntry
  icon: LucideIcon
  labels: SpineLabels
  actorName?: string
  crewName?: string
  /** Real portrait for the actor — an agent's face, else its crew's mark. */
  agentId?: string | null
  crewIcon?: string | null
  crewColor?: string | null
  selected?: boolean
  onSelect: () => void
  onSpineClick?: (link: SpineLink) => void
}

export function FeedRow({
  entry,
  icon: Icon,
  labels,
  actorName,
  crewName,
  agentId,
  crewIcon,
  crewColor,
  selected,
  onSelect,
  onSpineClick,
}: FeedRowProps) {
  const meta = sourceMeta(activitySource(entry.entry_type))
  const spine = buildSpine(entry, labels)
  const duration = entryDurationMs(entry)
  const cost = entryCostUSD(entry)

  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={selected}
      className={cn(
        "group grid w-full grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 rounded-md px-1.5 py-2 text-left",
        "transition-colors hover:bg-white/[0.03]",
        selected && "bg-white/[0.05]",
      )}
    >
      {/* Who did it, not what kind of row it is. An agent gets its own
          portrait and a crew its own mark — the same ones the rest of the
          app draws — and only a system event with no actor falls back to
          the source glyph. A column of identical grey squares is a column
          you stop reading. */}
      <span className="relative shrink-0">
        {agentId ? (
          <AgentAvatar seed={agentId} alt={actorName || ""} className="h-6 w-6 rounded-md" />
        ) : crewIcon || crewColor ? (
          <CrewIcon icon={crewIcon ?? "users"} color={crewColor} size="sm" className="!h-6 !w-6 !rounded-md" />
        ) : (
          <span
            className="flex h-6 w-6 items-center justify-center rounded-md"
            style={{ background: `color-mix(in oklab, var(${meta.token}) 18%, transparent)` }}
          >
            <Icon className="h-3 w-3" style={{ color: `var(${meta.token})` }} />
          </span>
        )}
        <span
          aria-hidden
          className={cn(
            "absolute -bottom-0.5 -right-0.5 h-2 w-2 rounded-full ring-2 ring-card",
            SCOPE_DOT[scopeOf(entry)] ?? "bg-muted",
          )}
        />
      </span>

      <span className="min-w-0">
        <span className="block truncate text-[12.5px] text-foreground/90">{entry.summary}</span>
        <span className="mt-0.5 flex items-center gap-1.5 text-[10.5px] text-muted-foreground-soft">
          {actorName && <span className="truncate">{actorName}</span>}
          {crewName && <span className="truncate">· {crewName}</span>}
          {spine.map((l) => (
            <React.Fragment key={`${l.kind}-${l.id}`}>
              <span aria-hidden>›</span>
              <span
                role={onSpineClick ? "link" : undefined}
                tabIndex={onSpineClick ? 0 : undefined}
                onClick={
                  onSpineClick
                    ? (e) => {
                        e.stopPropagation()
                        onSpineClick(l)
                      }
                    : undefined
                }
                onKeyDown={
                  onSpineClick
                    ? (e) => {
                        if (e.key === "Enter") {
                          e.stopPropagation()
                          onSpineClick(l)
                        }
                      }
                    : undefined
                }
                className={cn(
                  "max-w-[140px] truncate rounded bg-white/[0.05] px-1 py-px",
                  onSpineClick && "cursor-pointer hover:bg-white/[0.1] hover:text-foreground",
                  l.kind === "issue" && "font-mono text-info",
                )}
              >
                {l.label}
              </span>
            </React.Fragment>
          ))}
        </span>
      </span>

      <span className="flex shrink-0 items-center gap-3 font-mono text-[10.5px] tabular-nums text-muted-foreground-soft">
        {cost != null && <span>${cost.toFixed(3)}</span>}
        {duration != null && <span>{formatDurationMs(duration)}</span>}
        <span className="w-12 text-right">{relTime(entry.ts)}</span>
      </span>
    </button>
  )
}
