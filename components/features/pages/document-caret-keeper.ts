import { Extension } from "@tiptap/core"
import { Plugin, PluginKey, TextSelection } from "@tiptap/pm/state"
import { absolutePositionToRelativePosition, relativePositionToAbsolutePosition, ySyncPluginKey, type ProsemirrorBinding } from "@tiptap/y-tiptap"
import * as Y from "yjs"

/**
 * Keeps the local caret and range on the characters they were on when another
 * person's change arrives (#5162).
 *
 * The Yjs binding resolves the selection through Yjs relative positions, which
 * follow the characters, but then second-guesses that answer whenever the
 * caret's paragraph text changed (`isMisresolvedAfterStructuralChange`) and
 * falls back to the old offset inside the paragraph. A remote character that
 * lands before the caret in the same paragraph therefore moves the caret
 * left by one, and the next keystroke goes in front of the typist's own last
 * character: their text is scrambled although every client converges.
 *
 * This plugin captures the same relative positions before every Yjs
 * transaction and, for the editor transaction the binding dispatches for a
 * remote change, puts the selection back where those positions resolve. It
 * steps aside (the binding's recovery stays) when the text the caret was in no
 * longer exists, which is the structural case that recovery is for, and for
 * undo/redo, which restores its own stored selection.
 */
type Captured = { anchor: Y.RelativePosition; head: Y.RelativePosition }

const key = new PluginKey("documentCaretKeeper")

/** True while `type` and every ancestor up to the shared fragment exist (nothing on the path was deleted). */
function alive(type: Y.AbstractType<unknown>, root: Y.XmlFragment): boolean {
  let current: Y.AbstractType<unknown> | null = type
  while (current && current !== root) {
    const item: Y.Item | null = current._item
    if (!item || item.deleted) return false
    current = item.parent as Y.AbstractType<unknown> | null
  }
  return current === root
}

function resolve(binding: ProsemirrorBinding, position: Y.RelativePosition): number | null {
  const decoded = Y.createAbsolutePositionFromRelativePosition(position, binding.doc)
  if (!decoded || !(decoded.type instanceof Y.XmlText) || !alive(decoded.type as unknown as Y.AbstractType<unknown>, binding.type)) return null
  return relativePositionToAbsolutePosition(binding.doc, binding.type, position, binding.mapping)
}

export const DocumentCaretKeeper = Extension.create({
  name: "documentCaretKeeper",
  addProseMirrorPlugins() {
    let captured: Captured | null = null
    let binding: ProsemirrorBinding | undefined
    return [new Plugin({
      key,
      view: view => {
        binding = ySyncPluginKey.getState(view.state)?.binding as ProsemirrorBinding | undefined
        const doc = binding?.doc
        const before = () => {
          if (!binding || captured || binding.projectionError) return
          const selection = view.state.selection
          if (!(selection instanceof TextSelection)) return
          // During a local edit's own Yjs write the editor state is ahead of the mapping, so this capture may be off or throw;
          // it is never used: the binding dispatches no remote-change transaction inside its own write.
          try {
            captured = { anchor: absolutePositionToRelativePosition(selection.anchor, binding.type, binding.mapping), head: absolutePositionToRelativePosition(selection.head, binding.type, binding.mapping) }
          } catch { captured = null }
        }
        const after = () => { captured = null }
        doc?.on("beforeAllTransactions", before)
        doc?.on("afterAllTransactions", after)
        return { destroy: () => { doc?.off("beforeAllTransactions", before); doc?.off("afterAllTransactions", after); captured = null } }
      },
      appendTransaction: (transactions, _old, state) => {
        if (!binding || !captured || !(state.selection instanceof TextSelection)) return null
        const remote = transactions.some(tr => {
          const meta = tr.getMeta(ySyncPluginKey) as { isChangeOrigin?: boolean; isUndoRedoOperation?: boolean } | undefined
          return tr.docChanged && meta?.isChangeOrigin === true && !meta.isUndoRedoOperation
        })
        if (!remote) return null
        const anchor = resolve(binding, captured.anchor), head = resolve(binding, captured.head)
        if (anchor === null || head === null || anchor > state.doc.content.size || head > state.doc.content.size) return null
        if (anchor === state.selection.anchor && head === state.selection.head) return null
        const $anchor = state.doc.resolve(anchor), $head = state.doc.resolve(head)
        if (!$anchor.parent.isTextblock || !$head.parent.isTextblock) return null
        return state.tr.setSelection(TextSelection.between($anchor, $head)).setMeta("addToHistory", false)
      },
    })]
  },
})
