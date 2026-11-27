// Between the chat and the right panel (Artifacts, Sheet) a divider that can be dragged, so the Sheet
// can be the main surface. Separator semantics, arrow keys, double-click resets, width kept per person.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { PanelResizer, SPLIT_DEFAULT, SPLIT_MAX, SPLIT_MIN, useSplitWidth } from "../panel-resizer"

function Harness({ userId }: { userId: string }) {
  const [width, setWidth] = useSplitWidth(userId)
  return <div data-testid="row" style={{ width: 1000 }}>
    <PanelResizer value={width} onChange={setWidth} controls="panel" />
    <aside id="panel" data-width={width} />
  </div>
}
const bar = () => screen.getByRole("separator", { name: "Resize the panel" })
const width = () => Number(document.getElementById("panel")!.dataset.width)

// vitest.setup.ts stubs localStorage with one that keeps nothing; the width's memory needs a store that does.
let store: Map<string, string>
beforeEach(() => {
  store = new Map()
  vi.stubGlobal("localStorage", { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => { store.set(k, String(v)) }, removeItem: (k: string) => { store.delete(k) }, clear: () => store.clear() })
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe("PanelResizer", () => {
  it("is a vertical separator that says how wide the panel is", () => {
    render(<Harness userId="u1" />)
    expect(bar()).toHaveAttribute("aria-orientation", "vertical")
    expect(bar()).toHaveAttribute("aria-valuenow", String(SPLIT_DEFAULT))
    expect(bar()).toHaveAttribute("aria-valuemin", String(SPLIT_MIN))
    expect(bar()).toHaveAttribute("aria-valuemax", String(SPLIT_MAX))
    expect(bar()).toHaveAttribute("aria-controls", "panel")
    expect(bar()).toHaveAttribute("tabindex", "0")
    expect(bar().className).toMatch(/cursor-col-resize/)
  })
  it("widens the panel with ← and narrows it with →, within its limits", () => {
    render(<Harness userId="u1" />)
    fireEvent.keyDown(bar(), { key: "ArrowLeft" })
    expect(width()).toBe(SPLIT_DEFAULT + 5)
    fireEvent.keyDown(bar(), { key: "ArrowRight" }); fireEvent.keyDown(bar(), { key: "ArrowRight" })
    expect(width()).toBe(SPLIT_DEFAULT - 5)
    fireEvent.keyDown(bar(), { key: "Home" })
    expect(width()).toBe(SPLIT_MAX)
    fireEvent.keyDown(bar(), { key: "End" })
    expect(width()).toBe(SPLIT_MIN)
  })
  it("goes back to the default on a double-click", () => {
    render(<Harness userId="u1" />)
    fireEvent.keyDown(bar(), { key: "Home" })
    fireEvent.doubleClick(bar())
    expect(width()).toBe(SPLIT_DEFAULT)
  })
  it("follows the pointer while dragged", () => {
    render(<Harness userId="u1" />)
    const row = screen.getByTestId("row")
    row.getBoundingClientRect = () => ({ left: 0, right: 1000, width: 1000, top: 0, bottom: 100, height: 100, x: 0, y: 0, toJSON() {} })
    fireEvent.pointerDown(bar(), { clientX: 550, pointerId: 1, button: 0 })
    fireEvent.pointerMove(bar(), { clientX: 300, pointerId: 1 })
    expect(width()).toBe(70)
    fireEvent.pointerUp(bar(), { clientX: 300, pointerId: 1 })
    fireEvent.pointerMove(bar(), { clientX: 900, pointerId: 1 })
    expect(width(), "not after the pointer let go").toBe(70)
  })
  it("remembers the width for the same person, and not for another", () => {
    const first = render(<Harness userId="u1" />)
    fireEvent.keyDown(bar(), { key: "ArrowLeft" })
    expect(store.get("crewship.chat-split:v1:u1"), "kept under this person's key").toBe(String(SPLIT_DEFAULT + 5))
    first.unmount()
    render(<Harness userId="u1" />)
    expect(width()).toBe(SPLIT_DEFAULT + 5)
    cleanup()
    render(<Harness userId="u2" />)
    expect(width()).toBe(SPLIT_DEFAULT)
  })
  it("ignores a stored width outside the limits", () => {
    store.set("crewship.chat-split:v1:u1", "3")
    render(<Harness userId="u1" />)
    expect(width()).toBe(SPLIT_DEFAULT)
    act(() => {})
  })
})
