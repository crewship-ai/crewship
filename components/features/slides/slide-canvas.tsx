"use client"

import * as React from "react"
import type { Slide, SlideElement } from "@/lib/slides/model"
import { isNativeSlideDeck } from "./native-model"

function ElementContent({ element }: { element: SlideElement }) {
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
  selectedElementId?: string
  onSelectElement?: (elementId: string) => void
  onOverflowChange?: (elementIds: string[]) => void
}

/** One native typed frame. Text is React text; source cannot supply DOM or CSS. */
export function SlideCanvas({ deck, slideId, selectedElementId, onSelectElement, onOverflowChange }: SlideCanvasProps) {
  const root = React.useRef<SVGSVGElement>(null)
  const valid = isNativeSlideDeck(deck)
  const slide: Slide | undefined = valid ? deck.slides.find(value => value.id === slideId) : undefined
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
  return <svg ref={root} role="img" aria-label={`Slide ${deck.slide_order.indexOf(slideId) + 1}`} viewBox={`0 0 ${deck.width} ${deck.height}`} width="100%" style={{ display: "block", aspectRatio: `${deck.width} / ${deck.height}`, overflow: "hidden", background: deck.theme.background }}>
    {slide.elements.map(element => <g key={element.id} data-element-id={element.id} transform={`translate(${element.x} ${element.y})`} onClick={onSelectElement ? () => onSelectElement(element.id) : undefined}>
      <ElementContent element={element} />
      {selectedElementId === element.id && <rect pointerEvents="none" width={element.width} height={element.height} fill="none" stroke="#2563eb" strokeWidth={2} />}
    </g>)}
  </svg>
}
