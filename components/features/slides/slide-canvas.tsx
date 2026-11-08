"use client"

import * as React from "react"
import type { Slide, SlideElement } from "@/lib/slides/model"
import { clampDelta } from "./element-edit"
import type { Rect } from "./element-edit"
import { arrowhead, gradientLine, slideIconNodes, SLIDES_FONT_STACK, SLIDES_ICON_STROKE } from "@/lib/slides/rich"
import { isRenderableSlideDeck } from "./native-model"
import "./slide-fonts.css"

const ICON_TAGS = new Set(["path", "circle", "ellipse", "line", "rect", "polyline", "polygon"])
const ICON_ATTRS = new Set(["d", "cx", "cy", "r", "rx", "ry", "x1", "y1", "x2", "y2", "x", "y", "width", "height", "points"])

/** Geometry from the committed icon set only; the deck supplies a name and a hex colour. */
function SlideIconGlyph({ element }: { element: Extract<SlideElement, { type: "icon" }> }) {
  const nodes = slideIconNodes(element.icon.name)
  if (!nodes) throw new Error("Unknown slide icon")
  return <svg width={element.width} height={element.height} viewBox="0 0 24 24" overflow="hidden" fill="none" stroke={element.icon.color} strokeWidth={SLIDES_ICON_STROKE} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    {nodes.map(([tag, attrs], index) => ICON_TAGS.has(tag) ? React.createElement(tag, { key: index, ...Object.fromEntries(Object.entries(attrs).filter(([name]) => ICON_ATTRS.has(name))) }) : null)}
  </svg>
}

function SlideShapeGlyph({ element }: { element: Extract<SlideElement, { type: "shape" }> }) {
  const gradientId = `slide-gradient-${React.useId().replace(/[^A-Za-z0-9_-]/g, "")}`
  const { shape } = element, sw = shape.stroke_width
  if (shape.kind === "line" && shape.line) {
    const l = shape.line
    let { x1, y1, x2, y2 } = l
    const heads: string[] = []
    if (l.end === "triangle") { const a = arrowhead(l.x1, l.y1, l.x2, l.y2, sw); heads.push(a.points); x2 = a.bx; y2 = a.by }
    if (l.start === "triangle") { const a = arrowhead(l.x2, l.y2, l.x1, l.y1, sw); heads.push(a.points); x1 = a.bx; y1 = a.by }
    return <svg width={element.width} height={element.height} overflow="hidden">
      {(x1 !== x2 || y1 !== y2) && <line x1={x1} y1={y1} x2={x2} y2={y2} stroke={shape.stroke} strokeWidth={sw} />}
      {heads.map(points => <polygon key={points} points={points} fill={shape.stroke} />)}
    </svg>
  }
  const g = shape.gradient
  const fill = g ? `url(#${gradientId})` : shape.fill
  const defs = g && <defs><linearGradient id={gradientId} gradientUnits="userSpaceOnUse" {...gradientLine(g.angle, element.width, element.height)}>
    {g.stops.map((stop, index) => <stop key={index} offset={`${stop.position}%`} stopColor={stop.color} />)}
  </linearGradient></defs>
  const stroke = { stroke: shape.stroke, strokeWidth: sw }
  if (shape.kind === "ellipse") return <>{defs}
    {g && <ellipse cx={element.width / 2} cy={element.height / 2} rx={element.width / 2} ry={element.height / 2} fill={fill} />}
    <ellipse cx={element.width / 2} cy={element.height / 2} rx={Math.max(0, (element.width - sw) / 2)} ry={Math.max(0, (element.height - sw) / 2)} fill={g ? "none" : fill} {...stroke} />
  </>
  const radius = shape.corner_radius ?? 0
  return <>{defs}
    {g && <rect width={element.width} height={element.height} rx={radius || undefined} fill={fill} />}
    <rect x={sw / 2} y={sw / 2} width={Math.max(0, element.width - sw)} height={Math.max(0, element.height - sw)} rx={radius ? Math.max(0, radius - sw / 2) : undefined} fill={g ? "none" : fill} {...stroke} />
  </>
}

