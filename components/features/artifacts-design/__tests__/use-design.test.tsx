import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { tryRefresh } from "@/lib/api-fetch"
import { useDesign } from "../use-design"
import { designNodes, parseDesignDraft, removeDesignNode, serializeDesignDraft, upsertDesignNode } from "../design-document"
vi.mock("@/lib/artifact-drafts", () => ({
 artifactDraftKey: (kind: string, human: string, workspace: string, page: string) => page ? `${kind === "design-canvas" ? "crewship-design:v2" : kind}:${human}:${workspace}:${page}` : null,
 loadArtifactDraft: async (key: string) => { store.loads.push(key); await store.reads.get(key); if (store.failRead === key) throw new Error("storage"); return drafts.get(key) ?? "" },
 mergeArtifactDraft: async (key: string, merge: (existing: string) => string) => {
  store.merges.push(key)
  await store.mergeGate
  if (store.killAfterWrites === 0 || store.failKey === key) throw new Error("killed")
  if (store.failMerges || (store.failConflictMerge && key.startsWith("design-conflict"))) throw new Error("storage")
  const text = merge(drafts.get(key) ?? "")
  if (text) drafts.set(key, text); else drafts.delete(key)
  if (store.killAfterWrites !== null) store.killAfterWrites -= 1
  return text
 },
 writeArtifactDraft: async (key: string, value: string) => { drafts.set(key, value) },
 admittedArtifactDraftMerge: (key: string, merge: (existing: string) => string) => async () => { const text = merge(drafts.get(key) ?? ""); drafts.set(key, text); return text },
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
const store = vi.hoisted(() => ({ mergeGate: Promise.resolve() as Promise<unknown>, reads: new Map<string, Promise<unknown>>(), failRead: "", failMerges: false, failConflictMerge: false, killAfterWrites: null as number | null, failKey: "", merges: [] as string[], loads: [] as string[] }))
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
 store.mergeGate = Promise.resolve(); store.reads.clear(); store.failRead = ""; store.failMerges = false; store.failConflictMerge = false; store.killAfterWrites = null; store.failKey = ""; store.merges = []; store.loads = []
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
const tabsKey = "design-tabs:same-user:same-workspace:old-private-page"
const lost = (over: Record<string, unknown> = {}) => ({ id: "label", name: "Title", mine: "Mine", theirs: "Theirs", kept: "theirs", left: "Theirs", restore: { id: "label", type: "text", text: "Mine" }, ...over })
const mark = (doc: Y.Doc) => ({ baseline: "AQ==", work: btoa(String.fromCharCode(...Y.encodeSnapshot(Y.snapshot(doc)))) })
const encoded = (doc: Y.Doc) => btoa(String.fromCharCode(...Y.encodeStateAsUpdate(doc)))
async function mounted() {
 const view = mount()
 await waitFor(() => expect(view.result.current.doc).not.toBeNull())
 return view
}
const layer = (id: string, text = id) => ({ id, type: "text" as const, text, x: 0, y: 0, width: 200, height: 40 })
const ids = (doc: Y.Doc) => designNodes(doc).map(node => node.id).sort()
const decoded = (text: string) => Uint8Array.from(atob(text), char => char.charCodeAt(0))
const storedKey = () => tabsKey
const entries = () => parseDesignDraft(drafts.get(storedKey()) ?? "")
/** Everything the stored canvas row holds, whichever tab wrote it. */
const storedDoc = () => { const doc = new Y.Doc(); for (const entry of Object.values(entries())) Y.applyUpdate(doc, decoded(entry.state)); return doc }
const entryOf = (doc: Y.Doc, baseline: string | null = null) => ({ state: encoded(doc), baseline })
const edit = async (view: Awaited<ReturnType<typeof mounted>>, node: ReturnType<typeof layer>) => {
 act(() => upsertDesignNode(view.result.current.doc!, node))
 await act(async () => { await new Promise(resolve => setTimeout(resolve, 5)) })
}
const baseOf = (doc: Y.Doc) => Y.encodeStateAsUpdate(doc)
const b64 = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes))
describe("Design unsynced work", () => {
 it("is unsynced from the first local edit until the server confirms it", async () => {
  const view = await mounted()
  const provider = server.providers[0]
  act(() => provider.dirty())
  expect(view.result.current).toMatchObject({ dirty: true, unsynced: false })
  act(() => provider.tracking!.pending!(new Uint8Array([1])))
  expect(view.result.current.unsynced).toBe(true)
  await edit(view, layer("unsent"))
  await waitFor(() => expect(drafts.get(tabsKey)).toBeTruthy())
  await act(async () => provider.tracking!.pending!(null))
  expect(view.result.current.unsynced).toBe(false)
  await waitFor(() => expect(drafts.has(tabsKey)).toBe(false))
 })
 it("hands unconfirmed work and its baseline to the provider that replaces this one", async () => {
  const view = await mounted()
  await act(async () => server.providers[0].tracking!.pending!(new Uint8Array([7, 8])))
  await edit(view, layer("draft", "Unsent"))
  await waitFor(() => expect(drafts.get(tabsKey)).toBeTruthy())
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
  await edit(view, layer("unsent"))
  await waitFor(() => expect(drafts.get(tabsKey)).toBeTruthy())
  server.canEdit = false
  server.authorityRevision = "2"
  await act(async () => { await clients[0].invalidateQueries({ queryKey: ["artifact-design"] }) })
  await waitFor(() => expect(server.providers).toHaveLength(2))
  expect(server.providers[1].admittedEdit).toBe(false)
  await waitFor(() => expect(view.result.current.doc).not.toBeNull())
  expect(view.result.current.unsynced).toBe(true)
  expect(drafts.get(tabsKey)).toBeTruthy()
  view.unmount()
  const again = await mounted()
  await waitFor(() => expect(again.result.current.unsynced).toBe(true))
  expect(server.providers.at(-1)!.tracking?.unconfirmed).toBeFalsy()
 })
 it("keeps an entry of another tab for work the confirmed document does not hold", async () => {
  const view = await mounted()
  await edit(view, layer("mine"))
  const other = new Y.Doc(); upsertDesignNode(other, layer("their-work"))
  drafts.set(tabsKey, serializeDesignDraft({ ...entries(), "other-tab": entryOf(other) }))
  await act(async () => server.providers[0].tracking!.pending!(null))
  await waitFor(() => expect(Object.keys(entries())).toEqual(["other-tab"]))
  expect(view.result.current.unsynced).toBe(false)
 })
 it("clears a closed tab's entry once the confirmed document holds it", async () => {
  const stored = new Y.Doc(); upsertDesignNode(stored, layer("stored-work"))
  const base = new Y.Doc()
  drafts.set(tabsKey, serializeDesignDraft({ "closed-tab": entryOf(stored, b64(baseOf(base))) }))
  await mounted()
  expect(server.providers[0].tracking?.baseline).toEqual(baseOf(base))
  await act(async () => server.providers[0].tracking!.pending!(null))
  await waitFor(() => expect(drafts.has(tabsKey)).toBe(false))
 })
 it("judges coverage by the confirmed document, not by an edit made after it", async () => {
  const view = await mounted()
  await edit(view, layer("sent"))
  let open = () => {}
  store.mergeGate = new Promise<void>(resolve => { open = resolve })
  await act(async () => server.providers[0].tracking!.pending!(null))
  act(() => upsertDesignNode(view.result.current.doc!, layer("later")))
  store.mergeGate = Promise.resolve()
  await act(async () => { open(); await new Promise(resolve => setTimeout(resolve, 20)) })
  expect(ids(storedDoc())).toContain("later")
 })
 it("does not touch the stored canvas when nothing was stored", async () => {
  await mounted()
  await act(async () => server.providers[0].tracking!.pending!(null))
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 5)) })
  expect(store.merges).not.toContain(tabsKey)
 })
})
describe("Rows stored by older builds (#5017)", () => {
 it("uses a mark for the base of a bare canvas and removes both once confirmed", async () => {
  const base = new Y.Doc(); upsertDesignNode(base, layer("confirmed"))
  const older = new Y.Doc(); Y.applyUpdate(older, baseOf(base)); upsertDesignNode(older, layer("stored-unsent"))
  const newer = new Y.Doc(); Y.applyUpdate(newer, baseOf(older)); upsertDesignNode(newer, layer("never-stored"))
  drafts.set(canvasKey, encoded(older))
  drafts.set(pendingKey, JSON.stringify({ "killed-tab": { ...mark(newer), baseline: b64(baseOf(base)) } }))
  const view = await mounted()
  expect(ids(view.result.current.doc!)).toEqual(["confirmed", "stored-unsent"])
  expect(server.providers[0].tracking).toMatchObject({ unconfirmed: true, baseline: baseOf(base) })
  await act(async () => server.providers[0].tracking!.pending!(null))
  await waitFor(() => { expect(drafts.has(canvasKey)).toBe(false); expect(drafts.has(pendingKey)).toBe(false) })
 })
 it("does not compare from a mark's base the bare canvas does not hold", async () => {
  const stored = new Y.Doc(); upsertDesignNode(stored, layer("stored"))
  const foreign = new Y.Doc(); upsertDesignNode(foreign, layer("foreign"))
  drafts.set(canvasKey, encoded(stored))
  drafts.set(pendingKey, JSON.stringify({ "other-tab": { ...mark(foreign), baseline: b64(baseOf(foreign)) } }))
  await mounted()
  expect(server.providers[0].tracking?.baseline).toBeUndefined()
 })
 it("carries an old row's work into the new format beside this tab's entry", async () => {
  const stored = new Y.Doc(); upsertDesignNode(stored, layer("old"))
  drafts.set(canvasKey, encoded(stored))
  const view = await mounted()
  await edit(view, layer("new"))
  expect(ids(storedDoc())).toEqual(["new", "old"])
 })
 it("does not count a bare old canvas as unsent work for a reader", async () => {
  drafts.set(canvasKey, encoded(new Y.Doc()))
  server.canEdit = false
  const view = await mounted()
  expect(view.result.current.unsynced).toBe(false)
 })
})
describe("One stored entry per tab (#5017)", () => {
 it("keeps the work of two tabs when one of them is killed", async () => {
  const first = await mounted(), second = await mounted()
  await edit(first, layer("from-first"))
  await edit(second, layer("from-second"))
  first.unmount(); second.unmount()
  const reopened = await mounted()
  expect(ids(reopened.result.current.doc!)).toEqual(["from-first", "from-second"])
 })
 it("keeps a deletion from one tab beside the other tab's entry", async () => {
  const shared = new Y.Doc(); upsertDesignNode(shared, layer("gone")); upsertDesignNode(shared, layer("stays"))
  drafts.set(canvasKey, encoded(shared))
  const first = await mounted(), second = await mounted()
  act(() => removeDesignNode(first.result.current.doc!, "gone"))
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 5)) })
  await edit(second, layer("added"))
  first.unmount(); second.unmount()
  expect(ids(storedDoc())).toEqual(["added", "stays"])
 })
 it("stores the newest canvas once while earlier stores are still waiting", async () => {
  const view = await mounted()
  let open = () => {}
  store.mergeGate = new Promise<void>(resolve => { open = resolve })
  for (let keys = 0; keys < 20; keys++) act(() => upsertDesignNode(view.result.current.doc!, layer("typing", `Key ${keys}`)))
  expect(store.merges.filter(key => key === tabsKey)).toHaveLength(1)
  await act(async () => { open(); await Promise.resolve() })
  await waitFor(() => expect(designNodes(storedDoc())[0].text).toBe("Key 19"))
 })
 it("reports a canvas that could not be stored, keeps the last pair and stores the next edit", async () => {
  const view = await mounted()
  await act(async () => server.providers[0].tracking!.pending!(baseOf(new Y.Doc())))
  await edit(view, layer("kept"))
  const before = drafts.get(tabsKey)
  store.failMerges = true
  await edit(view, layer("refused"))
  await waitFor(() => expect(view.result.current.draftError).toBe(true))
  expect(drafts.get(tabsKey)).toBe(before)
  store.failMerges = false
  await edit(view, layer("next"))
  await waitFor(() => expect(ids(storedDoc())).toEqual(["kept", "next", "refused"]))
 })
 it("writes work and its base as one write, with no second row", async () => {
  const view = await mounted()
  await act(async () => server.providers[0].tracking!.pending!(baseOf(new Y.Doc())))
  await edit(view, layer("first"))
  await edit(view, layer("second"))
  expect(new Set(store.merges)).toEqual(new Set([tabsKey]))
  expect(Object.values(entries())).toMatchObject([{ baseline: b64(baseOf(new Y.Doc())) }])
  expect(Y.encodeStateVector(storedDoc())).toEqual(Y.encodeStateVector(view.result.current.doc!))
 })
 it("reopens a killed tab with its unsent work and its baseline", async () => {
  const before = new Y.Doc(); upsertDesignNode(before, layer("confirmed"))
  drafts.set(canvasKey, encoded(before))
  const view = await mounted()
  await act(async () => server.providers[0].tracking!.pending!(baseOf(before)))
  await edit(view, layer("unsent"))
  view.unmount()
  const reopened = await mounted()
  expect(ids(reopened.result.current.doc!)).toEqual(["confirmed", "unsent"])
  expect(server.providers[1].tracking).toMatchObject({ unconfirmed: true, baseline: baseOf(before) })
  expect(reopened.result.current.unsynced).toBe(true)
 })
 it("keeps the entry of newer work stored while another tab is clearing", async () => {
  const base = new Y.Doc(); upsertDesignNode(base, layer("confirmed"))
  drafts.set(canvasKey, encoded(base))
  const writer = await mounted(), other = await mounted()
  await act(async () => server.providers[0].tracking!.pending!(baseOf(base)))
  await edit(writer, layer("reached-the-server"))
  act(() => Y.applyUpdate(other.result.current.doc!, Y.encodeStateAsUpdate(writer.result.current.doc!), "design-server"))
  await act(async () => server.providers[1].tracking!.pending!(baseOf(base)))
  await edit(other, layer("other-work"))
  // The clear is admitted first; the newer canvas lands between its decision and its write.
  act(() => Y.applyUpdate(other.result.current.doc!, Y.encodeStateAsUpdate(writer.result.current.doc!), "design-server"))
  let open = () => {}
  store.mergeGate = new Promise<void>(resolve => { open = resolve })
  await act(async () => { server.providers[1].tracking!.pending!(null); await new Promise(resolve => setTimeout(resolve, 10)) })
  act(() => upsertDesignNode(writer.result.current.doc!, layer("unsent")))
  store.mergeGate = Promise.resolve()
  await act(async () => { open(); await new Promise(resolve => setTimeout(resolve, 20)) })
  writer.unmount(); other.unmount()
  await mounted()
  expect(ids(storedDoc())).toContain("unsent")
  expect(server.providers[2].tracking?.baseline).toEqual(baseOf(base))
 })
 it("reads no canvas to clear an entry, so the admitted capture keeps its cipher", async () => {
  const view = await mounted()
  await edit(view, layer("sent"))
  store.loads = []
  await act(async () => { server.providers[0].tracking!.pending!(null); await new Promise(resolve => setTimeout(resolve, 10)) })
  expect(store.loads).not.toContain(tabsKey)
 })
 it("takes one base from every entry, all of which the server held", async () => {
  const one = new Y.Doc(); upsertDesignNode(one, layer("one"))
  const two = new Y.Doc(); upsertDesignNode(two, layer("two"))
  drafts.set(tabsKey, serializeDesignDraft({ a: entryOf(one, b64(baseOf(one))), b: entryOf(two, b64(baseOf(two))) }))
  await mounted()
  const base = new Y.Doc(); Y.applyUpdate(base, server.providers[0].tracking!.baseline!)
  expect(ids(base)).toEqual(["one", "two"])
 })
})
describe("Entries a tab may remove (#5017)", () => {
 const titled = (text: string) => ({ ...layer("title", text), name: "Title" })
 const wait = () => act(async () => { await new Promise(resolve => setTimeout(resolve, 10)) })
 it("leaves an entry this tab never recovered, though its document covers it", async () => {
  const base = new Y.Doc(); upsertDesignNode(base, titled("Base"))
  const clean = await mounted()
  act(() => Y.applyUpdate(clean.result.current.doc!, baseOf(base), "design-server"))
  const crashed = new Y.Doc(); Y.applyUpdate(crashed, baseOf(base)); upsertDesignNode(crashed, titled("Mine"))
  drafts.set(storedKey(), serializeDesignDraft({ "crashed-tab": entryOf(crashed, b64(baseOf(base))) }))
  // The server relayed the losing update: this document covers the entry and shows no sign of it.
  act(() => Y.applyUpdate(clean.result.current.doc!, baseOf(crashed), "design-server"))
  await edit(clean, layer("mine"))
  await act(async () => server.providers[0].tracking!.pending!(null))
  await wait()
  expect(Object.keys(entries())).toEqual(["crashed-tab"])
 })
 it("removes a recovered entry only as it was recovered", async () => {
  const stored = new Y.Doc(); upsertDesignNode(stored, layer("stored"))
  drafts.set(storedKey(), serializeDesignDraft({ "closed-tab": entryOf(stored) }))
  await mounted()
  const newer = new Y.Doc(); Y.applyUpdate(newer, baseOf(stored)); upsertDesignNode(newer, layer("newer"))
  drafts.set(storedKey(), serializeDesignDraft({ "closed-tab": entryOf(newer) }))
  await act(async () => server.providers[0].tracking!.pending!(null))
  await wait()
  expect(Object.keys(entries())).toEqual(["closed-tab"])
 })
 it("keeps the entry when a notice found by the provider cannot be stored", async () => {
  const stored = new Y.Doc(); upsertDesignNode(stored, titled("Mine"))
  drafts.set(storedKey(), serializeDesignDraft({ "closed-tab": entryOf(stored) }))
  const view = await mounted()
  store.failConflictMerge = true
  await act(async () => { server.providers[0].tracking!.conflicts!([lost()]); server.providers[0].tracking!.pending!(null) })
  await waitFor(() => expect(view.result.current.draftError).toBe(true))
  await wait()
  expect(Object.keys(entries())).toEqual(["closed-tab"])
 })
 it("removes the entry once the provider's notice is stored", async () => {
  const stored = new Y.Doc(); upsertDesignNode(stored, titled("Mine"))
  drafts.set(storedKey(), serializeDesignDraft({ "closed-tab": entryOf(stored) }))
  await mounted()
  await act(async () => { server.providers[0].tracking!.conflicts!([lost()]); server.providers[0].tracking!.pending!(null) })
  await waitFor(() => expect(drafts.has(storedKey())).toBe(false))
  expect(JSON.parse(drafts.get(conflictKey)!)).toHaveLength(1)
 })
})
describe("Rollback to a build that reads only the old row (#5017)", () => {
 it("writes nothing to the rows an older build reads", async () => {
  const view = await mounted()
  await act(async () => server.providers[0].tracking!.pending!(baseOf(new Y.Doc())))
  await edit(view, layer("unsent"))
  expect(drafts.has(canvasKey)).toBe(false)
  expect(drafts.has(pendingKey)).toBe(false)
  expect(ids(storedDoc())).toEqual(["unsent"])
 })
})
describe("Competing offline work in two tabs (#5017)", () => {
 const titled = (text: string) => ({ ...layer("title", text), name: "Title" })
 async function twoTabs() {
  const base = new Y.Doc(); upsertDesignNode(base, titled("Base"))
  drafts.set(canvasKey, encoded(base))
  return { first: await mounted(), second: await mounted() }
 }
 const reopen = async (...tabs: { unmount(): void }[]) => { tabs.forEach(tab => tab.unmount()); const view = await mounted(); await act(async () => { await new Promise(resolve => setTimeout(resolve, 10)) }); return view }
 const lastProvider = () => server.providers.at(-1)!
 it("shows the text that lost when two tabs changed one layer and both died", async () => {
  const { first, second } = await twoTabs()
  await edit(first, titled("First"))
  await edit(second, titled("Second"))
  const reopened = await reopen(first, second)
  const shown = designNodes(reopened.result.current.doc!)[0].text
  await waitFor(() => expect(reopened.result.current.conflicts).toMatchObject([{ id: "title", left: shown, restore: { text: shown === "First" ? "Second" : "First" } }]))
 })
 it("stores nothing about a conflict while the tabs live", async () => {
  const { first, second } = await twoTabs()
  await edit(first, titled("First"))
  await edit(second, titled("Second"))
  expect(drafts.has(conflictKey)).toBe(false)
 })
 it("shows the same notice after a second reopen, once", async () => {
  const { first, second } = await twoTabs()
  await edit(first, titled("First"))
  await edit(second, titled("Second"))
  const again = await reopen(await reopen(first, second))
  expect(again.result.current.conflicts).toHaveLength(1)
 })
 it("keeps the entries until the notice made from them is stored", async () => {
  const { first, second } = await twoTabs()
  await edit(first, titled("First"))
  await edit(second, titled("Second"))
  store.failConflictMerge = true
  const reopened = await reopen(first, second)
  await waitFor(() => expect(reopened.result.current.draftError).toBe(true))
  const before = drafts.get(tabsKey)
  await act(async () => { lastProvider().tracking!.pending!(null); await new Promise(resolve => setTimeout(resolve, 10)) })
  expect(drafts.get(tabsKey)).toBe(before)
 })
 it("removes the entries after the server covers them, with the notice stored", async () => {
  const { first, second } = await twoTabs()
  await edit(first, titled("First"))
  await edit(second, titled("Second"))
  await reopen(first, second)
  await act(async () => lastProvider().tracking!.pending!(null))
  await waitFor(() => expect(drafts.has(tabsKey)).toBe(false))
  expect(JSON.parse(drafts.get(conflictKey)!)).toHaveLength(1)
 })
 it("names only the last text of a tab that kept typing", async () => {
  const { first, second } = await twoTabs()
  await edit(first, titled("First"))
  for (const text of ["S", "Se", "Sec"]) await edit(second, titled(text))
  const reopened = await reopen(first, second)
  expect(reopened.result.current.conflicts).toHaveLength(1)
  expect(["First", "Sec"]).toContain(reopened.result.current.conflicts[0].restore.text)
 })
 it("keeps a text whose layer another tab deleted", async () => {
  const { first, second } = await twoTabs()
  act(() => removeDesignNode(first.result.current.doc!, "title"))
  await edit(first, layer("elsewhere"))
  await edit(second, titled("Edited"))
  const reopened = await reopen(first, second)
  expect(reopened.result.current.conflicts).toMatchObject([{ id: "title", restore: { text: "Edited" } }])
 })
 it("reports nothing for a text one tab replaced knowing the other", async () => {
  const { first, second } = await twoTabs()
  await edit(first, titled("First"))
  act(() => Y.applyUpdate(second.result.current.doc!, Y.encodeStateAsUpdate(first.result.current.doc!), "design-server"))
  await edit(second, titled("On top"))
  const reopened = await reopen(first, second)
  expect(designNodes(reopened.result.current.doc!)[0].text).toBe("On top")
  expect(reopened.result.current.conflicts).toEqual([])
 })
 it("reports nothing for a tab retyping its own text beside the other tab's layer", async () => {
  const { first, second } = await twoTabs()
  await edit(first, layer("other-layer"))
  await edit(second, titled("One"))
  await edit(second, titled("Two"))
  const reopened = await reopen(first, second)
  expect(reopened.result.current.conflicts).toEqual([])
 })
})
describe("A store that dies part way (#5017)", () => {
 const titled = (text: string) => ({ ...layer("title", text), name: "Title" })
 const settle = () => act(async () => { await new Promise(resolve => setTimeout(resolve, 20)) })
 it("reopens with a baseline when the tab dies after its first write of a store", async () => {
  const base = new Y.Doc(); upsertDesignNode(base, titled("Base"))
  drafts.set(canvasKey, encoded(base))
  const view = await mounted()
  const baseline = baseOf(base)
  await act(async () => server.providers[0].tracking!.pending!(baseline))
  await edit(view, titled("Mine"))
  await settle()
  // A remote frame moves the confirmed canvas; the stored canvas stays behind it.
  const remote = new Y.Doc(); Y.applyUpdate(remote, baseline); upsertDesignNode(remote, layer("remote"))
  act(() => Y.applyUpdate(view.result.current.doc!, baseOf(remote), "design-server"))
  await act(async () => server.providers[0].tracking!.pending!(baseOf(remote)))
  await settle()
  store.killAfterWrites = 1
  await edit(view, layer("second"))
  await settle()
  view.unmount()
  store.killAfterWrites = null
  const reopened = await mounted()
  expect(designNodes(reopened.result.current.doc!).find(node => node.id === "title")?.text).toBe("Mine")
  expect(server.providers[1].tracking?.baseline).toBeDefined()
 })
 it("keeps canvas and baseline paired when one write of a store fails", async () => {
  const base = new Y.Doc(); upsertDesignNode(base, layer("confirmed"))
  drafts.set(canvasKey, encoded(base))
  const writer = await mounted(), other = await mounted()
  await act(async () => server.providers[0].tracking!.pending!(baseOf(base)))
  await edit(writer, layer("reached-the-server"))
  await settle()
  act(() => Y.applyUpdate(other.result.current.doc!, Y.encodeStateAsUpdate(writer.result.current.doc!), "design-server"))
  store.failKey = pendingKey
  await edit(writer, layer("unsent"))
  await settle()
  store.failKey = ""
  await act(async () => server.providers[1].tracking!.pending!(baseOf(base)))
  await settle()
  await act(async () => server.providers[1].tracking!.pending!(null))
  await settle()
  writer.unmount(); other.unmount()
  const reopened = await mounted()
  if (ids(reopened.result.current.doc!).includes("unsent")) expect(server.providers[2].tracking?.baseline).toBeDefined()
 })
 it("shows no notice of an overwrite when the store dies before the canvas landed", async () => {
  const base = new Y.Doc(); upsertDesignNode(base, titled("Base"))
  drafts.set(canvasKey, encoded(base))
  const first = await mounted(), second = await mounted()
  // The higher client wins a Yjs race, so the dying store would have won.
  first.result.current.doc!.clientID = 1
  second.result.current.doc!.clientID = 2
  await edit(first, titled("First"))
  await settle()
  store.killAfterWrites = 1
  await edit(second, titled("Second"))
  await settle()
  first.unmount(); second.unmount()
  store.killAfterWrites = null
  const reopened = await mounted()
  await settle()
  const shown = designNodes(reopened.result.current.doc!)[0].text
  for (const notice of reopened.result.current.conflicts) expect(notice.left).not.toBe(notice.restore.text)
  expect(reopened.result.current.conflicts.filter(notice => notice.restore.text === shown)).toEqual([])
  expect(shown === "First" || reopened.result.current.conflicts.length === 1).toBe(true)
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
  await waitFor(() => expect(drafts.size).toBe(1))
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
describe("A refused first read is not a loss of access (#5144)", () => {
 it("never claims access was removed to a person who could not read the Design in this tab", async () => {
  server.status = 404
  const view = mount()
  await waitFor(() => expect(view.result.current.readRevoked).toBe(true))
  expect(view.result.current.everRead).toBe(false)
 })
 it("does not report a refusal while the first read is still open", () => {
  const view = mount()
  expect(view.result.current.query.data).toBeUndefined()
  expect(view.result.current.readRevoked).toBe(false)
 })
 it("counts a read that worked before the refusal", async () => {
  const view = await mounted()
  await waitFor(() => expect(view.result.current.query.data).toBeDefined())
  server.status = 404
  await act(async () => { await clients[0].invalidateQueries({ queryKey: ["artifact-design"] }) })
  await waitFor(() => expect(view.result.current.readRevoked).toBe(true))
  expect(view.result.current.everRead).toBe(true)
 })
})
describe("Design local drafts after access loss", () => {
 it("deletes the canvas and comment drafts when the Page answers 404", async () => {
  server.pageId = "old-private-page"
  const view = mount()
  await waitFor(() => expect(view.result.current.doc).not.toBeNull())
  act(() => upsertDesignNode(view.result.current.doc!, { id: "confidential", type: "text", text: "Private canvas", x: 0, y: 0, width: 200, height: 40, fill: "#111111" }))
  drafts.set("design-comment:v2:same-user:same-workspace:old-private-page", "Unsent comment")
  await waitFor(() => expect(drafts.has(tabsKey)).toBe(true))
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
