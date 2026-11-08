import { Extension, type AnyExtension, type Editor } from "@tiptap/core"
import { Plugin, PluginKey } from "@tiptap/pm/state"
import { Decoration, DecorationSet } from "@tiptap/pm/view"
import type { Node as ProseMirrorNode } from "@tiptap/pm/model"
import { common, createLowlight } from "lowlight"

/**
 * How a Docs code block looks and behaves in the editor, without changing
 * what it holds. Highlighting is a ProseMirror decoration set computed from
 * the block's text: it never writes to the document, so collaborators, the
 * saved revision, comments and exports see exactly the typed characters.
 * The block header (language, Copy) is a node view's own chrome outside the
 * editable text. Code is only tokenized, never evaluated.
 */

const lowlight = createLowlight(common)

/** Longest block text that is highlighted; longer blocks stay plain so typing stays responsive. */
export const MAX_HIGHLIGHT_CHARS = 20_000

/** Languages the highlighter knows, by the name a block stores (aliases such as `ts`, `py`, `sh` and `yml` included). */
export function highlightable(language: unknown): language is string {
  return typeof language === "string" && language !== "plaintext" && lowlight.registered(language)
}

/** Suggestions offered by the language field; any valid language name can still be typed. */
export const SUGGESTED_LANGUAGES = ["bash", "c", "cpp", "csharp", "css", "go", "html", "java", "javascript", "json", "kotlin", "latex", "markdown", "php", "python", "ruby", "rust", "sql", "swift", "typescript", "yaml"]

/** One highlighted run inside a block's text: plain-text offsets and the token classes. */
export interface CodeToken { from: number; to: number; className: string }

type Hast = { type: string; value?: string; children?: Hast[]; properties?: { className?: unknown } }

/** Token runs for a block's text, or none when the language is unknown, the text too long or the tokenizer fails. */
export function codeTokens(text: string, language: unknown): CodeToken[] {
  if (!text || text.length > MAX_HIGHLIGHT_CHARS || !highlightable(language)) return []
  let tree: Hast
  try { tree = lowlight.highlight(language, text) as unknown as Hast } catch { return [] }
  const tokens: CodeToken[] = []
  let offset = 0
  const walk = (node: Hast, classes: string[]) => {
    if (node.type === "text") {
      const length = node.value?.length ?? 0
      if (length && classes.length) tokens.push({ from: offset, to: offset + length, className: classes.join(" ") })
      offset += length
      return
    }
    const own = Array.isArray(node.properties?.className) ? (node.properties.className as unknown[]).filter((name): name is string => typeof name === "string" && /^hljs-[a-z_-]+$/.test(name)) : []
    for (const child of node.children ?? []) walk(child, own.length ? [...classes, ...own] : classes)
  }
  walk(tree, [])
  // The tokenizer must account for every character, or offsets would point at the wrong text.
  return offset === text.length ? tokens : []
}

const highlightKey = new PluginKey<DecorationSet>("documentCodeHighlight")
const cache = new WeakMap<ProseMirrorNode, CodeToken[]>()

function tokensOf(node: ProseMirrorNode): CodeToken[] {
  let tokens = cache.get(node)
  if (!tokens) { tokens = codeTokens(node.textContent, node.attrs.language); cache.set(node, tokens) }
  return tokens
}

function highlight(doc: ProseMirrorNode): DecorationSet {
  const decorations: Decoration[] = []
  doc.descendants((node, pos) => {
    if (node.type.name !== "codeBlock") return true
    for (const token of tokensOf(node)) decorations.push(Decoration.inline(pos + 1 + token.from, pos + 1 + token.to, { class: token.className }))
    return false
  })
  return decorations.length ? DecorationSet.create(doc, decorations) : DecorationSet.empty
}

