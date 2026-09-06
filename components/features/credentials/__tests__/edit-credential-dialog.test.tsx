import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { EditCredentialDialog } from "../edit-credential-dialog"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))
beforeEach(() => {
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue({ ok: true, json: async () => ({}) })
})
function setup(securityLevel: number | null = 3) {
  const onSuccess = vi.fn()
  render(<EditCredentialDialog workspaceId="ws1" credential={{ id: "cred1", name: "TEST_SECRET", description: null, provider: "NONE", type: "SECRET", scope: "WORKSPACE", crew_id: null, crew_ids: [], security_level: securityLevel ?? undefined }} open onOpenChange={() => {}} onSuccess={onSuccess} />)
  return onSuccess
}
function patchBody() {
  const call = h.apiFetch.mock.calls.find(([, options]) => options?.method === "PATCH")
  return JSON.parse(call![1].body)
}
describe("credential edit safety", () => {
  it("does not overwrite an unknown server tier with the display default", async () => {
    const onSuccess = setup(null)
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    expect(patchBody()).not.toHaveProperty("security_level")
  })
  it("saves metadata without sending a value or changing the Keeper tier", async () => {
    const onSuccess = setup()
    expect(screen.queryByLabelText(/^replace secret value/i)).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Production certificate" } })
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    expect(patchBody()).toMatchObject({ name: "Production certificate", security_level: 3 })
    expect(patchBody()).not.toHaveProperty("value")
  })
  it("clears a replacement when the operator turns replacement off", async () => {
    const onSuccess = setup()
    const toggle = screen.getByRole("checkbox", { name: "Replace the stored secret" })
    fireEvent.click(toggle)
    fireEvent.change(screen.getByLabelText(/^replace secret value/i), { target: { value: "fixture-not-a-real-secret" } })
    fireEvent.click(toggle)
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    expect(patchBody()).not.toHaveProperty("value")
  })
  it("submits an explicitly supplied replacement", async () => {
    const onSuccess = setup()
    fireEvent.click(screen.getByRole("checkbox", { name: "Replace the stored secret" }))
    fireEvent.change(screen.getByLabelText(/^replace secret value/i), { target: { value: "fixture-not-a-real-secret" } })
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    expect(patchBody()).toHaveProperty("value", "fixture-not-a-real-secret")
  })
  it("guards dismissal of unsaved metadata", () => {
    setup()
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "RENAMED_SECRET" } })
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(screen.getByRole("alertdialog")).toBeInTheDocument()
  })
})
