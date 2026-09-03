import { describe, it, expect, vi, beforeEach } from "vitest"
import { act, renderHook } from "@testing-library/react"

// useUrlSelection is what makes a selection shareable: the value lives in the
// query string, a pick is a history entry (Back closes it), and older links
// that spell the parameter differently are read as aliases and rewritten.
//
// /routines is the screen that needed this: it read ?slug= once at mount and
// then kept the selection in component state, so a reload lost it, Back left
// the page, and the dashboard's "Up next" link — which says ?routine= — landed
// on the unfiltered overview.

let searchParams = new URLSearchParams()
vi.mock("next/navigation", () => ({
  useSearchParams: () => searchParams,
}))

import { readUrlSelection, useUrlSelection } from "@/hooks/use-issue-detail"

function setLocation(query: string) {
  window.history.replaceState(null, "", `/routines${query}`)
  searchParams = new URLSearchParams(query)
}

describe("readUrlSelection", () => {
  it("reads the key first and an alias only when the key is absent", () => {
    expect(readUrlSelection(new URLSearchParams("slug=a&routine=b"), "slug", ["routine"])).toBe("a")
    expect(readUrlSelection(new URLSearchParams("routine=b"), "slug", ["routine"])).toBe("b")
    expect(readUrlSelection(new URLSearchParams(""), "slug", ["routine"])).toBeNull()
  })
})

describe("useUrlSelection", () => {
  beforeEach(() => setLocation(""))

  it("arrives selected through the alias and rewrites it to the key on the first pick", () => {
    setLocation("?routine=page-watch")
    const { result } = renderHook(() => useUrlSelection("slug", { aliases: ["routine"] }))
    expect(result.current[0]).toBe("page-watch")

    act(() => result.current[1]("classify-ticket"))
    expect(result.current[0]).toBe("classify-ticket")
    expect(window.location.search).toBe("?slug=classify-ticket")
  })

  it("pushes a history entry so Back closes the selection", () => {
    const { result } = renderHook(() => useUrlSelection("slug"))
    const before = window.history.length
    act(() => result.current[1]("page-watch"))
    expect(window.location.search).toBe("?slug=page-watch")
    expect(window.history.length).toBe(before + 1)

    // The browser going back is a popstate the hook listens to.
    act(() => {
      window.history.replaceState(null, "", "/routines")
      window.dispatchEvent(new PopStateEvent("popstate"))
    })
    expect(result.current[0]).toBeNull()
  })

  it("clearing removes the key and leaves the other parameters alone", () => {
    setLocation("?slug=page-watch&tab=runs")
    const { result } = renderHook(() => useUrlSelection("slug"))
    act(() => result.current[1](null))
    expect(window.location.search).toBe("?tab=runs")
    expect(result.current[0]).toBeNull()
  })
})
