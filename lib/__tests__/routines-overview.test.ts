import { describe, it, expect } from "vitest"

import {
  runsToday,
  successRate,
  nextScheduled,
  needsAttention,
  catalogBuckets,
  upcomingSchedules,
  recentRuns,
  spendByDay,
} from "@/lib/routines-overview"

// The overview replaces a 38-row table in which 37 rows said "never
// invoked". Everything below is the arithmetic behind what took its
// place, kept out of the component so the numbers can be argued with
// directly.
//
// Dates are built with the LOCAL constructor rather than ISO literals:
// "is this run from today" is a local-calendar question, and a UTC
// literal makes the answer depend on the machine's timezone.

const NOW = new Date(2026, 7, 4, 12, 0, 0) // 4 Aug 2026, midday, local
const at = (d: number, h: number) => new Date(2026, 7, d, h, 0, 0).toISOString()

function run(over: Partial<Record<string, unknown>> = {}) {
  return {
    id: "run-1",
    pipeline_slug: "nightly",
    status: "completed",
    started_at: at(4, 9),
    cost_usd: 0,
    duration_ms: 1000,
    triggered_via: "manual",
    ...over,
  } as Parameters<typeof runsToday>[0][number]
}

function routine(over: Record<string, unknown> = {}) {
  return {
    slug: "nightly",
    invocation_count: 0,
    ...over,
  } as Parameters<typeof catalogBuckets>[0][number]
}

function schedule(over: Record<string, unknown> = {}) {
  return {
    id: "sch-1",
    name: "Morning",
    enabled: true,
    cron_expr: "0 8 * * *",
    ...over,
  } as Parameters<typeof nextScheduled>[0][number]
}

describe("runsToday", () => {
  it("counts only runs started on the current local day", () => {
    const runs = [run({ started_at: at(4, 9) }), run({ started_at: at(3, 23) })]
    expect(runsToday(runs, NOW)).toEqual({ total: 1, failed: 0 })
  })

  it("counts failures within the day separately", () => {
    const runs = [
      run({ started_at: at(4, 9) }),
      run({ started_at: at(4, 10), status: "failed" }),
      run({ started_at: at(3, 10), status: "failed" }),
    ]
    expect(runsToday(runs, NOW)).toEqual({ total: 2, failed: 1 })
  })

  it("ignores a run with an unparseable timestamp instead of counting it", () => {
    expect(runsToday([run({ started_at: "" })], NOW)).toEqual({ total: 0, failed: 0 })
  })
})

describe("successRate", () => {
  it("is null with nothing terminal to judge", () => {
    expect(successRate([run({ status: "running" })], NOW, 7)).toEqual({
      pct: null,
      ok: 0,
      total: 0,
    })
  })

  it("reports the denominator alongside the percentage", () => {
    // The old KPI read "100%" off a single run. A rate without its
    // denominator is a number the reader cannot weigh.
    const runs = [run({}), run({}), run({ status: "failed" })]
    expect(successRate(runs, NOW, 7)).toEqual({ pct: 67, ok: 2, total: 3 })
  })

  it("excludes runs older than the window", () => {
    const runs = [run({ started_at: at(4, 9) }), run({ started_at: new Date(2026, 6, 1).toISOString() })]
    expect(successRate(runs, NOW, 7).total).toBe(1)
  })

  it("does not let a cancelled run count as a verdict either way", () => {
    // Cancelling is a human changing their mind, not the routine
    // failing — counting it as a failure would make the health number
    // punish the operator for intervening.
    const runs = [run({}), run({ status: "cancelled" })]
    expect(successRate(runs, NOW, 7)).toEqual({ pct: 100, ok: 1, total: 1 })
  })
})

describe("nextScheduled", () => {
  it("returns the earliest future firing", () => {
    const s = [
      schedule({ id: "a", next_run_at: at(4, 18) }),
      schedule({ id: "b", next_run_at: at(4, 14) }),
    ]
    expect(nextScheduled(s, NOW)?.id).toBe("b")
  })

  it("skips paused schedules", () => {
    const s = [
      schedule({ id: "paused", enabled: false, next_run_at: at(4, 13) }),
      schedule({ id: "live", next_run_at: at(4, 15) }),
    ]
    expect(nextScheduled(s, NOW)?.id).toBe("live")
  })

  it("skips a firing time already in the past", () => {
    // A stale next_run_at means the scheduler has not caught up. It is
    // not "the next run", and showing it as one reads as a stuck clock.
    expect(nextScheduled([schedule({ next_run_at: at(4, 6) })], NOW)).toBeNull()
  })

  it("is null when nothing is scheduled", () => {
    expect(nextScheduled([], NOW)).toBeNull()
  })
})

