// Schema 4 formatting and the Comments drawer in the Docs page, over the real document session and
// frame with a mocked API, as in docs-view.test.tsx.
// Presence heartbeats are covered by document-remote-carets and the presence API tests; these tests count document writes.
vi.mock("@/hooks/use-document-presence", () => { const none: never[] = []; return { useDocumentPresence: () => none } })
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
import type { Editor } from "@tiptap/core"
import { decodeUpdate, encodeUpdate } from "@/hooks/use-page-document"
import { documentSchema } from "@/lib/documents/schema"
import { ArtifactFrame, NO_FRAME_CAPABILITIES, type FrameLayout } from "../../../frame"
import type { LibraryItem } from "../../../library-items"
import { DocsBody, type DocsFrameReport } from "../docs-view"


const env = vi.hoisted(() => ({ canWrite: true, canComment: true, comments: "ok" as "ok" | "missing" | "conflict" }))
const request = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-documents-availability", () => ({ useDocumentsAvailability: () => ({ data: true, isPending: false }) }))
vi.mock("@/lib/document-queue", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/document-queue")>(), openDocumentDrafts: async () => ({ load: async () => [], put: async () => {}, remove: async () => {}, close: () => {}, exclusive: true }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request, broadcastSessionExpired: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "docs-human" } } }) }))

afterEach(() => { cleanup(); request.mockReset(); env.canWrite = true; env.canComment = true; env.comments = "ok" })

const item = {
  slug: "launch-notes", pageId: "page-docs", name: "Launch notes", kind: "word", family: "document", sharedVersion: null, native: false,
  projectBacked: false, empty: false, private: false, ownerLabel: "Research crew", folder: { slug: "lab", name: "Demo Lab" }, updatedAt: null,
} as unknown as LibraryItem

/** Markup that must stay text: tabs, trailing spaces and the final newline are part of what Copy returns. */
const MARKUP = "<script>window.__ran = true</script>\n\t<img src=x onerror=\"window.__ran = true\">  \n"
const p = (id: string, text: string, marks?: { type: string; attrs: object }[]) => ({ type: "paragraph", attrs: { id }, content: [{ type: "text", text, ...(marks ? { marks } : {}) }] })
const formatted = { type: "doc", content: [
  p("goal", "Ship the agency offer on 2 December."),
  p("bold", "Partners only", [{ type: "bold", attrs: {} }]),
  { type: "codeBlock", attrs: { id: "code", language: "python" }, content: [{ type: "text", text: "print(1)" }] },
  { type: "blockquote", attrs: { id: "quote" }, content: [p("quote-p", "Quoted text")] },
  { type: "taskList", attrs: { id: "tasks" }, content: [
    { type: "taskItem", attrs: { id: "done", checked: true }, content: [p("done-p", "Legal review")] },
    { type: "taskItem", attrs: { id: "open", checked: false }, content: [p("open-p", "Pricing page")] },
  ] },
  { type: "codeBlock", attrs: { id: "markup", language: "html" }, content: [{ type: "text", text: MARKUP }] },
] }

const comments = [
  { id: "t1", body: "Is 2 December confirmed with legal?", actor: "user:jana", rev: 1, version: 1, resolved: false, created_at: "2026-11-08T10:12:00Z", block: "goal", quote: "2 December", from: 25, to: 35, status: "attached" },
  { id: "r1", parent: "t1", body: "Not yet. Legal answers on Thursday.", actor: "user:docs-human", rev: 1, version: 1, resolved: false, created_at: "2026-11-08T10:20:00Z" },
  { id: "t2", body: "Done.", actor: "user:tomas", rev: 1, version: 2, resolved: true, created_at: "2026-11-07T09:58:00Z", block: "bold", quote: "Partners", from: 0, to: 8, status: "resolved" },
]

function serve() {
  // The fake server stores what it acknowledges, as the real one does: Saved means it holds the editor's content.
  const doc = documentSchema.create(structuredClone(formatted), 4) as Y.Doc
  let rev = 1
  request.mockImplementation(async (url: string, init?: RequestInit) => {
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    if (url.includes("/document/patch")) { Y.applyUpdate(doc, decodeUpdate(body.update)); rev += 1; return { ok: true, json: async () => ({ id: body.id, epoch: body.epoch, rev, schema: body.schema }) } }
    if (url.includes("/document/comments")) {
      if (env.comments === "missing") return { ok: false, status: 404, json: async () => ({ error: "not found" }) }
      if (body) {
        if (env.comments === "conflict") return { ok: false, status: 409, json: async () => ({ error: "comment revision changed" }) }
        return { ok: true, json: async () => ({ id: body.id, rev, epoch: body.epoch, actor: "user:docs-human", kind: url.includes("/resolve") ? "resolve" : "comment" }) }
      }
      return { ok: true, json: async () => ({ page_id: "page-docs", comments, can_comment: env.canComment, can_resolve: env.canComment }) }
    }
    if (url.includes("/members")) return { ok: true, json: async () => [{ id: "m1", role: "MEMBER", created_at: "", user: { id: "jana", email: "jana@example.invalid", full_name: "Jana Nováková" } }] }
    if (url.includes("/document/checkpoints")) return { ok: true, json: async () => ({ checkpoints: [] }) }
    if (url.includes("/proposals")) return { ok: true, json: async () => ({ proposals: [] }) }
    return { ok: true, json: async () => ({ page_id: "page-docs", schema: 4, rev, epoch: "go:node:1", can_write: env.canWrite, can_comment: env.canComment, snapshot: encodeUpdate(Y.encodeStateAsUpdate(doc)) }) }
  })
}

function open(layout: FrameLayout = "desktop") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  function Harness() {
    const [report, setReport] = React.useState<DocsFrameReport | null>(null)
    return <ArtifactFrame name={item.name} kind={{ kind: "docs", mode: "page" }} folder="Demo Lab" layout={layout} onHome={() => {}}
      caps={{ ...NO_FRAME_CAPABILITIES, canEdit: report?.canEdit === true, status: report?.status ?? null, agent: { ask: { chatHref: () => "/chat" } } }}>
      <DocsBody workspaceId="ws1" item={item} onFrame={setReport} fallback={<p>saved version</p>} />
    </ArtifactFrame>
  }
  return render(<QueryClientProvider client={client}><Harness /></QueryClientProvider>)
}

