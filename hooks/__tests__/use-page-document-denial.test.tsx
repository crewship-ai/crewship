import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, renderHook as baseRenderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { documentSchema } from "@/lib/documents/schema"
import { DocumentStorageError, purgeOtherDocumentDrafts } from "@/lib/document-queue"
import { usePageDocument } from "../use-page-document"

const fetchMock = vi.hoisted(() => vi.fn())
const journal = vi.hoisted(() => ({ purge: vi.fn(), exclusive: true, rows: [] as unknown[], put: vi.fn(), remove: vi.fn() }))
vi.mock("@/lib/document-queue", async importOriginal => ({
  ...await importOriginal<typeof import("@/lib/document-queue")>(),
  purgeDocumentDrafts: journal.purge,
  openDocumentDrafts: async () => ({ load: async () => journal.rows, put: journal.put, remove: journal.remove, close: () => {}, exclusive: journal.exclusive }),
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "human" } } }) }))
beforeEach(() => { journal.purge.mockReset().mockResolvedValue(undefined); journal.exclusive = true; journal.rows = []; journal.put.mockReset().mockResolvedValue(undefined); journal.remove.mockReset().mockResolvedValue(undefined) })
afterEach(() => { vi.restoreAllMocks(); fetchMock.mockReset() })

const renderHook: typeof baseRenderHook = (callback, options) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return baseRenderHook(callback, { ...options, wrapper: ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children) })
}
const model = () => documentSchema.create({ type: "doc", content: [{ type: "paragraph", attrs: { id: "paragraph" }, content: [{ type: "text", text: "private" }] }] }, 3)
const text = (doc: Y.Doc) => { const node = documentSchema.textNode(doc, "paragraph"); if (!(node instanceof Y.XmlText)) throw new Error("text"); return node }
const response = (value: unknown, status = 200) => Promise.resolve({ ok: status < 400, status, json: async () => value })
const head = (doc: Y.Doc) => ({ page_id: "page", schema: 3, rev: 4, epoch: "epoch", generation: 1, incarnation: "i", snapshot: documentSchema.snapshot(doc), snapshotRev: 4, updates: [], blocks: [], content: { type: "doc" }, outline: [], can_write: true, can_comment: false, can_resolve: false })

it.each([401, 403, 404])("a %i from a read already in flight wins over a journal failure", async status => {
  const server = model()
  let hold = false, started = false
  let resolveRead: (() => void) | undefined
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => {
    if (init?.method === "POST") return response({ error: "offline" }, 503)
    if (!hold) return response(head(server))
    started = true
    return new Promise(resolve => { resolveRead = () => resolve({ ok: false, status, json: async () => ({ error: "gone" }) }) })
  })
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    expect(result.current.state).toBeDefined()
    hold = true
    await waitFor(() => expect(started).toBe(true))
    journal.put.mockRejectedValue(new DocumentStorageError("quota"))
    act(() => text(result.current.doc).insert(7, " x"))
    await waitFor(() => expect(journal.put).toHaveBeenCalled())
    await waitFor(() => expect(result.current.durability).toBe("unsaved"))
    act(() => resolveRead!())
    await waitFor(() => expect(result.current.denied).toBe(true))
    expect(result.current.state).toBeUndefined()
    expect(result.current.status).toBe("Your latest edits were not saved: access to this document was removed.")
    expect(result.current.recoveredDrafts).toHaveLength(0)
  } finally { unmount(); server.destroy() }
})

