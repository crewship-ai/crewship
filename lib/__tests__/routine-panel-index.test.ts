import { describe, it, expect } from "vitest"

import {
  buildRoutinePanelIndex,
  groupRoutines,
  primaryTrigger,
  sortRoutines,
  type RoutinePanelIndex,
} from "@/lib/routine-panel-index"

// The Routines panel composes a routine's triggers, its next start and its
// spend in the browser from lists the page already has (routines.md R01):
// GET /pipeline-schedules, GET /pipeline-webhooks, GET /pipelines/budget-summary,
// the slash-command catalog (`routine.run:<slug>`) and `linked_issue_count`.

const routines = [
  { id: "p1", slug: "weekly", name: "Weekly report", linked_issue_count: 0, last_invoked_at: "2026-10-16T08:00:00Z" },
  { id: "p2", slug: "triage", name: "Backlog triage", linked_issue_count: 2, last_invoked_at: "2026-10-17T02:00:00Z" },
  { id: "p3", slug: "pr", name: "Review pull requests", linked_issue_count: 0, last_invoked_at: "2026-10-15T10:00:00Z" },
  { id: "p4", slug: "manual", name: "Ad hoc export", linked_issue_count: 0 },
]

const index = buildRoutinePanelIndex({
  routines,
  schedules: [
    { target_pipeline_slug: "weekly", target_pipeline_id: "p1", enabled: true, next_run_at: "2026-10-22T08:00:00Z" },
    { target_pipeline_slug: "weekly", target_pipeline_id: "p1", enabled: true, next_run_at: "2026-10-19T08:00:00Z" },
    // Resolved through the id when the server left the slug out.
    { target_pipeline_id: "p2", enabled: true, next_run_at: "2026-10-18T02:00:00Z" },
    // A schedule that is off still says how the routine starts, but has no next start.
    { target_pipeline_slug: "pr", enabled: false, next_run_at: "2026-10-18T00:00:00Z" },
  ],
  webhooks: [{ target_pipeline_slug: "pr", target_pipeline_id: "p3" }],
  chatCommandIds: ["routine.run:weekly", "issue.create", "routine.run:gone"],
  spend: { routines: [{ slug: "pr", spent_usd: 18.9 }, { slug: "weekly", spent_usd: 6.2 }] },
})

describe("buildRoutinePanelIndex", () => {
  it.each([
    ["weekly", ["schedule", "chat"]],
    ["triage", ["schedule", "issue"]],
    ["pr", ["schedule", "webhook"]],
    ["manual", []],
  ])("%s starts by %j", (slug, kinds) => {
    expect(index.triggers.get(slug) ?? []).toEqual(kinds)
  })

  it("takes the earliest next start of the enabled schedules only", () => {
    expect(index.nextRun.get("weekly")).toBe("2026-10-19T08:00:00Z")
    expect(index.nextRun.get("triage")).toBe("2026-10-18T02:00:00Z")
    expect(index.nextRun.has("pr")).toBe(false)
    expect(index.nextRun.has("manual")).toBe(false)
  })

  it("reads this month's spend from the budget summary", () => {
    expect(index.spend.get("pr")).toBe(18.9)
    expect(index.spend.get("weekly")).toBe(6.2)
    expect(index.spend.has("manual")).toBe(false)
  })

  it("works with nothing loaded yet", () => {
    const empty = buildRoutinePanelIndex({ routines })
    expect(empty.triggers.get("triage")).toEqual(["issue"])
    expect(empty.nextRun.size).toBe(0)
    expect(empty.spend.size).toBe(0)
  })
})

describe("primaryTrigger", () => {
  it.each([
    [["schedule", "chat"], "schedule"],
    [["webhook", "issue"], "webhook"],
    [["chat"], "chat"],
    [[], "manual"],
  ] as const)("%j → %s", (kinds, kind) => expect(primaryTrigger(kinds)).toBe(kind))
})

