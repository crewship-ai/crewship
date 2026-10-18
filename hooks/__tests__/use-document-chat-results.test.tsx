import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { useDocumentChatResults } from "@/hooks/use-document-chat-results"

const events = vi.hoisted(() => new Map<string, () => void>())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: (event: string, callback: () => void) => events.set(event, callback) }))
const row = { page_id: "saved-doc", type: "document", page_slug: "old-slug", title: "Saved plan", revision: 0, created_at: "2026-10-17T00:00:00Z", agent_id: "agent", agent_name: "Ada", run_id: "expired-run", on_behalf_of_user_id: "human" }
const clients: QueryClient[] = []
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
beforeEach(() => { vi.mocked(apiFetch).mockReset(); events.clear(); vi.mocked(apiFetch).mockResolvedValue(response({ results: [row] })) })
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()) })
function mount(enabled = true) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  clients.push(client)
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  const view = renderHook(({ chat, available }) => useDocumentChatResults("ws /&", chat, "human", false, available), { wrapper, initialProps: { chat: "chat /&", available: enabled } })
  return { ...view, client }
}
it("does not query while Documents is unavailable and masks cached results on withdrawal", async () => {
  const view = mount(false)
  expect(apiFetch).not.toHaveBeenCalled()
  await act(async () => { await view.result.current.refetch() })
  expect(apiFetch).not.toHaveBeenCalled()
  view.rerender({ chat: "chat /&", available: true })
  await waitFor(() => expect(view.result.current.data?.results).toHaveLength(1))
  const calls = vi.mocked(apiFetch).mock.calls.length
  view.rerender({ chat: "chat /&", available: false })
  expect(view.result.current.data).toBeUndefined()
  expect(vi.mocked(apiFetch).mock.calls).toHaveLength(calls)
})
it.each([401, 403, 404])("clears formerly authorized cache after credential/chat denial %s", async status => {
  const view = mount()
  await waitFor(() => expect(view.result.current.data?.results).toHaveLength(1))
  vi.mocked(apiFetch).mockResolvedValue(response({ error: "Denied" }, status))
  await act(async () => { await view.result.current.refetch() })
  await waitFor(() => expect(view.result.current.data?.results).toEqual([]))
  expect(view.client.getQueryData(["document-chat-results", "ws /&", "chat /&", "human"])).toEqual({ results: [] })
})
it("uses encoded chat/workspace scope and invalidates verified results on realtime completion", async () => {
  const view = mount()
  await waitFor(() => expect(view.result.current.data?.results).toHaveLength(1))
  expect(vi.mocked(apiFetch).mock.calls[0][0]).toBe("/api/v1/chats/chat%20%2F%26/document-results?workspace_id=ws+%2F%26")
  vi.mocked(apiFetch).mockResolvedValue(response({ results: [] }))
  act(() => { events.get("run.completed")?.() })
  await waitFor(() => expect(view.result.current.data?.results).toEqual([]))
})
it("accepts only stable Document results, never artifact revisions or model claims", async () => {
  vi.mocked(apiFetch).mockResolvedValue(response({ results: [row, { ...row, type: "artifact", revision: 4 }, { ...row, page_id: "" }, { ...row, run_id: "" }, { ...row, type: undefined }] }))
  const view = mount()
  await waitFor(() => expect(view.result.current.isSuccess).toBe(true))
  expect(view.result.current.data?.results).toEqual([row])
})

it("retains authorized durable provenance when the former human identity is unavailable", async () => {
  vi.mocked(apiFetch).mockResolvedValue(response({ results: [{ ...row, on_behalf_of_user_id: "" }] }))
  const view = mount()
  await waitFor(() => expect(view.result.current.isSuccess).toBe(true))
  expect(view.result.current.data?.results).toHaveLength(1)
})

it("does not query or expose cache without a current human identity", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  clients.push(client)
  client.setQueryData(["document-chat-results", "workspace", "chat", "human-a"], { results: [row] })
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  const view = renderHook(() => useDocumentChatResults("workspace", "chat", null, false, true), { wrapper })
  expect(view.result.current.data).toBeUndefined()
  await act(async () => { await view.result.current.refetch() })
  expect(apiFetch).not.toHaveBeenCalled()
  expect(view.result.current.data).toBeUndefined()
})

it("partitions private cache by the current human before the next identity fetch resolves", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000 } } })
  clients.push(client)
  let rejectNext!: (error: Error) => void
  const nextResponse = new Promise<Response>((_resolve, reject) => { rejectNext = reject })
  vi.mocked(apiFetch).mockResolvedValueOnce(response({ results: [row] })).mockImplementation(() => nextResponse)
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  const view = renderHook(({ userId }) => useDocumentChatResults("workspace", "chat", userId, false, true), { wrapper, initialProps: { userId: "human-a" } })
  await waitFor(() => expect(view.result.current.data?.results).toEqual([row]))
  view.rerender({ userId: "human-b" })
  expect(view.result.current.data).toBeUndefined()
  await waitFor(() => expect(vi.mocked(apiFetch).mock.calls.length).toBeGreaterThanOrEqual(2))
  await act(async () => { rejectNext(new Error("synthetic next-user network failure")) })
  await waitFor(() => expect(view.result.current.isError).toBe(true))
  expect(view.result.current.data).toBeUndefined()
  expect(client.getQueryData(["document-chat-results", "workspace", "chat", "human-b"])).toBeUndefined()
})
