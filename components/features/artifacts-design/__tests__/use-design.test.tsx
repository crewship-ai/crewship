import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { useDesign } from "../use-design"
import { designNodes, upsertDesignNode } from "../design-document"
const server = vi.hoisted(() => ({ pageId: "old-private-page", urls: [] as string[], calls: [] as { url: string; method: string }[] }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "same-user", name: "Owner" } } }) }))
vi.mock("@/lib/server-base", () => ({ resolveWsBase: () => "ws://localhost" }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: async (url: string, init?: RequestInit) => {
 server.calls.push({ url, method: init?.method ?? "GET" })
 if (init?.method === "POST" && url.includes("/@old-private-page/") && server.pageId !== "old-private-page") return new Response(JSON.stringify({ error: "not found" }), { status: 404 })
 const common = { latest_version: 1, shared_version: 0 }
 const data = url.includes("/comments?") ? { comments: [] } : url.includes("/versions?") ? { ...common, versions: [] } : { ...common, page_id: server.pageId, can_edit: true, version: 1, nodes: [], order: [], actor: { id: "same-user", name: "Owner" } }
 return new Response(JSON.stringify(data), { status: 200, headers: { "Content-Type": "application/json" } })
} }))
vi.mock("../design-provider", () => ({ DesignProvider: class {
 constructor(_doc: unknown, url: string) { server.urls.push(url) }
 awareness = { on: () => {}, off: () => {}, getStates: () => new Map() }
 ready = true
 setEditable() {}
 destroy() {}
 setCursor() {}
 synchronize() { return Promise.resolve() }
} }))
const drafts = new Map<string, string>()
beforeEach(() => {
 drafts.clear()
 server.urls = []
 server.calls = []
 vi.mocked(localStorage.getItem).mockImplementation(key => drafts.get(key) ?? null)
 vi.mocked(localStorage.setItem).mockImplementation((key, value) => { drafts.set(key, value) })
})
const clients: QueryClient[] = []
function mount() {
 const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
 clients.push(client)
 return renderHook(() => useDesign("same-workspace", "reused-slug"), { wrapper: ({ children }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
}
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()); drafts.clear(); vi.mocked(localStorage.getItem).mockReturnValue(null); vi.mocked(localStorage.setItem).mockImplementation(() => {}) })
describe("Design local draft identity", () => {
 it("pins private mutation requests to the loaded Page during the slug reuse refetch gap", async () => {
  server.pageId = "old-private-page"
  const view = mount()
  await waitFor(() => expect(view.result.current.doc).not.toBeNull())
  const live = view.result.current
  server.pageId = "new-shared-page"
  let outcomes: PromiseSettledResult<unknown>[] = []
  await act(async () => {
   outcomes = await Promise.allSettled([
    live.save.mutateAsync("Private old draft"), live.restore.mutateAsync(1), live.share.mutateAsync(1), live.connectAgent.mutateAsync("private-chat"),
    live.comment.mutateAsync({ version: 1, path: "design.json", selector: "same-node", quote: "private", text: "Private feedback", send_to_agent: false }),
   ])
  })
  const writes = server.calls.filter(call => call.method === "POST")
  expect(writes).toHaveLength(5)
  expect(writes.every(call => call.url.startsWith("/api/v1/pages/@old-private-page/design/"))).toBe(true)
  expect(outcomes.every(outcome => outcome.status === "rejected")).toBe(true)
 })
 it("retires the live document when the same URL resolves to a different immutable Page", async () => {
  server.pageId = "old-private-page"
  const view = mount()
  await waitFor(() => expect(view.result.current.doc).not.toBeNull())
  const previous = view.result.current.doc!
  expect(server.urls.at(-1)).toBe("ws://localhost/api/v1/pages/@old-private-page/design/ws?workspace_id=same-workspace")
  act(() => upsertDesignNode(previous, { id: "confidential", type: "text", text: "Old private canvas", x: 0, y: 0, width: 200, height: 40, fill: "#111111" }))
  server.pageId = "new-shared-page"
  await act(async () => { await clients[0].invalidateQueries({ queryKey: ["artifact-design"] }) })
  await waitFor(() => { expect(view.result.current.doc).not.toBeNull(); expect(view.result.current.doc).not.toBe(previous) })
  expect(designNodes(view.result.current.doc!)).toEqual([])
  expect(server.urls.at(-1)).toBe("ws://localhost/api/v1/pages/@new-shared-page/design/ws?workspace_id=same-workspace")
 })
 it("never uploads a previous Page draft when another Page reuses its slug", async () => {
  server.pageId = "old-private-page"
  const old = mount()
  await waitFor(() => expect(old.result.current.doc).not.toBeNull())
  act(() => upsertDesignNode(old.result.current.doc!, { id: "confidential", type: "text", text: "Private old canvas", x: 0, y: 0, width: 200, height: 40, fill: "#111111" }))
  expect(drafts.size).toBe(1)
  old.unmount()
  const legacyKey = "crewship-design:same-user:same-workspace:reused-slug"
  const legacyDraft = [...drafts.values()][0]
  drafts.set(legacyKey, legacyDraft)
  server.pageId = "new-shared-page"
  const fresh = mount()
  await waitFor(() => expect(fresh.result.current.doc).not.toBeNull())
  expect(designNodes(fresh.result.current.doc!)).toEqual([])
  expect(drafts.get(legacyKey)).toBe(legacyDraft)
 })
})
