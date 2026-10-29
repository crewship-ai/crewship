import { describe, it, expect, vi, beforeEach } from "vitest"
import { createElement } from "react"
import { renderToString } from "react-dom/server"
import { renderHook, act } from "@testing-library/react"

type Listener = () => void

const listeners = new Map<string, Set<Listener>>()

function mqlFor(query: string) {
  const min = Number(/min-width:\s*(\d+)px/.exec(query)?.[1] ?? NaN)
  const max = Number(/max-width:\s*(\d+)px/.exec(query)?.[1] ?? Infinity)
  return {
    media: query,
    get matches() {
      return window.innerWidth >= min && window.innerWidth <= max
    },
    addEventListener: vi.fn((_event: string, handler: Listener) => {
      if (!listeners.has(query)) listeners.set(query, new Set())
      listeners.get(query)!.add(handler)
    }),
    removeEventListener: vi.fn((_event: string, handler: Listener) => {
      listeners.get(query)?.delete(handler)
    }),
  }
}

vi.stubGlobal("matchMedia", vi.fn(mqlFor))

import {
  LAYOUT_BAND_QUERIES,
  LAYOUT_BREAKPOINTS,
  layoutBreakpointFor,
  matchesLayoutBand,
  useLayoutBand,
  useLayoutBreakpoint,
} from "@/hooks/use-layout-breakpoint"
import { useIsMobile } from "@/hooks/use-mobile"

function setWidth(width: number) {
  Object.defineProperty(window, "innerWidth", { value: width, writable: true, configurable: true })
}

function resize(width: number) {
  setWidth(width)
  act(() => {
    for (const set of listeners.values()) for (const fn of set) fn()
  })
}

describe("layout breakpoints", () => {
  beforeEach(() => {
    listeners.clear()
    vi.mocked(matchMedia).mockClear()
  })

  it("uses the Tailwind md / xl / 2xl edges", () => {
    expect(LAYOUT_BREAKPOINTS).toEqual({ tablet: 768, desktop: 1280, wide: 1536 })
  })

  it.each([
    [0, "phone"],
    [375, "phone"],
    [767, "phone"],
    [768, "tablet"],
    [1024, "tablet"],
    [1279, "tablet"],
    [1280, "desktop"],
    [1535, "desktop"],
    [1536, "wide"],
    [2560, "wide"],
  ] as const)("names %ipx %s", (width, expected) => {
    expect(layoutBreakpointFor(width)).toBe(expected)
    setWidth(width)
    const { result } = renderHook(() => useLayoutBreakpoint())
    expect(result.current).toBe(expected)
  })

  it("agrees with useIsMobile on the phone edge", () => {
    for (const width of [767, 768]) {
      setWidth(width)
      const bp = renderHook(() => useLayoutBreakpoint()).result.current
      const mobile = renderHook(() => useIsMobile()).result.current
      expect(bp === "phone").toBe(mobile)
    }
  })

  it("listens on exactly the three edges", () => {
    setWidth(1024)
    renderHook(() => useLayoutBreakpoint())
    const queries = vi.mocked(matchMedia).mock.calls.map(([q]) => q).sort()
    expect(queries).toEqual(["(min-width: 1280px)", "(min-width: 1536px)", "(min-width: 768px)"])
  })

  it("follows the viewport across each edge", () => {
    setWidth(767)
    const { result } = renderHook(() => useLayoutBreakpoint())
    expect(result.current).toBe("phone")
    resize(768)
    expect(result.current).toBe("tablet")
    resize(1279)
    expect(result.current).toBe("tablet")
    resize(1280)
    expect(result.current).toBe("desktop")
    resize(1535)
    expect(result.current).toBe("desktop")
    resize(1536)
    expect(result.current).toBe("wide")
    resize(767)
    expect(result.current).toBe("phone")
  })

  it("removes every listener on unmount", () => {
    setWidth(1024)
    const { unmount } = renderHook(() => useLayoutBreakpoint())
    expect([...listeners.values()].reduce((n, s) => n + s.size, 0)).toBe(3)
    unmount()
    expect([...listeners.values()].reduce((n, s) => n + s.size, 0)).toBe(0)
  })

  it("renders on the server without touching window, as desktop", () => {
    // The server has no viewport. Desktop is the same answer useIsMobile
    // gives there (false), so the two hooks never disagree before hydration.
    function Probe() {
      return createElement("span", null, useLayoutBreakpoint())
    }
    vi.mocked(matchMedia).mockClear()
    expect(renderToString(createElement(Probe))).toContain("desktop")
    expect(matchMedia).not.toHaveBeenCalled()
  })

  it("builds the band queries from the same edges", () => {
    expect(LAYOUT_BAND_QUERIES).toEqual({
      tablet: "(min-width: 768px) and (max-width: 1279px)",
      wide: "(min-width: 1536px)",
    })
  })

  it.each([
    [767, false, false],
    [768, true, false],
    [1279, true, false],
    [1280, false, false],
    [1536, false, true],
  ])("at %ipx: tablet %s, wide %s", (width, tablet, wide) => {
    setWidth(width)
    expect(matchesLayoutBand("tablet")).toBe(tablet)
    expect(matchesLayoutBand("wide")).toBe(wide)
  })

  it("follows a band across its edges and starts from the server answer", () => {
    setWidth(1024)
    const { result, unmount } = renderHook(() => useLayoutBand("tablet"))
    expect(result.current).toBe(true)
    resize(1280)
    expect(result.current).toBe(false)
    resize(800)
    expect(result.current).toBe(true)
    unmount()
    expect([...listeners.values()].reduce((n, s) => n + s.size, 0)).toBe(0)

    function Probe() {
      return createElement("span", null, String(useLayoutBand("wide")))
    }
    setWidth(1600)
    expect(renderToString(createElement(Probe))).toContain("false")
  })
})
