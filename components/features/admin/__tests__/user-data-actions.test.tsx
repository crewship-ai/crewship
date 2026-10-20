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
