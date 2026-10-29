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

export function safeImageSource(value: string | undefined): string | undefined {
 if (!value) return undefined
 if (/^data:image\/(png|jpeg|webp|gif);base64,[a-zA-Z0-9+/=]+$/.test(value)) return value
 try { const url = new URL(value); return url.protocol === "https:" ? url.href : undefined } catch { return undefined }
}
