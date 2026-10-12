import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { CredentialPicker, type CredentialPickerProps } from "./credential-picker"

const state = vi.hoisted(() => ({ fetch: vi.fn(), success: vi.fn(), error: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => state.fetch(...args) }))
vi.mock("sonner", () => ({ toast: { success: state.success, error: state.error } }))
function props(patch: Partial<CredentialPickerProps> = {}): CredentialPickerProps {
  return { envKey: "GITHUB_TOKEN", envValue: "", credentials: [], credLoading: false, workspaceId: "workspace", onFetchCredentials: vi.fn(), onAddCredential: vi.fn(), onChangeValue: vi.fn(), ...patch }
}
const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
function deferred() { let resolve!: (response: Response) => void; const promise = new Promise<Response>(done => { resolve = done }); return { promise, resolve } }
async function createForm() {
  fireEvent.click(screen.getByRole("button", { name: "Select credential..." }))
  fireEvent.click(await screen.findByRole("button", { name: "Create new credential" }))
  fireEvent.change(screen.getByPlaceholderText("github-token"), { target: { value: " my-test-key " } })
  fireEvent.change(screen.getByPlaceholderText("ghp_xxxxxxxxxxxx"), { target: { value: " fixture-value " } })
}
beforeEach(() => { state.fetch.mockReset(); state.success.mockReset(); state.error.mockReset() })
afterEach(cleanup)

it.each(["workspace", "environment key"])("clears a pending creation when the %s changes", async (scope) => {
  const pending = deferred(); state.fetch.mockReturnValue(pending.promise)
  const p = props(); const view = render(<CredentialPicker {...p} />)
  await createForm(); fireEvent.click(screen.getByRole("button", { name: "Save" }))
  view.rerender(<CredentialPicker {...p} {...(scope === "workspace" ? { workspaceId: "next" } : { envKey: "NEXT_TOKEN" })} />)
  expect(screen.queryByDisplayValue(" fixture-value ")).not.toBeInTheDocument()
  await act(async () => pending.resolve(reply({ id: "old-key", name: "my-test-key", type: "SECRET" })))
  expect(p.onAddCredential).not.toHaveBeenCalled()
  expect(p.onChangeValue).not.toHaveBeenCalled()
  expect(state.success).not.toHaveBeenCalled()
})

it("ignores a completed credential creation after unmount", async () => {
  const pending = deferred(); state.fetch.mockReturnValue(pending.promise)
  const p = props(); const view = render(<CredentialPicker {...p} />)
  await createForm(); fireEvent.click(screen.getByRole("button", { name: "Save" })); view.unmount()
  await act(async () => pending.resolve(reply({ id: "old-key", name: "my-test-key", type: "SECRET" })))
  expect(p.onAddCredential).not.toHaveBeenCalled()
  expect(p.onChangeValue).not.toHaveBeenCalled()
  expect(state.success).not.toHaveBeenCalled()
})

it("creates a trimmed workspace secret and selects its environment reference", async () => {
  const credential = { id: "key", name: "my-test-key", type: "SECRET" }
  state.fetch.mockResolvedValue(reply(credential)); const p = props(); render(<CredentialPicker {...p} />)
  await createForm(); fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await waitFor(() => expect(p.onAddCredential).toHaveBeenCalledWith(credential))
  expect(p.onChangeValue).toHaveBeenCalledExactlyOnceWith("${GITHUB_TOKEN}")
  const [url, init] = state.fetch.mock.calls[0]
  expect(url).toBe("/api/v1/credentials?workspace_id=workspace")
  expect(JSON.parse(init.body)).toEqual({ name: "my-test-key", type: "SECRET", value: "fixture-value", scope: "WORKSPACE" })
  expect(init.method).toBe("POST")
  expect(screen.queryByDisplayValue(" fixture-value ")).not.toBeInTheDocument()
})

it.each([
  ["github-token", "ACTIVE"], ["GITHUB_TOKEN", "PENDING"], ["github_token", "EXPIRED"], ["different", "REVOKED"], ["github-token", undefined],
] as const)("shows and selects %s with status %s", async (name, status) => {
  const p = props({ envValue: "${GITHUB_TOKEN}", credentials: [{ id: "key", name, type: "SECRET", status }] })
  render(<CredentialPicker {...p} />)
  const trigger = screen.getByRole("button")
  expect(trigger).toHaveTextContent(name === "different" ? "GITHUB_TOKEN" : name)
  fireEvent.click(trigger)
  const choices = await screen.findAllByRole("button", { name: new RegExp(name) })
  fireEvent.click(choices.at(-1)!)
  expect(p.onChangeValue).toHaveBeenCalledExactlyOnceWith("${GITHUB_TOKEN}")
})

