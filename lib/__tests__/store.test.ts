import { describe, it, expect, beforeEach } from "vitest"
import { useAppStore } from "@/lib/store"

describe("useAppStore", () => {
  beforeEach(() => {
    // Reset store between tests
    useAppStore.setState({ currentWorkspaceId: null, sidebarOpen: true })
  })

  it("has correct initial state", () => {
    const state = useAppStore.getState()
    expect(state.currentWorkspaceId).toBeNull()
    expect(state.sidebarOpen).toBe(true)
  })

  it("setCurrentWorkspaceId sets workspace ID", () => {
    useAppStore.getState().setCurrentWorkspaceId("workspace-123")
    expect(useAppStore.getState().currentWorkspaceId).toBe("workspace-123")
  })

  it("setCurrentWorkspaceId clears workspace ID with null", () => {
    useAppStore.getState().setCurrentWorkspaceId("workspace-123")
    useAppStore.getState().setCurrentWorkspaceId(null)
    expect(useAppStore.getState().currentWorkspaceId).toBeNull()
  })

  it("setSidebarOpen toggles sidebar", () => {
    useAppStore.getState().setSidebarOpen(false)
    expect(useAppStore.getState().sidebarOpen).toBe(false)

    useAppStore.getState().setSidebarOpen(true)
    expect(useAppStore.getState().sidebarOpen).toBe(true)
  })
})