describe("needsAttention", () => {
  it("adds failing routines to ones awaiting approval", () => {
    const routines = [
      routine({ slug: "a", last_invocation_status: "failed", invocation_count: 2 }),
      routine({ slug: "b", status: "proposed" }),
      routine({ slug: "c", last_invocation_status: "completed", invocation_count: 1 }),
    ]
    expect(needsAttention(routines)).toEqual({ total: 2, failing: 1, awaitingApproval: 1 })
  })

  it("counts a routine that is both failing and proposed exactly once", () => {
    const routines = [
      routine({ slug: "a", status: "proposed", last_invocation_status: "failed", invocation_count: 1 }),
    ]
    expect(needsAttention(routines).total).toBe(1)
  })
})

describe("catalogBuckets", () => {
  it("separates the never-invoked majority from the rest", () => {
    // This is the fact the old table buried: 37 of 38 rows were
    // identical placeholders. As an arc it is the first thing you see.
    const routines = [
      ...Array.from({ length: 37 }, (_, i) => routine({ slug: `n${i}` })),
      routine({ slug: "ran", invocation_count: 1, last_invocation_status: "completed" }),
    ]
    const buckets = catalogBuckets(routines, new Set())
    const by = Object.fromEntries(buckets.map((b) => [b.key, b.count]))
    expect(by.never).toBe(37)
    expect(by.healthy).toBe(1)
  })

  it("keeps every bucket present so the legend does not reshuffle", () => {
    const buckets = catalogBuckets([routine({})], new Set())
    expect(buckets.map((b) => b.key)).toEqual([
      "live",
      "healthy",
      "failing",
      "awaiting",
      "disabled",
      "never",
    ])
  })

  it("counts a live routine as live rather than by its last result", () => {
    const routines = [routine({ slug: "busy", invocation_count: 3, last_invocation_status: "failed" })]
    const by = Object.fromEntries(
      catalogBuckets(routines, new Set(["busy"])).map((b) => [b.key, b.count]),
    )
    expect(by.live).toBe(1)
    expect(by.failing).toBe(0)
  })

  it("sums to the number of routines — every routine lands in exactly one arc", () => {
    const routines = [
      routine({ slug: "a" }),
      routine({ slug: "b", invocation_count: 1, last_invocation_status: "completed" }),
      routine({ slug: "c", invocation_count: 1, last_invocation_status: "failed" }),
      routine({ slug: "d", status: "proposed" }),
      routine({ slug: "e", status: "disabled" }),
      routine({ slug: "f", invocation_count: 2 }),
    ]
    const total = catalogBuckets(routines, new Set(["f"])).reduce((s, b) => s + b.count, 0)
    expect(total).toBe(routines.length)
  })
})

describe("upcomingSchedules", () => {
  it("orders by firing time and caps the list", () => {
    const s = [
      schedule({ id: "c", next_run_at: at(6, 8) }),
      schedule({ id: "a", next_run_at: at(4, 20) }),
      schedule({ id: "b", next_run_at: at(5, 8) }),
    ]
    expect(upcomingSchedules(s, NOW, 2).map((x) => x.id)).toEqual(["a", "b"])
  })

  it("leaves out paused schedules and past times", () => {
    const s = [
      schedule({ id: "off", enabled: false, next_run_at: at(4, 20) }),
      schedule({ id: "stale", next_run_at: at(3, 8) }),
    ]
    expect(upcomingSchedules(s, NOW, 5)).toEqual([])
  })
})

describe("recentRuns", () => {
  it("pins live runs above finished ones regardless of start time", () => {
    // A run happening NOW is what an operator is looking for, even if
    // it started before the last completed one.
    const runs = [
      run({ id: "done", started_at: at(4, 11) }),
      run({ id: "live", started_at: at(4, 9), status: "running" }),
    ]
    expect(recentRuns(runs, 10).map((r) => r.id)).toEqual(["live", "done"])
  })

  it("orders finished runs newest first and caps the list", () => {
    const runs = [
      run({ id: "old", started_at: at(4, 8) }),
      run({ id: "new", started_at: at(4, 11) }),
      run({ id: "mid", started_at: at(4, 10) }),
    ]
    expect(recentRuns(runs, 2).map((r) => r.id)).toEqual(["new", "mid"])
  })
})

describe("spendByDay", () => {
  it("returns one bucket per day in the window, oldest first", () => {
    const series = spendByDay([], NOW, 7)
    expect(series).toHaveLength(7)
    expect(series[series.length - 1].isToday).toBe(true)
  })

  it("sums cost into the day the run started", () => {
    const runs = [
      run({ started_at: at(4, 9), cost_usd: 0.002 }),
      run({ started_at: at(4, 10), cost_usd: 0.001 }),
      run({ started_at: at(3, 10), cost_usd: 0.5 }),
    ]
    const series = spendByDay(runs, NOW, 7)
    expect(series[series.length - 1].usd).toBeCloseTo(0.003, 6)
    expect(series[series.length - 2].usd).toBeCloseTo(0.5, 6)
  })

  it("ignores a negative or non-finite cost rather than subtracting it", () => {
    const runs = [run({ cost_usd: -5 }), run({ cost_usd: Number.NaN })]
    expect(spendByDay(runs, NOW, 7).every((d) => d.usd === 0)).toBe(true)
  })
})
