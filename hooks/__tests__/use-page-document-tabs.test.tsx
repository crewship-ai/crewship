vi.mock("@/lib/protected-document-drafts", () => ({ openProtectedDocumentDrafts: async (...args: Parameters<typeof import("@/lib/document-queue").openDocumentDrafts>) => (await import("@/lib/document-queue")).openDocumentDrafts(...args) }))
// Persistence mechanics are tested here; protected-store authorization and
// encryption are exercised by the real-browser and protected-document tests.
vi.mock("@/lib/local-draft-seal", () => ({
 createLocalDraftCipher: async () => ({ seal: async (text: string) => text, open: async (text: string) => text, close() {} }),
 isSealedLocalDraft: () => false, LocalDraftProtectionError: class extends Error {}, LocalDraftReadRevoked: class extends Error {},
}))
import { Fragment, StrictMode, createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, renderHook as baseRenderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { documentSchema } from "@/lib/documents/schema"
import { usePageDocument } from "../use-page-document"

vi.mock("@/lib/document-queue", async original => ({...await original<typeof import("@/lib/document-queue")>(), captureDocumentDrafts: async () => []}))
const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "human" } } }) }))

// One shared browser profile: IndexedDB rows and Web Locks are visible to every hook instance below.
const rows = new Map<string, { key: unknown[]; draft: { id: string } }>()
function browser() {
  rows.clear()
  const held = new Set<string>(), waiting = new Map<string, (() => void)[]>()
  const grant = (name: string, cb: (l: unknown) => Promise<void> | void) => { held.add(name); return Promise.resolve(cb({ name })).finally(() => { held.delete(name); waiting.get(name)?.shift()?.() }) }
  vi.stubGlobal("navigator", { locks: { request: (name: string, options: { signal?: AbortSignal }, cb: (l: unknown) => Promise<void> | void) => {
    if (!held.has(name)) return grant(name, cb)
    return new Promise<void>((resolve, reject) => {
      const start = () => { resolve(); void grant(name, cb) }
      waiting.set(name, [...(waiting.get(name) ?? []), start])
      options.signal?.addEventListener("abort", () => { waiting.set(name, (waiting.get(name) ?? []).filter(i => i !== start)); reject(new DOMException("aborted", "AbortError")) })
    })
  } } })
  vi.stubGlobal("IDBKeyRange", { bound: (lo: unknown[]) => ({ owner: lo[0] }) })
  vi.stubGlobal("indexedDB", { open: () => {
    const request: Record<string, unknown> = {}
    const db = {
      onversionchange: null, close: () => {}, exclusive: true,
      transaction: () => {
        const tx: Record<string, unknown> = {}
        const done = (result: unknown) => { const r = { result }; setTimeout(() => (tx.oncomplete as () => void)?.(), 0); return r }
        tx.objectStore = () => ({
          getAll: (range: { owner: unknown }) => done([...rows.values()].filter(row => row.key[0] === range.owner)),
          put: (value: { key: unknown[]; draft: { id: string } }) => { rows.set(JSON.stringify(value.key), structuredClone(value)); return done(undefined) },
          delete: (key: unknown[]) => { rows.delete(JSON.stringify(key)); return done(undefined) },
        })
        return tx
      },
    }
    Object.defineProperty(request, "result", { get: () => db })
    setTimeout(() => (request.onsuccess as () => void)(), 0)
    return request
  } })
}
beforeEach(() => { vi.useFakeTimers({ toFake: ["Date"] }); browser() })
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); fetchMock.mockReset() })

const renderHook: typeof baseRenderHook = (callback, options) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return baseRenderHook(callback, { ...options, wrapper: ({ children }: { children: ReactNode }) => createElement(options?.wrapper ?? StrictMode, null, createElement(QueryClientProvider, { client }, children)) })
}
const model = () => documentSchema.create({ type: "doc", content: [{ type: "paragraph", attrs: { id: "paragraph" }, content: [{ type: "text", text: "base" }] }] }, 3)
const text = (doc: Y.Doc) => { const node = documentSchema.textNode(doc, "paragraph"); if (!(node instanceof Y.XmlText)) throw new Error("text"); return node }
const response = (value: unknown, status = 200) => Promise.resolve({ ok: status < 400, status, json: async () => value })
const head = (doc: Y.Doc) => ({ page_id: "page", schema: 3, rev: 4, epoch: "epoch", generation: 1, incarnation: "i", snapshot: documentSchema.snapshot(doc), snapshotRev: 4, updates: [], blocks: [], content: { type: "doc" }, outline: [], can_write: true, can_comment: false, can_resolve: false })