it("a journal write failing after the denial does not reopen the document status", async () => {
  const server = model()
  let hold = false, started = false
  let resolveRead: (() => void) | undefined
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => {
    if (init?.method === "POST") return response({ error: "offline" }, 503)
    if (!hold) return response(head(server))
    started = true
    return new Promise(resolve => { resolveRead = () => resolve({ ok: false, status: 404, json: async () => ({ error: "gone" }) }) })
  })
  let release: (() => void) | undefined
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    hold = true
    await waitFor(() => expect(started).toBe(true))
    journal.put.mockImplementation(() => new Promise<void>((_resolve, reject) => { release = () => reject(new DocumentStorageError("quota")) }))
    act(() => text(result.current.doc).insert(7, " x"))
    await waitFor(() => expect(release).toBeDefined())
    act(() => resolveRead!())
    await waitFor(() => expect(result.current.denied).toBe(true))
    act(() => release!())
    await new Promise(resolve => setTimeout(resolve, 50))
    expect(result.current.state).toBeUndefined()
    expect(result.current.status).toBe("Your latest edits were not saved: access to this document was removed.")
    expect(result.current.denied).toBe(true)
  } finally { unmount(); server.destroy() }
})

it.each([
  ["a lockless journal row", { exclusive: false, expect: "unverifiedDrafts" }],
  ["a recovered journal row", { exclusive: true, expect: "recoveredDrafts" }],
] as const)("a denial clears %s that was already exposed", async (_name, mode) => {
  const server = model()
  journal.exclusive = mode.exclusive
  journal.rows = [{ id: "d1", update: "", attempted: false, schema: 3, epoch: "epoch", snapshot: documentSchema.snapshot(server) }]
  let deny = false
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({}, 500) : deny ? response({ error: "gone" }, 403) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current[mode.expect]).toHaveLength(1))
    deny = true
    await waitFor(() => expect(result.current.denied).toBe(true))
    expect(result.current.state).toBeUndefined()
    expect(result.current[mode.expect]).toHaveLength(0)
  } finally { unmount(); server.destroy() }
})

it("keeps checking authority after the journal broke: a later 404 clears the content", async () => {
  const server = model()
  let deny = false
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "offline" }, 503) : deny ? response({ error: "gone" }, 404) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    journal.put.mockRejectedValue(new DocumentStorageError("quota"))
    act(() => text(result.current.doc).insert(7, " x"))
    await waitFor(() => expect(result.current.durability).toBe("unsaved"))
    expect(result.current.state).toBeDefined()
    deny = true
    await waitFor(() => expect(result.current.denied).toBe(true), { timeout: 3000 })
    expect(result.current.state).toBeUndefined()
    expect(result.current.status).toBe("Your latest edits were not saved: access to this document was removed.")
  } finally { unmount(); server.destroy() }
})

it("a 200 while blocked neither resumes editing nor journals nor sends", async () => {
  const server = model()
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "offline" }, 503) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    journal.put.mockRejectedValue(new DocumentStorageError("quota"))
    act(() => text(result.current.doc).insert(7, " x"))
    await waitFor(() => expect(result.current.durability).toBe("unsaved"))
    const gets = () => fetchMock.mock.calls.filter(([, init]) => init?.method !== "POST").length
    const posts = () => fetchMock.mock.calls.filter(([, init]) => init?.method === "POST").length
    const before = { gets: gets(), puts: journal.put.mock.calls.length, posts: posts() }
    await waitFor(() => expect(gets()).toBeGreaterThan(before.gets + 1), { timeout: 3000 })
    expect(journal.put.mock.calls.length).toBe(before.puts)
    expect(posts()).toBe(before.posts)
    expect(result.current.editable).toBe(false)
    expect(result.current.denied).toBe(false)
    expect(result.current.durability).toBe("unsaved")
  } finally { unmount(); server.destroy() }
})

it.each([401, 403, 404])("a %i deletes this Page's journal rows from storage, not only from memory", async status => {
  const server = model()
  let deny = false
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({}, 500) : deny ? response({ error: "gone" }, status) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    expect(journal.purge).not.toHaveBeenCalled()
    deny = true
    await waitFor(() => expect(result.current.denied).toBe(true))
    await waitFor(() => expect(journal.purge).toHaveBeenCalledWith("human", "w", "page"))
  } finally { unmount(); server.destroy() }
})

