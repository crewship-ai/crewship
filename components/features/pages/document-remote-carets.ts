import { Extension, type Editor } from "@tiptap/core"
import { Plugin, PluginKey, type EditorState, type Transaction } from "@tiptap/pm/state"
import { Decoration, DecorationSet } from "@tiptap/pm/view"
import { relativePositionToAbsolutePosition, ySyncPluginKey, type ProsemirrorBinding } from "@tiptap/y-tiptap"
import type { PresencePeer } from "@/lib/documents/presence"
import { readablePeerFill } from "@/components/features/artifacts-design/peer-colors"

/**
 * Other people's carets and ranges in a Docs editor (#5166). Positions arrive
 * as Yjs relative positions; they are resolved when they arrive and whenever
 * a remote change rebuilds the document, and mapped through local edits in
 * between, so a caret stays on the character it was on while anybody types.
 *
 * The caret is a thin bar in the person's colour with their name above it;
 * the name is generated content (app/globals.css), never editor text.
 * It is decoration only (aria-hidden, not editable, not selectable): who is
 * here is announced by the presence list in the frame, which carries the
 * same names.
 */
type Placed = { peer: PresencePeer; anchor: number; head: number }
type CaretsState = { peers: PresencePeer[]; placed: Placed[]; decorations: DecorationSet }

export const remoteCaretsKey = new PluginKey<CaretsState>("documentRemoteCarets")

function place(state: EditorState, peers: PresencePeer[]): Placed[] {
  const binding = ySyncPluginKey.getState(state)?.binding as ProsemirrorBinding | undefined
  if (!binding || binding.projectionError) return []
  const size = state.doc.content.size
  const placed: Placed[] = []
  for (const peer of peers) {
    if (!peer.anchor || !peer.head) continue
    try {
      const anchor = relativePositionToAbsolutePosition(binding.doc, binding.type, peer.anchor, binding.mapping)
      const head = relativePositionToAbsolutePosition(binding.doc, binding.type, peer.head, binding.mapping)
      if (anchor === null || head === null || anchor < 0 || head < 0 || anchor > size || head > size) continue
      placed.push({ peer, anchor, head })
    } catch { /* a position the current document cannot place is simply not drawn */ }
  }
  return placed
}

function caretWidget(peer: PresencePeer): HTMLElement {
  const caret = document.createElement("span")
  caret.className = "document-peer-caret"
  caret.dataset.remoteCaret = peer.client
  caret.setAttribute("aria-hidden", "true")
  caret.contentEditable = "false"
  Object.assign(caret.style, { position: "relative", display: "inline-block", width: "0", height: "1.2em", marginLeft: "-1px", marginRight: "-1px", borderLeft: `2px solid ${peer.color}`, verticalAlign: "text-bottom", pointerEvents: "none", userSelect: "none" })
  // The name is the caret's generated ::after content (app/globals.css), not a child element: it is never part
  // of the editor's text (copy, find, innerText) and does not break lines.
  caret.dataset.name = peer.name
  caret.style.setProperty("--peer-fill", readablePeerFill(peer.color))
  return caret
}

function decorate(state: EditorState, placed: Placed[]): DecorationSet {
  const decorations: Decoration[] = []
  for (const { peer, anchor, head } of placed) {
    const from = Math.min(anchor, head), to = Math.max(anchor, head)
    if (to > from) decorations.push(Decoration.inline(from, to, { class: "document-peer-range", style: `background-color: ${peer.color}33`, "data-peer": peer.client }))
    decorations.push(Decoration.widget(head, () => caretWidget(peer), { key: `${peer.client}:${peer.name}:${peer.color}`, side: -1, ignoreSelection: true }))
  }
  return DecorationSet.create(state.doc, decorations)
}

function remoteChange(tr: Transaction): boolean {
  const meta = tr.getMeta(ySyncPluginKey) as { isChangeOrigin?: boolean } | undefined
  return tr.docChanged && meta?.isChangeOrigin === true
}

export const DocumentRemoteCarets = Extension.create({
  name: "documentRemoteCarets",
  addProseMirrorPlugins() {
    return [new Plugin<CaretsState>({
      key: remoteCaretsKey,
      state: {
        init: (_config, state) => ({ peers: [], placed: [], decorations: DecorationSet.empty.map(state.tr.mapping, state.doc) }),
        apply: (tr, value, _old, state) => {
          const peers = tr.getMeta(remoteCaretsKey) as PresencePeer[] | undefined
          if (peers) { const placed = place(state, peers); return { peers, placed, decorations: decorate(state, placed) } }
          if (!tr.docChanged || !value.peers.length) return value
          // A remote change rebuilt the blocks it touched: resolve again. A local edit only shifts positions.
          const placed = remoteChange(tr) ? place(state, value.peers) : value.placed.map(entry => ({ ...entry, anchor: tr.mapping.map(entry.anchor, -1), head: tr.mapping.map(entry.head, -1) }))
          return { peers: value.peers, placed, decorations: decorate(state, placed) }
        },
      },
      props: { decorations: state => remoteCaretsKey.getState(state)?.decorations },
    })]
  },
})

/** Shows these peers' carets in the editor (an empty list clears them). */
export function setRemoteCarets(editor: Editor, peers: PresencePeer[]) {
  if (editor.isDestroyed) return
  editor.view.dispatch(editor.state.tr.setMeta(remoteCaretsKey, peers).setMeta("addToHistory", false))
}
