import { describe, it, expect } from "vitest"

import { routineChecks, checkupCounts, routineHealthRows, type HealthRun } from "../routine-health"

const run = (i: number, over: Partial<HealthRun> = {}): HealthRun => ({
  id: `r${i}`,
  pipeline_slug: "weekly",
  status: "completed",
  started_at: new Date(Date.UTC(2026, 9, 17, 12) - i * 3_600_000).toISOString(),
  ...over,
})

describe("routineChecks (server data only)", () => {
  it("passes a routine that finishes", () => {
    const checks = routineChecks({ routine: { status: "active" }, runs: [run(0), run(1)] })
    expect(checks.every((c) => c.tone === "ok")).toBe(true)
    expect(checkupCounts(checks)).toMatchObject({ bad: 0, warn: 0, ok: checks.length, all: checks.length })
  })

  it("counts failures in a row from the newest run, without inventing a breaker", () => {
    const checks = routineChecks({ routine: { status: "active" }, runs: [run(0, { status: "failed" }), run(1, { status: "failed" }), run(2)] })
    const c = checks.find((x) => x.key === "failures-in-a-row")!
    expect(c.tone).toBe("warn")
    expect(c.title).toBe("2 failures in a row")
    expect(c.detail).not.toMatch(/turns itself off|breaker/i)
  })

  it("reports a waiting decision", () => {
    const c = routineChecks({ routine: { status: "active" }, runs: [run(0, { status: "waiting" })] }).find((x) => x.key === "decision-waiting")!
    expect(c.tone).toBe("warn")
  })

  it("reports a proposed routine as needing a Manager", () => {
    const c = routineChecks({ routine: { status: "proposed" }, runs: [] }).find((x) => x.key === "proposed")!
    expect(c.tone).toBe("bad")
  })

  it("reports a turned-off routine", () => {
    expect(routineChecks({ routine: { status: "disabled" }, runs: [] }).find((x) => x.key === "off")?.tone).toBe("bad")
  })

  it("reports an unpublished draft as changes not live", () => {
    const c = routineChecks({ routine: { status: "active", draft: { revision: 3 } }, runs: [] }).find((x) => x.key === "untested-changes")!
    expect(c.tone).toBe("warn")
  })

  it("reports a missing credential from the last failure", () => {
    const c = routineChecks({ routine: { status: "active" }, runs: [run(0, { status: "failed", error_message: "missing_credential: linear" })] }).find((x) => x.key === "missing-credential")!
    expect(c.tone).toBe("bad")
  })

  it("never lists a check it cannot verify", () => {
    const keys = routineChecks({ routine: { status: "active" }, runs: [run(0)] }).map((c) => c.key)
    expect(keys).not.toContain("agents-exist")
    expect(keys).not.toContain("cost")
  })
})

describe("routineHealthRows", () => {
  const routines = [
    { slug: "weekly", name: "Weekly", status: "active" as const },
    { slug: "quiet", name: "Quiet", status: "active" as const },
  ]
  it("takes the newest 20 runs per routine, oldest first, with success over finished runs", () => {
    const runs = Array.from({ length: 25 }, (_, i) => run(i, i % 5 === 0 ? { status: "failed" } : {}))
    const [row] = routineHealthRows(routines, runs, null)
    expect(row.slug).toBe("weekly")
    expect(row.bars).toHaveLength(20)
    expect(row.bars[19].id).toBe("r0")
    expect(row.finished).toBe(20)
    expect(row.ok).toBe(16)
    expect(row.successPct).toBe(80)
  })

  it("has no rate for a routine with no finished runs", () => {
    const rows = routineHealthRows(routines, [], null)
    expect(rows[1].successPct).toBeNull()
    expect(rows[1].bars).toEqual([])
  })

  it("shows spend as this month from the budget summary, never as 30 days", () => {
    const rows = routineHealthRows(routines, [], { month: "2026-10", routines: [{ slug: "weekly", monthly_budget_usd: 0, spent_usd: 4.5 }], total_budget_usd: 0, total_spent_usd: 4.5 })
    expect(rows[0].spent).toBe(4.5)
    expect(rows[1].spent).toBe(0)
    expect(routineHealthRows(routines, [], null)[0].spent).toBeNull()
  })

  it("carries the checkup counts and sorts routines to fix first", () => {
    const rows = routineHealthRows([{ slug: "a", name: "A", status: "active" as const }, { slug: "b", name: "B", status: "proposed" as const }], [], null)
    expect(rows[0].slug).toBe("b")
    expect(rows[0].checkup.bad).toBe(1)
  })
})

describe("routineHealthRows with the server's figures (ODCH-RT-07)", () => {
  const routines = [{ slug: "weekly", name: "Weekly" }, { slug: "idle", name: "Idle" }]
  const server = new Map([
    ["weekly", { slug: "weekly", last_outcomes: ["completed", "completed", "failed", "stopped", "running"], finished: 3, succeeded: 2, success_pct: 67, runs: 40, spend_usd: 4.2 }],
  ])

  it("takes bars, success rate and 30-day spend from the aggregate, not from the loaded runs", () => {
    const loaded = [{ id: "r1", pipeline_slug: "weekly", status: "failed", started_at: "2026-10-17T10:00:00Z" }]
    const rows = routineHealthRows(routines, loaded, null, 20, server)
    const weekly = rows.find((r) => r.slug === "weekly")!
    expect(weekly.bars.map((b) => b.tone)).toEqual(["ok", "ok", "bad", "mut", "info"])
    expect(weekly).toMatchObject({ runCount: 5, finished: 3, ok: 2, successPct: 67, spent: 4.2, spentWindow: "30 days" })
  })

  it("a routine the aggregate does not list has not run in that history", () => {
    const idle = routineHealthRows(routines, [], null, 20, server).find((r) => r.slug === "idle")!
    expect(idle).toMatchObject({ runCount: 0, successPct: null, spent: null })
  })

  it("without the aggregate it still says this month", () => {
    const row = routineHealthRows(routines, [], { month: "2026-10", routines: [{ slug: "weekly", monthly_budget_usd: 0, spent_usd: 1 }], total_budget_usd: 0, total_spent_usd: 1 }, 20)[0]
    expect(row).toMatchObject({ spent: 1, spentWindow: "this month" })
  })
})
