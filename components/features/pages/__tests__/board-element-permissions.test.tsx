import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { BoardElementPermissions } from "../board-element-permissions"
import { apiFetch } from "@/lib/api-fetch"
import type { PageBoard } from "@/hooks/use-page-board"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const board: PageBoard = { format: "board/v1", seq: 3, can_write: true, placements: [], records: [{ element_id: "shape", element_rev: 2, author_user_id: "human", author_agent_id: "", deleted: false, element: { id: "shape", type: "rectangle" } }] }
const clients: QueryClient[] = []
const writes: { path: string; method: string; body: Record<string, unknown> | null }[] = []
let denied: boolean
let loseResponse: boolean
let grantFailure: number
let revokeDelegationFailure: boolean
beforeEach(() => {
  writes.length = 0; denied = false; loseResponse = false; grantFailure = 0; revokeDelegationFailure = false
  vi.mocked(apiFetch).mockImplementation(async (url, options) => {
    const path = new URL(String(url), "https://studio.test").pathname
    if (!options?.method) return new Response(JSON.stringify(denied ? { error: "Forbidden" } : { permissions: [], eligible_runs: [{ run_id: "run1", agent_id: "agent1", agent_name: "Designer", expires_at: "2099-01-01T00:00:00Z", needs_board_delegation: true }] }), { status: denied ? 403 : 200 })
    const body = options.body ? JSON.parse(String(options.body)) : null
    writes.push({ path, method: options.method, body })
    if (path.endsWith("/element-permissions") && loseResponse) { loseResponse = false; throw new Error("Response lost") }
    if (path.endsWith("/delegations/run1") && revokeDelegationFailure) return new Response(JSON.stringify({ error: "Cannot revoke delegation" }), { status: 403 })
    if (path.endsWith("/element-permissions") && grantFailure) return new Response(JSON.stringify({ error: "Selected drawing changed" }), { status: grantFailure })
    return new Response(JSON.stringify({ id: "permission1", used: false, revoked: false }), { status: 201 })
  })
})
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()) })
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } }); clients.push(client)
  const dirty = vi.fn()
  const content = (value: PageBoard) => <QueryClientProvider client={client}><BoardElementPermissions workspaceId="ws" slug="design" board={value} selectedIds={["shape"]} disabled={false} onDirtyChange={dirty} /></QueryClientProvider>
  const view = render(content(board))
  return { client, dirty, change: (value: PageBoard) => view.rerender(content(value)) }
}
async function compose() {
  fireEvent.click(screen.getByRole("button", { name: "Agent editing permissions" }))
  await screen.findByRole("option", { name: /Designer/ })
  fireEvent.change(screen.getByLabelText("Running agent to authorize"), { target: { value: "run1" } })
  fireEvent.change(screen.getByLabelText("Permission purpose"), { target: { value: "Align the selected drawing." } })
}
it("requires an explicit confirmation and sends only captured human IDs, versions and selected methods", async () => {
  mount(); await compose(); expect(writes).toHaveLength(0)
  fireEvent.click(screen.getByRole("button", { name: "Allow one change" }))
  await waitFor(() => expect(writes).toHaveLength(2))
  expect(writes[0]).toMatchObject({ path: "/api/v1/pages/design/board/delegations", body: { run_id: "run1" } })
  expect(writes[1]).toMatchObject({ path: "/api/v1/pages/design/board/element-permissions", body: { run_id: "run1", elements: [{ element_id: "shape", expected_rev: 2 }], methods: ["update"], purpose: "Align the selected drawing.", expires_in_seconds: 900 } })
  expect(writes[1].body?.mutation_id).toEqual(expect.any(String))
})
it("freezes an uncertain permission and retries identical scope even if the board changes", async () => {
  const view = mount(); await compose(); loseResponse = true
  fireEvent.click(screen.getByRole("button", { name: "Allow one change" })); await screen.findByRole("alert")
  expect(screen.getByLabelText("Permission purpose")).toBeDisabled()
  view.change({ ...board, seq: 4, records: [{ ...board.records[0], element_rev: 3 }] })
  fireEvent.click(screen.getByRole("button", { name: "Retry permission" }))
  await waitFor(() => expect(writes).toHaveLength(4))
  expect(writes[1].body).toEqual(writes[3].body)
  await waitFor(() => expect(view.dirty).toHaveBeenLastCalledWith(false))
})
it("hides retained permission data and private draft after current access fails", async () => {
  const view = mount(); await compose(); denied = true
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["board-element-permissions"] }) })
  await screen.findByText("Your access changed. Permissions and drafts are hidden.")
  expect(screen.queryByDisplayValue("Align the selected drawing.")).toBeNull()
  expect(screen.queryByRole("button", { name: "Allow one change" })).toBeNull()
  await waitFor(() => expect(view.client.getQueryData(["board-element-permissions", "ws", "design"])).toBeUndefined())
  denied = false
  fireEvent.click(screen.getByRole("button", { name: "Recheck editing access" }))
  await waitFor(() => expect(view.client.getQueryData<{eligible_runs:unknown[]}>(["board-element-permissions", "ws", "design"])?.eligible_runs).toHaveLength(1))
  expect(screen.queryByDisplayValue("Align the selected drawing.")).toBeNull()
})
it("does not offer grant controls to a read-only viewer", () => {
  const view = mount(); view.change({ ...board, can_write: false })
  expect(screen.queryByRole("button", { name: "Agent editing permissions" })).toBeNull()
  expect(writes).toHaveLength(0)
})

