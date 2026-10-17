import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"
import { ChangePasswordDialog } from "../change-password-dialog"

// Settings › Profile › Password (Harbor 2): the dialog is a form. Enter in a
// field submits it, a refusal stays inside the dialog under the field it is
// about, and success is a state of the same dialog rather than a toast.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

function json(status: number, body: unknown) {
  return { ok: status >= 200 && status < 300, status, json: async () => body }
}

function renderDialog() {
  const onOpenChange = vi.fn()
  render(<ChangePasswordDialog open onOpenChange={onOpenChange} />)
  return { onOpenChange }
}

function fill(current: string, next: string, confirm = next) {
  fireEvent.change(screen.getByLabelText("Current password"), { target: { value: current } })
  fireEvent.change(screen.getByLabelText("New password"), { target: { value: next } })
  fireEvent.change(screen.getByLabelText("Confirm new password"), { target: { value: confirm } })
}

const passwordCalls = () => apiFetch.mock.calls.filter((c) => c[0] === "/api/v1/users/me/password")

describe("ChangePasswordDialog", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
  })

  it("Enter submits", async () => {
    apiFetch.mockResolvedValue(json(200, { success: true, sessions_revoked: 1, cli_tokens_revoked: 0 }))
    renderDialog()
    fill("oldpassword1", "brandnew123")
    fireEvent.submit(screen.getByLabelText("Confirm new password").closest("form")!)
    await waitFor(() => expect(passwordCalls()).toHaveLength(1))
    expect(JSON.parse(passwordCalls()[0][1].body)).toEqual({
      current_password: "oldpassword1", new_password: "brandnew123", revoke_cli_tokens: false,
    })
  })

  it("a field error stays in the dialog, under its field", async () => {
    apiFetch.mockResolvedValue(json(401, { error: "Current password is incorrect" }))
    const { onOpenChange } = renderDialog()
    fill("wrongpassword", "brandnew123")
    fireEvent.click(screen.getByRole("button", { name: "Change password" }))

    const message = await screen.findByText("Current password is incorrect")
    expect(screen.getByRole("dialog")).toContainElement(message)
    expect(screen.getByLabelText("Current password")).toHaveAttribute("aria-invalid", "true")
    expect(onOpenChange).not.toHaveBeenCalledWith(false)
  })

  it("checks the confirmation before asking the server", async () => {
    renderDialog()
    fill("oldpassword1", "brandnew123", "different99")
    fireEvent.click(screen.getByRole("button", { name: "Change password" }))
    expect(await screen.findByText(/do not match/i)).toBeInTheDocument()
    expect(screen.getByLabelText("Confirm new password")).toHaveAttribute("aria-invalid", "true")
    expect(passwordCalls()).toHaveLength(0)
  })

  it("success is a state of the same dialog", async () => {
    apiFetch.mockResolvedValue(json(200, { success: true, sessions_revoked: 1, cli_tokens_revoked: 0 }))
    renderDialog()
    fill("oldpassword1", "brandnew123")
    fireEvent.click(screen.getByRole("button", { name: "Change password" }))

    expect(await screen.findByText(/Password changed · other sessions signed out/)).toBeInTheDocument()
    expect(screen.getByRole("dialog")).toBeInTheDocument()
    expect(screen.queryByLabelText("Current password")).toBeNull()
    expect(screen.getByRole("button", { name: "Done" })).toBeInTheDocument()
  })
})
