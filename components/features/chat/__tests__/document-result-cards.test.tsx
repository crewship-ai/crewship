import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { DocumentResultCards } from "../document-result-cards"
import { apiFetch } from "@/lib/api-fetch"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
const row = { page_id: "saved-doc", type: "document", page_slug: "old-address", title: "Finished plan", revision: 0, created_at: "2026-10-17T00:00:00Z", agent_id: "agent", agent_name: "Ada", run_id: "expired-run", on_behalf_of_user_id: "human" }
let status = 200
const clients: QueryClient[] = []
beforeEach(() => { status = 200; vi.mocked(apiFetch).mockImplementation(async () => new Response(JSON.stringify(status === 200 ? { results: [row] } : { error: "Unavailable" }), { status })) })
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()); vi.clearAllMocks() })
function mount(enabled = true) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  clients.push(client)
  const content = (available: boolean) => <QueryClientProvider client={client}><DocumentResultCards workspaceId="ws /&" chatId="chat" isStreaming={false} currentUserId="human" documentsEnabled={available} /></QueryClientProvider>
  const view = render(content(enabled))
  return { client, availability: (available: boolean) => view.rerender(content(available)) }
}
it("opens the durable native Document identity with truthful provenance and no writes", async () => {
  mount()
  const link = await screen.findByRole("link", { name: "Open document Finished plan" })
  expect(link.getAttribute("href")).toBe("/pages?ref=saved-doc&workspace_id=ws+%2F%26")
  expect(screen.getByText("Document · Ada")).toBeTruthy()
  expect(screen.getByText("On behalf of you")).toBeTruthy()
  expect(screen.queryByText(/Revision 0|static artifact/i)).toBeNull()
  expect(vi.mocked(apiFetch).mock.calls.every(call => !call[1]?.method)).toBe(true)
})
it("is default-off and hides cached cards when Documents availability is withdrawn", async () => {
  const view = mount(false)
  expect(apiFetch).not.toHaveBeenCalled()
  expect(screen.queryByRole("region")).toBeNull()
  view.availability(true)
  await screen.findByText("Finished plan")
  view.availability(false)
  expect(screen.queryByText("Finished plan")).toBeNull()
})
it("removes private content and cache after a current authorization refusal", async () => {
  const view = mount()
  await screen.findByText("Finished plan")
  status = 403
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["document-chat-results"] }) })
  await waitFor(() => expect(screen.queryByText("Finished plan")).toBeNull())
  expect(view.client.getQueryData(["document-chat-results", "ws /&", "chat"])).toEqual({ results: [] })
})
it("retains the last authorized card with an honest warning on transient failure", async () => {
  const view = mount()
  await screen.findByText("Finished plan")
  status = 503
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["document-chat-results"] }) })
  expect(screen.getByText("Finished plan")).toBeTruthy()
  expect(await screen.findByText(/Showing the last authorized results/)).toBeTruthy()
})
