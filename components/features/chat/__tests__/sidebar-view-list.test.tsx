import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { SidebarViewList } from "../sidebar-view-list"
import type { ViewItem } from "../sidebar-views"

const item = (id: string, over: Partial<ViewItem> = {}): ViewItem => ({ id, kind: "room", title: id, unread: 1, running: false, ...over })
afterEach(cleanup)

function Harness({ items, hold = false, onHold = () => {}, onOpen = () => {} }: { items: ViewItem[]; hold?: boolean; onHold?: (h: boolean) => void; onOpen?: (i: ViewItem) => void }) {
  return <SidebarViewList label="Unreads" items={items} activeId="room:open" hold={hold} onHold={onHold} onOpen={onOpen} />
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
