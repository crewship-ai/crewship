// Schema 4 formatting and the Comments drawer in the Docs page, over the real document session and
// frame with a mocked API, as in docs-view.test.tsx.
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
import { encodeUpdate } from "@/hooks/use-page-document"
import { documentSchema } from "@/lib/documents/schema"
import { ArtifactFrame, NO_FRAME_CAPABILITIES, type FrameLayout } from "../../../frame"
import type { LibraryItem } from "../../../library-items"
import { DocsBody, type DocsFrameReport } from "../docs-view"


const env = vi.hoisted(() => ({ canWrite: true, canComment: true, comments: "ok" as "ok" | "missing" | "conflict" }))
const request = vi.hoisted(() => vi.fn())
// happy-dom cannot lay out SVG, so mermaid's renderer is replaced by one that returns hostile output; the
// real renderer is exercised in a browser harness. What reaches the page must still be safe.
const mermaid = vi.hoisted(() => ({ initialize: vi.fn(), render: vi.fn(async (id: string, text: string, container: HTMLElement) => {
  // A diagram mentioning "Slow" takes longer than the slow-render threshold.
  if (text.includes("Slow")) await new Promise(done => setTimeout(done, 600))
  // Like mermaid's sandbox mode: draw in a frame inside the given container and return that frame as markup.
  // insertBefore: happy-dom gives a frame no document until it loads one (the lock on attach is covered in the browser probe).
  const frame = container.ownerDocument.createElement("iframe"); frame.id = `i${id}`; container.insertBefore(frame, null)
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" id="${id}"><g onclick="window.__ran = true"><a href="javascript:window.__ran = true"><text>Start</text></a></g><foreignObject><img src="x" onerror="window.__ran = true"/></foreignObject><script>window.__ran = true</script></svg>`
  const bytes = new TextEncoder().encode(`<body style="margin:0">${svg}</body>`)
  return { svg: `<iframe src="data:text/html;charset=UTF-8;base64,${btoa(Array.from(bytes, byte => String.fromCodePoint(byte)).join(""))}" sandbox=""></iframe>` }
}) }))
vi.mock("mermaid", () => ({ default: mermaid }))
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
const CSV = "name,role\n\"Doe, Jane\",<b>lead</b>\n"
const MARKDOWN = "## Plan\n\n- [x] **ship**\n- read [docs](https://example.com/docs)\n\n<img src=x onerror=\"window.__ran = true\">\n\n![chart](https://example.test/chart.png)"
const FLOW = "flowchart LR\n  A[Start] --> B[Done]\n  click A \"javascript:window.__ran = true\""
const SALES = JSON.stringify({ type: "bar", title: "Sales", x: ["Q1", "Q2"], series: [{ name: "<b>North</b>", values: [10, 20] }] })
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
  { type: "codeBlock", attrs: { id: "euler", language: "latex" }, content: [{ type: "text", text: String.raw`e^{i\pi} + 1 = 0` }] },
  { type: "codeBlock", attrs: { id: "broken", language: "math" }, content: [{ type: "text", text: String.raw`\frac{1}{` }] },
  { type: "codeBlock", attrs: { id: "people", language: "csv" }, content: [{ type: "text", text: CSV }] },
  { type: "codeBlock", attrs: { id: "ragged", language: "csv" }, content: [{ type: "text", text: "a,b\n1,2,3" }] },
  { type: "codeBlock", attrs: { id: "notes", language: "markdown" }, content: [{ type: "text", text: MARKDOWN }] },
  { type: "codeBlock", attrs: { id: "plain-md", language: "python" }, content: [{ type: "text", text: "# not a heading" }] },
  { type: "codeBlock", attrs: { id: "flow", language: "mermaid" }, content: [{ type: "text", text: FLOW }] },
  { type: "codeBlock", attrs: { id: "slow-flow", language: "mermaid" }, content: [{ type: "text", text: "flowchart LR\n  Slow --> B" }] },
  { type: "codeBlock", attrs: { id: "sales", language: "chart" }, content: [{ type: "text", text: SALES }] },
  { type: "codeBlock", attrs: { id: "bad-chart", language: "chart" }, content: [{ type: "text", text: "{\"type\":\"pie\",\"x\":[],\"series\":[]}" }] },
] }