const slot = (name: string) => document.querySelector(`[data-artifact-slot="${name}"]`) as HTMLElement | null
async function editable() {
  const editor = (await screen.findByLabelText("Document editor")) as HTMLElement & { editor: Editor }
  await waitFor(() => expect(editor.editor.isEditable).toBe(true))
  return editor
}
const posts = (path: string) => request.mock.calls.filter(([url, init]) => String(url).includes(path) && init?.method === "POST").map(([, init]) => JSON.parse(String(init.body)))
/** Selects plain-text offsets inside the text block with this id. */
function select(editor: Editor, block: string, from: number, to: number) {
  let start = -1
  editor.state.doc.descendants((node, pos) => { if (node.isTextblock && node.attrs.id === block) start = pos + 1 })
  act(() => { editor.commands.setTextSelection({ from: start + from, to: start + to }) })
}

describe("Docs schema 4 formatting", () => {
  it("renders emphasis, the code language, the quote and the checklist; the editor offers the schema 4 tools", async () => {
    serve()
    open()
    const editor = await editable()
    expect(editor.querySelector("strong")!.textContent).toBe("Partners only")
    expect(editor.querySelector("pre")!.getAttribute("data-language")).toBe("python")
    expect(editor.querySelector("blockquote")!.textContent).toBe("Quoted text")
    const boxes = [...editor.querySelectorAll<HTMLInputElement>('li[data-type="taskItem"] input')]
    expect(boxes.map(box => [box.checked, box.disabled])).toEqual([[true, false], [false, false]])
    const tools = within(slot("tools")!)
    for (const name of ["Bold", "Italic", "Strikethrough", "Checklist", "Quote", "Inline code", "Code block", "Link"]) expect(tools.getByRole("button", { name })).toBeTruthy()
    // Bold on a selection is a real mark, saved like any edit.
    select(editor.editor, "goal", 0, 4)
    fireEvent.click(tools.getByRole("button", { name: "Bold" }))
    expect(editor.editor.getJSON().content![0].content![0]).toMatchObject({ type: "text", text: "Ship", marks: [{ type: "bold" }] })
    expect(documentSchema.validateJSON(editor.editor.getJSON(), 4)).toBeTruthy()
    // The code language shows for the code block the caret is in, and accepts only safe names.
    select(editor.editor, "code", 0, 1)
    fireEvent.click(await tools.findByRole("button", { name: "Code language" }))
    const language = await screen.findByRole("combobox", { name: "Code language" })
    fireEvent.change(language, { target: { value: "Py thon" } }); fireEvent.click(screen.getByRole("button", { name: "Apply language" }))
    expect(await screen.findByText("Use a short language name such as python, go or c++.")).toBeTruthy()
    fireEvent.change(language, { target: { value: "go" } }); fireEvent.click(screen.getByRole("button", { name: "Apply language" }))
    await waitFor(() => expect(editor.querySelector("pre")!.getAttribute("data-language")).toBe("go"))
  })

  it("toggles a checklist item with its box and saves the change", async () => {
    serve()
    open()
    const editor = await editable()
    const box = editor.querySelectorAll<HTMLInputElement>('li[data-type="taskItem"] input')[1]!
    box.checked = true
    fireEvent.change(box)
    await waitFor(() => expect(editor.querySelectorAll('li[data-type="taskItem"]')[1]!.getAttribute("data-checked")).toBe("true"))
    const items = (editor.editor.getJSON().content![4].content ?? []).map(entry => (entry as { attrs?: { checked?: boolean } }).attrs?.checked)
    expect(items).toEqual([true, true])
    expect(await screen.findByRole("button", { name: "Status: Saved" }, { timeout: 4000 })).toBeTruthy()
  })

  it("keeps the boxes read-only without write access", async () => {
    serve()
    env.canWrite = false
    open()
    expect(await screen.findByRole("button", { name: "Status: Read-only" })).toBeTruthy()
    const editor = screen.getByLabelText("Document editor")
    const box = editor.querySelector<HTMLInputElement>('li[data-type="taskItem"] input')!
    expect(box.disabled).toBe(true)
  })
})

