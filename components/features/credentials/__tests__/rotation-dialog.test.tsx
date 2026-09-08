import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { RotationDialog } from "../rotation-dialog"
const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))
describe("replacement verification", () => {
  it("does not probe the old stored value or claim that the new draft was verified", () => {
    vi.useFakeTimers()
    try {
      render(<RotationDialog workspaceId="w1" credentialId="c1" credentialName="Fixture" open onOpenChange={() => {}} onRotated={() => {}} />)
      fireEvent.change(screen.getByPlaceholderText("Paste the new token..."), { target: { value: "draft-fixture" } })
      vi.advanceTimersByTime(2000)
      expect(h.apiFetch).not.toHaveBeenCalled()
      expect(screen.getByText(/replacement has not been tested/i)).toBeInTheDocument()
    } finally { vi.useRealTimers() }
  })
})
