import * as Y from "yjs"

export type DesignNodeType = "frame" | "rect" | "text" | "image"
export interface DesignNode {
 id: string
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
