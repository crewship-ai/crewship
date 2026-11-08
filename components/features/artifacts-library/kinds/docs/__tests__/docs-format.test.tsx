// Schema 5 formatting in the Docs toolbar (D01-D05): underline, palette colour and highlight, H3, clear
// formatting, mixed states, the kept selection, save -> reload, and undo next to another writer's change.
// Same real document session and frame with a mocked API as docs-rich.test.tsx; this server merges the
// saved Yjs updates, so a reopened page shows exactly what was confirmed.
vi.mock("@/lib/protected-document-drafts", () => ({ openProtectedDocumentDrafts: async (...args: Parameters<typeof import("@/lib/document-queue").openDocumentDrafts>) => (await import("@/lib/document-queue")).openDocumentDrafts(...args) }))
vi.mock("@/lib/local-draft-seal", () => ({
  createLocalDraftCipher: async () => ({ seal: async (text: string) => text, open: async (text: string) => text, close() {} }),
  isSealedLocalDraft: () => false, LocalDraftProtectionError: class extends Error {}, LocalDraftReadRevoked: class extends Error {},
}))
import * as React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import * as Y from "yjs"
import type { Editor, JSONContent } from "@tiptap/core"
import { decodeUpdate, encodeUpdate } from "@/hooks/use-page-document"
import { documentSchema } from "@/lib/documents/schema"
import { ArtifactFrame, NO_FRAME_CAPABILITIES } from "../../../frame"
import type { LibraryItem } from "../../../library-items"
import { DocsBody, type DocsFrameReport } from "../docs-view"

const request = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-documents-availability", () => ({ useDocumentsAvailability: () => ({ data: true, isPending: false }) }))
vi.mock("@/lib/document-queue", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/document-queue")>(), openDocumentDrafts: async () => ({ load: async () => [], put: async () => {}, remove: async () => {}, close: () => {}, exclusive: true }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request, broadcastSessionExpired: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "docs-human" } } }) }))

afterEach(() => { cleanup(); request.mockReset() })

const item = {
  slug: "launch-notes", pageId: "page-docs", name: "Launch notes", kind: "word", family: "document", sharedVersion: null, native: false,
  projectBacked: false, empty: false, private: false, ownerLabel: "Research crew", folder: { slug: "lab", name: "Demo Lab" }, updatedAt: null,
} as unknown as LibraryItem

type Mark = { type: string; attrs: object }
const t = (text: string, ...marks: Mark[]) => ({ type: "text", text, ...(marks.length ? { marks } : {}) })
const p = (id: string, ...content: object[]) => ({ type: "paragraph", attrs: { id }, content })
const link: Mark = { type: "link", attrs: { href: "https://example.invalid/" } }
const initial = { type: "doc", content: [
  p("one", t("Plain and "), t("under", { type: "underline", attrs: {} }), t(" text")),
  p("two", t("Linked", link, { type: "bold", attrs: {} }), t(" second "), t("line", { type: "italic", attrs: {} })),
  p("three", t("Third")),
  { type: "taskList", attrs: { id: "tasks" }, content: [
    { type: "taskItem", attrs: { id: "task-a", checked: false }, content: [p("task-a-p", t("Draft the offer"))] },
    { type: "taskItem", attrs: { id: "task-b", checked: false }, content: [p("task-b-p", t("Legal review"))] },
  ] },
] }

/** A documents API that merges every saved update into its own Y.Doc, as the engine does. */
function serve(schema = 5) {
  const server = documentSchema.create(structuredClone(initial), schema) as Y.Doc
  let rev = 1
  request.mockImplementation(async (url: string, init?: RequestInit) => {
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    if (url.includes("/document/patch")) { Y.applyUpdate(server, decodeUpdate(body.update)); documentSchema.validate(server, schema); rev += 1; return { ok: true, json: async () => ({ id: body.id, epoch: body.epoch, rev, schema: body.schema }) } }
    if (url.includes("/document/comments")) return { ok: true, json: async () => ({ page_id: "page-docs", comments: [], can_comment: false, can_resolve: false }) }
    if (url.includes("/members")) return { ok: true, json: async () => [] }
    if (url.includes("/document/checkpoints")) return { ok: true, json: async () => ({ checkpoints: [] }) }
    if (url.includes("/proposals")) return { ok: true, json: async () => ({ proposals: [] }) }
    return { ok: true, json: async () => ({ page_id: "page-docs", schema, rev, epoch: "go:node:1", can_write: true, can_comment: false, snapshot: encodeUpdate(Y.encodeStateAsUpdate(server)) }) }
  })
  return {
    content: () => documentSchema.validate(server, schema) as JSONContent,
    /** Another person's confirmed edit, made on their own Yjs client. */
    otherWriter(edit: (doc: Y.Doc) => void) {
      const other = new Y.Doc({ gc: false }); Y.applyUpdate(other, Y.encodeStateAsUpdate(server))
      const before = Y.encodeStateVector(other); edit(other)
      Y.applyUpdate(server, Y.encodeStateAsUpdate(other, before)); documentSchema.validate(server, schema); rev += 1; other.destroy()
    },
  }
}

function open() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  function Harness() {
    const [report, setReport] = React.useState<DocsFrameReport | null>(null)
    return <ArtifactFrame name={item.name} kind={{ kind: "docs", mode: "page" }} folder="Demo Lab" layout="desktop" onHome={() => {}}
      caps={{ ...NO_FRAME_CAPABILITIES, canEdit: report?.canEdit === true, status: report?.status ?? null, agent: { ask: { chatHref: () => "/chat" } } }}>
      <DocsBody workspaceId="ws1" item={item} onFrame={setReport} fallback={<p>saved version</p>} />
    </ArtifactFrame>
  }
  return render(<QueryClientProvider client={client}><Harness /></QueryClientProvider>)
}

