import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import type { Editor } from "@tiptap/core"
import { PageDocument } from "../page-document"

// S1.4: access revoked while the human is editing. The editor ends, and the
// Document leaves the screen, the private browser journal and the React Query
// cache, including the edit that never reached the server.
const request = vi.hoisted(() => vi.fn())
const purge = vi.hoisted(() => vi.fn(async () => {}))
vi.mock("@/hooks/use-documents-availability", () => ({ useDocumentsAvailability: () => ({ data: true, isPending: false }) }))
vi.mock("@/lib/document-queue", async importOriginal => ({
  ...await importOriginal<typeof import("@/lib/document-queue")>(),
  openDocumentDrafts: async () => ({ load: async () => [], put: async () => {}, remove: async () => {}, close: () => {}, exclusive: true }),
  purgeDocumentDrafts: purge,
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request, broadcastSessionExpired: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "author" } } }) }))
afterEach(() => { cleanup(); request.mockReset(); purge.mockClear() })

it.each([
  ["the next read", "GET"],
  ["the save of that edit", "POST"],
] as const)("revoked while editing, found by %s: no content stays on screen or in the managed caches", async (_found, deniedMethod) => {
  const { documentSchema } = await import("@/lib/documents/schema")
  const saved = documentSchema.create({ type: "doc", content: [{ type: "paragraph", attrs: { id: "private" }, content: [{ type: "text", text: "Private server text" }] }] }, 3)
  let revoked = false
  request.mockImplementation(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET"
    if (revoked && method === deniedMethod) return { ok: false, status: 403, json: async () => ({ error: "forbidden" }) }
    if (method !== "GET") return { ok: false, status: 503, json: async () => ({ error: "unavailable" }) }
    return { ok: true, json: async () => url.includes("/comments") ? { comments: [], can_comment: false } : url.includes("/requests") ? { requests: [], can_dispatch: false, eligible_chats: [] } : { page_id: "page", schema: 3, rev: 1, epoch: "epoch", can_write: true, snapshot: documentSchema.snapshot(saved) } }
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(createElement(QueryClientProvider, { client }, <PageDocument workspaceId="w" slug="notes" pageId="page" /> as ReactNode))
  await screen.findByText("Private server text")
  const editor = (screen.getByLabelText("Document editor") as HTMLElement & { editor: Editor }).editor
  await waitFor(() => expect(editor.isEditable).toBe(true))
  act(() => { editor.commands.insertContent("Unsent private edit ") })
  revoked = true

  expect(await screen.findByRole("alert")).toHaveTextContent("Your latest edits were not saved: access to this document was removed.")
  expect(screen.queryByLabelText("Document editor")).toBeNull()
  if (deniedMethod === "POST") expect(request).toHaveBeenCalledWith(expect.stringContaining("/document/patch"), expect.objectContaining({ method: "POST" }))
  expect(document.body.textContent).not.toMatch(/Private server text|Unsent private edit/)
  expect(screen.queryByRole("heading", { name: "notes" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Download unsaved draft" })).toBeNull()
  await waitFor(() => expect(purge).toHaveBeenCalledWith("author", "w", "page"))
  // Reads use gcTime 0 and a denial removes the entry; the cache must hold nothing either way.
  expect(client.getQueryCache().findAll({ queryKey: ["page-document", "w"] })).toHaveLength(0)
  saved.destroy()
})
