import { Extension, type AnyExtension, type Editor } from "@tiptap/core"
import { Plugin, PluginKey } from "@tiptap/pm/state"
import { Decoration, DecorationSet } from "@tiptap/pm/view"
import type { Node as ProseMirrorNode } from "@tiptap/pm/model"
import { documentSchema } from "@/lib/documents/schema"
import { CodeHighlight, codeBlockView } from "./document-code-blocks"
import { formatDocumentDate, NO_DOCUMENT_PEOPLE, type DocumentPeople } from "@/lib/documents/people"

/**
 * The browser side of the closed document schemas. Every node and mark comes
 * from the shared engine definitions (`extensionsFor`), so the editor can
 * never produce content the engine refuses; this only adds editing behaviour
 * the engine has no use for: a clickable checklist box, keyboard shortcuts,
 * code block chrome and syntax colours, and comment highlights.
 */

const BASE_BLOCKS = ["paragraph", "heading", "bulletList", "orderedList", "listItem", "table", "tableRow", "tableCell", "tableHeader"]
const RICH_NAMES: Record<number, string[]> = {
  3: ["link", "code", "codeBlock"],
  4: ["link", "bold", "italic", "strike", "code", "codeBlock", "blockquote", "taskList", "taskItem"],
  // Schema 5 also has the inline date and mention atoms; an editor without them would drop them.
  5: ["link", "bold", "italic", "strike", "underline", "textColor", "highlight", "code", "codeBlock", "blockquote", "taskList", "taskItem", "date", "mention"],
}
// Schema 6 adds the Canvas surface beside the Page; its Page is exactly schema 5's.
RICH_NAMES[6] = RICH_NAMES[5]

/** Block types that carry a stable `id` in this schema. */
export function stableBlockTypes(schema: number | undefined): string[] {
  return [...BASE_BLOCKS, ...(isRichSchema(schema) ? ["codeBlock"] : []), ...(isFormattedSchema(schema) ? ["blockquote", "taskList", "taskItem"] : [])]
}

/** True where the schema has bold, italic, strike, quotes, checklists and code languages. */
export const isFormattedSchema = (schema: number | undefined) => schema === 4 || schema === 5 || schema === 6
/** True where the schema has links, inline code and code blocks. */
export const isRichSchema = (schema: number | undefined) => schema === 3 || schema === 4 || schema === 5 || schema === 6
/** True where the schema has underline and palette text colour and highlight. */
export const isStyledSchema = (schema: number | undefined) => schema === 5 || schema === 6

/** The schema's own marks and nodes beyond the StarterKit base, with editing behaviour. */
export function documentEditorExtensions(schema: number | undefined, people: DocumentPeople = NO_DOCUMENT_PEOPLE): AnyExtension[] {
  if (!isRichSchema(schema)) return []
  const names = RICH_NAMES[schema as number]
  const own = (documentSchema.extensionsFor(schema) as AnyExtension[]).filter(extension => names.includes(extension.name))
    .map(extension => extension.name === "taskItem" ? interactiveTaskItem(extension)
      : extension.name === "codeBlock" ? codeBlockView(extension, { languages: isFormattedSchema(schema) })
      : extension.name === "date" ? dateChip(extension) : extension.name === "mention" ? mentionChip(extension, people) : extension)
  return isStyledSchema(schema) ? [...own, FormatShortcuts, CodeHighlight, StyleShortcuts, DocumentMentions.configure({ people })] : isFormattedSchema(schema) ? [...own, FormatShortcuts, CodeHighlight] : own
}

type Extendable = AnyExtension & { extend: (config: object) => AnyExtension }
type ChipView = { node: ProseMirrorNode }

/** A date shows as one calendar day in the reader's locale; the stored value stays YYYY-MM-DD. */
function dateChip(base: AnyExtension): AnyExtension {
  return (base as Extendable).extend({
    addNodeView() {
      return ({ node }: ChipView) => {
        const dom = document.createElement("time")
        dom.className = "document-chip"
        dom.contentEditable = "false"
        const render = (next: ProseMirrorNode) => {
          const value = String(next.attrs.value ?? "")
          dom.dataset.date = value; dom.dateTime = value; dom.textContent = formatDocumentDate(value)
        }
        render(node)
        return { dom, update: (next: ProseMirrorNode) => { if (next.type !== node.type) return false; render(next); return true } }
      }
    },
  })
}

