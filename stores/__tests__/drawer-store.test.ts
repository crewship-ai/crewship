import { describe, it, expect, beforeEach } from "vitest"
import { useDrawerStore } from "@/stores/drawer-store"

beforeEach(() => {
  useDrawerStore.setState({
    open: false,
    activeTab: "files",
    mode: "overlay",
    width: 380,
  })
})

describe("useDrawerStore", () => {
  it("toggle without arg flips open state", () => {
    useDrawerStore.getState().toggle()
    expect(useDrawerStore.getState().open).toBe(true)
    useDrawerStore.getState().toggle()
    expect(useDrawerStore.getState().open).toBe(false)
  })

  it("toggle with the active tab while open flips closed", () => {
    useDrawerStore.setState({ open: true, activeTab: "files" })
    useDrawerStore.getState().toggle("files")
    // Same tab as active → just toggles open.
    expect(useDrawerStore.getState().open).toBe(false)
  })

  it("toggle with a different tab opens and switches", () => {
    useDrawerStore.setState({ open: false, activeTab: "files" })
    useDrawerStore.getState().toggle("triggers")
    const s = useDrawerStore.getState()
    expect(s.open).toBe(true)
    expect(s.activeTab).toBe("triggers")
  })

  it("setActiveTab opens the drawer", () => {
    useDrawerStore.getState().setActiveTab("team")
    const s = useDrawerStore.getState()
    expect(s.activeTab).toBe("team")
    expect(s.open).toBe(true)
  })

  it("setMode swaps overlay ↔ push", () => {
    useDrawerStore.getState().setMode("push")
    expect(useDrawerStore.getState().mode).toBe("push")
    useDrawerStore.getState().setMode("overlay")
    expect(useDrawerStore.getState().mode).toBe("overlay")
  })

  it("setWidth clamps to [280, 720]", () => {
    useDrawerStore.getState().setWidth(100)
    expect(useDrawerStore.getState().width).toBe(280)
    useDrawerStore.getState().setWidth(1000)
    expect(useDrawerStore.getState().width).toBe(720)
    useDrawerStore.getState().setWidth(450)
    expect(useDrawerStore.getState().width).toBe(450)
  })
})