/** Syntax colours for code blocks with a known language; decorations only, recomputed when the text changes. */
export const CodeHighlight = Extension.create({
  name: "documentCodeHighlight",
  addProseMirrorPlugins() {
    return [new Plugin<DecorationSet>({
      key: highlightKey,
      state: {
        init: (_config, state) => highlight(state.doc),
        apply: (tr, value) => tr.docChanged ? highlight(tr.doc) : value,
      },
      props: { decorations: state => highlightKey.getState(state) },
    })]
  },
})

/** The exact characters of a code block, newlines and trailing spaces included, as the clipboard should receive them. */
export const codeBlockText = (node: ProseMirrorNode) => node.textContent

/** What a preview renderer returns: the rendered element, or why this block cannot be shown. */
export type PreviewResult = { ok: true; node: HTMLElement } | { ok: false; error: string }
type Renderer = (source: string) => PreviewResult

/**
 * Languages that opt a code block into a rendered preview, and their
 * renderers. Only these languages are rendered; every other code block stays
 * code. Renderers load on first use so the editor does not carry them.
 */
const PREVIEWS: Record<string, { name: string; load: () => Promise<Renderer> }> = {
  latex: { name: "formula", load: () => import("./code-preview-math").then(module => module.renderMath) },
  math: { name: "formula", load: () => import("./code-preview-math").then(module => module.renderMath) },
}

/** True when a block with this language offers Source and Preview. */
export const previewable = (language: unknown): language is string => typeof language === "string" && Object.hasOwn(PREVIEWS, language)

/**
 * Whether each block shows its source or its preview, by block id. This is
 * the viewer's own choice in this tab: it is not document content, is never
 * saved and never reaches collaborators.
 */
const modes = new Map<string, "source" | "preview">()

/**
 * The code block with its header: the language (or "Code"), Source and
 * Preview for languages that opt in, and Copy, which puts the block's exact
 * text on the clipboard. Only `contentDOM` is editable; the header and the
 * preview are outside the document content. A preview that cannot render
 * shows its reason and the source beside it; nothing in the document changes.
 * Moving the caret into a previewed block switches it to Source, so nobody
 * types into hidden text.
 */
