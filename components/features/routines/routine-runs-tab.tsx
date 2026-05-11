"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { ChevronRight, Loader2, CheckCircle2, XCircle, Eye, Play } from "lucide-react"
import { usePipelineRuns } from "@/hooks/use-pipelines"
import { usePipelineRunRecords, type PipelineRunRecord } from "@/hooks/use-pipeline-run-records"
import { cn } from "@/lib/utils"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { RoutineRunsSkeleton } from "./routine-skeletons"
import { Card, EmptyState, Pill } from "./_shared"

// RoutineRunsTab — list of recent runs for one routine. Click to
// expand a run inline and see step-level waterfall.
//
// Two-source rendering since the v83 pipeline_runs migration:
// - List status / cost / duration columns prefer pipeline_runs
//   records (column-typed, B-tree scan) when the server has the
//   runStore wired.
// - Step-level events for the expanded waterfall stay journal-backed
//   (pipeline_runs has no per-step rows; the journal is canonical).
// - When the server doesn't have runStore wired (legacy=true), we
//   fall back to deriving the run list from journal events too.
//
// Live updates: WS broadcaster fires pipeline.run.* + pipeline.step.*
// events; both hooks subscribe and refresh on relevant events.

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
  // Primary list source: pipeline_runs (v83). Cleaner shape, faster
  // query. Falls back to journal-backed grouping when legacy=true.
  const { records, legacy, loading: recordsLoading, error: recordsError } = usePipelineRunRecords(workspaceId, slug)
  // Always fetch journal-backed runs — needed for step-level waterfall
  // when a run is expanded (pipeline_runs has no per-step rows).
  const { runs, loading: runsLoading, error: runsError } = usePipelineRuns(workspaceId, slug)
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

  // The list view prefers `records` (column-typed) when available,
  // and uses `groupRunsByRunId(runs)` as the legacy fallback. Both
  // shapes are normalized to a common GroupedRun rendering interface
  // so the row component below stays unchanged.
  //
  // When using records, we splice in journal entries for each run_id
  // so the expanded waterfall still has step events to render. Records
  // alone don't include step rows (those live in journal_entries).
  const groupedJournal = groupRunsByRunId(runs)
  const journalEntriesByRunID = useMemo(() => {
    const m = new Map<string, GroupedRun["entries"]>()
    for (const g of groupedJournal) m.set(g.runId, g.entries)
    return m
  }, [groupedJournal])
  const grouped: GroupedRun[] = useMemo(() => {
    if (legacy || records.length === 0) return groupedJournal
    return records.map((r) => {
      const g = toGroupedRun(r)
      const journalEntries = journalEntriesByRunID.get(r.id)
      if (journalEntries) g.entries = journalEntries
      return g
    })
  }, [legacy, records, groupedJournal, journalEntriesByRunID])

  // Loading + error fall back to whichever path is being used. When
  // both succeed we prefer records' status (more authoritative).
  const loading = legacy ? runsLoading : recordsLoading
  const error = legacy ? runsError : (recordsError ?? runsError)

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

  if (loading) {
    return (
      <Card title="Run history" subtitle="loading…">
        <div className="p-4">
          <RoutineRunsSkeleton rows={3} />
        </div>
      </Card>
    )
  }
  if (error) {
    return (
      <Card title="Run history">
        <div className="px-4 py-3 text-sm text-rose-400">Error: {error}</div>
      </Card>
    )
  }
  if (grouped.length === 0) {
    return (
      <Card title="Run history">
        <EmptyState
          icon={Play}
          title="No runs yet"
          description="Trigger one with the Run button above, or invoke via agent / CLI / schedule. Each invocation appears here with the step-level waterfall."
        />
      </Card>
    )
  }

  return (
    <Card title="Run history" subtitle={`${grouped.length} total · click to expand`}>
      <ol className="divide-y divide-white/[0.04]">
        {grouped.map((run) => {
          const expanded = expandedRunId === run.runId
          return (
            <li
              key={run.runId}
              className={cn("transition-colors", expanded && "bg-white/[0.015]")}
            >
              <button
                onClick={() => setExpandedRunId(expanded ? null : run.runId)}
                className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-white/[0.025]"
              >
                <ChevronRight
                  className={cn(
                    "h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform",
                    expanded && "rotate-90",
                  )}
                />
                <RunStatusIcon status={run.status} />
                <span className="font-mono text-sm text-foreground/90">{run.runId.slice(0, 18)}…</span>
                <Pill
                  tone={
                    run.status === "completed"
                      ? "emerald"
                      : run.status === "failed"
                        ? "rose"
                        : run.status === "running"
                          ? "blue"
                          : "default"
                  }
                  className="capitalize"
                >
                  {run.status}
                </Pill>
                <span className="ml-auto font-mono text-[12px] text-muted-foreground">
                  {formatRelative(run.startedAt)}
                </span>
              </button>
              {expanded && (
                <div className="border-t border-white/[0.04] bg-black/20 px-5 py-4">
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
    </Card>
  )
}

function RunStatusIcon({ status }: { status: "running" | "completed" | "failed" | "unknown" }) {
  if (status === "completed") return <CheckCircle2 className="h-4 w-4 shrink-0 text-emerald-400" />
  if (status === "failed") return <XCircle className="h-4 w-4 shrink-0 text-rose-400" />
  if (status === "running") return <Loader2 className="h-4 w-4 shrink-0 animate-spin text-blue-400" />
  return <Eye className="h-4 w-4 shrink-0 text-muted-foreground" />
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

  // Dedupe live + journal echoes WITHOUT collapsing legitimate
  // retries: a step that fails, retries, and starts again emits two
  // distinct started+failed pairs. Including the timestamp in the
  // key keeps each retry attempt visible in the waterfall while
  // still suppressing same-instant duplicates from the two sources.
  const seen = new Set<string>()
  const deduped = merged.filter((m) => {
    const k = `${m.stepId}|${m.kind}|${m.ts}`
    if (seen.has(k)) return false
    seen.add(k)
    return true
  })

  if (deduped.length === 0) {
    return (
      <p className="text-[13px] text-muted-foreground">
        No step events yet — waterfall will populate as the run executes.
      </p>
    )
  }

  return (
    <ol className="space-y-1.5">
      {deduped.map((s, i) => (
        <li key={i} className="flex items-center gap-3 text-sm">
          <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
            {new Date(s.ts).toLocaleTimeString()}
          </span>
          <Pill
            tone={
              s.kind === "completed"
                ? "emerald"
                : s.kind === "failed"
                  ? "rose"
                  : s.kind === "validation_failed"
                    ? "amber"
                    : "default"
            }
            className="capitalize"
          >
            {s.kind.replace(/_/g, " ")}
          </Pill>
          <span className="font-mono text-foreground/90">{s.stepId}</span>
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

// toGroupedRun maps a v83 PipelineRunRecord to the GroupedRun shape
// the row + waterfall components consume. Keeps the rendering layer
// agnostic of the backing source (records vs. journal grouping). The
// `entries` field stays empty here — caller splices in matching
// journal entries from groupedJournal so the waterfall has step
// events to render. When journal lookup misses (e.g., a run that's
// older than the journal retention window), the row still renders
// with status / cost / duration intact, just without the timeline.
function toGroupedRun(r: PipelineRunRecord): GroupedRun {
  let status: GroupedRun["status"] = "unknown"
  switch (r.status) {
    case "running":
    case "queued":
      status = "running"
      break
    case "completed":
      status = "completed"
      break
    case "failed":
    case "cancelled":
    case "interrupted":
      status = "failed"
      break
    default:
      status = "unknown"
  }
  return {
    runId: r.id,
    status,
    startedAt: r.started_at,
    entries: [],
  }
}
