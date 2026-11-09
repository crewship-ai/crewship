// The Design canvas viewport: where the canvas sits on screen and how big.
//
// A view is local to one person's browser. It is never written into the
// Design document, the history or awareness: panning and zooming must not
// change the work or anybody else's screen.
//
// Coordinates: a canvas point p is drawn at screen (p.x * scale + x,
// p.y * scale + y), relative to the viewport element's top-left corner.

export interface DesignView { scale: number; x: number; y: number }
export interface Point { x: number; y: number }
export interface Box { x: number; y: number; width: number; height: number }

export const MIN_SCALE = 0.05
export const MAX_SCALE = 8
/** Screen pixels a pointer may travel before a press becomes a drag (click vs drag). */
export const DRAG_THRESHOLD = 3

export const clampScale = (scale: number) => Math.min(MAX_SCALE, Math.max(MIN_SCALE, Number.isFinite(scale) ? scale : 1))

export const toCanvas = (view: DesignView, screen: Point): Point => ({ x: (screen.x - view.x) / view.scale, y: (screen.y - view.y) / view.scale })
export const toScreen = (view: DesignView, canvas: Point): Point => ({ x: canvas.x * view.scale + view.x, y: canvas.y * view.scale + view.y })

export const panBy = (view: DesignView, dx: number, dy: number): DesignView => ({ ...view, x: view.x + dx, y: view.y + dy })

/** Zooms to `scale` keeping the canvas point under `anchor` (screen) where it is. */
export function zoomAround(view: DesignView, anchor: Point, scale: number): DesignView {
 const next = clampScale(scale)
 const under = toCanvas(view, anchor)
 return { scale: next, x: anchor.x - under.x * next, y: anchor.y - under.y * next }
}

export interface FitOptions {
 /** Space taken by the frame's toolbars over the top of the viewport. */
 top: number
 /** Space kept free under the target. */
 bottom: number
 /** Horizontal padding on each side. */
 side: number
 /** Phone: fit the target's width and pin it to the left edge. */
 widthOnly?: boolean
 maxScale?: number
}

/** The view that shows `target` whole inside a viewport of `size`. */
export function fitView(target: Box, size: { width: number; height: number }, options: FitOptions): DesignView {
 const width = Math.max(1, target.width), height = Math.max(1, target.height)
 const room = { width: Math.max(1, size.width - options.side * 2), height: Math.max(1, size.height - options.top - options.bottom) }
 const scale = Math.min(options.maxScale ?? 2, clampScale(options.widthOnly ? room.width / width : Math.min(room.width / width, room.height / height)))
 const x = options.widthOnly ? options.side - target.x * scale : (size.width - width * scale) / 2 - target.x * scale
 return { scale, x, y: options.top - target.y * scale }
}

/** The view that brings `target` to the viewport's top-left (inside the padding) at the current scale. */
export function focusView(view: DesignView, target: Box, options: Pick<FitOptions, "top" | "side">): DesignView {
 return { scale: view.scale, x: options.side - target.x * view.scale, y: options.top - target.y * view.scale }
}

export interface WheelInput { deltaX: number; deltaY: number; deltaMode: number; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean }
export type WheelGesture = { kind: "zoom"; factor: number } | { kind: "pan"; dx: number; dy: number }

const LINE = 16
/**
 * A wheel event as a gesture. Ctrl or Cmd with the wheel, and a trackpad pinch
 * (which browsers report as a wheel with ctrlKey), zoom. Everything else, a
 * mouse wheel or a two-finger trackpad scroll, pans. Shift turns a vertical
 * wheel into a horizontal pan.
 */
