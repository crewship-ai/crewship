import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

// DSH-01: the dashboard dims after the reconnect grace (4 s) without live
// updates. While the socket retries, the status is "connecting" with a reason
// (#3783); the shell's banner (realtime-status-banner.tsx) counts the outage
// from the first loss until "connected", and the dimming must agree with it.

let status = "connected"
let reason: string | null = null
vi.mock("@/hooks/use-realtime", () => ({ useRealtime: () => ({ status, reason }) }))

import { useDashboardOffline, OFFLINE_AFTER_MS } from "../use-dashboard-offline"

beforeEach(() => { vi.useFakeTimers(); status = "connected"; reason = null })
afterEach(() => vi.useRealTimers())

describe("useDashboardOffline", () => {
  it("a blip shorter than the grace never dims", () => {
    const { result, rerender } = renderHook(() => useDashboardOffline())
    status = "disconnected"; rerender()
    act(() => { vi.advanceTimersByTime(OFFLINE_AFTER_MS - 500) })
    status = "connected"; rerender()
    act(() => { vi.advanceTimersByTime(OFFLINE_AFTER_MS) })
    expect(result.current.offline).toBe(false)
  })

  it("reconnect attempts inside the outage do not restart the grace", () => {
    const { result, rerender } = renderHook(() => useDashboardOffline())
    status = "connecting"; reason = "retrying"; rerender()
    for (let i = 0; i < 5; i++) {
      act(() => { vi.advanceTimersByTime(900) })
      status = "disconnected"; rerender()
      act(() => { vi.advanceTimersByTime(100) })
      status = "connecting"; rerender()
    }
    expect(result.current.offline).toBe(true)
    expect(result.current.since).toMatch(/^\d{1,2}:\d{2}$/)
  })

  it("dims while a retry is connecting, and lifts once connected", () => {
    const { result, rerender } = renderHook(() => useDashboardOffline())
    status = "disconnected"; rerender()
    act(() => { vi.advanceTimersByTime(OFFLINE_AFTER_MS + 10) })
    expect(result.current.offline).toBe(true)
    status = "connecting"; reason = "retrying"; rerender()
    expect(result.current.offline).toBe(true)
    status = "connected"; reason = null; rerender()
    expect(result.current.offline).toBe(false)
  })

  it("the first connect and a session without realtime are not offline", () => {
    status = "connecting"
    const { result, rerender } = renderHook(() => useDashboardOffline())
    act(() => { vi.advanceTimersByTime(OFFLINE_AFTER_MS * 2) })
    expect(result.current.offline).toBe(false)
    status = "unavailable"; rerender()
    act(() => { vi.advanceTimersByTime(OFFLINE_AFTER_MS * 2) })
    expect(result.current.offline).toBe(false)
  })
})
