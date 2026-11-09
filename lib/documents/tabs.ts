import * as Y from "yjs"
import { createTabs } from "../../tools/docs-service/tabs-core.mjs"

/** Where a tab goes: under `parent` (null for the top level), before `before` (null for last). */
export interface TabPlace { parent?: string | null; before?: string | null }

/** The model's API, typed (the JavaScript defaults would narrow `parent` and `before` to null). */
export interface DocumentTabsModel {
  TABS: string; NAMES: string; DEFAULT: string; PREFIX: string; ORIGIN: string
  LIMITS: Readonly<{ tabs: number; create: number; name: number; depth: number; retainedBytes: number }>
  fragmentName(id: string): string
  validateTabs(doc: Y.Doc): unknown
  tree(doc: Y.Doc): DocumentTab[]
  createTab(doc: Y.Doc, input: TabPlace & { id: string; name: string; blockId: string }, origin?: string): unknown
  renameTab(doc: Y.Doc, id: string, name: string, origin?: string): void
  moveTab(doc: Y.Doc, id: string, place: TabPlace, origin?: string): void
  deleteTab(doc: Y.Doc, id: string, origin?: string): void
}

/** The shared Docs tabs model with this browser's own pinned Yjs classes. */
export const documentTabs = createTabs({ Y }) as unknown as DocumentTabsModel

/**
 * The content schema planned to admit tabs. No server serves it yet, so the
 * tab strip never shows on a real document until that admission lands.
 */
export const TABS_SCHEMA = 7

export interface DocumentTab { id: string; name: string; order: string; children: DocumentTab[] }
export interface DocumentTabRow { id: string; name: string; depth: number; parent: string | null; siblings: string[] }

/** The tab tree in display order, each row with its depth, displayed parent and that parent's children. */
export function tabRows(doc: Y.Doc): DocumentTabRow[] {
  const out: DocumentTabRow[] = []
  const walk = (nodes: DocumentTab[], parent: string | null, depth: number) => {
    const siblings = nodes.map(node => node.id)
    for (const node of nodes) { out.push({ id: node.id, name: node.name, depth, parent, siblings }); walk(node.children, node.id, depth + 1) }
  }
  walk(documentTabs.tree(doc), null, 0)
  return out
}

/** The tab tree, or only the first tab when the stored metadata cannot be read (the view never crashes on it). */
export function readTabRows(doc: Y.Doc): { rows: DocumentTabRow[]; unreadable: boolean } {
  try { return { rows: tabRows(doc), unreadable: false } } catch {
    return { rows: [{ id: documentTabs.DEFAULT, name: "Tab 1", depth: 0, parent: null, siblings: [documentTabs.DEFAULT] }], unreadable: true }
  }
}

/**
 * The one rule for which tab is shown and edited: the chosen tab while the
 * tree shows it, otherwise the first row. The strip and the editor both use
 * it, so text can never go into a tab other than the one shown as chosen.
 */
export function shownTab(rows: DocumentTabRow[], selected: string): { id: string; field: string } {
  const id = (rows.find(row => row.id === selected) ?? rows[0])?.id ?? documentTabs.DEFAULT
  return { id, field: documentTabs.fragmentName(id) }
}

/** A new tab or block ID: random, within the model's ID pattern. */
export function newTabId(prefix: string): string {
  const bytes = crypto.getRandomValues(new Uint8Array(9))
  return prefix + Array.from(bytes, byte => "abcdefghijklmnopqrstuvwxyz0123456789"[byte % 36]).join("")
}

/** `#tab=<id>` in a URL fragment; anything else names no tab. */
export function tabFromHash(hash: string): string | null {
  const match = /^#tab=([a-zA-Z0-9_-]{1,80})$/.exec(hash)
  return match ? match[1] : null
}
