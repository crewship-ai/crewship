import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, act } from "@testing-library/react"
import { useSmoothText } from "@/hooks/use-smooth-text"

// The streaming text renderer must decouple network chunk arrival from the
// visual reveal: raw WS bursts pop whole sentences in at once, while modern
// chats (Claude.ai / ChatGPT) reveal at a smooth, roughly constant character
// rate that speeds up when a backlog builds. useSmoothText is that layer:
// input = full text-so-far + streaming flag, output = the prefix to render.
describe("useSmoothText", () => {
  // Deterministic rAF: we capture callbacks and advance time manually.
  let rafQueue: FrameRequestCallback[]
  let now: number

  beforeEach(() => {
    rafQueue = []
    now = 0
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      rafQueue.push(cb)
      return rafQueue.length
    })
    vi.stubGlobal("cancelAnimationFrame", () => {})
  })

  /** Run one animation frame, advancing the clock by ms. */
  function frame(ms = 16) {
    now += ms
    act(() => {
      const pending = rafQueue
      rafQueue = []
      for (const cb of pending) cb(now)
    })
  }

  it("returns the full text immediately when not streaming (history load)", () => {
    const { result } = renderHook(() => useSmoothText("a complete historical message", false))
    expect(result.current).toBe("a complete historical message")
  })

  it("reveals streamed text gradually, not all at once", () => {
    const full = "x".repeat(2000)
    const { result } = renderHook(({ text }) => useSmoothText(text, true), {
      initialProps: { text: full },
    })

    // First frame: some text visible, but nowhere near all of it.
    frame()
    expect(result.current.length).toBeGreaterThan(0)
    expect(result.current.length).toBeLessThan(full.length)

    // Reveal is monotonic.
    const len1 = result.current.length
    frame()
    expect(result.current.length).toBeGreaterThanOrEqual(len1)
    expect(full.startsWith(result.current)).toBe(true)
  })

  it("catches up to the full text after enough frames", () => {
    const full = "hello world, this is a streamed reply."
    const { result } = renderHook(({ text }) => useSmoothText(text, true), {
      initialProps: { text: full },
    })
    for (let i = 0; i < 300 && result.current.length < full.length; i++) frame()
    expect(result.current).toBe(full)
  })

  it("keeps revealing appended text as the stream grows", () => {
    const { result, rerender } = renderHook(({ text }) => useSmoothText(text, true), {
      initialProps: { text: "first chunk. " },
    })
    for (let i = 0; i < 100 && result.current.length < 13; i++) frame()
    expect(result.current).toBe("first chunk. ")

    rerender({ text: "first chunk. second chunk." })
    for (let i = 0; i < 100 && result.current.length < 26; i++) frame()
    expect(result.current).toBe("first chunk. second chunk.")
  })

  it("finishes the reveal after streaming ends (no truncated tail)", () => {
    const full = "the reply ends here."
    const { result, rerender } = renderHook(
      ({ text, streaming }) => useSmoothText(text, streaming),
      { initialProps: { text: full, streaming: true } },
    )
    frame()
    // Stream closes while the reveal is still behind.
    rerender({ text: full, streaming: false })
    for (let i = 0; i < 300 && result.current.length < full.length; i++) frame()
    expect(result.current).toBe(full)
  })

  it("snaps when the text is replaced rather than appended", () => {
    const { result, rerender } = renderHook(({ text }) => useSmoothText(text, true), {
      initialProps: { text: "something long that was being revealed" },
    })
    frame()
    rerender({ text: "new" })
    frame()
    expect(result.current).toBe("new")
  })

  it("resumes revealing after a replace-then-append sequence (no frozen prefix)", () => {
    // Regression: the replacement snap used to update only the internal ref;
    // a later frame could then advance to a count equal to the stale state
    // value, React would bail out of the re-render, and the reveal froze on
    // the truncated prefix forever.
    const { result, rerender } = renderHook(({ text }) => useSmoothText(text, true), {
      initialProps: { text: "hello" },
    })
    for (let i = 0; i < 20 && result.current.length < 5; i++) frame()
    expect(result.current).toBe("hello")

    rerender({ text: "new" })
    frame()
    expect(result.current).toBe("new")

    rerender({ text: "newer and longer content" })
    for (let i = 0; i < 200 && result.current.length < 24; i++) frame()
    expect(result.current).toBe("newer and longer content")
  })

  it("never splits a surrogate pair at the reveal boundary", () => {
    const full = "👋".repeat(400)
    const { result } = renderHook(({ text }) => useSmoothText(text, true), {
      initialProps: { text: full },
    })
    for (let i = 0; i < 400 && result.current.length < full.length; i++) {
      frame()
      // A lone high surrogate at the end would render as U+FFFD.
      const lastCode = result.current.charCodeAt(result.current.length - 1)
      expect(lastCode >= 0xd800 && lastCode <= 0xdbff).toBe(false)
    }
    expect(result.current).toBe(full)
  })
})
