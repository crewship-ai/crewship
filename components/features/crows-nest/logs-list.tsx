"use client"

import { useMemo, useState, useCallback } from "react"
import { Virtuoso } from "react-virtuoso"
import { ChevronRight, ChevronDown } from "lucide-react"
import { cn } from "@/lib/utils"
import type { JournalEntry } from "@/lib/types/journal"
import {
  GROUP_COLOR,
  SEVERITY_BG_CLASS,
  SEVERITY_COLOR,
  groupOf,
  pillLabelOf,
  severityOf,
} from "@/lib/journal-style"
import { formatRelativeTime } from "@/lib/time"

interface LogsListProps {
  entries: JournalEntry[]
  wrap: boolean
  /** When true, autoscroll sticks to the bottom (or top, depending on order). */
  followTail: boolean
  newestFirst: boolean
}

/**
 * Virtualized log stream rendered Grafana Explore-style:
 *   ┃ severity-bar │ HH:MM:SS.mmm │ type-pill │ summary │ age │ ▸
 * Click a row to expand it inline and reveal payload + refs as
 * formatted JSON. Multiple rows can be open at once.
 */
export function LogsList({ entries, wrap, followTail, newestFirst }: LogsListProps) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  const toggleExpand = useCallback((id: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

  // Stable derived list — newestFirst controlled by parent.
  const list = useMemo(() => entries, [entries])

  if (list.length === 0) {
    return (
      <div className="h-full flex items-center justify-center text-[12px] text-muted-foreground/60 italic">
        No log entries match the current filter.
      </div>
    )
  }

  const followOutput = followTail ? (newestFirst ? false : "auto") : false

  return (
    <Virtuoso
      data={list}
      followOutput={followOutput as false | "auto"}
      defaultItemHeight={26}
      computeItemKey={(_, e) => e.id}
      itemContent={(_, e) => (
        <LogRow
          entry={e}
          wrap={wrap}
          expanded={expanded.has(e.id)}
          onToggle={() => toggleExpand(e.id)}
        />
      )}
      className="h-full"
    />
  )
}

function LogRow({
  entry,
  wrap,
  expanded,
  onToggle,
}: {
  entry: JournalEntry
  wrap: boolean
  expanded: boolean
  onToggle: () => void
}) {
  const sev = severityOf(entry.severity)
  const grp = groupOf(entry.entry_type)
  const pill = pillLabelOf(entry.entry_type)
  const ts = entry.ts
  const tsLabel = formatTime(ts)

  return (
    <div
      onClick={onToggle}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault()
          onToggle()
        }
      }}
      className={cn(
        "group grid gap-2 px-2 py-[3px] items-start cursor-pointer text-[12px] leading-[18px] border-b border-border/30 hover:bg-accent/20",
        expanded && "bg-sky-500/5",
      )}
      style={{ gridTemplateColumns: "3px 96px 110px minmax(0,1fr) 70px 14px" }}
    >
      <span
        className={cn("self-stretch rounded-sm", SEVERITY_BG_CLASS[sev])}
        aria-hidden
      />
      <time
        dateTime={ts}
        className="font-mono text-[11px] tabular-nums text-muted-foreground/80 truncate"
      >
        {tsLabel}
      </time>
      <span className="flex items-center">
        <span
          className="inline-flex items-center px-1.5 h-[16px] rounded-sm text-[10px] font-mono border border-border/50 bg-card"
          style={{ color: GROUP_COLOR[grp] }}
          title={entry.entry_type}
        >
          {pill}
        </span>
      </span>
      <span
        className={cn(
          "font-mono text-foreground/90 min-w-0",
          wrap ? "whitespace-pre-wrap break-words" : "truncate whitespace-nowrap",
        )}
      >
        {entry.summary || "—"}
      </span>
      <span className="text-right font-mono text-[10px] tabular-nums text-muted-foreground/70">
        {formatRelativeTime(ts)}
      </span>
      <span className="text-muted-foreground/60 text-[10px] leading-[18px]">
        {expanded ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
      </span>

      {expanded && (
        <div className="col-start-2 col-end-7 mt-1 mb-1 rounded border border-border/60 bg-card/60">
          <Detail entry={entry} sev={sev} />
        </div>
      )}
    </div>
  )
}

function Detail({ entry, sev }: { entry: JournalEntry; sev: ReturnType<typeof severityOf> }) {
  const meta: Array<[string, string | undefined]> = [
    ["entry_type", entry.entry_type],
    ["severity", entry.severity as string],
    ["actor_type", entry.actor_type],
    ["actor_id", entry.actor_id],
    ["agent_id", entry.agent_id],
    ["crew_id", entry.crew_id],
    ["mission_id", entry.mission_id],
    ["trace_id", entry.trace_id],
  ]
  return (
    <div className="p-2 space-y-2">
      <div className="flex flex-wrap gap-x-3 gap-y-1 text-[10px] font-mono">
        {meta.map(([k, v]) =>
          v ? (
            <span key={k} className="text-muted-foreground">
              <span className="opacity-60">{k}:</span>{" "}
              <span
                className="text-foreground/85"
                style={k === "severity" ? { color: SEVERITY_COLOR[sev] } : undefined}
              >
                {v}
              </span>
            </span>
          ) : null,
        )}
      </div>
      {entry.payload && Object.keys(entry.payload).length > 0 && (
        <DetailJson title="payload" value={entry.payload} />
      )}
      {entry.refs && Object.keys(entry.refs).length > 0 && (
        <DetailJson title="refs" value={entry.refs} />
      )}
    </div>
  )
}

function DetailJson({ title, value }: { title: string; value: unknown }) {
  let text: string
  try {
    text = JSON.stringify(value, null, 2)
  } catch {
    text = String(value)
  }
  return (
    <div>
      <div className="text-[10px] uppercase tracking-wider text-muted-foreground mb-0.5">{title}</div>
      <pre className="text-[11px] font-mono text-foreground/85 bg-background/60 border border-border/40 rounded p-2 overflow-x-auto whitespace-pre">
        {text}
      </pre>
    </div>
  )
}

function formatTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const hh = String(d.getHours()).padStart(2, "0")
  const mm = String(d.getMinutes()).padStart(2, "0")
  const ss = String(d.getSeconds()).padStart(2, "0")
  const ms = String(d.getMilliseconds()).padStart(3, "0")
  return `${hh}:${mm}:${ss}.${ms}`
}
