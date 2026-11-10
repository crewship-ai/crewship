/**
 * Docs Canvas viewport and placement geometry (R71 rev 2 §0.4). Pure functions:
 * the viewport is local view state, never stored in the document and never
 * taken from another person. Placement edits return new geometry only; the
 * block itself is never touched here.
 *
 * Screen = (world - origin) * scale. `origin` is the world point at the
 * viewport's top-left corner.
 */

export interface Viewport { x: number; y: number; scale: number }
export interface Point { x: number; y: number }
export interface Size { width: number; height: number }
export interface Rect { x: number; y: number; w: number; h: number }
export type Handle = "n" | "s" | "e" | "w" | "ne" | "nw" | "se" | "sw"

/** Matches the surface validator in tools/docs-service/surface-core.mjs. */
export const PLACEMENT_LIMITS = { coordinate: 1_000_000, minW: 40, maxW: 4000, minH: 24, maxH: 4000 } as const
export const ZOOM = { min: 0.1, max: 4 } as const

const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value))

export function toWorld(view: Viewport, screen: Point): Point {
  return { x: view.x + screen.x / view.scale, y: view.y + screen.y / view.scale }
}

export function toScreen(view: Viewport, world: Point): Point {
  return { x: (world.x - view.x) * view.scale, y: (world.y - view.y) * view.scale }
}

/** Pan by a screen-space delta (drag, trackpad scroll). */
export function pan(view: Viewport, dx: number, dy: number): Viewport {
  return { ...view, x: view.x - dx / view.scale, y: view.y - dy / view.scale }
}

/** Zoom by `factor` keeping the world point under `pointer` where it is. */
export function zoomAt(view: Viewport, pointer: Point, factor: number): Viewport {
  const scale = clamp(view.scale * factor, ZOOM.min, ZOOM.max)
  const anchor = toWorld(view, pointer)
  return { scale, x: anchor.x - pointer.x / scale, y: anchor.y - pointer.y / scale }
}

/** Wheel delta to a zoom factor: one notch (100 px) is about 10 %. */
export function wheelFactor(deltaY: number, deltaMode = 0): number {
  const pixels = deltaMode === 1 ? deltaY * 16 : deltaMode === 2 ? deltaY * 800 : deltaY
  return Math.exp(-clamp(pixels, -400, 400) / 1000)
}

export function bounds(rects: readonly Rect[]): Rect | null {
  if (rects.length === 0) return null
  const left = Math.min(...rects.map(r => r.x)), top = Math.min(...rects.map(r => r.y))
  const right = Math.max(...rects.map(r => r.x + r.w)), bottom = Math.max(...rects.map(r => r.y + r.h))
  return { x: left, y: top, w: right - left, h: bottom - top }
}

/**
 * Show `target` whole and centred with `padding` screen pixels around it, at
 * most at 100 % (a fit never magnifies small content), never below the zoom floor.
 */
export function fitRect(target: Rect, size: Size, padding = 48): Viewport {
  const width = Math.max(1, size.width - 2 * padding), height = Math.max(1, size.height - 2 * padding)
  const scale = clamp(Math.min(width / Math.max(1, target.w), height / Math.max(1, target.h), 1), ZOOM.min, ZOOM.max)
  return { scale, x: target.x + target.w / 2 - size.width / 2 / scale, y: target.y + target.h / 2 - size.height / 2 / scale }
}

/** Fit everything on the canvas; an empty canvas shows the origin at 100 %. */
export function fitAll(rects: readonly Rect[], size: Size, padding = 48): Viewport {
  const all = bounds(rects)
  return all ? fitRect(all, size, padding) : { x: -padding, y: -padding, scale: 1 }
}

/** Focus one placement: centred and whole, at most at 100 %. */
export const focusRect = fitRect

const whole = (n: number) => Math.round(n)

/** Move by a screen-space drag delta. Geometry only, rounded to whole world units. */
export function moved(rect: Rect, dx: number, dy: number, scale: number): Rect {
  const limit = PLACEMENT_LIMITS.coordinate
  return { ...rect, x: clamp(whole(rect.x + dx / scale), -limit, limit), y: clamp(whole(rect.y + dy / scale), -limit, limit) }
}

/** Resize from a handle by a screen-space delta, within the validator's limits; the opposite edge stays put. */
export function resized(rect: Rect, handle: Handle, dx: number, dy: number, scale: number): Rect {
  const L = PLACEMENT_LIMITS
  let { x, y, w, h } = rect
  const wx = dx / scale, wy = dy / scale
  if (handle.includes("e")) w = clamp(whole(w + wx), L.minW, L.maxW)
  if (handle.includes("w")) { const next = clamp(whole(w - wx), L.minW, L.maxW); x = whole(x + w - next); w = next }
  if (handle.includes("s")) h = clamp(whole(h + wy), L.minH, L.maxH)
  if (handle.includes("n")) { const next = clamp(whole(h - wy), L.minH, L.maxH); y = whole(y + h - next); h = next }
  return { ...rect, x, y, w, h }
}

/** Arrow-key nudge for keyboard users: 8 world units, 1 with Alt. */
export function nudged(rect: Rect, key: string, fine = false): Rect | null {
  const step = fine ? 1 : 8
  const delta: Record<string, Point> = { ArrowLeft: { x: -step, y: 0 }, ArrowRight: { x: step, y: 0 }, ArrowUp: { x: 0, y: -step }, ArrowDown: { x: 0, y: step } }
  const d = delta[key]
  return d ? moved(rect, d.x, d.y, 1) : null
}

/** Where a newly placed block goes: below everything already on the canvas, left-aligned. */
export function nextPlacement(rects: readonly Rect[], w = 360, h = 120, gap = 32): Rect {
  const all = bounds(rects)
  return all ? { x: all.x, y: all.y + all.h + gap, w, h } : { x: 0, y: 0, w, h }
}
