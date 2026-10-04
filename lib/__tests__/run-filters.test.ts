import { afterEach, describe, it, expect, vi } from "vitest"
import {
  applyFilters,
  activeFilterCount,
  groupRuns,
  applyPipelineParam,
  applyStatusParam,
  type RunFilter,
  type GroupAxis,
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

describe("deep-link param composition (?pipeline= + ?status= together)", () => {
  it("chaining both appliers keeps both dimensions — neither clobbers the other", () => {
    const base = { crews: ["crew-1"] }
    const next = applyStatusParam(applyPipelineParam(base, "daily-digest"), "active")
    expect(next).toEqual({ crews: ["crew-1"], routines: ["daily-digest"], status: "active" })
  })

  it("order does not matter", () => {
    const a = applyStatusParam(applyPipelineParam({}, "s"), "failed")
    const b = applyPipelineParam(applyStatusParam({}, "failed"), "s")
    expect(a).toEqual(b)
  })
})


const activityRun = (id: string, fields: Partial<PipelineRun> = {}): PipelineRun => ({
  id, pipeline_id: "pipeline", pipeline_slug: "daily-etl", pipeline_name: "Daily ETL", status: "completed",
  mode: "run", started_at: "2026-10-02T12:00:00Z", ended_at: "", current_step_id: "", cost_usd: 2,
  duration_ms: 1000, triggered_via: "manual", triggered_by_id: "", invoking_crew_id: "crew-1",
  invoking_agent_id: "agent-1", invoking_user_id: "", error_message: "", failed_at_step: "", issue_identifier: "ENG-7",
  ...fields,
})
afterEach(() => { vi.useRealTimers() })

describe("multi-dimensional activity filters", () => {
  it.each([
    ["all", ["running", "queued", "paused", "waiting", "completed", "failed", "cancelled", "interrupted"]],
    ["active", ["running", "queued", "paused", "waiting"]],
    ["completed", ["completed"]],
    ["failed", ["failed", "cancelled", "interrupted"]],
  ] as const)("selects the %s status bucket including terminal cancellation/interruption", (status, expected) => {
    const rows = ["running", "queued", "paused", "waiting", "completed", "failed", "cancelled", "interrupted"].map(s => activityRun(s, { status: s }))
    expect(applyFilters(rows, { status }).map(r => r.status)).toEqual(expected)
  })

  it.each([
    { crews: ["crew-1"] }, { agents: ["agent-1"] }, { routines: ["daily-etl"] },
    { sources: ["manual"] }, { issueIdentifiers: ["ENG-7"] },
  ] satisfies RunFilter[])("filters by the selected membership dimension %j", filter => {
    const match = activityRun("match")
    const other = activityRun("other", { invoking_crew_id: "crew-2", invoking_agent_id: "agent-2", pipeline_slug: "other", triggered_via: "webhook", issue_identifier: "" })
    expect(applyFilters([match, other], filter)).toEqual([match])
  })

  it("ANDs dimensions while treating empty membership selections as unrestricted", () => {
    const rows = [activityRun("match"), activityRun("wrong-agent", { invoking_agent_id: "other" }), activityRun("wrong-crew", { invoking_crew_id: "other" })]
    expect(applyFilters(rows, { crews: ["crew-1"], agents: ["agent-1"] }).map(r => r.id)).toEqual(["match"])
    expect(applyFilters(rows, { crews: [], agents: [], routines: [], sources: [], issueIdentifiers: [] })).toEqual(rows)
    expect(applyFilters(rows, { search: "  " })).toEqual(rows)
  })

  it.each(["MATCH-ID", "DAILY-ETL", "daily etl", "eng-7"])("searches visible identifiers/names case-insensitively (%s)", search => {
    expect(applyFilters([activityRun("match-id"), activityRun("other", { pipeline_slug: "other", pipeline_name: "Other", issue_identifier: "" })], { search: `  ${search}  ` }).map(r => r.id)).toEqual(["match-id"])
  })

  it("handles absent optional names and issue identifiers without leaking undefined into search", () => {
    const run = activityRun("id", { pipeline_name: undefined, issue_identifier: undefined } as unknown as Partial<PipelineRun>)
    expect(applyFilters([run], { search: "undefined" })).toEqual([])
    expect(applyFilters([run], { search: "id" })).toEqual([run])
  })

  it.each(["1h", "24h", "7d"] as const)("includes the %s cutoff exactly and excludes older/invalid timestamps", dateRange => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date("2026-10-02T12:00:00Z"))
    const duration = { "1h": 3600000, "24h": 86400000, "7d": 604800000 }[dateRange]
    const cutoff = Date.now() - duration
    const rows = [activityRun("boundary", { started_at: new Date(cutoff).toISOString() }), activityRun("old", { started_at: new Date(cutoff - 1).toISOString() }), activityRun("invalid", { started_at: "invalid" }), activityRun("missing", { started_at: "" })]
    expect(applyFilters(rows, { dateRange }).map(r => r.id)).toEqual(["boundary"])
    expect(applyFilters(rows, { dateRange: "all" })).toEqual(rows)
  })

  it("applies inclusive cost and duration bounds, including zero", () => {
    const rows = [activityRun("zero", { cost_usd: 0, duration_ms: 0 }), activityRun("boundary"), activityRun("above", { cost_usd: 3, duration_ms: 2000 })]
    expect(applyFilters(rows, { costMin: 0, costMax: 0 }).map(r => r.id)).toEqual(["zero"])
    expect(applyFilters(rows, { costMin: 2, costMax: 2 }).map(r => r.id)).toEqual(["boundary"])
    expect(applyFilters(rows, { durationMinMs: 0, durationMaxMs: 0 }).map(r => r.id)).toEqual(["zero"])
    expect(applyFilters(rows, { durationMinMs: 1000, durationMaxMs: 1000 }).map(r => r.id)).toEqual(["boundary"])
  })

  it("requires a known pending waitpoint when that filter is selected", () => {
    const rows = [activityRun("pending"), activityRun("done")]
    expect(applyFilters(rows, { hasWaitpoint: true }, new Set(["pending"])).map(r => r.id)).toEqual(["pending"])
    expect(applyFilters(rows, { hasWaitpoint: true }, new Set())).toEqual([])
    expect(applyFilters(rows, { hasWaitpoint: false })).toEqual(rows)
  })

  it("counts toolbar dimensions once, excluding the always-visible status and search controls", () => {
    expect(activeFilterCount({ status: "failed", search: "error", crews: [], dateRange: "all", hasWaitpoint: false })).toBe(0)
    expect(activeFilterCount({ crews: ["a", "b"], agents: ["a"], routines: ["r"], sources: ["manual"], issueIdentifiers: ["ENG-7"], dateRange: "1h", costMin: 0, costMax: 4, durationMinMs: 0, durationMaxMs: 10, hasWaitpoint: true })).toBe(9)
    expect(activeFilterCount({ costMax: 0, durationMaxMs: 0 })).toBe(2)
  })
})

