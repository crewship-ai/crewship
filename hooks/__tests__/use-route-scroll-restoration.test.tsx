import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, cleanup, act } from "@testing-library/react"
import { useRef } from "react"

import { useRouteScrollRestoration } from "../use-route-scroll-restoration"

/**
 * jsdom has no layout, so `scrollTop` is whatever the test last set and
 * `scrollTo` is a no-op stub. The container here records what it was asked
 * to do, which is the only thing the hook decides.
 */
function Probe({ pathname }: { pathname: string }) {
  const ref = useRef<HTMLDivElement>(null)
  useRouteScrollRestoration(ref, pathname)
  return <div data-testid="scroller" ref={ref} />
}

let scrolledTo: number[]

beforeEach(() => {
  scrolledTo = []
  Object.defineProperty(HTMLElement.prototype, "scrollTo", {
    configurable: true,
    value: function (this: HTMLElement, opts: ScrollToOptions) {
      scrolledTo.push(opts.top ?? 0)
      this.scrollTop = opts.top ?? 0
    },
  })
  window.history.replaceState(null, "", "/a")
})
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  window.history.replaceState(null, "", "/")
})

/** What Next does on a push: a fresh state object, custom keys dropped. */
function pushRoute(path: string) {
  window.history.pushState({ __NA: true }, "", path)
}

/** What Next does on back: the popped entry's state survives, custom keys and all. */
function popTo(path: string, state: unknown) {
  window.history.replaceState(state, "", path)
}

function scrollTo(el: HTMLElement, top: number) {
  el.scrollTop = top
  el.dispatchEvent(new Event("scroll"))
}

describe("scroll restoration for a scrolling container", () => {
  it("starts a pushed route at the top", () => {
    const { getByTestId, rerender } = render(<Probe pathname="/a" />)
    scrollTo(getByTestId("scroller"), 480)

    pushRoute("/b")
    rerender(<Probe pathname="/b" />)
    expect(scrolledTo.at(-1)).toBe(0)
  })

  it("returns a popped route to where the person left it", () => {
    const { getByTestId, rerender } = render(<Probe pathname="/a" />)
    const entryA = window.history.state
    expect(entryA, "the entry was not keyed").toMatchObject({ __scrollKey: expect.any(String) })
    scrollTo(getByTestId("scroller"), 480)

    pushRoute("/b")
    rerender(<Probe pathname="/b" />)
    expect(scrolledTo.at(-1)).toBe(0)

    act(() => popTo("/a", entryA))
    rerender(<Probe pathname="/a" />)
    expect(scrolledTo.at(-1), "back did not restore the offset").toBe(480)
  })

  it("keys by the history entry, not by the route", () => {
    // Two visits to the same path are two entries. The second is a push and
    // starts at the top even though the first is remembered.
    const { getByTestId, rerender } = render(<Probe pathname="/a" />)
    scrollTo(getByTestId("scroller"), 300)

    pushRoute("/b")
    rerender(<Probe pathname="/b" />)
    pushRoute("/a")
    rerender(<Probe pathname="/a" />)
    expect(scrolledTo.at(-1)).toBe(0)
  })

  it("keeps the router's own state when it keys an entry", () => {
    window.history.replaceState({ __NA: true, __PRIVATE_NEXTJS_INTERNALS_TREE: "tree" }, "", "/a")
    render(<Probe pathname="/a" />)
    expect(window.history.state).toMatchObject({
      __NA: true,
      __PRIVATE_NEXTJS_INTERNALS_TREE: "tree",
      __scrollKey: expect.any(String),
    })
  })

  it("is not fooled by an overlay's own entry popping", () => {
    // A sheet pushes an entry on open and pops it on close, without the route
    // changing. A flag set on popstate would still be up for the next real
    // push; keying by entry ignores the pop entirely.
    const { getByTestId, rerender } = render(<Probe pathname="/a" />)
    scrollTo(getByTestId("scroller"), 200)
    window.history.pushState({ ...window.history.state, __overlay: true }, "")
    act(() => window.dispatchEvent(new PopStateEvent("popstate")))

    pushRoute("/b")
    rerender(<Probe pathname="/b" />)
    expect(scrolledTo.at(-1)).toBe(0)
  })
})