it("shows an unresolved reference and can replace it with a manual value", async () => {
  const p = props({ envValue: "${MISSING}" }); render(<CredentialPicker {...p} />)
  fireEvent.click(screen.getByRole("button", { name: "MISSING" }))
  expect(await screen.findByText("No credentials found")).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Manual value" }))
  expect(p.onChangeValue).toHaveBeenCalledWith("")
  fireEvent.change(screen.getByPlaceholderText("plain value"), { target: { value: "manual" } })
  expect(p.onChangeValue).toHaveBeenLastCalledWith("manual")
})

it("opens a manual value's credential choices and can keep the manual value", async () => {
  const p = props({ envValue: "manual" }); render(<CredentialPicker {...p} />)
  expect(screen.getByPlaceholderText("plain value")).toHaveValue("manual")
  fireEvent.click(screen.getByTitle("Switch to credential"))
  fireEvent.click(await screen.findByRole("button", { name: "Manual value" }))
  expect(screen.getByPlaceholderText("plain value")).toHaveValue("manual")
  expect(p.onChangeValue).not.toHaveBeenCalled()
})

it("derives an environment reference from the selected credential when the environment key is empty", async () => {
  const p = props({ envKey: "", credentials: [{ id: "key", name: "my-key", type: "SECRET" }] }); render(<CredentialPicker {...p} />)
  fireEvent.click(screen.getByRole("button", { name: "Select credential..." }))
  fireEvent.click(await screen.findByRole("button", { name: /my-key/ }))
  expect(p.onChangeValue).toHaveBeenCalledExactlyOnceWith("${MY_KEY}")
})

it("waits for the credential list to load", () => {
  render(<CredentialPicker {...props({ credLoading: true })} />)
  fireEvent.click(screen.getByRole("button", { name: "Select credential..." }))
  expect(screen.queryByRole("button", { name: "Create new credential" })).not.toBeInTheDocument()
  expect(screen.queryByText("No credentials found")).not.toBeInTheDocument()
})

it.each(["message", "object", "null", "malformed", "network"])("preserves creation fields and allows retry after a %s failure", async (kind) => {
  if (kind === "network") state.fetch.mockRejectedValueOnce(new Error("offline"))
  else if (kind === "malformed") state.fetch.mockResolvedValueOnce(new Response("unreadable", { status: 503 }))
  else state.fetch.mockResolvedValueOnce(reply(kind === "message" ? { error: "Refused" } : kind === "null" ? null : { error: { code: "no" } }, 403))
  const p = props(); render(<CredentialPicker {...p} />); await createForm()
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await waitFor(() => expect(state.error).toHaveBeenCalledWith(kind === "network" ? "Network error creating credential" : kind === "message" ? "Refused" : "Failed to create credential"))
  expect(screen.getByPlaceholderText("ghp_xxxxxxxxxxxx")).toHaveValue(" fixture-value ")
  expect(screen.getByRole("button", { name: "Save" })).toBeEnabled()
  expect(p.onAddCredential).not.toHaveBeenCalled()
  state.fetch.mockResolvedValueOnce(reply({ id: "key", name: "my-test-key", type: "SECRET" }))
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await waitFor(() => expect(p.onAddCredential).toHaveBeenCalledOnce())
})

it.each(["cancel", "escape"])("clears creation fields on %s and requires new input when reopened", async (action) => {
  render(<CredentialPicker {...props({ envKey: "" })} />); await createForm()
  if (action === "cancel") fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
  else {
    fireEvent.keyDown(document, { key: "Escape" })
    fireEvent.click(screen.getByRole("button", { name: "Select credential..." }))
  }
  fireEvent.click(await screen.findByRole("button", { name: "Create new credential" }))
  expect(screen.getByPlaceholderText("github-token")).toHaveValue("")
  expect(screen.getByPlaceholderText("ghp_xxxxxxxxxxxx")).toHaveValue("")
  expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()
})

it("aborts a creation closed with Escape and ignores the rejected request", async () => {
  let reject!: (error: Error) => void
  state.fetch.mockReturnValue(new Promise<Response>((_, fail) => { reject = fail }))
  const p = props(); render(<CredentialPicker {...p} />); await createForm()
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  const signal = state.fetch.mock.calls[0][1].signal as AbortSignal
  fireEvent.keyDown(document, { key: "Escape" })
  expect(signal.aborted).toBe(true)
  await act(async () => { reject(new Error("aborted")) })
  expect(state.error).not.toHaveBeenCalled()
  expect(p.onAddCredential).not.toHaveBeenCalled()
})

it.each([true, false])("ignores delayed JSON parsing after unmount (success=%s)", async (ok) => {
  let resolve!: (body: unknown) => void
  const json = new Promise<unknown>(done => { resolve = done })
  state.fetch.mockResolvedValue({ ok, json: () => json })
  const p = props(); const view = render(<CredentialPicker {...p} />); await createForm()
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await act(async () => {})
  view.unmount()
  await act(async () => { resolve(ok ? { id: "late", name: "late", type: "SECRET" } : { error: "late refusal" }) })
  expect(p.onAddCredential).not.toHaveBeenCalled()
  expect(state.error).not.toHaveBeenCalled()
  expect(state.success).not.toHaveBeenCalled()
})
