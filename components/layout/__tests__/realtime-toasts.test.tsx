import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, cleanup } from "@testing-library/react"

// #890 — RealtimeToasts redirects a tab out of a workspace that was deleted
// (workspace.deleted → window.location.href = "/"). This is the only new
// runtime behaviour in the PR, so it gets a focused test.

// Capture the callback registered for each realtime event type.
const handlers: Record<string, (e: { payload: Record<string, unknown> }) => void> = {}
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: (type: string, cb: (e: { payload: Record<string, unknown> }) => void) => {
    handlers[type] = cb
  },
}))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-current" }),
}))
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: { error: (...a: unknown[]) => toastError(...a), warning: vi.fn(), success: vi.fn() },
}))

import { RealtimeToasts } from "../realtime-toasts"

function fireWorkspaceDeleted(id: string) {
  handlers["workspace.deleted"]({ payload: { id } })
}

describe("RealtimeToasts — workspace.deleted redirect (#890)", () => {
  let hrefSetTo: string | null

  beforeEach(() => {
    vi.useFakeTimers()
    toastError.mockReset()
    hrefSetTo = null
    // Replace window.location with a plain object so we can observe href
    // assignment without happy-dom attempting a real navigation.
    Object.defineProperty(window, "location", {
      configurable: true,
      value: {
        get href() { return "http://localhost/" },
        set href(v: string) { hrefSetTo = v },
      },
    })
    render(<RealtimeToasts />)
  })

  afterEach(() => {
    vi.runOnlyPendingTimers()
    vi.useRealTimers()
    cleanup()
  })

  it("redirects to / when the CURRENT workspace is deleted", () => {
    fireWorkspaceDeleted("ws-current")
    expect(toastError).toHaveBeenCalledTimes(1)
    // Redirect is deferred so the toast is visible; advance past the delay.
    expect(hrefSetTo).toBeNull()
    vi.advanceTimersByTime(1300)
    expect(hrefSetTo).toBe("/")
  })

  it("ignores a delete for a DIFFERENT workspace", () => {
    fireWorkspaceDeleted("some-other-ws")
    vi.advanceTimersByTime(1300)
    expect(toastError).not.toHaveBeenCalled()
    expect(hrefSetTo).toBeNull()
  })
})
