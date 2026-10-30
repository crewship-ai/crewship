import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { tryRefresh } from "@/lib/api-fetch"
import { useDesign } from "../use-design"
import { designNodes, upsertDesignNode } from "../design-document"
const server = vi.hoisted(() => ({ status: 200, post401: false, actorId: "same-user", bearer: false, pageId: "old-private-page", authorityRevision: "1", canEdit: true, providers: [] as { admittedEdit: boolean; editable: boolean; destroyed: boolean; deny: () => void; connect: () => void; prepare?: () => Promise<string> }[], urls: [] as string[], calls: [] as { url: string; method: string; skipRefresh?: boolean }[] }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "same-user", name: "Owner" } } }) }))
vi.mock("@/lib/server-base", () => ({ resolveWsBase: () => "ws://localhost", getAuthMode: () => server.bearer ? "bearer" : "cookie" }))
vi.mock("@/lib/api-fetch", () => ({ tryRefresh: vi.fn(async () => "ok"), apiFetch: async (url: string, init?: RequestInit & { skipRefresh?: boolean }) => {
 server.calls.push({ url, method: init?.method ?? "GET", skipRefresh: init?.skipRefresh })
 if (init?.method === "POST" && server.post401) return new Response(JSON.stringify({ error: "session_expired" }), { status: 401 })
 if (init?.method === "POST" && url.includes("/@old-private-page/") && server.pageId !== "old-private-page") return new Response(JSON.stringify({ error: "not found" }), { status: 404 })
 if (server.status !== 200 && !url.includes("/comments?") && !url.includes("/versions?")) return new Response(JSON.stringify({ error: "not found" }), { status: server.status })
 const common = { latest_version: 1, shared_version: 0 }
 const data = url.includes("/comments?") ? { comments: [] } : url.includes("/versions?") ? { ...common, versions: [] } : { ...common, page_id: server.pageId, authority_revision: server.authorityRevision, can_edit: server.canEdit, version: 1, nodes: [], order: [], actor: { id: server.actorId, name: "Owner" } }
 return new Response(JSON.stringify(data), { status: 200, headers: { "Content-Type": "application/json" } })
} }))
vi.mock("../design-provider", () => ({ DesignProvider: class {
 admittedEdit: boolean
 editable: boolean
 destroyed = false
 constructor(_doc: unknown, url: string, editable: boolean, _user: unknown, private status: (status: "connected" | "denied") => void, _dirty: () => void, readonly prepare?: () => Promise<string>) {
  this.admittedEdit = editable
  this.editable = editable
  server.urls.push(url)
  server.providers.push(this)
 }
 deny() { this.editable = false; this.status("denied") }
 connect() { this.status("connected") }
 awareness = { on: () => {}, off: () => {}, getStates: () => new Map() }
 ready = true
 setEditable(value: boolean) { this.editable = value }
 destroy() { this.destroyed = true }
 setCursor() {}
 synchronize() { return Promise.resolve() }
} }))
const drafts = new Map<string, string>()
beforeEach(() => {
 drafts.clear()
 server.urls = []
 server.calls = []
 server.providers = []
 server.post401 = false
 server.status = 200
 server.actorId = "same-user"
 server.bearer = false
 vi.mocked(tryRefresh).mockClear().mockResolvedValue("ok")
 server.authorityRevision = "1"
 server.canEdit = true
 vi.mocked(localStorage.getItem).mockImplementation(key => drafts.get(key) ?? null)
 vi.mocked(localStorage.setItem).mockImplementation((key, value) => { drafts.set(key, value) })
 vi.mocked(localStorage.removeItem).mockImplementation(key => { drafts.delete(key) })
})
const clients: QueryClient[] = []
function mount() {
 const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
 clients.push(client)
 return renderHook(() => useDesign("same-workspace", "reused-slug"), { wrapper: ({ children }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
}
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()); drafts.clear(); vi.mocked(localStorage.getItem).mockReturnValue(null); vi.mocked(localStorage.setItem).mockImplementation(() => {}); vi.mocked(localStorage.removeItem).mockImplementation(() => {}) })
describe("Design local draft identity", () => {
 it("reconnects an editor only after fresh metadata advances the authority revision", async () => {
  server.pageId = "old-private-page"
  const view = mount()
  await waitFor(() => expect(view.result.current.doc).not.toBeNull())
  const previous = view.result.current.doc!
  const original = server.providers[0]
  act(() => original.connect())
  act(() => upsertDesignNode(previous, { id: "draft", type: "text", text: "Keep this pending draft", x: 0, y: 0, width: 200, height: 40 }))
  await act(async () => { original.deny() })
  await waitFor(() => expect(view.result.current.query.isFetching).toBe(false))
  expect(server.providers).toHaveLength(1)
  expect(view.result.current.editable).toBe(false)
  server.authorityRevision = "9007199254740993"
  await act(async () => { await clients[0].invalidateQueries({ queryKey: ["artifact-design"] }) })
  await waitFor(() => expect(server.providers).toHaveLength(2))
  expect(original.destroyed).toBe(true)
  expect(view.result.current.doc).not.toBe(previous)
  expect(designNodes(view.result.current.doc!)).toMatchObject([{ id: "draft", text: "Keep this pending draft" }])
  expect(server.providers[1].admittedEdit).toBe(true)
  act(() => server.providers[1].connect())
  expect(view.result.current.editable).toBe(true)
  expect(view.result.current.nodes).toMatchObject([{ id: "draft", text: "Keep this pending draft" }])
 })
 it("reconnects a downgraded reader without restoring edit permission or a writer draft", async () => {
  server.pageId = "old-private-page"
  const view = mount()
  await waitFor(() => expect(view.result.current.doc).not.toBeNull())
  const original = server.providers[0]
  act(() => upsertDesignNode(view.result.current.doc!, { id: "pending", type: "text", text: "Unsent editor draft", x: 0, y: 0, width: 200, height: 40 }))
  server.authorityRevision = "2"
  server.canEdit = false
  await act(async () => { original.deny() })
  await waitFor(() => expect(server.providers).toHaveLength(2))
  expect(original.destroyed).toBe(true)
  expect(server.providers[1].admittedEdit).toBe(false)
  act(() => server.providers[1].connect())
  expect(view.result.current.editable).toBe(false)
  expect(server.providers[1].editable).toBe(false)
  expect(designNodes(view.result.current.doc!)).toEqual([])
  expect(drafts.size).toBe(1)
 })
 it.each(["actor", "page", "authority", "edit"])("denies renewed transport after %s changes", async changed => {
  server.pageId = "old-private-page"
  const view = mount()
  await waitFor(() => expect(server.providers).toHaveLength(1))
  const original = server.providers[0]
  let admission: string | undefined
  await act(async () => { admission = await original.prepare!() })
  expect(admission).toBe("ready")
  expect(server.calls.at(-1)?.url).toContain("/pages/@old-private-page/design?")
  if (changed === "actor") server.actorId = "different-user"
  if (changed === "page") server.pageId = "different-page"
  if (changed === "authority") server.authorityRevision = "2"
  if (changed === "edit") { server.authorityRevision = "2"; server.canEdit = false }
  await act(async () => { admission = await original.prepare!() })
  expect(admission).toBe("denied")
  await waitFor(() => expect(original.destroyed).toBe(true))
  if (changed === "actor") {
   expect(view.result.current.query.isError).toBe(true)
   expect(view.result.current.doc).toBeNull()
   expect(server.providers).toHaveLength(1)
  }
 })
 it("does not admit a failed cookie refresh and skips cookie rotation for bearer mode", async () => {
  server.pageId = "old-private-page"
  mount()
  await waitFor(() => expect(server.providers).toHaveLength(1))
  const original = server.providers[0]
  vi.mocked(tryRefresh).mockResolvedValue("auth_failed")
  expect(await original.prepare!()).toBe("denied")
  expect(server.calls.some(call => call.url.includes("/@old-private-page/design?"))).toBe(false)
  vi.mocked(tryRefresh).mockClear()
  server.bearer = true
  expect(await original.prepare!()).toBe("ready")
  expect(tryRefresh).not.toHaveBeenCalled()
 })
 it("does not let the shared HTTP wrapper replay a snapshot after credential expiry", async () => {
  server.pageId = "old-private-page"
  const view = mount()
  await waitFor(() => expect(view.result.current.doc).not.toBeNull())
  server.post401 = true
  let outcome: PromiseSettledResult<unknown>[] = []
  await act(async () => { outcome = await Promise.allSettled([view.result.current.save.mutateAsync("Keep pending work")]) })
  const writes = server.calls.filter(call => call.method === "POST")
  expect(writes).toHaveLength(1)
  expect(writes[0].skipRefresh).toBe(true)
  expect(outcome[0].status).toBe("rejected")
  expect(tryRefresh).not.toHaveBeenCalled()
 })
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
  expect(server.urls.at(-1)).toBe("ws://localhost/api/v1/pages/@old-private-page/design/ws?workspace_id=same-workspace&authority_revision=1")
  act(() => upsertDesignNode(previous, { id: "confidential", type: "text", text: "Old private canvas", x: 0, y: 0, width: 200, height: 40, fill: "#111111" }))
  server.pageId = "new-shared-page"
  await act(async () => { await clients[0].invalidateQueries({ queryKey: ["artifact-design"] }) })
  await waitFor(() => { expect(view.result.current.doc).not.toBeNull(); expect(view.result.current.doc).not.toBe(previous) })
  expect(designNodes(view.result.current.doc!)).toEqual([])
  expect(server.urls.at(-1)).toBe("ws://localhost/api/v1/pages/@new-shared-page/design/ws?workspace_id=same-workspace&authority_revision=1")
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
describe("Design local drafts after access loss", () => {
 it("deletes the canvas and comment drafts when the Page answers 404", async () => {
  server.pageId = "old-private-page"
  const view = mount()
  await waitFor(() => expect(view.result.current.doc).not.toBeNull())
  act(() => upsertDesignNode(view.result.current.doc!, { id: "confidential", type: "text", text: "Private canvas", x: 0, y: 0, width: 200, height: 40, fill: "#111111" }))
  drafts.set("design-comment:v2:same-user:same-workspace:old-private-page", "Unsent comment")
  expect(drafts.has("crewship-design:v2:same-user:same-workspace:old-private-page")).toBe(true)
  server.status = 404
  await act(async () => { await clients[0].invalidateQueries({ queryKey: ["artifact-design"] }) })
  await waitFor(() => expect(view.result.current.query.isError).toBe(true))
  await waitFor(() => expect([...drafts.keys()]).toEqual([]))
  expect(drafts.size).toBe(0)
 })
})
