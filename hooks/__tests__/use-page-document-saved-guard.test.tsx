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
import { usePageDocument } from "../use-page-document"

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

// #5162: "Saved" means the server holds what this editor shows. A receipt
// followed by a confirmed read that lacks the edit (or holds different text)
// must never be reported as Saved: the person would believe their intent is
// stored when it is not.
it("never says Saved while the confirmed document differs from the editor", async () => {
 const server = documentSchema.create({ type: "doc", content: [{ type: "paragraph", attrs: { id: "paragraph" }, content: [{ type: "text", text: "base" }] }] }, 3)
 let rev = 4
 const head = () => ({ page_id: "page", schema: 3, rev, epoch: "engine-epoch", generation: 1, incarnation: "i", snapshot: documentSchema.snapshot(server), snapshotRev: rev, updates: [], blocks: [], content: { type: "doc" }, outline: [], can_write: true, can_comment: false, can_resolve: false })
 fetchMock.mockImplementation((_url: string, init?: RequestInit) => {
  if (init?.method !== "POST") return response(head())
  const body = JSON.parse(init.body as string)
  // A receipt for the write, but the confirmed head never contains it.
  rev = 5
  return response({ id: body.id, epoch: body.epoch, rev: 5, found: true, duplicate: false })
 })
 const statuses: string[] = []
 const author = renderHook(() => { const value = usePageDocument("w", "slug", "page"); statuses.push(value.status); return value })
 try {
  await waitFor(() => expect(author.result.current.status).toBe("Saved"))
  const edited = statuses.length
  act(() => text(author.result.current.doc).insert(4, " local"))
  await waitFor(() => expect(author.result.current.pending).toBe(0), { timeout: 4000 })
  await waitFor(() => expect(author.result.current.status).toMatch(/^Not confirmed/), { timeout: 4000 })
  expect(text(author.result.current.doc).toString()).toBe("base local")
  expect(statuses.slice(edited)).not.toContain("Saved")
  // Stopped like any unconfirmable state: recovery offered, no further edits built on the missing content.
  expect(author.result.current.recovery).toBe(true)
  expect(author.result.current.editable).toBe(false)
 } finally { author.unmount(); server.destroy() }
})

// One read without the edit can be a shared in-flight response from before the
// write; it must not lock the person out when the next read holds the edit.
it("reads again after one stale confirmed read and says Saved once the edit is there", async () => {
 const server = documentSchema.create({ type: "doc", content: [{ type: "paragraph", attrs: { id: "paragraph" }, content: [{ type: "text", text: "base" }] }] }, 3)
 let rev = 4, stale = 0, pending: Uint8Array | undefined
 const head = () => {
  // The first read after the receipt still lacks the write; later reads hold it.
  if (pending && stale++ >= 1) { Y.applyUpdate(server, pending); pending = undefined }
  return { page_id: "page", schema: 3, rev, epoch: "engine-epoch", generation: 1, incarnation: "i", snapshot: documentSchema.snapshot(server), snapshotRev: rev, updates: [], blocks: [], content: { type: "doc" }, outline: [], can_write: true, can_comment: false, can_resolve: false }
 }
 fetchMock.mockImplementation((_url: string, init?: RequestInit) => {
  if (init?.method !== "POST") return response(head())
  const body = JSON.parse(init.body as string)
  pending = Uint8Array.from(atob(body.update), c => c.charCodeAt(0)); rev = 5
  return response({ id: body.id, epoch: body.epoch, rev: 5, found: true, duplicate: false })
 })
 const author = renderHook(() => usePageDocument("w", "slug", "page"))
 try {
  await waitFor(() => expect(author.result.current.status).toBe("Saved"))
  act(() => text(author.result.current.doc).insert(4, " local"))
  await waitFor(() => expect(stale).toBeGreaterThan(1), { timeout: 4000 })
  await waitFor(() => expect(author.result.current.status).toBe("Saved"), { timeout: 4000 })
  expect(author.result.current.recovery).toBe(false)
  expect(author.result.current.editable).toBe(true)
 } finally { author.unmount(); server.destroy() }
})
