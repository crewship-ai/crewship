import { describe, expect, it } from "vitest"

import { groupRunsByHour, medianRunDuration, runHeadline, type DigestRun } from "@/lib/run-digest"

const run = (over: Partial<DigestRun> = {}): DigestRun => ({
  id: "run_a",
  status: "completed",
  started_at: "2026-08-10T12:51:00.000Z",
  duration_ms: 15_200,
  triggered_via: "schedule",
  ...over,
})

/** Local-clock helper: the buckets are the reader's hours, not UTC's. */
const atLocal = (h: number, m: number, base = Date.parse("2026-08-10T12:00:00.000Z")) => {
  const d = new Date(base)
  d.setHours(h, m, 0, 0)
  return d.toISOString()
}

describe("runHeadline", () => {
  it("is the failure when the run failed — that is what the row is for", () => {
    const h = runHeadline(run({ status: "failed", error_message: "step compute: timeout after 30s", output: "partial" }))
    expect(h.text).toBe("step compute: timeout after 30s")
    expect(h.tone).toBe("failed")
  })

  it("is the output's first line when the run finished", () => {
    expect(runHeadline(run({ output: "3 tickets classified\nENG-7, ENG-8, ENG-9" })).text).toBe("3 tickets classified")
  })

  it("truncates a long line rather than letting one run own the column", () => {
    const long = "x".repeat(200)
    const h = runHeadline(run({ output: long }))
    expect(h.text.length).toBeLessThanOrEqual(72)
    expect(h.text.endsWith("…")).toBe(true)
  })

  it("says nothing rather than inventing something when there is no output", () => {
    // The alternative is a stock phrase like "completed" on every row, which
    // is the run's status rendered twice and tells two runs apart never.
    expect(runHeadline(run({ output: "" })).text).toBe("")
  })

  it("collapses whitespace so a JSON blob does not become a wall", () => {
    expect(runHeadline(run({ output: "  {\n  \"ok\": true\n}  " })).text).toBe('{ "ok": true }')
  })

  it("names an in-flight run as running rather than as a result", () => {
    const h = runHeadline(run({ status: "running", output: "", duration_ms: 0 }))
    expect(h.tone).toBe("running")
  })

  it("flags a run that is slow against its own peers, not against a constant", () => {
    // 22s among 15s peers is the row worth spotting. A fixed threshold would
    // flag every run of a routine that simply takes a minute.
    const h = runHeadline(run({ duration_ms: 22_100 }), { medianMs: 15_000 })
    expect(h.tone).toBe("slow")
    expect(h.text).toContain("slow")
  })

  it("does not call a run slow when it has nothing to be slow against", () => {
    expect(runHeadline(run({ duration_ms: 22_100 })).tone).not.toBe("slow")
  })

  it("keeps the output when the run is merely slow — the result still matters", () => {
    const h = runHeadline(run({ duration_ms: 22_100, output: "3 tickets classified" }), { medianMs: 15_000 })
    expect(h.text).toContain("3 tickets classified")
    expect(h.text).toContain("slow")
  })
})

