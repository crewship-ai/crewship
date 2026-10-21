import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, renderHook as baseRenderHook, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { documentSchema } from "@/lib/documents/schema"
import { usePageDocument } from "../use-page-document"

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "human" } } }) }))
afterEach(() => { vi.unstubAllGlobals(); fetchMock.mockReset() })

const renderHook: typeof baseRenderHook = (callback, options) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return baseRenderHook(callback, { ...options, wrapper: ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children) })
}
const model = () => documentSchema.create({ type: "doc", content: [{ type: "paragraph", attrs: { id: "paragraph" }, content: [{ type: "text", text: "base" }] }] }, 3)
const text = (doc: Y.Doc) => { const node = documentSchema.textNode(doc, "paragraph"); if (!(node instanceof Y.XmlText)) throw new Error("text"); return node }
const response = (value: unknown, status = 200) => Promise.resolve({ ok: status < 400, status, json: async () => value })
const head = (doc: Y.Doc) => ({ page_id: "page", schema: 3, rev: 4, epoch: "epoch", generation: 1, incarnation: "i", snapshot: documentSchema.snapshot(doc), snapshotRev: 4, updates: [], blocks: [], content: { type: "doc" }, outline: [], can_write: true, can_comment: false, can_resolve: false })

const storages: Record<string, () => void> = {
  "indexedDB is undefined": () => vi.stubGlobal("indexedDB", undefined),
  "indexedDB.open errors": () => vi.stubGlobal("indexedDB", { open: () => { const request: Record<string, unknown> = {}; setTimeout(() => (request.onerror as () => void)?.(), 0); return request } }),
  "indexedDB.open never settles": () => vi.stubGlobal("indexedDB", { open: () => ({}) }),
}

it.each(Object.keys(storages))("loads the server document and never claims local storage when %s", async name => {
  storages[name]()
  const server = model()
  fetchMock.mockImplementation((_u: string, init?: RequestInit) => init?.method === "POST" ? response({ error: "offline" }, 503) : response(head(server)))
  const { result, unmount } = renderHook(() => usePageDocument("w", "slug", "page"))
  try {
    await waitFor(() => expect(result.current.state?.rev).toBe(4), { timeout: 6000 })
    await waitFor(() => expect(result.current.editable).toBe(true), { timeout: 6000 })
    expect(result.current.denied).toBe(false)
    expect(result.current.status).toBe("Saved")
    act(() => text(result.current.doc).insert(4, " x"))
    await waitFor(() => expect(result.current.pending).toBe(1))
    expect(result.current.durability).toBe("unsaved")
    expect(result.current.recovery).toBe(false)
  } finally { unmount(); server.destroy() }
}, 15000)
