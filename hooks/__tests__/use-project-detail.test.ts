// useProjectDetail after the selection moved into the URL.
//
// The old file tested an `initialProjectId` prop and a once-per-id ref that
// existed to survive the gap before the project list arrived. The prop is
// gone — the URL is the source of truth in both directions now — but every
// invariant it protected still has to hold, so they are re-stated here
// against `?project=` instead of against a prop.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

// The global setup mocks next/navigation with an always-empty
// useSearchParams. This hook is about the URL, so read the real one.
vi.mock("next/navigation", () => ({
  useSearchParams: () =>
    new URLSearchParams(typeof window !== "undefined" ? window.location.search : ""),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  usePathname: () => "/issues",
}))

import { useProjectDetail } from "@/hooks/use-project-detail"
import type { Project } from "@/lib/types/mission"

function project(id: string, name = id): Project {
  return {
    id,
    workspace_id: "ws-1",
    name,
    slug: id,
    description: null,
    icon: null,
    color: "blue",
    status: "in_progress",
    priority: "none",
    health: "on_track",
    lead_type: null,
    lead_id: null,
    start_date: null,
    target_date: null,
    created_at: "2026-05-01T00:00:00Z",
    updated_at: "2026-05-01T00:00:00Z",
    issue_count: 0,
    done_count: 0,
    progress: 0,
  }
}

/** Arrive on /issues with (or without) a project already named. */
function arriveAt(url: string) {
  window.history.replaceState(null, "", url)
}

beforeEach(() => arriveAt("/issues"))

describe("useProjectDetail", () => {
  it("starts with no selection", () => {
    const { result } = renderHook(() => useProjectDetail({ projects: [project("a")] }))
    expect(result.current.selectedProjectId).toBeNull()
    expect(result.current.selectedProject).toBeNull()
  })

  it("derives selectedProject from the id + the list", () => {
    const { result, rerender } = renderHook(
      ({ projects }: { projects: Project[] }) => useProjectDetail({ projects }),
      { initialProps: { projects: [project("a", "Alpha"), project("b", "Beta")] } },
    )

    act(() => result.current.setSelectedProjectId("b"))
    expect(result.current.selectedProject?.name).toBe("Beta")

    // Renaming the same id should reflect in the derived project.
    rerender({ projects: [project("a", "Alpha"), project("b", "Bravo")] })
    expect(result.current.selectedProject?.name).toBe("Bravo")
  })

  it("clears the selection when the project disappears from the list", () => {
    const { result, rerender } = renderHook(
      ({ projects }: { projects: Project[] }) => useProjectDetail({ projects }),
      { initialProps: { projects: [project("a"), project("b")] } },
    )

    act(() => result.current.setSelectedProjectId("b"))
    expect(result.current.selectedProjectId).toBe("b")

    // Project "b" deleted by another user — the refreshed list drops it.
    rerender({ projects: [project("a")] })
    expect(result.current.selectedProjectId).toBeNull()
    expect(result.current.selectedProject).toBeNull()
    expect(window.location.search).not.toContain("project=")
  })

  it("handleProjectClose clears the selection", () => {
    const { result } = renderHook(() => useProjectDetail({ projects: [project("a")] }))
    act(() => result.current.setSelectedProjectId("a"))
    act(() => result.current.handleProjectClose())
    expect(result.current.selectedProjectId).toBeNull()
  })
})

// ── Arriving from a link ───────────────────────────────────────────────────
//
// /issues?project=<id> — the ⌘K palette's Projects rows, and any bookmark.
// The difficulty is the gap before the project list arrives: on the first
// render `projects` is still [], and clearing there would wipe the very id
// the URL carried.

describe("useProjectDetail — arriving on a link", () => {
  it("holds the id until the list arrives, then resolves it", () => {
    arriveAt("/issues?project=b")
    const { result, rerender } = renderHook(
      ({ projects }: { projects: Project[] }) => useProjectDetail({ projects }),
      { initialProps: { projects: [] as Project[] } },
    )
    // Nothing to match against yet — and crucially, not cleared either.
    expect(result.current.selectedProjectId).toBe("b")
    expect(result.current.selectedProject).toBeNull()

    rerender({ projects: [project("a"), project("b")] })
    expect(result.current.selectedProject?.id).toBe("b")
  })

  it("drops an id no project has, once the list can say so", () => {
    arriveAt("/issues?project=ghost")
    const { result, rerender } = renderHook(
      ({ projects }: { projects: Project[] }) => useProjectDetail({ projects }),
      { initialProps: { projects: [] as Project[] } },
    )
    rerender({ projects: [project("a")] })
    expect(result.current.selectedProjectId).toBeNull()
  })

  it("never fights the user afterwards", () => {
    arriveAt("/issues?project=b")
    const { result, rerender } = renderHook(
      ({ projects }: { projects: Project[] }) => useProjectDetail({ projects }),
      { initialProps: { projects: [project("a"), project("b")] } },
    )
    expect(result.current.selectedProjectId).toBe("b")

    // The user clicks away; a later refresh of the roster is not a
    // navigation and must not drag them back to the URL's project.
    act(() => result.current.setSelectedProjectId("a"))
    rerender({ projects: [project("a"), project("b"), project("c")] })
    expect(result.current.selectedProjectId).toBe("a")

    act(() => result.current.setSelectedProjectId(null))
    rerender({ projects: [project("a"), project("b")] })
    expect(result.current.selectedProjectId).toBeNull()
  })
})

// ── A SECOND link, while the page stays mounted ────────────────────────────
//
// The first attempt at surviving the load gap was a once-ever latch, and it
// swallowed every later link: on /issues?project=A, opening ⌘K and picking
// project B changed the URL and nothing else, because the component never
// unmounts. That is the commoner path of the two — the palette is most
// reachable from a page you are already on.
describe("useProjectDetail — a changed link on a mounted page", () => {
  it("follows a new id without a remount", () => {
    arriveAt("/issues?project=a")
    const projects = [project("a"), project("b")]
    const { result, rerender } = renderHook(() => useProjectDetail({ projects }))
    expect(result.current.selectedProjectId).toBe("a")

    // ⌘K navigates: the App Router hands down new search params.
    arriveAt("/issues?project=b")
    rerender()
    expect(result.current.selectedProjectId).toBe("b")
  })
})