/**
 * A mention shows the name this reader may see, or "Unknown member". The id
 * stays in the content; the name only ever lives in this view.
 */
function mentionChip(base: AnyExtension, people: DocumentPeople): AnyExtension {
  return (base as Extendable).extend({
    addNodeView() {
      return ({ node }: ChipView) => {
        const dom = document.createElement("span")
        dom.className = "document-chip"
        dom.contentEditable = "false"
        let current = node
        const render = () => {
          const id = String(current.attrs.userId ?? "")
          dom.dataset.mention = id; dom.textContent = "@" + people.label(id)
        }
        render()
        const unsubscribe = people.subscribe(render)
        return {
          dom,
          update: (next: ProseMirrorNode) => { if (next.type !== current.type) return false; current = next; render(); return true },
          destroy: unsubscribe,
        }
      }
    },
  })
}

const mentionKey = new PluginKey("documentMentionTrigger")

/**
 * Typing "@" at the start of a word opens the mention picker (the bottom
 * bar's Mention menu). The "@" stays as typed until a person is picked; it is
 * replaced by the mention, and closing the picker leaves it as plain text.
 */
export const DocumentMentions = Extension.create<{ people: DocumentPeople }, { people: DocumentPeople; trigger: number | null; open: (() => void) | null }>({
  name: "documentMentions",
  addOptions: () => ({ people: NO_DOCUMENT_PEOPLE }),
  addStorage() { return { people: this.options.people, trigger: null, open: null } },
  addProseMirrorPlugins() {
    const storage = this.storage
    return [new Plugin({
      key: mentionKey,
      props: {
        handleTextInput(view, from, _to, text) {
          if (text !== "@" || !view.editable) return false
          const before = view.state.doc.textBetween(Math.max(view.state.selection.$from.start(), from - 1), from, "\0", "\0")
          if (before && !/\s/.test(before)) return false
          storage.trigger = from
          setTimeout(() => storage.open?.(), 0)
          return false
        },
      },
    })]
  },
})

/** Inserts a mention, replacing the "@" that opened the picker when it is still there. */
export function insertMention(editor: Editor, userId: string): boolean {
  const storage = (editor.storage as unknown as Record<string, { trigger: number | null } | undefined>).documentMentions
  const at = storage?.trigger ?? null
  if (storage) storage.trigger = null
  const mention = { type: "mention", attrs: { userId } }
  const chain = editor.chain().focus()
  if (at !== null && at + 1 <= editor.state.doc.content.size && editor.state.doc.textBetween(at, at + 1) === "@" && editor.state.selection.from === at + 1)
    return chain.insertContentAt({ from: at, to: at + 1 }, [mention, { type: "text", text: " " }]).run()
  return chain.insertContent([mention, { type: "text", text: " " }]).run()
}

/** The mention lookup an editor was created with, for the toolbar's picker. */
export function documentPeopleOf(editor: Editor): DocumentPeople | null {
  return (editor.storage as unknown as Record<string, { people?: DocumentPeople } | undefined>).documentMentions?.people ?? null
}

/** Lets the toolbar open its mention picker when "@" is typed. */
export function onMentionTrigger(editor: Editor, open: (() => void) | null) {
  const storage = (editor.storage as unknown as Record<string, { open: (() => void) | null } | undefined>).documentMentions
  if (storage) storage.open = open
}

const FormatShortcuts = Extension.create({
  name: "documentFormatShortcuts",
  addKeyboardShortcuts() {
    return {
      "Mod-b": () => this.editor.commands.toggleMark("bold"),
      "Mod-i": () => this.editor.commands.toggleMark("italic"),
      "Mod-Shift-s": () => this.editor.commands.toggleMark("strike"),
      "Mod-e": () => this.editor.commands.toggleMark("code"),
    }
  },
})

