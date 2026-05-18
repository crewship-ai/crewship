import { describe, it, expect } from "vitest"
import { renderHook } from "@testing-library/react"
import { useFilteredIssues } from "@/hooks/use-filtered-issues"
import type { Mission } from "@/lib/types/mission"

// Minimal issue factory — only fields the hook reads. Cast through
// the partial-shape escape hatch so the test stays lean; the
// production Mission type carries ~30 optional fields the hook
// doesn't touch.
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

describe("useFilteredIssues — issue #320 saved-view vs project-detail separation", () => {
  const issues = [
    issue({ id: "i1", project_id: "p-alpha", crew_id: "c1", assignee_id: "a1" }),
    issue({ id: "i2", project_id: "p-beta", crew_id: "c1", assignee_id: "a1" }),
    issue({ id: "i3", project_id: "p-alpha", crew_id: "c2", assignee_id: "a2" }),
    issue({ id: "i4", project_id: null, crew_id: "c1", assignee_id: "a1" }),
  ]

  it("filterProjectId narrows the list without requiring selectedProjectId", () => {
    // Saved-view-only flow: user applies a saved view that scopes
    // to project alpha. The detail panel is NOT open
    // (selectedProjectId === null). Issues for alpha should
    // appear; the other project + the null-project row should
    // be filtered out.
    const { result } = renderHook(() =>
      useFilteredIssues({
        issues,
        search: "",
        selectedProjectId: null,
        filterProjectId: "p-alpha",
        filterCrewId: null,
        filterAgentId: null,
        filterStatuses: [],
        filterPriority: null,
      }),
    )
    expect(result.current.map((i) => i.id)).toEqual(["i1", "i3"])
  })

  it("selectedProjectId wins over a different filterProjectId", () => {
    // User has a saved view pinning project alpha, then clicks
    // project beta in the explorer. The detail panel opens for
    // beta (selectedProjectId="p-beta"), and the issue list
    // should narrow to beta — explicit navigation overrides
    // the saved-view filter. This is the load-bearing #320
    // contract: explicit click wins, saved-view filter doesn't
    // bleed through.
    const { result } = renderHook(() =>
      useFilteredIssues({
        issues,
        search: "",
        selectedProjectId: "p-beta",
        filterProjectId: "p-alpha",
        filterCrewId: null,
        filterAgentId: null,
        filterStatuses: [],
        filterPriority: null,
      }),
    )
    expect(result.current.map((i) => i.id)).toEqual(["i2"])
  })

  it("selectedProjectId without filterProjectId narrows to the selected project", () => {
    // Detail-panel-only flow: no saved view applied, user
    // clicks project alpha. Behaves like the saved-view case
    // but driven by `selectedProjectId`.
    const { result } = renderHook(() =>
      useFilteredIssues({
        issues,
        search: "",
        selectedProjectId: "p-alpha",
        filterProjectId: null,
        filterCrewId: null,
        filterAgentId: null,
        filterStatuses: [],
        filterPriority: null,
      }),
    )
    expect(result.current.map((i) => i.id)).toEqual(["i1", "i3"])
  })

  it("both null returns the full list (with other filters still applying)", () => {
    // The "All Issues" baseline. Issue list = every issue in
    // the workspace, modulo any crew/agent/status/priority
    // filters. Today no other filters are active so the full
    // four-row set comes back.
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
      }),
    )
    expect(result.current.map((i) => i.id)).toEqual(["i1", "i2", "i3", "i4"])
  })

  it("clearing filterProjectId leaves an explicit selectedProjectId intact", () => {
    // Simulates the page state right after clicking "All
    // Issues" (clears filterProjectId) while the user still
    // has a project detail panel open. The detail panel
    // should keep showing beta's issues; the saved-view clear
    // should NOT collapse the explicit selection.
    const { result } = renderHook(() =>
      useFilteredIssues({
        issues,
        search: "",
        selectedProjectId: "p-beta",
        filterProjectId: null,
        filterCrewId: null,
        filterAgentId: null,
        filterStatuses: [],
        filterPriority: null,
      }),
    )
    expect(result.current.map((i) => i.id)).toEqual(["i2"])
  })

  it("filterProjectId composes with crew + agent filters AND-style", () => {
    // Saved-view project alpha + crew c2 — expect only the
    // intersection (i3 in this fixture). Pins the AND-compose
    // contract so a future refactor that drops the filter
    // chain doesn't silently widen results.
    const { result } = renderHook(() =>
      useFilteredIssues({
        issues,
        search: "",
        selectedProjectId: null,
        filterProjectId: "p-alpha",
        filterCrewId: "c2",
        filterAgentId: null,
        filterStatuses: [],
        filterPriority: null,
      }),
    )
    expect(result.current.map((i) => i.id)).toEqual(["i3"])
  })
})
