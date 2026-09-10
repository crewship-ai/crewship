import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, cleanup, act } from "@testing-library/react"

import { useOverlayBackButton } from "../use-overlay-back-button"

function Probe({ enabled = true }: { enabled?: boolean }) {
  useOverlayBackButton(enabled)
  return null
}

let pushSpy: ReturnType<typeof vi.spyOn>
let backSpy: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  pushSpy = vi.spyOn(window.history, "pushState")
  backSpy = vi.spyOn(window.history, "back").mockImplementation(() => {})
})
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe("back closes an overlay instead of leaving the page", () => {
  it("puts an entry on the stack while the overlay is mounted", () => {
    render(<Probe />)
    expect(pushSpy).toHaveBeenCalledTimes(1)
    expect(pushSpy.mock.calls[0][0]).toMatchObject({ __overlay: true })
  })

  it("keeps the router's own state, which the App Router needs to find its place", () => {
    window.history.replaceState({ __NA: "router-key" }, "")
    render(<Probe />)
    expect(pushSpy.mock.calls[0][0]).toMatchObject({ __NA: "router-key", __overlay: true })
  })

  it("asks Radix to dismiss when the entry is popped", () => {
    const seen: string[] = []
    const listener = (e: Event) => seen.push((e as KeyboardEvent).key)
    document.addEventListener("keydown", listener)
    render(<Probe />)
    act(() => { window.dispatchEvent(new PopStateEvent("popstate")) })
    document.removeEventListener("keydown", listener)
    // Dismissal goes through Radix's own path, so a dialog that blocks Escape
    // blocks back for the same reason rather than the two disagreeing.
    expect(seen).toContain("Escape")
  })

  it("never rewinds history itself, even when its own entry is on top", () => {
    // Tidying up the marker on close races pages that own their history — chat
    // writes the selected conversation with replaceState and re-reads it on
    // popstate — and rewound a conversation switch when it was tried. The
    // leftover entry is a no-op back press; undoing someone else's navigation
    // is not.
    window.history.replaceState({ __overlay: true }, "")
    const { unmount } = render(<Probe />)
    unmount()
    expect(backSpy).not.toHaveBeenCalled()
  })

  it("does not touch history when it is switched off", () => {
    render(<Probe enabled={false} />)
    expect(pushSpy).not.toHaveBeenCalled()
  })

  it("pops only once, so a second back reaches the page behind", () => {
    const seen: string[] = []
    const listener = (e: Event) => seen.push((e as KeyboardEvent).key)
    document.addEventListener("keydown", listener)
    render(<Probe />)
    act(() => { window.dispatchEvent(new PopStateEvent("popstate")) })
    act(() => { window.dispatchEvent(new PopStateEvent("popstate")) })
    document.removeEventListener("keydown", listener)
    expect(seen.filter((k) => k === "Escape")).toHaveLength(1)
  })
})
