import { describe, expect, it } from "vitest"

import {
  DEFAULT_RANGE,
  RAIL_SEVERITIES,
  activeFilterCount,
  clearedFilters,
  filterFacets,
  railSegments,
  railSources,
  scopeNarrowsFetch,
  type RailFilters,
} from "@/lib/activity-rail"
import { ACTIVITY_SOURCES, type ActivityScope } from "@/lib/activity-stream"

/** The shape activity-stream-view hands the rail: all four buckets, always. */
const counts = (over: Partial<Record<ActivityScope, number>> = {}): Record<ActivityScope, number> => ({
  active: 0,
  waiting: 0,
  failed: 0,
  done: 0,
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

describe("railSegments — the four places the rail lets you stand", () => {
  it("offers exactly All · Running · Waiting · Failed, in that order", () => {
    // The rail used to stack five status buckets, a crew list, an issue list
    // and a routine list in one column. Navigation is this line and the
    // workflow list; everything else is a narrowing and lives in the popover.
    expect(railSegments("all", counts(), 0).map((s) => s.key)).toEqual([
      "all",
      "active",
      "waiting",
      "failed",
    ])
  })

  it("adds Completed only while the page is actually in it", () => {
    // The overview's stat cards can set any scope (activity-overview.tsx:108),
    // "done" included. A control that cannot render the state it was given
    // shows nothing selected and reads as broken — so the fifth segment
    // appears exactly when it is the answer, and never as a permanent chip.
    expect(railSegments("done", counts(), 0).map((s) => s.key)).toEqual([
      "all",
      "active",
      "waiting",
      "failed",
      "done",
    ])
    expect(railSegments("failed", counts(), 0).map((s) => s.key)).not.toContain("done")
  })

  it("labels are short enough for one line, with the long name as the hint", () => {
    const waiting = railSegments("all", counts(), 0).find((s) => s.key === "waiting")
    expect(waiting?.label).toBe("Waiting")
    expect(waiting?.hint).toBe("Waiting on you")
  })

  it("carries the same tone token the overview cards use, so Failed is one colour", () => {
    const byKey = Object.fromEntries(railSegments("all", counts(), 0).map((s) => [s.key, s.token]))
    expect(byKey.active).toBe("--info")
    expect(byKey.waiting).toBe("--warn")
    expect(byKey.failed).toBe("--destructive")
  })
})

describe("railSegments — what a count is allowed to claim", () => {
  it("counts every bucket while the fetch is unnarrowed", () => {
    const segs = railSegments("all", counts({ active: 2, waiting: 1, failed: 3, done: 9 }), 15)
    expect(segs.map((s) => [s.key, s.count])).toEqual([
      ["all", 15],
      ["active", 2],
      ["waiting", 1],
      ["failed", 3],
    ])
  })

  it("still counts every bucket under Completed, which is narrowed client-side", () => {
    // scope=done sends no entry_type and no severity to the server
    // (activity-stream-view.tsx params memo), so the whole window is loaded
    // and every bucket in it is genuinely knowable.
    const segs = railSegments("done", counts({ active: 2, waiting: 1, failed: 3, done: 9 }), 15)
    expect(Object.fromEntries(segs.map((s) => [s.key, s.count]))).toEqual({
      all: 15,
      active: 2,
      waiting: 1,
      failed: 3,
      done: 9,
    })
  })

  it("refuses to count the buckets the query did not load", () => {
    // scope=failed fetches severity=error only. The other buckets are then 0
    // because nothing else was asked for — printing that 0 is the rail telling
    // a reader "nothing is running" on the evidence of a query that could not
    // have found anything running.
    const segs = railSegments("failed", counts({ failed: 3 }), 3)
    expect(Object.fromEntries(segs.map((s) => [s.key, s.count]))).toEqual({
      all: null,
      active: null,
      waiting: null,
      failed: 3,
    })
  })

  it("names which scopes narrow the fetch", () => {
    expect(scopeNarrowsFetch("active")).toBe(true)
    expect(scopeNarrowsFetch("waiting")).toBe(true)
    expect(scopeNarrowsFetch("failed")).toBe(true)
    expect(scopeNarrowsFetch("done")).toBe(false)
    expect(scopeNarrowsFetch("all")).toBe(false)
  })
})

describe("the failed filter exists once", () => {
  it("is a segment, and is not also a severity option", () => {
    // "Failed" as a status bucket and severity:error in the filter popover
    // are the same query (activity-stream-view maps scope=failed to
    // severity=error). Two controls for one filter is what the owner was
    // clicking through.
    const offered = [
      ...railSegments("all", counts(), 0)
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
  it("is a segment, and is not also a source option", () => {
    // Same trap as Failed, one facet over: scope=waiting fetches exactly
    // sourceEntryTypes("human"), which is what picking the "Waiting on you"
    // source does. Two controls, one query, and the popover one silently
    // fights the segment above it.
    expect(railSegments("all", counts(), 0).map((s) => s.key)).toContain("waiting")
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
