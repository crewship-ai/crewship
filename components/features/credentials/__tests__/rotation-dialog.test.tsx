import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { RotationDialog } from "../rotation-dialog"
const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))

const base = { workspaceId: "w1", credentialId: "c1", credentialName: "Fixture", open: true, onOpenChange: () => {}, onRotated: () => {} }

describe("replacement verification", () => {
  beforeEach(() => h.apiFetch.mockReset())
  it("does not probe the old stored value or claim that the new draft was verified", () => {
    vi.useFakeTimers()
    try {
      render(<RotationDialog {...base} />)
      fireEvent.change(screen.getByPlaceholderText("Paste the new value…"), { target: { value: "draft-fixture" } })
      vi.advanceTimersByTime(2000)
      expect(h.apiFetch).not.toHaveBeenCalled()
      expect(screen.getByText(/replacement has not been tested/i)).toBeInTheDocument()
    } finally { vi.useRealTimers() }
  })

  it("grace segment sends grace_seconds (default 24 hours)", async () => {
    h.apiFetch.mockResolvedValue({ ok: true })
    const onRotated = vi.fn()
    render(<RotationDialog {...base} onRotated={onRotated} />)
    expect(screen.getByRole("radio", { name: "24 hours" })).toHaveAttribute("aria-checked", "true")
    fireEvent.change(screen.getByLabelText("New value"), { target: { value: "  password with spaces  " } })
    fireEvent.click(screen.getByRole("button", { name: "Replace" }))
    await waitFor(() => expect(onRotated).toHaveBeenCalledTimes(1))
    expect(h.apiFetch).toHaveBeenLastCalledWith("/api/v1/credentials/c1/rotate?workspace_id=w1", expect.objectContaining({
      method: "POST", body: JSON.stringify({ value: "  password with spaces  ", grace_seconds: 86400 }),
    }))
  })

  it.each([["No grace", 0], ["1 hour", 3600], ["7 days", 604800]])("picking %s sends %i", async (label, seconds) => {
    h.apiFetch.mockResolvedValue({ ok: true })
    render(<RotationDialog {...base} />)
    fireEvent.change(screen.getByLabelText("New value"), { target: { value: "next" } })
    fireEvent.click(screen.getByRole("radio", { name: label as string }))
    fireEvent.click(screen.getByRole("button", { name: "Replace" }))
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalled())
    expect(JSON.parse(h.apiFetch.mock.calls[0][1].body)).toEqual({ value: "next", grace_seconds: seconds })
  })

  it("patch mode replaces at once through PATCH, with no grace choice", async () => {
    h.apiFetch.mockResolvedValue({ ok: true })
    render(<RotationDialog {...base} mode="patch" />)
    expect(screen.queryByRole("radio", { name: "24 hours" })).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText("New value"), { target: { value: "next" } })
    fireEvent.click(screen.getByRole("button", { name: "Replace" }))
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalled())
    expect(h.apiFetch).toHaveBeenCalledWith("/api/v1/credentials/c1?workspace_id=w1", expect.objectContaining({ method: "PATCH", body: JSON.stringify({ value: "next" }) }))
  })

  it("preserves multiline file contents and clears the draft when changing credentials", () => {
    const props = { ...base, credentialType: "CERT", onOpenChange: vi.fn(), onRotated: vi.fn() }
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
