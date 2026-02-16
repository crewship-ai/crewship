import { describe, it, expect, beforeEach } from "vitest"
import { useAppStore } from "@/lib/store"

describe("useAppStore", () => {
  beforeEach(() => {
    // Reset store between tests
    useAppStore.setState({ currentOrgId: null, sidebarOpen: true })
  })

  it("has correct initial state", () => {
    const state = useAppStore.getState()
    expect(state.currentOrgId).toBeNull()
    expect(state.sidebarOpen).toBe(true)
  })

  it("setCurrentOrgId sets org ID", () => {
    useAppStore.getState().setCurrentOrgId("org-123")
    expect(useAppStore.getState().currentOrgId).toBe("org-123")
  })

  it("setCurrentOrgId clears org ID with null", () => {
    useAppStore.getState().setCurrentOrgId("org-123")
    useAppStore.getState().setCurrentOrgId(null)
    expect(useAppStore.getState().currentOrgId).toBeNull()
  })

  it("setSidebarOpen toggles sidebar", () => {
    useAppStore.getState().setSidebarOpen(false)
    expect(useAppStore.getState().sidebarOpen).toBe(false)

    useAppStore.getState().setSidebarOpen(true)
    expect(useAppStore.getState().sidebarOpen).toBe(true)
  })
})
