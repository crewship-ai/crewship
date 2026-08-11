import { describe, expect, it } from "vitest"

import {
  groupRunsByHour,
  isFailedRunStatus,
  medianRunDuration,
  runHeadline,
  slowestRunDuration,
  type DigestRun,
} from "@/lib/run-digest"

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

/**
 * The same helper pinned to a calendar day, for the daily-cron case.
 *
 * Built from local components rather than parsed from a UTC string so the run
 * lands on that local DAY on every runner — a "09:00Z" stamp is the previous
 * day west of Greenwich, which is the very confusion these tests are about.
 */
const atLocalOn = (year: number, month: number, day: number, h: number, m = 0) =>
  new Date(year, month - 1, day, h, m, 0, 0).toISOString()

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

  it("collapses a YAML document that opens on '---' — a row reading '---' says nothing", () => {
    // The lead is three characters, which the old length rule let through: the
    // row read "---" while the answer sat on line two. Length was never the
    // question; whether the line is punctuation or a sentence is.
    expect(runHeadline(run({ output: "---\ntitle: 3 tickets classified\n---" })).text).toBe(
      "--- title: 3 tickets classified ---",
    )
  })

  it("collapses a markdown list whose first line is the bullet alone", () => {
    expect(runHeadline(run({ output: "-\nENG-7 reopened" })).text).toBe("- ENG-7 reopened")
  })

  it("keeps a short first line that is a word — 'ok' is an answer, '{' is not", () => {
    // Two characters, and the old rule collapsed it: a routine reporting "ok"
    // and then its detail lines got every line of the detail dragged onto the
    // row it was deliberately kept off.
    expect(runHeadline(run({ output: "ok\nchecked 4 queues\nnothing to do" })).text).toBe("ok")
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

  it("gives a daily 09:00 cron one identity per day, not one label three times", () => {
    // The bug this pins: the buckets were always right — keyed on the hour
    // INSTANT — but their labels were bare "09:00", so a page keying its rows
    // on the label handed React three identical keys and a reader three
    // identical headers for three different mornings.
    const buckets = groupRunsByHour([
      run({ id: "mon", started_at: atLocalOn(2026, 8, 10, 9, 0) }),
      run({ id: "sun", started_at: atLocalOn(2026, 8, 9, 9, 0) }),
      run({ id: "sat", started_at: atLocalOn(2026, 8, 8, 9, 0) }),
    ])
    expect(buckets).toHaveLength(3)
    expect(new Set(buckets.map((b) => b.key)).size).toBe(3)
  })

  it("names the day on each header once the page covers more than one", () => {
    const buckets = groupRunsByHour([
      run({ started_at: atLocalOn(2026, 8, 10, 9, 0) }),
      run({ started_at: atLocalOn(2026, 8, 9, 9, 0) }),
    ])
    // Every bucket carries one, and no two read the same — that is the whole
    // job. The exact rendering is the reader's locale's business, so asserting
    // its text would be asserting Intl's output rather than this module's rule.
    expect(buckets.every((b) => (b.day ?? "").length > 0)).toBe(true)
    expect(new Set(buckets.map((b) => b.day)).size).toBe(2)
  })

  it("leaves the day off when every run happened on the same one", () => {
    // A per-minute routine's thirty rows all share today's date; stamping it on
    // every header is thirty repetitions of a fact the reader had before they
    // arrived, and it crowds out the times that actually differ.
    const buckets = groupRunsByHour([
      run({ started_at: atLocalOn(2026, 8, 10, 14, 51) }),
      run({ started_at: atLocalOn(2026, 8, 10, 13, 2) }),
    ])
    expect(buckets.map((b) => b.day)).toEqual([undefined, undefined])
  })

  it("counts the undated bucket as its own day, and still keys it", () => {
    // Two hours of one day plus an unreadable stamp is ONE day of runs: the
    // undated bucket is not evidence of a second, and letting it stamp a date
    // on the other headers would be inventing one.
    const buckets = groupRunsByHour([
      run({ id: "bad", started_at: "not a date" }),
      run({ id: "ok", started_at: atLocalOn(2026, 8, 10, 9, 0) }),
    ])
    expect(buckets[1].key).toBe("undated")
    expect(new Set(buckets.map((b) => b.key)).size).toBe(2)
    expect(buckets.map((b) => b.day)).toEqual([undefined, undefined])
  })
})

describe("isFailedRunStatus", () => {
  // The page and the hour header both count failures, and until this predicate
  // was exported they counted different ones: a cancelled run showed in
  // "2 runs · 1 failed" and not in the strip's "Failed 0" directly beneath it.
  it("folds cancelled and interrupted in — the reader is asking what did not deliver", () => {
    expect(isFailedRunStatus("failed")).toBe(true)
    expect(isFailedRunStatus("cancelled")).toBe(true)
    expect(isFailedRunStatus("interrupted")).toBe(true)
  })

  it("leaves a finished or in-flight run alone", () => {
    for (const s of ["completed", "dry_run", "running", "queued", "waiting"]) {
      expect(isFailedRunStatus(s), s).toBe(false)
    }
  })
})

describe("slowestRunDuration", () => {
  it("is the longest run that finished", () => {
    expect(slowestRunDuration([run({ duration_ms: 100 }), run({ duration_ms: 900 })])).toBe(900)
  })

  it("ignores an in-flight run — a partial duration is not a record", () => {
    // Same argument medianRunDuration makes: duration_ms on a live run is
    // rewritten at every step boundary. A routine that normally takes 200ms
    // showing "Slowest 40s" because one run is still going is a page reporting
    // a number that will be smaller when it is reloaded.
    expect(
      slowestRunDuration([
        run({ duration_ms: 200 }),
        run({ duration_ms: 40_000, status: "running" }),
      ]),
    ).toBe(200)
  })

  it("keeps a failed run — a 30s timeout IS the slowest thing that happened", () => {
    // The median excludes failures because a run that dies at step one is fast
    // for a reason that says nothing about the work. That reason cannot reach a
    // MAXIMUM: a fast run never is one. What can reach it is the timeout, and
    // hiding it is hiding the slowest thing the routine did.
    expect(
      slowestRunDuration([run({ duration_ms: 200 }), run({ duration_ms: 30_000, status: "failed" })]),
    ).toBe(30_000)
  })

  it("is undefined when nothing has a duration yet", () => {
    expect(slowestRunDuration([])).toBeUndefined()
    expect(slowestRunDuration([run({ duration_ms: 0, status: "running" })])).toBeUndefined()
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
