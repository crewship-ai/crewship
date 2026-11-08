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
export const SUGGESTED_LANGUAGES = ["bash", "c", "cpp", "csharp", "css", "go", "html", "java", "javascript", "json", "kotlin", "markdown", "php", "python", "ruby", "rust", "sql", "swift", "typescript", "yaml"]

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

/**
 * The code block with its header: the language (or "Code"), and Copy, which
 * puts the block's exact text on the clipboard. Only `contentDOM` is
 * editable; the header is outside the document content.
 */
export function codeBlockView(base: AnyExtension, { languages }: { languages: boolean }): AnyExtension {
  return (base as AnyExtension & { extend: (config: object) => AnyExtension }).extend({
    addNodeView() {
      return ({ node }: { node: ProseMirrorNode; editor: Editor; getPos: () => number | undefined }) => {
        const dom = document.createElement("div"), header = document.createElement("div"), label = document.createElement("span")
        const copy = document.createElement("button"), status = document.createElement("span")
        const pre = document.createElement("pre"), code = document.createElement("code")
        dom.dataset.codeBlock = ""
        header.contentEditable = "false"
        header.dataset.codeHeader = ""
        label.dataset.codeLanguage = ""
        copy.type = "button"; copy.textContent = "Copy"; copy.dataset.codeCopy = ""
        status.setAttribute("role", "status"); status.className = "sr-only"
        header.append(label, copy, status)
        pre.append(code); dom.append(header, pre)
        let current = node, reset: ReturnType<typeof setTimeout> | undefined
        const render = (next: ProseMirrorNode) => {
          current = next
          const language = languages && typeof next.attrs.language === "string" ? next.attrs.language : null
          label.textContent = language ?? "Code"
          for (const element of [dom, pre]) { if (language) element.dataset.language = language; else delete element.dataset.language }
          if (next.attrs.id) dom.setAttribute("data-block-id", String(next.attrs.id)); else dom.removeAttribute("data-block-id")
          copy.setAttribute("aria-label", language ? `Copy ${language} code` : "Copy code")
        }
        render(node)
        // Keep the caret where it is: pressing Copy must not move ProseMirror's selection.
        copy.addEventListener("mousedown", event => event.preventDefault())
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
          // Only the code text is document content; the header is this view's own.
          ignoreMutation: (mutation: MutationRecord | { type: "selection"; target: Node }) => mutation.type !== "selection" && !code.contains(mutation.target),
          stopEvent: (event: Event) => header.contains(event.target as Node),
          destroy: () => clearTimeout(reset),
        }
      }
    },
  })
}
