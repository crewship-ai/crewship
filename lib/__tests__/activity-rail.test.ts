import { describe, expect, it } from "vitest"

import {
  DEFAULT_RANGE,
  RAIL_SEVERITIES,
  activeFilterCount,
  clearedFilters,
  filterFacets,
  railStatusRows,
  railSources,
  type RailFilters,
} from "@/lib/activity-rail"
import { ACTIVITY_SOURCES, type ActivityScope } from "@/lib/activity-stream"

/** The shape activity-stream-view hands the rail: all four buckets, always. */
const counts = (over: Partial<Record<ActivityScope, number>> = {}): Record<ActivityScope, number> => ({
  active: 0,
  waiting: 0,
  failed: 0,
  done: 0,
  stopped: 0,
  ...over,
})

const filters = (over: Partial<RailFilters> = {}): RailFilters => ({
  sources: [],
  severities: [],
  crewIDs: [],
  agentIDs: [],
  range: DEFAULT_RANGE,
  showTelemetry: false,
  ...over,
})

describe("railStatusRows — the STATUS section, in the Routines vocabulary", () => {
  it("lists every bucket, always, in the order the Routines rail uses", () => {
    // A bucket that disappears when empty moves every row under it, and the
    // Routines rail — one click away — never does that.
    expect(railStatusRows(counts(), 0).map((r) => r.key)).toEqual(["all", "waiting", "active", "done", "stopped", "failed"])
  })

  it("speaks the Routines words, so one state has one name across the app", () => {
    expect(railStatusRows(counts(), 0).map((r) => r.label)).toEqual([
      "All",
      "Waiting for you",
      "Running",
      "Completed",
      "Stopped",
      "Could not finish",
    ])
  })

  it("counts what the list under it holds", () => {
    const rows = railStatusRows(counts({ active: 2, waiting: 1, failed: 3, done: 9, stopped: 2 }), 17)
    expect(Object.fromEntries(rows.map((r) => [r.key, r.count]))).toEqual({
      all: 17,
      waiting: 1,
      active: 2,
      done: 9,
      stopped: 2,
      failed: 3,
    })
  })

  it("gives each bucket the tone the overview cards already use", () => {
    const tone = Object.fromEntries(railStatusRows(counts(), 0).map((r) => [r.key, r.tone]))
    expect(tone).toMatchObject({ waiting: "text-warn", active: "text-primary", done: "text-success", failed: "text-destructive" })
  })
})

describe("the failed filter exists once", () => {
  it("is a status row, and is not also a severity option", () => {
    // "Failed" as a status bucket and severity:error in the filter popover
    // are the same query (activity-stream-view maps scope=failed to
    // severity=error). Two controls for one filter is what the owner was
    // clicking through.
    const offered = [
      ...railStatusRows(counts(), 0)
        .filter((s) => s.key === "failed")
        .map((s) => `segment:${s.key}`),
      ...RAIL_SEVERITIES.filter((s) => s.key === "error").map((s) => `severity:${s.key}`),
    ]
    expect(offered).toEqual(["segment:failed"])
  })

  it("keeps the severities that are NOT reachable from the segments", () => {
    expect(RAIL_SEVERITIES.map((s) => s.key)).toEqual(["warn", "notice", "info"])
  })
})

describe("the waiting filter exists once", () => {
  it("is a status row, and is not also a source option", () => {
    // Same trap as Failed, one facet over: scope=waiting fetches exactly
    // sourceEntryTypes("human"), which is what picking the "Waiting on you"
    // source does. Two controls, one query, and the popover one silently
    // fights the segment above it.
    expect(railStatusRows(counts(), 0).map((s) => s.key)).toContain("waiting")
    expect(railSources(ACTIVITY_SOURCES).map((s) => s.key)).not.toContain("human")
  })

  it("leaves every other source alone", () => {
    expect(railSources(ACTIVITY_SOURCES).map((s) => s.key)).toEqual(
      ACTIVITY_SOURCES.filter((s) => s.key !== "human").map((s) => s.key),
    )
  })
})

describe("activeFilterCount — what the Filter badge promises", () => {
  it("is 0 for an untouched popover", () => {
    expect(activeFilterCount(filters(), false)).toBe(0)
  })

  it("does not count the default range as a narrowing", () => {
    expect(activeFilterCount(filters({ range: DEFAULT_RANGE }), false)).toBe(0)
    expect(activeFilterCount(filters({ range: "7d" }), false)).toBe(1)
  })

  it("counts the crews, issues and routines now that they live in the popover", () => {
    // They used to be rail sections, visible on their own. Behind a trigger,
    // an uncounted narrowing is an invisible one.
    expect(activeFilterCount(filters({ crewIDs: ["c1", "c2"] }), false)).toBe(2)
    expect(activeFilterCount(filters(), true)).toBe(1)
  })

  it("adds its parts", () => {
    expect(
      activeFilterCount(
        filters({
          sources: ["human"],
          severities: ["warn"],
          crewIDs: ["c1"],
          agentIDs: ["a1", "a2"],
          range: "7d",
          showTelemetry: true,
        }),
        true,
      ),
    ).toBe(8) // 1 source + 1 severity + 1 crew + 2 agents + range + telemetry + focus
  })
})

describe("clearedFilters — what Clear all is allowed to touch", () => {
  it("empties every facet the popover owns", () => {
    expect(
      clearedFilters(
        filters({
          sources: ["human"],
          severities: ["warn"],
          crewIDs: ["c1"],
          agentIDs: ["a1"],
          range: "7d",
          showTelemetry: true,
        }),
      ),
    ).toEqual(filters())
  })

  it("leaves the segment alone — it is where you are, not a filter", () => {
    const next = clearedFilters({ ...filters({ crewIDs: ["c1"] }), scope: "failed" as const })
    expect(next.scope).toBe("failed")
    expect(next.crewIDs).toEqual([])
  })
})

describe("filterFacets — what the popover holds", () => {
  it("puts the narrowings a person names first at the top", () => {
    expect(filterFacets({ crews: 3, agents: 4, issues: 17, routines: 39 })).toEqual([
      "crew",
      "agent",
      "issue",
      "routine",
      "range",
      "source",
      "severity",
      "noise",
    ])
  })

  it("omits an entity facet with nothing to list rather than showing an empty header", () => {
    expect(filterFacets({ crews: 0, agents: 0, issues: 0, routines: 0 })).toEqual([
      "range",
      "source",
      "severity",
      "noise",
    ])
  })
})

