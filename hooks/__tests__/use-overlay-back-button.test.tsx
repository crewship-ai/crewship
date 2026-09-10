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

  it("unwinds its own entry when the overlay closes by its own button", () => {
    // Otherwise opening and closing a drawer five times costs five back
    // presses before the page will leave.
    window.history.replaceState({ __overlay: true }, "")
    const { unmount } = render(<Probe />)
    unmount()
    expect(backSpy).toHaveBeenCalledTimes(1)
  })

  it("leaves history alone when something inside the overlay navigated", () => {
    // The router has pushed over our marker; going back here would undo that
    // navigation rather than closing anything. Chat writes its selected
    // conversation with replaceState and re-reads it on popstate.
    const { unmount } = render(<Probe />)
    window.history.replaceState({ __NA: "somewhere-else" }, "")
    unmount()
    expect(backSpy).not.toHaveBeenCalled()
  })

  it("answers one back press with one overlay, the innermost", () => {
    // One popstate reaches every listener on window. When each overlay answered
    // for itself, a sheet opened from inside a sheet dismissed both at once:
    // Radix closes only the top layer for a synchronous Escape burst, so the
    // outer one stayed open having already spent its history entry.
    const seen: string[] = []
    const listener = (e: Event) => seen.push((e as KeyboardEvent).key)
    document.addEventListener("keydown", listener)
    render(<><Probe /><Probe /></>)
    act(() => { window.dispatchEvent(new PopStateEvent("popstate")) })
    document.removeEventListener("keydown", listener)
    expect(seen.filter((k) => k === "Escape"), "both layers answered one back").toHaveLength(1)
  })

  it("stays out of history until the sheet is actually open", async () => {
    // The hook used to be called in SheetContent's body, which mounts as soon
    // as its parent renders it — Radix's Portal returns null inside the tree
    // it returns, so the body and its effects run for a closed sheet too. The
    // toolbar and the sidebar each keep one mounted on every dashboard page,
    // so this pushed two dead entries per page load and back stopped working
    // entirely: the top closed sheet swallowed the press with a no-op Escape.
    const { Sheet, SheetContent, SheetHeader, SheetTitle } = await import("@/components/ui/sheet")
    render(
      <Sheet open={false}>
        <SheetContent>
          <SheetHeader><SheetTitle>closed</SheetTitle></SheetHeader>
        </SheetContent>
      </Sheet>,
    )
    expect(
      pushSpy.mock.calls.length,
      `a closed sheet pushed ${pushSpy.mock.calls.length} history entries`,
    ).toBe(0)
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
