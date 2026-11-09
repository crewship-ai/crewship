import * as Y from "yjs"

export type DesignNodeType = "frame" | "rect" | "text" | "image"
export type DesignFontFamily = "sans" | "serif" | "mono"
export type DesignTextAlign = "left" | "center" | "right"
/**
 * Text style (#5161). Every field is optional; absent means the look every
 * older document has: 20 px sans, weight 400, line height 1.3, left, no
 * wrap (lines break only at a newline), drawn past the box (no clip). The
 * server accepts only these values (internal/design/engine.mjs).
 */
export const DESIGN_TEXT_DEFAULTS = { fontFamily: "sans", fontWeight: 400, fontSize: 20, lineHeight: 1.3, textAlign: "left", textWrap: "none", overflow: "visible" } as const
export const DESIGN_FONT_STACKS: Record<DesignFontFamily, string> = {
 sans: "system-ui, sans-serif",
 serif: "Georgia, 'Times New Roman', serif",
 mono: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
}
export const DESIGN_FONT_WEIGHTS = [100, 200, 300, 400, 500, 600, 700, 800, 900] as const
export const DESIGN_FONT_SIZE = { min: 1, max: 1000 } as const
export const DESIGN_LINE_HEIGHT = { min: 0.5, max: 5 } as const

/** The text style of a node with the defaults filled in; unknown or out-of-range values read as the default. */
export function designTextStyle(node: Partial<DesignNode>) {
 const family = node.fontFamily && node.fontFamily in DESIGN_FONT_STACKS ? node.fontFamily : DESIGN_TEXT_DEFAULTS.fontFamily
 const weight = DESIGN_FONT_WEIGHTS.includes(node.fontWeight as (typeof DESIGN_FONT_WEIGHTS)[number]) ? node.fontWeight! : DESIGN_TEXT_DEFAULTS.fontWeight
 const size = typeof node.fontSize === "number" && node.fontSize >= DESIGN_FONT_SIZE.min && node.fontSize <= DESIGN_FONT_SIZE.max ? node.fontSize : DESIGN_TEXT_DEFAULTS.fontSize
 const lineHeight = typeof node.lineHeight === "number" && node.lineHeight >= DESIGN_LINE_HEIGHT.min && node.lineHeight <= DESIGN_LINE_HEIGHT.max ? node.lineHeight : DESIGN_TEXT_DEFAULTS.lineHeight
 const align: DesignTextAlign = node.textAlign === "center" || node.textAlign === "right" ? node.textAlign : "left"
 return { family, stack: DESIGN_FONT_STACKS[family], weight, size, lineHeight, align, wrap: node.textWrap === "wrap", clip: node.overflow === "clip" }
}

export interface DesignNode {
 id: string
 /** Hide and lock (#5218): absent means shown and unlocked, like every older document. */
 hidden?: boolean
 locked?: boolean
 type: DesignNodeType
 name?: string
 parentId?: string
 x: number
 y: number
 width: number
 height: number
 fill: string
 text?: string
 src?: string
 /** Prototype link (#4449): ID of the screen a click on this layer opens. Absent or "" means no link. */
 linkTo?: string
 fontFamily?: DesignFontFamily
 fontWeight?: number
 fontSize?: number
 lineHeight?: number
 textAlign?: DesignTextAlign
 textWrap?: "none" | "wrap"
 overflow?: "visible" | "clip"
}

export function designNodes(doc: Y.Doc): DesignNode[] {
 const nodes = doc.getMap<Y.Map<unknown>>("nodes")
 const ordered = doc.getArray<string>("order").toArray()
 const ids = [...new Set([...ordered, ...nodes.keys()])]
 return ids.flatMap(id => {
  const node = nodes.get(id)
  if (!(node instanceof Y.Map)) return []
  return [{ ...node.toJSON(), id } as DesignNode]
 })
}

/** One text two people changed from the same stale canvas. */
export interface DesignConflict {
 id: string
 name: string
 mine: string
 theirs: string
 /** Whose text the merge left on the canvas. */
 kept: "mine" | "theirs"
 /** The text the merge left on the canvas; undefined when the layer was deleted. */
 left?: string
 /** The layer as the losing side had it. Only recreating a deleted layer uses all of it. */
 restore: DesignNode
}

/** Stable across tabs and reloads: the same two texts on one layer are one conflict. */
export function designConflictIdentity(conflict: DesignConflict): string {
 return JSON.stringify([conflict.id, conflict.mine, conflict.theirs])
}