it("an external deny() purges the journal too", async () => {
  const server = model()
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({}, 500) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    act(() => result.current.deny())
    await waitFor(() => expect(journal.purge).toHaveBeenCalledWith("human", "w", "page"))
  } finally { unmount(); server.destroy() }
})

it("rows exposed as recovered or staged are purged on denial, and a write in flight cannot resurrect them", async () => {
  const server = model()
  journal.exclusive = false
  journal.rows = [{ id: "d1", update: "", attempted: false, schema: 3, epoch: "epoch", snapshot: documentSchema.snapshot(server) }]
  let deny = false
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({}, 500) : deny ? response({ error: "gone" }, 404) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.unverifiedDrafts).toHaveLength(1))
    deny = true
    await waitFor(() => expect(result.current.denied).toBe(true))
    expect(result.current.unverifiedDrafts).toHaveLength(0)
    await waitFor(() => expect(journal.purge).toHaveBeenCalledWith("human", "w", "page"))
  } finally { unmount(); server.destroy() }
})

it("the purge waits for a journal write already in flight and no write follows the denial", async () => {
  const server = model()
  let hold = false, started = false
  let resolveRead: (() => void) | undefined
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => {
    if (init?.method === "POST") return response({ error: "offline" }, 503)
    if (!hold) return response(head(server))
    started = true
    return new Promise(resolve => { resolveRead = () => resolve({ ok: false, status: 403, json: async () => ({ error: "gone" }) }) })
  })
  const order: string[] = []
  let finishPut: (() => void) | undefined
  journal.put.mockImplementation(() => new Promise<void>(resolve => { finishPut = () => { order.push("put"); resolve() } }))
  journal.purge.mockImplementation(async () => { order.push("purge") })
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    hold = true
    await waitFor(() => expect(started).toBe(true))
    act(() => text(result.current.doc).insert(7, " x"))
    await waitFor(() => expect(finishPut).toBeDefined())
    act(() => resolveRead!())
    await waitFor(() => expect(result.current.denied).toBe(true))
    expect(journal.purge).not.toHaveBeenCalled()
    act(() => finishPut!())
    await waitFor(() => expect(journal.purge).toHaveBeenCalled())
    expect(order).toEqual(["put", "purge"])
    act(() => text(result.current.doc).insert(7, " y"))
    await new Promise(resolve => setTimeout(resolve, 400))
    expect(journal.put).toHaveBeenCalledTimes(1)
  } finally { unmount(); server.destroy() }
})

it("a refused write says the latest edits were not saved and keeps no copy of them", async () => {
  const server = model()
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "forbidden" }, 403) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    act(() => text(result.current.doc).insert(7, " quoted"))
    await waitFor(() => expect(result.current.denied).toBe(true), { timeout: 3000 })
    expect(result.current.status).toBe("Your latest edits were not saved: access to this document was removed.")
    expect(result.current.state).toBeUndefined()
    expect(result.current.recoveredDrafts).toHaveLength(0)
    await waitFor(() => expect(journal.purge).toHaveBeenCalledWith("human", "w", "page"))
  } finally { unmount(); server.destroy() }
})

it("a denial with nothing unsent keeps the plain access message", async () => {
  const server = model()
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({}, 500) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    act(() => result.current.deny())
    await waitFor(() => expect(result.current.denied).toBe(true))
    expect(result.current.status).toBe("Document access is unavailable.")
  } finally { unmount(); server.destroy() }
})

it("signing out with unsent edits says they were not saved", async () => {
  const server = model()
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "offline" }, 503) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    act(() => text(result.current.doc).insert(7, " quoted"))
    await waitFor(() => expect(result.current.pending).toBeGreaterThan(0))
    await act(async () => { await purgeOtherDocumentDrafts(null).catch(() => {}) })
    await waitFor(() => expect(result.current.denied).toBe(true))
    expect(result.current.status).toBe("Your latest edits were not saved: access to this document was removed.")
  } finally { unmount(); server.destroy() }
})
