import { act, cleanup, render } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { Editor, Extension } from "@tiptap/core"
import StarterKit from "@tiptap/starter-kit"
import * as Y from "yjs"
import { documentSurface } from "@/lib/documents/surface"
import { DocsCanvas, type MentionLabels } from "../docs-canvas"
import { sessionPeople } from "../docs-view"

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
  documentSurface.place(doc, "intro", { x: 0, y: 0, w: 320, h: 120, z: 1 })
  return doc
}
/** A stand-in for the Page's reader-scoped lookup: names arrive later, and only Ada may be named. */
function lookup() {
  const listeners = new Set<() => void>()
  let answered = false
  const people: MentionLabels = {
    label: vi.fn((id: string) => answered && id === "ada" ? "Ada Lovelace" : "Unknown member"),
    subscribe: vi.fn((listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener) } }),
  }
  return { people, answer: () => { answered = true; listeners.forEach(listener => listener()) }, listeners }
}
const labels = (root: ParentNode) => [...root.querySelectorAll("[data-mention]")].map(node => [node.getAttribute("data-mention"), node.textContent])

describe("Canvas mentions use the Page's people lookup", () => {
  it("takes the lookup the Page editor carries and re-renders the cards when names arrive", () => {
    const { people, answer, listeners } = lookup()
    const editor = new Editor({ extensions: [StarterKit, Extension.create({ name: "documentMentions", addStorage: () => ({ people }) })] })
    expect(sessionPeople(editor)).toBe(people)
    const { container, unmount } = render(<DocsCanvas doc={mentionDoc()} editable origin="me" people={sessionPeople(editor)} />)
    const card = container.querySelector('[data-placement="intro"]')!
    expect(labels(card)).toEqual([["ada", "@Unknown member"], ["cid", "@Unknown member"]])
    act(() => answer())
    expect(labels(card)).toEqual([["ada", "@Ada Lovelace"], ["cid", "@Unknown member"]])
    expect(card.getAttribute("aria-label")).toBe("Ask @Ada Lovelace and @Unknown member")
    expect(vi.mocked(people.label).mock.calls.map(call => call[0])).toEqual(expect.arrayContaining(["ada", "cid"]))
    unmount()
    expect(listeners.size).toBe(0)
    editor.destroy()
  })

  it("names nobody without the Page's lookup: an older schema or an editor without mention support", () => {
    const plain = new Editor({ extensions: [StarterKit] })
    expect(sessionPeople(plain)).toBeUndefined()
    expect(sessionPeople(null)).toBeUndefined()
    const { container } = render(<DocsCanvas doc={mentionDoc()} editable origin="me" people={sessionPeople(plain)} />)
    expect(labels(container)).toEqual([["ada", "@Unknown member"], ["cid", "@Unknown member"]])
    plain.destroy()
  })
})
