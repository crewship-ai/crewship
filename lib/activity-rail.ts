/**
 * The decisions the /activity-new left rail makes, as plain functions.
 *
 * The rail is NAVIGATION: one line of status segments, then the workflow
 * list. Everything else — crews, issues, routines, sources, severities,
 * agents, range, telemetry — is a NARROWING and lives in the filter popover.
 * The version before this one stacked all of it in the same column, so a
 * status bucket, a crew, an issue and a workflow all looked like the same
 * kind of thing and "Failed" existed twice (a bucket here, `severity: error`
 * in the popover).
 *
 * These live outside the component because they are the parts worth testing:
 * which segments exist, what a count is allowed to claim, what Clear all is
 * allowed to touch. Mounting a sidebar to assert those tests React.
 */

import { ACTIVITY_SCOPES, type ActivityScope } from "@/lib/activity-stream"

/* --------------------------------------------------------------- range */

export const TIME_RANGES = [
  { key: "1h", label: "Past hour", ms: 60 * 60_000 },
  { key: "24h", label: "Past 24 hours", ms: 24 * 60 * 60_000 },
  { key: "7d", label: "Past 7 days", ms: 7 * 24 * 60 * 60_000 },
  { key: "30d", label: "Past 30 days", ms: 30 * 24 * 60 * 60_000 },
] as const

export type TimeRangeKey = (typeof TIME_RANGES)[number]["key"]

/** The window the page opens on. Selecting it is not a narrowing. */
export const DEFAULT_RANGE: TimeRangeKey = "24h"

/* ------------------------------------------------------------ segments */

export type RailScope = ActivityScope | "all"

export interface RailSegment {
  key: RailScope
  /** One word — four of these share one 280px line. */
  label: string
  /** The full name, for the tooltip and the screen reader. */
  hint: string
  /** Tone token from globals.css, the same one the overview cards read. */
  token: string
  /**
   * How many rows are in this bucket — or `null` when the loaded window
   * cannot answer that, which is not the same as zero. See below.
   */
  count: number | null
}

/** Short names. `ACTIVITY_SCOPES` carries the long ones, and stays the source. */
const SHORT_LABEL: Record<ActivityScope, string> = {
  active: "Running",
  waiting: "Waiting",
  failed: "Failed",
  done: "Completed",
}

/** The segments that are always on the line, in reading order. */
const FIXED: RailScope[] = ["all", "active", "waiting", "failed"]

/**
 * Does picking this scope narrow the FETCH, or only the rendered list?
 *
 * activity-stream-view turns `active`/`waiting` into an `entry_type` filter
 * and `failed` into `severity=error`, so under those three the window holds
 * one bucket and the others are unloaded rather than empty. `done` has no
 * server-side expression and is filtered client-side, so under it — as under
 * `all` — the whole window is present and every bucket in it is real.
 */
export function scopeNarrowsFetch(scope: RailScope): boolean {
  return scope === "active" || scope === "waiting" || scope === "failed"
}

/**
 * The status line at the top of the rail.
 *
 * Four segments, mutually exclusive, plus `Completed` when — and only when —
 * that is the current scope: the overview's stat cards can send the page into
 * any scope, and a control that cannot draw the state it was handed shows
 * nothing selected and reads as broken.
 *
 * Counts are suppressed (null) for every bucket the current query did not
 * load. Printing the raw 0 is how the old rail told a reader "nothing is
 * running" on the evidence of a query that only asked for failures.
 */
export function railSegments(
  scope: RailScope,
  scopeCounts: Record<ActivityScope, number>,
  total: number,
): RailSegment[] {
  const keys: RailScope[] = scope === "done" ? [...FIXED, "done"] : FIXED
  const knowable = !scopeNarrowsFetch(scope)
  return keys.map((key) => {
    const meta = ACTIVITY_SCOPES.find((s) => s.key === key)
    const raw = key === "all" ? total : scopeCounts[key as ActivityScope]
    return {
      key,
      label: key === "all" ? "All" : SHORT_LABEL[key as ActivityScope],
      hint: key === "all" ? "All activity" : (meta?.label ?? key),
      token: meta?.token ?? "--muted-foreground",
      count: knowable || key === scope ? raw : null,
    }
  })
}

