import { act, renderHook } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { useMessageAnchor } from "../use-message-anchor"

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
  expect(toast).not.toHaveBeenCalled()
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
