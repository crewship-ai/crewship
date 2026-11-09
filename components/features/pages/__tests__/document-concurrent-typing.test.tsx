// #5162: two people type at the end of the same paragraph at once. Every
// remote update this editor receives must leave the typist's caret right after
// the character they typed last, so their own characters stay in the order
// they were typed, whatever the other person's edits do around them.
import { afterEach, describe, expect, it } from "vitest"
import * as Y from "yjs"
import { Editor } from "@tiptap/core"
import { documentSessionExtensions } from "../page-document"

const editors: Editor[] = []
afterEach(() => { for (const editor of editors.splice(0)) editor.destroy(); document.body.innerHTML = "" })

/** The saved document both people opened: two paragraphs with stable block ids, as the engine stores them. */
function saved(): Uint8Array {
  const doc = new Y.Doc({ gc: false })
  for (const [id, words] of [["alpha", "Alpha shared paragraph."], ["beta", "Beta second block."]] as const) {
    const paragraph = new Y.XmlElement("paragraph"); paragraph.setAttribute("id", id)
    const text = new Y.XmlText(); text.insert(0, words); paragraph.insert(0, [text])
    doc.getXmlFragment("default").push([paragraph])
  }
  return Y.encodeStateAsUpdate(doc)
}

/** A client id fixed per test, so the CRDT's order of concurrent inserts is the same on every run. */
function client(id: number): Y.Doc { const doc = new Y.Doc({ gc: false }); doc.clientID = id; return doc }

function mount(doc: Y.Doc, schema = 2): Editor {
  const element = document.createElement("div"); document.body.append(element)
  const editor = new Editor({ element, extensions: documentSessionExtensions(doc, schema) })
  editors.push(editor)
  return editor
}

const blockText = (doc: Y.Doc, index: number) => {
  const block = doc.getXmlFragment("default").get(index) as Y.XmlElement
  return block.toArray().map(part => (part instanceof Y.XmlText ? part.toString() : "")).join("").replace(/<[^>]+>/g, "")
}

/** The other person's editor, seen as Yjs: it appends at the end of `block` what it believes the end is. */
function typeRemote(doc: Y.Doc, block: number, char: string): Uint8Array {
  const before = Y.encodeStateVector(doc)
  const text = (doc.getXmlFragment("default").get(block) as Y.XmlElement).get(0) as Y.XmlText
  text.insert(text.length, char)
  return Y.encodeStateAsUpdate(doc, before)
}

/**
 * The local person types `mine` at the end of paragraph `localBlock`; the other
 * person types `theirs` at the end of paragraph `remoteBlock`. The other side
 * sees this editor's work only every `lag` keystrokes (the 500 ms poll), so its
 * inserts are genuinely concurrent with the local ones. Remote updates arrive
 * exactly as the poll applies them: Y.applyUpdate with origin "remote".
 */
function race({ localId, remoteId, localBlock, remoteBlock, mine, theirs, lag }: { localId: number; remoteId: number; localBlock: number; remoteBlock: number; mine: string; theirs: string; lag: number }) {
  const base = saved()
  const local = client(localId), remote = client(remoteId)
  Y.applyUpdate(local, base, "remote"); Y.applyUpdate(remote, base)
  const editor = mount(local)
  // The caret at the end of the local paragraph, as a click + End leaves it.
  const end = (() => { let pos = 0; editor.state.doc.forEach((node, offset, index) => { if (index === localBlock) pos = offset + node.nodeSize - 1 }); return pos })()
  editor.commands.setTextSelection(end)
  for (let i = 0; i < Math.max(mine.length, theirs.length); i++) {
    if (i < mine.length) editor.view.dispatch(editor.state.tr.insertText(mine[i]))
    if (i < theirs.length) Y.applyUpdate(local, typeRemote(remote, remoteBlock, theirs[i]), "remote")
    if (i % lag === lag - 1) Y.applyUpdate(remote, Y.encodeStateAsUpdate(local, Y.encodeStateVector(remote)))
  }
  // Both converge on one text once everything is exchanged.
  Y.applyUpdate(remote, Y.encodeStateAsUpdate(local, Y.encodeStateVector(remote)))
  Y.applyUpdate(local, Y.encodeStateAsUpdate(remote, Y.encodeStateVector(local)), "remote")
  return { editor, local, remote }
}

describe("#5162 concurrent typing in one paragraph", () => {
  // Both orders of the CRDT's tie-break: the remote character lands before or after the local run.
  for (const [localId, remoteId] of [[1, 2], [2, 1]] as const) {
    for (const lag of [2, 3, 5]) {
      it(`keeps the typist's characters in order (client ids ${localId}/${remoteId}, remote lags ${lag} keys)`, () => {
        const { editor, local, remote } = race({ localId, remoteId, localBlock: 0, remoteBlock: 0, mine: " [editor-same]", theirs: " [owner-same]", lag })
        const shown = editor.state.doc.child(0).textContent
        expect(shown).toBe(blockText(local, 0))
        expect(blockText(local, 0)).toBe(blockText(remote, 0))
        // The typist's own run is contiguous and in typing order: the caret never fell behind their last character.
        expect(shown).toContain("[editor-same]")
      })
    }
  }

  it("keeps the typist's caret after a remote edit earlier in the same paragraph", () => {
    const base = saved()
    const local = client(1), remote = client(2)
    Y.applyUpdate(local, base, "remote"); Y.applyUpdate(remote, base)
    const editor = mount(local)
    editor.commands.setTextSelection(1 + "Alpha shared paragraph.".length)
    for (const char of " mine") {
      editor.view.dispatch(editor.state.tr.insertText(char))
      Y.applyUpdate(remote, Y.encodeStateAsUpdate(local, Y.encodeStateVector(remote)))
      const before = Y.encodeStateVector(remote)
      const text = (remote.getXmlFragment("default").get(0) as Y.XmlElement).get(0) as Y.XmlText
      text.insert(0, "x")
      Y.applyUpdate(local, Y.encodeStateAsUpdate(remote, before), "remote")
    }
    expect(editor.state.doc.child(0).textContent).toBe("xxxxxAlpha shared paragraph. mine")
    expect(editor.state.selection.from).toBe(1 + "xxxxxAlpha shared paragraph. mine".length)
  })

  it("typing in another block stays in order while the other person types in the first one", () => {
    const { editor } = race({ localId: 1, remoteId: 2, localBlock: 0, remoteBlock: 1, mine: " [editor-other]", theirs: " [owner-other]", lag: 3 })
    expect(editor.state.doc.child(0).textContent).toBe("Alpha shared paragraph. [editor-other]")
    expect(editor.state.doc.child(1).textContent).toBe("Beta second block. [owner-other]")
  })
})
