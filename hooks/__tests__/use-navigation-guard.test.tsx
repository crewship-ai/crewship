import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, render, renderHook } from "@testing-library/react"
import * as React from "react"

const push = vi.fn()
vi.mock("next/navigation", () => ({ useRouter: () => ({ push, replace: vi.fn(), prefetch: vi.fn(), back: vi.fn() }) }))

import {
  useGuardedRouter,
  interceptableHref,
  navigationAllowed,
  registerNavigationGuard,
  useNavigationGuard,
  type NavigationGuard,
} from "@/hooks/use-navigation-guard"

/**
 * The counter-review's R3: the editor guarded its own address, Back, reload and
 * the workspace switcher, and every other link in the application walked
 * straight past it. The global sidebar is plain `next/link`, so clicking
 * Routines is a client-side navigation — not a reload, not a popstate — and the
 * editor unmounted with the half-typed form inside it.
 *
 * These tests pin both halves: the question is asked for an in-app link, and it
 * is *not* asked for the clicks a person means as "take me out of here".
 */

/**
 * Dispatches a click and reports what the interceptor made of it.
 *
 * The verdict is taken *during* dispatch, the way the real listener sees it:
 * an event's `target` does not outlive its dispatch here, so asking afterwards
 * answers null for every click and would have made this whole file pass
 * vacuously.
 */
function clickLink(attrs: Record<string, string>, init: MouseEventInit = {}) {
  const a = document.createElement("a")
  for (const [k, v] of Object.entries(attrs)) a.setAttribute(k, v)
  a.textContent = "go"
  document.body.appendChild(a)
  let verdict: string | null = null
  const probe = (e: Event) => {
    verdict = interceptableHref(e as MouseEvent)
  }
  document.addEventListener("click", probe, true)
  const event = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0, ...init })
  a.dispatchEvent(event)
  document.removeEventListener("click", probe, true)
  a.remove()
  return { event, verdict: verdict as string | null }
}

beforeEach(() => {
  push.mockReset()
  window.history.replaceState(null, "", "/pages/operations-lab?mode=edit")
})
afterEach(cleanup)

describe("interceptableHref", () => {
  it("claims an ordinary in-app link", () => {
    expect(clickLink({ href: "/routines" }).verdict).toBe("/routines")
  })

  const ignored: Array<{ name: string; attrs: Record<string, string>; init?: MouseEventInit }> = [
    { name: "a new tab", attrs: { href: "/routines", target: "_blank" } },
    { name: "a download", attrs: { href: "/export.yaml", download: "" } },
    { name: "another origin", attrs: { href: "https://example.invalid/x" } },
    { name: "an in-page anchor", attrs: { href: "#section" } },
    { name: "a command-click", attrs: { href: "/routines" }, init: { metaKey: true } },
    { name: "a control-click", attrs: { href: "/routines" }, init: { ctrlKey: true } },
    { name: "a middle click", attrs: { href: "/routines" }, init: { button: 1 } },
    { name: "the address already open", attrs: { href: "/pages/operations-lab?mode=edit" } },
  ]
  for (const c of ignored) {
    it(`leaves ${c.name} alone`, () => {
      expect(clickLink(c.attrs, c.init).verdict).toBeNull()
    })
  }

  it("leaves a click something else already handled alone", () => {
    const a = document.createElement("a")
    a.setAttribute("href", "/routines")
    document.body.appendChild(a)
    // Prevented in the capture phase, so it is already handled by the time the
    // interceptor is asked.
    a.addEventListener("click", (e) => e.preventDefault(), true)
    let verdict: string | null = "unset"
    const probe = (e: Event) => {
      verdict = interceptableHref(e as MouseEvent)
    }
    document.addEventListener("click", probe)
    a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }))
    document.removeEventListener("click", probe)
    a.remove()
    expect(verdict).toBeNull()
  })
})

