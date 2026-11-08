vi.mock("@/lib/protected-document-drafts", () => ({ openProtectedDocumentDrafts: async (...args: Parameters<typeof import("@/lib/document-queue").openDocumentDrafts>) => (await import("@/lib/document-queue")).openDocumentDrafts(...args) }))
// Persistence mechanics are tested here; protected-store authorization and
// encryption are exercised by the real-browser and protected-document tests.
vi.mock("@/lib/local-draft-seal", () => ({
 createLocalDraftCipher: async () => ({ seal: async (text: string) => text, open: async (text: string) => text, close() {} }),
 isSealedLocalDraft: () => false, LocalDraftProtectionError: class extends Error {}, LocalDraftReadRevoked: class extends Error {},
}))
import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, renderHook as baseRenderHook, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { documentSchema } from "@/lib/documents/schema"
import { decodeUpdate, usePageDocument } from "../use-page-document"

const fetchMock = vi.hoisted(() => vi.fn())
// Each mounted editor is a separate browser with its own private journal.
vi.mock("@/lib/document-queue", async importOriginal => ({
 ...await importOriginal<typeof import("@/lib/document-queue")>(),
 openDocumentDrafts: async () => { const rows = new Map<string, unknown>(); return { load: async () => [...rows.values()], put: async (draft: { id: string }) => { rows.set(draft.id, structuredClone(draft)) }, remove: async (id: string) => { rows.delete(id) }, close: () => {}, exclusive: true } },
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "document-save-state-human" } } }) }))
afterEach(() => { vi.restoreAllMocks(); fetchMock.mockReset() })

const renderHook: typeof baseRenderHook = (callback, options) => {
 const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
 return baseRenderHook(callback, { ...options, wrapper: ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children) })
}
const text = (doc: Y.Doc) => { const node = documentSchema.textNode(doc, "paragraph"); if (!(node instanceof Y.XmlText)) throw new Error("text"); return node }
const response = (value: unknown, status = 200) => Promise.resolve({ ok: status < 400, status, json: async () => value })

// #5130 scenario 4: the author's editor says "Saving…" until its own durable
// receipt arrives, even after the committed update already reached another
// client, and never says "Saved" on the strength of that other client's view.
it("shows Saving… until the durable receipt, not when the update only reached another client", async () => {
 const server = documentSchema.create({ type: "doc", content: [{ type: "paragraph", attrs: { id: "paragraph" }, content: [{ type: "text", text: "base" }] }] }, 3)
 let rev = 4, acknowledged = false
 const bodies: string[] = []
 let loseAck: (reason: Error) => void = () => {}
 const head = () => ({ page_id: "page", schema: 3, rev, epoch: "engine-epoch", generation: 1, incarnation: "i", snapshot: documentSchema.snapshot(server), snapshotRev: rev, updates: [], blocks: [], content: { type: "doc" }, outline: [], can_write: true, can_comment: false, can_resolve: false })
 fetchMock.mockImplementation((_url: string, init?: RequestInit) => {
  if (init?.method !== "POST") return response(head())
  bodies.push(init.body as string)
  const body = JSON.parse(init.body as string)
  if (bodies.length === 1) {
   // Durable commit, visible to every reader, but the reply never arrives.
   Y.applyUpdate(server, decodeUpdate(body.update)); rev = 5
   return new Promise((_resolve, reject) => { loseAck = reject })
  }
  acknowledged = true
  return response({ id: body.id, epoch: body.epoch, rev: 5, found: true, duplicate: true })
 })
 const history: { status: string; durability: string; acknowledged: boolean }[] = []
 const author = renderHook(() => { const value = usePageDocument("w", "slug", "page"); history.push({ status: value.status, durability: value.durability, acknowledged }); return value })
 let reader: ReturnType<typeof renderHook<ReturnType<typeof usePageDocument>, unknown>> | undefined
 try {
  await waitFor(() => expect(author.result.current.status).toBe("Saved"))
  const edited = history.length
  act(() => text(author.result.current.doc).insert(4, " local"))
  await waitFor(() => expect(bodies).toHaveLength(1))
  expect(author.result.current.status).toBe("Saving…")
  expect(author.result.current.durability).not.toBe("confirmed")

  reader = renderHook(() => usePageDocument("w", "slug", "page"))
  await waitFor(() => expect(reader!.result.current.status).toBe("Saved"))
  expect(text(reader.result.current.doc).toString()).toBe("base local")
  // Several poll intervals pass while the author's own receipt is missing.
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 1200)) })
  expect(author.result.current.status).toBe("Saving…")
  expect(author.result.current.pending).toBe(1)

  act(() => loseAck(new TypeError("connection dropped after commit")))
  await waitFor(() => expect(author.result.current.status).toBe("Saved"), { timeout: 4000 })
  expect(bodies).toHaveLength(2)
  expect(bodies[1]).toBe(bodies[0])
  expect(author.result.current.pending).toBe(0)
  expect(author.result.current.durability).toBe("confirmed")
  expect(text(author.result.current.doc).toString()).toBe("base local")
  const afterEdit = history.slice(edited)
  expect(afterEdit.some(entry => entry.status === "Saving…")).toBe(true)
  expect(afterEdit.some(entry => entry.status === "Disconnected · changes are unsaved")).toBe(true)
  expect(afterEdit.filter(entry => entry.status === "Saved" || entry.durability === "confirmed").every(entry => entry.acknowledged)).toBe(true)
 } finally { reader?.unmount(); author.unmount(); server.destroy() }
})