const tools = () => within(document.querySelector('[data-artifact-slot="tools"]') as HTMLElement)
async function editable() {
  const element = (await screen.findByLabelText("Document editor")) as HTMLElement & { editor: Editor }
  await waitFor(() => expect(element.editor.isEditable).toBe(true))
  return element
}
function position(editor: Editor, block: string, offset: number) {
  let start = -1
  editor.state.doc.descendants((node, pos) => { if (node.isTextblock && node.attrs.id === block) start = pos + 1 })
  return start + offset
}
function select(editor: Editor, from: [string, number], to: [string, number]) {
  act(() => { editor.commands.setTextSelection({ from: position(editor, ...from), to: position(editor, ...to) }) })
}
const marksOf = (json: JSONContent, block: number) => (json.content![block].content ?? []).map(node => [node.text, (node.marks ?? []).map(mark => mark.attrs?.color ? `${mark.type}:${mark.attrs.color}` : mark.type)])
const saved = () => screen.findByRole("button", { name: "Status: Saved" }, { timeout: 4000 })

describe("Docs schema 5 toolbar", () => {
  it("offers H3, underline, colour, highlight and clear formatting, and shows mixed and active states", async () => {
    serve()
    open()
    const { editor } = await editable()
    for (const name of ["Paragraph", "Heading 1", "Heading 2", "Heading 3", "Bold", "Italic", "Underline", "Strikethrough", "Text colour", "Highlight", "Clear formatting", "Undo", "Redo"]) expect(tools().getByRole("button", { name })).toBeTruthy()
    expect(tools().getByRole("button", { name: "Underline" }).getAttribute("aria-keyshortcuts")).toMatch(/^(Control|Meta)\+U$/)
    // Part of the selection is underlined: the toggle says so, and one click underlines all of it.
    select(editor, ["one", 6], ["one", 15])
    expect(tools().getByRole("button", { name: "Underline" }).getAttribute("aria-pressed")).toBe("mixed")
    fireEvent.click(tools().getByRole("button", { name: "Underline" }))
    expect(tools().getByRole("button", { name: "Underline" }).getAttribute("aria-pressed")).toBe("true")
    expect(marksOf(editor.getJSON(), 0)).toEqual([["Plain ", []], ["and under", ["underline"]], [" text", []]])
    fireEvent.click(tools().getByRole("button", { name: "Underline" }))
    expect(marksOf(editor.getJSON(), 0)).toEqual([["Plain and under text", []]])
    // A selection across paragraphs reports bold as mixed.
    select(editor, ["one", 0], ["two", 6])
    expect(tools().getByRole("button", { name: "Bold" }).getAttribute("aria-pressed")).toBe("mixed")
  })

  it("keeps the selection while the colour menu is open, applies a palette colour and resets it to default", async () => {
    serve()
    open()
    const element = await editable(), editor = element.editor
    select(editor, ["two", 7], ["three", 3])
    const { from, to } = editor.state.selection
    fireEvent.click(tools().getByRole("button", { name: "Text colour" }))
    const menu = await screen.findByRole("group", { name: "Text colour" })
    expect(editor.state.selection.from).toBe(from); expect(editor.state.selection.to).toBe(to)
    act(() => { editor.view.dom.dispatchEvent(new FocusEvent("blur")) })
    await waitFor(() => expect(element.querySelector(".document-selection-kept")).toBeTruthy())
    expect(within(menu).getAllByRole("button").map(button => button.getAttribute("aria-label"))).toEqual(["Default", "Gray", "Red", "Orange", "Yellow", "Green", "Blue", "Purple", "Pink"])
    fireEvent.click(within(menu).getByRole("button", { name: "Red" }))
    expect(marksOf(editor.getJSON(), 1).slice(1)).toEqual([[" ", []], ["second ", ["textColor:red"]], ["line", ["italic", "textColor:red"]]])
    expect(marksOf(editor.getJSON(), 2)).toEqual([["Thi", ["textColor:red"]], ["rd", []]])
    expect(editor.state.selection.from).toBe(from); expect(editor.state.selection.to).toBe(to)
    await waitFor(() => expect(element.querySelector('[data-text-color="red"]')).toBeTruthy())
    expect(tools().getByRole("button", { name: "Text colour" }).getAttribute("title")).toBe("Text colour: Red")
    fireEvent.click(tools().getByRole("button", { name: "Highlight" }))
    fireEvent.click(within(await screen.findByRole("group", { name: "Highlight" })).getByRole("button", { name: "Yellow" }))
    await waitFor(() => expect(element.querySelector('mark[data-highlight="yellow"]')).toBeTruthy())
    fireEvent.click(tools().getByRole("button", { name: "Text colour" }))
    fireEvent.click(within(await screen.findByRole("group", { name: "Text colour" })).getByRole("button", { name: "Default" }))
    await waitFor(() => expect(element.querySelector("[data-text-color]")).toBeNull())
    expect(documentSchema.validateJSON(editor.getJSON(), 5)).toBeTruthy()
  })

  it("clears formatting but keeps links, and changes several paragraphs to H3 without losing inline marks", async () => {
    serve()
    open()
    const { editor } = await editable()
    select(editor, ["one", 0], ["two", 18])
    fireEvent.click(tools().getByRole("button", { name: "Heading 3" }))
    expect(editor.getJSON().content!.slice(0, 2).map(node => [node.type, node.attrs?.level])).toEqual([["heading", 3], ["heading", 3]])
    expect(marksOf(editor.getJSON(), 1)).toEqual([["Linked", ["link", "bold"]], [" second ", []], ["line", ["italic"]]])
    fireEvent.click(tools().getByRole("button", { name: "Clear formatting" }))
    expect(marksOf(editor.getJSON(), 0)).toEqual([["Plain and under text", []]])
    expect(marksOf(editor.getJSON(), 1)).toEqual([["Linked", ["link"]], [" second line", []]])
    fireEvent.click(tools().getByRole("button", { name: "Undo" }))
    expect(marksOf(editor.getJSON(), 1)).toEqual([["Linked", ["link", "bold"]], [" second ", []], ["line", ["italic"]]])
  })

  it("saves the formatting, and a reopened page shows exactly what the server confirmed", async () => {
    const api = serve()
    const first = open()
    const { editor } = await editable()
    select(editor, ["three", 0], ["three", 5])
    fireEvent.keyDown(editor.view.dom, { key: "u", code: "KeyU", ctrlKey: true })
    fireEvent.click(tools().getByRole("button", { name: "Highlight" }))
    fireEvent.click(within(await screen.findByRole("group", { name: "Highlight" })).getByRole("button", { name: "Green" }))
    expect(marksOf(editor.getJSON(), 2)).toEqual([["Third", ["underline", "highlight:green"]]])
    await saved()
    expect(marksOf(api.content(), 2)).toEqual([["Third", ["underline", "highlight:green"]]])
    first.unmount()
    open()
    const reopened = await editable()
    expect(marksOf(reopened.editor.getJSON(), 2)).toEqual([["Third", ["underline", "highlight:green"]]])
    expect(reopened.querySelector('mark[data-highlight="green"] u, u mark[data-highlight="green"]')?.textContent).toBe("Third")
  })

  it("undo takes back only this person's formatting, never another writer's confirmed text", async () => {
    const api = serve()
    open()
    const { editor } = await editable()
    select(editor, ["three", 0], ["three", 5])
    fireEvent.click(tools().getByRole("button", { name: "Bold" }))
    await saved()
    // Another writer adds text to the same paragraph on their own client; it reaches this editor by the poll.
    api.otherWriter(doc => { const text = documentSchema.find(doc, "three").toArray()[0] as Y.XmlText; text.insert(5, " from Jana") })
    await waitFor(() => expect(editor.getText()).toContain("Third from Jana"), { timeout: 4000 })
    fireEvent.click(tools().getByRole("button", { name: "Undo" }))
    expect(marksOf(editor.getJSON(), 2)).toEqual([["Third from Jana", []]])
    await saved()
    expect(marksOf(api.content(), 2)).toEqual([["Third from Jana", []]])
  })

  it("nests, splits, ticks and undoes checklist items next to another writer, and the list survives reload (D09)", async () => {
    const api = serve()
    const first = open()
    const element = await editable(), editor = element.editor
    // Tab nests the second item under the first; Enter at its end starts a new sibling item.
    act(() => { editor.commands.setTextSelection(position(editor, "task-b-p", 3)) })
    fireEvent.keyDown(editor.view.dom, { key: "Tab", code: "Tab" })
    const nested = () => editor.getJSON().content![3].content![0].content!
    expect(nested().map(node => node.type)).toEqual(["paragraph", "taskList"])
    act(() => { editor.commands.setTextSelection(position(editor, "task-b-p", 12)) })
    fireEvent.keyDown(editor.view.dom, { key: "Enter", code: "Enter" })
    act(() => { editor.commands.insertContent("Sign") })
    expect(nested()[1].content!.map(item => item.content![0].content?.[0]?.text)).toEqual(["Legal review", "Sign"])
    // Ticking the box is its own undoable change (Yjs groups edits less than 500 ms apart); undo leaves the typed item alone.
    await saved(); await act(() => new Promise(resolve => setTimeout(resolve, 600)))
    const boxes = () => [...element.querySelectorAll<HTMLInputElement>('li[data-type="taskItem"] input')]
    boxes()[0]!.checked = true; fireEvent.change(boxes()[0]!)
    await waitFor(() => expect(editor.getJSON().content![3].content![0].attrs!.checked).toBe(true))
    await saved()
    // Another writer edits the first item's text on their own client meanwhile.
    api.otherWriter(doc => { (documentSchema.find(doc, "task-a-p").toArray()[0] as Y.XmlText).insert(15, " today") })
    await waitFor(() => expect(editor.getText()).toContain("Draft the offer today"), { timeout: 4000 })
    fireEvent.click(tools().getByRole("button", { name: "Undo" }))
    expect(editor.getJSON().content![3].content![0].attrs!.checked).toBe(false)
    expect(editor.getText()).toContain("Draft the offer today")
    expect(nested()[1].content!.map(item => item.content![0].content?.[0]?.text)).toEqual(["Legal review", "Sign"])
    await saved()
    first.unmount()
    open()
    const reopened = (await editable()).editor
    const list = reopened.getJSON().content![3]
    expect(list.content![0].attrs!.checked).toBe(false)
    expect(list.content![0].content![0].content![0].text).toBe("Draft the offer today")
    expect(list.content![0].content![1].content!.map(item => item.content![0].content?.[0]?.text)).toEqual(["Legal review", "Sign"])
    expect(documentSchema.validateJSON(api.content(), 5)).toBeTruthy()
  })
})
