import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import type { Editor } from "@tiptap/core"
import { documentSchema } from "@/lib/documents/schema"
import { PageDocument } from "../page-document"

const account = vi.hoisted(() => ({ user: "human-A" as string | null, status: "authenticated" }))
const request = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: account.status, data: account.user ? { user: { id: account.user } } : null }) }))
vi.mock("@/hooks/use-documents-availability", () => ({ useDocumentsAvailability: () => ({ data: true, isPending: false }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request, broadcastSessionExpired: vi.fn() }))
afterEach(() => { cleanup(); request.mockReset(); account.user = "human-A"; account.status = "authenticated" })

it.each([200, 403])("unmounts the former human's editor and draft download before the next human's %s response", async (status) => {
  const a = documentSchema.create({ type: "doc", content: [{ type: "paragraph", attrs: { id: "paragraph" }, content: [{ type: "text", text: "A-only private content" }] }] }, 3)
  const b = documentSchema.create({ type: "doc", content: [{ type: "paragraph", attrs: { id: "paragraph" }, content: [{ type: "text", text: "B-authorized content" }] }] }, 3)
  request.mockImplementation((_url: string, init?: RequestInit) => {
    if (init?.method === "POST") return Promise.resolve({ ok: false, status: 400, json: async () => ({ error: "Draft rejected" }) })
    const second = account.user === "human-B"
    return Promise.resolve(second && status === 403
      ? { ok: false, status, json: async () => ({ error: "B has no Page access" }) }
      : { ok: true, status: 200, json: async () => ({ page_id: "page", schema: 3, rev: 1, epoch: "opaque", snapshot: documentSchema.snapshot(second ? b : a), can_write: !second }) })
  })
  const client = new QueryClient()
  const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children)
  const view = render(<PageDocument workspaceId="workspace" slug="slug" pageId="page" />, { wrapper })
  try {
    await screen.findByText("A-only private content")
    const editor = (screen.getByLabelText("Document editor") as HTMLElement & { editor: Editor }).editor
    await waitFor(() => expect(editor.isEditable).toBe(true))
    act(() => { editor.commands.insertContent("A-only unsaved draft ") })
    await screen.findByRole("button", { name: "Download unsaved draft" })
    account.user = "human-B"
    view.rerender(<PageDocument workspaceId="workspace" slug="slug" pageId="page" />)
    expect(screen.queryByText(/A-only private content/)).toBeNull()
    expect(screen.queryByText(/A-only unsaved draft/)).toBeNull()
    expect(screen.queryByRole("button", { name: "Download unsaved draft" })).toBeNull()
    if (status === 403) {
      await screen.findByText(/Document access is unavailable/)
      expect(screen.queryByLabelText("Document editor")).toBeNull()
    } else {
      await screen.findByText("B-authorized content")
      const next = (screen.getByLabelText("Document editor") as HTMLElement & { editor: Editor }).editor
      await waitFor(() => expect(next.isEditable).toBe(false))
      expect(next).not.toBe(editor)
    }
  } finally { view.unmount(); client.clear(); a.destroy(); b.destroy() }
})

it.each(["loading", "unauthenticated", "missing-human"])("does not mount a private editor or recovery controls for %s", async (status) => {
  account.status = status === "missing-human" ? "authenticated" : status
  account.user = status === "missing-human" ? null : "human-A"
  const client = new QueryClient()
  const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children)
  const view = render(<PageDocument workspaceId="workspace" slug="slug" pageId="page" />, { wrapper })
  try {
    expect(screen.queryByLabelText("Document editor")).toBeNull()
    expect(screen.queryByRole("button", { name: "Download unsaved draft" })).toBeNull()
    expect(request).not.toHaveBeenCalled()
  } finally { view.unmount(); client.clear() }
})
