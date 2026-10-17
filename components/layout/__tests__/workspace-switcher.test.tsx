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

// CHR-01 / G13 (#3482): Create workspace is a small CreateSurface (v67
// `newws`, "Dialog S; chyby u pole"): its refusals belong to the field, not
// to a toast.
const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => fetchMock(...a) }))

describe("CreateWorkspaceDialog on the kit surface", () => {
  beforeEach(() => {
    cleanup()
    fetchMock.mockReset()
  })

  it("is a small surface named Create workspace, with Cancel and Create workspace", () => {
    render(<CreateWorkspaceDialog open onOpenChange={vi.fn()} onCreated={vi.fn()} />)
    const dialog = screen.getByRole("dialog", { name: "Create workspace" })
    expect(dialog.className).toMatch(/sm:max-w-\[480px\]/)
    expect(screen.getByRole("button", { name: "Cancel" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Create workspace" })).toBeDisabled()
  })

  it("shows a slug the server refuses under the Slug field", async () => {
    fetchMock.mockResolvedValue({ ok: false, status: 409, text: async () => JSON.stringify({ error: "Slug already taken", field: "slug" }) })
    render(<CreateWorkspaceDialog open onOpenChange={vi.fn()} onCreated={vi.fn()} />)
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "Acme" } })
    fireEvent.click(screen.getByRole("button", { name: "Create workspace" }))
    const error = await screen.findByText("Slug already taken")
    expect(error).toHaveAttribute("id", "ws-slug-error")
    expect(screen.getByLabelText(/^slug$/i)).toHaveAttribute("aria-invalid", "true")
    expect(screen.getByLabelText(/^slug$/i)).toHaveAttribute("aria-describedby", "ws-slug-error")
  })

  it("shows any other refusal under the Name field", async () => {
    fetchMock.mockResolvedValue({ ok: false, status: 400, text: async () => JSON.stringify({ error: "Name is reserved" }) })
    render(<CreateWorkspaceDialog open onOpenChange={vi.fn()} onCreated={vi.fn()} />)
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "Admin" } })
    fireEvent.click(screen.getByRole("button", { name: "Create workspace" }))
    expect(await screen.findByText("Name is reserved")).toHaveAttribute("id", "ws-name-error")
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
