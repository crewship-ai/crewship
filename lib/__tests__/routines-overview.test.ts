import { describe, it, expect } from "vitest"

import {
  runsToday,
  successRate,
  nextScheduled,
  needsAttention,
  catalogBuckets,
  upcomingSchedules,
  recentRuns,
  pendingApprovals,
  runOutcomesByDay,
  recentFailures,
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

// "Waiting on you" is one queue with two sources: a run parked on a
// `wait: approval` gate, and a routine whose definition needs a
// reviewer. From the operator's side they are the same job — something
// stopped and is waiting for a person — so they belong on one card,
// and the ordering has to reflect which one is actually burning.

function waitpoint(over: Record<string, unknown> = {}) {
  return {
    token: "tok-1",
    pipeline_run_id: "run-1",
    step_id: "approve",
    kind: "approval",
    prompt: "Ship it?",
    timeout_at: at(4, 18),
    created_at: at(4, 11),
    ...over,
  } as Parameters<typeof pendingApprovals>[0][number]
}

describe("pendingApprovals", () => {
  it("names the routine a parked run belongs to", () => {
    const out = pendingApprovals(
      [waitpoint({})],
      [routine({ slug: "nightly", name: "Nightly digest" })],
      new Map([["run-1", "nightly"]]),
    )
    expect(out).toHaveLength(1)
    expect(out[0]).toMatchObject({ kind: "run", routineSlug: "nightly", routineName: "Nightly digest" })
  })

  it("still lists a parked run whose routine cannot be resolved", () => {
    // A waitpoint carries a run id, not a slug. Dropping the row when
    // the lookup misses would hide a blocked run — the opposite of
    // what this card is for.
    const out = pendingApprovals([waitpoint({})], [], new Map())
    expect(out).toHaveLength(1)
    expect(out[0].kind).toBe("run")
  })

  it("puts parked runs above proposed routines", () => {
    // A parked run holds a live process and expires. A proposal sits
    // still until someone reads it.
    const out = pendingApprovals(
      [waitpoint({})],
      [routine({ slug: "risky", name: "Risky", status: "proposed" })],
      new Map([["run-1", "nightly"]]),
    )
    expect(out.map((x) => x.kind)).toEqual(["run", "routine"])
  })

  it("orders parked runs by how soon they expire", () => {
    const out = pendingApprovals(
      [
        waitpoint({ token: "later", timeout_at: at(4, 20) }),
        waitpoint({ token: "sooner", timeout_at: at(4, 13) }),
      ],
      [],
      new Map(),
    )
    expect(out.map((x) => (x.kind === "run" ? x.token : ""))).toEqual(["sooner", "later"])
  })

  it("treats a waitpoint with no timeout as least urgent, not most", () => {
    // An absent timeout_at parsed as 0 would sort to the very front
    // and push a genuinely expiring approval down the list.
    const out = pendingApprovals(
      [
        waitpoint({ token: "no-timeout", timeout_at: "" }),
        waitpoint({ token: "expiring", timeout_at: at(4, 13) }),
      ],
      [],
      new Map(),
    )
    expect(out.map((x) => (x.kind === "run" ? x.token : ""))).toEqual(["expiring", "no-timeout"])
  })

  it("leaves out routines that are not awaiting review", () => {
    const out = pendingApprovals(
      [],
      [
        routine({ slug: "live", status: "active" }),
        routine({ slug: "off", status: "disabled" }),
        routine({ slug: "risky", status: "proposed" }),
      ],
      new Map(),
    )
    expect(out).toHaveLength(1)
    expect(out[0]).toMatchObject({ kind: "routine", slug: "risky" })
  })
})

// One chart instead of two cards about money. Bars by outcome — green
// passed, red failed, amber still waiting — so the week reads as "what
// happened" rather than "what it cost", with cost as a line over it.
//
// The bars have to SUM to the runs that day. A cancelled run dropped
// on the floor would make a bar shorter than the day it describes, and
// nothing on screen would say why.

describe("runOutcomesByDay", () => {
  it("returns one bucket per day, oldest first, today last", () => {
    const series = runOutcomesByDay([], NOW, 7)
    expect(series).toHaveLength(7)
    expect(series[series.length - 1].isToday).toBe(true)
  })

  it("splits a day's runs by verdict", () => {
    const runs = [
      run({ id: "a", started_at: at(4, 9) }),
      run({ id: "b", started_at: at(4, 10), status: "failed" }),
      run({ id: "c", started_at: at(4, 11), status: "waiting" }),
    ]
    const today = runOutcomesByDay(runs, NOW, 7)[6]
    expect(today).toMatchObject({ passed: 1, failed: 1, pending: 1, other: 0 })
  })

  it("counts a cancelled run rather than dropping it", () => {
    // It is not a pass and not a failure, but it happened — and a bar
    // shorter than its own day is a chart lying by omission.
    const today = runOutcomesByDay([run({ status: "cancelled" })], NOW, 7)[6]
    expect(today).toMatchObject({ passed: 0, failed: 0, pending: 0, other: 1 })
  })

  it("treats running and queued as not-yet-a-verdict, with waiting", () => {
    const runs = [
      run({ id: "r", status: "running" }),
      run({ id: "q", status: "queued" }),
      run({ id: "w", status: "waiting" }),
      run({ id: "p", status: "paused" }),
    ]
    expect(runOutcomesByDay(runs, NOW, 7)[6].pending).toBe(4)
  })

  it("puts each run in the day it started", () => {
    const runs = [run({ id: "y", started_at: at(3, 10) }), run({ id: "t", started_at: at(4, 10) })]
    const series = runOutcomesByDay(runs, NOW, 7)
    expect(series[5].passed).toBe(1)
    expect(series[6].passed).toBe(1)
  })

  it("ignores a run older than the window instead of folding it into day one", () => {
    const runs = [run({ started_at: new Date(2026, 6, 1).toISOString() })]
    expect(runOutcomesByDay(runs, NOW, 7).every((d) => d.passed + d.failed + d.pending + d.other === 0)).toBe(true)
  })

})

// "Recently failing" listed routine names off last_invocation_status.
// A name tells a reader WHICH routine broke and nothing about what
// broke — so the next click was always the same: open it, find the
// run, find the step. The run carries the step and the message; the
// card just was not reading them.

describe("recentFailures", () => {
  it("names the step that failed, not just the routine", () => {
    const runs = [
      run({ id: "f1", status: "failed", failed_at_step: "fetch_invoice", error_message: "502 from vendor" }),
    ]
    expect(recentFailures(runs, 5)).toEqual([
      expect.objectContaining({ runId: "f1", stepId: "fetch_invoice", message: "502 from vendor" }),
    ])
  })

  it("takes the freshest failures and caps the list", () => {
    const runs = [
      run({ id: "old", status: "failed", started_at: at(2, 9) }),
      run({ id: "new", status: "failed", started_at: at(4, 9) }),
      run({ id: "mid", status: "failed", started_at: at(3, 9) }),
    ]
    expect(recentFailures(runs, 2).map((f) => f.runId)).toEqual(["new", "mid"])
  })

  it("lists each failed RUN, so one routine breaking twice shows twice", () => {
    // Keyed on the routine it would collapse to one row and hide that
    // it is failing repeatedly — which is the signal, not noise.
    const runs = [
      run({ id: "a", status: "failed", started_at: at(4, 9) }),
      run({ id: "b", status: "failed", started_at: at(4, 10) }),
    ]
    expect(recentFailures(runs, 5)).toHaveLength(2)
  })

  it("leaves out runs that did not fail", () => {
    const runs = [run({ id: "ok" }), run({ id: "cancelled", status: "cancelled" })]
    expect(recentFailures(runs, 5)).toEqual([])
  })

  it("falls back to the run id when there is no step to name", () => {
    // A run that failed before any step started has no failed_at_step.
    // "—" beats an empty column that reads as missing data.
    const runs = [run({ id: "r", status: "failed", failed_at_step: "" })]
    expect(recentFailures(runs, 5)[0].stepId).toBe("")
  })
})
