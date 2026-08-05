// Which routines survive the explorer's filters.
//
// Extracted from the layout because two of the buckets stopped being a
// field comparison. `last_invocation_status` reads "running" while a run
// is parked on a human, so "running" and "awaiting approval" are
// indistinguishable from the routine row alone — telling them apart
// needs the live run feed, which is why it is an argument here rather
// than something this reaches for.

import { isAwaitingApproval } from "@/hooks/use-active-routine-runs"

export type RoutineStatusFilter =
  | "all"
  | "awaiting"
  | "running"
  | "completed"
  | "failed"
  | "never"

export interface RoutineFilterState {
  status: RoutineStatusFilter
  invocations: "all" | "popular" | "fresh"
  authorAgentId: string | null
  showEphemeral: boolean
}

/** The routine fields the filter reads — a narrow view, not the row. */
export interface RoutineFilterInput {
  slug: string
  name: string
  description?: string | null
  authorAgentId?: string | null
  authorAgentName?: string | null
  invocationCount: number
  lastStatus?: string | null
  ephemeral?: boolean
}

/** Live runs keyed by pipeline slug — only `status` is read. */
export type LiveRuns = ReadonlyMap<string, { status: string }>

export function matchesRoutineFilters(
  routine: RoutineFilterInput,
  filters: RoutineFilterState,
  live: LiveRuns,
  search = "",
): boolean {
  if (search) {
    const q = search.toLowerCase()
    const haystack = [
      routine.slug,
      routine.name,
      routine.description ?? "",
      routine.authorAgentName ?? "",
    ]
      .join(" ")
      .toLowerCase()
    if (!haystack.includes(q)) return false
  }

  if (!filters.showEphemeral && routine.ephemeral) return false

  if (filters.invocations === "popular" && routine.invocationCount < 10) return false
  if (filters.invocations === "fresh" && routine.invocationCount > 0) return false

  if (filters.authorAgentId !== null && routine.authorAgentId !== filters.authorAgentId) {
    return false
  }

  switch (filters.status) {
    case "all":
      return true
    case "never":
      return routine.invocationCount === 0
    case "awaiting": {
      const run = live.get(routine.slug)
      // No live run means not awaiting, whatever the last one did. A
      // routine whose previous run ended parked and then timed out must
      // not sit in this bucket forever.
      return run ? isAwaitingApproval(run.status) : false
    }
    case "running": {
      const run = live.get(routine.slug)
      return run ? !isAwaitingApproval(run.status) : false
    }
    default:
      return routine.lastStatus?.toLowerCase() === filters.status
  }
}
