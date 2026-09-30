import { describe, it, expect } from "vitest"

import {
  computeNextRuns, contentsFromPreview, formatPhases, formatPlannedRun, formatSize, parseScope, proofLabel, requiredWhy,
  resolveContents, resolveSelection, retentionDiff, runPlanLabel, runResult, scopeQuery, stripCells, verdictHeadline, writeScope,
  type CategoryKey, type Schedule,
} from "../backups-model"
import { FIXTURE_WORKSPACES } from "../__fixtures__/backups"

const drop = (...k: CategoryKey[]) => new Set<CategoryKey>(k)
const byKey = (rows: ReturnType<typeof resolveContents>) => Object.fromEntries(rows.map((r) => [r.key, r.state]))

describe("resolveContents: Included / Required dependency / Excluded", () => {
  it("a preset includes every category; environments follow the environment mode", () => {
    expect(Object.values(byKey(resolveContents("complete", "complete", drop())))).toEqual(Array(10).fill("included"))
    const files = byKey(resolveContents("workspace", "files", drop("memory")))
    expect(files.env).toBe("excluded")
    expect(files.memory).toBe("included") // dropping only counts for a custom plan
  })

  it("a custom plan keeps what an included category needs, and says who needs it", () => {
    const rows = resolveContents("custom", "files", drop("agents", "chats", "att", "routines", "journal", "creds", "pages", "files"))
    const agents = rows.find((r) => r.key === "agents")!
    expect(agents.state).toBe("required")
    expect(agents.neededBy).toEqual(["memory"])
    expect(requiredWhy(agents)).toBe("kept because memory and its version history need it")
    expect(byKey(rows).journal).toBe("excluded")
  })

  it("lists every category that needs a dependency", () => {
    const rows = resolveContents("custom", "files", drop("agents"))
    expect(rows.find((r) => r.key === "agents")!.neededBy.sort()).toEqual(["creds", "memory", "routines"])
  })

  it("complete environments pull crew working files back in", () => {
    const rows = resolveContents("custom", "complete", drop("files"))
    expect(byKey(rows).files).toBe("required")
    expect(requiredWhy(rows.find((r) => r.key === "files")!)).toBe("kept because complete container environments need it")
  })

  it("warns when chats go in without their attachment files", () => {
    const att = resolveContents("custom", "files", drop("att")).find((r) => r.key === "att")!
    expect(att.state).toBe("excluded")
    expect(att.warning).toBe("chats will open with missing files")
    expect(resolveContents("custom", "files", drop("att", "chats")).find((r) => r.key === "att")!.warning).toBeUndefined()
  })

  it("folds the server's preview into the same rows", () => {
    const rows = contentsFromPreview({ included: ["memory"], required: [{ key: "agents", because: ["memory"] }], excluded: [] })
    expect(byKey(rows)).toMatchObject({ memory: "included", agents: "required", chats: "excluded" })
  })
})

describe("stripCells: the last 14 nights", () => {
  it("always draws fourteen cells ending today, a missing night as no run", () => {
    const cells = stripCells([{ date: "2026-09-30", status: "ok", proof: 3 }, { date: "2026-09-20", status: "failed", proof: 0, detail: "failed: lock held" }], "2026-09-30")
    expect(cells).toHaveLength(14)
    expect(cells[0].date).toBe("2026-09-17")
    expect(cells[13]).toMatchObject({ date: "2026-09-30", status: "ok", mark: "◆", day: 30 })
    expect(cells.find((c) => c.date === "2026-09-20")!.title).toBe("20 Sep · failed: lock held")
    expect(cells[1].status).toBe("none")
  })
  it("marks contents checked with ✓ and a checksum with nothing", () => {
    const cells = stripCells([{ date: "2026-09-29", status: "ok", proof: 2 }, { date: "2026-09-28", status: "ok", proof: 1 }], "2026-09-30")
    expect(cells[12].mark).toBe("✓")
    expect(cells[11].mark).toBe("")
  })
  it("shows a created but incomplete night as incomplete, never as a green ok", () => {
    const cells = stripCells([
      { date: "2026-09-30", status: "incomplete", proof: 2, detail: null },
      { date: "2026-09-29", status: "incomplete", proof: 0, detail: "created · incomplete: 12 attachment files missing" },
    ], "2026-09-30")
    expect(cells[13]).toMatchObject({ status: "incomplete", mark: "✓", title: "30 Sep · created · incomplete" })
    expect(cells[12].title).toBe("29 Sep · created · incomplete: 12 attachment files missing")
  })
  it("crosses a month boundary", () => {
    expect(stripCells([], "2026-10-03")[0].date).toBe("2026-09-20")
  })
})

