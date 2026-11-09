import { act, cleanup, render } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { Editor } from "@tiptap/core"
import StarterKit from "@tiptap/starter-kit"
import Collaboration from "@tiptap/extension-collaboration"
import * as Y from "yjs"
import { documentSurface } from "@/lib/documents/surface"
import { createMentionNames } from "@/lib/documents/mention-names"
import { documentEditorExtensions } from "@/components/features/pages/document-rich-extensions"
import { DocsCanvas } from "../docs-canvas"

afterEach(cleanup)

/** One schema-6 paragraph that mentions a co-reader and someone the reader may not see. */
function mentionDoc() {
  const doc = new Y.Doc({ gc: false })
  const paragraph = new Y.XmlElement("paragraph"); paragraph.setAttribute("id", "intro")
  const lead = new Y.XmlText(); lead.insert(0, "Ask ")
  const ada = new Y.XmlElement("mention"); ada.setAttribute("userId", "ada")
  const and = new Y.XmlText(); and.insert(0, " and ")
  const hidden = new Y.XmlElement("mention"); hidden.setAttribute("userId", "cid")
  paragraph.insert(0, [lead, ada, and, hidden])
  doc.getXmlFragment("default").insert(0, [paragraph])
  return doc
}
const labels = (root: ParentNode) => [...root.querySelectorAll("[data-mention]")].map(node => [node.getAttribute("data-mention"), node.textContent])

describe("mentions on the Page and the Canvas", () => {
  it("resolve through one reader-scoped lookup: a name only for people the server names, otherwise @Unknown member", async () => {
    const doc = mentionDoc()
    documentSurface.place(doc, "intro", { x: 0, y: 0, w: 320, h: 120, z: 1 })
    // The server names only Ada; Cid is a workspace member who cannot read this Document.
    const fetcher = vi.fn(async (url: string) => {
      const ids = new URL(url, "http://x").searchParams.get("ids")!.split(",")
      return Response.json({ people: ids.filter(id => id === "ada").map(id => ({ id, name: "Ada Lovelace" })) })
    })
    const names = createMentionNames("ws", "page-1", fetcher)
    const page = document.createElement("div"); document.body.append(page)
    const editor = new Editor({ element: page, extensions: [StarterKit.configure({ undoRedo: false, bold: false, italic: false, strike: false, code: false, codeBlock: false, blockquote: false, horizontalRule: false, hardBreak: false, link: false, underline: false, trailingNode: false }),
      ...documentEditorExtensions(6, { mentions: names }), Collaboration.configure({ document: doc, field: "default" })] })
    const { container } = render(<DocsCanvas doc={doc} editable origin="me" mentions={names} />)
    const canvasCard = container.querySelector('[data-placement="intro"]')!
    // Before the answer both surfaces show the neutral label, never the stored id.
    expect(labels(page)).toEqual([["ada", "@Unknown member"], ["cid", "@Unknown member"]])
    expect(labels(canvasCard)).toEqual(labels(page))
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(labels(page)).toEqual([["ada", "@Ada Lovelace"], ["cid", "@Unknown member"]])
    expect(labels(canvasCard)).toEqual(labels(page))
    // One request for both surfaces, never the workspace member list.
    expect(fetcher.mock.calls.map(call => call[0])).toEqual(["/api/v1/pages/%40page-1/document/people?ids=ada%2Ccid&workspace_id=ws"])
    // The name is display only: the stored content and its HTML keep just the id.
    expect(JSON.stringify(editor.getJSON())).not.toContain("Ada Lovelace")
    expect(editor.getHTML()).not.toContain("Ada Lovelace")
    editor.destroy(); page.remove()
  })
})
