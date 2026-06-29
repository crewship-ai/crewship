"use client"

import { useCallback, useMemo } from "react"
import { cn } from "@/lib/utils"
import { formatLogTime } from "@/lib/utils/log-format"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalStream } from "@/hooks/use-journal-stream"
import type { JournalEntry } from "@/lib/types/journal"
import {
  humanizeRun,
  isRunInFlight,
  type RunActivityRow,
  type RunActivityTone,
} from "@/lib/run-activity"

// RunActivityTimeline — the readable "what the agent did" rail for a single
// run. Pulls journal entries (filtered by the given params, e.g. a mission or
// a trace), humanizes each into a one-liner (lib/run-activity), and renders the
// rail-with-detail layout: a vertical connector, a toned icon node per step,
// title + right-aligned meta, and an optional second line for the concrete
// target (path / command / url).
//
// The presentational RunActivityRail is exported so other run sources (routine
// pipeline runs, the global Activity Bar) render the same rail without
// re-deriving the journal fetch.

const TONE_ICON: Record<RunActivityTone, string> = {
  active: "text-blue-400",
  success: "text-emerald-400",
  warn: "text-amber-400",
  error: "text-red-400",
  default: "text-foreground/50",
}

// The journal entry types that represent actual agent WORK during a run.
// Issue pages filter to these so the "Run activity" rail stays distinct from
// the existing issue-lifecycle "Activity" feed (assignee/status changes).
export const RUN_WORK_ENTRY_TYPES = [
  "run.started",
  "run.completed",
  "run.failed",
  "run.cancelled",
  "run.timeout",
  "assignment.running",
  "assignment.completed",
  "assignment.failed",
  "exec.command",
  "file.written",
  "network.egress",
  "network.port_opened",
  "llm.call",
  "keeper.request",
  "keeper.decision",
] as const

interface RunActivityTimelineProps {
  workspaceId: string | null
  /**
   * Journal filter params — e.g. `{ mission_id }` for an issue run or
   * `{ trace_id }` for a standalone run. The rail enables once at least one
   * value is non-empty.
   */
  params: Record<string, string | undefined>
  /** Subscribe to the live journal stream for in-flight runs. Default true. */
  live?: boolean
  /** Header label. Default "Run activity". */
  title?: string
  /** Hide the whole section when there's nothing to show (default true). */
  hideWhenEmpty?: boolean
  /**
   * Treat the run as in-flight even before its first journal row lands (e.g.
   * an issue is IN_PROGRESS but the container is still booting). Drives the
   * "waiting for the first step…" empty state instead of "no activity".
   */
  forceRunning?: boolean
  className?: string
}

export function RunActivityTimeline({
  workspaceId,
  params,
  live = true,
  title = "Run activity",
  hideWhenEmpty = true,
  forceRunning = false,
  className,
}: RunActivityTimelineProps) {
  const hasFilter = Object.values(params).some((v) => !!v)
  const enabled = !!workspaceId && hasFilter

  // Memoise on the serialised params so object-literal callers don't churn
  // the fetch/stream effects every render.
  const paramsKey = JSON.stringify(params)
  const stableParams = useMemo(() => params, [paramsKey]) // eslint-disable-line react-hooks/exhaustive-deps

  const { entries, loading, prependLive } = useJournalList({
    workspaceId,
    params: stableParams,
    enabled,
    // A single run is bounded; cap defensively against a pathological filter.
    maxEntries: 1000,
  })

  // Live tail: prepend entries matching this filter as they land. The stream
  // may be workspace-wide, so guard client-side on the same keys we filter by.
  const onEntry = useCallback(
    (entry: JournalEntry) => {
      if (params.trace_id && entry.trace_id !== params.trace_id) return
      if (params.mission_id && entry.mission_id !== params.mission_id) return
      prependLive(entry)
    },
    [params.trace_id, params.mission_id, prependLive],
  )
  useJournalStream({ workspaceId, params: stableParams, enabled: enabled && live, onEntry })

  const rows = useMemo(() => humanizeRun(entries), [entries])
  const detectedRunning = useMemo(() => isRunInFlight(entries.map((e) => e.entry_type)), [entries])
  // Before the first row lands, fall back to the caller's hint so the empty
  // state reads "waiting…" rather than "no activity recorded".
  const running = detectedRunning || (forceRunning && entries.length === 0)

  if (!enabled) return null
  if (hideWhenEmpty && !loading && rows.length === 0 && !running) return null

  return (
    <RunActivityRail
      rows={rows}
      running={running}
      loading={loading}
      title={title}
      className={className}
    />
  )
}

