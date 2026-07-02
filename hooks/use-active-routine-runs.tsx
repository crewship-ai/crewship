"use client"

import { createContext, useContext, useMemo, type ReactNode } from "react"
import { usePipelineRuns, type PipelineRun } from "@/hooks/use-pipeline-runs"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { useWorkspace } from "@/hooks/use-workspace"
import { ACTIVE_STATUSES } from "@/lib/activity/run-filters"

// useActiveRoutineRuns — the single workspace-scoped "what routine is
// doing something right now?" source, shared by three surfaces:
//   1. the global header live-runs chip + popover (app-toolbar)
//   2. the /routines explorer sidebar (live sub-line per routine)
//   3. the /routines list table (Running status cell)
//
// One subscription for all of them: the provider below mounts once in
// the dashboard layout and owns the only fetch/poll/WS loop
// (usePipelineRuns with the server-side `status=active` shortcut =
// running+queued+paused+waiting). Consumers read derived, memoized
// views via context so a page that renders the chip AND the routines
// surfaces still costs exactly one request stream.
//
// Live refresh piggybacks on usePipelineRuns (pipeline.run.started/
// completed/failed + 3s poll while anything is active); we add a
// pipeline.step.started nudge so the "current step" line advances
// within a beat of the step boundary instead of waiting for the next
// poll tick. There is no `pipeline.waitpoint.created` broadcast on the
// wire today (internal/pipeline/journal.go emits run.* + step.* only)
// — parked runs surface via the poll + the step/run events around
// them.

export interface ActiveRoutineRunsValue {
  /** Active runs (running/queued/paused/waiting), newest first. */
  runs: PipelineRun[]
  /** Total number of active runs. */
  activeCount: number
  /** How many of them are parked on a human approval (waiting/paused). */
  awaitingApproval: number
  /** Newest active run per pipeline_slug — feeds the routines surfaces. */
  bySlug: ReadonlyMap<string, PipelineRun>
  loading: boolean
  error: string | null
  refresh: () => void
}

// isAwaitingApproval — a run parked on a human decision. The store
// writes 'waiting' (SetWaiting, internal/pipeline/runs.go); 'paused'
// is kept for tolerance with the API's historical vocabulary.
export function isAwaitingApproval(status: string): boolean {
  return status === "waiting" || status === "paused"
}

interface Derived {
  runs: PipelineRun[]
  activeCount: number
  awaitingApproval: number
  bySlug: ReadonlyMap<string, PipelineRun>
}

// deriveActiveRoutineRuns — pure derivation over the wire rows so the
// counts/sorting/per-slug mapping are unit-testable without React.
// Defensive re-filter on ACTIVE_STATUSES: the endpoint already scopes
// to active, but a stale row between poll ticks must not leak a
// completed run into the live chip.
export function deriveActiveRoutineRuns(rows: PipelineRun[]): Derived {
  const active = rows.filter((r) => ACTIVE_STATUSES.has(r.status))
  active.sort((a, b) => parseTs(b.started_at) - parseTs(a.started_at))
  const bySlug = new Map<string, PipelineRun>()
  let awaiting = 0
  for (const r of active) {
    if (isAwaitingApproval(r.status)) awaiting++
    // `active` is newest-first, so first hit per slug wins.
    if (r.pipeline_slug && !bySlug.has(r.pipeline_slug)) bySlug.set(r.pipeline_slug, r)
  }
  return {
    runs: active,
    activeCount: active.length,
    awaitingApproval: awaiting,
    bySlug,
  }
}

function parseTs(iso?: string): number {
  if (!iso) return 0
  const t = new Date(iso).getTime()
  return Number.isNaN(t) ? 0 : t
}

const EMPTY: ActiveRoutineRunsValue = {
  runs: [],
  activeCount: 0,
  awaitingApproval: 0,
  bySlug: new Map(),
  loading: false,
  error: null,
  refresh: () => {},
}

const ActiveRoutineRunsContext = createContext<ActiveRoutineRunsValue | null>(null)

export function ActiveRoutineRunsProvider({ children }: { children: ReactNode }) {
  const { workspaceId } = useWorkspace()
  // Server-side `status=active` + the full 200-row budget so a
  // cron-heavy workspace doesn't truncate its active set at 100.
  const { runs, loading, error, refresh } = usePipelineRuns(workspaceId, "active", 200)

  // Current-step advancement: run.* events only fire at run
  // boundaries; a step boundary mid-run should move the "▶ <step>"
  // line without waiting for the 3s poll.
  useRealtimeEvent("pipeline.step.started", refresh)

  const value = useMemo<ActiveRoutineRunsValue>(() => {
    const d = deriveActiveRoutineRuns(runs)
    return { ...d, loading, error, refresh }
  }, [runs, loading, error, refresh])

  return (
    <ActiveRoutineRunsContext.Provider value={value}>
      {children}
    </ActiveRoutineRunsContext.Provider>
  )
}

// useActiveRoutineRuns — consumer side. Falls back to the inert EMPTY
// value outside the provider (e.g. isolated component tests) instead
// of throwing; every real surface lives under the dashboard layout
// where the provider is mounted.
export function useActiveRoutineRuns(): ActiveRoutineRunsValue {
  return useContext(ActiveRoutineRunsContext) ?? EMPTY
}