const sched = (s: Partial<Schedule>): Schedule => ({ cadence: "daily", time: "03:00", weekday: 0, monthday: null, cron: null, envMode: "complete", envCadence: "weekly", ...s })
// Wednesday 30 September 2026, 10:00 wall clock.
const FROM = new Date(Date.UTC(2026, 8, 30, 10, 0))

describe("computeNextRuns and formatPlannedRun", () => {
  it("daily with weekly environments: Sunday takes them", () => {
    const runs = computeNextRuns(sched({}), FROM, 5)!
    expect(runs.map((r) => formatPlannedRun(r))).toEqual([
      "Thu 1 Oct 03:00", "Fri 2 Oct 03:00", "Sat 3 Oct 03:00", "Sun 4 Oct 03:00 + environments 04:00", "Mon 5 Oct 03:00",
    ])
  })
  it("today's time still ahead counts", () => {
    expect(computeNextRuns(sched({ time: "22:30", envMode: "files" }), FROM, 1)![0]).toEqual({ date: "2026-09-30", time: "22:30", environments: false })
  })
  it("weekly on Sunday", () => {
    expect(computeNextRuns(sched({ cadence: "weekly", envMode: "files" }), FROM, 3)!.map((r) => r.date)).toEqual(["2026-10-04", "2026-10-11", "2026-10-18"])
  })
  it("monthly without a day is the first Sunday", () => {
    expect(computeNextRuns(sched({ cadence: "monthly", envMode: "files" }), FROM, 3)!.map((r) => r.date)).toEqual(["2026-10-04", "2026-11-01", "2026-12-06"])
  })
  it("monthly on a day of the month", () => {
    expect(computeNextRuns(sched({ cadence: "monthly", monthday: 15, envMode: "files" }), FROM, 2)!.map((r) => r.date)).toEqual(["2026-10-15", "2026-11-15"])
  })
  it("custom every six hours from a cron", () => {
    const runs = computeNextRuns(sched({ cadence: "custom", cron: "0 */6 * * *" }), FROM, 4)!
    expect(runs.map((r) => formatPlannedRun(r))).toEqual(["Wed 30 Sep 12:00", "Wed 30 Sep 18:00", "Thu 1 Oct 00:00", "Thu 1 Oct 06:00"])
  })
  it("leaves a cron with day fields to the server", () => {
    expect(computeNextRuns(sched({ cadence: "custom", cron: "0 3 1 * *" }), FROM)).toBeNull()
    expect(computeNextRuns(sched({ cadence: "custom", cron: "nonsense" }), FROM)).toBeNull()
  })
})

