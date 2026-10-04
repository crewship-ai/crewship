import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { RotationDialog } from "../rotation-dialog"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
beforeEach(() => { api.mockReset() })
afterEach(cleanup)
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
function props() { return { workspaceId: "workspace-a", credentialId: "credential-a", credentialName: "Fixture", open: true, onOpenChange: vi.fn(), onRotated: vi.fn() } }
function submit() {
  fireEvent.change(screen.getByLabelText("New value"), { target: { value: "replacement fixture" } })
  fireEvent.click(screen.getByRole("button", { name: "Replace value" }))
}

it("clears the secret draft and visibility when the workspace changes", () => {
  const p = props()
  const { rerender } = render(<RotationDialog {...p} />)
  fireEvent.change(screen.getByLabelText("New value"), { target: { value: "private draft" } })
  fireEvent.click(screen.getByRole("button", { name: "Show new value" }))
  expect(screen.getByLabelText("New value")).toHaveAttribute("type", "text")
  rerender(<RotationDialog {...p} workspaceId="workspace-b" />)
  expect(screen.getByLabelText("New value")).toHaveValue("")
  expect(screen.getByLabelText("New value")).toHaveAttribute("type", "password")
})

it.each(["workspace", "credential", "close", "unmount"])("discards a late rotation success after %s", async (change) => {
  const pending = deferred<{ ok: boolean }>()
  api.mockReturnValueOnce(pending.promise)
  const p = props()
  const view = render(<RotationDialog {...p} />)
  submit()
  if (change === "unmount") view.unmount()
  else view.rerender(<RotationDialog {...p} workspaceId={change === "workspace" ? "workspace-b" : p.workspaceId} credentialId={change === "credential" ? "credential-b" : p.credentialId} open={change !== "close"} />)
  await act(async () => pending.resolve({ ok: true }))
  expect(p.onRotated).not.toHaveBeenCalled()
  expect(p.onOpenChange).not.toHaveBeenCalled()
})

it.each(["network", "response body"])("does not show an obsolete %s failure in the new target", async (failure) => {
  const pending = deferred<unknown>()
  api.mockReturnValueOnce(failure === "network" ? pending.promise : Promise.resolve({ ok: false, json: () => pending.promise }))
  const p = props()
  const { rerender } = render(<RotationDialog {...p} />)
  submit()
  await act(async () => {})
  rerender(<RotationDialog {...p} credentialId="credential-b" />)
  await act(async () => { if (failure === "network") pending.reject(new Error("offline")); else pending.resolve({ error: "old target refused" }) })
  expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  expect(screen.getByLabelText("New value")).not.toBeDisabled()
})

it.each(["server", "malformed", "null", "network"])("shows a recoverable %s failure without discarding the replacement draft", async (failure) => {
  if (failure === "network") api.mockRejectedValueOnce(new Error("offline"))
  else api.mockResolvedValueOnce({ ok: false, json: async () => { if (failure === "malformed") throw new Error("bad JSON"); return failure === "null" ? null : { error: "Permission denied" } } })
  const p = props()
  render(<RotationDialog {...p} />)
  submit()
  const alert = await screen.findByRole("alert")
  expect(alert).toHaveTextContent(failure === "server" ? "Permission denied" : failure === "network" ? "Network error" : "Could not replace the value")
  expect(screen.getByLabelText("New value")).toHaveValue("replacement fixture")
  api.mockResolvedValueOnce({ ok: true })
  fireEvent.click(screen.getByRole("button", { name: "Replace value" }))
  await waitFor(() => expect(p.onRotated).toHaveBeenCalledOnce())
  expect(p.onOpenChange).toHaveBeenCalledWith(false)
})

it("encodes the target and refuses blank or repeated submissions while pending", async () => {
  const pending = deferred<{ ok: boolean }>()
  api.mockReturnValue(pending.promise)
  const p = { ...props(), workspaceId: "workspace/a&b", credentialId: "credential/a?b" }
  render(<RotationDialog {...p} />)
  fireEvent.change(screen.getByLabelText("New value"), { target: { value: "   " } })
  expect(screen.getByRole("button", { name: "Replace value" })).toBeDisabled()
  submit()
  fireEvent.click(screen.getByRole("button", { name: /Replace value|Saving|Replacing/ }))
  expect(api).toHaveBeenCalledTimes(1)
  expect(api).toHaveBeenCalledWith("/api/v1/credentials/credential%2Fa%3Fb/rotate?workspace_id=workspace%2Fa%26b", expect.objectContaining({ method: "POST" }))
  await act(async () => pending.resolve({ ok: true }))
})

it("does not carry a pending state or secret draft into a reopened dialog", () => {
  api.mockReturnValue(new Promise(() => {}))
  const p = props()
  const { rerender } = render(<RotationDialog {...p} />)
  submit()
  rerender(<RotationDialog {...p} open={false} />)
  rerender(<RotationDialog {...p} />)
  expect(screen.getByLabelText("New value")).toHaveValue("")
  expect(screen.getByLabelText("New value")).not.toBeDisabled()
})

it("toggles multiline masking and cancels a pristine replacement", () => {
  const p = props()
  render(<RotationDialog {...p} credentialType="SSH_KEY" />)
  const field = screen.getByLabelText("New value")
  expect(field.tagName).toBe("TEXTAREA")
  fireEvent.click(screen.getByRole("button", { name: "Show new value" }))
  expect(field).not.toHaveClass("[-webkit-text-security:disc]")
  fireEvent.click(screen.getByRole("button", { name: "Hide new value" }))
  expect(field).toHaveClass("[-webkit-text-security:disc]")
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
  expect(p.onOpenChange).toHaveBeenCalledWith(false)
})
