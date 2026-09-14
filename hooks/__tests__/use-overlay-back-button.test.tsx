import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, cleanup, act } from "@testing-library/react"

import { useOverlayBackButton } from "../use-overlay-back-button"

function Probe({ enabled = true, stillOpen }: { enabled?: boolean; stillOpen?: () => boolean }) {
  useOverlayBackButton(enabled, stillOpen)
  return null
}

const back = () => act(() => { window.dispatchEvent(new PopStateEvent("popstate")) })

/** Lets the hook's post-Escape settle timer (a 0ms task) run. */
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 5)) })

function recordKeys() {
  const seen: string[] = []
  const listener = (e: Event) => seen.push((e as KeyboardEvent).key)
  document.addEventListener("keydown", listener)
  return {
    escapes: () => seen.filter((k) => k === "Escape").length,
    stop: () => document.removeEventListener("keydown", listener),
  }
}

let pushSpy: ReturnType<typeof vi.spyOn>
let backSpy: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  pushSpy = vi.spyOn(window.history, "pushState")
  // A real `back()` fires popstate in a later task, which would land in
  // whichever test runs next. Firing it here, synchronously, keeps each
  // test's traversal inside that test — and it is what the nested-sheet case
  // needs to observe at all.
  backSpy = vi.spyOn(window.history, "back").mockImplementation(() => {
    window.dispatchEvent(new PopStateEvent("popstate"))
  })
})
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  // The hook marks the current entry; with `back` mocked nothing unmarks it,
  // and the next test would start inside an overlay it never opened.
  window.history.replaceState(null, "", "/")
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

  it("keeps its entry when the overlay refuses to close", async () => {
    // Radix prevents Escape's default whether it dismisses or is told not
    // to, so the hook reads the overlay's own answer instead. An
    // unsaved-changes guard that blocks Escape must block back the same way:
    // the entry goes back on the stack, and the next back asks again rather
    // than navigating underneath a sheet that is still showing.
    const keys = recordKeys()
    render(<Probe stillOpen={() => true} />)
    expect(pushSpy).toHaveBeenCalledTimes(1)

    back()
    await settle()
    expect(pushSpy, "entry was not restored after a refused Escape").toHaveBeenCalledTimes(2)
    expect(pushSpy.mock.calls[1][0]).toMatchObject({ __overlay: true })

    back()
    await settle()
    keys.stop()
    expect(keys.escapes(), "second back did not ask the overlay again").toBe(2)
  })

  it("lets its entry go once the overlay is actually closing", async () => {
    const keys = recordKeys()
    render(<Probe stillOpen={() => false} />)
    back()
    await settle()
    expect(pushSpy, "entry was restored for an overlay that closed").toHaveBeenCalledTimes(1)

    // A second back during the exit animation belongs to the page.
    back()
    await settle()
    keys.stop()
    expect(keys.escapes()).toBe(1)
  })

  it("does not answer the popstate its own unwind produces", () => {
    // Closing an inner sheet by its × unwinds the inner entry with
    // history.back(). That fires popstate like any other, and the outer
    // sheet — now innermost — took it for a back press and closed too.
    window.history.replaceState({ __overlay: true }, "")
    const keys = recordKeys()
    const outer = render(<Probe />)
    const inner = render(<Probe />)

    inner.unmount()
    expect(backSpy, "inner sheet did not unwind its entry").toHaveBeenCalledTimes(1)
    expect(keys.escapes(), "outer sheet answered the unwind").toBe(0)

    // The next real back press does reach the outer sheet.
    back()
    keys.stop()
    expect(keys.escapes()).toBe(1)
    outer.unmount()
  })
})
