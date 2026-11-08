"use client"

import * as React from "react"
import type * as Y from "yjs"
import { Maximize, Minus, Plus, StickyNote } from "lucide-react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { fitAll, focusRect, moved, nextPlacement, nudged, pan, resized, toWorld, wheelFactor, zoomAt, type Handle, type Rect, type Viewport } from "@/lib/documents/canvas-viewport"
import { documentSurface, type SurfaceItem, type SurfacePlacement, type SurfaceView } from "@/lib/documents/surface"

/**
 * The Canvas view of one Docs document (R71 rev 2 §0.4). It shows the same
 * Y.Doc as the Page: a card is a placement of a Page block, never a copy of
 * it. Pan, zoom, fit and focus are local; only a completed move, resize, add or
 * removal writes, once, as the person's own origin so their undo stays theirs.
 *
 * Read-only people can pan, zoom, fit, focus and select; they cannot move,
 * resize, add or remove anything. Pointer: background drag pans with any
 * button; on a card the primary button moves (Edit only) and the others pan.
 * Ctrl/⌘ + wheel or a pinch zooms around the pointer; a plain wheel pans.
 */
export interface DocsCanvasProps {
  doc: Y.Doc
  editable: boolean
  /** The person's own transaction origin (for example their session ID), tracked by their undo manager. */
  origin: string
  /** Open this block in the Page view (double-click or Enter on a card). */
  onOpenInPage?: (blockId: string) => void
}

type Drag =
  | { kind: "pan"; pointer: number; x: number; y: number }
  | { kind: "move" | "resize"; pointer: number; id: string; x: number; y: number; start: Rect; handle?: Handle }

interface BlockSummary { id: string; type: string; text: string; level?: number }

const NOTE_TONES: Record<string, string> = {
  default: "bg-card", yellow: "bg-warn/15", green: "bg-success/15", blue: "bg-primary/10", red: "bg-destructive/10", purple: "bg-accent",
}

function plain(node: Y.XmlElement | Y.XmlText | Y.XmlFragment): string {
  if ("toDelta" in node && typeof (node as Y.XmlText).toDelta === "function" && !("nodeName" in node)) {
    return (node as Y.XmlText).toDelta().map((part: { insert?: unknown }) => typeof part.insert === "string" ? part.insert : "").join("")
  }
  return (node as Y.XmlElement).toArray().map(child => plain(child as Y.XmlElement)).join(" ")
}

/** Top-level Page blocks with their plain text, in Page order. */
export function pageBlocks(doc: Y.Doc): BlockSummary[] {
  return doc.getXmlFragment("default").toArray().flatMap(node => {
    const element = node as Y.XmlElement
    const id = typeof element.getAttribute === "function" ? element.getAttribute("id") : undefined
    if (typeof id !== "string") return []
    const level = element.getAttribute("level") as unknown
    return [{ id, type: element.nodeName, text: plain(element).replace(/\s+/g, " ").trim(), ...(typeof level === "number" ? { level } : {}) }]
  })
}

function useSurface(doc: Y.Doc): { view: SurfaceView | null; blocks: BlockSummary[]; error: boolean } {
  const read = React.useCallback(() => {
    try { return { view: documentSurface.project(doc) as SurfaceView, blocks: pageBlocks(doc), error: false } }
    catch { return { view: null, blocks: pageBlocks(doc), error: true } }
  }, [doc])
  const [state, setState] = React.useState(read)
  React.useEffect(() => {
    setState(read())
    const update = () => setState(read())
    doc.on("update", update)
    return () => doc.off("update", update)
  }, [doc, read])
  return state
}

const HANDLES: Handle[] = ["e", "s", "se"]
const handleClass: Record<string, string> = {
  e: "right-[-5px] top-1/2 -translate-y-1/2 h-6 w-2.5 cursor-ew-resize",
  s: "bottom-[-5px] left-1/2 -translate-x-1/2 h-2.5 w-6 cursor-ns-resize",
  se: "bottom-[-5px] right-[-5px] size-3 cursor-nwse-resize",
}

