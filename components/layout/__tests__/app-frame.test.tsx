import { describe, it, expect, vi, afterEach } from "vitest"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"

import { AppFrame } from "../app-frame"

afterEach(() => { cleanup(); vi.useRealTimers() })

describe("AppFrame", () => {
  it("is plain ground for a second, then rail, top bar and phone tabs as skeletons", () => {
    vi.useFakeTimers()
    const { container } = render(<AppFrame />)
    expect(container.querySelector(".app-frame-side")).toBeNull()
    act(() => { vi.advanceTimersByTime(1000) })
    expect(container.querySelectorAll(".app-frame-row")).toHaveLength(8)
    expect(container.querySelector(".app-frame-crumb")).not.toBeNull()
    expect(container.querySelectorAll(".app-frame-tab")).toHaveLength(5)
  })

  it("an error shows at once, also when the frame was still in its quiet second", () => {
    vi.useFakeTimers()
    const retry = vi.fn(), signOut = vi.fn()
    const { rerender } = render(<AppFrame />)
    rerender(<AppFrame state="error" onRetry={retry} onSignOut={signOut} />)
    expect(screen.getByRole("alert")).toHaveTextContent("Your workspaces could not load")
    // No navigation rows: they depend on the workspace that failed to load.
    expect(document.querySelector(".app-frame-row")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))
    fireEvent.click(screen.getByRole("button", { name: "Log out" }))
    expect(retry).toHaveBeenCalledOnce()
    expect(signOut).toHaveBeenCalledOnce()
  })
})
