import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, render, renderHook as baseRenderHook, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { documentSchema } from "@/lib/documents/schema"
import { DocumentSaveState } from "@/components/features/pages/document-save-state"
import { usePageDocument } from "../use-page-document"

const fetchMock = vi.hoisted(() => vi.fn())
const journal = vi.hoisted(() => ({ rows: new Map<string, unknown>(), put: vi.fn(), remove: vi.fn(), open: vi.fn(), load: vi.fn() }))
vi.mock("@/lib/document-queue", async importOriginal => ({
  ...await importOriginal<typeof import("@/lib/document-queue")>(),
  openDocumentDrafts: (...args: unknown[]) => journal.open(...args),
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "human" } } }) }))
const store = () => ({ load: journal.load, put: journal.put, remove: journal.remove, close: () => {}, exclusive: true })
beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] }) // only the clock the retry backoff reads
  journal.rows.clear()
  journal.put.mockReset().mockImplementation(async draft => { journal.rows.set(draft.id, structuredClone(draft)) })
  journal.remove.mockReset().mockImplementation(async id => { journal.rows.delete(id) })
  journal.load.mockReset().mockImplementation(async () => [...journal.rows.values()])
  journal.open.mockReset().mockImplementation(async () => store())
})
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); fetchMock.mockReset() })

const renderHook: typeof baseRenderHook = (callback, options) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return baseRenderHook(callback, { ...options, wrapper: ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children) })
}
const model = () => documentSchema.create({ type: "doc", content: [{ type: "paragraph", attrs: { id: "paragraph" }, content: [{ type: "text", text: "base" }] }] }, 3)
const text = (doc: Y.Doc) => { const node = documentSchema.textNode(doc, "paragraph"); if (!(node instanceof Y.XmlText)) throw new Error("text"); return node }
const response = (value: unknown, status = 200) => Promise.resolve({ ok: status < 400, status, json: async () => value })
const head = (doc: Y.Doc) => ({ page_id: "page", schema: 3, rev: 4, epoch: "epoch", generation: 1, incarnation: "i", snapshot: documentSchema.snapshot(doc), snapshotRev: 4, updates: [], blocks: [], content: { type: "doc" }, outline: [], can_write: true, can_comment: false, can_resolve: false })
const heldDraft = (server: Y.Doc) => {
  const other = new Y.Doc({ gc: false }); Y.applyUpdate(other, Y.encodeStateAsUpdate(server)); text(other).insert(4, " old")
  let b = ""; for (const byte of Y.encodeStateAsUpdate(other, Y.encodeStateVector(server))) b += String.fromCharCode(byte)
  const draft = { id: "d1", update: btoa(b), attempted: false, schema: 3, epoch: "epoch", snapshot: documentSchema.snapshot(other) }
  other.destroy(); return draft
}
const later = (ms = 6000) => vi.setSystemTime(Date.now() + ms)
const storageError = async () => new (await import("@/lib/document-queue")).DocumentStorageError("down")

it("confirms a save by the server in no-journal mode and says the browser storage is unavailable", async () => {
  journal.open.mockRejectedValue(await storageError())
  const server = model()
  let rev = 4
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => { if (init?.method === "POST") { const body = JSON.parse(init.body as string); rev = 5; return response({ id: body.id, epoch: body.epoch, rev: 5 }) } return response({ ...head(server), rev }) })
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    expect(result.current.storageUnavailable).toBe(true)
    act(() => text(result.current.doc).insert(4, " x"))
    await waitFor(() => expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(true))
    await waitFor(() => expect(result.current.pending).toBe(0), { timeout: 4000 })
    expect(result.current.durability).toBe("confirmed")
    expect(result.current.status).toBe("Saved")
    const view = render(<DocumentSaveState status={result.current.status} durability={result.current.durability} storageUnavailable={result.current.storageUnavailable} />)
    expect(view.container.textContent).toContain("Confirmed by server")
    expect(view.container.textContent).toContain("This browser's storage is not available; an earlier unsent draft may exist")
  } finally { unmount(); server.destroy() }
})

it("stays editable and in no-journal mode when open succeeds but load fails", async () => {
  journal.load.mockRejectedValue(await storageError())
  const server = model()
  fetchMock.mockImplementation(() => response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    expect(result.current.storageUnavailable).toBe(true)
  } finally { unmount(); server.destroy() }
})