describe("scope in the URL", () => {
  const all = FIXTURE_WORKSPACES
  it("defaults to the whole instance and every workspace", () => {
    expect(parseScope("")).toEqual({ scope: "instance", ws: null })
    expect(resolveSelection(parseScope(""), all).size).toBe(5)
  })
  it("reads workspaces by slug or id; unknown ones drop out; none is none", () => {
    const st = parseScope("?tab=backups&scope=workspaces&ws=dess,ws-coolify,gone")
    expect(st.scope).toBe("workspaces")
    expect([...resolveSelection(st, all)].sort()).toEqual(["ws-coolify", "ws-dess"])
    expect(resolveSelection(parseScope("?ws=none"), all).size).toBe(0)
  })
  it("writes slugs, drops ws when every workspace is ticked, keeps other params", () => {
    expect(writeScope("?tab=backups&section=history", "workspaces", new Set(["ws-dess", "ws-unify"]), all)).toBe("?tab=backups&section=history&scope=workspaces&ws=dess%2Cunify-lab")
    expect(writeScope("?tab=backups&ws=dess", "instance", new Set(all.map((w) => w.id)), all)).toBe("?tab=backups&scope=instance")
    expect(writeScope("", "workspaces", new Set(), all)).toBe("?scope=workspaces&ws=none")
  })
  it("round-trips", () => {
    const sel = new Set(["ws-pages", "ws-sandbox"])
    expect(resolveSelection(parseScope(writeScope("", "workspaces", sel, all)), all)).toEqual(sel)
  })
  it("asks the API for ids only when not everything is ticked", () => {
    expect(scopeQuery("instance", new Set(["ws-dess"]), all)).toBe("scope=instance")
    expect(scopeQuery("workspaces", new Set(["ws-dess"]), all)).toBe("scope=workspaces&ws=ws-dess")
    expect(scopeQuery("workspaces", new Set(all.map((w) => w.id)), all)).toBe("scope=workspaces")
  })
})

describe("formatting", () => {
  it("never phrases a partial test restore up", () => {
    expect(verdictHeadline("partial")).toEqual({ text: "Partial restore verified", tone: "warn" })
    expect(verdictHeadline("contents_checked").text).toBe("Latest backup: contents checked, not test-restored yet")
    expect(proofLabel(3, "partial")).toBe("test restore · partial")
    expect(proofLabel(3, "ok")).toBe("test restore")
    expect(proofLabel(0)).toBe("—")
  })
  it("words a run's result and plan the way the table shows them", () => {
    expect(runResult({ status: "incomplete" }).text).toBe("done · incomplete")
    expect(runResult({ status: "interrupted", retried_at: "x" }).text).toBe("interrupted · retried")
    expect(runPlanLabel({ plan_name: null, trigger: "manual", note: "before upgrade", kind: "full" })).toBe("manual · before upgrade")
    expect(runPlanLabel({ plan_name: "Complete recovery", trigger: "schedule", note: null, kind: "environments" })).toBe("Complete recovery · environments")
  })
  it("sizes in decimal units", () => {
    expect(formatSize(3.9e9)).toBe("3.9 GB")
    expect(formatSize(38e6)).toBe("38 MB")
    expect(formatSize(212e9)).toBe("212 GB")
    expect(formatSize(null)).toBe("—")
  })
  it("phases with durations, a skipped one as a dash", () => {
    expect(formatPhases([
      { name: "copy", status: "done", started_at: "2026-09-30T03:00:00Z", ended_at: "2026-09-30T03:06:40Z" },
      { name: "pack", status: "done", started_at: "2026-09-30T03:06:40Z", ended_at: "2026-09-30T03:17:40Z" },
      { name: "encrypt", status: "done" },
      { name: "off-site", status: "skipped" },
    ])).toBe("copy 6m40s · pack 11m · encrypt · off-site —")
  })
  it("retention: only real edits count, and a mixed value always counts", () => {
    const rows = [{ key: "a", label: "A", days: 30 }, { key: "b", label: "B", days: null, mixed: true }, { key: "h", label: "H", days: 7, housekeeping: true }]
    expect(retentionDiff(rows, { a: 30 })).toEqual([])
    expect(retentionDiff(rows, { a: 90, b: null, h: 30 })).toEqual([{ key: "a", from: 30, to: 90, mixed: false }, { key: "b", from: null, to: null, mixed: true }])
  })
})
