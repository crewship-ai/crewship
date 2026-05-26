import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"
import { CreateWorkspaceDialog } from "../workspace-switcher"

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

describe("CreateWorkspaceDialog", () => {
  const onOpenChange = vi.fn()
  const onCreated = vi.fn()

  beforeEach(() => {
    cleanup()
    onOpenChange.mockReset()
    onCreated.mockReset()
  })

  function renderDialog(open: boolean) {
    return render(
      <CreateWorkspaceDialog open={open} onOpenChange={onOpenChange} onCreated={onCreated} />,
    )
  }

  it("clears the form when open transitions from true to false (Cancel-button path)", async () => {
    const { rerender } = renderDialog(true)

    const nameInput = screen.getByLabelText(/^name$/i) as HTMLInputElement
    fireEvent.change(nameInput, { target: { value: "Acme" } })
    expect(nameInput.value).toBe("Acme")
    const slugInput = screen.getByLabelText(/^slug$/i) as HTMLInputElement
    expect(slugInput.value).toBe("acme")

    // Close the dialog by flipping the parent-controlled prop — this is
    // the path the Cancel button takes, which previously did not run reset().
    rerender(
      <CreateWorkspaceDialog open={false} onOpenChange={onOpenChange} onCreated={onCreated} />,
    )

    // Reopen — name and slug must be empty again.
    rerender(
      <CreateWorkspaceDialog open={true} onOpenChange={onOpenChange} onCreated={onCreated} />,
    )

    await waitFor(() => {
      const reopenedName = screen.getByLabelText(/^name$/i) as HTMLInputElement
      const reopenedSlug = screen.getByLabelText(/^slug$/i) as HTMLInputElement
      expect(reopenedName.value).toBe("")
      expect(reopenedSlug.value).toBe("")
    })
  })

  it("auto-derives slug from name until the user edits the slug manually", () => {
    renderDialog(true)
    const nameInput = screen.getByLabelText(/^name$/i) as HTMLInputElement
    const slugInput = screen.getByLabelText(/^slug$/i) as HTMLInputElement

    fireEvent.change(nameInput, { target: { value: "Acme Engineering" } })
    expect(slugInput.value).toBe("acme-engineering")

    // User edits slug — auto-derivation should stop
    fireEvent.change(slugInput, { target: { value: "custom" } })
    fireEvent.change(nameInput, { target: { value: "Acme Engineering 2" } })
    expect(slugInput.value).toBe("custom")
  })
})
