import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { SidebarViewList } from "../sidebar-view-list"
import type { ViewItem } from "../sidebar-views"

const item = (id: string, over: Partial<ViewItem> = {}): ViewItem => ({ id, kind: "room", title: id, unread: 1, running: false, ...over })
afterEach(cleanup)

function Harness({ items, viewId = "unreads", query = "", activeId = "room:open", hold = false, onHold = () => {}, onOpen = () => {} }: { items: ViewItem[]; viewId?: string; query?: string; activeId?: string; hold?: boolean; onHold?: (h: boolean) => void; onOpen?: (i: ViewItem) => void }) {
  return <SidebarViewList label="Unreads" viewId={viewId} query={query} items={items} activeId={activeId} hold={hold} onHold={onHold} onOpen={onOpen} />
}

it("keeps rows still while held, offers Refresh and marks the kept open chat", () => {
  const onHold = vi.fn()
  const { rerender } = render(<Harness items={[item("room:a"), item("room:b")]} onHold={onHold} />)
  fireEvent.pointerEnter(screen.getByRole("region", { name: "Unreads" }))
  expect(onHold).toHaveBeenCalledWith(true)
  rerender(<Harness hold onHold={onHold} items={[item("room:c"), item("room:a"), item("room:b"), item("room:open", { kept: true })]} />)
  expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual(["Refresh", "room:a1", "room:b1"])
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }))
  const labels = screen.getAllByRole("button").map((b) => b.textContent)
  expect(labels).toEqual(["room:c1", "room:a1", "room:b1", "room:openOpen chat1"])
  expect(screen.getByRole("button", { name: /room:open/ })).toHaveAttribute("aria-current", "page")
})

it("does not open preview or not-yet-loaded rows, and releases the hold on unmount", () => {
  const onOpen = vi.fn(), onHold = vi.fn()
  const { unmount } = render(<Harness onOpen={onOpen} onHold={onHold} items={[item("agent:a", { kind: "agent", disabled: true }), item("fx:1", { fixture: true }), item("room:ok")]} />)
  fireEvent.click(screen.getByRole("button", { name: /agent:a/ }))
  fireEvent.click(screen.getByRole("button", { name: /fx:1/ }))
  expect(onOpen).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button", { name: /room:ok/ }))
  expect(onOpen).toHaveBeenCalledTimes(1)
  unmount()
  expect(onHold).toHaveBeenLastCalledWith(false)
})

it("a chat opened (search, shortcut) while the pointer is in the list shows at once, without Refresh", () => {
  const { rerender } = render(<Harness hold activeId="room:a" items={[item("room:a"), item("room:b")]} />)
  rerender(<Harness hold activeId="room:z" items={[item("room:a"), item("room:b"), item("room:z", { kept: true })]} />)
  expect(screen.getByRole("button", { name: /room:z/ })).toHaveAttribute("aria-current", "page")
  expect(screen.queryByRole("button", { name: "Refresh" })).toBeNull()
})

it("a title change on a held row shows at once", () => {
  const { rerender } = render(<Harness hold items={[item("room:a"), item("room:b")]} />)
  rerender(<Harness hold items={[item("room:a", { title: "Renamed" }), item("room:b")]} />)
  expect(screen.getByRole("button", { name: /Renamed/ })).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Refresh" })).toBeNull()
})

it("typing in the sidebar search while the list is held adds the newly matching rows at once, no Refresh", () => {
  const { rerender } = render(<Harness hold query="" items={[item("room:a"), item("room:b")]} />)
  rerender(<Harness hold query="c" items={[item("room:c"), item("room:a")]} />)
  expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual(["room:c1", "room:a1"])
  expect(screen.queryByRole("button", { name: "Refresh" })).toBeNull()
})

it("switching view while held shows the new view at once", () => {
  const { rerender } = render(<Harness hold viewId="unreads" items={[item("room:a")]} />)
  rerender(<Harness hold viewId="running" items={[item("agent:z", { kind: "agent" })]} />)
  expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual(["agent:zAI1"])
})

it("releases the hold when the focused row is removed (no blur event fires)", () => {
  const onHold = vi.fn()
  const { rerender } = render(<Harness hold onHold={onHold} items={[item("room:a"), item("room:b")]} />)
  const a = screen.getByText("room:a").closest("button")!
  const real = Element.prototype.matches
  const matches = vi.spyOn(Element.prototype, "matches").mockImplementation(function (this: Element, sel: string) { return sel === ":focus-visible" ? true : real.call(this, sel) })
  try {
    a.focus()
    fireEvent.focus(a)
    expect(onHold).toHaveBeenLastCalledWith(true)
    rerender(<Harness hold onHold={onHold} items={[item("room:b")]} />)
    expect(onHold).toHaveBeenLastCalledWith(false)
  } finally { matches.mockRestore() }
})

it("a disabled agent row says why", () => {
  render(<Harness items={[item("agent:a", { kind: "agent", disabled: true })]} />)
  const row = screen.getByRole("button", { name: /agent:a/ })
  expect(row).toHaveAttribute("title", expect.stringMatching(/not loaded yet/i))
  expect(row.textContent).toMatch(/chats not loaded yet/i)
})
