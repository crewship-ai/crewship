import { act, renderHook } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { usePageReferenceWorkspace } from "@/hooks/use-page-reference-workspace"
import { useEditorRoute } from "@/components/features/pages/editor/use-editor-route"
import { navigationAllowed } from "@/hooks/use-navigation-guard"

const reference = { kind: "page" as const, pageId: "original", workspaceId: "linked-workspace" }

describe("stable link workspace adoption", () => {
  it("adopts once, then clears reference for an intentional picker switch instead of bouncing back", () => {
    const setWorkspace = vi.fn()
    const clear = vi.fn()
    const hook = renderHook(({ workspaceId }) => usePageReferenceWorkspace(reference, workspaceId, true, setWorkspace, clear),
      { initialProps: { workspaceId: "initial" } })
    expect(setWorkspace).toHaveBeenCalledWith("linked-workspace")
    hook.rerender({ workspaceId: "linked-workspace" })
    setWorkspace.mockClear()
    hook.rerender({ workspaceId: "chosen-in-picker" })
    expect(setWorkspace).not.toHaveBeenCalled()
    expect(clear).toHaveBeenCalledOnce()
  })

  it("lets the picker leave a revoked linked workspace without re-adoption", () => {
    const setWorkspace = vi.fn()
    const clear = vi.fn()
    const hook = renderHook(({ workspaceId, eligible }) => usePageReferenceWorkspace(reference, workspaceId, eligible, setWorkspace, clear),
      { initialProps: { workspaceId: "linked-workspace", eligible: true } })
    hook.rerender({ workspaceId: "linked-workspace", eligible: false })
    expect(clear).not.toHaveBeenCalled()
    hook.rerender({ workspaceId: "accessible", eligible: false })
    expect(clear).toHaveBeenCalledOnce()
    expect(setWorkspace).not.toHaveBeenCalled()
  })

  it("keeps route, workspace, and draft when a dirty picker change is canceled", () => {
    window.history.replaceState(null, "", "/pages/current?ref=original&workspace_id=linked-workspace")
    let workspace = "linked-workspace"
    const switchWorkspace = (id: string | null) => {
      const perform = () => { workspace = id! }
      if (navigationAllowed(perform)) perform()
    }
    const hook = renderHook(() => {
      const nav = useEditorRoute("current")
      usePageReferenceWorkspace(nav.reference, workspace, true, switchWorkspace, () => nav.openPage(null))
      return nav
    })
    act(() => hook.result.current.setDirty(true))
    act(() => switchWorkspace("other"))
    expect(workspace).toBe("linked-workspace")
    expect(hook.result.current.pending).not.toBeNull()
    act(() => hook.result.current.pending!.keep())
    expect(hook.result.current.dirty).toBe(true)
    expect(hook.result.current.reference).toEqual(reference)
    expect(workspace).toBe("linked-workspace")
    expect(window.location.search).toContain("ref=original")
  })
})
