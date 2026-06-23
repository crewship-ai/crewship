import { describe, it, expect } from "vitest"
import { renderHook } from "@testing-library/react"
import { useFilteredIssues } from "@/hooks/use-filtered-issues"
import type { Mission, MissionStatus, IssuePriority } from "@/lib/types/mission"

// Coverage companion for use-filtered-issues.test.ts — that file pins the
// project/crew/agent precedence rules; this one drives the status,
// priority, and free-text search branches.

function issue(overrides: Partial<Mission>): Mission {
  return {
    id: "i-default",
    title: "default",
    workspace_id: "ws-1",
    crew_id: "crew-1",
    lead_agent_id: "agent-1",
    trace_id: "trace-1",
    status: "in_progress",
    ...overrides,
  } as Mission
}

const issues = [
  issue({ id: "i1", title: "Fix login flow", identifier: "CRE-101", status: "in_progress" as MissionStatus, priority: "high" as IssuePriority, assignee_name: "Viktor", crew_name: "Engineering" }),
  issue({ id: "i2", title: "Write release notes", identifier: "CRE-102", status: "done" as MissionStatus, priority: "low" as IssuePriority, assignee_name: "Nela", crew_name: "Writing" }),
  issue({ id: "i3", title: "Probe the network", identifier: "CRE-103", status: "backlog" as MissionStatus, priority: undefined, assignee_name: undefined, crew_name: undefined }),
]

function run(overrides: Partial<Parameters<typeof useFilteredIssues>[0]>) {
  const { result } = renderHook(() =>
    useFilteredIssues({
      issues,
      search: "",
      selectedProjectId: null,
      filterProjectId: null,
      filterCrewId: null,
      filterAgentId: null,
      filterStatuses: [],
      filterPriority: null,
      ...overrides,
    }),
  )
  return result.current.map((i) => i.id)
}

describe("useFilteredIssues — status filter", () => {
  it("narrows to the given statuses", () => {
    expect(run({ filterStatuses: ["done"] as MissionStatus[] })).toEqual(["i2"])
  })

  it("multiple statuses OR-compose", () => {
    expect(run({ filterStatuses: ["done", "backlog"] as MissionStatus[] })).toEqual(["i2", "i3"])
  })
})

describe("useFilteredIssues — priority filter", () => {
  it("narrows to the given priority", () => {
    expect(run({ filterPriority: "high" as IssuePriority })).toEqual(["i1"])
  })

  it("treats a missing priority as 'none'", () => {
    expect(run({ filterPriority: "none" as IssuePriority })).toEqual(["i3"])
  })
})

describe("useFilteredIssues — search", () => {
  it("matches title case-insensitively", () => {
    expect(run({ search: "LOGIN" })).toEqual(["i1"])
  })

  it("matches identifier", () => {
    expect(run({ search: "cre-102" })).toEqual(["i2"])
  })

  it("matches assignee name", () => {
    expect(run({ search: "viktor" })).toEqual(["i1"])
  })

  it("matches crew name", () => {
    expect(run({ search: "writing" })).toEqual(["i2"])
  })

  it("returns nothing for a query with no match (and tolerates rows with missing optional fields)", () => {
    expect(run({ search: "zzz-not-there" })).toEqual([])
  })

  it("search composes AND-style with status filter", () => {
    expect(run({ search: "e", filterStatuses: ["in_progress"] as MissionStatus[] })).toEqual(["i1"])
  })
})
