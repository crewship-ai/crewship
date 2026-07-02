"use client"

import { createContext, useContext, useMemo, type ReactNode } from "react"
import { usePipelineRuns, type PipelineRun } from "@/hooks/use-pipeline-runs"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { useWorkspace } from "@/hooks/use-workspace"
import { ACTIVE_STATUSES } from "@/lib/activity/run-filters"

// useActiveRoutineRuns — the single workspace-scoped "what routine is
// doing something right now?" source, shared by three surfaces:
//   1. the header Activity dropdown (badge + LIVE/RECENT sections)
//   2. the /routines explorer sidebar (live sub-line per routine)
//   3. the /routines list table (Running status cell)
//
// One subscription for all of them: the provider below mounts once in
// the dashboard layout and owns the only fetch/poll/WS loop.
// Consumers read derived, memoized views via context so a page that
// renders the dropdown AND the routines surfaces still costs exactly
// one request stream.
//
// The fetch uses the unfiltered feed (`status` absent = all) at the
// full 200-row budget: the Activity dropdown's RECENT section needs
// the last few terminal runs, and fetching "all" once is cheaper than
// a second standing poll just for them. Rows come newest-first, so
// the tradeoff is theoretical: an active run only falls off if 200+
// runs started after it — deriveActiveRoutineRuns re-filters, so a
// terminal row can never leak into a live surface.
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
  /**
   * Last few terminal runs (completed/failed), newest first — feeds
   * the Activity dropdown's RECENT section.
   */
  recentRuns: PipelineRun[]
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

// deriveRecentTerminalRuns — the RECENT slice of the same feed:
// completed/failed runs (cancelled/interrupted are noise here — the
// dropdown answers "what just finished?", the /activity rail owns the
// full post-mortem), newest ended first, capped so the dropdown never
// holds more rows than it renders.
const RECENT_STATUSES: ReadonlySet<string> = new Set(["completed", "failed"])

export function deriveRecentTerminalRuns(rows: PipelineRun[], limit = 3): PipelineRun[] {
  const terminal = rows.filter((r) => RECENT_STATUSES.has(r.status))
  terminal.sort(
    (a, b) => parseTs(b.ended_at || b.started_at) - parseTs(a.ended_at || a.started_at),
  )
  return terminal.slice(0, limit)
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
  recentRuns: [],
  loading: false,
  error: null,
  refresh: () => {},
}

const ActiveRoutineRunsContext = createContext<ActiveRoutineRunsValue | null>(null)

export function ActiveRoutineRunsProvider({ children }: { children: ReactNode }) {
  const { workspaceId } = useWorkspace()
  // Unfiltered feed at the full 200-row budget: one stream supplies
  // both the live derivation and the RECENT terminal slice (see the
  // header comment for the tradeoff).
  const { runs, loading, error, refresh } = usePipelineRuns(workspaceId, "all", 200)

  // Current-step advancement: run.* events only fire at run
  // boundaries; a step boundary mid-run should move the "▶ <step>"
  // line without waiting for the 3s poll.
  useRealtimeEvent("pipeline.step.started", refresh)

  const value = useMemo<ActiveRoutineRunsValue>(() => {
    const d = deriveActiveRoutineRuns(runs)
    return { ...d, recentRuns: deriveRecentTerminalRuns(runs), loading, error, refresh }
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