/** Inline formatting that "Clear formatting" removes. Links are content, not formatting, and stay. */
export const CLEARABLE_MARKS = ["bold", "italic", "strike", "underline", "textColor", "highlight", "code"] as const

/** Removes every clearable mark this schema has from the selection, as one undoable step. */
export function clearFormatting(editor: Editor, chain = editor.chain()): boolean {
  const present = CLEARABLE_MARKS.filter(name => editor.schema.marks[name])
  if (!present.length) return false
  return present.reduce((next, name) => next.unsetMark(name, { extendEmptyMarkRange: false }), chain).run()
}

/** Schema 5: underline, and clearing inline formatting from the selection. */
const StyleShortcuts = Extension.create({
  name: "documentStyleShortcuts",
  addKeyboardShortcuts() {
    return {
      "Mod-u": () => this.editor.commands.toggleMark("underline"),
      "Mod-\\": () => clearFormatting(this.editor),
    }
  },
})

/**
 * The engine's checklist item renders a disabled box (it is also the
 * read-only and export rendering). In the editor the box toggles `checked`
 * through an ordinary transaction while the editor is editable, so the change
 * syncs, undoes and saves like any other edit.
 */
function interactiveTaskItem(base: AnyExtension): AnyExtension {
  return (base as AnyExtension & { extend: (config: object) => AnyExtension }).extend({
    addKeyboardShortcuts(this: { editor: Editor; name: string }) {
      return {
        Enter: () => this.editor.commands.splitListItem(this.name),
        Tab: () => this.editor.commands.sinkListItem(this.name),
        "Shift-Tab": () => this.editor.commands.liftListItem(this.name),
      }
    },
    addNodeView() {
      return ({ node, editor, getPos }: { node: ProseMirrorNode; editor: Editor; getPos: () => number | undefined }) => {
        const item = document.createElement("li"), label = document.createElement("label"), box = document.createElement("input"), content = document.createElement("div")
        item.dataset.type = "taskItem"
        label.contentEditable = "false"
        box.type = "checkbox"
        box.setAttribute("aria-label", "Done")
        label.append(box); item.append(label, content)
        let current = node
        const render = (next: ProseMirrorNode) => {
          current = next
          const checked = next.attrs.checked === true
          item.dataset.checked = String(checked); box.checked = checked
          if (next.attrs.id) item.setAttribute("data-block-id", String(next.attrs.id)); else item.removeAttribute("data-block-id")
        }
        const editable = () => { box.disabled = !editor.isEditable }
        render(node); editable()
        // Keep the caret in the text: a click on the box must not move ProseMirror's selection.
        box.addEventListener("mousedown", event => event.preventDefault())
        box.addEventListener("change", () => {
          const pos = getPos()
          if (!editor.isEditable || typeof pos !== "number") { box.checked = current.attrs.checked === true; return }
          editor.chain().command(({ tr }) => {
            const target = tr.doc.nodeAt(pos)
            if (!target || target.type.name !== "taskItem") return false
            tr.setNodeMarkup(pos, undefined, { ...target.attrs, checked: box.checked })
            return true
          }).run()
        })
        editor.on("transaction", editable); editor.on("update", editable)
        return {
          dom: item, contentDOM: content,
          update: (next: ProseMirrorNode) => { if (next.type !== current.type) return false; render(next); return true },
          // Only the text lives in the content element; the box and its attributes are this view's own.
          ignoreMutation: (mutation: MutationRecord | { type: "selection"; target: Node }) => mutation.type !== "selection" && !content.contains(mutation.target),
          destroy: () => { editor.off("transaction", editable); editor.off("update", editable) },
        }
      }
    },
  })
}

/** One anchored thread to highlight: plain-text offsets inside a text block, as the engine resolves them. */
export interface CommentHighlight { id: string; block: string; from: number; to: number }
type HighlightState = { ranges: CommentHighlight[]; active: string | null; set: DecorationSet }
const highlightKey = new PluginKey<HighlightState>("documentCommentHighlights")