describe("activity grouping", () => {
  it("rejects an unsupported grouping preference without inventing a group", () => {
    expect(groupRuns([activityRun("run")], "unsupported" as GroupAxis)).toEqual([])
  })
  it("collapses scheduled runs per routine and keeps trigger sources separate", () => {
    const rows = [activityRun("scheduled-old", { triggered_via: "schedule", started_at: "2026-10-01T00:00:00Z", status: "failed" }), activityRun("scheduled-new", { triggered_via: "schedule", status: "running" }), activityRun("issue", { triggered_via: "issue" }), activityRun("webhook", { triggered_via: "webhook" }), activityRun("manual"), activityRun("child", { triggered_via: "call_pipeline" }), activityRun("legacy", { triggered_via: "unknown" }), activityRun("missing", { triggered_via: "" })]
    const groups = groupRuns(rows, "source", { cronBySlug: new Map([["daily-etl", "0 * * * *"]]) })
    expect(groups.map(g => g.kind)).toEqual(["cron", "issue", "webhook", "manual", "call_pipeline"])
    expect(groups[0]).toMatchObject({ key: "src:cron", status: "running", totalRuns: 2, subgroups: [{ label: "Daily ETL", totalRuns: 2, metadata: { cronExpr: "0 * * * *", failureCount: 1 } }] })
    expect(groups[0].subgroups![0].runs!.map(r => r.id)).toEqual(["scheduled-new", "scheduled-old"])
    expect(groups[3].runs!.map(r => r.id)).toEqual(["manual", "legacy", "missing"])
  })

  it("orders routine groups by frequency and retains fallback names and failure totals", () => {
    const rows = [activityRun("a", { pipeline_slug: "rare", pipeline_name: "", status: "cancelled" }), activityRun("b"), activityRun("c", { status: "interrupted" })]
    const groups = groupRuns(rows, "routine", { routineNameBySlug: new Map([["rare", "Rare routine"]]) })
    expect(groups.map(g => g.key)).toEqual(["rt-daily-etl", "rt-rare"])
    expect(groups[1]).toMatchObject({ label: "rare", metadata: { routineName: "Rare routine", failureCount: 1 } })
  })

  it("groups by issue identifier and includes runs without an issue as an explicit orphan group", () => {
    const groups = groupRuns([activityRun("one"), activityRun("two"), activityRun("orphan", { issue_identifier: "", pipeline_name: "" })], "issue")
    expect(groups.map(g => g.totalRuns)).toEqual([2, 1])
    expect(groups[0]).toMatchObject({ key: "iss-ENG-7", metadata: { issueIdentifier: "ENG-7", routineSlug: "daily-etl" } })
    expect(groups[1]).toMatchObject({ key: "iss-_orphan", label: "Without an issue" })
    expect(groupRuns([activityRun("fallback", { pipeline_name: "" })], "issue")[0].label).toBe("daily-etl")
  })

  it("uses known crew names and stable fallbacks for unknown/missing crews", () => {
    const groups = groupRuns([activityRun("known"), activityRun("unknown", { invoking_crew_id: "crew-2" }), activityRun("none", { invoking_crew_id: "" })], "crew", { crewNameById: new Map([["crew-1", "Operations"]]) })
    expect(groups.map(g => g.label)).toEqual(["Operations", "crew-2", "No crew"])
    expect(groups[0].metadata?.crewName).toBe("Operations")
  })

  it.each([
    [["completed", "queued", "failed", "paused", "running"], "running"],
    [["completed", "queued", "failed", "waiting"], "paused"],
    [["completed", "queued", "cancelled"], "failed"],
    [["completed", "queued"], "queued"], [["completed"], "completed"], [["custom"], "mixed"],
  ] as const)("rolls up %j to the most urgent status %s", (statuses, expected) => {
    expect(groupRuns(statuses.map((status, i) => activityRun(String(i), { status })), "none")[0].status).toBe(expected)
  })

  it("sorts newest first without mutating input, puts missing/invalid timestamps last and handles empty lists", () => {
    const rows = [activityRun("missing", { started_at: "" }), activityRun("invalid", { started_at: "invalid" }), activityRun("old", { started_at: "2026-10-01T00:00:00Z" }), activityRun("new")]
    expect(groupRuns(rows, "none")[0].runs!.map(r => r.id)).toEqual(["new", "old", "missing", "invalid"])
    expect(rows.map(r => r.id)).toEqual(["missing", "invalid", "old", "new"])
    for (const axis of ["none", "source", "routine", "crew", "issue"] as const) expect(groupRuns([], axis)).toEqual([])
  })
})
