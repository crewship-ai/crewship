import { act, renderHook } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { MESSAGE_ANCHOR_EVENT, useMessageAnchor } from "../use-message-anchor"

const params = vi.hoisted(() => ({ current: new URLSearchParams() }))
const toast = vi.hoisted(() => vi.fn())
vi.mock("next/navigation", () => ({ useSearchParams: () => params.current }))
vi.mock("sonner", () => ({ toast: { message: toast } }))

function turn(id: string) {
  const el = document.createElement("div")
  el.setAttribute("data-turn-id", id)
  el.scrollIntoView = vi.fn()
  document.body.appendChild(el)
  return el
}
beforeEach(() => { vi.useFakeTimers(); params.current = new URLSearchParams(); toast.mockReset() })
afterEach(() => { vi.useRealTimers(); document.body.innerHTML = "" })

it("scrolls to a message already rendered", () => {
  const el = turn("m1")
  params.current = new URLSearchParams("session=s1&message=m1")
  renderHook(() => useMessageAnchor("s1"))
  expect(el.scrollIntoView).toHaveBeenCalled()
  expect(el.getAttribute("data-search-target")).toBe("true")
  act(() => { vi.advanceTimersByTime(3000) })
  expect(el.hasAttribute("data-search-target")).toBe(false)
})

it("waits for a message that renders later", async () => {
  params.current = new URLSearchParams("message=m1")
  renderHook(() => useMessageAnchor("s1"))
  const el = turn("m1")
  await act(async () => { await Promise.resolve() })
  expect(el.scrollIntoView).toHaveBeenCalled()
  expect(toast).not.toHaveBeenCalledWith("Could not locate that message in this conversation view.")
})

it("says so when the message never appears", () => {
  params.current = new URLSearchParams("message=nope")
  renderHook(() => useMessageAnchor("s1"))
  act(() => { vi.advanceTimersByTime(9000) })
  expect(toast).toHaveBeenCalledTimes(1)
})

it("scrolls again when another hit in the already open session is picked, and survives the URL being rewritten", () => {
  const a = turn("m1"), b = turn("m2")
  params.current = new URLSearchParams("session=s1&message=m1")
  const { rerender } = renderHook(() => useMessageAnchor("s1"))
  expect(a.scrollIntoView).toHaveBeenCalledTimes(1)
  params.current = new URLSearchParams("session=s1")
  rerender()
  params.current = new URLSearchParams("session=s1&message=m2")
  rerender()
  expect(b.scrollIntoView).toHaveBeenCalledTimes(1)
  expect(a.scrollIntoView).toHaveBeenCalledTimes(1)
})

it("keeps a pending target when the page strips the param before the turn renders", async () => {
  params.current = new URLSearchParams("session=s1&message=m1")
  const { rerender } = renderHook(() => useMessageAnchor("s1"))
  params.current = new URLSearchParams("session=s1")
  rerender()
  const el = turn("m1")
  await act(async () => { await Promise.resolve() })
  expect(el.scrollIntoView).toHaveBeenCalled()
})

it("tells the transcript (scoped to its session) to release the bottom lock after scrolling", () => {
  const order: string[] = []
  const el = turn("m1")
  el.scrollIntoView = vi.fn(() => { order.push("scroll") })
  const on = (e: Event) => order.push(`release:${(e as CustomEvent).detail.sessionId}`)
  window.addEventListener(MESSAGE_ANCHOR_EVENT, on)
  params.current = new URLSearchParams("message=m1")
  renderHook(() => useMessageAnchor("s1"))
  window.removeEventListener(MESSAGE_ANCHOR_EVENT, on)
  expect(order.slice(0, 2)).toEqual(["scroll", "release:s1"])
})

it("keeps the hit in view while rows above settle, then stops after the window", () => {
  const el = turn("m1")
  params.current = new URLSearchParams("message=m1")
  renderHook(() => useMessageAnchor("s1"))
  const first = vi.mocked(el.scrollIntoView).mock.calls.length
  act(() => { vi.advanceTimersByTime(500) })
  const mid = vi.mocked(el.scrollIntoView).mock.calls.length
  expect(mid).toBeGreaterThan(first + 5)
  act(() => { vi.advanceTimersByTime(1500) })
  const end = vi.mocked(el.scrollIntoView).mock.calls.length
  act(() => { vi.advanceTimersByTime(1000) })
  expect(vi.mocked(el.scrollIntoView).mock.calls.length).toBe(end)
})

