/**
 * The decisions the /activity left rail makes, as plain functions.
 *
 * The rail is NAVIGATION: a STATUS section, then every run of the window. Everything else — crews, issues, routines, sources, severities,
 * agents, range, telemetry — is a NARROWING and lives in the filter popover.
 * The version before this one stacked all of it in the same column, so a
 * status bucket, a crew, an issue and a workflow all looked like the same
 * kind of thing and "Failed" existed twice (a bucket here, `severity: error`
 * in the popover).
 *
 * These live outside the component because they are the parts worth testing:
 * which status rows exist, what a count is allowed to claim, what Clear all is
 * allowed to touch. Mounting a sidebar to assert those tests React.
 */

import type { ActivityScope } from "@/lib/activity-stream"

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

/* -------------------------------------------------------------- status */

export type RailScope = ActivityScope | "all"

export interface RailStatusRow {
  key: RailScope
  /** The Routines rail's word for the same state. */
  label: string
  /** Tailwind text tone — the same one the overview cards read. */
  tone: string
  count: number
}

/**
 * The STATUS section at the top of the rail, in the Routines rail's order and
 * words (#2979).
 *
 * It used to be a segmented switch — All · Running · Waiting · Failed on one
 * 280px line, with Completed appearing only once it was picked — a control no
 * other page has. /routines draws the same question as a section of rows with
 * a count each, so this does too: every bucket, always, so a row never moves
 * because its neighbour emptied.
 *
 * The counts are complete. The rail lists CHAINS, and the chain index is
 * fetched independently of the scope facet, so every bucket of this window is
 * genuinely held whichever row is picked.
 */
const STATUS_ROWS: { key: RailScope; label: string; tone: string }[] = [
  { key: "all", label: "All", tone: "text-foreground/70" },
  // Live buckets first, as on /routines: the one state that is waiting for
  // the person reading it must not sort under three historical outcomes.
  { key: "waiting", label: "Waiting for you", tone: "text-warn" },
  { key: "active", label: "Running", tone: "text-primary" },
  { key: "done", label: "Completed", tone: "text-success" },
  { key: "failed", label: "Could not finish", tone: "text-destructive" },
]

export function railStatusRows(scopeCounts: Record<ActivityScope, number>, total: number): RailStatusRow[] {
  return STATUS_ROWS.map((r) => ({
    ...r,
    count: r.key === "all" ? total : scopeCounts[r.key as ActivityScope],
  }))
}

/* -------------------------------------------------------------- facets */

/**
 * Severities the popover offers.
 *
 * `error` is deliberately absent: it IS the Could-not-finish status row
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
 * status row. `scope=waiting` fetches exactly `sourceEntryTypes("human")`, so
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
 * The scope is not in here on purpose: it is the status row you are standing on,
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
