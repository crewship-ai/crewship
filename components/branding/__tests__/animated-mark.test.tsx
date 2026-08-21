import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen } from "@testing-library/react"
import { AnimatedMark } from "@/components/branding/animated-mark"
import { MARK_SAILS } from "@/lib/brand-mark"

// Reduced motion is a branch, not a stylesheet, so it needs to be steerable
// per test. vi.mock is hoisted above the imports, hence vi.hoisted for the
// flag it closes over.
const motionState = vi.hoisted(() => ({ reduced: false }))
vi.mock("motion/react", () => ({
  useReducedMotion: () => motionState.reduced,
}))

// The mark sits on the login screen, which is the one page every user and
// every e2e run passes through. A canvas that throws on mount takes the
// whole route with it, so the properties worth pinning are about survival:
// no 2D context, no DOMMatrix, no ResizeObserver quirks, no leaked loop.

let rafSpy: ReturnType<typeof vi.spyOn> | undefined
let cancelSpy: ReturnType<typeof vi.spyOn> | undefined

beforeEach(() => {
  motionState.reduced = false
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
})

afterEach(() => {
  rafSpy?.mockRestore()
  cancelSpy?.mockRestore()
  rafSpy = undefined
  cancelSpy = undefined
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("AnimatedMark", () => {
  it("renders a decorative canvas that assistive tech skips", () => {
    render(<AnimatedMark />)
    const canvas = screen.getByTestId("animated-mark")
    expect(canvas.tagName).toBe("CANVAS")
    expect(canvas).toHaveAttribute("aria-hidden", "true")
  })

  it("mounts without a 2D context instead of throwing", () => {
    // happy-dom gives back null here, which is exactly the degraded case
    // the component has to survive — the shell's CSS gradient still paints.
    expect(HTMLCanvasElement.prototype.getContext.call(
      document.createElement("canvas"),
      "2d"
    )).toBeNull()
    expect(() => render(<AnimatedMark />)).not.toThrow()
  })

  it("does not start an animation loop when it cannot draw", () => {
    rafSpy = vi.spyOn(window, "requestAnimationFrame")
    render(<AnimatedMark />)
    expect(rafSpy).not.toHaveBeenCalled()
  })

  it("accepts every motion variant", () => {
    for (const variant of ["swell", "assemble", "drift"] as const) {
      const { unmount } = render(<AnimatedMark variant={variant} />)
      expect(screen.getByTestId("animated-mark")).toBeInTheDocument()
      unmount()
    }
  })

  it("takes a replayKey without remounting the canvas", () => {
    const { rerender } = render(<AnimatedMark replayKey={0} />)
    const first = screen.getByTestId("animated-mark")
    rerender(<AnimatedMark replayKey={1} />)
    expect(screen.getByTestId("animated-mark")).toBe(first)
  })

  it("cancels its frame and detaches listeners on unmount", () => {
    // With a stubbed 2D context the loop does start, so unmount has real
    // teardown to do. A leaked RAF on a route the user logs away from is
    // the kind of thing that only shows up as a warm laptop.
    const ctx = fakeContext()
    const getContext = vi
      .spyOn(HTMLCanvasElement.prototype, "getContext")
      .mockReturnValue(ctx as unknown as CanvasRenderingContext2D)
    vi.stubGlobal("DOMMatrix", FakeMatrix)
    vi.stubGlobal("Path2D", class { addPath() {} })
    rafSpy = vi.spyOn(window, "requestAnimationFrame").mockReturnValue(42 as never)
    cancelSpy = vi.spyOn(window, "cancelAnimationFrame")
    const removeDoc = vi.spyOn(document, "removeEventListener")

    const { unmount } = render(<AnimatedMark />)
    expect(rafSpy).toHaveBeenCalled()

    unmount()
    expect(cancelSpy).toHaveBeenCalledWith(42)
    expect(removeDoc).toHaveBeenCalledWith("visibilitychange", expect.any(Function))

    getContext.mockRestore()
    removeDoc.mockRestore()
  })

  it("paints one settled frame and starts no loop under reduced motion", () => {
    motionState.reduced = true
    const ctx = fakeContext()
    const getContext = vi
      .spyOn(HTMLCanvasElement.prototype, "getContext")
      .mockReturnValue(ctx as unknown as CanvasRenderingContext2D)
    vi.stubGlobal("DOMMatrix", FakeMatrix)
    vi.stubGlobal("Path2D", FakePath)
    rafSpy = vi.spyOn(window, "requestAnimationFrame")

    render(<AnimatedMark />)

    // The mark is drawn — a frozen panel is not an empty one — but nothing
    // is scheduled, so a user who asked for stillness gets it.
    expect(ctx.fill).toHaveBeenCalled()
    expect(rafSpy).not.toHaveBeenCalled()

    getContext.mockRestore()
  })

  it("draws every sail the split produced, not a hardcoded three", () => {
    const ctx = fakeContext()
    const getContext = vi
      .spyOn(HTMLCanvasElement.prototype, "getContext")
      .mockReturnValue(ctx as unknown as CanvasRenderingContext2D)
    vi.stubGlobal("DOMMatrix", FakeMatrix)
    vi.stubGlobal("Path2D", FakePath)
    // Pump exactly one frame. Calling the callback unconditionally would
    // recurse until the stack gives out, since the loop re-schedules itself.
    let pumped = false
    rafSpy = vi.spyOn(window, "requestAnimationFrame").mockImplementation((cb) => {
      if (!pumped) {
        pumped = true
        cb(0)
      }
      return 1 as never
    })

    render(<AnimatedMark />)
    // One fill per sail, plus the bloom.
    expect(ctx.fill.mock.calls.length).toBe(MARK_SAILS.length)
    expect(ctx.fillRect).toHaveBeenCalled()

    getContext.mockRestore()
  })
})

class FakePath {
  addPath() {}
}

class FakeMatrix {
  translateSelf() {
    return this
  }
  scaleSelf() {
    return this
  }
  rotateSelf() {
    return this
  }
  multiply() {
    return this
  }
}

function fakeContext() {
  const gradient = { addColorStop: vi.fn() }
  return {
    setTransform: vi.fn(),
    clearRect: vi.fn(),
    fillRect: vi.fn(),
    fill: vi.fn(),
    save: vi.fn(),
    restore: vi.fn(),
    clip: vi.fn(),
    createRadialGradient: vi.fn(() => gradient),
    createLinearGradient: vi.fn(() => gradient),
    globalAlpha: 1,
    fillStyle: "",
  }
}