function ElementContent({ element }: { element: SlideElement }) {
  // The whole-deck schema gate excludes images before any source text renders.
  if (element.type === "image") throw new Error("Images are unavailable in this renderer")
  if (element.type === "icon") return <SlideIconGlyph element={element} />
  if (element.type === "shape") return <SlideShapeGlyph element={element} />
  const style = element.type === "text" ? element.text : element.table
  const typography = element.type === "text" ? element.text : undefined
  return <foreignObject width={element.width} height={element.height}>
    <div data-slide-content={element.id} style={{ width: "100%", height: "100%", overflow: "hidden", fontFamily: Object.hasOwn(SLIDES_FONT_STACK, style.font_family) ? SLIDES_FONT_STACK[style.font_family] : style.font_family, fontSize: style.font_size, fontWeight: typography?.font_weight, letterSpacing: typography?.letter_spacing ? `${typography.letter_spacing}px` : undefined, color: style.color, lineHeight: (typography?.line_height ?? 120) / 100, boxSizing: "border-box" }}>
      {element.type === "text"
        ? <div style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere", textAlign: element.text.align }}>{element.text.value}</div>
        : <table style={{ width: "100%", tableLayout: "fixed", borderCollapse: "collapse", background: element.table.fill }}><tbody>{element.table.cells.map((row, rowIndex) => <tr key={rowIndex}>{row.map((cell, column) => <td key={column} style={{ border: `1px solid ${element.table.border}`, padding: 4, verticalAlign: "top", whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{cell}</td>)}</tr>)}</tbody></table>}
    </div>
  </foreignObject>
}

export interface SlideCanvasProps {
  deck: unknown
  slideId: string
  selectedIds?: readonly string[]
  /** Present only while editing; without it the canvas is a read-only picture. */
  onSelectElement?: (elementId: string, additive: boolean) => void
  onClearSelection?: () => void
  /** One call per finished drag, in deck units, already clamped to the frame. */
  onMoveElements?: (dx: number, dy: number) => void
  onResizeElement?: (elementId: string, rect: Rect) => void
  onOverflowChange?: (elementIds: string[]) => void
}

type Drag = { kind: "move" | "resize"; id: string; startX: number; startY: number; dx: number; dy: number }
// Handle edge in screen pixels (44 on coarse pointers); converted to deck units per render.
const coarse = () => typeof window !== "undefined" && Boolean(window.matchMedia?.("(pointer: coarse)").matches)

/** One native typed frame. Text is React text; source cannot supply DOM or CSS. */
export function SlideCanvas({ deck, slideId, selectedIds = [], onSelectElement, onClearSelection, onMoveElements, onResizeElement, onOverflowChange }: SlideCanvasProps) {
  const root = React.useRef<SVGSVGElement>(null)
  const [drag, setDrag] = React.useState<Drag>()
  const [focused, setFocused] = React.useState<string>()
  const valid = isRenderableSlideDeck(deck)
  const slide: Slide | undefined = valid ? deck.slides.find(value => value.id === slideId) : undefined
  const editable = Boolean(onSelectElement)
  React.useLayoutEffect(() => {
    let active = true
    const check = () => {
      if (!active) return
      const overflowing = Array.from(root.current?.querySelectorAll<HTMLElement>("[data-slide-content]") ?? []).filter(node => node.scrollHeight > node.clientHeight + 1 || node.scrollWidth > node.clientWidth + 1).map(node => node.dataset.slideContent!)
      onOverflowChange?.(overflowing)
    }
    check()
    const observer = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(check)
    for (const node of root.current?.querySelectorAll("[data-slide-content]") ?? []) observer?.observe(node)
    void document.fonts?.ready.then(check)
    return () => { active = false; observer?.disconnect() }
  }, [deck, slideId, onOverflowChange])
  if (!valid || !slide) return <p role="alert">This slide format or target is unavailable.</p>
  const selected = slide.elements.filter(element => selectedIds.includes(element.id))
  // Pointer pixels to deck units; a zero-size box (no layout) falls back to 1:1.
  const scale = () => { const width = root.current?.getBoundingClientRect().width; return width ? deck.width / width : 1 }
  const start = (kind: Drag["kind"], id: string, event: React.PointerEvent) => {
    event.stopPropagation()
    try { (event.currentTarget as Element).setPointerCapture?.(event.pointerId) } catch { /* synthetic events have no active pointer */ }
    setDrag({ kind, id, startX: event.clientX, startY: event.clientY, dx: 0, dy: 0 })
  }
  const track = (event: React.PointerEvent) => {
    if (!drag) return
    const k = scale(), dx = (event.clientX - drag.startX) * k, dy = (event.clientY - drag.startY) * k
    setDrag({ ...drag, ...(drag.kind === "move" ? clampDelta(deck, selected, dx, dy) : { dx: Math.round(dx), dy: Math.round(dy) }) })
  }
  const finish = () => {
    if (!drag) return
    const done = drag; setDrag(undefined)
    if (!done.dx && !done.dy) return
    if (done.kind === "move") onMoveElements?.(done.dx, done.dy)
    else { const element = slide.elements.find(value => value.id === done.id); if (element) onResizeElement?.(done.id, { x: element.x, y: element.y, width: element.width + done.dx, height: element.height + done.dy }) }
  }
  const shown = (element: SlideElement) => {
    if (!drag) return element
    if (drag.kind === "move") return selectedIds.includes(element.id) ? { ...element, x: element.x + drag.dx, y: element.y + drag.dy } : element
    return element.id === drag.id ? { ...element, width: Math.max(1, element.width + drag.dx), height: Math.max(1, element.height + drag.dy) } : element
  }
  const handle = (coarse() ? 44 : 24) * scale()
  const only = selected.length === 1 ? shown(selected[0]) : undefined
  return <svg ref={root} role={editable ? "group" : "img"} aria-label={`Slide ${deck.slide_order.indexOf(slideId) + 1}`} viewBox={`0 0 ${deck.width} ${deck.height}`} width="100%" style={{ display: "block", aspectRatio: `${deck.width} / ${deck.height}`, overflow: "hidden", background: deck.theme.background, touchAction: editable ? "none" : undefined }}
    onPointerDown={editable ? event => { if (event.target === event.currentTarget) onClearSelection?.() } : undefined} onPointerMove={drag ? track : undefined} onPointerUp={drag ? finish : undefined} onPointerCancel={drag ? () => setDrag(undefined) : undefined}>
    {slide.elements.map((original, index) => {
      const element = shown(original), picked = selectedIds.includes(element.id)
      return <g key={element.id} data-element-id={element.id} transform={`translate(${element.x} ${element.y})`} style={editable ? { cursor: "move", outline: "none" } : undefined}
        {...(editable ? {
          tabIndex: 0, role: "button", "aria-pressed": picked, "aria-label": `${element.type} ${index + 1}`,
          onFocus: () => setFocused(element.id), onBlur: () => setFocused(value => value === element.id ? undefined : value),
          onPointerDown: (event: React.PointerEvent) => { onSelectElement!(element.id, event.shiftKey); if (!event.shiftKey && onMoveElements) start("move", element.id, event) },
          onKeyDown: (event: React.KeyboardEvent) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); onSelectElement!(element.id, event.shiftKey) } },
        } : {})}>
        <ElementContent element={element} />
        {picked && <rect pointerEvents="none" width={element.width} height={element.height} fill="none" stroke="#2563eb" strokeWidth={2} />}
        {focused === element.id && <rect data-focus-ring pointerEvents="none" x={-3} y={-3} width={element.width + 6} height={element.height + 6} fill="none" stroke="#f59e0b" strokeWidth={3} strokeDasharray="8 4" />}
      </g>
    })}
    {only && onResizeElement && <rect data-resize-handle={only.id} role="presentation" x={only.x + only.width - handle / 2} y={only.y + only.height - handle / 2} width={handle} height={handle} fill="#2563eb" stroke="#ffffff" strokeWidth={2} style={{ cursor: "nwse-resize" }} onPointerDown={event => start("resize", only.id, event)} />}
  </svg>
}
