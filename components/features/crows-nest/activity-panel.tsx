"use client"

import { useMemo } from "react"
import { Activity } from "lucide-react"
import { cn } from "@/lib/utils"
import { formatRelativeTime } from "@/lib/time"
import type { JournalEntry } from "@/lib/types/journal"

interface ActivityPanelProps {
  entries: JournalEntry[]
}

const SEVERITY_DOT: Record<string, string> = {
  info: "bg-sky-400",
  notice: "bg-violet-400",
  warn: "bg-amber-400",
  error: "bg-red-400",
}

const SEVERITY_LABEL: Record<string, string> = {
  info: "text-sky-300",
  notice: "text-violet-300",
  warn: "text-amber-300",
  error: "text-red-300",
}

const TYPE_LABEL: Record<string, string> = {
  "exec.command": "exec",
  "exec.output_chunk": "stdout",
  "network.egress": "egress",
  "network.port_opened": "port↑",
  "network.port_closed": "port↓",
  "file.written": "file",
  "container.metrics": "stats",
  "container.snapshot": "snapshot",
  "agent.status_change": "status",
  "run.started": "run·start",
  "run.completed": "run·done",
  "run.failed": "run·fail",
  "run.cancelled": "run·cancel",
  "peer.conversation": "peer",
  "peer.escalation": "escalate",
  "keeper.decision": "keeper",
  "keeper.request": "keeper·req",
  "mission.status_change": "mission",
  "assignment.created": "assign",
  "assignment.completed": "assign·done",
  "assignment.failed": "assign·fail",
  "task.delegated": "delegate",
  "approval.requested": "approval",
  "approval.granted": "approval·ok",
  "approval.denied": "approval·no",
  "cost.incurred": "cost",
  "budget.warning": "budget·warn",
  "budget.exceeded": "budget·over",
  "skill.assigned": "skill+",
  "memory.updated": "memory",
  "system.compaction": "compact",
}

/**
 * Compact chronological feed of every journal event for the crew that
 * doesn't already have a dedicated panel. Designed so an empty Resources
 * + Filesystem area still leaves the user with visible signal of what
 * the crew is doing — runs starting / finishing, peer messages, keeper
 * decisions, etc.
 *
 * Filtering is done in the parent (severity sidebar already applies);
 * this component just renders newest-first up to a cap.
 */
export function ActivityPanel({ entries }: ActivityPanelProps) {
  const rows = useMemo(() => entries.slice(0, 80), [entries])

  return (
    <div className="flex flex-col h-full bg-card border border-border/50 rounded-lg overflow-hidden">
      <div className="flex items-center justify-between px-3 py-1.5 bg-muted/40 border-b border-border/50 shrink-0">
        <div className="flex items-center gap-2">
          <Activity className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="text-[11px] text-muted-foreground font-medium">Activity</span>
        </div>
        <span className="text-[10px] text-muted-foreground/70 font-mono tabular-nums">
          {rows.length}
        </span>
      </div>

      <div className="flex-1 min-h-0 overflow-auto">
        {rows.length === 0 ? (
          <div className="text-[11px] text-muted-foreground/60 italic p-3">
            No activity yet — events appear as the crew runs.
          </div>
        ) : (
          <ul className="divide-y divide-border/40">
            {rows.map((e) => {
              const sev = e.severity || "info"
              const typeLabel = TYPE_LABEL[e.entry_type] ?? e.entry_type
              return (
                <li key={e.id} className="px-3 py-1.5 flex items-start gap-2 text-[11px] hover:bg-accent/20">
                  <span
                    className={cn("mt-1 h-1.5 w-1.5 rounded-full shrink-0", SEVERITY_DOT[sev] ?? SEVERITY_DOT.info)}
                    aria-label={sev}
                  />
                  <code className={cn("font-mono shrink-0 tabular-nums", SEVERITY_LABEL[sev] ?? SEVERITY_LABEL.info)}>
                    {typeLabel}
                  </code>
                  <span className="flex-1 min-w-0 truncate text-foreground/85">
                    {e.summary || "—"}
                  </span>
                  <time
                    dateTime={e.ts}
                    className="text-[10px] text-muted-foreground/70 font-mono tabular-nums shrink-0"
                  >
                    {formatRelativeTime(e.ts)}
                  </time>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </div>
  )
}
