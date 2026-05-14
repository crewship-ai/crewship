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