export function codeBlockView(base: AnyExtension, { languages }: { languages: boolean }): AnyExtension {
  return (base as AnyExtension & { extend: (config: object) => AnyExtension }).extend({
    addNodeView() {
      return ({ node, editor, getPos }: { node: ProseMirrorNode; editor: Editor; getPos: () => number | undefined }) => {
        const dom = document.createElement("div"), header = document.createElement("div"), label = document.createElement("span")
        const toggle = document.createElement("span"), sourceButton = document.createElement("button"), previewButton = document.createElement("button")
        const copy = document.createElement("button"), status = document.createElement("span")
        const preview = document.createElement("div"), pre = document.createElement("pre"), code = document.createElement("code")
        dom.dataset.codeBlock = ""
        header.contentEditable = "false"
        header.dataset.codeHeader = ""
        label.dataset.codeLanguage = ""
        toggle.setAttribute("role", "group"); toggle.dataset.codeMode = ""
        for (const [button, text] of [[sourceButton, "Source"], [previewButton, "Preview"]] as const) { button.type = "button"; button.textContent = text; toggle.append(button) }
        copy.type = "button"; copy.textContent = "Copy"; copy.dataset.codeCopy = ""
        status.setAttribute("role", "status"); status.className = "sr-only"
        preview.contentEditable = "false"; preview.dataset.codePreview = ""
        header.append(label, toggle, copy, status)
        pre.append(code); dom.append(header, preview, pre)
        let current = node, reset: ReturnType<typeof setTimeout> | undefined, destroyed = false
        let shown: { language: string; source: string } | null = null
        const blockId = () => typeof current.attrs.id === "string" ? current.attrs.id : null
        const language = () => languages && typeof current.attrs.language === "string" ? current.attrs.language : null
        const mode = () => { const id = blockId(); return (id && modes.get(id)) || "preview" }

        const showPreview = () => {
          const lang = language()
          const active = lang !== null && previewable(lang) && mode() === "preview"
          toggle.hidden = lang === null || !previewable(lang)
          sourceButton.setAttribute("aria-pressed", String(!active)); previewButton.setAttribute("aria-pressed", String(active))
          if (!active) { preview.hidden = true; pre.hidden = false; shown = null; preview.replaceChildren(); return }
          const source = current.textContent
          if (shown && shown.language === lang && shown.source === source) return
          shown = { language: lang, source }
          preview.hidden = false
          const draw = (result: PreviewResult) => {
            if (destroyed || shown?.source !== source || shown.language !== lang) return
            if (result.ok) { preview.replaceChildren(result.node); delete preview.dataset.error; pre.hidden = true }
            else {
              const message = document.createElement("p"); message.textContent = result.error; message.setAttribute("role", "status")
              preview.replaceChildren(message); preview.dataset.error = ""; pre.hidden = false
            }
          }
          if (!preview.childElementCount) { const wait = document.createElement("p"); wait.textContent = "Loading preview…"; preview.replaceChildren(wait) }
          PREVIEWS[lang].load().then(render => draw(render(source)), () => draw({ ok: false, error: "The preview could not be loaded. The source is unchanged." }))
        }
        const setMode = (next: "source" | "preview") => { const id = blockId(); if (id) modes.set(id, next); showPreview() }

        const render = (next: ProseMirrorNode) => {
          current = next
          const lang = language()
          label.textContent = lang ?? "Code"
          for (const element of [dom, pre]) { if (lang) element.dataset.language = lang; else delete element.dataset.language }
          if (next.attrs.id) dom.setAttribute("data-block-id", String(next.attrs.id)); else dom.removeAttribute("data-block-id")
          copy.setAttribute("aria-label", lang ? `Copy ${lang} code` : "Copy code")
          toggle.setAttribute("aria-label", lang && previewable(lang) ? `Show ${PREVIEWS[lang].name}` : "Show")
          showPreview()
        }
        render(node)
        for (const button of [sourceButton, previewButton, copy]) button.addEventListener("mousedown", event => event.preventDefault())
        sourceButton.addEventListener("click", () => setMode("source"))
        previewButton.addEventListener("click", () => setMode("preview"))
        // Typing must never land in hidden text: a caret inside a previewed block shows its source.
        const followCaret = () => {
          if (!editor.isEditable || mode() !== "preview" || !previewable(language()) || pre.hidden === false) return
          const pos = getPos()
          if (typeof pos !== "number") return
          const { from, to } = editor.state.selection
          if (to > pos && from < pos + current.nodeSize) setMode("source")
        }
        editor.on("selectionUpdate", followCaret)
        // Keep the caret where it is: pressing Copy must not move ProseMirror's selection.
        copy.addEventListener("click", () => {
          const done = (text: string) => {
            copy.textContent = text; status.textContent = text === "Copied" ? "Code copied" : text
            clearTimeout(reset); reset = setTimeout(() => { copy.textContent = "Copy"; status.textContent = "" }, 2000)
          }
          const clipboard = typeof navigator !== "undefined" ? navigator.clipboard : undefined
          if (!clipboard?.writeText) { done("Copy unavailable"); return }
          clipboard.writeText(codeBlockText(current)).then(() => done("Copied"), () => done("Copy failed"))
        })
        return {
          dom, contentDOM: code,
          update: (next: ProseMirrorNode) => { if (next.type !== current.type) return false; render(next); return true },
          // Only the code text is document content; the header and the preview are this view's own.
          ignoreMutation: (mutation: MutationRecord | { type: "selection"; target: Node }) => mutation.type !== "selection" && !code.contains(mutation.target),
          stopEvent: (event: Event) => header.contains(event.target as Node) || preview.contains(event.target as Node),
          destroy: () => { destroyed = true; clearTimeout(reset); editor.off("selectionUpdate", followCaret) },
        }
      }
    },
  })
}
