import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { tryRefresh } from "@/lib/api-fetch"
import { useDesign } from "../use-design"
import { designNodes, upsertDesignNode } from "../design-document"
vi.mock("@/lib/artifact-drafts", () => ({
 artifactDraftKey: (kind: string, human: string, workspace: string, page: string) => page ? `${kind === "design-canvas" ? "crewship-design:v2" : kind}:${human}:${workspace}:${page}` : null,
 loadArtifactDraft: async (key: string) => { await store.reads.get(key); if (store.failRead === key) throw new Error("storage"); return drafts.get(key) ?? "" },
 mergeArtifactDraft: async (key: string, merge: (existing: string) => string) => {
  store.merges.push(key)
  if (store.failMerges) throw new Error("storage")
  const text = merge(drafts.get(key) ?? "")
  if (text) drafts.set(key, text); else drafts.delete(key)
  return text
 },
 writeArtifactDraft: async (key: string, value: string) => { drafts.set(key, value) },
 admittedArtifactDraftCapture: (key: string) => async (value:string) => { drafts.set(key,value) },
 captureLocalDraftPage: async () => ({}),
 purgeLocalDraftPage: async () => { drafts.clear() },
}))
vi.mock("@/lib/local-draft-seal", () => {
 class LocalDraftReadRevoked extends Error {}
 return { LocalDraftReadRevoked, createLocalDraftCipher: async () => {
  if (server.status === 403 || server.status === 404) throw new LocalDraftReadRevoked()
  return { close() {} }
 } }
})
const store = vi.hoisted(() => ({ reads: new Map<string, Promise<unknown>>(), failRead: "", failMerges: false, merges: [] as string[] }))
const server = vi.hoisted(() => ({ status: 200, post401: false, actorId: "same-user", bearer: false, pageId: "old-private-page", authorityRevision: "1", canEdit: true, nodes: [] as Record<string, unknown>[], providers: [] as { admittedEdit: boolean; editable: boolean; destroyed: boolean; deny: () => void; connect: () => void; prepare?: () => Promise<string>; dirty: () => void; tracking?: { baseline?: Uint8Array; unconfirmed?: boolean; pending?: (base: Uint8Array | null) => void; conflicts?: (found: unknown[]) => void } }[], urls: [] as string[], calls: [] as { url: string; method: string; skipRefresh?: boolean }[] }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "same-user", name: "Owner" } } }) }))
vi.mock("@/lib/server-base", () => ({ resolveWsBase: () => "ws://localhost", getAuthMode: () => server.bearer ? "bearer" : "cookie" }))
vi.mock("@/lib/api-fetch", () => ({ tryRefresh: vi.fn(async () => "ok"), apiFetch: async (url: string, init?: RequestInit & { skipRefresh?: boolean }) => {
 server.calls.push({ url, method: init?.method ?? "GET", skipRefresh: init?.skipRefresh })
 if (init?.method === "POST" && server.post401) return new Response(JSON.stringify({ error: "session_expired" }), { status: 401 })
 if (init?.method === "POST" && url.includes("/@old-private-page/") && server.pageId !== "old-private-page") return new Response(JSON.stringify({ error: "not found" }), { status: 404 })
 if (server.status !== 200 && !url.includes("/comments?") && !url.includes("/versions?")) return new Response(JSON.stringify({ error: "not found" }), { status: server.status })
 const common = { latest_version: 1, shared_version: 0 }
 const data = url.includes("/comments?") ? { comments: [] } : url.includes("/versions?") ? { ...common, versions: [] } : { ...common, page_id: server.pageId, authority_revision: server.authorityRevision, can_edit: server.canEdit, version: 1, nodes: server.nodes, order: [], actor: { id: server.actorId, name: "Owner" } }
 return new Response(JSON.stringify(data), { status: 200, headers: { "Content-Type": "application/json" } })
} }))
vi.mock("../design-provider", () => ({ DesignProvider: class {
 admittedEdit: boolean
 editable: boolean
 destroyed = false
 constructor(_doc: unknown, url: string, editable: boolean, _user: unknown, private status: (status: "connected" | "denied") => void, readonly dirty: () => void, readonly prepare?: () => Promise<string>, readonly tracking?: { baseline?: Uint8Array; unconfirmed?: boolean; pending?: (base: Uint8Array | null) => void; conflicts?: (found: unknown[]) => void }) {
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
 store.reads.clear(); store.failRead = ""; store.failMerges = false; store.merges = []
 vi.mocked(tryRefresh).mockClear().mockResolvedValue("ok")
 server.authorityRevision = "1"
 server.canEdit = true
 server.nodes = []
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
const pendingKey = "design-pending:same-user:same-workspace:old-private-page"
const conflictKey = "design-conflict:same-user:same-workspace:old-private-page"
const canvasKey = "crewship-design:v2:same-user:same-workspace:old-private-page"
const lost = (over: Record<string, unknown> = {}) => ({ id: "label", name: "Title", mine: "Mine", theirs: "Theirs", kept: "theirs", left: "Theirs", restore: { id: "label", type: "text", text: "Mine" }, ...over })
const encoded = (doc: Y.Doc) => btoa(String.fromCharCode(...Y.encodeStateAsUpdate(doc)))
async function mounted() {
 const view = mount()
 await waitFor(() => expect(view.result.current.doc).not.toBeNull())
 return view
}
describe("Design unsynced work", () => {
 it("is unsynced from the first local edit until the server confirms it", async () => {
  const view = await mounted()
  const provider = server.providers[0]
  act(() => provider.dirty())
  expect(view.result.current).toMatchObject({ dirty: true, unsynced: false })
  act(() => provider.tracking!.pending!(new Uint8Array([1])))
  expect(view.result.current.unsynced).toBe(true)
  await waitFor(() => expect(drafts.get(pendingKey)).toBeTruthy())
  await act(async () => provider.tracking!.pending!(null))
  expect(view.result.current.unsynced).toBe(false)
  await waitFor(() => expect(drafts.has(pendingKey)).toBe(false))
 })
 it("hands unconfirmed work and its baseline to the provider that replaces this one", async () => {
  const view = await mounted()
  act(() => upsertDesignNode(view.result.current.doc!, { id: "draft", type: "text", text: "Unsent", x: 0, y: 0, width: 200, height: 40 }))
  await act(async () => server.providers[0].tracking!.pending!(new Uint8Array([7, 8])))
  await waitFor(() => expect(drafts.get(pendingKey)).toBeTruthy())
  server.authorityRevision = "2"
  await act(async () => { await clients[0].invalidateQueries({ queryKey: ["artifact-design"] }) })
  await waitFor(() => expect(server.providers).toHaveLength(2))
  await waitFor(() => expect(view.result.current.doc).not.toBeNull())
  expect(server.providers[1].tracking).toMatchObject({ baseline: new Uint8Array([7, 8]), unconfirmed: true })
  expect(view.result.current.unsynced).toBe(true)
 })
 it("does not let a new provider or a stored draft clear work before its own confirmation", async () => {
  drafts.set(canvasKey, encoded(new Y.Doc()))
  const view = await mounted()
  expect(server.providers[0].tracking?.unconfirmed).toBe(true)
  expect(view.result.current.unsynced).toBe(true)
  await act(async () => server.providers[0].tracking!.pending!(null))
  expect(view.result.current.unsynced).toBe(false)
 })
 it("keeps work unsynced through revocation, on a read-only provider and after a reload", async () => {
  const view = await mounted()
  await act(async () => server.providers[0].tracking!.pending!(new Uint8Array([1])))
  await waitFor(() => expect(drafts.get(pendingKey)).toBeTruthy())
  server.canEdit = false
  server.authorityRevision = "2"
  await act(async () => { await clients[0].invalidateQueries({ queryKey: ["artifact-design"] }) })
  await waitFor(() => expect(server.providers).toHaveLength(2))
  expect(server.providers[1].admittedEdit).toBe(false)
  await waitFor(() => expect(view.result.current.doc).not.toBeNull())
  expect(view.result.current.unsynced).toBe(true)
  expect(drafts.get(pendingKey)).toBeTruthy()
  view.unmount()
  const again = await mounted()
  await waitFor(() => expect(again.result.current.unsynced).toBe(true))
  expect(server.providers.at(-1)!.tracking?.unconfirmed).toBeFalsy()
 })
 it("removes only this tab's mark when another tab still has unsent work", async () => {
  const view = await mounted()
  await act(async () => server.providers[0].tracking!.pending!(new Uint8Array([1])))
  await waitFor(() => expect(drafts.get(pendingKey)).toBeTruthy())
  const other = JSON.stringify({ ...JSON.parse(drafts.get(pendingKey)!), "other-tab": "AQ==" })
  drafts.set(pendingKey, other)
  await act(async () => server.providers[0].tracking!.pending!(null))
  await waitFor(() => expect(Object.keys(JSON.parse(drafts.get(pendingKey)!))).toEqual(["other-tab"]))
  expect(view.result.current.unsynced).toBe(false)
 })
 it("does not touch the pending record when nothing was marked", async () => {
  await mounted()
  await act(async () => server.providers[0].tracking!.pending!(null))
  expect(store.merges).not.toContain(pendingKey)
 })
})
describe("Design conflict record", () => {
 it("adds to what another tab stored, not to this tab's memory", async () => {
  const view = await mounted()
  await waitFor(() => expect(view.result.current.conflicts).toEqual([]))
  drafts.set(conflictKey, JSON.stringify([lost({ id: "other", mine: "Other tab" })]))
  await act(async () => server.providers[0].tracking!.conflicts!([lost()]))
  await waitFor(() => expect(JSON.parse(drafts.get(conflictKey)!)).toHaveLength(2))
  await waitFor(() => expect(view.result.current.conflicts).toHaveLength(2))
 })
 it("removes one record and keeps records this tab never saw", async () => {
  drafts.set(conflictKey, JSON.stringify([lost()]))
  const view = await mounted()
  await waitFor(() => expect(view.result.current.conflicts).toHaveLength(1))
  drafts.set(conflictKey, JSON.stringify([lost(), lost({ id: "other", mine: "Other tab" })]))
  await act(async () => view.result.current.dismissConflict(view.result.current.conflicts[0]))
  await waitFor(() => expect(JSON.parse(drafts.get(conflictKey)!)).toMatchObject([{ id: "other" }]))
  await waitFor(() => expect(view.result.current.conflicts).toMatchObject([{ id: "other" }]))
 })
 it("stores the same conflict once when two tabs detect it", async () => {
  drafts.set(conflictKey, JSON.stringify([lost()]))
  const view = await mounted()
  await waitFor(() => expect(view.result.current.conflicts).toHaveLength(1))
  await act(async () => server.providers[0].tracking!.conflicts!([lost()]))
  await waitFor(() => expect(store.merges).toContain(conflictKey))
  await waitFor(() => expect(JSON.parse(drafts.get(conflictKey)!)).toHaveLength(1))
  expect(view.result.current.conflicts).toHaveLength(1)
 })
 it("keeps a conflict found while the first read is pending, and what that read returns", async () => {
  drafts.set(conflictKey, JSON.stringify([lost({ id: "stored", mine: "Stored" })]))
  let open = () => {}
  store.reads.set(conflictKey, new Promise<void>(resolve => { open = resolve }))
  const view = await mounted()
  await act(async () => server.providers[0].tracking!.conflicts!([lost()]))
  await waitFor(() => expect(JSON.parse(drafts.get(conflictKey)!)).toHaveLength(2))
  await act(async () => open())
  await waitFor(() => expect(view.result.current.conflicts).toHaveLength(2))
 })
 it("does not replace the record after a failed first read", async () => {
  drafts.set(conflictKey, JSON.stringify([lost({ id: "stored", mine: "Stored" })]))
  store.failRead = conflictKey
  const view = await mounted()
  await waitFor(() => expect(view.result.current.draftError).toBe(true))
  await act(async () => server.providers[0].tracking!.conflicts!([lost()]))
  await waitFor(() => expect(JSON.parse(drafts.get(conflictKey)!)).toHaveLength(2))
  await waitFor(() => expect(view.result.current.conflicts).toHaveLength(2))
 })
 it("keeps the live canvas for a reader whose mark could not be read", async () => {
  server.canEdit = false
  store.failRead = pendingKey
  const view = await mounted()
  await waitFor(() => expect(view.result.current.draftError).toBe(true))
  expect(server.providers).toHaveLength(1)
 })
 it("still shows a conflict that could not be stored, and retries it with the next change", async () => {
  const view = await mounted()
  store.failMerges = true
  await act(async () => server.providers[0].tracking!.conflicts!([lost()]))
  await waitFor(() => expect(view.result.current.draftError).toBe(true))
  expect(view.result.current.conflicts).toHaveLength(1)
  store.failMerges = false
  await act(async () => server.providers[0].tracking!.conflicts!([lost({ id: "second", mine: "Second" })]))
  await waitFor(() => expect(JSON.parse(drafts.get(conflictKey)!)).toHaveLength(2))
 })
 it("does not bring a dismissed conflict back when the first read lands late", async () => {
  drafts.set(conflictKey, JSON.stringify([lost(), lost({ id: "other", mine: "Other" })]))
  let open = () => {}
  store.reads.set(conflictKey, new Promise<void>(resolve => { open = resolve }))
  const view = await mounted()
  await act(async () => server.providers[0].tracking!.conflicts!([lost({ id: "third", mine: "Third" })]))
  await waitFor(() => expect(view.result.current.conflicts).toHaveLength(3))
  await act(async () => view.result.current.dismissConflict(view.result.current.conflicts[0]))
  await waitFor(() => expect(JSON.parse(drafts.get(conflictKey)!)).toHaveLength(2))
  await act(async () => open())
  await new Promise(resolve => setTimeout(resolve, 0))
  expect(view.result.current.conflicts).toHaveLength(2)
 })
 it("shows nothing and marks nothing after a read loss", async () => {
  drafts.set(conflictKey, JSON.stringify([lost()]))
  const view = await mounted()
  await waitFor(() => expect(view.result.current.conflicts).toHaveLength(1))
  await act(async () => server.providers[0].tracking!.pending!(new Uint8Array([1])))
  server.status = 404
  await act(async () => { await clients[0].invalidateQueries({ queryKey: ["artifact-design"] }) })
  await waitFor(() => expect(view.result.current.readRevoked).toBe(true))
  expect(view.result.current.conflicts).toEqual([])
  expect(view.result.current.unsynced).toBe(false)
 })
})
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


it("renders the authorized shared snapshot for a read-only user even when its live document differs", async () => {
 server.canEdit = false
 server.nodes = [{ id: "shared-title", type: "text", text: "Shared", x: 0, y: 0, width: 200, height: 30 }]
 const view = mount()
 await waitFor(() => expect(view.result.current.query.data?.can_edit).toBe(false))
 expect(view.result.current.nodes.map(node => node.id)).toEqual(["shared-title"])
 await waitFor(() => expect(view.result.current.doc).not.toBeNull())
 act(() => upsertDesignNode(view.result.current.doc!, { id: "old-private-live", type: "text", text: "Private", x: 0, y: 0, width: 200, height: 30 }))
 expect(view.result.current.nodes.map(node => node.id)).toEqual(["shared-title"])
})
