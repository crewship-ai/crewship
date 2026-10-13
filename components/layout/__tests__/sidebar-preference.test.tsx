import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, cleanup, act } from "@testing-library/react"

let mode: "hover" | "collapsed" | "pinned" = "hover"
const setSidebarMode = vi.fn((m: typeof mode) => { mode = m })
vi.mock("@/components/ui/sidebar", () => ({
  useSidebar: () => ({ sidebarMode: mode, setSidebarMode, isMobile: false }),
}))

import { SidebarPreference } from "../sidebar-preference"
import { SIDEBAR_STORAGE_KEY, readSidebar, setSidebarChoice } from "@/lib/theme/appearance"

let store: Map<string, string>
beforeEach(() => {
  store = new Map()
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  })
  mode = "hover"
  setSidebarMode.mockClear()
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("SidebarPreference", () => {
  it("records the rail's own mode in the synced store", () => {
    mode = "pinned"
    render(<SidebarPreference />)
    expect(store.get(SIDEBAR_STORAGE_KEY)).toBe("pinned")
    expect(readSidebar()).toBe("pinned")
    expect(setSidebarMode).not.toHaveBeenCalled()
  })

  it("switches the rail when the preference changes (Settings or the server)", () => {
    const { rerender } = render(<SidebarPreference />)
    act(() => setSidebarChoice("collapsed"))
    rerender(<SidebarPreference />)
    expect(setSidebarMode).toHaveBeenCalledWith("collapsed")
  })
})
