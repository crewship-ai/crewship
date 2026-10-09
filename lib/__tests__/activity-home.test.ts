import { describe, expect, it } from "vitest"

import {
  activeProblems,
  scopeRuns,
  headlineParts,
  issueEffects,
  outcomesByDay,
  runLanes,
  runLine,
  upNext,
  type HomeRun,
} from "../activity-home"

const NOW = Date.parse("2026-10-08T12:00:00Z")
const at = (hoursAgo: number) => new Date(NOW - hoursAgo * 3_600_000).toISOString()
const run = (over: Partial<HomeRun> = {}): HomeRun => ({
  id: "r",
  pipeline_slug: "check",
  pipeline_name: "Check deliveries",
  status: "completed",
  started_at: at(1),
  duration_ms: 2000,
  error_message: "",
  failed_at_step: "",
  current_step_id: "",
  ...over,
})

describe("headlineParts", () => {
  it("reads as one sentence and only names what is there", () => {
    expect(headlineParts({ runs: 14, running: 2, waiting: 1, failed: 3, cost: 0.42 }).map((p) => p.text)).toEqual([
      "14 runs",
      "2 running",
      "1 needs you",
      "3 could not finish",
      "$0.42",
    ])
    expect(headlineParts({ runs: 1, running: 0, waiting: 0, failed: 0, cost: null }).map((p) => p.text)).toEqual([
      "1 run",
      "nothing needs you",
    ])
  })

  it("colours the parts a reader acts on", () => {
    const parts = headlineParts({ runs: 3, running: 0, waiting: 1, failed: 1, cost: 0 })
    expect(parts.find((p) => p.text === "1 needs you")?.tone).toBe("warn")
    expect(parts.find((p) => p.text === "1 could not finish")?.tone).toBe("destructive")
  })
})

describe("runLanes", () => {
  const window = { from: NOW - 24 * 3_600_000, to: NOW }

  it("draws one lane per routine with every run as a bar placed in the window", () => {
    const lanes = runLanes(
      [
        run({ id: "a", started_at: at(12) }),
        run({ id: "b", started_at: at(6), status: "failed" }),
        run({ id: "c", pipeline_slug: "telemetry", pipeline_name: "Refresh telemetry", started_at: at(23) }),
      ],
      window,
    )
    expect(lanes.lanes.map((l) => l.name)).toEqual(["Check deliveries", "Refresh telemetry"])
    const check = lanes.lanes[0]
    expect(check.bars.map((b) => [b.id, b.tone])).toEqual([
      ["a", "done"],
      ["b", "failed"],
    ])
    expect(check.bars[0].left).toBeCloseTo(50, 0)
    expect(check.summary).toEqual({ runs: 2, failed: 1, active: 0 })
  })

  it("puts live and failing lanes first and folds the quiet tail", () => {
    const runs = [
      ...Array.from({ length: 5 }, (_, i) => run({ id: `q${i}`, pipeline_slug: "quiet", pipeline_name: "Quiet", started_at: at(i + 1) })),
      run({ id: "f", pipeline_slug: "broken", pipeline_name: "Broken", status: "failed", started_at: at(3) }),
      run({ id: "w", pipeline_slug: "asks", pipeline_name: "Asks", status: "waiting", started_at: at(2) }),
      run({ id: "x", pipeline_slug: "x", pipeline_name: "X", started_at: at(2) }),
    ]
    const lanes = runLanes(runs, window, 3)
    expect(lanes.lanes.map((l) => l.name)).toEqual(["Asks", "Broken", "Quiet"])
    expect(lanes.hidden).toBe(1)
  })

  it("keeps a bar visible however short the run, and inside the window", () => {
    const lanes = runLanes([run({ started_at: at(0.01), duration_ms: 10 })], window)
    const bar = lanes.lanes[0].bars[0]
    expect(bar.width).toBeGreaterThanOrEqual(0.6)
    expect(bar.left + bar.width).toBeLessThanOrEqual(100)
  })
})

