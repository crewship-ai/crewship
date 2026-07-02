import { describe, it, expect } from "vitest"
import {
  applyFilters,
  applyPipelineParam,
  applyStatusParam,
  type RunFilter,
} from "@/lib/activity/run-filters"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

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

describe("applyStatusParam", () => {
  it("maps ?status=active onto the status axis", () => {
    const base: RunFilter = { status: "all" }
    expect(applyStatusParam(base, "active")).toEqual({ status: "active" })
  })

  it("accepts every rail status bucket", () => {
    expect(applyStatusParam({}, "completed")).toEqual({ status: "completed" })
    expect(applyStatusParam({}, "failed")).toEqual({ status: "failed" })
    expect(applyStatusParam({ status: "failed" }, "all")).toEqual({ status: "all" })
  })

  it("preserves the other filter dimensions", () => {
    const base: RunFilter = { status: "all", routines: ["daily-etl"], crews: ["crew-1"] }
    expect(applyStatusParam(base, "active")).toEqual({
      status: "active",
      routines: ["daily-etl"],
      crews: ["crew-1"],
    })
  })

  it("returns the base filter untouched (same reference) when the param is absent, blank or unknown", () => {
    const base: RunFilter = { status: "all" }
    expect(applyStatusParam(base, null)).toBe(base)
    expect(applyStatusParam(base, undefined)).toBe(base)
    expect(applyStatusParam(base, "")).toBe(base)
    expect(applyStatusParam(base, "   ")).toBe(base)
    expect(applyStatusParam(base, "bogus")).toBe(base)
  })

  it("returns the same reference when the status is already applied", () => {
    const base: RunFilter = { status: "active" }
    expect(applyStatusParam(base, "active")).toBe(base)
  })

  it("trims + lowercases the param", () => {
    expect(applyStatusParam({}, " Active ")).toEqual({ status: "active" })
  })

  it("does not mutate the base filter", () => {
    const base: RunFilter = { status: "all" }
    applyStatusParam(base, "active")
    expect(base.status).toBe("all")
  })
})

describe("applyFilters status=active", () => {
  const run = (id: string, status: string): PipelineRun =>
    ({
      id,
      pipeline_id: "pipe-1",
      pipeline_slug: "daily-etl",
      pipeline_name: "Daily ETL",
      status,
      mode: "run",
      started_at: new Date().toISOString(),
      ended_at: "",
      current_step_id: "",
      step_outputs: null,
      cost_usd: 0,
      duration_ms: 0,
      triggered_via: "manual",
      triggered_by_id: "",
      invoking_crew_id: "",
      invoking_agent_id: "",
      invoking_user_id: "",
      error_message: "",
      failed_at_step: "",
      issue_identifier: "",
    }) as PipelineRun

  it("includes waitpoint-parked runs (status=waiting) in the active bucket", () => {
    const runs = [
      run("r1", "running"),
      run("r2", "queued"),
      run("r3", "paused"),
      run("r4", "waiting"),
      run("r5", "completed"),
      run("r6", "failed"),
    ]
    const out = applyFilters(runs, { status: "active" })
    expect(out.map((r) => r.id)).toEqual(["r1", "r2", "r3", "r4"])
  })
})
