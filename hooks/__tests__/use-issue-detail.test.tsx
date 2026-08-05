// Selecting an issue is a navigation, so it belongs in the URL.
//
// Before this, /issues held the open issue in component state: the link could
// not be shared, a refresh lost it, and Back left the page entirely instead of
// closing the detail. `?project=` was worse — read on arrival, never written,
// so the two halves of the same screen disagreed about whether the URL meant
// anything.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

// The global setup mocks next/navigation with an always-empty
// useSearchParams. These hooks are about the URL, so read the real one.
vi.mock("next/navigation", () => ({
  useSearchParams: () =>
    new URLSearchParams(typeof window !== "undefined" ? window.location.search : ""),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  usePathname: () => "/issues",
}))

import { useIssueDetail } from "../use-issue-detail"
import { useProjectDetail } from "../use-project-detail"
import type { Mission, Project } from "@/lib/types/mission"

// useProjectDetail appears here only for the half of the story it shares with
// the issue selection — that ?project= is written as well as read, and that
// the two params do not clobber each other. Its own invariants (the load gap,
// a changed link on a mounted page) live in use-project-detail.test.ts.

function issue(over: Partial<Mission> = {}): Mission {
  return {
    id: "iss1",
    workspace_id: "ws1",
    crew_id: "crew1",
    lead_agent_id: "",
    lead_agent_name: "",
    lead_agent_slug: "",
    trace_id: "",
    title: "One",
    description: null,
    status: "BACKLOG",
    plan: null,
    workflow_template: null,
    total_token_count: null,
    total_estimated_cost: null,
    created_at: "2026-08-01T12:00:00Z",
    updated_at: "2026-08-01T12:00:00Z",
    completed_at: null,
    task_stats: null,
    tasks: [],
    total_token_budget: null,
    complexity: null,
    pattern: null,
    identifier: "ENG-4",
    ...over,
  }
}

function project(over: Partial<Project> = {}): Project {
  return {
    id: "p1",
    workspace_id: "ws1",
    name: "File Operations",
    slug: "file-operations",
    description: null,
    icon: "folder",
    color: "blue",
    status: "in_progress",
    priority: "none",
    health: "on_track",
    lead_type: null,
    lead_id: null,
    start_date: null,
    target_date: null,
    created_at: "2026-07-26T12:00:00Z",
    updated_at: "2026-08-01T12:00:00Z",
    issue_count: 0,
    done_count: 0,
    progress: 0,
    ...over,
  }
}

const ISSUES = [issue(), issue({ id: "iss2", identifier: "ENG-9", title: "Two" })]

function url() {
  return window.location.pathname + window.location.search
}

describe("useIssueDetail — the selection lives in the URL", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/issues")
  })
  afterEach(() => {
    window.history.replaceState(null, "", "/issues")
  })

  it("writes the identifier when an issue is opened", () => {
    const { result } = renderHook(() => useIssueDetail({ issues: ISSUES }))
    act(() => result.current.handleIssueSelect(ISSUES[1]))
    expect(url()).toBe("/issues?issue=ENG-9")
    expect(result.current.selectedIssue?.id).toBe("iss2")
  })

  it("takes the identifier out of the URL when the detail is closed", () => {
    const { result } = renderHook(() => useIssueDetail({ issues: ISSUES }))
    act(() => result.current.handleIssueSelect(ISSUES[0]))
    act(() => result.current.handleIssueClose())
    expect(url()).toBe("/issues")
    expect(result.current.selectedIssue).toBeNull()
  })

  it("opens whatever the URL already named — a shared link, or a refresh", () => {
    window.history.replaceState(null, "", "/issues?issue=ENG-9")
    const { result } = renderHook(() => useIssueDetail({ issues: ISSUES }))
    expect(result.current.selectedIssue?.title).toBe("Two")
  })

  it("Back closes the detail instead of leaving the page", async () => {
    const { result } = renderHook(() => useIssueDetail({ issues: ISSUES }))
    act(() => result.current.handleIssueSelect(ISSUES[0]))
    expect(url()).toBe("/issues?issue=ENG-4")

    // A history entry, not a replace — that is the whole difference.
    act(() => {
      window.history.back()
    })
    await vi.waitFor(() => expect(url()).toBe("/issues"))
    await vi.waitFor(() => expect(result.current.selectedIssue).toBeNull())
  })

  it("clicking the open issue again closes it", () => {
    const { result } = renderHook(() => useIssueDetail({ issues: ISSUES }))
    act(() => result.current.handleIssueSelect(ISSUES[0]))
    act(() => result.current.handleIssueSelect(ISSUES[0]))
    expect(url()).toBe("/issues")
  })

  it("keeps the project param when the issue param changes", () => {
    window.history.replaceState(null, "", "/issues?project=p1")
    const { result } = renderHook(() => useIssueDetail({ issues: ISSUES }))
    act(() => result.current.handleIssueSelect(ISSUES[0]))
    expect(window.location.search).toContain("project=p1")
    expect(window.location.search).toContain("issue=ENG-4")
  })
})

describe("useProjectDetail — ?project= is written, not just read", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/issues")
  })
  afterEach(() => {
    window.history.replaceState(null, "", "/issues")
  })

  it("writes the id when a project is opened", () => {
    const { result } = renderHook(() => useProjectDetail({ projects: [project()] }))
    act(() => result.current.setSelectedProjectId("p1"))
    expect(url()).toBe("/issues?project=p1")
    expect(result.current.selectedProject?.name).toBe("File Operations")
  })

  it("still honours a project the URL named on arrival", () => {
    window.history.replaceState(null, "", "/issues?project=p1")
    const { result } = renderHook(() => useProjectDetail({ projects: [project()] }))
    expect(result.current.selectedProjectId).toBe("p1")
  })

  it("drops the param when the project is closed", () => {
    window.history.replaceState(null, "", "/issues?project=p1")
    const { result } = renderHook(() => useProjectDetail({ projects: [project()] }))
    act(() => result.current.handleProjectClose())
    expect(url()).toBe("/issues")
  })

  it("clears a selection whose project has left the list — but not before it loads", () => {
    window.history.replaceState(null, "", "/issues?project=p1")
    const { result, rerender } = renderHook(
      ({ projects }: { projects: Project[] }) => useProjectDetail({ projects }),
      { initialProps: { projects: [] as Project[] } },
    )
    // An empty list is "not loaded yet", not "deleted".
    expect(result.current.selectedProjectId).toBe("p1")
    rerender({ projects: [project({ id: "p2" })] })
    expect(result.current.selectedProjectId).toBeNull()
  })
})