describe("navigationAllowed", () => {
  it("is true with nothing registered", () => {
    expect(navigationAllowed(vi.fn())).toBe(true)
  })

  it("treats a guard that throws as allowing, so one broken guard cannot wedge the app", () => {
    const off = registerNavigationGuard(() => {
      throw new Error("boom")
    })
    expect(navigationAllowed(vi.fn())).toBe(true)
    off()
  })

  it("hands the refusing guard the retry that performs what it refused", () => {
    const retry = vi.fn()
    let captured: (() => void) | null = null
    const off = registerNavigationGuard((r) => {
      captured = r
      return false
    })
    expect(navigationAllowed(retry)).toBe(false)
    expect(captured).not.toBeNull()
    captured!()
    expect(retry).toHaveBeenCalledTimes(1)
    off()
  })
})

describe("useGuardedRouter over programmatic navigation", () => {
  // The capture listener only ever sees anchors. The dashboard chrome the
  // editor lives inside navigates without one — the command palette, the
  // activity bell and the inbox bell all call `router.push` — so closing R3
  // for links alone left the same hole one layer further in.
  it("asks before a push, and does not navigate when refused", () => {
    const off = registerNavigationGuard(() => false)
    const { result } = renderHook(() => useGuardedRouter())
    act(() => result.current.push("/routines"))
    expect(push).not.toHaveBeenCalled()
    off()
  })

  it("performs the push once the guard's retry is called", () => {
    let retry: (() => void) | null = null
    const off = registerNavigationGuard(r => {
      retry = r
      return false
    })
    const { result } = renderHook(() => useGuardedRouter())
    act(() => result.current.push("/routines"))
    expect(push).not.toHaveBeenCalled()
    act(() => retry!())
    expect(push).toHaveBeenCalledWith("/routines")
    off()
  })

  it("pushes straight through when nothing is guarding", () => {
    const { result } = renderHook(() => useGuardedRouter())
    act(() => result.current.push("/routines"))
    expect(push).toHaveBeenCalledWith("/routines")
  })
})

describe("useNavigationGuard over application links", () => {
  function arm(guard: NavigationGuard, enabled = true) {
    return renderHook(({ g, e }: { g: NavigationGuard; e: boolean }) => useNavigationGuard(g, e), {
      initialProps: { g: guard, e: enabled },
    })
  }

  it("stops an in-app link when the guard refuses, and does not navigate", () => {
    arm(() => false)
    expect(clickLink({ href: "/routines" }).event.defaultPrevented).toBe(true)
    expect(push).not.toHaveBeenCalled()
  })

  it("performs the refused navigation once the guard's retry is called", () => {
    let retry: (() => void) | null = null
    arm((r) => {
      retry = r
      return false
    })
    clickLink({ href: "/routines" })
    expect(push).not.toHaveBeenCalled()
    retry!()
    expect(push).toHaveBeenCalledWith("/routines")
  })

  it("lets the link through when the guard allows", () => {
    arm(() => true)
    expect(clickLink({ href: "/routines" }).event.defaultPrevented).toBe(false)
    // Next's own handler navigates; this module does not push in that case.
    expect(push).not.toHaveBeenCalled()
  })

  it("never claims a new tab, even while refusing everything", () => {
    arm(() => false)
    expect(clickLink({ href: "/routines", target: "_blank" }).event.defaultPrevented).toBe(false)
  })

  it("stops asking once the guard is disarmed", () => {
    const { rerender } = arm(() => false)
    expect(clickLink({ href: "/routines" }).event.defaultPrevented).toBe(true)
    rerender({ g: () => false, e: false })
    expect(clickLink({ href: "/routines" }).event.defaultPrevented).toBe(false)
  })

  it("stops asking once the component unmounts", () => {
    const { unmount } = arm(() => false)
    expect(clickLink({ href: "/routines" }).event.defaultPrevented).toBe(true)
    unmount()
    expect(clickLink({ href: "/routines" }).event.defaultPrevented).toBe(false)
  })

  it("asks before Next's own click handler gets to start the navigation", () => {
    // Capture phase is the whole mechanism: a bubble-phase listener would run
    // after the framework had already begun, and preventDefault would be a
    // promise kept too late.
    const inner = vi.fn()
    arm(() => false)
    function Nav() {
      return (
        <a href="/routines" onClick={inner}>
          Routines
        </a>
      )
    }
    const { container } = render(<Nav />)
    act(() => {
      container.querySelector("a")!.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }))
    })
    expect(inner).not.toHaveBeenCalled()
  })
})