it("stops re-applying as soon as the reader scrolls or interacts", () => {
  const el = turn("m1")
  params.current = new URLSearchParams("message=m1")
  renderHook(() => useMessageAnchor("s1"))
  act(() => { vi.advanceTimersByTime(200) })
  window.dispatchEvent(new Event("wheel"))
  const n = vi.mocked(el.scrollIntoView).mock.calls.length
  act(() => { vi.advanceTimersByTime(500) })
  expect(vi.mocked(el.scrollIntoView).mock.calls.length).toBe(n)
})

const INPUTS = ["wheel", "touchstart", "pointerdown", "keydown"]

it("unmounting mid-settle removes all four window listeners and stops re-applying", () => {
  const el = turn("m1")
  params.current = new URLSearchParams("message=m1")
  const add = vi.spyOn(window, "addEventListener")
  const remove = vi.spyOn(window, "removeEventListener")
  const { unmount } = renderHook(() => useMessageAnchor("s1"))
  const added = INPUTS.map((t) => add.mock.calls.filter((c) => c[0] === t).length)
  expect(added).toEqual([1, 1, 1, 1])
  act(() => { vi.advanceTimersByTime(100) })
  unmount()
  INPUTS.forEach((t) => expect(remove.mock.calls.filter((c) => c[0] === t).length).toBeGreaterThanOrEqual(1))
  const n = vi.mocked(el.scrollIntoView).mock.calls.length
  act(() => { vi.advanceTimersByTime(1500) })
  expect(vi.mocked(el.scrollIntoView).mock.calls.length).toBe(n)
  add.mockRestore(); remove.mockRestore()
})

it("a session switch mid-settle stops it and clears the marker", () => {
  const el = turn("m1")
  params.current = new URLSearchParams("message=m1")
  const { rerender } = renderHook(({ id }) => useMessageAnchor(id), { initialProps: { id: "s1" } })
  act(() => { vi.advanceTimersByTime(100) })
  rerender({ id: "s2" })
  const n = vi.mocked(el.scrollIntoView).mock.calls.length
  act(() => { vi.advanceTimersByTime(1500) })
  expect(vi.mocked(el.scrollIntoView).mock.calls.length).toBe(n)
  expect(el.hasAttribute("data-search-target")).toBe(false)
})

it("moves focus to the hit without scrolling and announces the jump", () => {
  const el = turn("m1")
  el.focus = vi.fn()
  params.current = new URLSearchParams("message=m1")
  renderHook(() => useMessageAnchor("s1"))
  expect(el.getAttribute("tabindex")).toBe("-1")
  expect(el.focus).toHaveBeenCalledWith({ preventScroll: true })
  expect(toast).toHaveBeenCalledWith("Jumped to message")
})

it("a session switch while waiting drops the pending hit: no 'could not locate' in an unrelated chat", () => {
  params.current = new URLSearchParams("session=s1&message=nope")
  const { rerender } = renderHook(({ id }) => useMessageAnchor(id), { initialProps: { id: "s1" as string | null } })
  rerender({ id: "s2" })
  act(() => { vi.advanceTimersByTime(9000) })
  expect(toast).not.toHaveBeenCalled()
})

it("a second reveal to a different element removes the marker from the old one", () => {
  const a = turn("m1"), b = turn("m2")
  params.current = new URLSearchParams("message=m1")
  const { rerender } = renderHook(() => useMessageAnchor("s1"))
  expect(a.getAttribute("data-search-target")).toBe("true")
  params.current = new URLSearchParams("message=m2")
  rerender()
  expect(b.getAttribute("data-search-target")).toBe("true")
  expect(a.hasAttribute("data-search-target")).toBe(false)
})

it("does not steal focus from an input the reader is typing in, but still scrolls and announces", () => {
  const input = document.createElement("textarea")
  document.body.appendChild(input)
  input.focus()
  const el = turn("m1")
  el.focus = vi.fn()
  params.current = new URLSearchParams("message=m1")
  renderHook(() => useMessageAnchor("s1"))
  expect(el.focus).not.toHaveBeenCalled()
  expect(el.scrollIntoView).toHaveBeenCalled()
  expect(toast).toHaveBeenCalledWith("Jumped to message")
  expect(document.activeElement).toBe(input)
})
