"use client"

import { useCallback, useMemo } from "react"
import { cn } from "@/lib/utils"
import { formatLogTime } from "@/lib/utils/log-format"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalStream } from "@/hooks/use-journal-stream"
import { humanizeRun, type RunActivityRow, type RunActivityTone } from "@/lib/run-activity"

// RunActivityTimeline — the readable "what the agent did" rail for a single
// run. Pulls every journal entry sharing one `trace_id`, humanizes each into a
// one-liner (lib/run-activity), and renders the rail-with-detail layout: a
// vertical connector, a toned icon node per step, title + right-aligned meta,
// and an optional second line for the concrete target (path / command / url).
//
// Shared surface: mounted on the issue detail page, the routine run view, and
// (later) the global Activity Bar — anywhere we have a trace_id and want to
// show progress without dumping raw logs.

const TONE_ICON: Record<RunActivityTone, string> = {
  active: "text-blue-400",
  success: "text-emerald-400",
  warn: "text-amber-400",
  error: "text-red-400",
  default: "text-foreground/50",
}

interface RunActivityTimelineProps {
  workspaceId: string | null
  /** The run's trace_id; all spans for one run share it. */
  traceId: string | null
  /** Subscribe to the live journal stream for in-flight runs. Default true. */
  live?: boolean
  /** Header label. Default "Run activity". */
  title?: string
  className?: string
}

export function RunActivityTimeline({
  workspaceId,
  traceId,
  live = true,
  title = "Run activity",
  className,
}: RunActivityTimelineProps) {
  const enabled = !!workspaceId && !!traceId
  const params = useMemo(() => ({ trace_id: traceId ?? undefined }), [traceId])

  const { entries, loading, prependLive } = useJournalList({
    workspaceId,
    params,
    enabled,
    // A single run is bounded; cap defensively against a pathological trace.
    maxEntries: 1000,
  })

  // Live tail: prepend entries for THIS trace as they land. The stream may be
  // workspace-wide, so guard on trace_id client-side regardless of server
  // filtering. prependLive dedupes by id, so a poll/stream overlap is safe.
  const onEntry = useCallback(
    (entry: Parameters<typeof prependLive>[0]) => {
      if (traceId && entry.trace_id && entry.trace_id !== traceId) return
      prependLive(entry)
    },
    [traceId, prependLive],
  )
  useJournalStream({ workspaceId, params, enabled: enabled && live, onEntry })

  const rows = useMemo(() => humanizeRun(entries), [entries])

  // A run is "in flight" when it has opened but not reached a terminal entry.
  const running = useMemo(() => {
    let opened = false
    let terminal = false
    for (const e of entries) {
      if (e.entry_type === "run.started" || e.entry_type === "assignment.running") opened = true
      if (
        e.entry_type === "run.completed" ||
        e.entry_type === "run.failed" ||
        e.entry_type === "run.cancelled" ||
        e.entry_type === "run.timeout" ||
        e.entry_type === "assignment.completed" ||
        e.entry_type === "assignment.failed"
      )
        terminal = true
    }
    return opened && !terminal
  }, [entries])

  if (!enabled) return null

  return (
    <div className={cn("border-t border-white/[0.06] pt-3 px-4 pb-4", className)}>
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <span className="text-[11px] font-semibold text-foreground/80">{title}</span>
          {running && (
            <span className="inline-flex items-center gap-1 text-[10px] text-blue-400">
              <span className="h-1.5 w-1.5 rounded-full bg-blue-400 animate-pulse" />
              Running
            </span>
          )}
        </div>
        {rows.length > 0 && (
          <span className="text-[10px] text-foreground/35 tabular-nums">{rows.length} steps</span>
        )}
      </div>

      {loading && rows.length === 0 ? (
        <p className="text-[11px] text-foreground/40">Loading activity…</p>
      ) : rows.length === 0 ? (
        <p className="text-[11px] text-foreground/40">
          {running ? "Waiting for the first step…" : "No activity recorded for this run"}
        </p>
      ) : (
        <ol className="space-y-0">
          {rows.map((row, i) => (
            <RunActivityRowView key={row.id} row={row} last={i === rows.length - 1} />
          ))}
        </ol>
      )}
    </div>
  )
}

function RunActivityRowView({ row, last }: { row: RunActivityRow; last: boolean }) {
  const Icon = row.icon
  return (
    <li className="flex items-start gap-2.5 py-1.5 relative">
      {/* Connector rail down to the next node. */}
      {!last && <div className="absolute left-[7px] top-[24px] w-px h-[calc(100%-8px)] bg-white/[0.06]" />}
      <Icon className={cn("h-3.5 w-3.5 shrink-0 mt-0.5", TONE_ICON[row.tone])} />
      <div className="flex-1 min-w-0">
        <div className="flex items-baseline gap-2">
          <span className="text-[10px] text-foreground/30 tabular-nums shrink-0">
            {formatLogTime(row.ts).slice(11, 19)}
          </span>
          <span
            className={cn(
              "text-[11px] truncate",
              row.tone === "error" ? "text-red-300/90" : "text-foreground/85 font-medium",
            )}
          >
            {row.title}
          </span>
          {row.meta && (
            <span className="ml-auto text-[10px] text-foreground/40 tabular-nums shrink-0 pl-2">
              {row.meta}
            </span>
          )}
        </div>
        {row.detail && (
          <p className="mt-0.5 text-[10px] text-foreground/45 font-mono truncate" title={row.detail}>
            {row.detail}
          </p>
        )}
      </div>
    </li>
  )
}