export function wheelGesture(event: WheelInput, pageHeight = 800): WheelGesture {
 const unit = event.deltaMode === 1 ? LINE : event.deltaMode === 2 ? pageHeight : 1
 let dx = event.deltaX * unit, dy = event.deltaY * unit
 if (event.ctrlKey || event.metaKey) {
  // One mouse notch (~100 px) zooms by ~18 %; a pinch reports small deltas and zooms smoothly.
  const delta = Math.max(-20, Math.min(20, dy))
  return { kind: "zoom", factor: Math.exp(-delta * 0.01) }
 }
 if (event.shiftKey && dx === 0) { dx = dy; dy = 0 }
 return { kind: "pan", dx: -dx, dy: -dy }
}

export interface PressInput {
 button: number
 pointerType: string
 /** Space is held (Space + left drag pans). */
 space: boolean
 /** The press started on a layer, not on the empty canvas. */
 onNode: boolean
 /** This person may move that layer here (edit rights, not a phone review). */
 canMove: boolean
}
/**
 * What a press starts:
 * - pan: right or middle button anywhere, Space + left, one finger on the
 *   empty canvas, or a finger on a layer that cannot be moved;
 * - move: left button or a finger on a layer this person may move
 *   (selects it; it moves only after DRAG_THRESHOLD);
 * - select: left on a layer that cannot be moved (selects, nothing moves);
 * - deselect: left on the empty canvas;
 * - none: anything else (pen eraser, extra buttons).
 */
export type PressIntent = "pan" | "move" | "select" | "deselect" | "none"
export function pressIntent(press: PressInput): PressIntent {
 const touch = press.pointerType === "touch"
 if (press.button === 1 || press.button === 2) return "pan"
 if (press.button !== 0) return "none"
 if (press.space) return "pan"
 if (!press.onNode) return touch ? "pan" : "deselect"
 if (press.canMove) return "move"
 return touch ? "pan" : "select"
}

/** Two touch points as a pinch: their midpoint and distance. */
export function pinchOf(a: Point, b: Point): { center: Point; distance: number } {
 return { center: { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }, distance: Math.max(1, Math.hypot(a.x - b.x, a.y - b.y)) }
}

/** The view during a pinch that started at `start` with `from` and now is `now`: zoom by the distance ratio, follow the midpoint. */
export function pinchView(start: DesignView, from: { center: Point; distance: number }, now: { center: Point; distance: number }): DesignView {
 const zoomed = zoomAround(start, from.center, start.scale * (now.distance / from.distance))
 return panBy(zoomed, now.center.x - from.center.x, now.center.y - from.center.y)
}

/** True when a keyboard event belongs to a text field, so Space and shortcuts stay with it. */
export function typingTarget(target: EventTarget | null): boolean {
 if (!target || typeof (target as Element).closest !== "function") return false
 return Boolean((target as Element).closest("input, textarea, select, [contenteditable=''], [contenteditable='true']"))
}

/** A corner or edge handle of the selected layer. */
export type ResizeHandle = "nw" | "n" | "ne" | "e" | "se" | "s" | "sw" | "w"
export const RESIZE_HANDLES: readonly ResizeHandle[] = ["nw", "n", "ne", "e", "se", "s", "sw", "w"]
/** A layer's box in its screen's coordinates. */
export interface DesignBox { x: number; y: number; width: number; height: number }

/**
 * The box after dragging `handle` by (dx, dy) canvas units. The opposite edge
 * stays put; a box never gets smaller than 1 × 1 (a drag past the opposite
 * edge stops there). Values are rounded to whole units like a move.
 */
export function resizeBox(box: DesignBox, handle: ResizeHandle, dx: number, dy: number): DesignBox {
 let { x, y, width, height } = box
 const right = x + width, bottom = y + height
 if (handle.includes("e")) width = Math.max(1, Math.round(width + dx))
 if (handle.includes("s")) height = Math.max(1, Math.round(height + dy))
 if (handle.includes("w")) { x = Math.min(right - 1, Math.round(x + dx)); width = right - x }
 if (handle.includes("n")) { y = Math.min(bottom - 1, Math.round(y + dy)); height = bottom - y }
 return { x, y, width, height }
}
