import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
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

// Ctrl+End (or any caret move the browser makes) changes the DOM selection first and tells the editor
// in a later selectionchange event. An Enter that arrives before that event must still split where the
// caret is shown, not where the editor last saw it.
it("Enter splits at the caret the browser shows, even before selectionchange reaches the editor", async () => {
  const saved = new Y.Doc({ gc: false })
  for (const [id, words] of [["first", "Alpha paragraph"], ["last", "Omega paragraph"]] as const) {
    const paragraph = new Y.XmlElement("paragraph"); paragraph.setAttribute("id", id)
    const text = new Y.XmlText(); text.insert(0, words); paragraph.insert(0, [text])
    saved.getXmlFragment("default").push([paragraph])
  }
  const snapshot = encodeUpdate(Y.encodeStateAsUpdate(saved))
  request.mockImplementation(async (url: string) => ({ ok: true, json: async () => url.includes("/comments") ? { comments: [], can_comment: false } : url.includes("/requests") ? { requests: [], can_dispatch: false, eligible_chats: [] } : { page_id:"page", schema: 2, rev: 1, epoch: "epoch", can_write: true, snapshot } }))
  render(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
  await screen.findByText("Omega paragraph")
  const dom = screen.getByLabelText("Document editor") as HTMLElement & { editor: Editor }
  const editor = dom.editor
  await waitFor(() => expect(editor.isEditable).toBe(true))
  // The editor last saw the caret inside the first paragraph, where the user clicked.
  act(() => { dom.focus(); editor.commands.setTextSelection(4) })
  // The browser has already moved the caret to the very end; its selectionchange has not been delivered yet
  // (jsdom would deliver it at once, so it is held back here).
  const undelivered = (event: Event) => event.stopImmediatePropagation()
  window.addEventListener("selectionchange", undelivered, true)
  const lastText = dom.querySelectorAll("p")[1].firstChild!
  document.getSelection()!.collapse(lastText, lastText.textContent!.length)
  expect(editor.state.selection.from).toBe(4)
  act(() => { dom.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 13, bubbles: true, cancelable: true })) })
  window.removeEventListener("selectionchange", undelivered, true)
  act(() => { editor.commands.insertContent("Typed after Enter") })
  expect(editor.getJSON().content?.map(block => block.content?.map(part => ("text" in part ? part.text : "")).join("") ?? "")).toEqual(["Alpha paragraph", "Omega paragraph", "Typed after Enter"])
  saved.destroy()
})

function render(element:ReactNode) {
  const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})
  return baseRender(createElement(QueryClientProvider,{client},element))
}
