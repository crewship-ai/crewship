import { describe, expect, it } from "vitest"

import { firingNext, type FiringSchedule } from "@/lib/firing-next"

const NOW = Date.parse("2026-08-10T12:00:00.000Z")
const inMin = (n: number) => new Date(NOW + n * 60_000).toISOString()

const sched = (over: Partial<FiringSchedule> = {}): FiringSchedule => ({
  id: "s1",
  name: "Nightly deploy",
  cron_expr: "0 2 * * *",
  enabled: true,
  next_run_at: inMin(30),
  ...over,
})

describe("firingNext", () => {
  it("orders soonest first", () => {
    const rows = firingNext(
      [sched({ id: "late", next_run_at: inMin(90) }), sched({ id: "soon", next_run_at: inMin(5) })],
      NOW,
    )
    expect(rows.map((r) => r.id)).toEqual(["soon", "late"])
  })

  it("drops a disabled schedule — it is not firing next, it is not firing", () => {
    expect(firingNext([sched({ enabled: false })], NOW)).toEqual([])
  })

  it("drops a schedule with no next time rather than sorting it to the top", () => {
    // next_run_at is omitted on a schedule the server has not computed one for.
    // Absent sorting as 0 would put it first, which is the loudest possible
    // place for the row that knows the least.
    expect(firingNext([sched({ next_run_at: undefined })], NOW)).toEqual([])
  })

  it("drops one whose time has passed — 'next' is a claim about the future", () => {
    // Reachable between a tick and a refresh: the row is stale, not wrong, and
    // rendering it under "firing next" would be a countdown running backwards.
    expect(firingNext([sched({ next_run_at: inMin(-5) })], NOW)).toEqual([])
  })

  it("counts down in words a person reads at a glance", () => {
    const [a] = firingNext([sched({ next_run_at: inMin(0.2) })], NOW)
    expect(a.dueIn).toBe("in 12s")
    const [b] = firingNext([sched({ next_run_at: inMin(45) })], NOW)
    expect(b.dueIn).toBe("in 45m")
    const [c] = firingNext([sched({ next_run_at: inMin(200) })], NOW)
    expect(c.dueIn).toBe("in 3h")
  })

  it("carries the routine the schedule fires, not the schedule's own name", () => {
    // "Nightly deploy" is what somebody called the cron; the reader is looking
    // for which ROUTINE is about to run.
    const [row] = firingNext([sched({ target_pipeline_slug: "classify-ticket" })], NOW)
    expect(row.slug).toBe("classify-ticket")
    expect(row.name).toBe("Nightly deploy")
  })

  it("caps the list — this is a glance, not a calendar", () => {
    const many = Array.from({ length: 10 }, (_, i) => sched({ id: `s${i}`, next_run_at: inMin(i + 1) }))
    expect(firingNext(many, NOW)).toHaveLength(4)
  })

  it("returns nothing for nothing", () => {
    expect(firingNext([], NOW)).toEqual([])
  })
})
