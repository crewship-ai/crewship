import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { execFileSync } from "node:child_process"
import { resolve } from "node:path"
import { act, cleanup, render as baseRender, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import type { Editor } from "@tiptap/core"
import { PageDocument } from "../page-document"
import { encodeUpdate } from "@/hooks/use-page-document"
const request = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-documents-availability", () => ({ useDocumentsAvailability: () => ({data:true,isPending:false}) }))
vi.mock("@/lib/document-queue", async importOriginal => ({...await importOriginal<typeof import("@/lib/document-queue")>(),openDocumentDrafts:async()=>({load:async()=>[],put:async()=>{},remove:async()=>{},close: () => {}, exclusive: true})}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request, broadcastSessionExpired: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "document-test-human" } } }) }))
afterEach(() => { cleanup(); request.mockReset() })
it("validates actual heading, Enter, list, and table updates including retained stable IDs", async () => {
  const saved = new Y.Doc({ gc: false })
  const paragraph = new Y.XmlElement("paragraph"); paragraph.setAttribute("id", "paragraph")
  const text = new Y.XmlText(); text.insert(0, "Alpha Beta"); paragraph.insert(0, [text]); saved.getXmlFragment("default").insert(0, [paragraph])
  const before = encodeUpdate(Y.encodeStateAsUpdate(saved))
  let snapshot = before
  request.mockImplementation(async (url: string) => ({ ok: true, json: async () => url.includes("/comments") ? { comments: [], can_comment: false } : url.includes("/requests") ? { requests: [], can_dispatch: false, eligible_chats: [] } : { page_id:"page", schema: 2, rev: 1, epoch: "epoch", can_write: true, snapshot } }))
  render(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
  await screen.findByText("Alpha Beta")
  const editor = (screen.getByLabelText("Document editor") as HTMLElement & { editor: Editor }).editor
  await waitFor(() => expect(editor.isEditable).toBe(true))
  act(() => { editor.commands.setTextSelection(11); editor.commands.keyboardShortcut("Enter"); editor.commands.toggleHeading({ level: 2 }); editor.commands.insertContent("Acceptance heading"); editor.commands.keyboardShortcut("Enter"); editor.commands.toggleBulletList(); editor.commands.insertContent("Acceptance list item"); editor.commands.keyboardShortcut("Enter"); editor.commands.keyboardShortcut("Enter"); editor.commands.insertTable({ rows: 3, cols: 3, withHeaderRow: true }) })
  // Capture the CRDT directly from the configured collaboration document.
  const collaboration = editor.extensionManager.extensions.find(extension => extension.name === "collaboration")!
  expect(collaboration.options.document.getMap("__tiptap_position_migrations:root:default").size).toBeGreaterThan(0)
  snapshot = encodeUpdate(Y.encodeStateAsUpdate(collaboration.options.document))
  // Validate in the production Node module boundary: its Yjs constructors must
  // decode client bytes, rather than sharing browser objects across packages.
  const fixture = resolve(process.cwd(), "tools/docs-service/editor-patch-fixture.mjs")
  const projection = JSON.parse(execFileSync("node", [fixture], { input: JSON.stringify({ before, update: snapshot }), encoding: "utf8" })) as { content: { type: string }[] }
  expect(projection.content.some(node => node.type === "heading")).toBe(true)
  expect(projection.content.some(node => node.type === "bulletList")).toBe(true)
  expect(projection.content.some(node => node.type === "table")).toBe(true)
  const formatted = snapshot
  act(() => { expect(editor.commands.undo()).toBe(true) })
  const undone = encodeUpdate(Y.encodeStateAsUpdate(collaboration.options.document))
  const undoProjection = JSON.parse(execFileSync("node", [fixture], { input: JSON.stringify({ before: formatted, update: undone }), encoding: "utf8" })) as { content: { type: string }[] }
  expect(undoProjection.content.some(node => node.type === "table")).toBe(false)
  act(() => { expect(editor.commands.redo()).toBe(true) })
  const redone = encodeUpdate(Y.encodeStateAsUpdate(collaboration.options.document))
  const redoProjection = JSON.parse(execFileSync("node", [fixture], { input: JSON.stringify({ before: undone, update: redone }), encoding: "utf8" })) as { content: { type: string }[] }
  expect(redoProjection.content.some(node => node.type === "table")).toBe(true)
  saved.destroy()
})

function render(element:ReactNode) {
  const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})
  return baseRender(createElement(QueryClientProvider,{client},element))
}
