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
  expect(view.client.getQueryData(["document-chat-results", "ws /&", "chat", "human"])).toEqual({ results: [] })
})
it("retains the last authorized card with an honest warning on transient failure", async () => {
  const view = mount()
  await screen.findByText("Finished plan")
  status = 503
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["document-chat-results"] }) })
  expect(screen.getByText("Finished plan")).toBeTruthy()
  expect(await screen.findByText(/Showing the last authorized results/)).toBeTruthy()
})

it("never shows the previous human's private card while the next identity waits or fails", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000 } } })
  clients.push(client)
  let rejectNext!: (error: Error) => void
  const nextResponse = new Promise<Response>((_resolve, reject) => { rejectNext = reject })
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify({ results: [row] })))
    .mockImplementation(() => nextResponse)
  const content = (userId: string) => <QueryClientProvider client={client}><DocumentResultCards workspaceId="same-workspace" chatId="same-chat" isStreaming={false} currentUserId={userId} documentsEnabled /></QueryClientProvider>
  const view = render(content("human-a"))
  await screen.findByRole("link", { name: "Open document Finished plan" })
  view.rerender(content("human-b"))
  expect(screen.queryByText("Finished plan")).toBeNull()
  expect(screen.queryByRole("link", { name: "Open document Finished plan" })).toBeNull()
  await waitFor(() => expect(vi.mocked(apiFetch).mock.calls.length).toBeGreaterThanOrEqual(2))
  await act(async () => { rejectNext(new Error("synthetic next-user network failure")) })
  await waitFor(() => expect(client.isFetching()).toBe(0))
  expect(screen.queryByText("Finished plan")).toBeNull()
  expect(screen.queryByRole("link", { name: "Open document Finished plan" })).toBeNull()
})

it("immediately hides authorized cards when the current human identity becomes unavailable", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000 } } })
  clients.push(client)
  const content = (userId?: string) => <QueryClientProvider client={client}><DocumentResultCards workspaceId="workspace" chatId="chat" isStreaming={false} currentUserId={userId} documentsEnabled /></QueryClientProvider>
  const view = render(content("human"))
  await screen.findByRole("link", { name: "Open document Finished plan" })
  const calls = vi.mocked(apiFetch).mock.calls.length
  view.rerender(content())
  expect(screen.queryByText("Finished plan")).toBeNull()
  expect(screen.queryByRole("link", { name: "Open document Finished plan" })).toBeNull()
  expect(vi.mocked(apiFetch).mock.calls).toHaveLength(calls)
})