it("retries the open and surfaces a held draft, keeping in-memory edits and sending nothing", async () => {
  const server = model()
  journal.rows.set("d1", heldDraft(server))
  journal.open.mockRejectedValueOnce(await storageError())
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "no" }, 503) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.storageUnavailable).toBe(true))
    act(() => text(result.current.doc).insert(4, " mem"))
    later()
    await waitFor(() => expect(result.current.recoveredDrafts).toHaveLength(1), { timeout: 4000 })
    expect(result.current.durability).toBe("held")
    expect(result.current.pending).toBe(2)
    expect(result.current.storageUnavailable).toBe(false)
    expect(result.current.recovery).toBe(true)
    expect(result.current.editable).toBe(false)
    expect(text(result.current.doc).toString()).toContain(" mem")
    const posts = fetchMock.mock.calls.filter(([, init]) => init?.method === "POST").length
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 700)) })
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST").length).toBe(posts)
  } finally { unmount(); server.destroy() }
})

it("journals queued in-memory edits when a retry finds no held draft", async () => {
  const server = model()
  journal.open.mockRejectedValueOnce(await storageError())
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "offline" }, 503) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.storageUnavailable).toBe(true))
    act(() => text(result.current.doc).insert(4, " mem"))
    await waitFor(() => expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(true))
    later()
    await waitFor(() => expect(result.current.storageUnavailable).toBe(false), { timeout: 4000 })
    await waitFor(() => expect(result.current.durability).toBe("local"), { timeout: 4000 })
    expect(journal.rows.size).toBe(1)
    const [row] = [...journal.rows.values()] as { id: string; attempted: boolean; request?: { id: string } }[]
    expect(row.attempted).toBe(true)
    expect(row.request?.id).toBe(row.id)
  } finally { unmount(); server.destroy() }
})

it("backs off the retry: an instantly rejecting store is not reopened on every poll", async () => {
  journal.open.mockRejectedValue(await storageError())
  const server = model()
  fetchMock.mockImplementation(() => response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.storageUnavailable).toBe(true))
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 1600)) })
    expect(journal.open).toHaveBeenCalledTimes(1)
    later(); // first backoff step is 5 s
    await waitFor(() => expect(journal.open).toHaveBeenCalledTimes(2))
    later(6000) // the next step is longer than 6 s
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 1200)) })
    expect(journal.open).toHaveBeenCalledTimes(2)
  } finally { unmount(); server.destroy() }
})

it("stops retrying after unmount", async () => {
  journal.open.mockRejectedValue(await storageError())
  const server = model()
  fetchMock.mockImplementation(() => response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  await waitFor(() => expect(result.current.storageUnavailable).toBe(true))
  unmount()
  later(120000)
  await new Promise(resolve => setTimeout(resolve, 1200))
  expect(journal.open).toHaveBeenCalledTimes(1)
  server.destroy()
})

it("sends queued edits while a slow retry is pending, then recovers and clears the unavailable text", async () => {
  const server = model()
  let finish: ((value: ReturnType<typeof store>) => void) | undefined
  journal.open.mockRejectedValueOnce(await storageError()).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  let rev = 4
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => { if (init?.method === "POST") { const body = JSON.parse(init.body as string); rev = 5; return response({ id: body.id, epoch: body.epoch, rev: 5 }) } return response({ ...head(server), rev }) })
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.storageUnavailable).toBe(true))
    later()
    await waitFor(() => expect(finish).toBeDefined())
    act(() => text(result.current.doc).insert(4, " x"))
    await waitFor(() => expect(result.current.pending).toBe(0), { timeout: 3000 }) // sent while the open is still hanging
    expect(result.current.durability).toBe("confirmed")
    await act(async () => finish!(store()))
    await waitFor(() => expect(result.current.storageUnavailable).toBe(false))
    await waitFor(() => expect(result.current.editable).toBe(true))
  } finally { unmount(); server.destroy() }
})

it("keeps session edits in the pending count and sends them once the held draft is discarded", async () => {
  const server = model()
  journal.rows.set("d1", heldDraft(server))
  journal.open.mockRejectedValueOnce(await storageError())
  let rev = 4
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => { if (init?.method === "POST") { const body = JSON.parse(init.body as string); rev = 5; return response({ id: body.id, epoch: body.epoch, rev: 5 }) } return response({ ...head(server), rev }) })
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.storageUnavailable).toBe(true))
    fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "offline" }, 503) : response({ ...head(server), rev }))
    act(() => text(result.current.doc).insert(4, " mem"))
    later()
    await waitFor(() => expect(result.current.recoveredDrafts).toHaveLength(1), { timeout: 4000 })
    expect(result.current.pending).toBe(2)
    fetchMock.mockImplementation((_u: string, init?: RequestInit) => { if (init?.method === "POST") { const body = JSON.parse(init.body as string); rev = 5; return response({ id: body.id, epoch: body.epoch, rev: 5 }) } return response({ ...head(server), rev }) })
    await act(async () => { await result.current.discardRecoveredDrafts() })
    await waitFor(() => expect(result.current.pending).toBe(0), { timeout: 4000 })
    expect(result.current.recoveredDrafts).toHaveLength(0)
    expect(journal.rows.has("d1")).toBe(false)
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(true)
  } finally { unmount(); server.destroy() }
})

