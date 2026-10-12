import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { RevealDialog } from "../reveal-dialog"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
const reason = "Investigating the configured endpoint during this incident"
const privateValue = "reveal-test-placeholder"
beforeEach(() => { api.mockReset(); api.mockResolvedValue({ ok: true, json: async () => ({ value: privateValue }) }) })
afterEach(() => { cleanup(); vi.restoreAllMocks() })
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
function show() {
  const p = { workspaceId: "workspace/a&b", credentialId: "credential/a?b", credentialName: "Fixture", open: true, onOpenChange: vi.fn() }
  return { ...render(<RevealDialog {...p} />), p }
}
async function reveal() {
  fireEvent.change(screen.getByLabelText("Reason"), { target: { value: reason } })
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "Reveal the existing value" })))
}

it("reports copied only after the browser confirms the clipboard write", async () => {
  const pending = deferred<void>()
  const write = vi.fn(() => pending.promise)
  vi.spyOn(navigator, "clipboard", "get").mockReturnValue({ writeText: write } as unknown as Clipboard)
  const { p } = show()
  await reveal()
  fireEvent.click(screen.getByRole("button", { name: "Copy" }))
  expect(write).toHaveBeenCalledWith(privateValue)
  expect(screen.queryByRole("button", { name: "Copied" })).not.toBeInTheDocument()
  await act(async () => pending.resolve())
  expect(screen.getByRole("button", { name: "Copied" })).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Done" }))
  expect(p.onOpenChange).toHaveBeenCalledWith(false)
})

it("does not claim success when the clipboard API is unavailable", async () => {
  vi.spyOn(navigator, "clipboard", "get").mockReturnValue(undefined as unknown as Clipboard)
  show()
  await reveal()
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "Copy" })))
  expect(screen.queryByRole("button", { name: "Copied" })).not.toBeInTheDocument()
  expect(screen.getByRole("alert")).toHaveTextContent("Could not copy")
})

it("handles a rejected clipboard permission and allows a successful retry", async () => {
  const write = vi.fn().mockRejectedValueOnce(new DOMException("Denied", "NotAllowedError")).mockResolvedValueOnce(undefined)
  vi.spyOn(navigator, "clipboard", "get").mockReturnValue({ writeText: write } as unknown as Clipboard)
  show()
  await reveal()
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "Copy" })))
  expect(screen.getByRole("alert")).toHaveTextContent("Could not copy")
  expect(screen.queryByRole("button", { name: "Copied" })).not.toBeInTheDocument()
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "Copy" })))
  expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Copied" })).toBeInTheDocument()
})

it("ignores an old clipboard rejection after the reveal is hidden", async () => {
  const pending = deferred<void>()
  vi.spyOn(navigator, "clipboard", "get").mockReturnValue({ writeText: () => pending.promise } as unknown as Clipboard)
  show()
  await reveal()
  fireEvent.click(screen.getByRole("button", { name: "Copy" }))
  vi.spyOn(document, "hidden", "get").mockReturnValue(true)
  fireEvent(document, new Event("visibilitychange"))
  await act(async () => pending.reject(new Error("permission rejected")))
  expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  expect(screen.queryByTestId("revealed-value")).not.toBeInTheDocument()
})

it("keeps an old clipboard completion from marking a new reveal as copied", async () => {
  const pending = deferred<void>()
  vi.spyOn(navigator, "clipboard", "get").mockReturnValue({ writeText: () => pending.promise } as unknown as Clipboard)
  show()
  await reveal()
  fireEvent.click(screen.getByRole("button", { name: "Copy" }))
  const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(true)
  fireEvent(document, new Event("visibilitychange"))
  hidden.mockReturnValue(false)
  await reveal()
  await act(async () => pending.resolve())
  expect(screen.getByRole("button", { name: "Copy" })).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Copied" })).not.toBeInTheDocument()
})

it("refuses a response that arrives while the tab is hidden and clears the reason", async () => {
  const pending = deferred<unknown>()
  api.mockReturnValueOnce(pending.promise)
  show()
  await reveal()
  vi.spyOn(document, "hidden", "get").mockReturnValue(true)
  await act(async () => pending.resolve({ ok: true, json: async () => ({ value: privateValue }) }))
  expect(screen.queryByTestId("revealed-value")).not.toBeInTheDocument()
  expect(screen.getByRole("alert")).toHaveTextContent("tab was hidden")
  expect(screen.getByLabelText("Reason")).toHaveValue("")
})

it("aborts a request on close and ignores its later network error", async () => {
  const pending = deferred<unknown>()
  api.mockReturnValueOnce(pending.promise)
  const { rerender, p } = show()
  await reveal()
  expect(api).toHaveBeenCalledWith("/api/v1/credentials/credential%2Fa%3Fb/reveal?workspace_id=workspace%2Fa%26b", expect.anything())
  const signal = api.mock.calls[0][1].signal as AbortSignal
  rerender(<RevealDialog {...p} open={false} />)
  expect(signal.aborted).toBe(true)
  await act(async () => pending.reject(new Error("offline")))
  rerender(<RevealDialog {...p} />)
  expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  expect(screen.getByLabelText("Reason")).toHaveValue("")
})

it("treats an unreadable success body as no value and permits cancelling", async () => {
  api.mockResolvedValueOnce({ ok: true, json: async () => { throw new Error("malformed JSON") } })
  const { p } = show()
  await reveal()
  expect(screen.getByRole("alert")).toHaveTextContent("Invalid reveal response")
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
  expect(p.onOpenChange).toHaveBeenCalledWith(false)
})
