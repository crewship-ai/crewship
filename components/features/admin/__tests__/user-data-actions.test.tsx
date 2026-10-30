import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

const h = vi.hoisted(() => ({
  apiFetch: vi.fn(),
  toast: { success: vi.fn(), error: vi.fn(), message: vi.fn(), info: vi.fn() },
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("sonner", () => ({ toast: h.toast }))
import { UserDataActions } from "../user-data-actions"

const EMAIL = "ada@example.com"
const res = (ok: boolean, body: unknown = {}, status = ok ? 200 : 500) => ({
  ok, status, statusText: "x", json: async () => body, text: async () => JSON.stringify(body),
})
const deletes = () => h.apiFetch.mock.calls.filter((c) => c[1]?.method === "DELETE")

beforeEach(() => {
  h.apiFetch.mockReset()
  for (const f of Object.values(h.toast)) f.mockReset()
})
afterEach(cleanup)

function open(onErased?: () => void) {
  render(<UserDataActions userId="u1" email={EMAIL} workspaceId="w1" onErased={onErased} />)
  fireEvent.click(screen.getByRole("button", { name: /Erase data/ }))
  return screen.getByTestId("confirm-dialog")
}
const erase = (d: HTMLElement) => within(d).getByRole("button", { name: "Erase permanently" })

it("Erase uses the shared confirmation: typed e-mail and a reason gate the button", () => {
  const d = open()
  expect(erase(d)).toBeDisabled()
  fireEvent.change(within(d).getByLabelText(/Reason/), { target: { value: "request #1" } })
  expect(erase(d)).toBeDisabled() // reason alone is not enough
  fireEvent.change(within(d).getByLabelText(/to confirm/), { target: { value: EMAIL } })
  expect(erase(d)).toBeEnabled()
  fireEvent.change(within(d).getByLabelText(/Reason/), { target: { value: "   " } })
  expect(erase(d)).toBeDisabled() // typed e-mail alone is not enough
})

it("a failed DELETE keeps the dialog, the reason and the typed e-mail", async () => {
  h.apiFetch.mockResolvedValueOnce(res(false, {}, 500))
  const d = open()
  fireEvent.change(within(d).getByLabelText(/Reason/), { target: { value: "request #1" } })
  fireEvent.change(within(d).getByLabelText(/to confirm/), { target: { value: EMAIL } })
  fireEvent.click(erase(d))
  await waitFor(() => expect(deletes()).toHaveLength(1))
  expect(JSON.parse(deletes()[0][1].body)).toEqual({ reason: "request #1" })
  await waitFor(() => expect(h.toast.error).toHaveBeenCalled())
  const again = screen.getByTestId("confirm-dialog")
  expect(within(again).getByLabelText(/Reason/)).toHaveValue("request #1")
  expect(within(again).getByLabelText(/to confirm/)).toHaveValue(EMAIL)
})

it("a successful DELETE closes the dialog and tells the roster", async () => {
  h.apiFetch.mockResolvedValueOnce(res(true, { action_id: "a1", rows_deleted: 3 }))
  const onErased = vi.fn()
  const d = open(onErased)
  fireEvent.change(within(d).getByLabelText(/Reason/), { target: { value: "r" } })
  fireEvent.change(within(d).getByLabelText(/to confirm/), { target: { value: EMAIL } })
  fireEvent.click(erase(d))
  await waitFor(() => expect(onErased).toHaveBeenCalled())
  expect(screen.queryByTestId("confirm-dialog")).toBeNull()
})

// A refusal shows the server's sentence; a non-JSON failure (proxy 502 HTML
// page) shows a plain fallback, never the raw body or a status code.
const htmlRes = (status: number) => ({
  ok: false, status, statusText: "Bad Gateway",
  json: async () => { throw new SyntaxError("Unexpected token <") },
  text: async () => "<html><body><h1>502 Bad Gateway</h1></body></html>",
})
const lastError = () => String(h.toast.error.mock.calls.at(-1)?.[0])

it("a refused export shows the server's sentence", async () => {
  h.apiFetch.mockResolvedValueOnce(res(false, { error: "Only workspace owners can export member data." }, 403))
  render(<UserDataActions userId="u1" email={EMAIL} workspaceId="w1" />)
  fireEvent.click(screen.getByRole("button", { name: /Export data/ }))
  await waitFor(() => expect(h.toast.error).toHaveBeenCalled())
  expect(lastError()).toBe("Only workspace owners can export member data.")
})

it("an export that fails with an HTML page shows the plain fallback", async () => {
  h.apiFetch.mockResolvedValueOnce(htmlRes(502))
  render(<UserDataActions userId="u1" email={EMAIL} workspaceId="w1" />)
  fireEvent.click(screen.getByRole("button", { name: /Export data/ }))
  await waitFor(() => expect(h.toast.error).toHaveBeenCalled())
  expect(lastError()).toBe("Export failed. Nothing was downloaded.")
})

async function failErase(r: unknown) {
  h.apiFetch.mockResolvedValueOnce(r)
  const d = open()
  fireEvent.change(within(d).getByLabelText(/Reason/), { target: { value: "request #1" } })
  fireEvent.change(within(d).getByLabelText(/to confirm/), { target: { value: EMAIL } })
  fireEvent.click(erase(d))
  await waitFor(() => expect(h.toast.error).toHaveBeenCalled())
}

it("a refused erase shows the server's sentence", async () => {
  await failErase(res(false, { error: "Only workspace owners can erase member data." }, 403))
  expect(h.toast.error).toHaveBeenCalledTimes(1)
  expect(lastError()).toBe("Only workspace owners can erase member data.")
})

it("an erase that fails with an HTML page shows the plain fallback", async () => {
  await failErase(htmlRes(502))
  expect(h.toast.error).toHaveBeenCalledTimes(1)
  expect(lastError()).toBe("Erase failed. No data was removed.")
})