function readConflictRecord(text: string): DesignConflict[] {
 if (!text) return []
 const saved: unknown = JSON.parse(text)
 if (!Array.isArray(saved)) throw new Error("Not a Design conflict record.")
 return saved as DesignConflict[]
}
export const parseDesignConflictRecord = readConflictRecord

/**
 * One step of the durable conflict list: the additions and removals of one
 * tab applied to what is stored now, not to what that tab last saw. Throws on
 * an unreadable record, so the caller's merge stores nothing over it.
 */
export function mergeDesignConflictRecord(existing: string, add: DesignConflict[], remove: string[]): string {
 const gone = new Set(remove), merged = new Map<string, DesignConflict>()
 for (const conflict of [...readConflictRecord(existing), ...add]) {
  const identity = designConflictIdentity(conflict)
  if (!gone.has(identity) && !merged.has(identity)) merged.set(identity, conflict)
 }
 return merged.size ? JSON.stringify([...merged.values()]) : ""
}

/**
 * Yjs merges a map field by client order, not by time, so one of two concurrent
 * texts disappears without a trace. Compares what each side made of the last
 * confirmed canvas with the merged result, and names every text that lost. Only
 * the text field counts: geometry and colours stay last-writer-wins.
 */
export function designConflicts(base: DesignNode[], mine: DesignNode[], theirs: DesignNode[], merged: DesignNode[]): DesignConflict[] {
 const byId = (nodes: DesignNode[]) => new Map(nodes.map(node => [node.id, node]))
 const [was, local, remote, now] = [byId(base), byId(mine), byId(theirs), byId(merged)]
 return [...new Set([...was.keys(), ...local.keys(), ...remote.keys()])].flatMap(id => {
  const before = was.get(id), my = local.get(id), their = remote.get(id)
  const isText = (my ?? their)?.type === "text"
  // A live layer without a text says nothing: a frame carries the server's whole
  // delete set, so the confirmed side loses a text to a local edit it lacks. Only
  // a missing layer is a change.
  if (!before || !isText || my?.text === before.text || their?.text === before.text || my?.text === their?.text || (their && their.text === undefined)) return []
  const kept = now.get(id)?.text === my?.text ? "mine" : "theirs"
  const restore = kept === "mine" ? their : my
  if (!restore) return []
  return [{ id, name: my?.name || their?.name || "Text", mine: my?.text ?? "", theirs: their?.text ?? "", kept, left: now.get(id)?.text, restore }]
 })
}

export function upsertDesignNode(doc: Y.Doc, patch: Partial<DesignNode> & { id: string }) {
 doc.transact(() => {
  const nodes = doc.getMap<Y.Map<unknown>>("nodes")
  let node = nodes.get(patch.id)
  if (!node && !patch.type) return
  if (!node) {
   node = new Y.Map()
   nodes.set(patch.id, node)
   doc.getArray<string>("order").push([patch.id])
  }
  for (const [key, value] of Object.entries(patch)) if (value !== undefined) node.set(key, value)
 }, "editor")
}

/**
 * Brings a lost text back without taking anything else from the stale side.
 * The layer's current geometry, style, name and links stay. Text changed after
 * the conflict stays too: the lost text then lands in a separate layer beside
 * it. Only a deleted layer is rebuilt from the snapshot.
 */
export function restoreDesignConflict(doc: Y.Doc, conflict: DesignConflict): "text" | "layer" | "recreated" | "none" {
 const lost = conflict.kept === "mine" ? conflict.theirs : conflict.mine
 const nodes = doc.getMap<Y.Map<unknown>>("nodes")
 const current = nodes.get(conflict.id)
 if (!(current instanceof Y.Map)) {
  const { parentId, ...layer } = conflict.restore
  upsertDesignNode(doc, { ...layer, text: lost, ...(parentId && nodes.has(parentId) ? { parentId } : {}) })
  return "recreated"
 }
 const text = current.get("text")
 if (text === lost) return "none"
 if (text === conflict.left) {
  upsertDesignNode(doc, { id: conflict.id, text: lost })
  return "text"
 }
 const { linkTo: _linkTo, ...layer } = { ...current.toJSON(), id: conflict.id } as DesignNode
 upsertDesignNode(doc, { ...layer, id: crypto.randomUUID(), name: `${layer.name || conflict.name} (restored text)`, y: (Number(layer.y) || 0) + (Number(layer.height) || 0) + 8, text: lost })
 return "layer"
}

