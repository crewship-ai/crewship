// #5166: another person's caret and range in a Docs editor, drawn from the
// Yjs relative positions their presence heartbeat carries. The caret stays on
// the same character while either side edits, and carries their name.
import { afterEach, describe, expect, it } from "vitest"
import * as Y from "yjs"
import { Editor } from "@tiptap/core"
import { documentSessionExtensions } from "../page-document"
import { setRemoteCarets } from "../document-remote-carets"
import { decodePosition, encodePosition, parsePresenceReply, peerColor, presentPeople, type PresencePeer } from "@/lib/documents/presence"

const editors: Editor[] = []
afterEach(() => { for (const editor of editors.splice(0)) editor.destroy(); document.body.innerHTML = "" })

function documents() {
  const saved = new Y.Doc({ gc: false })
  const paragraph = new Y.XmlElement("paragraph"); paragraph.setAttribute("id", "alpha")
  const text = new Y.XmlText(); text.insert(0, "Caret paragraph with words."); paragraph.insert(0, [text])
  saved.getXmlFragment("default").push([paragraph])
  const local = new Y.Doc({ gc: false }), remote = new Y.Doc({ gc: false })
  Y.applyUpdate(local, Y.encodeStateAsUpdate(saved), "remote"); Y.applyUpdate(remote, Y.encodeStateAsUpdate(saved))
  const element = document.createElement("div"); document.body.append(element)
  const editor = new Editor({ element, extensions: documentSessionExtensions(local, 2) })
  editors.push(editor)
  const remoteText = () => (remote.getXmlFragment("default").get(0) as Y.XmlElement).get(0) as Y.XmlText
  return { editor, local, remote, remoteText }
}

/** What the other person's editor sends: positions inside their own copy of the text, as relative positions. */
function peerAt(text: Y.XmlText, anchor: number, head: number, name = "Erin Editor"): PresencePeer {
  const wire = { client: "tab-erin-0001", user_id: "erin", name, color: 1, anchor: encodePosition(Y.createRelativePositionFromTypeIndex(text, anchor, -1)), head: encodePosition(Y.createRelativePositionFromTypeIndex(text, head, -1)) }
  return parsePresenceReply({ peers: [wire] }).peers[0]
}

const caretOffset = (editor: Editor) => {
  const caret = editor.view.dom.querySelector("[data-remote-caret]")
  if (!caret) return -1
  return editor.view.posAtDOM(caret, 0) - 1
}

describe("remote carets", () => {
  it("draws one named caret where the other person is, and their range", () => {
    const { editor, remoteText } = documents()
    setRemoteCarets(editor, [peerAt(remoteText(), 6, 15)])
    const carets = editor.view.dom.querySelectorAll('[class*="caret"], [class*="cursor"], [data-remote-caret]')
    expect(carets).toHaveLength(1)
    expect(carets[0].textContent).toBe("Erin Editor")
    expect(carets[0].getAttribute("aria-hidden")).toBe("true")
    expect(editor.view.dom.querySelector(".document-peer-range")?.textContent).toBe("paragraph")
    expect(caretOffset(editor)).toBe(15)
    // The caret is decoration: the document itself is unchanged.
    expect(editor.state.doc.textContent).toBe("Caret paragraph with words.")
  })

  it("stays on the same character through local and remote edits before it", () => {
    const { editor, local, remote, remoteText } = documents()
    setRemoteCarets(editor, [peerAt(remoteText(), 6, 6)])
    expect(caretOffset(editor)).toBe(6)
    // A local edit at the start of the paragraph.
    editor.view.dispatch(editor.state.tr.insertText("Local ", 1))
    expect(editor.state.doc.textContent).toBe("Local Caret paragraph with words.")
    expect(caretOffset(editor)).toBe(12)
    // A remote edit at the start, applied the way the poll applies it.
    Y.applyUpdate(remote, Y.encodeStateAsUpdate(local, Y.encodeStateVector(remote)))
    const before = Y.encodeStateVector(remote)
    remoteText().insert(0, "Remote ")
    Y.applyUpdate(local, Y.encodeStateAsUpdate(remote, before), "remote")
    expect(editor.state.doc.textContent.indexOf("paragraph")).toBe(caretOffset(editor))
  })

  it("clears every caret on an empty list", () => {
    const { editor, remoteText } = documents()
    setRemoteCarets(editor, [peerAt(remoteText(), 3, 3)])
    setRemoteCarets(editor, [])
    expect(editor.view.dom.querySelector("[data-remote-caret]")).toBeNull()
  })
})

describe("presence wire", () => {
  it("drops malformed peers and never takes a colour value from the wire", () => {
    const reply = parsePresenceReply({ peers: [
      { client: "tab-1", user_id: "u1", name: "  Erin  ", color: 2 },
      { client: "tab-2", user_id: "u2", name: "", color: 1 },
      { client: 3, user_id: "u3", name: "Bad", color: 1 },
      { client: "tab-4", user_id: "u4", name: "Paint", color: "red; background:url(x)" },
      { client: "tab-5", user_id: "u5", name: "Half", color: 1, anchor: encodePosition(Y.createRelativePositionFromTypeIndex(new Y.Doc().getText("t"), 0)) },
    ], interval_ms: 1 })
    expect(reply.peers.map(peer => peer.name)).toEqual(["Erin", "Paint", "Half"])
    expect(reply.peers[0].color).toBe(peerColor(2))
    expect(reply.peers[1].color).toBe(peerColor(0))
    expect(reply.peers[2].anchor).toBeUndefined()
    expect(reply.intervalMs).toBe(500)
    expect(decodePosition("%%%")).toBeUndefined()
  })

  it("lists a person with two tabs once", () => {
    const peers = parsePresenceReply({ peers: [{ client: "a", user_id: "u1", name: "Erin", color: 0 }, { client: "b", user_id: "u1", name: "Erin", color: 0 }] }).peers
    expect(presentPeople(peers)).toHaveLength(1)
  })
})