it("discloses confirmed delegation after scoped permission failure and revokes only by explicit action", async () => {
  const view = mount(); await compose(); grantFailure = 409
  fireEvent.click(screen.getByRole("button", { name: "Allow one change" }))
  await screen.findByText(/Board delegation was granted, but the selected-drawing permission has not been confirmed/)
  await screen.findByText("Selected drawing changed")
  expect(screen.getByLabelText("Permission purpose")).toBeDisabled()
  expect(writes).toHaveLength(2)
  view.change({ ...board, seq: 4, records: [{ ...board.records[0], element_rev: 3 }] })
  fireEvent.click(screen.getByRole("button", { name: "Allow one change" }))
  await waitFor(() => expect(writes).toHaveLength(4))
  expect(writes[1].body).toEqual(writes[3].body)
  expect(screen.getByText(/Board delegation was granted/)).toBeTruthy()
  expect(writes.some(item => item.method === "DELETE")).toBe(false)
  fireEvent.click(screen.getByRole("button", { name: "Revoke granted run delegation" }))
  await waitFor(() => expect(writes.at(-1)).toMatchObject({ method: "DELETE", path: "/api/v1/pages/design/board/delegations/run1" }))
  await waitFor(() => expect(screen.queryByText(/Board delegation was granted/)).toBeNull())
  expect(screen.queryByDisplayValue("Align the selected drawing.")).toBeNull()
})

it("purges cached runs after base-delegation revoke denial and resets the error only through fresh recheck", async () => {
  const view = mount(); await compose(); grantFailure = 409
  fireEvent.click(screen.getByRole("button", { name: "Allow one change" }))
  await screen.findByText("Selected drawing changed")
  revokeDelegationFailure = true
  fireEvent.click(screen.getByRole("button", { name: "Revoke granted run delegation" }))
  await screen.findByText("Your access changed. Permissions and drafts are hidden.")
  await waitFor(() => expect(view.client.getQueryData(["board-element-permissions", "ws", "design"])).toBeUndefined())
  expect(screen.getByText(/Board delegation was granted/)).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Recheck editing access" }))
  await waitFor(() => expect(screen.queryByText("Cannot revoke delegation")).toBeNull())
  await waitFor(() => expect(view.client.getQueryData(["board-element-permissions", "ws", "design"])).toBeDefined())
})
