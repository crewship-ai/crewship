import { cleanup, render } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { MESSAGE_ANCHOR_EVENT } from "@/hooks/use-message-anchor"
import { MessageAnchorRelease } from "../message-anchor-release"

const ctx = vi.hoisted(() => ({ stopScroll: vi.fn(), scrollToBottom: vi.fn() }))
vi.mock("use-stick-to-bottom", () => ({ useStickToBottomContext: () => ctx }))
const motion = vi.hoisted(() => ({ reduce: false }))
vi.mock("motion/react", async (importOriginal) => ({ ...(await importOriginal<typeof import("motion/react")>()), useReducedMotionConfig: () => motion.reduce }))
afterEach(() => { motion.reduce = false; cleanup(); ctx.stopScroll.mockReset(); ctx.scrollToBottom.mockReset() })

const anchor = (sessionId: string) => window.dispatchEvent(new CustomEvent(MESSAGE_ANCHOR_EVENT, { detail: { sessionId } }))

it("lets go of the stick-to-bottom lock when a message of this session is revealed", () => {
  render(<MessageAnchorRelease sessionId="s1" sendNonce={0} />)
  anchor("s1")
  expect(ctx.stopScroll).toHaveBeenCalledTimes(1)
})

it("ignores an anchor for another session and stops listening when unmounted", () => {
  const { unmount } = render(<MessageAnchorRelease sessionId="s1" sendNonce={0} />)
  anchor("other")
  expect(ctx.stopScroll).not.toHaveBeenCalled()
  unmount()
  anchor("s1")
  expect(ctx.stopScroll).not.toHaveBeenCalled()
})

it("release, then the reader sends: the lock is re-armed so the reply is followed", () => {
  const { rerender } = render(<MessageAnchorRelease sessionId="s1" sendNonce={3} />)
  expect(ctx.scrollToBottom).not.toHaveBeenCalled()
  anchor("s1")
  rerender(<MessageAnchorRelease sessionId="s1" sendNonce={4} />)
  expect(ctx.scrollToBottom).toHaveBeenCalledTimes(1)
  rerender(<MessageAnchorRelease sessionId="s1" sendNonce={4} />)
  expect(ctx.scrollToBottom).toHaveBeenCalledTimes(1)
})

it("with reduced motion the re-armed follow jumps instead of animating", () => {
  motion.reduce = true
  const { rerender } = render(<MessageAnchorRelease sessionId="s1" sendNonce={1} />)
  rerender(<MessageAnchorRelease sessionId="s1" sendNonce={2} />)
  expect(ctx.scrollToBottom).toHaveBeenCalledWith("instant")
})