/**
 * Whether a document holds a stored work generation (an encoded Y.Snapshot).
 * Compares the delete set too: work that is only a deletion leaves the state
 * vector alone. An unreadable generation is not covered.
 */
export function designWorkCovered(document: Y.Snapshot, work: Uint8Array): boolean {
 try {
  const stored = Y.decodeSnapshot(work)
  for (const [client, clock] of stored.sv) if (clock > (document.sv.get(client) ?? 0)) return false
  for (const [client, ranges] of stored.ds.clients) {
   for (const range of ranges) {
    for (let clock = range.clock; clock < range.clock + range.len; clock++) if (!Y.isDeleted(document.ds, Y.createID(client, clock))) return false
   }
  }
  return true
 } catch { return false }
}

/**
 * The stored canvas: one entry per tab, each a full document state with the
 * confirmed canvas it was made from. One row, so an entry and its base are one
 * write, and a tab replaces only its own entry. Older builds read another row
 * kind, a bare state, which is only ever read here: a rollback finds no draft
 * rather than a format it cannot open.
 */
export interface DesignDraftEntry { state: string; baseline: string | null }
export type DesignDraftRecord = Record<string, DesignDraftEntry>
const DRAFT_FORMAT = "crewship-design-draft-v2"
export function parseDesignDraft(text: string): DesignDraftRecord {
 if (!text) return {}
 const saved = JSON.parse(text) as { format?: unknown; tabs?: DesignDraftRecord }
 if (saved.format !== DRAFT_FORMAT || !saved.tabs) throw new Error("Not a Design draft record.")
 return saved.tabs
}
export function serializeDesignDraft(tabs: DesignDraftRecord): string {
 return Object.keys(tabs).length ? JSON.stringify({ format: DRAFT_FORMAT, tabs }) : ""
}
/** Parts a tab added after an identity change. A later part holds that tab's newer entry. */
export function combineDesignDrafts(parts: string[]): string {
 return serializeDesignDraft(Object.assign({}, ...parts.map(parseDesignDraft)))
}

/** Whether the document holds everything in a full-state update. An unreadable update is not held. */
export function designStateCovered(document: Y.Snapshot, state: Uint8Array): boolean {
 const stored = new Y.Doc()
 try {
  Y.applyUpdate(stored, state)
  return designWorkCovered(document, Y.encodeSnapshot(Y.snapshot(stored)))
 } catch { return false } finally { stored.destroy() }
}

/**
 * What a merge of this tab's canvas into the stored one would drop. Tabs share
 * no history until the server relays it, so a merge keeps one of two concurrent
 * texts by client order, or none when the layer was deleted. A text the other
 * side has already seen was replaced knowingly and does not count.
 */
export function designDraftConflicts(mine: Y.Doc, stored: Uint8Array): DesignConflict[] {
 const other = new Y.Doc(), merged = new Y.Doc()
 try {
  Y.applyUpdate(other, stored)
  Y.applyUpdate(merged, stored)
  Y.applyUpdate(merged, Y.encodeStateAsUpdate(mine))
  const [local, theirs, now] = [mine, other, merged].map(doc => new Map(designNodes(doc).map(node => [node.id, node])))
  const unseen = (from: Y.Doc, by: Y.Doc, id: string) => {
   // The map entry behind the value: only it says who wrote that text.
   const item = from.getMap<Y.Map<unknown>>("nodes").get(id)?._map.get("text")
   return Boolean(item && item.id.clock >= Y.getState(by.store, item.id.client))
  }
  return [...new Set([...local.keys(), ...theirs.keys()])].flatMap(id => {
   const my = local.get(id), their = theirs.get(id), left = now.get(id)?.text
   const lost = (node: DesignNode | undefined, from: Y.Doc, by: Y.Doc) => node?.type === "text" && node.text !== undefined && node.text !== left && unseen(from, by, id)
   return ([[their, other, mine, "mine"], [my, mine, other, "theirs"]] as const).flatMap(([node, from, by, kept]) => lost(node, from, by) ? [{ id, name: my?.name || their?.name || "Text", mine: my?.text ?? "", theirs: their?.text ?? "", kept, left, restore: node! }] : [])
  })
 } finally { other.destroy(); merged.destroy() }
}

