import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { Buffer } from "node:buffer"
import type { DocumentDraft } from "../document-queue"
import { openNativeDraftJournal, validNativeDraft, type NativeDraft } from "./native-draft"

const mock = vi.hoisted(() => ({ open: vi.fn(), purge: vi.fn(), retire: undefined as undefined | (() => Promise<void>) }))
vi.mock("../document-queue", async importOriginal => ({
  ...await importOriginal<typeof import("../document-queue")>(),
  openDocumentDrafts: mock.open,
  purgeDocumentDrafts: mock.purge,
  registerDocumentDraftWriter: (_human: string, retire: () => Promise<void>) => { mock.retire = retire; return () => {} },
}))
const scope = { human: "h", workspace: "w", page: "p", applicationId: "wordcraft" }
const draft = (): NativeDraft => ({ applicationId: "wordcraft", enginePin: "engine-1", sessionId: "tab-a", generation: "1", baseVersion: "1", editorRevision: "doc:1:0", bytes: new Uint8Array([1, 2, 255]) })
const rows = new Map<string, DocumentDraft>()
function storage(exclusive = true) {
  return { exclusive, load: vi.fn(async () => [...rows.values()]), put: vi.fn(async (row: DocumentDraft) => { rows.set(row.id, row) }), remove: vi.fn(async (id: string) => { rows.delete(id) }), close: vi.fn() }
}
beforeEach(() => { rows.clear(); mock.open.mockReset(); mock.purge.mockReset(); mock.purge.mockImplementation(async () => { rows.clear() }); mock.retire = undefined })
afterEach(() => vi.restoreAllMocks())

it("bounds raw native bytes at the server's 32 MiB limit separately from the encoded journal budget", () => {
  expect(validNativeDraft({ ...draft(), bytes: new Uint8Array(32 * 1024 * 1024 + 1) })).toBe(false)
})

it("reuses the existing journal with bounded native opt-in and offers reload drafts only for compare/copy", async () => {
  mock.open.mockResolvedValue(storage())
  const first = await openNativeDraftJournal(scope)
  const original = draft(); original.bytes = new Uint8Array(5 * 1024 * 1024).fill(73)
  original.bytes[0] = 0; original.bytes[1] = 255; original.bytes[original.bytes.length - 1] = 41
  await first.persist(original)
  await first.close()
  const reloaded = await openNativeDraftJournal(scope)
  const recovered = await reloaded.recovery()
  expect(mock.open).toHaveBeenCalledWith("h", "w", "p", { maxDraftBytes: 64 * 1024 * 1024, requireExclusive: true })
  expect(recovered).toHaveLength(1)
  expect(recovered[0].actions).toEqual(["compare", "copy"])
  expect(Buffer.from(recovered[0].draft.bytes).equals(Buffer.from(original.bytes))).toBe(true)
  expect(recovered[0].draft).toMatchObject({ generation: "1", sessionId: "tab-a", baseVersion: "1", enginePin: "engine-1" })
  await reloaded.close()
})

it("refuses a native writer when the underlying journal did not acquire an exclusive lock", async () => {
  const store = storage(false); mock.open.mockResolvedValue(store)
  await expect(openNativeDraftJournal(scope)).rejects.toThrow()
  expect(store.put).not.toHaveBeenCalled(); expect(store.close).toHaveBeenCalledOnce()
})

it("an old ACK never removes a newer durable native snapshot", async () => {
  mock.open.mockResolvedValue(storage())
  const journal = await openNativeDraftJournal(scope)
  await journal.persist(draft())
  await journal.persist({ ...draft(), bytes: new Uint8Array([4]), editorRevision: "doc:2:0" })
  await journal.confirm(draft())
  expect(rows.size).toBe(1)
  await journal.close()
})

it("waits for an already-started IDB write before privacy purge and refuses late writes", async () => {
  const store = storage()
  let finish!: () => void
  store.put.mockImplementationOnce(row => new Promise<void>(resolve => { finish = () => { rows.set(row.id, row); resolve() } }))
  mock.open.mockResolvedValue(store)
  const journal = await openNativeDraftJournal(scope)
  const writing = journal.persist(draft())
  const observed = writing.catch(() => {})
  await vi.waitFor(() => expect(store.put).toHaveBeenCalledOnce())
  const retired = journal.revokeRead()
  expect(mock.purge).not.toHaveBeenCalled()
  finish()
  await retired; await observed
  expect(rows.size).toBe(0)
  await expect(journal.persist(draft())).rejects.toThrow()
})

it("retires synchronously during sign-out even if IndexedDB opens late", async () => {
  let finish!: (value: ReturnType<typeof storage>) => void
  mock.open.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
  const opening = openNativeDraftJournal(scope)
  const rejected = expect(opening).rejects.toThrow()
  const retirement = mock.retire!()
  const store = storage(); finish(store)
  await retirement; await rejected
  expect(store.close).toHaveBeenCalled()
  expect(store.put).not.toHaveBeenCalled()
})

it("captures immutable human and Page scope before the IndexedDB open awaits", async () => {
  let finish!: (value: ReturnType<typeof storage>) => void
  mock.open.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
  const mutableScope = { ...scope }
  const opening = openNativeDraftJournal(mutableScope)
  Object.assign(mutableScope, { human: "other", workspace: "other", page: "other", applicationId: "gridcraft" })
  finish(storage()); const journal = await opening
  await journal.persist(draft()); await journal.revokeRead()
  expect(mock.purge).toHaveBeenCalledExactlyOnceWith("h", "w", "p")
})

it("keeps the exclusive store until a requested read purge completes and lets close join it", async () => {
  let finish!: () => void
  mock.purge.mockImplementationOnce(() => new Promise<void>(resolve => { finish = resolve }))
  const store = storage(); mock.open.mockResolvedValue(store)
  const journal = await openNativeDraftJournal(scope)
  const purging = journal.revokeRead()
  await vi.waitFor(() => expect(mock.purge).toHaveBeenCalledOnce())
  let closed = false
  const closing = journal.close().then(() => { closed = true })
  await Promise.resolve()
  expect(closed).toBe(false); expect(store.close).not.toHaveBeenCalled()
  finish(); await Promise.all([purging, closing])
  expect(store.close).toHaveBeenCalledOnce()
})
