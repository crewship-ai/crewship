import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { usePages } from "@/hooks/use-pages"
import { DocumentCatalog } from "../document-catalog"

const identity = vi.hoisted(() => ({ userId: "human-a" as string | null }))
const request = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: identity.userId ? { user: { id: identity.userId } } : null, status: identity.userId ? "authenticated" : "loading" }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeChannelSafe: vi.fn(), useRealtimeEventSafe: vi.fn(), useRealtimeStatusSafe: () => "disconnected" }))
const clients: QueryClient[] = []
beforeEach(() => {
  identity.userId = "human-a"
  request.mockResolvedValue(Response.json([{ id: "private-doc", slug: "private", name: "A private Document", has_document: true, panels: [] }]))
})
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()); request.mockReset() })
function Catalog() { return <DocumentCatalog pages={usePages("workspace").pages} onSelect={vi.fn()} /> }
it("removes the previous human's real catalog title while another human waits and then fails", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000 } } })
  clients.push(client)
  const content = () => <QueryClientProvider client={client}><Catalog /></QueryClientProvider>
  const view = render(content())
  await screen.findByRole("button", { name: "A private Document Doc" })
  let finish!: (response: Response) => void
  request.mockImplementation(() => new Promise<Response>(resolve => { finish = resolve }))
  identity.userId = "human-b"
  view.rerender(content())
  expect(screen.queryByText("A private Document")).toBeNull()
  await waitFor(() => expect(request).toHaveBeenCalledTimes(2))
  await act(async () => { finish(Response.json({ error: "Unavailable" }, { status: 503 })) })
  await waitFor(() => expect(client.isFetching()).toBe(0))
  expect(screen.queryByRole("button", { name: "A private Document Doc" })).toBeNull()
})
