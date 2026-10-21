import { act, renderHook } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { useRoutineStepSearch } from "../use-routine-step-search"

const { fetch } = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetch }))

const reply = (...slugs: string[]) => new Response(JSON.stringify(slugs.map((slug) => ({ slug }))))

beforeEach(() => {
  vi.useFakeTimers()
  fetch.mockReset()
})
afterEach(() => vi.useRealTimers())

it("asks the server for routines whose steps match, after the typing pauses (ODCH-RT-03)", async () => {
  fetch.mockResolvedValue(reply("weekly"))
  const view = renderHook(({ q }) => useRoutineStepSearch("ws", q), { initialProps: { q: "pri" } })
  expect(view.result.current).toBeNull()
  view.rerender({ q: "pricing page" })
  await act(async () => {
    await vi.advanceTimersByTimeAsync(400)
  })
  expect(fetch).toHaveBeenCalledTimes(1)
  expect(fetch.mock.calls[0][0]).toBe("/api/v1/workspaces/ws/pipelines?q=pricing%20page&limit=500")
  expect([...view.result.current!]).toEqual(["weekly"])
})

it("does not ask for a single character and clears when the search is cleared", async () => {
  fetch.mockResolvedValue(reply("weekly"))
  const view = renderHook(({ q }) => useRoutineStepSearch("ws", q), { initialProps: { q: "a" } })
  await act(async () => {
    await vi.advanceTimersByTimeAsync(400)
  })
  expect(fetch).not.toHaveBeenCalled()
  view.rerender({ q: "pricing" })
  await act(async () => {
    await vi.advanceTimersByTimeAsync(400)
  })
  expect(view.result.current).not.toBeNull()
  view.rerender({ q: "" })
  expect(view.result.current).toBeNull()
})

it("keeps the plain name search working when the server refuses", async () => {
  fetch.mockResolvedValue(new Response("nope", { status: 500 }))
  const view = renderHook(() => useRoutineStepSearch("ws", "pricing"))
  await act(async () => {
    await vi.advanceTimersByTimeAsync(400)
  })
  expect(view.result.current).toBeNull()
})
