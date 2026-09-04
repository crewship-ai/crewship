import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"

describe("ConfirmDialog", () => {
  it("lists what is lost and kept, and confirms", async () => {
    const onConfirm = vi.fn().mockResolvedValue(undefined)
    const onOpenChange = vi.fn()
    render(
      <ConfirmDialog
        open
        onOpenChange={onOpenChange}
        title="Delete agent Alex?"
        consequences={[{ tone: "lost", text: "Its bindings are removed" }, { tone: "kept", text: "Sessions and runs stay 30 days" }]}
        confirmLabel="Delete agent"
        destructive
        onConfirm={onConfirm}
      />,
    )
    expect(screen.getByRole("alertdialog", { name: "Delete agent Alex?" })).toBeInTheDocument()
    expect(screen.getByText("Its bindings are removed").closest("li")).toHaveAttribute("data-tone", "lost")
    expect(screen.getByText("Sessions and runs stay 30 days").closest("li")).toHaveAttribute("data-tone", "kept")
    fireEvent.click(screen.getByRole("button", { name: "Delete agent" }))
    await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  })

  it("keeps the confirm button off until the name is typed back", () => {
    const onConfirm = vi.fn()
    render(
      <ConfirmDialog open onOpenChange={() => {}} title="Delete crew Engineering?" confirmLabel="Delete crew" destructive typeToConfirm="engineering" onConfirm={onConfirm} />,
    )
    const button = screen.getByRole("button", { name: "Delete crew" })
    expect(button).toBeDisabled()
    fireEvent.click(button)
    expect(onConfirm).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText(/to confirm/), { target: { value: "engineering" } })
    expect(button).toBeEnabled()
  })

  it("stays open when the work fails, so nothing typed is lost", async () => {
    const onConfirm = vi.fn().mockRejectedValue(new Error("HTTP 500"))
    const onOpenChange = vi.fn()
    render(<ConfirmDialog open onOpenChange={onOpenChange} title="Revoke?" confirmLabel="Revoke" onConfirm={onConfirm} />)
    fireEvent.click(screen.getByRole("button", { name: "Revoke" }))
    await waitFor(() => expect(onConfirm).toHaveBeenCalled())
    expect(onOpenChange).not.toHaveBeenCalledWith(false)
  })
})
