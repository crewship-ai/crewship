"use client"

import { useEffect, useRef, useState } from "react"
import { ChevronRight, Loader2, CheckCircle2, XCircle, Eye } from "lucide-react"
import { usePipelineRuns } from "@/hooks/use-pipelines"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { RoutineRunsSkeleton } from "./routine-skeletons"

// RoutineRunsTab — list of recent runs for one routine. Click to
// expand a run inline and see step-level waterfall. Step events come
// from the WS broadcaster (until now broadcast into void); we
// subscribe per-run and accumulate timeline locally so the waterfall
// stays live even after the run completes.

interface Props {
  workspaceId: string
  slug: string
}

interface StepEvent {
  ts: string
  kind: "started" | "completed" | "failed" | "validation_failed"
  step_id: string
  duration_ms?: number
  error_message?: string
}

export function RoutineRunsTab({ workspaceId, slug }: Props) {
  const { runs, loading, error } = usePipelineRuns(workspaceId, slug)
  const [expandedRunId, setExpandedRunId] = useState<string | null>(null)
  const [stepEvents, setStepEvents] = useState<Map<string, StepEvent[]>>(new Map())
  // Track which slug the auto-expand has fired for, so switching
  // routines re-arms it but re-rendering on data refresh doesn't.
  const autoExpandedFor = useRef<string | null>(null)

  // Subscribe to step events for the currently-expanded run. We
  // accumulate in a Map keyed by run_id so jumping back to a run
  // after expanding another one preserves history seen earlier.
  const handleStepEvent = (kind: StepEvent["kind"]) => (event: RealtimeEvent) => {
    const payload = event.payload as { run_id?: string; step_id?: string; duration_ms?: number; error_message?: string }
    if (!payload?.run_id || !payload?.step_id) return
    const evt: StepEvent = {
      ts: new Date().toISOString(),
      kind,
      step_id: payload.step_id,
      duration_ms: payload.duration_ms,
      error_message: payload.error_message,
    }
    setStepEvents((prev) => {
      const next = new Map(prev)
      const list = next.get(payload.run_id!) ?? []
      next.set(payload.run_id!, [...list, evt])
      return next
    })
  }

  useRealtimeEvent("pipeline.step.started", handleStepEvent("started"))
  useRealtimeEvent("pipeline.step.completed", handleStepEvent("completed"))
  useRealtimeEvent("pipeline.step.failed", handleStepEvent("failed"))
  useRealtimeEvent("pipeline.step.validation_failed", handleStepEvent("validation_failed"))

  // Group journal entries by run_id so each run renders as a single
  // expandable row with its lifecycle entries underneath.
  const grouped = groupRunsByRunId(runs)

  // Auto-expand the most recent run on first load for this slug, so
  // users land on a populated waterfall instead of having to manually
  // open the row. Tracked per-slug so navigating between routines
  // re-arms the auto-expand, but data refresh on the same slug doesn't.
  useEffect(() => {
    if (!grouped.length) return
    if (autoExpandedFor.current === slug) return
    autoExpandedFor.current = slug
    setExpandedRunId(grouped[0].runId)
  }, [grouped, slug])

  if (loading) return <RoutineRunsSkeleton rows={3} />
  if (error) return <div className="py-4 text-xs text-red-400">Error: {error}</div>
  if (grouped.length === 0)
    return (
      <div className="rounded-md border border-dashed border-border/60 p-6 text-center text-xs text-muted-foreground">
        No runs yet. Trigger one with the Run button above, or invoke via agent / CLI / schedule.
      </div>
    )

  return (
    <ol className="space-y-1.5">
      {grouped.map((run) => {
        const expanded = expandedRunId === run.runId
        return (
          <li
            key={run.runId}
            className={cn(
              "rounded-md border border-white/[0.06] bg-card/40",
              expanded && "ring-1 ring-blue-500/30",
            )}
          >
            <button
              onClick={() => setExpandedRunId(expanded ? null : run.runId)}
              className="flex w-full items-center gap-2 px-3 py-2 text-left hover:bg-muted/40"
            >
              <ChevronRight
                className={cn("h-3 w-3 text-muted-foreground transition-transform", expanded && "rotate-90")}
              />
              <RunStatusIcon status={run.status} />
              <span className="font-mono text-[11px]">{run.runId.slice(0, 16)}…</span>
              <Badge variant="outline" className="text-[10px] capitalize">
                {run.status}
              </Badge>
              <span className="ml-auto font-mono text-[10px] text-muted-foreground">
                {formatRelative(run.startedAt)}
              </span>
            </button>
            {expanded && (
              <div className="border-t border-white/[0.06] px-3 py-2">
                <RunWaterfall
                  runId={run.runId}
                  journalEntries={run.entries}
                  liveSteps={stepEvents.get(run.runId) ?? []}
                />
              </div>
            )}
          </li>
        )
      })}
    </ol>
  )
}

function RunStatusIcon({ status }: { status: "running" | "completed" | "failed" | "unknown" }) {
  if (status === "completed") return <CheckCircle2 className="h-3.5 w-3.5 text-emerald-400" />
  if (status === "failed") return <XCircle className="h-3.5 w-3.5 text-red-400" />
  if (status === "running") return <Loader2 className="h-3.5 w-3.5 animate-spin text-blue-400" />
  return <Eye className="h-3.5 w-3.5 text-muted-foreground" />
}

