import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

// The stored height is a synced preference: one chosen on a desktop arrives
// on a phone taller than the viewport. 900 is PANEL_HEIGHT_MAX. The mock
// keeps the value, so a sequence of key presses reads the rendered height
// the previous press produced, as the real preference would.
const { pref, setHeight } = vi.hoisted(() => {
  const pref = { height: 900 }
  const setHeight = vi.fn((h: number) => { pref.height = h })
  return { pref, setHeight }
})
vi.mock("@/hooks/use-user-preference", () => ({
  useUserPreference: () => [pref.height, setHeight, { loading: false }],
}))
vi.mock("next/dynamic", () => ({ default: () => () => null }))

import { BottomPanel } from "../index"

const VIEWPORT = 664 // iOS Safari's visible area at 390 wide
const CEILING = Math.round(VIEWPORT * 0.6) // 398

beforeEach(() => {
  cleanup()
  setHeight.mockClear()
  pref.height = 900
  Object.defineProperty(window, "innerHeight", { configurable: true, value: VIEWPORT })
})

// A fresh element each time: React skips a re-render for the identical one.
const panel = () => (
  <BottomPanel
    workspaceId="ws-1"
    context={{ kind: "crew", crewId: "crew-1", crewSlug: "crew-1" }}
    initialOpen
  />
)

function handle() {
  const view = render(panel())
  const h = () => screen.getByRole("separator", { name: /resize bottom panel/i })
  // A key press stores the new height; the rendered panel only sees it on the
  // next render, which the preference hook would trigger and this mock cannot.
  const press = (key: string) => {
    fireEvent.keyDown(h(), { key })
    view.rerender(panel())
  }
  return { h, press }
}

describe("the dock's resize handle on a phone", () => {
  it("announces the phone ceiling, not the desktop maximum", () => {
    const { h } = handle()
    expect(h().getAttribute("aria-valuemax")).toBe(String(CEILING))
    expect(h().getAttribute("aria-valuenow")).toBe(String(CEILING))
  })

  it("steps the keyboard from the rendered height and stops at the ceiling", () => {
    // Stepping from the stored 900 changed the preference to 916 and nothing
    // on screen; stepping down from it took 32 presses to move at all.
    const { h, press } = handle()
    press("ArrowUp")
    expect(setHeight).toHaveBeenLastCalledWith(CEILING)
    expect(h().getAttribute("aria-valuenow")).toBe(String(CEILING))
    press("ArrowDown")
    expect(setHeight).toHaveBeenLastCalledWith(CEILING - 16)
    expect(h().getAttribute("aria-valuenow")).toBe(String(CEILING - 16))
    press("PageDown")
    expect(setHeight).toHaveBeenLastCalledWith(CEILING - 16 - 64)
    expect(h().getAttribute("aria-valuenow")).toBe(String(CEILING - 16 - 64))
  })
})
