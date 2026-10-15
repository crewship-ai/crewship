import { afterEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { RestrictedRoutineDefinitions } from "../restricted-routine-definitions"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-auth", () => ({ useSession: () => ({ data: { user: { id: "viewer" } } }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
afterEach(() => { cleanup(); api.mockReset() })
const item = { id: "r1", slug: "example", name: "Shared recipe", status: "active", can_view: true, can_run: false, runtime_ready: false }
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}><RestrictedRoutineDefinitions workspaceId="w" /></QueryClientProvider>)
}
it("shows a View-only definition without a runtime or invocation controls", async () => {
  api.mockImplementation(async (url: string) => response(url.endsWith("/r1") ? { ...item, definition: { steps: [{ prompt: "Explicitly shared prompt" }] }, definition_hash: "hash", version: 2 } : [item, { ...item, id: "run-only", name: "Run only", can_view: false }]))
  show()
  await screen.findByRole("option", { name: "Shared recipe" })
  expect(screen.queryByRole("option", { name: "Run only" })).toBeNull()
  fireEvent.change(screen.getByRole("combobox", { name: "Routine definition" }), { target: { value: "r1" } })
  await screen.findAllByText(/Explicitly shared prompt/)
  expect(screen.queryByRole("button", { name: /run|publish|edit/i })).toBeNull()
  expect(api.mock.calls.every(([, options]) => !options?.method)).toBe(true)
})
it("clears the visible definition when a refresh refuses access", async () => {
  let revoked = false
  api.mockImplementation(async (url: string) => revoked ? response({}, 404) : response(url.endsWith("/r1") ? { ...item, definition: { private: "OLD_DEFINITION_CANARY" }, definition_hash: "hash", version: 1 } : [item]))
  show()
  await screen.findByRole("option", { name: "Shared recipe" })
  fireEvent.change(screen.getByRole("combobox"), { target: { value: "r1" } })
  await screen.findByText(/OLD_DEFINITION_CANARY/)
  revoked = true
  fireEvent.click(screen.getByRole("button", { name: "Refresh definitions" }))
  await waitFor(() => expect(screen.queryByText(/OLD_DEFINITION_CANARY/)).toBeNull())
  expect(screen.getByRole("alert")).toBeInTheDocument()
})
it("rejects a malformed or mismatched definition response", async () => {
  api.mockImplementation(async (url: string) => response(url.endsWith("/r1") ? { ...item, id: "another", definition: { private: "FOREIGN_DEFINITION_CANARY" }, definition_hash: "hash", version: 1 } : [item]))
  show()
  await screen.findByRole("option", { name: "Shared recipe" })
  fireEvent.change(screen.getByRole("combobox"), { target: { value: "r1" } })
  await screen.findByRole("alert")
  expect(screen.queryByText(/FOREIGN_DEFINITION_CANARY/)).toBeNull()
})
