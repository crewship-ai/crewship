import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, renderHook as baseRenderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { documentSchema } from "@/lib/documents/schema"
import { usePageDocument } from "../use-page-document"

const fetchMock = vi.hoisted(() => vi.fn())
const journal = vi.hoisted(() => ({ rows: new Map<string, unknown>(), put: vi.fn(), remove: vi.fn() }))
vi.mock("@/lib/document-queue", async importOriginal => ({
  ...await importOriginal<typeof import("@/lib/document-queue")>(),
  openDocumentDrafts: async () => ({ load: async () => [...journal.rows.values()], put: journal.put, remove: journal.remove, close: () => {}, exclusive: true }),
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "human" } } }) }))
beforeEach(() => {
  journal.rows.clear()
  journal.put.mockReset().mockImplementation(async draft => { journal.rows.set(draft.id, structuredClone(draft)) })
  journal.remove.mockReset().mockImplementation(async id => { journal.rows.delete(id) })
})
afterEach(() => { vi.restoreAllMocks(); fetchMock.mockReset() })

const renderHook: typeof baseRenderHook = (callback, options) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return baseRenderHook(callback, { ...options, wrapper: ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children) })
}
const model = () => documentSchema.create({ type: "doc", content: [{ type: "paragraph", attrs: { id: "paragraph" }, content: [{ type: "text", text: "base" }] }] }, 3)
const text = (doc: Y.Doc) => { const node = documentSchema.textNode(doc, "paragraph"); if (!(node instanceof Y.XmlText)) throw new Error("text"); return node }
const response = (value: unknown, status = 200) => Promise.resolve({ ok: status < 400, status, json: async () => value })
const head = (doc: Y.Doc) => ({ page_id: "page", schema: 3, rev: 4, epoch: "epoch", generation: 1, incarnation: "i", snapshot: documentSchema.snapshot(doc), snapshotRev: 4, updates: [], blocks: [], content: { type: "doc" }, outline: [], can_write: true, can_comment: false, can_resolve: false })

it("is 'writing' until the journal write resolves, then 'local', then 'confirmed' after the server receipt", async () => {
  const server = model()
  let finish: (() => void) | undefined
  let gated = true
  journal.put.mockImplementation(async draft => { if (gated) { gated = false; await new Promise<void>(resolve => { finish = resolve }) }; journal.rows.set(draft.id, structuredClone(draft)) })
  let allowSend = false
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => {
    if (init?.method === "POST") { const body = JSON.parse(init.body as string); return allowSend ? response({ id: body.id, epoch: body.epoch, rev: 5 }) : response({ error: "offline" }, 503) }
    return response(head(server))
  })
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    expect(result.current.durability).toBe("confirmed")
    act(() => text(result.current.doc).insert(4, " x"))
    await waitFor(() => expect(result.current.durability).toBe("writing"))
    await waitFor(() => expect(finish).toBeDefined())
    act(() => finish!()); finish = undefined
    await waitFor(() => expect(result.current.durability).toBe("local"), { timeout: 4000 })
    allowSend = true
    await waitFor(() => expect(result.current.durability).toBe("confirmed"), { timeout: 4000 })
  } finally { unmount(); server.destroy() }
})

it("says 'unsaved' when the browser journal refuses the write", async () => {
  const server = model()
  journal.put.mockRejectedValue(new (await import("@/lib/document-queue")).DocumentStorageError("quota"))
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "offline" }, 503) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true))
    act(() => text(result.current.doc).insert(4, " x"))
    await waitFor(() => expect(result.current.durability).toBe("unsaved"))
  } finally { unmount(); server.destroy() }
})

it("reports an earlier unsent draft as held, not as confirmed", async () => {
  const server = model(), other = new Y.Doc({ gc: false })
  Y.applyUpdate(other, Y.encodeStateAsUpdate(server)); text(other).insert(4, " old")
  const update = (() => { let b = ""; for (const byte of Y.encodeStateAsUpdate(other, Y.encodeStateVector(server))) b += String.fromCharCode(byte); return btoa(b) })()
  journal.rows.set("d1", { id: "d1", update, attempted: false, schema: 3, epoch: "epoch", snapshot: documentSchema.snapshot(other) })
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({}, 500) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.recoveredDrafts).toHaveLength(1))
    expect(result.current.durability).toBe("held")
  } finally { unmount(); server.destroy(); other.destroy() }
})
