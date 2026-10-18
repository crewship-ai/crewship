import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { DocumentResultCards } from "../document-result-cards"
import { apiFetch } from "@/lib/api-fetch"
import type { ViewerTarget } from "../artifact-viewer/types"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
const row = { page_id: "saved-doc", type: "document", page_slug: "old-address", title: "Finished plan", revision: 0, created_at: "2026-10-17T00:00:00Z", agent_id: "agent", agent_name: "Ada", run_id: "expired-run", on_behalf_of_user_id: "human" }
let status = 200
const clients: QueryClient[] = []
beforeEach(() => { status = 200; vi.mocked(apiFetch).mockImplementation(async () => new Response(JSON.stringify(status === 200 ? { results: [row] } : { error: "Unavailable" }), { status })) })
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()); vi.clearAllMocks() })
function mount(enabled = true, onOpenDocument?: (target: ViewerTarget, opener: HTMLAnchorElement) => void) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  clients.push(client)
  const content = (available: boolean) => <QueryClientProvider client={client}><DocumentResultCards workspaceId="ws /&" chatId="chat" isStreaming={false} currentUserId="human" documentsEnabled={available} onOpenDocument={onOpenDocument} /></QueryClientProvider>
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

it("intercepts only a plain click and passes the saved identity plus actual anchor", async () => {
  const open = vi.fn(); mount(true, open)
  const link = await screen.findByRole("link", { name: "Open document Finished plan" })
  const event = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 })
  act(() => { link.querySelector("p")!.dispatchEvent(event) })
  expect(event.defaultPrevented).toBe(true)
  expect(open).toHaveBeenCalledExactlyOnceWith({ workspaceId: "ws /&", pageId: "saved-doc", type: "document", mode: "latest" }, link)
  expect(link).toHaveAttribute("href", "/pages?ref=saved-doc&workspace_id=ws+%2F%26")
  expect(link).toHaveAttribute("target", "_blank")
  expect(link).toHaveAttribute("rel", "noopener noreferrer")
  expect(vi.mocked(apiFetch).mock.calls.every(call => !call[1]?.method)).toBe(true)
})

it.each([{ctrlKey:true},{metaKey:true},{shiftKey:true},{altKey:true},{button:1},{button:2}])("keeps native fallback for modified/non-left clicks: %j", async init => {
  const open = vi.fn(); mount(true, open)
  const link = await screen.findByRole("link", { name: "Open document Finished plan" })
  const event = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0, ...init })
  act(() => { link.dispatchEvent(event) })
  expect(event.defaultPrevented).toBe(false)
  expect(open).not.toHaveBeenCalled()
})

it("keeps auxiliary clicks and plain clicks without a callback native", async () => {
  const open = vi.fn(); const view = mount(true, open)
  const link = await screen.findByRole("link", { name: "Open document Finished plan" })
  const auxiliary = new MouseEvent("auxclick", { bubbles: true, cancelable: true, button: 1 })
  act(() => { link.dispatchEvent(auxiliary) })
  expect(auxiliary.defaultPrevented).toBe(false); expect(open).not.toHaveBeenCalled()
  view.availability(false)
  mount()
  const native = await screen.findByRole("link", { name: "Open document Finished plan" })
  const plain = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 })
  act(() => { native.dispatchEvent(plain) })
  expect(plain.defaultPrevented).toBe(false)
})

it("does not override a click already consumed by another control", async () => {
  const open = vi.fn(); mount(true, open)
  const link = await screen.findByRole("link", { name: "Open document Finished plan" })
  const event = new MouseEvent("click", { bubbles: true, cancelable: true })
  event.preventDefault()
  act(() => { link.dispatchEvent(event) })
  expect(open).not.toHaveBeenCalled()
})

it("hides the old workspace card while the next workspace query is pending", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } }); clients.push(client)
  const open = vi.fn()
  const content = (workspace: string) => <QueryClientProvider client={client}><DocumentResultCards workspaceId={workspace} chatId="chat" isStreaming={false} currentUserId="human" documentsEnabled onOpenDocument={open} /></QueryClientProvider>
  const view = render(content("workspace-a"))
  await screen.findByRole("link", { name: "Open document Finished plan" })
  vi.mocked(apiFetch).mockReturnValue(new Promise(() => {}))
  view.rerender(content("workspace-b"))
  expect(screen.queryByText("Finished plan")).toBeNull()
  expect(open).not.toHaveBeenCalled()
})
