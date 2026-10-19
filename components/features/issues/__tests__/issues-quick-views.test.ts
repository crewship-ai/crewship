import { describe, it, expect } from "vitest"

import {
  QUICK_VIEWS,
  dueLabel,
  groupIssues,
  orderIssues,
  quickViewQuery,
  shortAgo,
} from "../issues-view-model"
import type { Mission } from "@/lib/types/mission"

const m = (over: Partial<Mission>): Mission =>
  ({ id: over.id ?? "x", title: over.id ?? "x", status: "TODO", created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-01T00:00:00Z", ...over }) as Mission

describe("quick views", () => {
  it("offers v67's six views in v67's order", () => {
    expect(QUICK_VIEWS.map((v) => v.label)).toEqual([
      "My work",
      "Needs my reply",
      "Agents working",
      "Waiting for my review",
      "Blocked",
      "All issues",
    ])
  })

  it("each view is a server filter, never a filter over the loaded page", () => {
    expect(quickViewQuery("all")).toBe("")
    expect(quickViewQuery("mine")).toBe("mine=1")
    expect(quickViewQuery("reply")).toBe("needs=reply")
    expect(quickViewQuery("working")).toBe("working=1")
    expect(quickViewQuery("review")).toBe("needs=review")
    expect(quickViewQuery("blocked")).toBe("blocked=1")
  })
})

describe("groupIssues", () => {
  const issues = [
    m({ id: "a", status: "BACKLOG", priority: "low", project_id: "p2", project_name: "Infra" }),
    m({ id: "b", status: "IN_PROGRESS", priority: "urgent", project_id: "p1", project_name: "Launch", delegate: { id: "ag", name: "Scout" } }),
    m({ id: "c", status: "COMPLETED", priority: "low" }),
    m({ id: "d", status: "REVIEW" }),
  ]

  it("by status: the work in motion first; the retired COMPLETED is Done", () => {
    expect(groupIssues(issues, "status").map((g) => [g.label, g.issues.map((i) => i.id)])).toEqual([
      ["In progress", ["b"]],
      ["In review", ["d"]],
      ["Backlog", ["a"]],
      ["Done", ["c"]],
    ])
  })

  it("by project and by delegate: named groups alphabetically, the empty one last", () => {
    expect(groupIssues(issues, "project").map((g) => g.label)).toEqual(["Infra", "Launch", "No project"])
    expect(groupIssues(issues, "delegate").map((g) => g.label)).toEqual(["Scout", "No delegate"])
  })

  it("by priority leaves empty priorities out; none is one group", () => {
    expect(groupIssues(issues, "priority").map((g) => g.label)).toEqual(["Urgent", "Low", "No priority"])
    expect(groupIssues(issues, "none")).toHaveLength(1)
    expect(groupIssues([], "none")).toEqual([])
  })
})

describe("orderIssues", () => {
  const issues = [
    m({ id: "old", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-10-05T00:00:00Z", priority: "low", due_date: "2026-11-01" }),
    m({ id: "new", created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-02T00:00:00Z", priority: "urgent" }),
    m({ id: "mid", created_at: "2026-05-01T00:00:00Z", updated_at: "2026-10-01T00:00:00Z", priority: "high", due_date: "2026-10-20" }),
  ]
  it.each([
    ["created", ["new", "mid", "old"]],
    ["updated", ["old", "mid", "new"]],
    ["priority", ["new", "mid", "old"]],
    ["due", ["mid", "old", "new"]],
  ] as const)("%s", (order, want) => {
    expect(orderIssues(issues, order).map((i) => i.id)).toEqual(want)
  })
})

describe("row bits", () => {
  it("shortAgo reads like the list column", () => {
    const now = Date.parse("2026-10-17T12:00:00Z")
    expect(shortAgo("2026-10-17T11:59:30Z", now)).toBe("now")
    expect(shortAgo("2026-10-17T11:15:00Z", now)).toBe("45m")
    expect(shortAgo("2026-10-17T09:00:00Z", now)).toBe("3h")
    expect(shortAgo("2026-10-14T12:00:00Z", now)).toBe("3d")
    expect(shortAgo(null, now)).toBe("")
  })

  it("dueLabel: today and overdue are late only while the issue is open", () => {
    const now = new Date(2026, 9, 17, 12)
    expect(dueLabel(new Date(2026, 9, 17, 9).toISOString(), "TODO", now)).toEqual({ text: "Today", late: true })
    expect(dueLabel(new Date(2026, 9, 18, 9).toISOString(), "TODO", now)).toEqual({ text: "Tomorrow", late: false })
    expect(dueLabel(new Date(2026, 9, 10, 9).toISOString(), "TODO", now)).toEqual({ text: "Overdue", late: true })
    expect(dueLabel(new Date(2026, 9, 10, 9).toISOString(), "DONE", now)?.late).toBe(false)
    expect(dueLabel(null, "TODO", now)).toBeNull()
  })
})
