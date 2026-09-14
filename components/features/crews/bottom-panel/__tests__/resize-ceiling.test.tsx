import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

// The stored height is a synced preference: one chosen on a desktop arrives
// on a phone taller than the viewport. 900 is PANEL_HEIGHT_MAX.
const setHeight = vi.fn()
vi.mock("@/hooks/use-user-preference", () => ({
  useUserPreference: () => [900, setHeight, { loading: false }],
}))
vi.mock("next/dynamic", () => ({ default: () => () => null }))

import { BottomPanel } from "../index"

const VIEWPORT = 664 // iOS Safari's visible area at 390 wide
const CEILING = Math.round(VIEWPORT * 0.6) // 398

beforeEach(() => {
  cleanup()
  setHeight.mockClear()
  Object.defineProperty(window, "innerHeight", { configurable: true, value: VIEWPORT })
})

function handle() {
  render(
    <BottomPanel
      workspaceId="ws-1"
      context={{ kind: "crew", crewId: "crew-1", crewSlug: "crew-1" }}
      initialOpen
    />,
  )
  return screen.getByRole("separator", { name: /resize bottom panel/i })
}

describe("the dock's resize handle on a phone", () => {
  it("announces the phone ceiling, not the desktop maximum", () => {
    const h = handle()
    expect(h.getAttribute("aria-valuemax")).toBe(String(CEILING))
    expect(h.getAttribute("aria-valuenow")).toBe(String(CEILING))
  })

  it("steps the keyboard from the rendered height and stops at the ceiling", () => {
    // Stepping from the stored 900 changed the preference to 916 and nothing
    // on screen; stepping down from it took 32 presses to move at all.
    const h = handle()
    fireEvent.keyDown(h, { key: "ArrowUp" })
    expect(setHeight).toHaveBeenLastCalledWith(CEILING)
    fireEvent.keyDown(h, { key: "ArrowDown" })
    expect(setHeight).toHaveBeenLastCalledWith(CEILING - 16)
    fireEvent.keyDown(h, { key: "PageDown" })
    expect(setHeight).toHaveBeenLastCalledWith(CEILING - 64)
  })
})