describe("outcomesByDay", () => {
  it("counts each of the last N days by outcome, oldest first, today last", () => {
    const days = outcomesByDay(
      [run({ started_at: at(1) }), run({ started_at: at(2), status: "failed" }), run({ started_at: at(30) })],
      7,
      NOW,
    )
    expect(days).toHaveLength(7)
    expect(days.at(-1)).toMatchObject({ done: 1, failed: 1, other: 0, today: true })
    expect(days.at(-2)).toMatchObject({ done: 1, failed: 0 })
  })
})

describe("upNext", () => {
  it("lists the next planned runs after now, soonest first", () => {
    const next = upNext(
      [
        { kind: "planned", at: "2026-10-08T18:00:00Z", slug: "digest", name: "Workspace digest" },
        { kind: "planned", at: "2026-10-08T12:12:00Z", slug: "telemetry", name: "Refresh telemetry" },
        { kind: "planned", at: "2026-10-08T11:00:00Z", slug: "old", name: "Past" },
        { kind: "run", at: "2026-10-08T12:30:00Z", slug: "x", name: "Not planned" },
      ],
      NOW,
      2,
    )
    expect(next.map((e) => e.name)).toEqual(["Refresh telemetry", "Workspace digest"])
  })
})

describe("runLine", () => {
  it("says in one line what a run came to", () => {
    expect(runLine(run({ status: "failed", error_message: "HTTP 502 Bad Gateway\nstack…", failed_at_step: "post" }))).toBe(
      "HTTP 502 Bad Gateway",
    )
    expect(runLine(run({ status: "failed", failed_at_step: "post" }))).toBe("Stopped at step “post”")
    expect(runLine(run({ status: "waiting" }))).toBe("Waiting for a decision")
    expect(runLine(run({ status: "running", current_step_id: "query" }))).toBe("Running · step “query”")
    expect(runLine(run({ status: "completed", duration_ms: 4700 }))).toBe("Finished in 4.7s")
    // #2981: the two stops say which they were.
    expect(runLine(run({ status: "cancelled" }))).toBe("Cancelled before it finished")
    expect(runLine(run({ status: "interrupted" }))).toBe("Interrupted — the process running it stopped")
  })
})

describe("issueEffects", () => {
  it("separates issues the window's runs created from ones they touched, once each", () => {
    const fx = issueEffects([
      { issues: [{ id: "i1", identifier: "OPS-1", title: "A", created: true }, { id: "i2", identifier: "OPS-2", title: "B" }] },
      { issues: [{ id: "i2", identifier: "OPS-2", title: "B" }] },
      {},
    ])
    expect(fx.created.map((i) => i.identifier)).toEqual(["OPS-1"])
    expect(fx.touched.map((i) => i.identifier)).toEqual(["OPS-2"])
  })
})

describe("activeProblems", () => {
  it("keeps only causes that failed inside the window and counts them there", () => {
    const groups = [
      { fingerprint: "a", count: 40, pipeline_slug: "repair", failed_at_step: "post", sample_error: "HTTP 502", run_ids: ["r1", "r2", "old"] },
      { fingerprint: "b", count: 3, pipeline_slug: "gone", failed_at_step: "x", sample_error: "boom", run_ids: ["old2"] },
    ]
    const runs = [
      run({ id: "r1", pipeline_slug: "repair", pipeline_name: "Repair demo delivery", status: "failed" }),
      run({ id: "r2", pipeline_slug: "repair", pipeline_name: "Repair demo delivery", status: "failed" }),
    ]
    expect(activeProblems(groups, runs)).toEqual([
      { fingerprint: "a", name: "Repair demo delivery", step: "post", error: "HTTP 502", recent: 2, total: 40, latestRunId: "r1" },
    ])
  })
})

describe("scopeRuns (#3002)", () => {
  it("keeps the runs of the rail's chains — a root by its id, the rest by chain_origin", () => {
    const runs = [
      run({ id: "root_ops" }),
      run({ id: "child_ops", chain_origin: "root_ops" }),
      run({ id: "root_fin" }),
    ]
    expect(scopeRuns(runs, new Set(["root_ops"])).map((r) => r.id)).toEqual(["root_ops", "child_ops"])
    expect(scopeRuns(runs, null)).toHaveLength(3)
  })
})