export function removeDesignNode(doc: Y.Doc, id: string) {
 doc.transact(() => {
  const nodes = doc.getMap<Y.Map<unknown>>("nodes")
  const deleted = new Set([id])
  let changed = true
  while (changed) {
   changed = false
   nodes.forEach((node, key) => {
    if (!deleted.has(key) && deleted.has(String(node.get("parentId")))) { deleted.add(key); changed = true }
   })
  }
  deleted.forEach(key => nodes.delete(key))
  const order = doc.getArray<string>("order")
  for (let index = order.length - 1; index >= 0; index--) if (deleted.has(order.get(index))) order.delete(index, 1)
 }, "editor")
}

/**
 * Sets or removes the prototype link of an existing, non-screen layer. A
 * removed link deletes the key, so documents without links stay byte-for-byte
 * the shape older clients wrote. Playback never calls this.
 */
export function setDesignNodeLink(doc: Y.Doc, id: string, target: string | null) {
 const node = doc.getMap<Y.Map<unknown>>("nodes").get(id)
 if (!(node instanceof Y.Map) || node.get("type") === "frame") return
 const next = target || undefined
 if ((node.get("linkTo") || undefined) === next) return
 doc.transact(() => { if (next) node.set("linkTo", next); else node.delete("linkTo") }, "editor")
}

export type DesignLinkTarget = { kind: "none" } | { kind: "screen"; screen: DesignNode } | { kind: "missing"; target: string }

/** Resolves a layer's link. A target that is gone or not a screen is "missing", never an error. */
export function designLinkTarget(nodes: DesignNode[], node: DesignNode): DesignLinkTarget {
 const target = node.type === "frame" || typeof node.linkTo !== "string" ? "" : node.linkTo
 if (!target) return { kind: "none" }
 const screen = nodes.find(item => item.id === target && item.type === "frame")
 return screen ? { kind: "screen", screen } : { kind: "missing", target }
}

/** The screen that contains a layer (or the screen itself), or undefined on the bare canvas. */
export function designScreenOf(nodes: DesignNode[], node: DesignNode): string | undefined {
 const byId = new Map(nodes.map(item => [item.id, item]))
 const seen = new Set<string>()
 for (let current: DesignNode | undefined = node; current && !seen.has(current.id); current = current.parentId ? byId.get(current.parentId) : undefined) {
  if (current.type === "frame") return current.id
  seen.add(current.id)
 }
 return undefined
}

export function safeImageSource(value: string | undefined): string | undefined {
 if (!value) return undefined
 if (/^data:image\/(png|jpeg|webp|gif);base64,[a-zA-Z0-9+/=]+$/.test(value)) return value
 try { const url = new URL(value); return url.protocol === "https:" ? url.href : undefined } catch { return undefined }
}

export type DesignOrderMove = "forward" | "backward" | "front" | "back"

/**
 * Changes a layer's stacking order among its siblings: layers with the same
 * screen (or none), screens among screens. Drawing follows the document order,
 * so this moves the one ID in `order` (its entry is deleted and inserted again
 * next to its new neighbour); every other entry stays, so a concurrent edit,
 * delete or reorder of another layer keeps its effect. Returns false when the
 * layer is already at that end.
 */
export function reorderDesignNode(doc: Y.Doc, id: string, move: DesignOrderMove): boolean {
 const nodes = doc.getMap<Y.Map<unknown>>("nodes")
 if (!(nodes.get(id) instanceof Y.Map)) return false
 const order = doc.getArray<string>("order")
 const listed = order.toArray()
 const group = (key: string) => {
  const node = nodes.get(key)
  return !(node instanceof Y.Map) ? null : node.get("type") === "frame" ? "frame" : `layer:${String(node.get("parentId") ?? "")}`
 }
 const mine = group(id)
 // Siblings in drawing order: listed first (first entry wins), then unlisted ones.
 const siblings = [...new Set([...listed, ...nodes.keys()])].filter(key => group(key) === mine)
 const at = siblings.indexOf(id)
 const to = move === "forward" ? at + 1 : move === "backward" ? at - 1 : move === "front" ? siblings.length - 1 : 0
 if (to < 0 || to >= siblings.length || to === at) return false
 const neighbour = siblings[to]
 doc.transact(() => {
  // Remove this ID's own entries only, from the end so indexes stay valid.
  for (let index = listed.length - 1; index >= 0; index--) if (listed[index] === id) order.delete(index, 1)
  const now = order.toArray()
  let anchor = now.indexOf(neighbour)
  if (anchor < 0) { order.push([neighbour]); anchor = now.length }
  order.insert(to > at ? anchor + 1 : anchor, [id])
 }, "editor")
 return true
}