/**
 * The engine counts comment offsets in text only: a schema 5 date or mention
 * takes no place (#5155), while the editor counts it as one position. These
 * convert between the two inside one text block.
 */
export function textOffsetIn(block: ProseMirrorNode, parentOffset: number): number {
  let text = 0
  block.forEach((child, offset) => { if (child.isText) text += Math.min(child.nodeSize, Math.max(0, parentOffset - offset)) })
  return text
}
/** The editor offset of a text offset; `start` places it after any atoms at that point, otherwise before them. */
export function parentOffsetIn(block: ProseMirrorNode, textOffset: number, start: boolean): number | null {
  let text = 0, found: number | null = null
  block.forEach((child, offset) => {
    if (found !== null || !child.isText) return
    if (start ? textOffset < text + child.nodeSize : textOffset <= text + child.nodeSize) found = offset + textOffset - text
    text += child.nodeSize
  })
  if (found !== null) return found
  return textOffset === text ? (start ? block.content.size : lastTextEnd(block)) : null
}
function lastTextEnd(block: ProseMirrorNode): number {
  let end = 0
  block.forEach((child, offset) => { if (child.isText) end = offset + child.nodeSize })
  return end
}

function decorate(doc: ProseMirrorNode, ranges: CommentHighlight[], active: string | null): DecorationSet {
  if (!ranges.length) return DecorationSet.empty
  const blocks = new Map<string, { pos: number; node: ProseMirrorNode }>()
  doc.descendants((node, pos) => { if (node.isTextblock && typeof node.attrs.id === "string") blocks.set(node.attrs.id, { pos, node }) })
  const decorations: Decoration[] = []
  for (const range of ranges) {
    const target = blocks.get(range.block)
    if (!target || range.from < 0 || range.to <= range.from) continue
    const from = parentOffsetIn(target.node, range.from, true), to = parentOffsetIn(target.node, range.to, false)
    if (from === null || to === null || to <= from) continue
    decorations.push(Decoration.inline(target.pos + 1 + from, target.pos + 1 + to, {
      class: range.id === active ? "document-comment document-comment-active" : "document-comment", "data-comment-anchor": range.id,
    }))
  }
  return DecorationSet.create(doc, decorations)
}

/** Marks where open comment threads point; it never changes the document. */
export const CommentHighlights = Extension.create({
  name: "documentCommentHighlights",
  addProseMirrorPlugins() {
    return [new Plugin<HighlightState>({
      key: highlightKey,
      state: {
        init: (): HighlightState => ({ ranges: [], active: null, set: DecorationSet.empty }),
        apply: (tr, value): HighlightState => {
          const meta = tr.getMeta(highlightKey) as { ranges: CommentHighlight[]; active: string | null } | undefined
          if (meta) return { ...meta, set: decorate(tr.doc, meta.ranges, meta.active) }
          // Anchors are re-resolved by the engine on the next comments read; until then the marks follow the edit.
          return tr.docChanged ? { ...value, set: value.set.map(tr.mapping, tr.doc) } : value
        },
      },
      props: { decorations: state => highlightKey.getState(state)?.set },
    })]
  },
})

export function setCommentHighlights(editor: Editor, ranges: CommentHighlight[], active: string | null = null) {
  if (editor.isDestroyed) return
  editor.view.dispatch(editor.state.tr.setMeta(highlightKey, { ranges, active }).setMeta("addToHistory", false))
}