interface RunActivityRailProps {
  rows: RunActivityRow[]
  running?: boolean
  /**
   * Run is parked on a human approval. Renders an amber "Waiting for approval"
   * status in the header instead of the blue "Running" pulse. Takes precedence
   * over `running` since a parked run is technically in-flight but the human,
   * not the agent, is the bottleneck.
   */
  waiting?: boolean
  loading?: boolean
  title?: string
  emptyLabel?: string
  className?: string
}

/**
 * Presentational rail. Renders humanized rows with the connector + toned
 * icon nodes. Source-agnostic — fed by the journal timeline, pipeline runs,
 * or the Activity Bar.
 */
export function RunActivityRail({
  rows,
  running = false,
  waiting = false,
  loading = false,
  title = "Run activity",
  emptyLabel,
  className,
}: RunActivityRailProps) {
  return (
    <div className={cn("border-t border-white/[0.06] pt-3 px-4 pb-4", className)}>
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <span className="text-[11px] font-semibold text-foreground/80">{title}</span>
          {waiting ? (
            <span className="inline-flex items-center gap-1 text-[10px] text-amber-400">
              <span className="h-1.5 w-1.5 rounded-full bg-amber-400 animate-pulse" />
              Waiting for approval
            </span>
          ) : running ? (
            <span className="inline-flex items-center gap-1 text-[10px] text-blue-400">
              <span className="h-1.5 w-1.5 rounded-full bg-blue-400 animate-pulse" />
              Running
            </span>
          ) : null}
        </div>
        {rows.length > 0 && (
          <span className="text-[10px] text-foreground/35 tabular-nums">{rows.length} steps</span>
        )}
      </div>

      {loading && rows.length === 0 ? (
        <p className="text-[11px] text-foreground/40">Loading activity…</p>
      ) : rows.length === 0 ? (
        <p className="text-[11px] text-foreground/40">
          {running ? "Waiting for the first step…" : emptyLabel ?? "No activity recorded for this run"}
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
    <li
      className={cn(
        "flex items-start gap-2.5 py-1.5 relative",
        // Parked-on-approval row: amber tint + ring so the blocked step reads
        // as "this is on you" rather than another grey completed line.
        row.awaiting && "-mx-2 rounded-md bg-amber-500/[0.06] px-2 ring-1 ring-inset ring-amber-500/20",
      )}
    >
      {/* Connector rail down to the next node. */}
      {!last && <div className="absolute left-[7px] top-[24px] w-px h-[calc(100%-8px)] bg-white/[0.06]" />}
      <Icon
        className={cn(
          "h-3.5 w-3.5 shrink-0 mt-0.5",
          TONE_ICON[row.tone],
          row.awaiting && "animate-pulse",
        )}
      />
      <div className="flex-1 min-w-0">
        <div className="flex items-baseline gap-2">
          <span className="text-[10px] text-foreground/30 tabular-nums shrink-0">
            {formatLogTime(row.ts).slice(11, 19)}
          </span>
          <span
            className={cn(
              "text-[11px] truncate",
              row.awaiting
                ? "font-semibold text-amber-300"
                : row.tone === "error"
                  ? "text-red-300/90"
                  : "text-foreground/85 font-medium",
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
