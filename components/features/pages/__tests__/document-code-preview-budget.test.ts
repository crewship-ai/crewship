import { afterEach, expect, it, vi } from "vitest"
import { Editor, Extension } from "@tiptap/core"
import StarterKit from "@tiptap/starter-kit"
import { documentSchema } from "@/lib/documents/schema"
import { codeBlockView, PREVIEW_BUDGET_MS, type PreviewResult } from "../document-code-blocks"

const render = vi.hoisted(() => vi.fn<(source: string) => PreviewResult | Promise<PreviewResult>>())
vi.mock("../code-preview-math", () => ({ renderMath: render }))
let editor: Editor | undefined
afterEach(() => { editor?.destroy(); editor = undefined; render.mockReset() })

it("renders an explicit Preview even when an earlier queued render spends the budget", async () => {
  let finishBusy!: (result: PreviewResult) => void
  const result = (text: string, ms = 0): PreviewResult => {
    const node = document.createElement("span")
    node.textContent = text
    return { ok: true, node, ms }
  }
  render.mockImplementation(source => source === "busy"
    ? new Promise(resolve => { finishBusy = resolve })
    : result(source))
  const base = documentSchema.extensionsFor(4).find(extension => extension.name === "codeBlock")!
  editor = new Editor({
    extensions: [StarterKit.configure({ codeBlock: false, trailingNode: false }), codeBlockView(base, { languages: true }),
      Extension.create({ name: "testBlockIds", addGlobalAttributes: () => [{ types: ["codeBlock"], attributes: { id: { default: null } } }] })],
    content: { type: "doc", content: [
      { type: "paragraph", content: [{ type: "text", text: "Outside" }] },
      ...["busy", "requested", "automatic"].map((text, i) => ({ type: "codeBlock", attrs: { id: text, language: i === 0 ? "math" : "python" }, content: [{ type: "text", text }] })),
    ] },
  })
  const ed = editor
  const block = (id: string) => ed.view.dom.querySelector<HTMLElement>(`[data-block-id="${id}"]`)!
  const position = (id: string) => {
    let found = -1
    ed.state.doc.descendants((node, at) => { if (node.attrs.id === id) found = at })
    expect(found).toBeGreaterThan(-1)
    return found
  }
  await vi.waitFor(() => expect(render).toHaveBeenCalledWith("busy"))
  ed.commands.setTextSelection(position("requested") + 1)
  ed.commands.updateAttributes("codeBlock", { language: "math" })
  expect(block("requested").querySelector("pre")!.hidden).toBe(false)
  block("requested").querySelector<HTMLButtonElement>("[data-code-mode] button:last-child")!.click()
  // An unrelated block also becomes previewable while the first render is held.
  const at = position("automatic")
  ed.view.dispatch(ed.state.tr.setNodeMarkup(at, undefined, { ...ed.state.doc.nodeAt(at)!.attrs, language: "math" }))
  await new Promise(resolve => setTimeout(resolve, 20))
  expect(render.mock.calls.map(([source]) => source)).toEqual(["busy"])
  finishBusy(result("busy", PREVIEW_BUDGET_MS + 1))
  await vi.waitFor(() => expect(block("requested").querySelector("[data-code-preview]")?.textContent).toBe("requested"))
  expect(block("requested").querySelector("pre")!.hidden).toBe(true)
  await vi.waitFor(() => expect(block("automatic").querySelector("[data-code-paused]")).not.toBeNull())
  expect(render.mock.calls.map(([source]) => source)).toEqual(["busy", "requested"])
  expect(block("automatic").querySelector("pre")!.hidden).toBe(false)
})
