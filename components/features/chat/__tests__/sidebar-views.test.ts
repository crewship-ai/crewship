import { renderHook } from "@testing-library/react"
import { describe, expect, it } from "vitest"
import { availableViews, itemsForView, keepActive, VIEW_FIXTURES, type ViewItem } from "../sidebar-views"
import { useStableOrder } from "../use-stable-order"

const item = (id: string, over: Partial<ViewItem> = {}): ViewItem => ({ id, kind: "room", title: id, unread: 0, running: false, ...over })

describe("sidebar views", () => {
  it("offers only views with a real source unless the smart-section flag is on", () => {
    expect(availableViews(false).map((v) => v.id)).toEqual(["all", "unreads", "running"])
    expect(availableViews(true)).toHaveLength(9)
  })
  it("filters unreads and running agents; fixtures are marked as previews", () => {
    const all = [item("room:1", { unread: 2 }), item("agent:1", { kind: "agent", running: true }), item("agent:2", { kind: "agent" })]
    expect(itemsForView("unreads", all, {}).map((i) => i.id)).toEqual(["room:1"])
    expect(itemsForView("running", all, {}).map((i) => i.id)).toEqual(["agent:1"])
    expect(itemsForView("mentions", all, VIEW_FIXTURES).every((i) => i.fixture)).toBe(true)
    expect(itemsForView("saved", all, {})).toEqual([])
  })
  it("keeps the open chat appended outside the filter without moving other rows", () => {
    const all = [item("room:1", { unread: 1 }), item("room:2")]
    const shown = itemsForView("unreads", all, {})
    expect(keepActive(shown, all, "room:2")).toEqual({ items: [all[0], all[1]], activeOutside: true })
    expect(keepActive(shown, all, "room:1").activeOutside).toBe(false)
    expect(keepActive(shown, all, "room:gone").items).toBe(shown)
  })
})

describe("useStableOrder", () => {
  const a = { id: "a", n: 0 }, b = { id: "b", n: 0 }, c = { id: "c", n: 0 }
  it("does not move rows while held, updates shown rows in place, and offers refresh", () => {
    const { result, rerender } = renderHook(({ items, hold }) => useStableOrder(items, (x) => x.id, hold), { initialProps: { items: [a, b], hold: false } })
    rerender({ items: [a, b], hold: true })
    const b2 = { id: "b", n: 5 }
    rerender({ items: [c, b2, a], hold: true })
    expect(result.current.items.map((x) => x.id)).toEqual(["a", "b"])
    expect(result.current.items[1]).toBe(b2)
    expect(result.current.pending).toBe(true)
    rerender({ items: [c, b2, a], hold: false })
    expect(result.current.items.map((x) => x.id)).toEqual(["c", "b", "a"])
    expect(result.current.pending).toBe(false)
  })
})
