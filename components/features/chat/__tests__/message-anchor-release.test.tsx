import { cleanup, render } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { MESSAGE_ANCHOR_EVENT, MessageAnchorRelease } from "../message-anchor-release"

const stopScroll = vi.hoisted(() => vi.fn())
vi.mock("use-stick-to-bottom", () => ({ useStickToBottomContext: () => ({ stopScroll }) }))
afterEach(() => { cleanup(); stopScroll.mockReset() })

it("lets go of the stick-to-bottom lock (and its running scroll) when a message is revealed", () => {
  render(<MessageAnchorRelease />)
  window.dispatchEvent(new Event(MESSAGE_ANCHOR_EVENT))
  expect(stopScroll).toHaveBeenCalledTimes(1)
})

it("stops listening when unmounted", () => {
  const { unmount } = render(<MessageAnchorRelease />)
  unmount()
  window.dispatchEvent(new Event(MESSAGE_ANCHOR_EVENT))
  expect(stopScroll).not.toHaveBeenCalled()
})
