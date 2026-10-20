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
const store = () => ({ load: journal.load, put: journal.put, remove: journal.remove, close: () => {} })
beforeEach(() => {
  journal.rows.clear()
  journal.put.mockReset().mockImplementation(async draft => { journal.rows.set(draft.id, structuredClone(draft)) })
  journal.remove.mockReset().mockImplementation(async id => { journal.rows.delete(id) })
  journal.load.mockReset().mockImplementation(async () => [...journal.rows.values()])
  journal.open.mockReset().mockImplementation(async () => store())
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
const heldDraft = (server: Y.Doc) => {
  const other = new Y.Doc({ gc: false }); Y.applyUpdate(other, Y.encodeStateAsUpdate(server)); text(other).insert(4, " old")
  let b = ""; for (const byte of Y.encodeStateAsUpdate(other, Y.encodeStateVector(server))) b += String.fromCharCode(byte)
  const draft = { id: "d1", update: btoa(b), attempted: false, schema: 3, epoch: "epoch", snapshot: documentSchema.snapshot(other) }
  other.destroy(); return draft
}
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
    await waitFor(() => expect(result.current.recoveredDrafts).toHaveLength(1), { timeout: 4000 })
    expect(result.current.durability).toBe("held")
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
    await waitFor(() => expect(result.current.storageUnavailable).toBe(false), { timeout: 4000 })
    await waitFor(() => expect(result.current.durability).toBe("local"), { timeout: 4000 })
    expect(journal.rows.size).toBe(1)
  } finally { unmount(); server.destroy() }
})