/** The text block range a new comment would anchor to, or why there is none. */
export type CommentTarget = { block: string; from: number; to: number; quote: string }
export function commentTarget(editor: Editor): CommentTarget | string {
  let { from, to } = editor.state.selection
  // A read-only editor may not have taken the browser's selection into its state.
  if (from === to && typeof window !== "undefined") {
    const selection = window.getSelection()
    if (selection && selection.rangeCount && !selection.isCollapsed && editor.view.dom.contains(selection.anchorNode) && editor.view.dom.contains(selection.focusNode)) {
      try {
        const a = editor.view.posAtDOM(selection.anchorNode!, selection.anchorOffset), b = editor.view.posAtDOM(selection.focusNode!, selection.focusOffset)
        from = Math.min(a, b); to = Math.max(a, b)
      } catch { /* outside the document content */ }
    }
  }
  if (from === to) return "Select the text you want to comment on."
  const $from = editor.state.doc.resolve(from), $to = editor.state.doc.resolve(to)
  const block = $from.parent
  if ($from.parent !== $to.parent || !["paragraph", "heading", "codeBlock"].includes(block.type.name) || typeof block.attrs.id !== "string") {
    return "Select text inside one paragraph, heading or code block."
  }
  const start = $from.parentOffset, end = $to.parentOffset
  const textFrom = textOffsetIn(block, start), textTo = textOffsetIn(block, end)
  if (textFrom === textTo) return "Select some text, not only a date or mention."
  return { block: block.attrs.id, from: textFrom, to: textTo, quote: block.textBetween(start, end) }
}

/** A toolbar toggle's state over the selection: on everywhere, on for part of it, or off. */
export type ToggleState = boolean | "mixed"

/** Text in the selection that may carry `name` (code blocks take no marks). */
function markableText(editor: Editor, name: string, visit: (node: ProseMirrorNode) => void) {
  const { state } = editor, type = state.schema.marks[name]
  if (!type) return false
  const { from, to } = state.selection
  state.doc.nodesBetween(from, to, (node, _pos, parent) => { if (node.isText && parent?.type.allowsMarkType(type)) visit(node) })
  return true
}

/** Whether all, part or none of the selected text has the mark. An empty selection reports the marks typing would use. */
export function markState(editor: Editor, name: string): ToggleState {
  const { state } = editor, type = state.schema.marks[name]
  if (!type) return false
  if (state.selection.empty) return Boolean(type.isInSet(state.storedMarks ?? state.selection.$from.marks()))
  let some = false, all = true, any = false
  markableText(editor, name, node => { any = true; const has = Boolean(type.isInSet(node.marks)); some ||= has; all &&= has })
  return !any ? false : all ? true : some ? "mixed" : false
}

/** The selection's palette colour for `textColor` or `highlight`: one name, null for the default, or "mixed". */
export function colorState(editor: Editor, name: "textColor" | "highlight"): string | null | "mixed" {
  const { state } = editor, type = state.schema.marks[name]
  if (!type) return null
  if (state.selection.empty) return (type.isInSet(state.storedMarks ?? state.selection.$from.marks())?.attrs.color as string | undefined) ?? null
  const seen = new Set<string | null>()
  markableText(editor, name, node => { seen.add((type.isInSet(node.marks)?.attrs.color as string | undefined) ?? null) })
  return seen.size > 1 ? "mixed" : seen.values().next().value ?? null
}

const keptKey = new PluginKey<boolean>("documentKeptSelection")

/**
 * Keeps a non-empty selection visible while the editor is not focused, for
 * example while a toolbar menu is open, so the person still sees what the
 * command will change. It only decorates; the selection itself never moves.
 */
export const KeptSelection = Extension.create({
  name: "documentKeptSelection",
  addProseMirrorPlugins() {
    const focus = (focused: boolean) => (view: import("@tiptap/pm/view").EditorView) => {
      if (keptKey.getState(view.state) !== focused) view.dispatch(view.state.tr.setMeta(keptKey, focused).setMeta("addToHistory", false))
      return false
    }
    return [new Plugin<boolean>({
      key: keptKey,
      state: { init: () => false, apply: (tr, focused) => (tr.getMeta(keptKey) as boolean | undefined) ?? focused },
      props: {
        handleDOMEvents: { focus: focus(true), blur: focus(false) },
        decorations: state => {
          const { from, to, empty } = state.selection
          if (keptKey.getState(state) || empty) return null
          return DecorationSet.create(state.doc, [Decoration.inline(from, to, { class: "document-selection-kept" })])
        },
      },
    })]
  },
})
