"use client"

import { useEffect, useRef, useState } from "react"
import { Activity, RefreshCw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { useRealtimeEvent } from "@/hooks/use-realtime"

// RoutinesActivityView — workspace journal feed filtered to the
// pipeline.* entry-type prefix, including step-level events. Mirrors
// the orchestration Activity tab but scoped to routines so users
// reviewing routine behaviour aren't drowned in mission/agent events.

interface Props {
  workspaceId: string
}

interface JournalEntry {
  id: string
  ts: string
  entry_type: string
  severity: string
  summary: string
  payload?: Record<string, unknown>
}

export function RoutinesActivityView({ workspaceId }: Props) {
  const [entries, setEntries] = useState<JournalEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [severity, setSeverity] = useState<"all" | "info" | "warning" | "error">("all")
  // abortRef cancels the prior in-flight fetch when workspace
  // changes or a refresh fires; without this, a slow request
  // could race a fresh one and overwrite newer state with stale
  // entries.
  const abortRef = useRef<AbortController | null>(null)

  const fetchEntries = async () => {
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    setError(null)
    try {
      const url = `/api/v1/workspaces/${workspaceId}/journal?entry_type_prefix=pipeline.&limit=300`
      const res = await fetch(url, { signal: ctrl.signal })
      if (ctrl.signal.aborted) return
      if (!res.ok) {
        setError(`journal: ${res.status}`)
        setEntries([])
        return
      }
      const data: JournalEntry[] = await res.json()
      if (ctrl.signal.aborted) return
      setEntries(Array.isArray(data) ? data : [])
    } catch (e) {
      if (ctrl.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!ctrl.signal.aborted) setLoading(false)
    }
  }

  useEffect(() => {
    fetchEntries()
    return () => {
      abortRef.current?.abort()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId])

  // Step-level events are intentionally consumed here — this is the
  // place where users go to see fine-grained routine behaviour, the
  // exact granularity the broadcaster has been emitting into void.
  useRealtimeEvent("pipeline.run.started", fetchEntries)
  useRealtimeEvent("pipeline.run.completed", fetchEntries)
  useRealtimeEvent("pipeline.run.failed", fetchEntries)
  useRealtimeEvent("pipeline.step.started", fetchEntries)
  useRealtimeEvent("pipeline.step.completed", fetchEntries)
  useRealtimeEvent("pipeline.step.failed", fetchEntries)
  useRealtimeEvent("pipeline.step.validation_failed", fetchEntries)

  const filtered = severity === "all" ? entries : entries.filter((e) => e.severity === severity)

  return (
    <div className="p-4">
      <div className="mb-3 flex items-center gap-2">
        <span className="text-[11px] uppercase tracking-wider text-muted-foreground">Severity</span>
        {(["all", "info", "warning", "error"] as const).map((s) => (
          <button
            key={s}
            onClick={() => setSeverity(s)}
            className={cn(
              "rounded px-2 py-0.5 text-xs capitalize transition-colors",
              severity === s ? "bg-blue-500/15 text-blue-300" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {s}
          </button>
        ))}
        <div className="flex-1" />
        <span className="text-[11px] text-muted-foreground">{filtered.length} entries</span>
        <Button size="sm" variant="ghost" onClick={fetchEntries} className="h-7 gap-1.5 text-xs">
          <RefreshCw className={cn("h-3 w-3", loading && "animate-spin")} />
          Refresh
        </Button>
      </div>

      {error && (
        <div className="mb-3 rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs text-amber-400">
          Could not load activity ({error}). Try /orchestration → Activity for the full journal
          while routine-scoped filtering is wired up.
        </div>
      )}

      {!loading && !error && filtered.length === 0 ? (
        <div className="rounded-md border border-dashed border-border/60 p-8 text-center">
          <Activity className="mx-auto mb-3 h-8 w-8 text-muted-foreground/40" />
          <p className="text-sm font-medium">No routine activity yet</p>
          <p className="mt-1 text-xs text-muted-foreground">
            Events appear here as agents and schedules invoke routines. Includes step-level
            transitions, validation failures, and tier escalations.
          </p>
        </div>
      ) : (
        <ol className="divide-y divide-white/[0.04] rounded-md border border-white/[0.06]">
          {filtered.map((e) => (
            <ActivityRow key={e.id} entry={e} />
          ))}
        </ol>
      )}
    </div>
  )
}

function ActivityRow({ entry }: { entry: JournalEntry }) {
  const sevColor =
    entry.severity === "error"
      ? "border-red-500/30 text-red-400"
      : entry.severity === "warning"
        ? "border-amber-500/30 text-amber-400"
        : "border-border text-muted-foreground"

  return (
    <li className="flex items-start gap-3 px-3 py-2 hover:bg-muted/40">
      <span className="mt-0.5 font-mono text-[10px] text-muted-foreground tabular-nums">
        {new Date(entry.ts).toLocaleTimeString()}
      </span>
      <Badge variant="outline" className={cn("text-[10px]", sevColor)}>
        {entry.severity}
      </Badge>
      <span className="font-mono text-[10px] text-muted-foreground/80">{entry.entry_type}</span>
      <p className="min-w-0 flex-1 text-xs text-foreground/90">{entry.summary}</p>
    </li>
  )
}
