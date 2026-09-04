import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, renderHook, act } from "@testing-library/react"
import { EmptyState, useRetry } from "@/components/features/crews/bottom-panel/shared"

// README §6: every error offers a retry. The dock's thirteen tabs shared one
// "Failed to load: …" string and not one of them had a way back.
describe("dock EmptyState", () => {
  it("offers Retry only when given something to retry", () => {
    const onRetry = vi.fn()
    const { rerender } = render(<EmptyState>Nothing here.</EmptyState>)
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
    rerender(<EmptyState onRetry={onRetry}>Failed to load: HTTP 500</EmptyState>)
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it("useRetry bumps a counter a fetch effect can depend on", () => {
    const { result } = renderHook(() => useRetry())
    expect(result.current[0]).toBe(0)
    act(() => result.current[1]())
    expect(result.current[0]).toBe(1)
  })
})
