import { describe, it, expect, vi, beforeEach } from "vitest"
import React from "react"
import { render, screen, cleanup } from "@testing-library/react"

const startAnimation = vi.fn()
const stopAnimation = vi.fn()

// motion/react pulls in runtime animation plumbing that isn't useful in unit
// tests. Replace SparklesIcon with a probe that exposes the imperative handle
// the StatusIndicator component uses via useRef.
vi.mock("@/components/ui/sparkles", () => ({
  SparklesIcon: React.forwardRef<{ startAnimation: () => void; stopAnimation: () => void }>(
    function MockSparkles(_props, ref) {
      React.useImperativeHandle(ref, () => ({ startAnimation, stopAnimation }))
      return <span data-testid="sparkles-mock" />
    },
  ),
}))

import { StatusIndicator } from "../status-indicator"

describe("StatusIndicator", () => {
  beforeEach(() => {
    startAnimation.mockReset()
    stopAnimation.mockReset()
    cleanup()
  })

  it("renders the content text", () => {
    render(<StatusIndicator content="Thinking..." />)
    expect(screen.getByText("Thinking...")).toBeTruthy()
  })

  it("exposes the status to assistive tech with polite live region", () => {
    render(<StatusIndicator content="Working" />)
    const region = screen.getByRole("status")
    expect(region.getAttribute("aria-live")).toBe("polite")
  })

  it("renders the sparkles probe icon", () => {
    render(<StatusIndicator content="Loading" />)
    expect(screen.getByTestId("sparkles-mock")).toBeTruthy()
  })

  it("kicks off the sparkles animation on mount via rAF", () => {
    const rafSpy = vi.spyOn(globalThis, "requestAnimationFrame")
    render(<StatusIndicator content="Starting" />)

    // The effect schedules the animation start through rAF; flush it.
    expect(rafSpy).toHaveBeenCalled()
    const cb = rafSpy.mock.calls[0]![0] as FrameRequestCallback
    cb(performance.now())

    expect(startAnimation).toHaveBeenCalledTimes(1)
    rafSpy.mockRestore()
  })

  it("cancels the pending rAF on unmount so animation never fires after teardown", () => {
    let pending: FrameRequestCallback | null = null
    const rafSpy = vi.spyOn(globalThis, "requestAnimationFrame").mockImplementation((cb) => {
      pending = cb
      return 42
    })
    const cancelSpy = vi.spyOn(globalThis, "cancelAnimationFrame")

    const { unmount } = render(<StatusIndicator content="x" />)
    unmount()

    expect(cancelSpy).toHaveBeenCalledWith(42)
    // Pending rAF callback must never run startAnimation if we cancel correctly
    // (we also don't flush it here, mirroring real browser behaviour).
    expect(startAnimation).not.toHaveBeenCalled()
    expect(pending).not.toBeNull() // sanity: we did capture one

    rafSpy.mockRestore()
    cancelSpy.mockRestore()
  })

  it("updates the rendered text when content prop changes", () => {
    const { rerender } = render(<StatusIndicator content="first" />)
    expect(screen.getByText("first")).toBeTruthy()

    rerender(<StatusIndicator content="second" />)
    expect(screen.queryByText("first")).toBeNull()
    expect(screen.getByText("second")).toBeTruthy()
  })
})