const comments = [
  { id: "t1", body: "Is 2 December confirmed with legal?", actor: "user:jana", rev: 1, version: 1, resolved: false, created_at: "2026-11-08T10:12:00Z", block: "goal", quote: "2 December", from: 25, to: 35, status: "attached" },
  { id: "r1", parent: "t1", body: "Not yet. Legal answers on Thursday.", actor: "user:docs-human", rev: 1, version: 1, resolved: false, created_at: "2026-11-08T10:20:00Z" },
  { id: "t2", body: "Done.", actor: "user:tomas", rev: 1, version: 2, resolved: true, created_at: "2026-11-07T09:58:00Z", block: "bold", quote: "Partners", from: 0, to: 8, status: "resolved" },
]

function serve() {
  const doc = documentSchema.create(structuredClone(formatted), 4) as Y.Doc
  const snapshot = encodeUpdate(Y.encodeStateAsUpdate(doc)); doc.destroy()
  let rev = 1
  request.mockImplementation(async (url: string, init?: RequestInit) => {
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    if (url.includes("/document/patch")) { rev += 1; return { ok: true, json: async () => ({ id: body.id, epoch: body.epoch, rev, schema: body.schema }) } }
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
    return { ok: true, json: async () => ({ page_id: "page-docs", schema: 4, rev, epoch: "go:node:1", can_write: env.canWrite, can_comment: env.canComment, snapshot }) }
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

describe("Docs math blocks", () => {
  const blockDom = (editor: HTMLElement, id: string) => editor.querySelector<HTMLElement>(`[data-code-block][data-block-id="${id}"]`)!

  it("typesets a latex block, switches to its source and back, and never changes the document", async () => {
    serve()
    open()
    const editor = await editable()
    const before = structuredClone(editor.editor.getJSON())
    const euler = blockDom(editor, "euler")
    await waitFor(() => expect(euler.querySelector("[data-code-preview] .katex math")).not.toBeNull())
    expect(euler.querySelector("[data-code-preview] annotation")!.textContent).toBe(String.raw`e^{i\pi} + 1 = 0`)
    expect(euler.querySelector("pre")!.hidden).toBe(true)
    const group = within(euler).getByRole("group", { name: "Show formula" })
    expect(within(group).getByRole("button", { name: "Preview" }).getAttribute("aria-pressed")).toBe("true")
    fireEvent.click(within(group).getByRole("button", { name: "Source" }))
    expect(euler.querySelector("pre")!.hidden).toBe(false)
    expect(euler.querySelector<HTMLElement>("[data-code-preview]")!.hidden).toBe(true)
    expect(euler.querySelector("pre")!.textContent).toBe(String.raw`e^{i\pi} + 1 = 0`)
    fireEvent.click(within(group).getByRole("button", { name: "Preview" }))
    await waitFor(() => expect(euler.querySelector("[data-code-preview] .katex")).not.toBeNull())
    // Source/Preview is this viewer's view: nothing was written or sent.
    expect(editor.editor.getJSON()).toEqual(before)
    expect(posts("/document/patch")).toHaveLength(0)
  })

  it("explains a syntax error inside that block and keeps its source and the rest of the page", async () => {
    serve()
    open()
    const editor = await editable()
    const broken = blockDom(editor, "broken")
    await waitFor(() => expect(broken.querySelector("[data-code-preview][data-error]")?.textContent).toMatch(/^Cannot show this formula: /))
    expect(broken.querySelector("pre")!.hidden).toBe(false)
    expect(broken.querySelector("pre")!.textContent).toBe(String.raw`\frac{1}{`)
    await waitFor(() => expect(blockDom(editor, "euler").querySelector(".katex")).not.toBeNull())
    expect(editor.querySelector("strong")!.textContent).toBe("Partners only")
  })

  it("shows the source when the caret enters a previewed block, so typing is never hidden, and re-renders the edit", async () => {
    serve()
    open()
    const editor = await editable()
    const euler = blockDom(editor, "euler")
    await waitFor(() => expect(euler.querySelector("[data-code-preview] .katex")).not.toBeNull())
    let start = -1
    editor.editor.state.doc.descendants((node, pos) => { if (node.attrs.id === "euler") start = pos + 1 })
    act(() => { editor.editor.commands.setTextSelection(start) })
    expect(euler.querySelector("pre")!.hidden).toBe(false)
    act(() => { editor.editor.commands.insertContent("x + ") })
    expect(editor.editor.getJSON().content!.find(block => block.attrs?.id === "euler")!.content).toEqual([{ type: "text", text: String.raw`x + e^{i\pi} + 1 = 0` }])
    fireEvent.click(within(euler).getByRole("button", { name: "Preview" }))
    await waitFor(() => expect(euler.querySelector("[data-code-preview] annotation")?.textContent).toBe(String.raw`x + e^{i\pi} + 1 = 0`))
  })

  it("keeps the source shown when the block under the caret becomes latex, so typing is never hidden", async () => {
    serve()
    open()
    const editor = await editable()
    let start = -1
    editor.editor.state.doc.descendants((node, pos) => { if (node.attrs.id === "code") start = pos + 1 })
    act(() => { editor.editor.commands.setTextSelection(start) })
    act(() => { editor.editor.commands.updateAttributes("codeBlock", { language: "latex" }) })
    const block = blockDom(editor, "code")
    await new Promise(resolve => setTimeout(resolve, 400))
    expect(block.querySelector("pre")!.hidden).toBe(false)
    expect(within(block).getByRole("button", { name: "Source" }).getAttribute("aria-pressed")).toBe("true")
    // Choosing Preview moves the caret out of the block first.
    fireEvent.click(within(block).getByRole("button", { name: "Preview" }))
    await waitFor(() => expect(block.querySelector("[data-code-preview] .katex")).not.toBeNull())
    expect(block.querySelector("pre")!.hidden).toBe(true)
    const { from } = editor.editor.state.selection
    expect(from < start - 1 || from > start - 1 + editor.editor.state.doc.nodeAt(start - 1)!.nodeSize).toBe(true)
  })

  it("previews the last block of the page with the caret in it by moving the caret before it", async () => {
    serve()
    open()
    const editor = await editable()
    act(() => { editor.editor.commands.insertContentAt(editor.editor.state.doc.content.size, { type: "codeBlock", attrs: { id: "last", language: "latex" }, content: [{ type: "text", text: "a^2" }] }) })
    let start = -1
    editor.editor.state.doc.descendants((node, pos) => { if (node.attrs.id === "last") start = pos })
    expect(start + editor.editor.state.doc.nodeAt(start)!.nodeSize).toBe(editor.editor.state.doc.content.size)
    act(() => { editor.editor.commands.setTextSelection(start + 2) })
    const block = blockDom(editor, "last")
    expect(block.querySelector("pre")!.hidden).toBe(false)
    fireEvent.click(within(block).getByRole("button", { name: "Preview" }))
    await waitFor(() => expect(block.querySelector("[data-code-preview] .katex")).not.toBeNull())
    expect(block.querySelector("pre")!.hidden).toBe(true)
    expect(editor.editor.state.selection.from).toBeLessThan(start)
  })

  it("previews math for a reader too, without a caret to follow", async () => {
    serve()
    env.canWrite = false
    open()
    expect(await screen.findByRole("button", { name: "Status: Read-only" })).toBeTruthy()
    const editor = screen.getByLabelText("Document editor")
    await waitFor(() => expect(blockDom(editor, "euler").querySelector("[data-code-preview] .katex")).not.toBeNull())
  })
})

describe("Docs CSV and Markdown blocks", () => {
  const blockDom = (editor: HTMLElement, id: string) => editor.querySelector<HTMLElement>(`[data-code-block][data-block-id="${id}"]`)!

  it("shows a csv block as a table of text cells, and its source on request", async () => {
    serve()
    open()
    const editor = await editable()
    const before = structuredClone(editor.editor.getJSON())
    const people = blockDom(editor, "people")
    await waitFor(() => expect(people.querySelector("[data-code-preview] table")).not.toBeNull())
    const table = people.querySelector("[data-code-preview] table")!
    expect([...table.querySelectorAll("th")].map(th => th.textContent)).toEqual(["name", "role"])
    expect([...table.querySelectorAll("td")].map(td => td.textContent)).toEqual(["Doe, Jane", "<b>lead</b>"])
    expect(table.querySelector("b")).toBeNull()
    fireEvent.click(within(within(people).getByRole("group", { name: "Show table" })).getByRole("button", { name: "Source" }))
    expect(people.querySelector("pre")!.hidden).toBe(false)
    expect(people.querySelector("pre")!.textContent).toBe(CSV)
    expect(editor.editor.getJSON()).toEqual(before)
    expect(posts("/document/patch")).toHaveLength(0)
  })

  it("explains a malformed csv block in that block only", async () => {
    serve()
    open()
    const editor = await editable()
    const ragged = blockDom(editor, "ragged")
    await waitFor(() => expect(ragged.querySelector("[data-code-preview][data-error]")?.textContent).toBe("Cannot show this CSV: this row has 3 cells, the header has 2 (line 2)."))
    expect(ragged.querySelector("pre")!.hidden).toBe(false)
  })

  it("renders a markdown block without its raw HTML or images, and leaves other code alone", async () => {
    serve()
    open()
    const editor = await editable()
    const notes = blockDom(editor, "notes")
    await waitFor(() => expect(notes.querySelector("[data-code-preview] h2")?.textContent).toBe("Plan"))
    const preview = notes.querySelector("[data-code-preview]")!
    expect(preview.querySelector("strong")!.textContent).toBe("ship")
    expect(preview.querySelector("a")!.getAttribute("href")).toBe("https://example.com/docs")
    expect(preview.querySelector("img, script, [onerror]")).toBeNull()
    expect(preview.textContent).toContain("<img src=x onerror=")
    expect(preview.textContent).toContain("[Image: chart]")
    expect((window as unknown as { __ran?: boolean }).__ran).toBeUndefined()
    // Only blocks that opt in by language are rendered.
    const plain = blockDom(editor, "plain-md")
    expect(plain.querySelector<HTMLElement>("[data-code-preview]")!.hidden).toBe(true)
    expect(within(plain).queryByRole("button", { name: "Preview" })).toBeNull()
    expect(plain.querySelector("pre")!.textContent).toBe("# not a heading")
  })
})

describe("Docs diagram and chart blocks", () => {
  const blockDom = (editor: HTMLElement, id: string) => editor.querySelector<HTMLElement>(`[data-code-block][data-block-id="${id}"]`)!

  it("draws a mermaid block from its sanitized SVG and shows its source on request", async () => {
    serve()
    open()
    const editor = await editable()
    const before = structuredClone(editor.editor.getJSON())
    const flow = blockDom(editor, "flow")
    await waitFor(() => expect(flow.querySelector<HTMLElement>("[data-diagram-preview]")?.shadowRoot?.querySelector("svg")).toBeTruthy())
    const root = flow.querySelector<HTMLElement>("[data-diagram-preview]")!.shadowRoot!
    expect(mermaid.render).toHaveBeenCalledWith(expect.stringMatching(/^docs-diagram-/), FLOW, expect.anything())
    expect(root.querySelector("svg")!.getAttribute("role")).toBe("img")
    expect(root.querySelector("text")!.textContent).toBe("Start")
    expect(root.querySelector("a, script, foreignObject, img, [onclick], [href]")).toBeNull()
    expect((window as unknown as { __ran?: boolean }).__ran).toBeUndefined()
    expect(flow.querySelector("pre")!.hidden).toBe(true)
    fireEvent.click(within(within(flow).getByRole("group", { name: "Show diagram" })).getByRole("button", { name: "Source" }))
    expect(flow.querySelector("pre")!.hidden).toBe(false)
    expect(flow.querySelector("pre")!.textContent).toBe(FLOW)
    expect(editor.editor.getJSON()).toEqual(before)
    expect(posts("/document/patch")).toHaveLength(0)
  })

  it("stops redrawing a slow diagram on remote edits until the reader chooses Preview", async () => {
    serve()
    open()
    const editor = await editable()
    const slow = blockDom(editor, "slow-flow")
    await waitFor(() => expect(slow.querySelector<HTMLElement>("[data-diagram-preview]")?.shadowRoot?.querySelector("svg")).toBeTruthy(), { timeout: 3000 })
    const drawn = () => mermaid.render.mock.calls.filter(([, text]) => String(text).includes("Slow")).map(([, text]) => text)
    const before = drawn().length
    // An edit that does not move this reader's caret, as a collaborator's would.
    act(() => { editor.editor.commands.command(({ tr }) => {
      let pos = -1
      tr.doc.descendants((node, at) => { if (node.attrs.id === "slow-flow") pos = at })
      tr.insertText("\n  B --> C", pos + 1 + "flowchart LR\n  Slow --> B".length)
      return true
    }) })
    await waitFor(() => expect(slow.querySelector("[data-code-stale]")?.textContent).toBe("The source changed. Choose Preview to draw it again."))
    await new Promise(done => setTimeout(done, 400))
    expect(drawn()).toHaveLength(before)
    expect(slow.querySelector<HTMLElement>("[data-diagram-preview]")?.shadowRoot?.querySelector("svg")).toBeTruthy()
    fireEvent.click(within(within(slow).getByRole("group", { name: "Show diagram" })).getByRole("button", { name: "Preview" }))
    await waitFor(() => expect(drawn()).toContain("flowchart LR\n  Slow --> B\n  B --> C"))
  })

  it("lets further blocks wait for the reader once the page's preview time is spent", async () => {
    serve()
    open()
    const editor = await editable()
    const fixture = blockDom(editor, "slow-flow")
    await waitFor(() => expect(fixture.querySelector<HTMLElement>("[data-diagram-preview]")?.shadowRoot?.querySelector("svg")).toBeTruthy(), { timeout: 3000 })
    // Two more slow diagrams arrive (as from a collaborator); the first spends the rest of the budget.
    act(() => { editor.editor.commands.command(({ tr }) => {
      const block = (id: string, text: string) => editor.editor.schema.nodes.codeBlock.create({ id, language: "mermaid" }, editor.editor.schema.text(text))
      tr.insert(tr.doc.content.size, [block("slow-2", "flowchart LR\n  Slow --> Two"), block("slow-3", "flowchart LR\n  Slow --> Three")])
      return true
    }) })
    const third = () => blockDom(editor, "slow-3")
    await waitFor(() => expect(third().querySelector("[data-code-paused]")?.textContent).toBe("Previews on this page took long to draw. Choose Preview to draw this one."), { timeout: 4000 })
    expect(blockDom(editor, "slow-2").querySelector<HTMLElement>("[data-diagram-preview]")?.shadowRoot?.querySelector("svg")).toBeTruthy()
    expect(third().querySelector("pre")!.hidden).toBe(false)
    expect(mermaid.render.mock.calls.some(([, text]) => String(text).includes("Three"))).toBe(false)
    fireEvent.click(within(within(third()).getByRole("group", { name: "Show diagram" })).getByRole("button", { name: "Preview" }))
    await waitFor(() => expect(third().querySelector<HTMLElement>("[data-diagram-preview]")?.shadowRoot?.querySelector("svg")).toBeTruthy(), { timeout: 3000 })
  }, 15_000)

  it("draws a chart block with its data table, and explains a bad spec in that block only", async () => {
    serve()
    open()
    const editor = await editable()
    const before = structuredClone(editor.editor.getJSON())
    const sales = blockDom(editor, "sales")
    await waitFor(() => expect(sales.querySelector("[data-code-preview] svg[role=img]")).not.toBeNull())
    const preview = sales.querySelector("[data-code-preview]")!
    expect(preview.querySelector("svg")!.getAttribute("aria-label")).toBe("Bar chart: Sales. 1 series (<b>North</b>) over 2 categories (Q1 to Q2); values from 10 to 20.")
    expect(preview.querySelectorAll("rect[data-chart-value]")).toHaveLength(2)
    expect(preview.querySelector("b")).toBeNull()
    expect(within(preview as HTMLElement).getByText("Show data table")).toBeTruthy()
    expect([...preview.querySelectorAll("tbody td")].map(td => td.textContent)).toEqual(["10", "20"])
    const bad = blockDom(editor, "bad-chart")
    await waitFor(() => expect(bad.querySelector("[data-code-preview][data-error]")?.textContent).toBe('Cannot show this chart: "type" must be "bar" or "line".'))
    expect(bad.querySelector("pre")!.hidden).toBe(false)
    fireEvent.click(within(within(sales).getByRole("group", { name: "Show chart" })).getByRole("button", { name: "Source" }))
    expect(sales.querySelector("pre")!.textContent).toBe(SALES)
    expect(editor.editor.getJSON()).toEqual(before)
    expect(posts("/document/patch")).toHaveLength(0)
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