it("does not offer recovered content or say Saved when access is denied while the store is opening", async () => {
  const server = model()
  journal.rows.set("d1", heldDraft(server))
  let finish: ((value: ReturnType<typeof store>) => void) | undefined
  journal.open.mockImplementation(() => new Promise(resolve => { finish = resolve }))
  fetchMock.mockImplementation(() => response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(finish).toBeDefined())
    act(() => result.current.deny())
    await act(async () => finish!(store()))
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 700)) })
    expect(result.current.recoveredDrafts).toHaveLength(0)
    expect(result.current.state).toBeUndefined()
    expect(result.current.status).not.toBe("Saved")
    expect(result.current.denied).toBe(true)
  } finally { unmount(); server.destroy() }
})

it("keeps dirty protection and never shows confirmed after discarding a held draft while session edits are unsent", async () => {
  const server = model()
  journal.rows.set("d1", heldDraft(server))
  journal.open.mockRejectedValueOnce(await storageError())
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "offline" }, 503) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.storageUnavailable).toBe(true))
    act(() => text(result.current.doc).insert(4, " mem"))
    later()
    await waitFor(() => expect(result.current.recoveredDrafts).toHaveLength(1), { timeout: 4000 })
    await act(async () => { await result.current.discardRecoveredDrafts() })
    expect(result.current.pending).toBe(1)
    expect(result.current.durability).not.toBe("confirmed")
    await waitFor(() => expect(["writing", "local"]).toContain(result.current.durability))
    expect(journal.rows.has("d1")).toBe(false)
    expect(journal.rows.size).toBe(1)
    const event = new Event("beforeunload", { cancelable: true })
    window.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(true)
  } finally { unmount(); server.destroy() }
})

it("treats a journal owned by another tab as busy: nothing is shown as recovery and the edit is still sent", async () => {
  const server = model()
  journal.rows.set("a1", heldDraft(server)) // tab A's live batch, never visible to this tab
  const { DocumentStorageBusy } = await import("@/lib/document-queue")
  journal.open.mockRejectedValue(new DocumentStorageBusy("busy"))
  let rev = 4
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => { if (init?.method === "POST") { const body = JSON.parse(init.body as string); rev = 5; return response({ id: body.id, epoch: body.epoch, rev: 5 }) } return response({ ...head(server), rev }) })
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    expect(result.current.storageBusy).toBe(true)
    expect(result.current.recoveredDrafts).toHaveLength(0)
    act(() => text(result.current.doc).insert(4, " b"))
    await waitFor(() => expect(result.current.pending).toBe(0), { timeout: 3000 })
    expect(journal.remove).not.toHaveBeenCalled()
    expect(journal.rows.has("a1")).toBe(true)
  } finally { unmount(); server.destroy() }
})

it("coalesces rapid edits into few journal writes and keeps every edit", async () => {
  const server = model()
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "offline" }, 503) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    await act(async () => { for (let i = 0; i < 30; i++) text(result.current.doc).insert(4, "x") })
    await waitFor(() => expect(result.current.durability).toBe("local"), { timeout: 4000 })
    expect(journal.put.mock.calls.length).toBeLessThanOrEqual(3)
    const rows = [...journal.rows.values()] as { snapshot: string }[]
    const probe = new Y.Doc({ gc: false })
    Y.applyUpdate(probe, Uint8Array.from(atob(rows.at(-1)!.snapshot), c => c.charCodeAt(0)))
    expect(text(probe).toString().match(/x/g)).toHaveLength(30)
    probe.destroy()
  } finally { unmount(); server.destroy() }
})

it("re-reads the server after the awaited open: access revoked meanwhile exposes no recovered rows", async () => {
  const server = model()
  journal.rows.set("d1", heldDraft(server))
  let finish: ((value: ReturnType<typeof store>) => void) | undefined
  journal.open.mockImplementation(() => new Promise(resolve => { finish = resolve }))
  let reads = 0
  fetchMock.mockImplementation(() => response(++reads === 1 ? head(server) : { error: "Page unavailable" }, reads === 1 ? 200 : 404))
  const seen: number[] = []
  const { result, unmount } = renderHook(() => { const r = usePageDocument("w", "slug", "page"); seen.push(r.recoveredDrafts.length); return r })
  try {
    await waitFor(() => expect(finish).toBeDefined())
    await act(async () => finish!(store()))
    await waitFor(() => expect(result.current.denied).toBe(true))
    expect(Math.max(...seen)).toBe(0) // never exposed, not merely cleared afterwards
    expect(result.current.recoveredDrafts).toHaveLength(0)
    expect(result.current.state).toBeUndefined()
    expect(result.current.editable).toBe(false)
  } finally { unmount(); server.destroy() }
})

