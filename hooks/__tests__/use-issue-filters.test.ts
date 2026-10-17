import { describe, it, expect, vi, beforeEach } from "vitest"
import { act, renderHook } from "@testing-library/react"

const params = vi.hoisted(() => ({ value: new URLSearchParams() }))
vi.mock("next/navigation", () => ({ useSearchParams: () => params.value }))

import {
  NO_FILTERS,
  activeFilterCount,
  filtersFromParams,
  filtersFromSavedView,
  filtersToParams,
  savedViewPayload,
  useIssueFilters,
} from "@/hooks/use-issue-filters"
import type { SavedView } from "@/lib/types/mission"

describe("URL ⇄ filters", () => {
  it("reads every facet a link can carry, including the older crew_id / assignee_id", () => {
    const f = filtersFromParams(new URLSearchParams("status=review,todo&priority=high&crew_id=c1&assignee_id=a1&owner=u1&holder=person&label=docs"))
    expect(f).toEqual({ statuses: ["REVIEW", "TODO"], priority: "high", crewId: "c1", agentId: "a1", ownerId: "u1", holder: "person", label: "docs" })
    expect(activeFilterCount(f)).toBe(8)
  })

  it("ignores values it does not know", () => {
    expect(filtersFromParams(new URLSearchParams("priority=extreme&holder=robot"))).toEqual(NO_FILTERS)
  })

  it("writes back only its own keys and keeps the issue and project selection", () => {
    const out = filtersToParams({ ...NO_FILTERS, statuses: ["TODO"], crewId: "c1" }, new URLSearchParams("issue=ENG-4&project=p1&priority=low"))
    expect(out.get("issue")).toBe("ENG-4")
    expect(out.get("project")).toBe("p1")
    expect(out.get("status")).toBe("TODO")
    expect(out.get("crew_id")).toBe("c1")
    expect(out.has("priority")).toBe(false)
  })

  it("round-trips", () => {
    const f = { ...NO_FILTERS, statuses: ["IN_PROGRESS" as const], ownerId: "u9", label: "infra" }
    expect(filtersFromParams(filtersToParams(f, new URLSearchParams()))).toEqual(f)
  })
})

describe("saved views", () => {
  const view = (filters_json: string): SavedView => ({ id: "v", name: "v", filters_json, sort_json: null, view_type: "list", is_default: false, shared: false, created_at: "" })

  it("a saved view restores what was saved", () => {
    const payload = savedViewPayload({ ...NO_FILTERS, statuses: ["TODO"], priority: "urgent", crewId: "c1", ownerId: "u1", label: "bug" }, "p1", "pricing")
    const back = filtersFromSavedView(view(JSON.stringify(payload)))
    expect(back.filters).toEqual({ ...NO_FILTERS, statuses: ["TODO"], priority: "urgent", crewId: "c1", ownerId: "u1", label: "bug" })
    expect(back.projectId).toBe("p1")
    expect(back.search).toBe("pricing")
  })

  it("a malformed view applies nothing", () => {
    expect(filtersFromSavedView(view("{not json")).filters).toEqual(NO_FILTERS)
  })
})

describe("useIssueFilters", () => {
  beforeEach(() => {
    params.value = new URLSearchParams("crew_id=c1")
    window.history.replaceState(null, "", "/issues?crew_id=c1&issue=ENG-1")
  })

  it("starts from the link and writes changes into the URL without a history entry", () => {
    const before = window.history.length
    const { result } = renderHook(() => useIssueFilters())
    expect(result.current.filters.crewId).toBe("c1")
    act(() => result.current.patch({ statuses: ["REVIEW"] }))
    const now = new URLSearchParams(window.location.search)
    expect([now.get("crew_id"), now.get("issue"), now.get("status")]).toEqual(["c1", "ENG-1", "REVIEW"])
    expect(window.history.length).toBe(before)
    act(() => result.current.clear())
    expect(window.location.search).toBe("?issue=ENG-1")
  })
})
