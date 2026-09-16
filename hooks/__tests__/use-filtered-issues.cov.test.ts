import { describe, it, expect } from "vitest"
import { renderHook } from "@testing-library/react"
import { useFilteredIssues } from "@/hooks/use-filtered-issues"
import type { Mission } from "@/lib/types/mission"

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
    status: "IN_PROGRESS",
    ...overrides,
  } as Mission
}

const issues = [
  issue({ id: "i1", title: "Fix login flow", identifier: "CRE-101", status: "IN_PROGRESS", priority: "high", assignee_name: "Viktor", crew_name: "Engineering" }),
  issue({ id: "i2", title: "Write release notes", identifier: "CRE-102", status: "DONE", priority: "low", assignee_name: "Nela", crew_name: "Writing" }),
  issue({ id: "i3", title: "Probe the network", identifier: "CRE-103", status: "BACKLOG", priority: undefined, assignee_name: undefined, crew_name: undefined }),
]

function render(overrides: Partial<Parameters<typeof useFilteredIssues>[0]>) {
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
  return result.current
}

function run(overrides: Partial<Parameters<typeof useFilteredIssues>[0]>) {
  return render(overrides).visible.map((i) => i.id)
}

function facet(overrides: Partial<Parameters<typeof useFilteredIssues>[0]>) {
  return render(overrides).statusFacet.map((i) => i.id)
}

describe("useFilteredIssues — status filter", () => {
  it("narrows to the given statuses", () => {
    expect(run({ filterStatuses: ["DONE"] })).toEqual(["i2"])
  })

  it("multiple statuses OR-compose", () => {
    expect(run({ filterStatuses: ["DONE", "BACKLOG"] })).toEqual(["i2", "i3"])
  })
})

// `statusFacet` is what the status chips count. Deriving the counts from
// `visible` (which has the status filter applied) makes every unselected chip
// read 0 — and a zero-count chip is not rendered, so multi-select is
// unreachable from the UI.
describe("useFilteredIssues — statusFacet", () => {
  it("ignores the status filter", () => {
    expect(facet({ filterStatuses: ["DONE"] })).toEqual(["i1", "i2", "i3"])
  })

  it("is the same list as `visible` when no status is selected", () => {
    const { visible, statusFacet } = render({})
    expect(statusFacet).toEqual(visible)
  })

  it("still applies every other filter", () => {
    expect(
      facet({ search: "probe", filterStatuses: ["DONE"] }),
    ).toEqual(["i3"])
    expect(
      facet({ filterPriority: "high", filterStatuses: ["DONE"] }),
    ).toEqual(["i1"])
  })
})

describe("useFilteredIssues — priority filter", () => {
  it("narrows to the given priority", () => {
    expect(run({ filterPriority: "high" })).toEqual(["i1"])
  })

  it("treats a missing priority as 'none'", () => {
    expect(run({ filterPriority: "none" })).toEqual(["i3"])
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
    expect(run({ search: "e", filterStatuses: ["IN_PROGRESS"] })).toEqual(["i1"])
  })
})