it("writes the journal row before the first send and resends byte-identical request bytes", async () => {
  const server = model()
  const events: string[] = []
  let release: (() => void) | undefined
  journal.put.mockImplementation(async draft => { events.push("put:start"); if (draft.request) await new Promise<void>(resolve => { release = resolve }); journal.rows.set(draft.id, structuredClone(draft)); events.push("put:done") })
  const bodies: string[] = []
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => { if (init?.method === "POST") { events.push("post"); bodies.push(JSON.parse(init.body as string).update); return response({ error: "offline" }, 503) } return response(head(server)) })
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    act(() => text(result.current.doc).insert(4, " x"))
    await waitFor(() => expect(release).toBeDefined())
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 700)) })
    expect(events).not.toContain("post") // the barrier holds while the put is pending
    await act(async () => release!())
    await waitFor(() => expect(bodies.length).toBeGreaterThanOrEqual(2), { timeout: 4000 })
    expect(events.indexOf("post")).toBeGreaterThan(events.lastIndexOf("put:done") - 1)
    expect(new Set(bodies).size).toBe(1)
    // An unchanged attempted row is not rewritten on every poll.
    expect(journal.put.mock.calls.filter(([draft]) => draft.request).length).toBe(1)
  } finally { unmount(); server.destroy() }
})

it("(a) discard keeps an epoch-change block even while the next read hangs", async () => {
  const server = model()
  journal.rows.set("d1", heldDraft(server))
  let epoch = "epoch", hang = false
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "offline" }, 503) : hang ? new Promise(() => {}) : response({ ...head(server), epoch }))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.recoveredDrafts).toHaveLength(1))
    epoch = "other-epoch"
    await waitFor(() => expect(result.current.status).toContain("reopened"), { timeout: 4000 })
    hang = true
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 600)) })
    await act(async () => { await result.current.discardRecoveredDrafts() })
    expect(journal.remove).toHaveBeenCalledWith("d1")
    expect(result.current.recovery).toBe(true)
    expect(result.current.editable).toBe(false)
  } finally { unmount(); server.destroy() }
})

it("(b) discard waits for the pending journal write and never rewrites the discarded row", async () => {
  const server = model()
  journal.rows.set("d1", heldDraft(server))
  journal.open.mockRejectedValueOnce(await storageError())
  let release: (() => void) | undefined
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "offline" }, 503) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.storageUnavailable).toBe(true))
    act(() => text(result.current.doc).insert(4, " mem"))
    journal.put.mockImplementation(async draft => { await new Promise<void>(resolve => { release = resolve }); journal.rows.set(draft.id, structuredClone(draft)) })
    later()
    await waitFor(() => expect(release).toBeDefined(), { timeout: 4000 }) // the session edit is being journaled
    await waitFor(() => expect(result.current.recoveredDrafts).toHaveLength(1))
    let done = false
    const discarding = act(async () => { await result.current.discardRecoveredDrafts(); done = true })
    await new Promise(resolve => setTimeout(resolve, 300))
    expect(journal.remove).not.toHaveBeenCalled()
    expect(done).toBe(false)
    release!()
    await discarding
    expect(journal.remove).toHaveBeenCalledWith("d1")
    expect(journal.rows.has("d1")).toBe(false)
    expect(journal.put.mock.calls.some(([draft]) => draft.id === "d1")).toBe(false)
  } finally { unmount(); server.destroy() }
})

it("(c) discard keeps a failed-journal block", async () => {
  const server = model()
  journal.rows.set("d1", heldDraft(server))
  journal.open.mockRejectedValueOnce(await storageError())
  journal.put.mockRejectedValue(await storageError())
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "offline" }, 503) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.storageUnavailable).toBe(true))
    act(() => text(result.current.doc).insert(4, " mem"))
    later()
    await waitFor(() => expect(result.current.recoveredDrafts).toHaveLength(1), { timeout: 4000 })
    await waitFor(() => expect(journal.put).toHaveBeenCalled())
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 300)) })
    await act(async () => { await result.current.discardRecoveredDrafts() })
    expect(result.current.recovery).toBe(true)
    expect(result.current.editable).toBe(false)
    const posts = fetchMock.mock.calls.filter(([, init]) => init?.method === "POST").length
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 700)) })
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST").length).toBe(posts)
  } finally { unmount(); server.destroy() }
})