function RunWaterfall({
  runId: _runId,
  journalEntries,
  liveSteps,
}: {
  runId: string
  journalEntries: Array<{ ts: string; entry_type: string; severity: string; summary: string; payload?: unknown }>
  liveSteps: StepEvent[]
}) {
  // Merge journal step entries + live step events, dedupe by step_id+kind.
  // Payload extraction is defensive: the API may return payload as an
  // object (parsed JSON) or a string (when an upstream JSON.parse
  // failure pushed raw bytes through). We tolerate both rather than
  // showing "No step events" when the actual data is present.
  const merged: Array<{ ts: string; kind: string; stepId: string; summary: string; severity: string }> = []
  for (const entry of journalEntries) {
    const stepId = extractStepID(entry.payload)
    if (entry.entry_type.startsWith("pipeline.step.") && stepId) {
      merged.push({
        ts: entry.ts,
        kind: entry.entry_type.replace("pipeline.step.", ""),
        stepId,
        summary: entry.summary,
        severity: entry.severity,
      })
    }
  }
  for (const live of liveSteps) {
    merged.push({ ts: live.ts, kind: live.kind, stepId: live.step_id, summary: "(live event)", severity: "info" })
  }
  merged.sort((a, b) => a.ts.localeCompare(b.ts))

  // Dedupe (same ts+stepId+kind) — live + journal can echo.
  const seen = new Set<string>()
  const deduped = merged.filter((m) => {
    const k = `${m.stepId}|${m.kind}`
    if (seen.has(k)) return false
    seen.add(k)
    return true
  })

  if (deduped.length === 0) {
    return (
      <p className="text-[11px] text-muted-foreground">
        No step events yet — waterfall will populate as the run executes.
      </p>
    )
  }

  return (
    <ol className="space-y-1">
      {deduped.map((s, i) => (
        <li key={i} className="flex items-baseline gap-2 text-[11px]">
          <span className="font-mono text-[10px] text-muted-foreground tabular-nums">
            {new Date(s.ts).toLocaleTimeString()}
          </span>
          <Badge
            variant="outline"
            className={cn(
              "text-[9px] capitalize",
              s.kind === "completed" && "border-emerald-500/30 text-emerald-400",
              s.kind === "failed" && "border-red-500/30 text-red-400",
              s.kind === "validation_failed" && "border-amber-500/30 text-amber-400",
            )}
          >
            {s.kind.replace(/_/g, " ")}
          </Badge>
          <span className="font-mono text-foreground">{s.stepId}</span>
        </li>
      ))}
    </ol>
  )
}

interface GroupedRun {
  runId: string
  status: "running" | "completed" | "failed" | "unknown"
  startedAt: string
  entries: Array<{ ts: string; entry_type: string; severity: string; summary: string; payload?: unknown }>
}

function groupRunsByRunId(rows: Array<{ id: string; ts: string; entry_type: string; severity: string; summary: string; run_id?: string; payload?: unknown }>): GroupedRun[] {
  const groups = new Map<string, GroupedRun>()
  for (const r of rows) {
    const runId = r.run_id ?? r.id
    if (!groups.has(runId)) {
      groups.set(runId, { runId, status: "unknown", startedAt: r.ts, entries: [] })
    }
    const g = groups.get(runId)!
    g.entries.push({ ts: r.ts, entry_type: r.entry_type, severity: r.severity, summary: r.summary, payload: r.payload })
    // Status determination is order-sensitive because the API returns
    // events in DESC ts order. Without the guard, run.started (which
    // appears LAST in DESC iteration) overwrites a previously-set
    // "completed"/"failed" with "running" — UI then shows the run as
    // perpetually Running. Treat completed/failed as terminal: once
    // we observe one of those for a run_id, never downgrade.
    if (g.status === "completed" || g.status === "failed") {
      // terminal already; keep it
    } else if (r.entry_type === "pipeline.run.completed") {
      g.status = "completed"
    } else if (r.entry_type === "pipeline.run.failed") {
      g.status = "failed"
    } else if (r.entry_type === "pipeline.run.started") {
      g.status = "running"
    }
    if (r.entry_type === "pipeline.run.started") {
      g.startedAt = r.ts
    }
  }
  return Array.from(groups.values()).sort((a, b) => b.startedAt.localeCompare(a.startedAt))
}

// extractStepID pulls the step_id from a journal entry's payload field,
// tolerating three on-the-wire shapes: parsed object (the common case),
// JSON-encoded string (when upstream serialized it twice), and absent.
function extractStepID(payload: unknown): string {
  if (!payload) return ""
  if (typeof payload === "string") {
    try {
      const parsed = JSON.parse(payload) as { step_id?: unknown }
      return typeof parsed.step_id === "string" ? parsed.step_id : ""
    } catch {
      return ""
    }
  }
  if (typeof payload === "object" && payload !== null) {
    const obj = payload as { step_id?: unknown }
    return typeof obj.step_id === "string" ? obj.step_id : ""
  }
  return ""
}

function formatRelative(iso: string): string {
  const then = new Date(iso).getTime()
  if (!Number.isFinite(then)) return "—"
  const diffMs = Date.now() - then
  const sec = Math.floor(diffMs / 1000)
  if (sec < 60) return `${sec}s ago`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min}m ago`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr}h ago`
  return new Date(iso).toLocaleDateString()
}
