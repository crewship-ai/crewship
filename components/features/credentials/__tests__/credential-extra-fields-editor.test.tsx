import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { CredentialExtraFieldsEditor } from "../credential-extra-fields-editor"
const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))
beforeEach(() => h.apiFetch.mockReset())
const fields = [{ key: "passphrase", is_secret: true, value: "must-not-prefill" }, { key: "public_key", is_secret: false, value: "public-fixture" }]
function setup() {
  const onSaved = vi.fn(), onDirtyChange = vi.fn()
  render(<CredentialExtraFieldsEditor fields={fields} credentialId="c1" workspaceId="w1" onSaved={onSaved} onDirtyChange={onDirtyChange} />)
  return { onSaved, onDirtyChange }
}
describe("explicit field editing", () => {
  it("never preloads secret values and writes only the selected field", async () => {
    h.apiFetch.mockResolvedValue({ ok: true })
    const { onSaved } = setup()
    fireEvent.click(screen.getByRole("button", { name: "Edit Passphrase" }))
    expect(screen.getByLabelText("Passphrase")).toHaveValue("")
    fireEvent.change(screen.getByLabelText("Passphrase"), { target: { value: "replacement-fixture" } })
    fireEvent.click(screen.getByRole("button", { name: "Save Passphrase" }))
    await waitFor(() => expect(onSaved).toHaveBeenCalledOnce())
    expect(h.apiFetch.mock.calls[0][0]).toContain("/fields/passphrase?")
    expect(JSON.parse(h.apiFetch.mock.calls[0][1].body)).toEqual({ value: "replacement-fixture", is_secret: true })
  })
  it("preserves a draft after a failed save and allows cancelling it", async () => {
    h.apiFetch.mockResolvedValue({ ok: false, status: 403 })
    const { onSaved, onDirtyChange } = setup()
    fireEvent.click(screen.getByRole("button", { name: "Edit Public key" }))
    fireEvent.change(screen.getByLabelText("Public key"), { target: { value: "draft" } })
    fireEvent.click(screen.getByRole("button", { name: "Save Public key" }))
    expect(await screen.findByText(/Field could not be saved/)).toBeInTheDocument()
    expect(screen.getByLabelText("Public key")).toHaveValue("draft")
    expect(onSaved).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Cancel Public key" }))
    expect(onDirtyChange).toHaveBeenLastCalledWith(false)
  })
})
