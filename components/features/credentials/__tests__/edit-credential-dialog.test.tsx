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
  it("edits a USERPASS username without requiring a password replacement", async () => {
    render(<EditCredentialDialog workspaceId="ws1" credential={{ id: "login1", name: "Database", description: null, provider: "NONE", type: "USERPASS", username: "old-user", scope: "WORKSPACE", crew_id: null, crew_ids: [] }} open onOpenChange={() => {}} onSuccess={() => {}} />)
    fireEvent.change(screen.getByLabelText("Username"), { target: { value: "new-user" } })
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(patchBody()).toHaveProperty("username", "new-user"))
    expect(patchBody()).not.toHaveProperty("value")
  })
  it.each([["SSH_KEY", /Replace private key/i], ["CERTIFICATE", /Replace certificate/i], ["GENERIC_SECRET", /Replace file or secret contents/i]] as const)("uses a multiline replacement for %s", (type, label) => {
    render(<EditCredentialDialog workspaceId="ws1" credential={{ id: "c1", name: "Fixture", description: null, provider: "NONE", type, scope: "WORKSPACE", crew_id: null, crew_ids: [] }} open onOpenChange={() => {}} onSuccess={() => {}} />)
    fireEvent.click(screen.getByRole("checkbox", { name: "Replace the stored secret" }))
    expect(screen.getByLabelText(label)).toHaveProperty("tagName", "TEXTAREA")
  })
  it("includes a pending tag on save and guards a tag-only draft", async () => {
    const onSuccess = setup()
    fireEvent.change(screen.getByLabelText(/Tags/), { target: { value: " Production " } })
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(screen.getByRole("alertdialog")).toBeInTheDocument()
    // Submit directly: keyboard/programmatic submission need not blur the tag input.
    fireEvent.submit(screen.getByLabelText("Name").closest("form")!)
    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    expect(patchBody().tags).toEqual(["production"])
  })
  it("adds, deduplicates and removes tags without submitting the form", async () => {
    const onSuccess = setup()
    const tags = screen.getByLabelText(/Tags/)
    for (const draft of ["Demo", "demo", "infra"]) {
      fireEvent.change(tags, { target: { value: draft } })
      fireEvent.keyDown(tags, { key: "Enter" })
    }
    expect(onSuccess).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Remove tag demo" }))
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    expect(patchBody().tags).toEqual(["infra"])
  })
  it("keeps provider identity, authentication and expiry out of metadata PATCH", async () => {
    render(<EditCredentialDialog workspaceId="ws1" credential={{ id: "login1", name: "ChatGPT", description: null, provider: "OPENAI", type: "AI_CLI_TOKEN", scope: "WORKSPACE", crew_id: null, crew_ids: [], isProviderLogin: true, token_expires_at: "2026-09-08T16:47:22Z" }} open onOpenChange={() => {}} onSuccess={() => {}} />)
    expect(screen.getByRole("dialog", { name: /Edit provider/ })).toBeInTheDocument()
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalled())
    expect(patchBody()).not.toHaveProperty("provider")
    expect(patchBody()).not.toHaveProperty("value")
    expect(patchBody()).not.toHaveProperty("token_expires_at")
  })
  it("does not truncate an existing expiry timestamp on a name-only edit", async () => {
    render(<EditCredentialDialog workspaceId="ws1" credential={{ id: "secret1", name: "SECRET", description: null, provider: "NONE", type: "SECRET", scope: "WORKSPACE", crew_id: null, crew_ids: [], token_expires_at: "2026-09-08T16:47:22Z" }} open onOpenChange={() => {}} onSuccess={() => {}} />)
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Renamed" } })
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalled())
    expect(patchBody()).not.toHaveProperty("token_expires_at")
  })
  it.each([
    ["2026-10-01", "2026-10-01T00:00:00.000Z"],
    ["", null],
  ])("still allows explicitly changing or clearing expiry: %s", async (date, expected) => {
    render(<EditCredentialDialog workspaceId="ws1" credential={{ id: "secret1", name: "SECRET", description: null, provider: "NONE", type: "SECRET", scope: "WORKSPACE", crew_id: null, crew_ids: [], token_expires_at: "2026-09-08T16:47:22Z" }} open onOpenChange={() => {}} onSuccess={() => {}} />)
    fireEvent.click(screen.getByRole("button", { name: /Access & security/ }))
    fireEvent.change(screen.getByLabelText("Expires on"), { target: { value: date } })
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalled())
    expect(patchBody().token_expires_at).toBe(expected)
  })
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
