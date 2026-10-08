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

// #3005: a workspace with a logo is drawn with it — in the rail tile and in
// the switcher's list — and with its initial otherwise.
import { WorkspaceMark } from "../workspace-switcher"

describe("WorkspaceMark", () => {
  beforeEach(() => cleanup())

  it("draws the logo when the workspace has one", () => {
    const { container } = render(<WorkspaceMark name="Dess" logoUrl="/api/v1/workspaces/w1/logo?v=1" className="h-7 w-7" />)
    expect(container.querySelector("img")?.getAttribute("src")).toBe("/api/v1/workspaces/w1/logo?v=1")
  })

  it("falls back to the initial without a logo", () => {
    const { container } = render(<WorkspaceMark name="Dess" className="h-7 w-7" />)
    expect(container.querySelector("img")).toBeNull()
    expect(container.textContent).toBe("D")
  })
})
