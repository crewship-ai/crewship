import { describe, it, expect } from "vitest"

import { matchesRoutineFilters, type RoutineFilterInput } from "@/lib/routine-filters"

// The live buckets are the ones that cannot be answered from the routine
// row: `last_invocation_status` still reads "running" while a run is
// parked on a human, so "running" and "awaiting approval" look identical
// there. Distinguishing them needs the live run feed, which is exactly
// why the filter takes it as an argument rather than reading a field.

const base: RoutineFilterInput = {
  slug: "demo",
  name: "Demo routine",
  description: "",
  authorAgentId: null,
  authorAgentName: null,
  invocationCount: 3,
  lastStatus: "completed",
  ephemeral: false,
}

const all = { status: "all", invocations: "all", authorAgentId: null, showEphemeral: false } as const

describe("matchesRoutineFilters — live buckets", () => {
  it("awaiting matches a routine parked on a human, not one merely running", () => {
    const live = new Map([["demo", { status: "waiting" }]])
    expect(matchesRoutineFilters(base, { ...all, status: "awaiting" }, live)).toBe(true)
    expect(
      matchesRoutineFilters(base, { ...all, status: "awaiting" }, new Map([["demo", { status: "running" }]])),
    ).toBe(false)
  })

  it("running matches an in-flight run that is NOT parked", () => {
    expect(
      matchesRoutineFilters(base, { ...all, status: "running" }, new Map([["demo", { status: "running" }]])),
    ).toBe(true)
    expect(
      matchesRoutineFilters(base, { ...all, status: "running" }, new Map([["demo", { status: "waiting" }]])),
    ).toBe(false)
  })

  it("neither live bucket matches a routine with no live run", () => {
    expect(matchesRoutineFilters(base, { ...all, status: "awaiting" }, new Map())).toBe(false)
    expect(matchesRoutineFilters(base, { ...all, status: "running" }, new Map())).toBe(false)
  })

  it("does not fall back to last_invocation_status for the live buckets", () => {
    // A routine whose LAST run was running but which has nothing in
    // flight now must not appear under Running.
    const stale = { ...base, lastStatus: "running" }
    expect(matchesRoutineFilters(stale, { ...all, status: "running" }, new Map())).toBe(false)
  })
})

describe("matchesRoutineFilters — historical buckets", () => {
  it("keeps completed / failed reading the routine row", () => {
    expect(matchesRoutineFilters(base, { ...all, status: "completed" }, new Map())).toBe(true)
    expect(matchesRoutineFilters(base, { ...all, status: "failed" }, new Map())).toBe(false)
  })

  it("never matches only a routine that has not run", () => {
    expect(matchesRoutineFilters(base, { ...all, status: "never" }, new Map())).toBe(false)
    expect(
      matchesRoutineFilters({ ...base, invocationCount: 0 }, { ...all, status: "never" }, new Map()),
    ).toBe(true)
  })
})

describe("matchesRoutineFilters — the other facets still apply", () => {
  it("filters by search across name, slug, description and author", () => {
    expect(matchesRoutineFilters(base, all, new Map(), "demo")).toBe(true)
    expect(matchesRoutineFilters(base, all, new Map(), "nothing-like-this")).toBe(false)
  })

  it("hides ephemeral routines unless asked for", () => {
    const eph = { ...base, ephemeral: true }
    expect(matchesRoutineFilters(eph, all, new Map())).toBe(false)
    expect(matchesRoutineFilters(eph, { ...all, showEphemeral: true }, new Map())).toBe(true)
  })

  it("applies usage and author facets", () => {
    expect(matchesRoutineFilters(base, { ...all, invocations: "popular" }, new Map())).toBe(false)
    expect(
      matchesRoutineFilters({ ...base, invocationCount: 12 }, { ...all, invocations: "popular" }, new Map()),
    ).toBe(true)
    expect(matchesRoutineFilters(base, { ...all, authorAgentId: "a1" }, new Map())).toBe(false)
  })
})