/* -------------------------------------------------------------- facets */

/**
 * Severities the popover offers.
 *
 * `error` is deliberately absent: it IS the Failed segment
 * (activity-stream-view maps `scope=failed` to `severity=error`), and one
 * filter reachable from two controls is exactly the duplication this rail
 * was rebuilt to remove.
 */
export const RAIL_SEVERITIES: { key: string; label: string; token: string }[] = [
  { key: "warn", label: "Warning", token: "--warn" },
  { key: "notice", label: "Notice", token: "--notice" },
  { key: "info", label: "Info", token: "--muted-foreground" },
]

/**
 * Sources the popover offers.
 *
 * `human` is dropped for the same reason `error` is: it IS the Waiting
 * segment. `scope=waiting` fetches exactly `sourceEntryTypes("human")`, so
 * the source option was a second control issuing the first one's query —
 * and, sitting in a different facet, one that could quietly contradict it.
 */
export function railSources<T extends { key: string }>(all: T[]): T[] {
  return all.filter((s) => s.key !== "human")
}

export type RailFacetKey =
  | "crew"
  | "agent"
  | "issue"
  | "routine"
  | "range"
  | "source"
  | "severity"
  | "noise"

/**
 * Which facets the popover renders, in order.
 *
 * Nouns first — a crew, an agent, an issue, a routine are what a person came
 * looking for; time, source and severity are how they trim what is left. An
 * entity facet with nothing to list is dropped rather than rendered as an
 * empty header, and the first one in the returned list is the one that draws
 * without a leading divider.
 */
export function filterFacets(present: {
  crews: number
  agents: number
  issues: number
  routines: number
}): RailFacetKey[] {
  const keys: RailFacetKey[] = []
  if (present.crews > 0) keys.push("crew")
  if (present.agents > 0) keys.push("agent")
  if (present.issues > 0) keys.push("issue")
  if (present.routines > 0) keys.push("routine")
  keys.push("range", "source", "severity", "noise")
  return keys
}

/* --------------------------------------------------------------- state */

/** The narrowings the popover owns. `FacetState` satisfies this. */
export interface RailFilters {
  sources: string[]
  severities: string[]
  crewIDs: string[]
  agentIDs: string[]
  range: TimeRangeKey
  showTelemetry: boolean
}

/**
 * The number on the Filter trigger.
 *
 * Counts the entity narrowings too, now that crews, issues and routines sit
 * behind the trigger: an active filter nobody can see is one people fight
 * without knowing what they are fighting. `focused` is the issue/routine
 * focus, which is one narrowing at a time.
 */
export function activeFilterCount(f: RailFilters, focused: boolean): number {
  return (
    f.sources.length +
    f.severities.length +
    f.crewIDs.length +
    f.agentIDs.length +
    (f.range === DEFAULT_RANGE ? 0 : 1) +
    (f.showTelemetry ? 1 : 0) +
    (focused ? 1 : 0)
  )
}

/**
 * Clear all — every facet the popover owns, and nothing else.
 *
 * The scope is not in here on purpose: it is the segment you are standing on,
 * not a filter, and clearing filters should not move you somewhere else.
 * (The entity focus is cleared alongside this by the caller, since it is not
 * part of the facet state.)
 */
export function clearedFilters<T extends RailFilters>(current: T): T {
  return {
    ...current,
    sources: [] as T["sources"],
    severities: [] as T["severities"],
    crewIDs: [],
    agentIDs: [],
    range: DEFAULT_RANGE as T["range"],
    showTelemetry: false,
  }
}