it("a same-tab remount (StrictMode) takes over its own journal instead of reporting another tab", async () => {
  const server = model()
  fetchMock.mockImplementation(() => response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.editable).toBe(true), { timeout: 4000 })
    expect(result.current.storageBusy).toBe(false)
    expect(result.current.storageUnavailable).toBe(false)
  } finally { unmount(); server.destroy() }
})

it("tab B sees tab A's unconfirmed batch as recovery only after tab A closes", async () => {
  const server = model()
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "offline" }, 503) : response(head(server)))
  const a = baseRenderHook(() => usePageDocument("w", "slug", "page"), { wrapper: ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client: new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } }) }, children) })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const b = baseRenderHook(() => usePageDocument("w", "slug", "page"), { wrapper: ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children) })
  try {
    await waitFor(() => expect(a.result.current.editable).toBe(true), { timeout: 4000 })
    act(() => text(a.result.current.doc).insert(4, " a"))
    await waitFor(() => expect(a.result.current.durability).toBe("local"), { timeout: 4000 })
    await waitFor(() => expect(b.result.current.storageBusy).toBe(true), { timeout: 4000 })
    expect(b.result.current.recoveredDrafts).toHaveLength(0)
    expect(rows.size).toBe(1)
    a.unmount()
    vi.setSystemTime(Date.now() + 6000)
    await waitFor(() => expect(b.result.current.recoveredDrafts).toHaveLength(1), { timeout: 4000 })
  } finally { b.unmount(); server.destroy() }
})

it("without Web Locks a journal row of a live tab (e.g. from a locking browser tab) survives tab B entirely: B only downloads, never deletes", async () => {
  vi.stubGlobal("navigator", {})
  const server = model()
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "offline" }, 503) : response(head(server)))
  const owner = JSON.stringify(["human", "w", "page"])
  const key = [owner, JSON.stringify([3, "epoch", "live"])]
  rows.set(JSON.stringify(key), { key, draft: { id: "live", update: "", attempted: false, schema: 3, epoch: "epoch", snapshot: documentSchema.snapshot(server) } as never })
  const b = renderHook(() => usePageDocument("w", "slug", "page"), { wrapper: Fragment })
  try {
    await waitFor(() => expect(b.result.current.unverifiedDrafts).toHaveLength(1), { timeout: 4000 })
    expect(b.result.current.editable).toBe(true)
    expect(b.result.current.recovery).toBe(false)
    expect(b.result.current.recoveredDrafts).toHaveLength(0)
    expect(b.result.current.storageUnavailable).toBe(true)
    expect("discardUnverifiedDrafts" in b.result.current).toBe(false) // there is no shared delete without a lock
    act(() => text(b.result.current.doc).insert(4, " b"))
    await waitFor(() => expect(b.result.current.durability).toBe("unsaved"))
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 600)) })
    b.unmount()
    expect([...rows.keys()]).toEqual([JSON.stringify(key)]) // untouched, nothing added
  } finally { server.destroy() }
})

it("without a lock an empty journal is still never written: no undeletable rows are created", async () => {
  vi.stubGlobal("navigator", {})
  const server = model()
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "offline" }, 503) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"), { wrapper: Fragment })
  try {
    await waitFor(() => expect(result.current.editable).toBe(true), { timeout: 4000 })
    expect(result.current.storageUnavailable).toBe(true)
    expect(result.current.storageLockless).toBe(true)
    act(() => text(result.current.doc).insert(4, " x"))
    await waitFor(() => expect(result.current.durability).toBe("unsaved"))
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 800)) })
    expect(rows.size).toBe(0)
    expect(result.current.durability).toBe("unsaved")
  } finally { unmount(); server.destroy() }
})
