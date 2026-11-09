// Multi-selection on the Design canvas (#5217): pure helpers for toggling,
// marquee hits and group move/resize. Layer positions are stored relative to
// their screen; these helpers work in canvas coordinates and give back the
// stored (relative) values to write.
import type { DesignNode } from "./design-document"
import type { DesignBox } from "./design-viewport"

type Point = { x: number; y: number }

/** Adds a layer to the selection, or takes it out when it is already there. */
export function toggleSelection(ids: readonly string[], id: string): string[] {
 return ids.includes(id) ? ids.filter(item => item !== id) : [...ids, id]
}

function absolute(nodes: readonly DesignNode[], node: DesignNode, seen = new Set<string>()): Point {
 const own = { x: Number(node.x) || 0, y: Number(node.y) || 0 }
 if (seen.has(node.id)) return own
 seen.add(node.id)
 const parent = node.parentId ? nodes.find(item => item.id === node.parentId) : undefined
 if (!parent) return own
 const offset = absolute(nodes, parent, seen)
 return { x: offset.x + own.x, y: offset.y + own.y }
}

function boxOf(nodes: readonly DesignNode[], node: DesignNode): DesignBox {
 return { ...absolute(nodes, node), width: Math.max(1, Number(node.width) || 1), height: Math.max(1, Number(node.height) || 1) }
}

/** The layers wholly inside the marquee from `a` to `b` (canvas coordinates), in document order. */
export function marqueeHits(nodes: readonly DesignNode[], a: Point, b: Point): string[] {
 const left = Math.min(a.x, b.x), right = Math.max(a.x, b.x), top = Math.min(a.y, b.y), bottom = Math.max(a.y, b.y)
 if (right - left < 1 || bottom - top < 1) return []
 return nodes.filter(node => {
  const box = boxOf(nodes, node)
  return box.x >= left && box.y >= top && box.x + box.width <= right && box.y + box.height <= bottom
 }).map(node => node.id)
}

/** The selected layers whose screen is not selected too, so a group action moves each layer once. */
export function selectionRoots(nodes: readonly DesignNode[], ids: readonly string[]): string[] {
 const chosen = new Set(ids)
 const inside = (node: DesignNode, seen = new Set<string>()): boolean => {
  if (!node.parentId || seen.has(node.id)) return false
  seen.add(node.id)
  if (chosen.has(node.parentId)) return true
  const parent = nodes.find(item => item.id === node.parentId)
  return parent ? inside(parent, seen) : false
 }
 return nodes.filter(node => chosen.has(node.id) && !inside(node)).map(node => node.id)
}

/** The stored positions after moving the selection by (dx, dy) canvas units. */
export function moveGroup(nodes: readonly DesignNode[], ids: readonly string[], dx: number, dy: number) {
 return selectionRoots(nodes, ids).map(id => {
  const node = nodes.find(item => item.id === id)!
  return { id, x: Math.round((Number(node.x) || 0) + dx), y: Math.round((Number(node.y) || 0) + dy) }
 })
}

/** The bounding box of the selection in canvas coordinates, or null when nothing is selected. */
export function groupBox(nodes: readonly DesignNode[], ids: readonly string[]): DesignBox | null {
 const boxes = selectionRoots(nodes, ids).map(id => boxOf(nodes, nodes.find(item => item.id === id)!))
 if (!boxes.length) return null
 const left = Math.min(...boxes.map(box => box.x)), top = Math.min(...boxes.map(box => box.y))
 return { x: left, y: top, width: Math.max(...boxes.map(box => box.x + box.width)) - left, height: Math.max(...boxes.map(box => box.y + box.height)) - top }
}

/** The stored boxes after the selection's bounding box `from` became `to`: each layer scales with it. */
export function resizeGroup(nodes: readonly DesignNode[], ids: readonly string[], from: DesignBox, to: DesignBox) {
 const sx = to.width / Math.max(1, from.width), sy = to.height / Math.max(1, from.height)
 return selectionRoots(nodes, ids).map(id => {
  const node = nodes.find(item => item.id === id)!
  const box = boxOf(nodes, node)
  const x = to.x + (box.x - from.x) * sx, y = to.y + (box.y - from.y) * sy
  return {
   id,
   x: Math.round((Number(node.x) || 0) + x - box.x),
   y: Math.round((Number(node.y) || 0) + y - box.y),
   width: Math.max(1, Math.round(box.width * sx)),
   height: Math.max(1, Math.round(box.height * sy)),
  }
 })
}