describe("Docs code blocks", () => {
  const blockDom = (editor: HTMLElement, id: string) => editor.querySelector<HTMLElement>(`[data-code-block][data-block-id="${id}"]`)!

  it("colours code by its language with decorations that never change the document", async () => {
    serve()
    open()
    const editor = await editable()
    const before = structuredClone(editor.editor.getJSON())
    const python = blockDom(editor, "code")
    await waitFor(() => expect(python.querySelector("pre .hljs-number")?.textContent).toBe("1"))
    expect(python.querySelector("pre .hljs-built_in")?.textContent).toBe("print")
    expect(python.querySelector("[data-code-language]")!.textContent).toBe("python")
    // The colours are view state: the document, its text and its marks are exactly as loaded, and nothing is sent.
    expect(editor.editor.getJSON()).toEqual(before)
    expect(editor.editor.getJSON().content![2]).toEqual({ type: "codeBlock", attrs: { id: "code", language: "python" }, content: [{ type: "text", text: "print(1)" }] })
    expect(posts("/document/patch")).toHaveLength(0)
    // Markup is only tokenized: no element from the code text reaches the page, and nothing ran.
    const markup = blockDom(editor, "markup")
    await waitFor(() => expect(markup.querySelector("pre .hljs-name")).not.toBeNull())
    expect(markup.querySelector("pre")!.textContent).toBe(MARKUP)
    expect(editor.querySelector("script, img")).toBeNull()
    expect((window as unknown as { __ran?: boolean }).__ran).toBeUndefined()
  })

  it("re-colours after a language change and an edit, still without marks in the text", async () => {
    serve()
    open()
    const editor = await editable()
    act(() => { editor.editor.commands.command(({ tr }) => {
      let pos = -1
      tr.doc.descendants((node, at) => { if (node.attrs.id === "code") pos = at })
      tr.setNodeMarkup(pos, undefined, { id: "code", language: "go" }); tr.insertText("func main() {}\n", pos + 1)
      return true
    }) })
    const go = blockDom(editor, "code")
    await waitFor(() => expect(go.querySelector("pre .hljs-keyword")?.textContent).toBe("func"))
    expect(go.querySelector("[data-code-language]")!.textContent).toBe("go")
    expect(editor.editor.getJSON().content![2].content).toEqual([{ type: "text", text: "func main() {}\nprint(1)" }])
    expect(documentSchema.validateJSON(editor.editor.getJSON(), 4)).toBeTruthy()
  })

  it("copies the exact characters of a block, also for a reader", async () => {
    serve()
    env.canWrite = false
    const writeText = vi.fn(async (_text: string) => {})
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } })
    open()
    expect(await screen.findByRole("button", { name: "Status: Read-only" })).toBeTruthy()
    const editor = screen.getByLabelText("Document editor")
    const copy = within(blockDom(editor, "markup")).getByRole("button", { name: "Copy html code" })
    fireEvent.click(copy)
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(MARKUP))
    expect(await within(blockDom(editor, "markup")).findByText("Copied")).toBeTruthy()
    // The header is not document text: it is outside the code and outside the saved content.
    expect(blockDom(editor, "markup").querySelector("pre")!.textContent).not.toContain("Copy")
  })
})

