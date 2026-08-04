import { describe, it, expect } from "vitest"
import { renderHook, act } from "@testing-library/react"
import { useProjectDetail } from "@/hooks/use-project-detail"
import type { Project } from "@/lib/types/mission"

function project(id: string, name = id): Project {
  return {
    id,
    workspace_id: "ws-1",
    name,
    description: null,
    status: "active",
    crew_id: null,
    color: null,
    icon: null,
    target_date: null,
    created_at: "2026-05-01T00:00:00Z",
    updated_at: "2026-05-01T00:00:00Z",
  } as Project
}

describe("useProjectDetail", () => {
  it("starts with no selection", () => {
    const { result } = renderHook(() => useProjectDetail({ projects: [project("a")] }))
    expect(result.current.selectedProjectId).toBeNull()
    expect(result.current.selectedProject).toBeNull()
  })

  it("derives selectedProject from selectedProjectId + projects", () => {
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

  it("clears selectedProjectId when the selected project disappears", () => {
    const { result, rerender } = renderHook(
      ({ projects }: { projects: Project[] }) => useProjectDetail({ projects }),
      { initialProps: { projects: [project("a"), project("b")] } },
    )

    act(() => result.current.setSelectedProjectId("b"))
    expect(result.current.selectedProjectId).toBe("b")

    // Project "b" deleted by another user — refreshed list no longer carries it.
    rerender({ projects: [project("a")] })
    expect(result.current.selectedProjectId).toBeNull()
    expect(result.current.selectedProject).toBeNull()
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
// /issues?project=<id> — the ⌘K palette's Projects rows, and any bookmark —
// used to be ignored outright: the hook started at null and nothing read the
// URL, so picking a project in search dropped the caller on an unfiltered
// board. The id has to survive the gap before the project list arrives, which
// is the whole difficulty: on the first render `projects` is still [].

describe("useProjectDetail — initialProjectId", () => {
  it("applies the id once the project list arrives", () => {
    const { result, rerender } = renderHook(
      ({ projects }: { projects: Project[] }) =>
        useProjectDetail({ projects, initialProjectId: "b" }),
      { initialProps: { projects: [] as Project[] } },
    )
    // Nothing to match against yet — and crucially, not cleared either.
    expect(result.current.selectedProjectId).toBeNull()

    rerender({ projects: [project("a"), project("b")] })
    expect(result.current.selectedProjectId).toBe("b")
  })

  it("ignores an id no project has", () => {
    const { result, rerender } = renderHook(
      ({ projects }: { projects: Project[] }) =>
        useProjectDetail({ projects, initialProjectId: "ghost" }),
      { initialProps: { projects: [] as Project[] } },
    )
    rerender({ projects: [project("a")] })
    expect(result.current.selectedProjectId).toBeNull()
  })

  it("applies once, and never fights the user afterwards", () => {
    const { result, rerender } = renderHook(
      ({ projects }: { projects: Project[] }) =>
        useProjectDetail({ projects, initialProjectId: "b" }),
      { initialProps: { projects: [] as Project[] } },
    )
    rerender({ projects: [project("a"), project("b")] })
    expect(result.current.selectedProjectId).toBe("b")

    // The user clicks away; a later refresh of the list must not drag them
    // back to the project the URL named.
    act(() => result.current.setSelectedProjectId("a"))
    rerender({ projects: [project("a"), project("b")] })
    expect(result.current.selectedProjectId).toBe("a")

    act(() => result.current.setSelectedProjectId(null))
    rerender({ projects: [project("a"), project("b")] })
    expect(result.current.selectedProjectId).toBeNull()
  })

  it("changes nothing when no id was given", () => {
    const { result, rerender } = renderHook(
      ({ projects }: { projects: Project[] }) => useProjectDetail({ projects }),
      { initialProps: { projects: [] as Project[] } },
    )
    rerender({ projects: [project("a")] })
    expect(result.current.selectedProjectId).toBeNull()
  })
})