export function DocsCanvas({ doc, editable, origin, onOpenInPage }: DocsCanvasProps) {
  const { view: surface, blocks, error } = useSurface(doc)
  const ref = React.useRef<HTMLDivElement>(null)
  const [viewport, setViewport] = React.useState<Viewport>({ x: -48, y: -48, scale: 1 })
  const [selected, setSelected] = React.useState<string | null>(null)
  const [preview, setPreview] = React.useState<{ id: string; rect: Rect } | null>(null)
  const [failure, setFailure] = React.useState<string | null>(null)
  const drag = React.useRef<Drag | null>(null)
  const pointers = React.useRef(new Map<number, { x: number; y: number }>())
  const pinch = React.useRef<number | null>(null)
  const fitted = React.useRef(false)

  const byId = React.useMemo(() => new Map(blocks.map(b => [b.id, b])), [blocks])
  const placements = React.useMemo(() => surface?.placements ?? [], [surface])
  const items = React.useMemo(() => surface?.items ?? [], [surface])
  const rects = React.useMemo(() => [...placements, ...items.filter(i => i.kind !== "connector")] as (SurfacePlacement | Exclude<SurfaceItem, { kind: "connector" }>)[], [placements, items])
  const unplaced = blocks.filter(b => !placements.some(p => p.id === b.id))

  const size = () => ({ width: ref.current?.clientWidth || 800, height: ref.current?.clientHeight || 600 })
  const local = (event: { clientX: number; clientY: number }) => {
    const box = ref.current?.getBoundingClientRect()
    return { x: event.clientX - (box?.left ?? 0), y: event.clientY - (box?.top ?? 0) }
  }
  const fit = React.useCallback(() => setViewport(fitAll(rects, size())), [rects])
  React.useEffect(() => { if (!fitted.current && surface) { fitted.current = true; setViewport(fitAll(rects, size())) } }, [surface, rects])

  const write = (action: () => void) => {
    if (!editable) return
    try { action(); setFailure(null) } catch (e) { setFailure(e instanceof Error ? e.message : "This change could not be made.") }
  }
  const rectOf = (id: string): Rect | null => {
    const found = rects.find(r => r.id === id)
    return found ? { x: found.x, y: found.y, w: found.w, h: found.h } : null
  }
  const commit = (id: string, rect: Rect) => write(() => documentSurface.move(doc, id, rect, origin))

  // Wheel: Ctrl/⌘ (and trackpad pinch, which browsers report with ctrlKey) zooms at the pointer; otherwise pan.
  React.useEffect(() => {
    const element = ref.current
    if (!element) return
    const onWheel = (event: WheelEvent) => {
      event.preventDefault()
      const box = element.getBoundingClientRect()
      const point = { x: event.clientX - box.left, y: event.clientY - box.top }
      setViewport(v => event.ctrlKey || event.metaKey ? zoomAt(v, point, wheelFactor(event.deltaY, event.deltaMode)) : pan(v, -event.deltaX, -event.deltaY))
    }
    element.addEventListener("wheel", onWheel, { passive: false })
    return () => element.removeEventListener("wheel", onWheel)
  }, [])

  const cancel = () => {
    drag.current = null; pinch.current = null; setPreview(null)
  }

  const onPointerDown = (event: React.PointerEvent, target?: { id: string; handle?: Handle }) => {
    pointers.current.set(event.pointerId, local(event))
    if (pointers.current.size === 2) {
      // Two touches: pinch zoom around their midpoint; any card drag is abandoned.
      const [a, b] = [...pointers.current.values()]
      pinch.current = Math.hypot(a.x - b.x, a.y - b.y); drag.current = null; setPreview(null)
      return
    }
    event.stopPropagation()
    ;(event.currentTarget as Element).setPointerCapture?.(event.pointerId)
    const start = target ? rectOf(target.id) : null
    if (target) setSelected(target.id)
    else setSelected(null)
    const primary = event.button === 0
    const movable = target && start && editable && primary && !(items.find(i => i.id === target.id)?.kind === "connector")
    drag.current = movable
      ? { kind: target.handle ? "resize" : "move", pointer: event.pointerId, id: target.id, x: event.clientX, y: event.clientY, start: start!, handle: target.handle }
      : { kind: "pan", pointer: event.pointerId, x: event.clientX, y: event.clientY }
  }

  const onPointerMove = (event: React.PointerEvent) => {
    if (pointers.current.has(event.pointerId)) pointers.current.set(event.pointerId, local(event))
    if (pinch.current !== null && pointers.current.size === 2) {
      const [a, b] = [...pointers.current.values()]
      const distance = Math.hypot(a.x - b.x, a.y - b.y)
      if (pinch.current > 0) { const ratio = distance / pinch.current; setViewport(v => zoomAt(v, { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }, ratio)) }
      pinch.current = distance
      return
    }
    const d = drag.current
    if (!d || d.pointer !== event.pointerId) return
    const dx = event.clientX - d.x, dy = event.clientY - d.y
    if (d.kind === "pan") { d.x = event.clientX; d.y = event.clientY; setViewport(v => pan(v, dx, dy)); return }
    setPreview({ id: d.id, rect: d.kind === "move" ? moved(d.start, dx, dy, viewport.scale) : resized(d.start, d.handle!, dx, dy, viewport.scale) })
  }

  const onPointerUp = (event: React.PointerEvent) => {
    pointers.current.delete(event.pointerId)
    if (pointers.current.size < 2) pinch.current = null
    const d = drag.current
    if (!d || d.pointer !== event.pointerId) return
    drag.current = null
    // One write per completed gesture: history and retained values stay bounded.
    if (d.kind !== "pan" && preview && preview.id === d.id) {
      const { x, y, w, h } = preview.rect, s = d.start
      if (x !== s.x || y !== s.y || w !== s.w || h !== s.h) commit(d.id, preview.rect)
    }
    setPreview(null)
  }

  const onPointerCancel = (event: React.PointerEvent) => { pointers.current.delete(event.pointerId); cancel() }

  const onKeyDown = (event: React.KeyboardEvent) => {
    if (event.key === "Escape") { if (drag.current) cancel(); else setSelected(null); return }
    const center = { x: size().width / 2, y: size().height / 2 }
    if (event.target === ref.current || !selected) {
      if (event.key === "+" || event.key === "=") { event.preventDefault(); setViewport(v => zoomAt(v, center, 1.2)) }
      else if (event.key === "-") { event.preventDefault(); setViewport(v => zoomAt(v, center, 1 / 1.2)) }
      else if (event.key === "0" || (event.key === "!" && event.shiftKey)) { event.preventDefault(); fit() }
      return
    }
    const rect = rectOf(selected)
    if (!rect) return
    if (event.key === "Enter") { event.preventDefault(); if (byId.has(selected)) onOpenInPage?.(selected); return }
    if (event.key === "f" || event.key === "F") { event.preventDefault(); setViewport(focusRect(rect, size())); return }
    if (!editable) return
    const next = nudged(rect, event.key, event.altKey)
    if (next) { event.preventDefault(); commit(selected, next); return }
    if (event.key === "Delete" || event.key === "Backspace") {
      event.preventDefault()
      // A placement is removed from the canvas only; the block stays in the Page.
      if (placements.some(p => p.id === selected)) write(() => documentSurface.unplace(doc, selected, origin))
      else write(() => documentSurface.removeItem(doc, selected, origin))
      setSelected(null)
    }
  }

  const topZ = () => Math.max(0, ...placements.map(p => p.z), ...items.map(i => i.z)) + 1
  const addBlock = (id: string) => write(() => {
    const at = nextPlacement(rects)
    documentSurface.place(doc, id, { ...at, z: topZ() }, origin)
    setSelected(id); setViewport(focusRect(at, size()))
  })
  const addNote = () => write(() => {
    const center = toWorld(viewport, { x: size().width / 2 - 100, y: size().height / 2 - 80 })
    const id = "note-" + crypto.randomUUID()
    documentSurface.putItem(doc, id, { kind: "note", x: Math.round(center.x), y: Math.round(center.y), w: 200, h: 160, z: topZ(), text: "", tone: "yellow" }, origin)
    setSelected(id)
  })

  if (error) return <div role="alert" className="m-6 rounded-md border border-destructive/40 p-4 text-sm">This canvas contains data this version cannot read. Nothing was changed; reload to update.</div>

  const shown = (r: { id: string; x: number; y: number; w: number; h: number }) => preview?.id === r.id ? { ...r, ...preview.rect } : r
  const centre = (id: string) => { const r = rects.find(x => x.id === id); if (!r) return null; const s = shown(r); return { x: s.x + s.w / 2, y: s.y + s.h / 2 } }
  const zoomBy = (factor: number) => setViewport(v => zoomAt(v, { x: size().width / 2, y: size().height / 2 }, factor))

  return <div className="relative h-full min-h-[480px] w-full overflow-hidden bg-muted/30" data-docs-canvas data-editable={editable ? "true" : "false"}>
    <div ref={ref} role="application" aria-roledescription="canvas" aria-label="Canvas" tabIndex={0}
      className="absolute inset-0 touch-none select-none outline-none focus-visible:ring-2 focus-visible:ring-ring"
      onPointerDown={e => onPointerDown(e)} onPointerMove={onPointerMove} onPointerUp={onPointerUp} onPointerCancel={onPointerCancel}
      onContextMenu={e => e.preventDefault()} onKeyDown={onKeyDown}>
      <div data-canvas-world className="absolute left-0 top-0 origin-top-left"
        style={{ transform: `scale(${viewport.scale}) translate(${-viewport.x}px, ${-viewport.y}px)` }}>
        <svg aria-hidden className="pointer-events-none absolute left-0 top-0 overflow-visible" width="1" height="1">
          {items.filter((i): i is Extract<SurfaceItem, { kind: "connector" }> => i.kind === "connector").map(c => {
            const a = centre(c.from.ref), b = centre(c.to.ref)
            if (!a || !b) return null
            return <line key={c.id} data-connector={c.id} x1={a.x} y1={a.y} x2={b.x} y2={b.y} className="stroke-muted-foreground" strokeWidth={2 / viewport.scale} />
          })}
        </svg>
        {placements.map(p => {
          const block = byId.get(p.id), r = shown(p)
          if (!block) return null
          return <div key={p.id} role="group" aria-label={block.text || "Empty block"} data-placement={p.id} data-selected={selected === p.id ? "true" : undefined}
            tabIndex={0} onFocus={() => setSelected(p.id)}
            onPointerDown={e => onPointerDown(e, { id: p.id })} onDoubleClick={() => onOpenInPage?.(p.id)}
            className={cn("absolute overflow-hidden rounded-md border bg-card p-3 text-sm shadow-sm outline-none", editable ? "cursor-grab" : "cursor-default",
              selected === p.id && "ring-2 ring-primary")}
            style={{ left: r.x, top: r.y, width: r.w, minHeight: r.h, zIndex: p.z }}>
            <p className={cn("whitespace-pre-wrap break-words", block.type === "heading" && "text-base font-semibold")}>{block.text || <span className="text-muted-foreground">Empty block</span>}</p>
            {editable && selected === p.id && HANDLES.map(h => <span key={h} aria-hidden data-handle={h}
              onPointerDown={e => onPointerDown(e, { id: p.id, handle: h })}
              className={cn("absolute rounded-sm border border-primary bg-background", handleClass[h])} />)}
          </div>
        })}
        {items.filter(i => i.kind !== "connector").map(i => {
          const item = i as Exclude<SurfaceItem, { kind: "connector" }>, r = shown(item)
          return <div key={item.id} role="group" aria-label={item.kind === "note" ? `Note: ${item.text || "empty"}` : `Shape: ${item.text ?? item.shape}`}
            data-item={item.id} tabIndex={0} onFocus={() => setSelected(item.id)} onPointerDown={e => onPointerDown(e, { id: item.id })}
            className={cn("absolute rounded-md border p-2 text-sm outline-none", NOTE_TONES[item.tone] ?? "bg-card", item.kind === "shape" && item.shape === "ellipse" && "rounded-full",
              selected === item.id && "ring-2 ring-primary")}
            style={{ left: r.x, top: r.y, width: r.w, height: r.h, zIndex: item.z }}>
            <p className="whitespace-pre-wrap break-words">{item.text}</p>
          </div>
        })}
      </div>
    </div>

    {placements.length === 0 && items.length === 0 && <p className="pointer-events-none absolute inset-x-0 top-1/3 text-center text-sm text-muted-foreground">
      {editable ? "Nothing is on the canvas yet. Add blocks from this document below." : "Nothing is on the canvas yet."}
    </p>}

    {editable && unplaced.length > 0 && <details className="absolute left-3 top-3 max-h-[50%] w-64 overflow-y-auto rounded-md border bg-background text-sm shadow-sm">
      <summary className="cursor-pointer px-3 py-2 font-medium coarse:min-h-[44px]">Add blocks ({unplaced.length})</summary>
      <ul className="grid gap-px p-1">
        {unplaced.map(b => <li key={b.id}><button type="button" onClick={() => addBlock(b.id)}
          className="w-full truncate rounded-sm px-2 py-1.5 text-left hover:bg-accent coarse:min-h-[44px]">{b.text || "Empty block"}</button></li>)}
      </ul>
    </details>}

    <div className="absolute bottom-3 right-3 flex items-center gap-1 rounded-md border bg-background p-1 shadow-sm">
      {editable && <Button type="button" size="sm" variant="ghost" onClick={addNote} aria-label="Add note"><StickyNote aria-hidden className="size-4" /></Button>}
      <Button type="button" size="sm" variant="ghost" onClick={() => zoomBy(1 / 1.2)} aria-label="Zoom out"><Minus aria-hidden className="size-4" /></Button>
      <span className="w-12 text-center font-mono text-xs tabular-nums" aria-live="polite" data-zoom>{Math.round(viewport.scale * 100)}%</span>
      <Button type="button" size="sm" variant="ghost" onClick={() => zoomBy(1.2)} aria-label="Zoom in"><Plus aria-hidden className="size-4" /></Button>
      <Button type="button" size="sm" variant="ghost" onClick={fit} aria-label="Fit to screen"><Maximize aria-hidden className="size-4" /></Button>
    </div>
    {failure && <p role="alert" className="absolute bottom-14 right-3 rounded-md border border-destructive/40 bg-background px-3 py-2 text-sm">{failure}</p>}
  </div>
}
