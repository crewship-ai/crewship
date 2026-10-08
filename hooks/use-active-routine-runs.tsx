"use client"

import { createContext, useCallback, useContext, useMemo, type ReactNode } from "react"
import { usePipelineRuns, type PipelineRun } from "@/hooks/use-pipeline-runs"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { useTrustedWorkspaceId } from "@/hooks/use-access-mode"
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
// Two feeds. Active runs come from `status=active`, so a long run stays
// visible however many runs started after it — one unfiltered feed of the
// 200 newest rows dropped it once 200 newer runs existed. The RECENT
// sections read the unfiltered feed, which needs no poll of its own: a run
// only becomes terminal through the run.completed/failed events it already
// listens to. deriveActiveRoutineRuns re-filters, so a terminal row can
// never leak into a live surface.
//
// Live refresh piggybacks on usePipelineRuns (pipeline.run.started/
// completed/failed + the active feed's 3s poll while anything runs); we add a
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
  /** Wider terminal window for the scrollable dashboard results list. */
  recentDashboardRuns: PipelineRun[]
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
  recentDashboardRuns: [],
  loading: false,
  error: null,
  refresh: () => {},
}

const ActiveRoutineRunsContext = createContext<ActiveRoutineRunsValue | null>(null)

export function ActiveRoutineRunsProvider({ children }: { children: ReactNode }) {
  // Pipeline runs are not on the restricted allowlist: null until the session
  // is known to be trusted, which keeps the feed (and its poll) off.
  const workspaceId = useTrustedWorkspaceId()
  const active = usePipelineRuns(workspaceId, "active", 200)
  const history = usePipelineRuns(workspaceId, "all", 200, { poll: false })
  const { runs } = active
  const loading = active.loading || history.loading
  const error = active.error ?? history.error
  const refreshActive = active.refresh
  const refreshHistory = history.refresh
  const refresh = useCallback(() => {
    void refreshActive()
    void refreshHistory()
  }, [refreshActive, refreshHistory])

  // Current-step advancement: run.* events only fire at run
  // boundaries; a step boundary mid-run should move the "▶ <step>"
  // line without waiting for the 3s poll.
  useRealtimeEvent("pipeline.step.started", refreshActive)

  const value = useMemo<ActiveRoutineRunsValue>(() => {
    const d = deriveActiveRoutineRuns(runs)
    return { ...d, recentRuns: deriveRecentTerminalRuns(history.runs), recentDashboardRuns: deriveRecentTerminalRuns(history.runs, 12), loading, error, refresh }
  }, [runs, history.runs, loading, error, refresh])

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
