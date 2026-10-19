"use client"

import * as React from "react"
import type { Slide, SlideElement } from "@/lib/slides/model"
import { clampDelta } from "./element-edit"
import type { Rect } from "./element-edit"
import { isNativeSlideDeck } from "./native-model"

function ElementContent({ element }: { element: SlideElement }) {
  // The whole-deck schema-1 gate excludes images before any source text renders.
  if (element.type === "image") throw new Error("Images are unavailable in this renderer")
  if (element.type === "shape") {
    const style = { fill: element.shape.fill, stroke: element.shape.stroke, strokeWidth: element.shape.stroke_width }
    return element.shape.kind === "ellipse"
      ? <ellipse cx={element.width / 2} cy={element.height / 2} rx={Math.max(0, (element.width - element.shape.stroke_width) / 2)} ry={Math.max(0, (element.height - element.shape.stroke_width) / 2)} {...style} />
      : <rect x={element.shape.stroke_width / 2} y={element.shape.stroke_width / 2} width={Math.max(0, element.width - element.shape.stroke_width)} height={Math.max(0, element.height - element.shape.stroke_width)} {...style} />
  }
  const style = element.type === "text" ? element.text : element.table
  return <foreignObject width={element.width} height={element.height}>
    <div data-slide-content={element.id} style={{ width: "100%", height: "100%", overflow: "hidden", fontFamily: style.font_family, fontSize: style.font_size, color: style.color, lineHeight: 1.2, boxSizing: "border-box" }}>
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
const handle = 14

/** One native typed frame. Text is React text; source cannot supply DOM or CSS. */
export function SlideCanvas({ deck, slideId, selectedIds = [], onSelectElement, onClearSelection, onMoveElements, onResizeElement, onOverflowChange }: SlideCanvasProps) {
  const root = React.useRef<SVGSVGElement>(null)
  const [drag, setDrag] = React.useState<Drag>()
  const [focused, setFocused] = React.useState<string>()
  const valid = isNativeSlideDeck(deck)
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