/**
 * Undo history of one person: only transactions this client made through the
 * editor ("editor" origin) are recorded, so undo never takes back a peer's
 * change. Edits within half a second form one step. When a peer changes a
 * property (or deletes a layer) that a step in this history also changed, that
 * step is dropped: undoing it after concurrent edits could otherwise delete or
 * overwrite the value both sides agreed on. Layer order is not tracked this way,
 * since order edits from both sides merge without losing entries.
 */
export function createDesignUndo(doc: Y.Doc) {
 const nodes = doc.getMap<Y.Map<unknown>>("nodes")
 const undo = new Y.UndoManager([nodes, doc.getArray("order")], { trackedOrigins: new Set(["editor"]), captureTimeout: 500 })
 // Keys are "<node id>:<property>"; "<node id>:*" stands for the whole layer.
 const touched = (changed: Y.Transaction["changed"]) => {
  const keys = new Set<string>()
  const root: unknown = nodes
  changed.forEach((subs, type) => {
   if (type === root) subs.forEach(id => { if (id) keys.add(`${id}:*`) })
   else if (type._item && type._item.parent === root && typeof type._item.parentSub === "string") {
    const id = type._item.parentSub
    // "id" never changes, but every edit writes it again; it is no conflict.
    subs.forEach(key => { if (key && key !== "id") keys.add(`${id}:${key}`) })
   }
  })
  return keys
 }
 const overlaps = (mine: Set<string> | undefined, theirs: Set<string>) => {
  if (!mine) return false
  for (const key of mine) {
   if (theirs.has(key)) return true
   const id = key.slice(0, key.lastIndexOf(":"))
   if (theirs.has(`${id}:*`) || (key.endsWith(":*") && [...theirs].some(other => other.startsWith(`${id}:`)))) return true
  }
  return false
 }
 const onTransaction = (tr: Y.Transaction) => {
  const keys = touched(tr.changed)
  if (keys.size === 0) return
  const local = tr.local && (tr.origin === "editor" || tr.origin === undo)
  if (local) {
   // Tag the step this transaction joined (or the redo step it created).
   const item = tr.origin === undo ? (undo.redoing ? undo.undoStack : undo.undoing ? undo.redoStack : undo.undoStack).at(-1) : undo.undoStack.at(-1)
   if (!item) return
   const mine = (item.meta.get("keys") as Set<string> | undefined) ?? new Set<string>()
   keys.forEach(key => mine.add(key))
   item.meta.set("keys", mine)
   return
  }
  const before = undo.undoStack.length + undo.redoStack.length
  undo.undoStack = undo.undoStack.filter(item => !overlaps(item.meta.get("keys") as Set<string> | undefined, keys))
  undo.redoStack = undo.redoStack.filter(item => !overlaps(item.meta.get("keys") as Set<string> | undefined, keys))
  if (undo.undoStack.length + undo.redoStack.length !== before) {
   undo.stopCapturing()
   undo.emit("stack-cleared", [{ undoStackCleared: true, redoStackCleared: true }])
  }
 }
 doc.on("afterTransaction", onTransaction)
 const destroy = undo.destroy.bind(undo)
 undo.destroy = () => { doc.off("afterTransaction", onTransaction); destroy() }
 return undo
}

/**
 * Layers not drawn (#5218): hidden ones and everything inside a hidden screen.
 * Locked ones work the same way for `designLockedIds`.
 */
function inherited(nodes: readonly DesignNode[], flag: "hidden" | "locked"): Set<string> {
 const byId = new Map(nodes.map(node => [node.id, node]))
 const out = new Set<string>()
 for (const node of nodes) {
  const seen = new Set<string>()
  for (let at: DesignNode | undefined = node; at && !seen.has(at.id); at = at.parentId ? byId.get(at.parentId) : undefined) {
   seen.add(at.id)
   if (at[flag] === true) { out.add(node.id); break }
  }
 }
 return out
}
export const designHiddenIds = (nodes: readonly DesignNode[]) => inherited(nodes, "hidden")
export const designLockedIds = (nodes: readonly DesignNode[]) => inherited(nodes, "locked")
