import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, act, cleanup } from "@testing-library/react"

// =============================================================================
// The auto-close is the counterpart of the auto-open, and must not outlive it.
//
// `Reasoning` opens itself while the model is thinking and closes itself a
// second after the stream ends — one shot, tracked by `hasAutoClosed`. The
// chat surface passes `defaultOpen={false}` so the block never opens itself at
// all, because a wall of chain of thought in the middle of a transcript is not
// what the reader came for.
//
// Opting out of the auto-open left the one-shot budget UNSPENT: the block was
// never open while streaming, so the closing effect never fired, and
// `hasAutoClosed` was still false when the stream ended. The reader's own
// first click then set `isOpen`, re-fired that effect, and collapsed the block
// a second later — in front of them, on the one thing they had just asked to
// see. Only the second click stuck.
//
// `hasEverStreamedRef` is seeded at MOUNT, so this only ever bit a turn whose
// reasoning streamed in front of you — which is every live turn, and no turn
// in a reloaded transcript. That is why it survived manual testing.
// =============================================================================

import { Reasoning, ReasoningTrigger } from "../reasoning"

function block(props: { isStreaming: boolean; defaultOpen?: boolean }) {
  return (
    <Reasoning isStreaming={props.isStreaming} defaultOpen={props.defaultOpen}>
      <ReasoningTrigger />
    </Reasoning>
  )
}

/** The trigger carries Radix's `aria-expanded`, which is the open state. */
function isOpen() {
  return screen.getByRole("button").getAttribute("aria-expanded") === "true"
}

/**
 * Advance past the auto-close delay INSIDE `act`.
 *
 * Advancing outside it leaves the resulting `setState` unflushed, so the
 * assertion reads the pre-timeout render and a broken component passes. That
 * is the failure mode this repo has hit before; the mutation check at the
 * bottom of the commit message is what proves it is not happening here.
 */
function runAutoCloseWindow() {
  act(() => {
    vi.advanceTimersByTime(1500)
  })
}

beforeEach(() => vi.useFakeTimers())
afterEach(() => {
  vi.useRealTimers()
  cleanup()
})

describe("<Reasoning> — a block the reader opened stays open", () => {
  it("does not auto-close after a manual open when defaultOpen is false", () => {
    const { rerender } = render(block({ isStreaming: true, defaultOpen: false }))
    // Opted out of the auto-open: still closed while the model thinks.
    expect(isOpen()).toBe(false)

    rerender(block({ isStreaming: false, defaultOpen: false }))
    expect(isOpen()).toBe(false)

    fireEvent.click(screen.getByRole("button"))
    expect(isOpen()).toBe(true)

    runAutoCloseWindow()
    expect(isOpen()).toBe(true)
  })

  it("still auto-closes the block it opened itself", () => {
    // The control. The one-shot close is not being removed — it is being kept
    // paired with the open it undoes, and this is the pairing.
    const { rerender } = render(block({ isStreaming: true }))
    expect(isOpen()).toBe(true)

    rerender(block({ isStreaming: false }))
    runAutoCloseWindow()
    expect(isOpen()).toBe(false)
  })

  it("lets the reader re-open a block it auto-closed, and leaves it open", () => {
    const { rerender } = render(block({ isStreaming: true }))
    rerender(block({ isStreaming: false }))
    runAutoCloseWindow()
    expect(isOpen()).toBe(false)

    fireEvent.click(screen.getByRole("button"))
    runAutoCloseWindow()
    expect(isOpen()).toBe(true)
  })
})
