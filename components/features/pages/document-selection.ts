import type { Editor } from "@tiptap/core"
import type { DocumentState } from "@/hooks/use-page-document"

export interface DocumentTarget { id: string; hash: string; from: number; to: number; quote: string }
export interface DocumentSelection { page_id: string; base_rev: number; epoch: string; targets: DocumentTarget[] }
function canonical(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`
  if (value && typeof value === "object") return `{${Object.entries(value).filter(([key, item]) => item !== undefined && !(["content", "marks"].includes(key) && Array.isArray(item) && item.length === 0)).sort(([a], [b]) => a.localeCompare(b)).map(([key, item]) => `${JSON.stringify(key)}:${canonical(key === "attrs" && item && typeof item === "object" ? Object.fromEntries(Object.entries(item).filter(([, attribute]) => attribute !== null)) : item)}`).join(",")}}`
  return JSON.stringify(value) ?? "undefined"
}
export function captureDocumentSelection(editor: Editor | null, confirmed: DocumentState | undefined, pageId: string | undefined, ready: boolean): DocumentSelection {
  if (!editor || !confirmed || !ready) throw new Error("Wait until the document is saved before selecting text for the agent.")
  const identity = confirmed.page_id || pageId
  if (!identity || (confirmed.page_id && pageId && confirmed.page_id !== pageId)) throw new Error("The confirmed page identity is unavailable. Reopen this page.")
  if (canonical(editor.getJSON()) !== canonical(confirmed.content)) throw new Error("The editor differs from the confirmed document. Wait for saving, then select it again.")
  const { from, to } = editor.state.selection
  if (from === to) throw new Error("Select text in a paragraph or heading before sending agent work.")
  const savedBlocks = new Map<string, unknown>()
  function visit(value: unknown) {
    if (!value || typeof value !== "object") return
    const node = value as { attrs?: { id?: string }; content?: unknown[] }
    if (node.attrs?.id) savedBlocks.set(node.attrs.id, node)
    for (const child of node.content ?? []) visit(child)
  }
  visit(confirmed.content)
  const targets: DocumentTarget[] = []
  let quoted = 0
  editor.state.doc.nodesBetween(from, to, (node, pos) => {
    if (!["paragraph", "heading"].includes(node.type.name)) return
    const start = Math.max(from, pos + 1) - pos - 1
    const end = Math.min(to, pos + 1 + node.content.size) - pos - 1
    if (end <= start) return false
    const id = node.attrs.id as string | undefined
    const entry = confirmed.outline?.find(block => block.id === id)
    if (!id || !entry?.hash || entry.type !== node.type.name || entry.text !== node.textContent || canonical(savedBlocks.get(id)) !== canonical(node.toJSON())) throw new Error("Selected text differs from the confirmed document. Wait for saving, then select it again.")
    const splitsSurrogate = (offset: number) => offset > 0 && offset < node.textContent.length && /[\uD800-\uDBFF]/.test(node.textContent[offset - 1]) && /[\uDC00-\uDFFF]/.test(node.textContent[offset])
    if (splitsSurrogate(start) || splitsSurrogate(end)) throw new Error("Select whole characters before sending agent work.")
    const quote = node.textBetween(start, end)
    quoted += quote.length
    targets.push({ id, hash: entry.hash, from: start, to: end, quote })
    if (targets.length > 1) throw new Error("Select text within one paragraph or heading per request.")
    if (quoted > 4096) throw new Error("Select up to 4096 characters.")
    return false
  })
  if (!targets.length) throw new Error("Select nonempty text in a paragraph or heading before sending agent work.")
  return { page_id: identity, base_rev: confirmed.rev, epoch: confirmed.epoch, targets }
}

export interface DocumentStructureSelectionSnapshot {
  page_id: string
  base_rev: number
  epoch: string
  structure: { block_ids: string[]; allow_insert_after: true }
  blocks: { id: string; type: string; text: string }[]
}

export function documentRootBlocks(content: unknown): { id: string; type: string; text: string }[] {
  if (!content || typeof content !== "object" || !Array.isArray((content as { content?: unknown }).content)) return []
  const text = (value: unknown): string => {
    if (!value || typeof value !== "object") return ""
    const node = value as { text?: unknown; content?: unknown[] }
    return typeof node.text === "string" ? node.text : (node.content ?? []).map(text).join("")
  }
  return ((content as { content: unknown[] }).content).flatMap(value => {
    if (!value || typeof value !== "object") return []
    const node = value as { type?: unknown; attrs?: { id?: unknown } }
    return typeof node.type === "string" && typeof node.attrs?.id === "string" ? [{ id: node.attrs.id, type: node.type, text: text(value) }] : []
  })
}

export function captureDocumentStructureSelection(editor: Editor | null, confirmed: DocumentState | undefined, pageId: string | undefined, ready: boolean, ids: string[]): DocumentStructureSelectionSnapshot {
  if (!editor || !confirmed || !ready) throw new Error("Wait until the document is saved before selecting blocks for the agent.")
  if (confirmed.schema !== 3) throw new Error("Explicitly upgrade this document before requesting structural edits.")
  if (ids.length === 0 || ids.length > 256 || new Set(ids).size !== ids.length) throw new Error("Select between 1 and 256 whole blocks. The selection is never truncated.")
  const identity = confirmed.page_id || pageId
  if (!identity || (confirmed.page_id && pageId && confirmed.page_id !== pageId)) throw new Error("The confirmed page identity is unavailable. Reopen this page.")
  if (canonical(editor.getJSON()) !== canonical(confirmed.content)) throw new Error("The editor differs from the confirmed document. Wait for saving, then select blocks again.")
  const roots = documentRootBlocks(confirmed.content), start = roots.findIndex(block => block.id === ids[0])
  if (start < 0 || ids.some((id, index) => roots[start + index]?.id !== id)) throw new Error("Select a contiguous range of whole root blocks.")
  if (ids.some(id => !confirmed.outline?.some(block => block.id === id && block.hash))) throw new Error("The confirmed block outline is unavailable. Reopen this page.")
  return { page_id: identity, base_rev: confirmed.rev, epoch: confirmed.epoch, structure: { block_ids: [...ids], allow_insert_after: true }, blocks: roots.slice(start, start + ids.length) }
}
