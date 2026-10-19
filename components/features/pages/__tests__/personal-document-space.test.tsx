import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { toPageView } from "@/hooks/use-pages"
import { PersonalDocumentSpace } from "../personal-document-space"

const identity = vi.hoisted(() => ({ userId: "human-a" as string | null }))
const request = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: identity.userId ? { user: { id: identity.userId } } : null, status: identity.userId ? "authenticated" : "loading" }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeChannelSafe: vi.fn(), useRealtimeEventSafe: vi.fn(), useRealtimeStatusSafe: () => "disconnected" }))
const clients: QueryClient[] = []
const row = (id: string, extra = {}) => ({ id, slug: id, name: id, owner: "user/human-a", has_document: true, panels: [], ...extra })
beforeEach(() => { identity.userId = "human-a"; request.mockReset() })
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()) })
function mount(workspace = "workspace-a") {
 const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000 } } }); clients.push(client)
 const select = vi.fn()
 const content = (ws: string) => <QueryClientProvider client={client}><PersonalDocumentSpace workspaceId={ws} onSelect={select} /></QueryClientProvider>
 return { ...render(content(workspace)), content, select, client }
}
it("keeps absent and malformed audience classification unknown rather than private", () => {
 expect(toPageView(row("old")).privateAudience).toBeUndefined()
 expect(toPageView(row("bad", { private_audience: "true" } as never)).privateAudience).toBeUndefined()
 expect(toPageView(row("private", { private_audience: true })).privateAudience).toBe(true)
 expect(toPageView(row("shared", { private_audience: false })).privateAudience).toBe(false)
})
it("reuses the catalog for own explicitly private Documents only", async () => {
 request.mockResolvedValue(Response.json([row("Own private", { id: "immutable-own", slug: "reused-address", private_audience: true }), row("Own legacy"), row("Own shared", { private_audience: false }), row("Foreign custody", { private_audience: true, owner: "user/other" }), row("Not Document", { private_audience: true, has_document: false })]))
 const view = mount()
 fireEvent.click(await screen.findByRole("button", { name: "Own private Doc" }))
 expect(view.select).toHaveBeenCalledWith("@immutable-own")
 for (const name of ["Own legacy", "Own shared", "Foreign custody", "Not Document"]) expect(screen.queryByText(name)).toBeNull()
 expect(screen.getByRole("region", { name: "My space" })).toBeTruthy()
})
it.each(["human", "workspace"])("masks old titles synchronously on %s change and failure", async kind => {
 request.mockResolvedValue(Response.json([row("Protected title", { private_audience: true })]))
 const view = mount(); await screen.findByText("Protected title")
 let finish!: (response: Response) => void
 request.mockImplementation(() => new Promise<Response>(resolve => { finish = resolve }))
 if (kind === "human") identity.userId = "human-b"
 view.rerender(view.content(kind === "workspace" ? "workspace-b" : "workspace-a"))
 expect(screen.queryByText("Protected title")).toBeNull()
 await waitFor(() => expect(request).toHaveBeenCalledTimes(2))
 await act(async () => { finish(Response.json({ error: "Unavailable" }, { status: 503 })) })
 await waitFor(() => expect(view.client.isFetching()).toBe(0))
 expect(screen.queryByText("Protected title")).toBeNull()
})
it("does not fetch without a current human and shows an honest empty state", async () => {
 identity.userId = null; const view = mount(); expect(request).not.toHaveBeenCalled()
 identity.userId = "human-a"; request.mockResolvedValue(Response.json([])); view.rerender(view.content("workspace-a"))
 await screen.findByText("No private Documents in your space.")
})

it.each([false, true])("reports unknown own Document classification without claiming empty (known private present=%s)", async mixed => {
 request.mockResolvedValue(Response.json([row("Unknown audience title"), ...(mixed ? [row("Known private", { private_audience: true })] : [])]))
 mount()
 await screen.findByText("Document audience information is unavailable for some of your Documents.")
 expect(screen.queryByText("No private Documents in your space.")).toBeNull()
 expect(screen.queryByText("Unknown audience title")).toBeNull()
 if (mixed) expect(screen.getByRole("button", { name: "Known private Doc" })).toBeTruthy()
})
