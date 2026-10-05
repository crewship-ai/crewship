import { act, cleanup, renderHook } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { useAgentSpend, useCrewSpend, useSubscriptionUsage, useTopSpenders } from "@/hooks/use-paymaster"

const { api, events } = vi.hoisted(() => ({ api: vi.fn(), events: new Map<string, () => void>() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: (event: string, callback: () => void) => { events.set(event, callback) } }))
beforeEach(() => {
  vi.useFakeTimers()
  events.clear()
  api.mockReset().mockResolvedValue({ ok: true, status: 200, json: async () => ({ rows: [] }) })
})
afterEach(() => { cleanup(); vi.useRealTimers() })

it("coalesces terminal run and routine events into one refresh per debounce window", async () => {
  const { result } = renderHook(() => useCrewSpend("24h"))
  await act(async () => { await Promise.resolve() })
  expect(result.current.loading).toBe(false)
  expect(api).toHaveBeenCalledTimes(1)
  act(() => { events.get("run.completed")!(); events.get("pipeline.run.completed")!(); events.get("run.completed")!() })
  await act(async () => { await vi.advanceTimersByTimeAsync(1999) })
  expect(api).toHaveBeenCalledTimes(1)
  await act(async () => { await vi.advanceTimersByTimeAsync(1) })
  expect(api).toHaveBeenCalledTimes(2)
  act(() => { events.get("pipeline.run.completed")!() })
  await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
  expect(api).toHaveBeenCalledTimes(3)
  expect(result.current.error).toBeNull()
})

it("cancels a queued top-spender refresh when its surface unmounts", async () => {
  const { unmount } = renderHook(() => useTopSpenders("7d"))
  await act(async () => { await Promise.resolve() })
  act(() => { events.get("run.completed")!() })
  expect(vi.getTimerCount()).toBe(1)
  unmount()
  expect(vi.getTimerCount()).toBe(0)
  await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
  expect(api).toHaveBeenCalledTimes(1)
})

it("does not restore a deselected crew when a queued event expires", async () => {
  const { result, rerender } = renderHook(({ crew }: { crew: string | null }) => useAgentSpend(crew, "24h"), { initialProps: { crew: "crew-a" as string | null } })
  await act(async () => { await Promise.resolve() })
  act(() => { events.get("run.completed")!() })
  rerender({ crew: null })
  await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
  expect(api).toHaveBeenCalledTimes(1)
  expect(result.current.data).toBeNull()
  expect(result.current.loading).toBe(false)
})

it("does not refresh subscription data after its permission is disabled", async () => {
  const { result, rerender } = renderHook(({ enabled }) => useSubscriptionUsage("7d", 0, enabled), { initialProps: { enabled: true } })
  await act(async () => { await Promise.resolve() })
  act(() => { events.get("pipeline.run.completed")!() })
  rerender({ enabled: false })
  await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
  expect(api).toHaveBeenCalledTimes(1)
  expect(result.current.data).toBeNull()
  rerender({ enabled: true })
  await act(async () => { await Promise.resolve() })
  expect(api).toHaveBeenCalledTimes(2)
})
