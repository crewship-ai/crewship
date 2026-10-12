import { describe, expect, it } from "vitest"

import { focusRuns, rowKind } from "../activity-rail-focus"
import type { ChainSummary } from "@/hooks/use-chains"

const NOW = new Date("2026-10-08T12:00:00")
const rec = (id: string, at: Date, status = "completed", duration_ms = 300) => ({
  id,
  status,
  started_at: at.toISOString(),
  duration_ms,
  triggered_via: "manual",
})
const hoursAgo = (h: number) => new Date(NOW.getTime() - h * 3_600_000)

describe("focusRuns (#2998)", () => {
  const records = [
    rec("r1", hoursAgo(0.1)),
    rec("r2", hoursAgo(2), "failed"),
    rec("r3", hoursAgo(20), "cancelled"),
    rec("r4", hoursAgo(30)),
    rec("r5", hoursAgo(24 * 10)),
  ]

  it("groups a routine's runs by day, newest first, with their time and outcome", () => {
    const out = focusRuns(records, { scope: "all", rangeMs: 7 * 24 * 3_600_000, now: NOW.getTime() })
    expect(out.days.map((d) => [d.label, d.runs.map((r) => r.id)])).toEqual([
      ["Today", ["r1", "r2"]],
      ["Yesterday", ["r3", "r4"]],
    ])
    expect(out.days[0].runs[1]).toMatchObject({ tone: "failed", time: expect.stringMatching(/^\d{2}:\d{2}$/) })
    expect(out.total).toBe(4)
  })

  it("narrows to one outcome and counts every outcome over the window", () => {
    const out = focusRuns(records, { scope: "failed", rangeMs: 7 * 24 * 3_600_000, now: NOW.getTime() })
    expect(out.days.flatMap((d) => d.runs.map((r) => r.id))).toEqual(["r2"])
    expect(out.counts).toEqual({ active: 0, waiting: 0, failed: 1, done: 2, stopped: 1 })
  })

  it("keeps to the time window", () => {
    const out = focusRuns(records, { scope: "all", rangeMs: 24 * 3_600_000, now: NOW.getTime() })
    expect(out.days.flatMap((d) => d.runs.map((r) => r.id))).toEqual(["r1", "r2", "r3"])
  })
})

describe("rowKind (#2998)", () => {
  const base = { origin: "o", started_by_kind: "user", started_by: "", runs: 1, max_chain_depth: 0, failed_runs: 0, failed: false, first_activity: "", last_activity: "", issue_count: 0, agent_count: 0 } as ChainSummary
  it("says what a row is, not only what started it", () => {
    expect(rowKind({ ...base, routine_slug: "match-payments" }).label).toBe("Routine")
    expect(rowKind({ ...base, routine_slug: "match-payments", started_by_kind: "issue", started_by_key: "QUA-1" }).label).toBe("Routine")
    expect(rowKind({ ...base, kind: "assignment", started_by_kind: "issue" }).label).toBe("Issue")
    expect(rowKind({ ...base, kind: "assignment", started_by_kind: "lead_planning" }).label).toBe("Issue")
    expect(rowKind({ ...base, kind: "assignment", started_by_kind: "agent" }).label).toBe("Agent")
  })
})
