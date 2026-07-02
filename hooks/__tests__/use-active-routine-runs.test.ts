import { describe, it, expect } from "vitest"
import {
  deriveActiveRoutineRuns,
  deriveRecentTerminalRuns,
  isAwaitingApproval,
} from "@/hooks/use-active-routine-runs"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

function run(overrides: Partial<PipelineRun>): PipelineRun {
  return {
    id: "run-1",
    pipeline_id: "pipe-1",
    pipeline_slug: "daily-etl",
    pipeline_name: "Daily ETL",
    status: "running",
    mode: "run",
    started_at: "2026-07-02T10:00:00Z",
    ended_at: "",
    current_step_id: "step-1",
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
    ...overrides,
  } as PipelineRun
}

describe("isAwaitingApproval", () => {
  it("treats waiting and paused as awaiting a human", () => {
    expect(isAwaitingApproval("waiting")).toBe(true)
    expect(isAwaitingApproval("paused")).toBe(true)
    expect(isAwaitingApproval("running")).toBe(false)
    expect(isAwaitingApproval("queued")).toBe(false)
    expect(isAwaitingApproval("completed")).toBe(false)
  })
})

describe("deriveActiveRoutineRuns", () => {
  it("keeps only active runs, newest first", () => {
    const derived = deriveActiveRoutineRuns([
      run({ id: "old", started_at: "2026-07-02T09:00:00Z" }),
      run({ id: "done", status: "completed", started_at: "2026-07-02T11:00:00Z" }),
      run({ id: "new", started_at: "2026-07-02T10:30:00Z" }),
      run({ id: "parked", status: "waiting", started_at: "2026-07-02T10:15:00Z" }),
    ])
    expect(derived.runs.map((r) => r.id)).toEqual(["new", "parked", "old"])
    expect(derived.activeCount).toBe(3)
  })

  it("counts runs awaiting approval (waiting/paused)", () => {
    const derived = deriveActiveRoutineRuns([
      run({ id: "a", status: "running" }),
      run({ id: "b", status: "waiting" }),
      run({ id: "c", status: "paused" }),
    ])
    expect(derived.awaitingApproval).toBe(2)
  })

  it("maps the newest active run per pipeline slug", () => {
    const derived = deriveActiveRoutineRuns([
      run({ id: "older", pipeline_slug: "etl", started_at: "2026-07-02T09:00:00Z" }),
      run({ id: "newer", pipeline_slug: "etl", started_at: "2026-07-02T10:00:00Z" }),
      run({ id: "other", pipeline_slug: "digest", started_at: "2026-07-02T08:00:00Z" }),
    ])
    expect(derived.bySlug.get("etl")?.id).toBe("newer")
    expect(derived.bySlug.get("digest")?.id).toBe("other")
    expect(derived.bySlug.size).toBe(2)
  })

  it("returns empty derivations for no runs", () => {
    const derived = deriveActiveRoutineRuns([])
    expect(derived.runs).toEqual([])
    expect(derived.activeCount).toBe(0)
    expect(derived.awaitingApproval).toBe(0)
    expect(derived.bySlug.size).toBe(0)
  })
})

// RECENT section of the Activity dropdown: last few terminal runs
// (completed/failed) out of the same "all" feed the provider already
// polls — no second fetch.
describe("deriveRecentTerminalRuns", () => {
  it("keeps only completed/failed runs, newest ended first, capped", () => {
    const recent = deriveRecentTerminalRuns(
      [
        run({ id: "live", status: "running" }),
        run({ id: "parked", status: "waiting" }),
        run({ id: "c1", status: "completed", ended_at: "2026-07-02T10:00:00Z" }),
        run({ id: "f1", status: "failed", ended_at: "2026-07-02T11:00:00Z" }),
        run({ id: "c2", status: "completed", ended_at: "2026-07-02T09:00:00Z" }),
        run({ id: "c3", status: "completed", ended_at: "2026-07-02T08:00:00Z" }),
      ],
      3,
    )
    expect(recent.map((r) => r.id)).toEqual(["f1", "c1", "c2"])
  })

  it("excludes cancelled/interrupted and falls back to started_at when ended_at is empty", () => {
    const recent = deriveRecentTerminalRuns([
      run({ id: "x", status: "cancelled", ended_at: "2026-07-02T12:00:00Z" }),
      run({ id: "y", status: "interrupted", ended_at: "2026-07-02T12:00:00Z" }),
      run({ id: "no-end", status: "completed", ended_at: "", started_at: "2026-07-02T11:30:00Z" }),
      run({ id: "older", status: "completed", ended_at: "2026-07-02T10:00:00Z" }),
    ])
    expect(recent.map((r) => r.id)).toEqual(["no-end", "older"])
  })

  it("returns an empty list when nothing is terminal", () => {
    expect(deriveRecentTerminalRuns([run({ id: "live" })])).toEqual([])
  })
})
