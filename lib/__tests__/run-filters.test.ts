import { describe, it, expect } from "vitest"
import { applyPipelineParam, type RunFilter } from "@/lib/activity/run-filters"

describe("applyPipelineParam", () => {
  it("pins the routines filter to the slug from ?pipeline=", () => {
    const base: RunFilter = { status: "all" }
    expect(applyPipelineParam(base, "daily-etl")).toEqual({
      status: "all",
      routines: ["daily-etl"],
    })
  })

  it("replaces an existing routines filter — the deep-link wins", () => {
    const base: RunFilter = { status: "failed", routines: ["other-routine"] }
    expect(applyPipelineParam(base, "daily-etl")).toEqual({
      status: "failed",
      routines: ["daily-etl"],
    })
  })

  it("trims whitespace around the slug", () => {
    expect(applyPipelineParam({}, "  daily-etl ")).toEqual({
      routines: ["daily-etl"],
    })
  })

  it("returns the base filter untouched (same reference) when the param is absent or blank", () => {
    const base: RunFilter = { status: "all", crews: ["crew-1"] }
    expect(applyPipelineParam(base, null)).toBe(base)
    expect(applyPipelineParam(base, undefined)).toBe(base)
    expect(applyPipelineParam(base, "")).toBe(base)
    expect(applyPipelineParam(base, "   ")).toBe(base)
  })

  it("returns the same reference when the filter already pins exactly that routine", () => {
    const base: RunFilter = { status: "all", routines: ["daily-etl"] }
    expect(applyPipelineParam(base, "daily-etl")).toBe(base)
  })

  it("does not mutate the base filter", () => {
    const base: RunFilter = { status: "all", routines: ["other"] }
    applyPipelineParam(base, "daily-etl")
    expect(base.routines).toEqual(["other"])
  })
})
