import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

let changeHandler: (() => void) | null = null

const mockMql = {
  addEventListener: vi.fn((_event: string, handler: () => void) => {
    changeHandler = handler
  }),
  removeEventListener: vi.fn(),
}

vi.stubGlobal("matchMedia", vi.fn(() => mockMql))

import { useIsMobile } from "@/hooks/use-mobile"

describe("useIsMobile", () => {
  beforeEach(() => {
    changeHandler = null
    vi.mocked(matchMedia).mockClear()
    mockMql.addEventListener.mockClear()
    mockMql.removeEventListener.mockClear()
  })

  it("returns false on desktop viewport", () => {
    Object.defineProperty(window, "innerWidth", { value: 1024, writable: true })
    const { result } = renderHook(() => useIsMobile())
    expect(result.current).toBe(false)
  })

  it("returns true on mobile viewport", () => {
    Object.defineProperty(window, "innerWidth", { value: 375, writable: true })
    const { result } = renderHook(() => useIsMobile())
    expect(result.current).toBe(true)
  })

  it("returns true at exactly 767px (below breakpoint)", () => {
    Object.defineProperty(window, "innerWidth", { value: 767, writable: true })
    const { result } = renderHook(() => useIsMobile())
    expect(result.current).toBe(true)
  })

  it("returns false at exactly 768px (at breakpoint)", () => {
    Object.defineProperty(window, "innerWidth", { value: 768, writable: true })
    const { result } = renderHook(() => useIsMobile())
    expect(result.current).toBe(false)
  })

  it("updates when viewport changes", () => {
    Object.defineProperty(window, "innerWidth", { value: 1024, writable: true })
    const { result } = renderHook(() => useIsMobile())
    expect(result.current).toBe(false)

    // Simulate resize to mobile
    Object.defineProperty(window, "innerWidth", { value: 375, writable: true })
    act(() => {
      changeHandler?.()
    })
    expect(result.current).toBe(true)
  })

  it("listens to matchMedia with correct breakpoint", () => {
    Object.defineProperty(window, "innerWidth", { value: 1024, writable: true })
    renderHook(() => useIsMobile())
    expect(matchMedia).toHaveBeenCalledWith("(max-width: 767px)")
  })

  it("cleans up event listener on unmount", () => {
    Object.defineProperty(window, "innerWidth", { value: 1024, writable: true })
    const { unmount } = renderHook(() => useIsMobile())
    unmount()
    expect(mockMql.removeEventListener).toHaveBeenCalledWith("change", expect.any(Function))
  })
})
