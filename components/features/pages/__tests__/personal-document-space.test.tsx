import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { toPageView, usePages } from "@/hooks/use-pages"
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
 const requested = new URL(request.mock.calls[0][0], "http://localhost")
 expect(requested.searchParams.get("audience")).toBe("personal")
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

function AllPages() {
 const { pages } = usePages("workspace-a")
 return <p>All catalog count: {pages.length}</p>
}
it("keeps the personal server projection separate from the all-pages cache", async () => {
 request.mockImplementation(async (input: string) => {
  const personal = new URL(input, "http://localhost").searchParams.get("audience") === "personal"
  return Response.json([row("Own private", { private_audience: true }), ...(personal ? [] : [row("Shared page", { private_audience: false })])])
 })
 const client = new QueryClient({ defaultOptions: { queries: { retry: false } } }); clients.push(client)
 render(<QueryClientProvider client={client}><AllPages /><PersonalDocumentSpace workspaceId="workspace-a" onSelect={vi.fn()} /></QueryClientProvider>)
 await screen.findByText("All catalog count: 2")
 await screen.findByRole("button", { name: "Own private Doc" })
 expect(request).toHaveBeenCalledTimes(2)
 expect(client.getQueryCache().getAll().filter(query => query.queryKey[0] === "pages")).toHaveLength(2)
})

it("counts and shows nothing for items the server did not admit or that are not the caller's", async () => {
 request.mockResolvedValue(Response.json([row("Mine", { private_audience: true }), row("Other human", { private_audience: true, owner: "user/other" }), row("Unclassified")]))
 mount()
 await screen.findByText("Mine")
 expect(screen.queryByText("Other human")).toBeNull()
 expect(screen.queryByText("Unclassified")).toBeNull()
 expect(document.body.textContent).not.toMatch(/Other human|Unclassified/)
 expect(screen.queryByRole("group", { name: "Scope" })).toBeNull()
})
it("wires Duplicate once per click and opens the copy by its new ID", async () => {
 vi.stubEnv("NEXT_PUBLIC_ARTIFACT_COPY_MOVE", "true")
 let finish!: () => void
 request.mockImplementation(async (url: string, init?: RequestInit) => {
  if (init?.method === "POST") { await new Promise<void>(resolve => { finish = resolve }); return Response.json({ page: { id: "copy-id", slug: "copy" } }, { status: 201 }) }
  return Response.json([row("Own private", { id: "src-id", private_audience: true }), row("No id", { id: "bad id!", private_audience: true })])
 })
 const view = mount()
 const button = await screen.findByRole("button", { name: "Duplicate Own private" })
 expect(screen.queryByRole("button", { name: "Duplicate No id" })).toBeNull()
 fireEvent.click(button); fireEvent.click(button)
 await waitFor(() => expect(request.mock.calls.filter(c => c[1]?.method === "POST")).toHaveLength(1))
 await act(async () => { finish() })
 await waitFor(() => expect(view.select).toHaveBeenCalledWith("@copy-id"))
 vi.unstubAllEnvs()
})

it("replays the same mutation key when a failed Duplicate is retried", async () => {
 vi.stubEnv("NEXT_PUBLIC_ARTIFACT_COPY_MOVE", "true")
 const keys: string[] = []
 let attempt = 0
 request.mockImplementation(async (url: string, init?: RequestInit) => {
  if (init?.method === "POST") { keys.push((init.headers as Record<string, string>)["Idempotency-Key"]); return attempt++ === 0 ? Response.json({ error: "Unavailable" }, { status: 503 }) : Response.json({ page: { id: "copy-id", slug: "copy" } }, { status: 201 }) }
  return Response.json([row("Own private", { id: "src-id", private_audience: true })])
 })
 mount()
 const button = await screen.findByRole("button", { name: "Duplicate Own private" })
 fireEvent.click(button)
 await screen.findByRole("alert")
 fireEvent.click(button)
 await waitFor(() => expect(keys).toHaveLength(2))
 expect(keys[1]).toBe(keys[0])
 vi.unstubAllEnvs()
})
