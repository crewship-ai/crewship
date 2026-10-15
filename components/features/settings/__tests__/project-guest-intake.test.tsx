import { afterEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ProjectGuestIntake } from "../project-guest-intake"

const fetch = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetch }))
const initial = { project_id: "p1", crew_id: "crew-1", crew_name: "Original crew", revision: 4 }
const crews = [{ id: "crew-1", name: "Original crew" }, { id: "crew-2", name: "Client intake" }]
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
afterEach(() => { cleanup(); fetch.mockReset() })
function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const busy = vi.fn()
  render(<QueryClientProvider client={client}><ProjectGuestIntake workspaceId="ws" projectId="p1" disabled={false} onBusy={busy} /></QueryClientProvider>)
  return { client, busy }
}
it("uses a named crew and current revision, without reissuing any grants", async () => {
  fetch.mockImplementation(async (path: string, init?: RequestInit) => response(init?.method === "PUT" ? { ...initial, crew_id: "crew-2", crew_name: "Client intake", revision: 5 } : path.includes("/crews?") ? crews : initial))
  const { busy } = show()
  const select = await screen.findByLabelText("Issue intake crew")
  fireEvent.change(select, { target: { value: "crew-2" } })
  fireEvent.click(screen.getByRole("button", { name: "Save intake crew" }))
  await waitFor(() => expect(screen.getByRole("button", { name: "Save intake crew" })).toBeDisabled())
  const writes = fetch.mock.calls.filter(([, init]) => init?.method === "PUT")
  expect(writes).toHaveLength(1)
  expect(writes[0][0]).toBe("/api/v1/workspaces/ws/projects/p1/guest-intake")
  expect(JSON.parse(String(writes[0][1].body))).toEqual({ crew_id: "crew-2", revision: 4 })
  expect(busy.mock.calls).toEqual([[true], [false]])
})
it("keeps a refused crew choice and never retries a conflict", async () => {
  let revision = 4
  fetch.mockImplementation(async (path: string, init?: RequestInit) => response(init?.method === "PUT" ? { error: "stale" } : path.includes("/crews?") ? crews : { ...initial, revision }, init?.method === "PUT" ? 409 : 200))
  show()
  fireEvent.change(await screen.findByLabelText("Issue intake crew"), { target: { value: "crew-2" } })
  fireEvent.click(screen.getByRole("button", { name: "Save intake crew" }))
  await screen.findByText("Issue intake changed. Reload intake before saving.")
  expect(screen.getByLabelText("Issue intake crew")).toHaveValue("crew-2")
  expect(screen.getByRole("button", { name: "Save intake crew" })).toBeDisabled()
  expect(fetch.mock.calls.filter(([, init]) => init?.method === "PUT")).toHaveLength(1)
  revision = 6
  fireEvent.click(screen.getByRole("button", { name: "Reload intake" }))
  await waitFor(() => expect(screen.getByLabelText("Issue intake crew")).toHaveValue("crew-1"))
})
it("does not turn a failed or malformed read into an editable empty configuration", async () => {
  fetch.mockResolvedValue(response({ unexpected: true }))
  show()
  await screen.findByRole("alert")
  expect(screen.queryByLabelText("Issue intake crew")).toBeNull()
  expect(fetch.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false)
})
