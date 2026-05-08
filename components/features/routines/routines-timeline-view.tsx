"use client"

import { useEffect, useRef, useState } from "react"
import { Clock } from "lucide-react"
import type { Pipeline } from "@/hooks/use-pipelines"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { useRealtimeEvent } from "@/hooks/use-realtime"

// RoutinesTimelineView — vertical timeline of all recent routine runs
// across the workspace, grouped by day. Pulls from the same journal
// endpoint that backs the orchestration Activity tab, filtered by
// pipeline.run.* entry types.

interface Props {
  workspaceId: string
  routines: Pipeline[]
  onSelect: (slug: string) => void
}

interface RunEntry {
  id: string
  ts: string
  entry_type: string
  severity: string
  summary: string
  pipeline_id?: string
  run_id?: string
  payload?: Record<string, unknown>
}

export function RoutinesTimelineView({ workspaceId, routines, onSelect }: Props) {
  const [runs, setRuns] = useState<RunEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [window, setWindow] = useState<"24h" | "7d" | "30d">("24h")
  // abortRef: realtime events + window changes can fire fetches in
  // quick succession; without cancellation the older request can
  // resolve last and clobber the freshly-windowed result.
  const abortRef = useRef<AbortController | null>(null)

  const fetchRuns = async () => {
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    setError(null)
    try {
      const since = windowToISO(window)
      const url = `/api/v1/workspaces/${workspaceId}/journal?entry_type_prefix=pipeline.run.&since=${encodeURIComponent(since)}&limit=200`
      const res = await fetch(url, { signal: ctrl.signal })
      if (ctrl.signal.aborted) return
      if (!res.ok) {
        setError(`journal: ${res.status}`)
        setRuns([])
        return
      }
      const data: RunEntry[] = await res.json()
      if (ctrl.signal.aborted) return
      setRuns(Array.isArray(data) ? data : [])
    } catch (e) {
      if (ctrl.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!ctrl.signal.aborted) setLoading(false)
    }
  }

  useEffect(() => {
    fetchRuns()
    return () => {
      abortRef.current?.abort()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId, window])

  // Refresh on completion events so the timeline updates without
  // polling. Step-level events are not consumed here — the run-level
  // signal is enough for timeline granularity.
  useRealtimeEvent("pipeline.run.completed", fetchRuns)
  useRealtimeEvent("pipeline.run.failed", fetchRuns)
  useRealtimeEvent("pipeline.run.started", fetchRuns)

  const slugByPipelineId = new Map(routines.map((r) => [r.id, r.slug]))
  const grouped = groupByDay(runs)

  return (
    <div className="p-4">
      {/* Window selector */}
      <div className="mb-3 flex items-center gap-2">
        <span className="text-[11px] uppercase tracking-wider text-muted-foreground">Window</span>
        {(["24h", "7d", "30d"] as const).map((w) => (
          <button
            key={w}
            onClick={() => setWindow(w)}
            className={cn(
              "rounded px-2 py-0.5 text-xs transition-colors",
              window === w ? "bg-blue-500/15 text-blue-300" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {w}
          </button>
        ))}
        <div className="flex-1" />
        <span className="text-[11px] text-muted-foreground">{runs.length} entries</span>
      </div>

      {loading && <div className="py-8 text-center text-sm text-muted-foreground">Loading…</div>}
      {error && (
        <div className="py-4 text-sm text-amber-400">
          Could not load timeline ({error}). The journal endpoint may not be wired for routine
          filtering yet — view runs in /orchestration → Activity meanwhile.
        </div>
      )}
      {!loading && !error && runs.length === 0 && (
        <div className="rounded-md border border-dashed border-border/60 p-8 text-center">
          <Clock className="mx-auto mb-3 h-8 w-8 text-muted-foreground/40" />
          <p className="text-sm font-medium">No routine runs in the last {window}</p>
          <p className="mt-1 text-xs text-muted-foreground">
            When a routine is invoked (manually, by an agent, on a schedule, or via webhook), it
            shows up here.
          </p>
        </div>
      )}

      {grouped.map(({ day, items }) => (
        <div key={day} className="mb-5">
          <div className="sticky top-0 z-10 mb-2 -mx-4 border-b border-white/[0.06] bg-background/80 px-4 py-1 text-[11px] font-medium uppercase tracking-wider text-muted-foreground backdrop-blur-sm">
            {day}
          </div>
          <ol className="space-y-1.5">
            {items.map((r) => (
              <TimelineItem
                key={r.id}
                run={r}
                slug={slugByPipelineId.get(r.pipeline_id ?? "") ?? r.pipeline_id}
                onClick={() => {
                  const slug = slugByPipelineId.get(r.pipeline_id ?? "")
                  if (slug) onSelect(slug)
                }}
              />
            ))}
          </ol>
        </div>
      ))}
    </div>
  )
}

function TimelineItem({
  run,
  slug,
  onClick,
}: {
  run: RunEntry
  slug?: string
  onClick: () => void
}) {
  const kind = run.entry_type.replace("pipeline.run.", "")
  const dotColor =
    kind === "completed" ? "bg-emerald-500" : kind === "failed" ? "bg-red-500" : "bg-blue-500"

  return (
    <li
      onClick={onClick}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter") onClick()
      }}
      className="group flex items-start gap-3 rounded px-3 py-2 hover:bg-muted/40 cursor-pointer"
    >
      <div className="flex flex-col items-center pt-0.5">
        <span className={cn("h-2 w-2 rounded-full", dotColor)} />
        <span className="mt-1 h-full w-px bg-border/40" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="font-mono text-[10px] text-muted-foreground">{shortTime(run.ts)}</span>
          <Badge variant="outline" className="text-[10px] capitalize">
            {kind}
          </Badge>
          {slug && <span className="font-mono text-xs text-foreground">{slug}</span>}
        </div>
        <p className="mt-0.5 text-xs text-muted-foreground">{run.summary}</p>
      </div>
    </li>
  )
}

function windowToISO(w: "24h" | "7d" | "30d"): string {
  const ms = w === "24h" ? 24 * 3600e3 : w === "7d" ? 7 * 86400e3 : 30 * 86400e3
  return new Date(Date.now() - ms).toISOString()
}

function shortTime(iso: string): string {
  const d = new Date(iso)
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })
}

function groupByDay(runs: RunEntry[]): Array<{ day: string; items: RunEntry[] }> {
  const groups = new Map<string, RunEntry[]>()
  for (const r of runs) {
    const day = new Date(r.ts).toLocaleDateString(undefined, { weekday: "long", month: "short", day: "numeric" })
    if (!groups.has(day)) groups.set(day, [])
    groups.get(day)!.push(r)
  }
  return Array.from(groups.entries()).map(([day, items]) => ({ day, items }))
}