describe("Docs comments", () => {
  it("shows threads with quotes, names and replies, resolved threads behind a toggle, and highlights anchors", async () => {
    serve()
    open()
    const editor = await editable()
    fireEvent.click(await screen.findByRole("button", { name: "Comments" }))
    const drawer = screen.getByRole("complementary", { name: "Comments" })
    const thread = await within(drawer).findByRole("article", { name: "Comment by Jana Nováková" })
    expect(within(thread).getByText("“2 December”")).toBeTruthy()
    expect(within(thread).getByText("Is 2 December confirmed with legal?")).toBeTruthy()
    expect(within(thread).getByText("You")).toBeTruthy()
    expect(within(thread).getByText("Not yet. Legal answers on Thursday.")).toBeTruthy()
    expect(within(drawer).getByText("1 resolved thread")).toBeTruthy()
    expect(within(drawer).queryByText("Done.")).toBeNull()
    fireEvent.click(within(drawer).getByRole("button", { name: "Show" }))
    expect(within(drawer).getByText("Done.")).toBeTruthy()
    await waitFor(() => expect(editor.querySelector('[data-comment-anchor="t1"]')?.textContent).toBe("2 December"))
    expect(editor.querySelector('[data-comment-anchor="t2"]')).toBeNull()
  })

  it("starts a thread on the selection, replies and resolves with the confirmed revision", async () => {
    serve()
    open()
    const editor = await editable()
    await screen.findByRole("button", { name: "Comments" })
    select(editor.editor, "quote-p", 0, 6)
    fireEvent.click(within(slot("tools")!).getByRole("button", { name: "Comment" }))
    const form = await screen.findByRole("form", { name: "New comment" })
    expect(within(form).getByText("“Quoted”")).toBeTruthy()
    fireEvent.change(within(form).getByRole("textbox", { name: "Comment" }), { target: { value: "Who said this?" } })
    fireEvent.click(within(form).getByRole("button", { name: "Comment" }))
    await waitFor(() => expect(posts("/document/comments?")).toHaveLength(1))
    expect(posts("/document/comments?")[0]).toEqual({ id: expect.any(String), schema: 4, page_id: "page-docs", epoch: "go:node:1", ifRev: 1, block: "quote-p", from: 0, to: 6, text: "Who said this?" })
    await waitFor(() => expect(screen.queryByRole("form", { name: "New comment" })).toBeNull())

    const drawer = screen.getByRole("complementary", { name: "Comments" })
    const thread = within(drawer).getByRole("article", { name: "Comment by Jana Nováková" })
    fireEvent.click(within(thread).getByRole("button", { name: "Reply" }))
    fireEvent.change(within(thread).getByRole("textbox", { name: "Reply" }), { target: { value: "Thursday it is." } })
    fireEvent.submit(within(thread).getByRole("form", { name: "Reply" }))
    await waitFor(() => expect(posts("/document/comments?")).toHaveLength(2))
    expect(posts("/document/comments?")[1]).toEqual({ id: expect.any(String), schema: 4, page_id: "page-docs", epoch: "go:node:1", ifRev: 1, parent: "t1", text: "Thursday it is." })
    fireEvent.click(within(thread).getByRole("button", { name: "Resolve" }))
    await waitFor(() => expect(posts("/document/comments/resolve")).toHaveLength(1))
    expect(posts("/document/comments/resolve")[0]).toEqual({ id: expect.any(String), schema: 4, page_id: "page-docs", epoch: "go:node:1", ifRev: 1, comment: "t1", version: 1, resolved: true })
  })

  it("keeps the text and says why when the page changed under the comment", async () => {
    serve()
    env.comments = "conflict"
    open()
    const editor = await editable()
    await screen.findByRole("button", { name: "Comments" })
    select(editor.editor, "goal", 0, 4)
    fireEvent.click(within(slot("tools")!).getByRole("button", { name: "Comment" }))
    const form = await screen.findByRole("form", { name: "New comment" })
    fireEvent.change(within(form).getByRole("textbox", { name: "Comment" }), { target: { value: "Keep me" } })
    fireEvent.click(within(form).getByRole("button", { name: "Comment" }))
    expect(await within(form).findByText("The page changed while you wrote. Your text is kept; send it again.")).toBeTruthy()
    expect((within(form).getByRole("textbox", { name: "Comment" }) as HTMLTextAreaElement).value).toBe("Keep me")
  })

  it("lets a reader read but not write, and has no Comments tab when the read is refused", async () => {
    serve()
    env.canWrite = false; env.canComment = false
    const first = open()
    fireEvent.click(await screen.findByRole("button", { name: "Comments" }))
    const drawer = screen.getByRole("complementary", { name: "Comments" })
    expect(await within(drawer).findByText("You can read comments. Commenting needs comment access.")).toBeTruthy()
    expect(within(drawer).queryByRole("button", { name: /Reply|Resolve|Comment on selection/ })).toBeNull()
    first.unmount()
    serve()
    env.comments = "missing"
    open()
    await screen.findByLabelText("Document editor")
    await waitFor(() => expect(request.mock.calls.some(([url]) => String(url).includes("/document/comments"))).toBe(true))
    expect(screen.queryByRole("button", { name: "Comments" })).toBeNull()
  })
})
