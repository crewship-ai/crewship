import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { RotationDialog } from "../rotation-dialog"
const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))
describe("replacement verification", () => {
  beforeEach(() => h.apiFetch.mockReset())
  it("does not probe the old stored value or claim that the new draft was verified", () => {
    vi.useFakeTimers()
    try {
      render(<RotationDialog workspaceId="w1" credentialId="c1" credentialName="Fixture" open onOpenChange={() => {}} onRotated={() => {}} />)
      fireEvent.change(screen.getByPlaceholderText("Paste the new value…"), { target: { value: "draft-fixture" } })
      vi.advanceTimersByTime(2000)
      expect(h.apiFetch).not.toHaveBeenCalled()
      expect(screen.getByText(/replacement has not been tested/i)).toBeInTheDocument()
    } finally { vi.useRealTimers() }
  })
  it("replaces the supplied value without provider rotation or grace overlap", async () => {
    h.apiFetch.mockResolvedValue({ ok: true })
    const onRotated = vi.fn()
    render(<RotationDialog workspaceId="w1" credentialId="c1" credentialName="Fixture" open onOpenChange={() => {}} onRotated={onRotated} />)
    fireEvent.change(screen.getByLabelText("New value"), { target: { value: "  password with spaces  " } })
    expect(screen.queryByText("Grace overlap")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Replace value", exact: true }))
    await waitFor(() => expect(onRotated).toHaveBeenCalledTimes(1))
    expect(h.apiFetch).toHaveBeenCalledWith("/api/v1/credentials/c1/rotate?workspace_id=w1", expect.objectContaining({
      method: "POST", body: JSON.stringify({ value: "  password with spaces  ", grace_seconds: 0 }),
    }))
  })
  it("preserves multiline file contents and clears the draft when changing credentials", () => {
    const props = { workspaceId: "w1", credentialId: "c1", credentialName: "Fixture", credentialType: "CERT", open: true, onOpenChange: vi.fn(), onRotated: vi.fn() }
    const { rerender } = render(<RotationDialog {...props} />)
    const field = screen.getByLabelText("New value")
    expect(field.tagName).toBe("TEXTAREA")
    fireEvent.change(field, { target: { value: "fixture line one\nfixture line two" } })
    expect(field).toHaveValue("fixture line one\nfixture line two")
    fireEvent.click(screen.getByRole("button", { name: "Show new value" }))
    rerender(<RotationDialog {...props} credentialId="c2" />)
    expect(screen.getByLabelText("New value")).toHaveValue("")
    expect(screen.getByRole("button", { name: "Show new value" })).toBeInTheDocument()
  })
})