describe("sortRoutines", () => {
  const names = (rows: { name: string }[]) => rows.map((r) => r.name)
  it("Next run: soonest first, routines without a next start last by name", () => {
    expect(names(sortRoutines(routines, "next", index))).toEqual(["Backlog triage", "Weekly report", "Ad hoc export", "Review pull requests"])
  })
  it("Spend: most spent first, nothing spent last by name", () => {
    expect(names(sortRoutines(routines, "spend", index))).toEqual(["Review pull requests", "Weekly report", "Ad hoc export", "Backlog triage"])
  })
  it("Last run: newest run first, never run last", () => {
    expect(names(sortRoutines(routines, "recent", index))).toEqual(["Backlog triage", "Weekly report", "Review pull requests", "Ad hoc export"])
  })
  it("Name", () => {
    expect(names(sortRoutines(routines, "name", index))).toEqual(["Ad hoc export", "Backlog triage", "Review pull requests", "Weekly report"])
  })
  it("does not change the list it was given", () => {
    const copy = [...routines]
    sortRoutines(routines, "name", index)
    expect(routines).toEqual(copy)
  })
})

describe("groupRoutines by trigger", () => {
  it("puts each routine under the first way it starts, in a fixed order, with Run only last", () => {
    const groups = groupRoutines(routines, "trigger", index as RoutinePanelIndex, new Map())
    expect(groups.map((g) => [g.label, g.rows.map((r) => r.slug)])).toEqual([
      ["Schedule", ["weekly", "triage", "pr"]],
      ["Only when someone presses Run", ["manual"]],
    ])
  })
  it("by crew keeps No crew last; none is one group", () => {
    const crews = new Map([["c1", { id: "c1", name: "Research" }]])
    const rows = [
      { ...routines[0], author_crew_id: "c1" },
      { ...routines[3], author_crew_id: undefined },
    ]
    expect(groupRoutines(rows, "crew", index, crews).map((g) => g.label)).toEqual(["Research", "No crew"])
    expect(groupRoutines(rows, "none", index, crews).map((g) => g.label)).toEqual(["Routines"])
  })
})

describe("groupRoutines by folder (ODCH-RT-01)", () => {
  const rows = [
    { id: "a", slug: "weekly", name: "Weekly", folder_id: "f_intel" },
    { id: "b", slug: "news", name: "News", folder_id: "f_intel" },
    { id: "c", slug: "pr", name: "PR", folder_id: "f_eng" },
    { id: "d", slug: "loose", name: "Loose" },
    { id: "e", slug: "stale", name: "Stale", folder_id: "f_gone" },
  ]
  const folders = new Map([
    ["f_intel", { id: "f_intel", name: "Market intel" }],
    ["f_eng", { id: "f_eng", name: "Engineering" }],
    ["f_empty", { id: "f_empty", name: "Archive" }],
  ])
  const index = { triggers: new Map(), nextRun: new Map(), spend: new Map() } as unknown as RoutinePanelIndex

  it("groups by folder, alphabetical, with the unfiled last (a deleted folder counts as unfiled)", () => {
    const groups = groupRoutines(rows, "folder", index, new Map(), { folders })
    expect(groups.map((g) => [g.label, g.rows.map((r) => r.slug)])).toEqual([
      ["Engineering", ["pr"]],
      ["Market intel", ["weekly", "news"]],
      ["No folder", ["loose", "stale"]],
    ])
  })

  it("keeps an empty folder when asked, so a new folder does not vanish", () => {
    const groups = groupRoutines(rows, "folder", index, new Map(), { folders, includeEmptyFolders: true })
    expect(groups.map((g) => g.label)).toEqual(["Archive", "Engineering", "Market intel", "No folder"])
    expect(groups[0].rows).toEqual([])
  })

  it("has no No-folder section when everything is filed", () => {
    const groups = groupRoutines(rows.slice(0, 3), "folder", index, new Map(), { folders })
    expect(groups.map((g) => g.label)).toEqual(["Engineering", "Market intel"])
  })
})