describe("groupRunsByHour", () => {
  it("groups by the reader's local hour", () => {
    const buckets = groupRunsByHour([
      run({ id: "a", started_at: atLocal(14, 51) }),
      run({ id: "b", started_at: atLocal(14, 3) }),
      run({ id: "c", started_at: atLocal(13, 59) }),
    ])
    expect(buckets.map((b) => b.runs.map((r) => r.id))).toEqual([["a", "b"], ["c"]])
  })

  it("labels the hour with the span the runs actually cover", () => {
    // "14:00 — 14:51" rather than "14:00 — 15:00": the second is the hour's
    // definition, the first is what happened in it.
    const [b] = groupRunsByHour([run({ started_at: atLocal(14, 51) }), run({ started_at: atLocal(14, 3) })])
    expect(b.label).toBe("14:03 — 14:51")
  })

  it("labels a single-run hour with the one time, not a span of zero", () => {
    const [b] = groupRunsByHour([run({ started_at: atLocal(14, 3) })])
    expect(b.label).toBe("14:03")
  })

  it("summarises so a quiet hour can be skipped without reading it", () => {
    const [b] = groupRunsByHour([run({ started_at: atLocal(14, 1) }), run({ started_at: atLocal(14, 2) })])
    expect(b.summary).toBe("2 runs · all ok")
  })

  it("counts the failures in the summary when there are any", () => {
    const [b] = groupRunsByHour([
      run({ started_at: atLocal(14, 1) }),
      run({ started_at: atLocal(14, 2), status: "failed" }),
      run({ started_at: atLocal(14, 3), status: "failed" }),
    ])
    expect(b.summary).toBe("3 runs · 2 failed")
  })

  it("reports in-flight work in the summary — it is not a result yet", () => {
    const [b] = groupRunsByHour([
      run({ started_at: atLocal(14, 1) }),
      run({ started_at: atLocal(14, 2), status: "running" }),
    ])
    expect(b.summary).toBe("2 runs · 1 running")
  })

  it("orders hours newest first and runs newest first inside them", () => {
    const buckets = groupRunsByHour([
      run({ id: "old", started_at: atLocal(13, 1) }),
      run({ id: "new", started_at: atLocal(14, 2) }),
      run({ id: "mid", started_at: atLocal(14, 1) }),
    ])
    expect(buckets[0].runs.map((r) => r.id)).toEqual(["new", "mid"])
    expect(buckets[1].runs.map((r) => r.id)).toEqual(["old"])
  })

  it("keeps a run with an unreadable start in its own bucket rather than dropping it", () => {
    const buckets = groupRunsByHour([run({ id: "bad", started_at: "not a date" })])
    expect(buckets).toHaveLength(1)
    expect(buckets[0].label).toBe("undated")
    expect(buckets[0].runs).toHaveLength(1)
  })

  it("puts the undated bucket last, whatever else is there", () => {
    const buckets = groupRunsByHour([
      run({ id: "bad", started_at: "" }),
      run({ id: "ok", started_at: atLocal(9, 0) }),
    ])
    expect(buckets.map((b) => b.label)).toEqual(["09:00", "undated"])
  })

  it("returns nothing for nothing", () => {
    expect(groupRunsByHour([])).toEqual([])
  })
})

describe("hour labels read the reader's clock", () => {
  it("prints the local wall time, not UTC", () => {
    // The grouping itself cannot prove this in a whole-hour timezone — local
    // and UTC hours partition the same runs identically there, and only a
    // half-hour offset would split them. The LABEL is where the choice becomes
    // visible, so that is what this pins: a stamp is rendered as the hour the
    // reader's clock showed, whatever offset they are on.
    const at = "2026-08-10T12:51:00.000Z"
    const expected = new Date(at).toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })
    expect(groupRunsByHour([run({ started_at: at })])[0].label).toBe(expected)
  })
})

describe("medianRunDuration", () => {
  it("is the middle of the finished runs", () => {
    expect(
      medianRunDuration([run({ duration_ms: 10 }), run({ duration_ms: 30 }), run({ duration_ms: 20 })]),
    ).toBe(20)
  })

  it("averages the two middles on an even count", () => {
    expect(
      medianRunDuration([run({ duration_ms: 10 }), run({ duration_ms: 30 })]),
    ).toBe(20)
  })

  it("excludes an in-flight run — its duration is a partial value, not a result", () => {
    // duration_ms is rewritten at every step boundary, so a live run carries
    // however far it has got. Letting it in drags the median toward a number
    // that is still moving, and the whole point of the median is to be the
    // stable thing a run is called slow against.
    const withLive = medianRunDuration([
      run({ duration_ms: 100 }),
      run({ duration_ms: 100 }),
      run({ duration_ms: 1, status: "running" }),
    ])
    expect(withLive).toBe(100)
  })

  it("excludes a failed run — dying at step one is fast for the wrong reason", () => {
    expect(
      medianRunDuration([
        run({ duration_ms: 100 }),
        run({ duration_ms: 100 }),
        run({ duration_ms: 2, status: "failed" }),
      ]),
    ).toBe(100)
  })

  it("is undefined when nothing finished — there is nothing to be slow against", () => {
    expect(medianRunDuration([run({ status: "running", duration_ms: 5 })])).toBeUndefined()
    expect(medianRunDuration([])).toBeUndefined()
  })
})
